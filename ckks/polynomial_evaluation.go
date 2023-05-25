package ckks

import (
	"fmt"
	"math/big"
	"math/bits"
	"runtime"

	"github.com/tuneinsight/lattigo/v4/rlwe"
	"github.com/tuneinsight/lattigo/v4/utils"
	"github.com/tuneinsight/lattigo/v4/utils/bignum"
	"github.com/tuneinsight/lattigo/v4/utils/bignum/polynomial"
)

// Polynomial evaluates a polynomial in standard basis on the input Ciphertext in ceil(log2(deg+1)) levels.
// Returns an error if the input ciphertext does not have enough level to carry out the full polynomial evaluation.
// Returns an error if something is wrong with the scale.
// If the polynomial is given in Chebyshev basis, then a change of basis ct' = (2/(b-a)) * (ct + (-a-b)/(b-a))
// is necessary before the polynomial evaluation to ensure correctness.
// input must be either *rlwe.Ciphertext or *PolynomialBasis.
// pol: a *polynomial.Polynomial, *rlwe.Polynomial or *rlwe.PolynomialVector
// targetScale: the desired output scale. This value shouldn't differ too much from the original ciphertext scale. It can
// for example be used to correct small deviations in the ciphertext scale and reset it to the default scale.
func (eval *Evaluator) Polynomial(input interface{}, p interface{}, targetScale rlwe.Scale) (opOut *rlwe.Ciphertext, err error) {

	var polyVec *rlwe.PolynomialVector
	switch p := p.(type) {
	case *polynomial.Polynomial:
		polyVec = &rlwe.PolynomialVector{Value: []*rlwe.Polynomial{&rlwe.Polynomial{Polynomial: p, MaxDeg: p.Degree(), Lead: true, Lazy: false}}}
	case *rlwe.Polynomial:
		polyVec = &rlwe.PolynomialVector{Value: []*rlwe.Polynomial{p}}
	case *rlwe.PolynomialVector:
		polyVec = p
	default:
		return nil, fmt.Errorf("cannot Polynomial: invalid polynomial type: %T", p)
	}

	var powerbasis *PowerBasis
	switch input := input.(type) {
	case *rlwe.Ciphertext:
		powerbasis = NewPowerBasis(input, polyVec.Value[0].Basis)
	case *PowerBasis:
		if input.Value[1] == nil {
			return nil, fmt.Errorf("cannot evaluatePolyVector: given PowerBasis.Value[1] is empty")
		}
		powerbasis = input
	default:
		return nil, fmt.Errorf("cannot evaluatePolyVector: invalid input, must be either *rlwe.Ciphertext or *PowerBasis")
	}

	nbModuliPerRescale := eval.params.DefaultScaleModuliRatio()

	if err := checkEnoughLevels(powerbasis.Value[1].Level(), nbModuliPerRescale*polyVec.Value[0].Depth()); err != nil {
		return nil, err
	}

	logDegree := bits.Len64(uint64(polyVec.Value[0].Degree()))
	logSplit := polynomial.OptimalSplit(logDegree)

	var odd, even bool = false, false
	for _, p := range polyVec.Value {
		odd, even = odd || p.IsOdd, even || p.IsEven
	}

	// Computes all the powers of two with relinearization
	// This will recursively compute and store all powers of two up to 2^logDegree
	if err = powerbasis.GenPower(1<<(logDegree-1), false, targetScale, eval); err != nil {
		return nil, err
	}

	// Computes the intermediate powers, starting from the largest, without relinearization if possible
	for i := (1 << logSplit) - 1; i > 2; i-- {
		if !(even || odd) || (i&1 == 0 && even) || (i&1 == 1 && odd) {
			if err = powerbasis.GenPower(i, polyVec.Value[0].Lazy, targetScale, eval); err != nil {
				return nil, err
			}
		}
	}

	PS := polyVec.GetPatersonStockmeyerPolynomial(eval.params.Parameters, powerbasis.Value[1].Level(), powerbasis.Value[1].Scale, targetScale)

	polyEval := &polynomialEvaluator{
		Evaluator: eval,
	}

	if opOut, err = rlwe.EvaluatePatersonStockmeyerPolynomialVector(PS, powerbasis.PowerBasis, polyEval); err != nil {
		return nil, err
	}

	powerbasis = nil

	runtime.GC()
	return opOut, err
}

type polynomialEvaluator struct {
	*Evaluator
}

func (polyEval *polynomialEvaluator) Rescale(op0, op1 *rlwe.Ciphertext) (err error) {
	return polyEval.Evaluator.Rescale(op0, polyEval.Evaluator.Parameters.DefaultScale(), op1)
}

func (polyEval *polynomialEvaluator) UpdateLevelAndScale(lead bool, tLevelOld int, tScaleOld, xPowScale rlwe.Scale) (tLevelNew int, tScaleNew rlwe.Scale) {

	params := polyEval.Parameters
	nbModuliPerRescale := params.DefaultScaleModuliRatio()
	level := tLevelOld

	var qi *big.Int
	if lead {
		qi = bignum.NewInt(params.Q()[level])
		for i := 1; i < nbModuliPerRescale; i++ {
			qi.Mul(qi, bignum.NewInt(params.Q()[level-i]))
		}
	} else {
		qi = bignum.NewInt(params.Q()[level+nbModuliPerRescale])
		for i := 1; i < nbModuliPerRescale; i++ {
			qi.Mul(qi, bignum.NewInt(params.Q()[level+nbModuliPerRescale-i]))
		}
	}

	tScaleNew = tScaleOld.Mul(rlwe.NewScale(qi))
	tScaleNew = tScaleNew.Div(xPowScale)

	return tLevelOld + nbModuliPerRescale, tScaleNew
}

func (polyEval *polynomialEvaluator) EvaluatePolynomialVectorFromPowerBasis(targetLevel int, pol *rlwe.PolynomialVector, pb *rlwe.PowerBasis, targetScale rlwe.Scale) (res *rlwe.Ciphertext, err error) {

	// Map[int] of the powers [X^{0}, X^{1}, X^{2}, ...]
	X := pb.Value

	// Retrieve the number of slots
	logSlots := X[1].LogSlots
	slots := 1 << X[1].LogSlots

	params := polyEval.Evaluator.params
	nbModuliPerRescale := params.DefaultScaleModuliRatio()
	slotsIndex := pol.SlotsIndex
	even := pol.IsEven()
	odd := pol.IsOdd()

	if pol.Value[0].Lead {
		targetScale = targetScale.Mul(rlwe.NewScale(params.Q()[targetLevel]))
		for i := 1; i < nbModuliPerRescale; i++ {
			targetScale = targetScale.Mul(rlwe.NewScale(params.Q()[targetLevel-i]))
		}
	}

	// Retrieve the degree of the highest degree non-zero coefficient
	// TODO: optimize for nil/zero coefficients
	minimumDegreeNonZeroCoefficient := len(pol.Value[0].Coeffs) - 1
	if even && !odd {
		minimumDegreeNonZeroCoefficient--
	}

	// Gets the maximum degree of the ciphertexts among the power basis
	// TODO: optimize for nil/zero coefficients, odd/even polynomial
	maximumCiphertextDegree := 0
	for i := pol.Value[0].Degree(); i > 0; i-- {
		if x, ok := X[i]; ok {
			maximumCiphertextDegree = utils.Max(maximumCiphertextDegree, x.Degree())
		}
	}

	// If an index slot is given (either multiply polynomials or masking)
	if slotsIndex != nil {

		var toEncode bool

		// Allocates temporary buffer for coefficients encoding
		values := make([]*bignum.Complex, slots)

		// If the degree of the poly is zero
		if minimumDegreeNonZeroCoefficient == 0 {

			// Allocates the output ciphertext
			res = NewCiphertext(params, 1, targetLevel)
			res.Scale = targetScale
			res.LogSlots = logSlots

			// Looks for non-zero coefficients among the degree 0 coefficients of the polynomials
			if even {
				for i, p := range pol.Value {
					if !isZero(p.Coeffs[0]) {
						toEncode = true
						for _, j := range slotsIndex[i] {
							values[j] = p.Coeffs[0]
						}
					}
				}
			}

			// If a non-zero coefficient was found, encode the values, adds on the ciphertext, and returns
			if toEncode {
				pt := &rlwe.Plaintext{}
				pt.Value = res.Value[0]
				pt.MetaData = res.MetaData
				if err = polyEval.Evaluator.Encode(values, pt); err != nil {
					return nil, err
				}
			}

			return
		}

		// Allocates the output ciphertext
		res = NewCiphertext(params, maximumCiphertextDegree, targetLevel)
		res.Scale = targetScale
		res.LogSlots = logSlots

		// Looks for a non-zero coefficient among the degree zero coefficient of the polynomials
		if even {
			for i, p := range pol.Value {
				if !isZero(p.Coeffs[0]) {
					toEncode = true
					for _, j := range slotsIndex[i] {
						values[j] = p.Coeffs[0]
					}
				}
			}
		}

		// If a non-zero degre coefficient was found, encode and adds the values on the output
		// ciphertext
		if toEncode {
			polyEval.Add(res, values, res)
			toEncode = false
		}

		// Loops starting from the highest degree coefficient
		for key := pol.Value[0].Degree(); key > 0; key-- {

			var reset bool

			if !(even || odd) || (key&1 == 0 && even) || (key&1 == 1 && odd) {

				// Loops over the polynomials
				for i, p := range pol.Value {

					// Looks for a non-zero coefficient
					if !isZero(p.Coeffs[key]) {
						toEncode = true

						// Resets the temporary array to zero
						// is needed if a zero coefficient
						// is at the place of a previous non-zero
						// coefficient
						if !reset {
							for j := range values {
								if values[j] != nil {
									values[j][0].SetFloat64(0)
									values[j][1].SetFloat64(0)
								}
							}
							reset = true
						}

						// Copies the coefficient on the temporary array
						// according to the slot map index
						for _, j := range slotsIndex[i] {
							values[j] = p.Coeffs[key]
						}
					}
				}
			}

			// If a non-zero degre coefficient was found, encode and adds the values on the output
			// ciphertext
			if toEncode {
				polyEval.MulThenAdd(X[key], values, res)
				toEncode = false
			}
		}

	} else {

		var c *bignum.Complex
		if even && !isZero(pol.Value[0].Coeffs[0]) {
			c = pol.Value[0].Coeffs[0]
		}

		if minimumDegreeNonZeroCoefficient == 0 {

			res = NewCiphertext(params, 1, targetLevel)
			res.Scale = targetScale
			res.LogSlots = logSlots

			if !isZero(c) {
				polyEval.Add(res, c, res)
			}

			return
		}

		res = NewCiphertext(params, maximumCiphertextDegree, targetLevel)
		res.Scale = targetScale
		res.LogSlots = logSlots

		if c != nil {
			polyEval.Add(res, c, res)
		}

		for key := pol.Value[0].Degree(); key > 0; key-- {
			if c = pol.Value[0].Coeffs[key]; key != 0 && !isZero(c) && (!(even || odd) || (key&1 == 0 && even) || (key&1 == 1 && odd)) {
				polyEval.Evaluator.MulThenAdd(X[key], c, res)
			}
		}
	}

	return
}

func isZero(c *bignum.Complex) bool {
	zero := new(big.Float)
	return c == nil || (c[0].Cmp(zero) == 0 && c[1].Cmp(zero) == 0)
}

// checkEnoughLevels checks that enough levels are available to evaluate the polynomial.
// Also checks if c is a Gaussian integer or not. If not, then one more level is needed
// to evaluate the polynomial.
func checkEnoughLevels(levels, depth int) (err error) {

	if levels < depth {
		return fmt.Errorf("%d levels < %d log(d) -> cannot evaluate", levels, depth)
	}

	return nil
}
