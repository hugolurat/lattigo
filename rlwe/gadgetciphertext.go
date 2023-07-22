package rlwe

import (
	"bufio"
	"fmt"
	"io"

	"github.com/google/go-cmp/cmp"
	"github.com/tuneinsight/lattigo/v4/ring"
	"github.com/tuneinsight/lattigo/v4/rlwe/ringqp"
	"github.com/tuneinsight/lattigo/v4/utils"
	"github.com/tuneinsight/lattigo/v4/utils/buffer"
	"github.com/tuneinsight/lattigo/v4/utils/structs"
)

// GadgetCiphertext is a struct for storing an encrypted
// plaintext times the gadget power matrix.
type GadgetCiphertext struct {
	BaseTwoDecomposition int
	Value                structs.Matrix[vectorQP]
}

// NewGadgetCiphertext returns a new Ciphertext key with pre-allocated zero-value.
// Ciphertext is always in the NTT domain.
// A GadgetCiphertext is created by default at degree 1 with the the maximum levelQ and levelP and with no base 2 decomposition.
// Give the optional GadgetCiphertextParameters struct to create a GadgetCiphertext with at a specific degree, levelQ, levelP and/or base 2 decomposition.
func NewGadgetCiphertext(params GetRLWEParameters, Degree, LevelQ, LevelP, BaseTwoDecomposition int) *GadgetCiphertext {

	p := params.GetRLWEParameters()

	decompRNS := p.DecompRNS(LevelQ, LevelP)
	decompPw2 := p.DecompPw2(LevelQ, LevelP, BaseTwoDecomposition)

	m := make(structs.Matrix[vectorQP], decompRNS)
	for i := 0; i < decompRNS; i++ {
		m[i] = make([]vectorQP, decompPw2)
		for j := range m[i] {
			m[i][j] = newVectorQP(params, Degree+1, LevelQ, LevelP)
		}
	}

	return &GadgetCiphertext{BaseTwoDecomposition: BaseTwoDecomposition, Value: m}
}

// LevelQ returns the level of the modulus Q of the target Ciphertext.
func (ct GadgetCiphertext) LevelQ() int {
	return ct.Value[0][0][0].LevelQ()
}

// LevelP returns the level of the modulus P of the target Ciphertext.
func (ct GadgetCiphertext) LevelP() int {
	return ct.Value[0][0][0].LevelP()
}

// DecompRNS returns the number of element in the RNS decomposition basis.
func (ct GadgetCiphertext) DecompRNS() int {
	return len(ct.Value)
}

// DecompPw2 returns the number of element in the Power of two decomposition basis.
func (ct GadgetCiphertext) DecompPw2() int {
	return len(ct.Value[0])
}

// Equal checks two Ciphertexts for equality.
func (ct GadgetCiphertext) Equal(other *GadgetCiphertext) bool {
	return (ct.BaseTwoDecomposition == other.BaseTwoDecomposition) && cmp.Equal(ct.Value, other.Value)
}

// CopyNew creates a deep copy of the receiver Ciphertext and returns it.
func (ct GadgetCiphertext) CopyNew() (ctCopy *GadgetCiphertext) {
	return &GadgetCiphertext{BaseTwoDecomposition: ct.BaseTwoDecomposition, Value: *ct.Value.CopyNew()}
}

// BinarySize returns the serialized size of the object in bytes.
func (ct GadgetCiphertext) BinarySize() (dataLen int) {
	return 8 + ct.Value.BinarySize()
}

// WriteTo writes the object on an io.Writer. It implements the io.WriterTo
// interface, and will write exactly object.BinarySize() bytes on w.
//
// Unless w implements the buffer.Writer interface (see lattigo/utils/buffer/writer.go),
// it will be wrapped into a bufio.Writer. Since this requires allocations, it
// is preferable to pass a buffer.Writer directly:
//
//   - When writing multiple times to a io.Writer, it is preferable to first wrap the
//     io.Writer in a pre-allocated bufio.Writer.
//   - When writing to a pre-allocated var b []byte, it is preferable to pass
//     buffer.NewBuffer(b) as w (see lattigo/utils/buffer/buffer.go).
func (ct GadgetCiphertext) WriteTo(w io.Writer) (n int64, err error) {

	switch w := w.(type) {
	case buffer.Writer:

		var inc int64

		if inc, err = buffer.WriteInt(w, ct.BaseTwoDecomposition); err != nil {
			return n + inc, err
		}

		n += inc

		inc, err = ct.Value.WriteTo(w)

		return n + inc, err

	default:
		return ct.WriteTo(bufio.NewWriter(w))
	}
}

// ReadFrom reads on the object from an io.Writer. It implements the
// io.ReaderFrom interface.
//
// Unless r implements the buffer.Reader interface (see see lattigo/utils/buffer/reader.go),
// it will be wrapped into a bufio.Reader. Since this requires allocation, it
// is preferable to pass a buffer.Reader directly:
//
//   - When reading multiple values from a io.Reader, it is preferable to first
//     first wrap io.Reader in a pre-allocated bufio.Reader.
//   - When reading from a var b []byte, it is preferable to pass a buffer.NewBuffer(b)
//     as w (see lattigo/utils/buffer/buffer.go).
func (ct *GadgetCiphertext) ReadFrom(r io.Reader) (n int64, err error) {
	switch r := r.(type) {
	case buffer.Reader:

		var inc int64

		if inc, err = buffer.ReadInt(r, &ct.BaseTwoDecomposition); err != nil {
			return n + inc, err
		}

		n += inc

		inc, err = ct.Value.ReadFrom(r)

		return n + inc, err

	default:
		return ct.ReadFrom(bufio.NewReader(r))
	}
}

// MarshalBinary encodes the object into a binary form on a newly allocated slice of bytes.
func (ct GadgetCiphertext) MarshalBinary() (data []byte, err error) {
	buf := buffer.NewBufferSize(ct.BinarySize())
	_, err = ct.WriteTo(buf)
	return buf.Bytes(), err
}

// UnmarshalBinary decodes a slice of bytes generated by
// MarshalBinary or WriteTo on the object.
func (ct *GadgetCiphertext) UnmarshalBinary(p []byte) (err error) {
	_, err = ct.ReadFrom(buffer.NewBuffer(p))
	return
}

// AddPolyTimesGadgetVectorToGadgetCiphertext takes a plaintext polynomial and a list of Ciphertexts and adds the
// plaintext times the RNS and BIT decomposition to the i-th element of the i-th Ciphertexts. This method return
// an error if len(cts) > 2.
func AddPolyTimesGadgetVectorToGadgetCiphertext(pt ring.Poly, cts []GadgetCiphertext, ringQP ringqp.Ring, buff ring.Poly) (err error) {

	levelQ := cts[0].LevelQ()
	levelP := cts[0].LevelP()

	ringQ := ringQP.RingQ.AtLevel(levelQ)

	if len(cts) > 2 {
		return fmt.Errorf("cannot AddPolyTimesGadgetVectorToGadgetCiphertext: len(cts) should be <= 2")
	}

	if levelP != -1 {
		ringQ.MulScalarBigint(pt, ringQP.RingP.AtLevel(levelP).Modulus(), buff) // P * pt
	} else {
		levelP = 0
		if !utils.Alias1D(pt.Buff, buff.Buff) {
			ring.CopyLvl(levelQ, pt, buff) // 1 * pt
		}
	}

	RNSDecomp := len(cts[0].Value)
	BITDecomp := len(cts[0].Value[0])
	N := ringQ.N()

	var index int
	for j := 0; j < BITDecomp; j++ {
		for i := 0; i < RNSDecomp; i++ {

			// e + (m * P * w^2j) * (q_star * q_tild) mod QP
			//
			// q_prod = prod(q[i*#Pi+j])
			// q_star = Q/qprod
			// q_tild = q_star^-1 mod q_prod
			//
			// Therefore : (pt * P * w^2j) * (q_star * q_tild) = pt*P*w^2j mod q[i*#Pi+j], else 0
			for k := 0; k < levelP+1; k++ {

				index = i*(levelP+1) + k

				// Handle cases where #pj does not divide #qi
				if index >= levelQ+1 {
					break
				}

				qi := ringQ.SubRings[index].Modulus
				p0tmp := buff.Coeffs[index]

				for u, ct := range cts {
					p1tmp := ct.Value[i][j][u].Q.Coeffs[index]
					for w := 0; w < N; w++ {
						p1tmp[w] = ring.CRed(p1tmp[w]+p0tmp[w], qi)
					}
				}

			}
		}

		// w^2j
		ringQ.MulScalar(buff, 1<<cts[0].BaseTwoDecomposition, buff)
	}

	return
}

// GadgetPlaintext stores a plaintext value times the gadget vector.
type GadgetPlaintext struct {
	Value structs.Vector[ring.Poly]
}

// NewGadgetPlaintext creates a new gadget plaintext from value, which can be either uint64, int64 or *ring.Poly.
// Plaintext is returned in the NTT and Mongtomery domain.
func NewGadgetPlaintext(params Parameters, value interface{}, levelQ, levelP, baseTwoDecomposition int) (pt *GadgetPlaintext, err error) {

	ringQ := params.RingQP().RingQ.AtLevel(levelQ)

	decompPw2 := params.DecompPw2(levelQ, levelP, baseTwoDecomposition)

	pt = new(GadgetPlaintext)
	pt.Value = make([]ring.Poly, decompPw2)

	switch el := value.(type) {
	case uint64:
		pt.Value[0] = ringQ.NewPoly()
		for i := 0; i < levelQ+1; i++ {
			pt.Value[0].Coeffs[i][0] = el
		}
	case int64:
		pt.Value[0] = ringQ.NewPoly()
		if el < 0 {
			for i := 0; i < levelQ+1; i++ {
				pt.Value[0].Coeffs[i][0] = ringQ.SubRings[i].Modulus - uint64(-el)
			}
		} else {
			for i := 0; i < levelQ+1; i++ {
				pt.Value[0].Coeffs[i][0] = uint64(el)
			}
		}
	case ring.Poly:
		pt.Value[0] = *el.CopyNew()
	default:
		return nil, fmt.Errorf("cannot NewGadgetPlaintext: unsupported type, must be either int64, uint64 or ring.Poly but is %T", el)
	}

	if levelP > -1 {
		ringQ.MulScalarBigint(pt.Value[0], params.RingP().AtLevel(levelP).Modulus(), pt.Value[0])
	}

	ringQ.NTT(pt.Value[0], pt.Value[0])
	ringQ.MForm(pt.Value[0], pt.Value[0])

	for i := 1; i < len(pt.Value); i++ {

		pt.Value[i] = *pt.Value[0].CopyNew()

		for j := 0; j < i; j++ {
			ringQ.MulScalar(pt.Value[i], 1<<baseTwoDecomposition, pt.Value[i])
		}
	}

	return
}
