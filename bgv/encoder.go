package bgv

import (
	"fmt"
	"math/big"

	"github.com/tuneinsight/lattigo/v4/ring"
	"github.com/tuneinsight/lattigo/v4/rlwe"
	"github.com/tuneinsight/lattigo/v4/rlwe/ringqp"
	"github.com/tuneinsight/lattigo/v4/utils"
	"github.com/tuneinsight/lattigo/v4/utils/bignum"
)

// GaloisGen is an integer of order N=2^d modulo M=2N and that spans Z_M with the integer -1.
// The j-th ring automorphism takes the root zeta to zeta^(5j).
const GaloisGen uint64 = ring.GaloisGen

// Encoder is a structure that stores the parameters to encode values on a plaintext in a SIMD (Single-Instruction Multiple-Data) fashion.
type Encoder struct {
	parameters Parameters

	indexMatrix []uint64

	bufQ ring.Poly
	bufT ring.Poly
	bufB []*big.Int

	paramsQP []ring.ModUpConstants
	qHalf    []*big.Int

	tInvModQ []*big.Int
}

// NewEncoder creates a new Encoder from the provided parameters.
func NewEncoder(parameters Parameters) *Encoder {

	ringQ := parameters.RingQ()
	ringT := parameters.RingT()

	paramsQP := make([]ring.ModUpConstants, ringQ.ModuliChainLength())

	qHalf := make([]*big.Int, ringQ.ModuliChainLength())

	moduli := ringQ.ModuliChain()
	T := ringT.SubRings[0].Modulus

	for i := 1; i < ringQ.ModuliChainLength(); i++ {
		paramsQP[i] = ring.GenModUpConstants(moduli[:i+1], []uint64{T})
		qHalf[i] = new(big.Int).Set(ringQ.ModulusAtLevel[i])
		qHalf[i].Rsh(qHalf[i], 1)
	}

	tInvModQ := make([]*big.Int, ringQ.ModuliChainLength())
	for i := range moduli {
		tInvModQ[i] = bignum.NewInt(T)
		tInvModQ[i].ModInverse(tInvModQ[i], ringQ.ModulusAtLevel[i])
	}

	var bufB []*big.Int

	if parameters.PlaintextLogDimensions().Cols < parameters.LogN()-1 {

		slots := parameters.PlaintextSlots()

		bufB = make([]*big.Int, slots)

		for i := 0; i < slots; i++ {
			bufB[i] = new(big.Int)
		}
	}

	return &Encoder{
		parameters:  parameters,
		indexMatrix: permuteMatrix(parameters.PlaintextLogSlots()),
		bufQ:        ringQ.NewPoly(),
		bufT:        ringT.NewPoly(),
		bufB:        bufB,
		paramsQP:    paramsQP,
		qHalf:       qHalf,
		tInvModQ:    tInvModQ,
	}
}

func permuteMatrix(logN int) (perm []uint64) {

	var N, pow, pos uint64 = uint64(1 << logN), 1, 0

	mask := 2*N - 1

	perm = make([]uint64, N)

	halfN := int(N >> 1)

	for i, j := 0, halfN; i < halfN; i, j = i+1, j+1 {

		pos = utils.BitReverse64(pow>>1, logN) // = (pow-1)/2

		perm[i] = pos
		perm[j] = N - pos - 1

		pow *= GaloisGen
		pow &= mask
	}

	return perm
}

func (ecd Encoder) GetRLWEParameters() *rlwe.Parameters {
	return &ecd.parameters.Parameters
}

// Encode encodes a slice of integers of type []uint64 or []int64 on a pre-allocated plaintext.
//
// inputs:
//   - values: a slice of []uint64 or []int64 of size at most the cyclotomic order of the plaintext modulus (smallest value for N satisfying T = 1 mod 2N)
//   - pt: an *rlwe.Plaintext
func (ecd Encoder) Encode(values interface{}, pt *rlwe.Plaintext) (err error) {

	if pt.IsBatched {
		return ecd.Embed(values, true, pt.MetaData, pt.Value)
	} else {

		ringT := ecd.parameters.RingT()
		N := ringT.N()
		T := ringT.SubRings[0].Modulus
		BRC := ringT.SubRings[0].BRedConstant

		ptT := ecd.bufT.Coeffs[0]

		var valLen int
		switch values := values.(type) {
		case []uint64:

			if len(values) > N {
				return fmt.Errorf("cannot Encode (TimeDomain): len(values)=%d > N=%d", len(values), N)
			}

			copy(ptT, values)
			valLen = len(values)
		case []int64:

			if len(values) > N {
				return fmt.Errorf("cannot Encode (TimeDomain: len(values)=%d > N=%d", len(values), N)
			}

			var sign, abs uint64
			for i, c := range values {
				sign = uint64(c) >> 63
				abs = ring.BRedAdd(uint64(c*((int64(sign)^1)-int64(sign))), T, BRC)
				ptT[i] = sign*(T-abs) | (sign^1)*abs
			}

			valLen = len(values)
		}

		for i := valLen; i < N; i++ {
			ptT[i] = 0
		}

		ringT.MulScalar(ecd.bufT, pt.Scale.Uint64(), ecd.bufT)
		ecd.RingT2Q(pt.Level(), true, ecd.bufT, pt.Value)

		if pt.IsNTT {
			ecd.parameters.RingQ().AtLevel(pt.Level()).NTT(pt.Value, pt.Value)
		}

		return
	}
}

// EncodeRingT encodes a slice of []uint64 or []int64 at the given scale on a polynomial pT with coefficients modulo the plaintext modulus T.
//
// inputs:
//   - values: a slice of []uint64 or []int64 of size at most the cyclotomic order of T (smallest value for N satisfying T = 1 mod 2N)
//   - plaintextScale: the scaling factor by which the values are multiplied before being encoded
//   - pT: a polynomial with coefficients modulo T
func (ecd Encoder) EncodeRingT(values interface{}, plaintextScale rlwe.Scale, pT ring.Poly) (err error) {
	perm := ecd.indexMatrix

	pt := pT.Coeffs[0]

	ringT := ecd.parameters.RingT()

	slots := pT.N()

	var valLen int
	switch values := values.(type) {
	case []uint64:

		if len(values) > slots {
			return fmt.Errorf("cannot EncodeRingT (FrequencyDomain): len(values)=%d > slots=%d", len(values), slots)
		}

		for i, c := range values {
			pt[perm[i]] = c
		}

		ringT.Reduce(pT, pT)

		valLen = len(values)

	case []int64:

		if len(values) > slots {
			return fmt.Errorf("cannot EncodeRingT (FrequencyDomain): len(values)=%d > slots=%d", len(values), slots)
		}

		T := ringT.SubRings[0].Modulus
		BRC := ringT.SubRings[0].BRedConstant

		var sign, abs uint64
		for i, c := range values {
			sign = uint64(c) >> 63
			abs = ring.BRedAdd(uint64(c*((int64(sign)^1)-int64(sign))), T, BRC)
			pt[perm[i]] = sign*(T-abs) | (sign^1)*abs
		}

		valLen = len(values)
	default:
		return fmt.Errorf("cannot EncodeRingT: values.(type) must be either []uint64 or []int64 but is %T", values)
	}

	// Zeroes the non-mapped coefficients
	N := len(ecd.indexMatrix)
	for i := valLen; i < N; i++ {
		pt[perm[i]] = 0
	}

	// INTT on the Y = X^{N/n}
	ringT.INTT(pT, pT)
	ringT.MulScalar(pT, plaintextScale.Uint64(), pT)

	return nil
}

// Embed is a generic method to encode slices of []uint64 or []int64 on ringqp.Poly or *ring.Poly.
// inputs:
//   - values: a slice of []uint64 or []int64 of size at most the cyclotomic order of T (smallest value for N satisfying T = 1 mod 2N)
//   - scaleUp: a boolean indicating if the values need to be multiplied by T^{-1} mod Q after being encoded on the polynomial
//   - metadata: a metadata struct containing the fields Scale, IsNTT and IsMontgomery
//   - polyOut: a ringqp.Poly or *ring.Poly
func (ecd Encoder) Embed(values interface{}, scaleUp bool, metadata *rlwe.MetaData, polyOut interface{}) (err error) {

	pT := ecd.bufT

	if err = ecd.EncodeRingT(values, metadata.Scale, pT); err != nil {
		return
	}

	// Maps Y = X^{N/n} -> X and quantizes.
	switch p := polyOut.(type) {
	case ringqp.Poly:

		levelQ := p.Q.Level()

		ecd.RingT2Q(levelQ, scaleUp, pT, p.Q)

		ringQ := ecd.parameters.RingQ().AtLevel(levelQ)

		if metadata.IsNTT {
			ringQ.NTT(p.Q, p.Q)
		}

		if metadata.IsMontgomery {
			ringQ.MForm(p.Q, p.Q)
		}

		if p.P.Level() > -1 {

			levelP := p.P.Level()

			ecd.RingT2Q(levelP, scaleUp, pT, p.P)

			ringP := ecd.parameters.RingP().AtLevel(levelP)

			if metadata.IsNTT {
				ringP.NTT(p.P, p.P)
			}

			if metadata.IsMontgomery {
				ringP.MForm(p.P, p.P)
			}
		}

	case ring.Poly:

		level := p.Level()

		ecd.RingT2Q(level, scaleUp, pT, p)

		ringQ := ecd.parameters.RingQ().AtLevel(level)

		if metadata.IsNTT {
			ringQ.NTT(p, p)
		}

		if metadata.IsMontgomery {
			ringQ.MForm(p, p)
		}

	default:
		return fmt.Errorf("cannot embed: invalid polyOut.(Type) must be ringqp.Poly or *ring.Poly")
	}

	return
}

// DecodeRingT decodes a polynomial pT with coefficients modulo the plaintext modulu T on a slice of []uint64 or []int64 at the given scale.
//
// inputs:
//   - pT: a polynomial with coefficients modulo T
//   - scale: the scaling factor by which the coefficients of pT will be divided by
//   - values: a slice of []uint64 or []int of size at most the degree of pT
func (ecd Encoder) DecodeRingT(pT ring.Poly, scale rlwe.Scale, values interface{}) (err error) {
	ringT := ecd.parameters.RingT()
	ringT.MulScalar(pT, ring.ModExp(scale.Uint64(), ringT.SubRings[0].Modulus-2, ringT.SubRings[0].Modulus), ecd.bufT)
	ringT.NTT(ecd.bufT, ecd.bufT)

	tmp := ecd.bufT.Coeffs[0]

	N := ringT.N()

	switch values := values.(type) {
	case []uint64:
		for i := 0; i < N; i++ {
			values[i] = tmp[ecd.indexMatrix[i]]
		}
	case []int64:
		modulus := int64(ecd.parameters.T())
		modulusHalf := modulus >> 1
		var value int64
		for i := 0; i < N; i++ {
			if value = int64(tmp[ecd.indexMatrix[i]]); value >= modulusHalf {
				values[i] = value - modulus
			} else {
				values[i] = value
			}
		}
	default:
		return fmt.Errorf("cannot DecodeRingT: values must be either []uint64 or []int64 but is %T", values)
	}

	return
}

// RingT2Q takes pT in base T and returns it in base Q on pQ.
// inputs:
//   - level: the level of the polynomial pQ
//   - scaleUp: a boolean indicating of the polynomial pQ must be multiplied by T^{-1} mod Q
//   - pT: a polynomial with coefficients modulo T
//   - pQ: a polynomial with coefficients modulo Q
func (ecd Encoder) RingT2Q(level int, scaleUp bool, pT, pQ ring.Poly) {

	N := pQ.N()
	n := pT.N()

	gap := N / n

	for i := 0; i < level+1; i++ {

		coeffs := pQ.Coeffs[i]

		copy(coeffs, pT.Coeffs[0])

		if gap > 1 {

			for j := n; j < N; j++ {
				coeffs[j] = 0
			}

			for j := n - 1; j > 0; j-- {
				coeffs[j*gap] = coeffs[j]
				coeffs[j] = 0
			}
		}
	}

	if scaleUp {
		ecd.parameters.RingQ().AtLevel(level).MulScalarBigint(pQ, ecd.tInvModQ[level], pQ)
	}
}

// RingQ2T takes pQ in base Q and returns it in base T (centered) on pT.
// inputs:
//   - level: the level of the polynomial pQ
//   - scaleDown: a boolean indicating of the polynomial pQ must be multiplied by T mod Q
//   - pQ: a polynomial with coefficients modulo Q
//   - pT: a polynomial with coefficients modulo T
func (ecd Encoder) RingQ2T(level int, scaleDown bool, pQ, pT ring.Poly) {

	ringQ := ecd.parameters.RingQ().AtLevel(level)
	ringT := ecd.parameters.RingT()

	var poly ring.Poly
	if scaleDown {
		ringQ.MulScalar(pQ, ecd.parameters.T(), ecd.bufQ)
		poly = ecd.bufQ
	} else {
		poly = pQ
	}

	gap := pQ.N() / pT.N()

	if level > 0 {

		if gap == 1 {
			ringQ.AddScalarBigint(poly, ecd.qHalf[level], ecd.bufQ)
			ring.ModUpExact(ecd.bufQ.Coeffs[:level+1], pT.Coeffs, ringQ, ringT, ecd.paramsQP[level])
			ringT.SubScalarBigint(pT, ecd.qHalf[level], pT)
		} else {
			ringQ.PolyToBigintCentered(poly, gap, ecd.bufB)
			ringT.SetCoefficientsBigint(ecd.bufB, pT)
		}

	} else {

		if gap == 1 {
			ringQ.AddScalar(poly, ringQ.SubRings[0].Modulus>>1, ecd.bufQ)
			ringT.Reduce(ecd.bufQ, pT)
		} else {

			n := pT.N()

			pQCoeffs := poly.Coeffs[0]
			bufQCoeffs := ecd.bufQ.Coeffs[0]

			for i := 0; i < n; i++ {
				bufQCoeffs[i] = pQCoeffs[i*gap]
			}

			ringQ.SubRings[0].AddScalar(bufQCoeffs[:n], ringQ.SubRings[0].Modulus>>1, bufQCoeffs[:n])
			ringT.SubRings[0].Reduce(bufQCoeffs[:n], pT.Coeffs[0])
		}

		ringT.SubScalar(pT, ring.BRedAdd(ringQ.SubRings[0].Modulus>>1, ringT.SubRings[0].Modulus, ringT.SubRings[0].BRedConstant), pT)
	}
}

// Decode decodes a plaintext on a slice of []uint64 or []int64 mod T of size at most N, where N is the smallest value satisfying T = 1 mod 2N.
func (ecd Encoder) Decode(pt *rlwe.Plaintext, values interface{}) (err error) {

	if pt.IsNTT {
		ecd.parameters.RingQ().AtLevel(pt.Level()).INTT(pt.Value, ecd.bufQ)
	}

	bufT := ecd.bufT

	ecd.RingQ2T(pt.Level(), true, ecd.bufQ, bufT)

	if pt.IsBatched {
		return ecd.DecodeRingT(ecd.bufT, pt.Scale, values)
	} else {
		ringT := ecd.parameters.RingT()
		ringT.MulScalar(bufT, ring.ModExp(pt.Scale.Uint64(), ringT.SubRings[0].Modulus-2, ringT.SubRings[0].Modulus), bufT)

		switch values := values.(type) {
		case []uint64:
			copy(values, ecd.bufT.Coeffs[0])
		case []int64:

			ptT := bufT.Coeffs[0]

			N := ecd.parameters.RingT().N()
			modulus := int64(ecd.parameters.T())
			modulusHalf := modulus >> 1

			var value int64
			for i := 0; i < N; i++ {
				if value = int64(ptT[i]); value >= modulusHalf {
					values[i] = value - modulus
				} else {
					values[i] = value
				}
			}

		default:
			return fmt.Errorf("cannot Decode: values must be either []uint64 or []int64 but is %T", values)
		}

		return
	}
}

// ShallowCopy creates a shallow copy of Encoder in which all the read-only data-structures are
// shared with the receiver and the temporary buffers are reallocated. The receiver and the returned
// Encoder can be used concurrently.
func (ecd Encoder) ShallowCopy() *Encoder {
	return &Encoder{
		parameters:  ecd.parameters,
		indexMatrix: ecd.indexMatrix,
		bufQ:        ecd.parameters.RingQ().NewPoly(),
		bufT:        ecd.parameters.RingT().NewPoly(),
		paramsQP:    ecd.paramsQP,
		qHalf:       ecd.qHalf,
		tInvModQ:    ecd.tInvModQ,
	}
}

type encoder[T int64 | uint64, U ring.Poly | ringqp.Poly | *rlwe.Plaintext] struct {
	*Encoder
}

func (e encoder[T, U]) Encode(values []T, metadata *rlwe.MetaData, output U) (err error) {
	return e.Embed(values, false, metadata, output)
}
