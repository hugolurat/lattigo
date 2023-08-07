package bgv

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"runtime"
	"testing"

	"github.com/tuneinsight/lattigo/v4/ring"
	"github.com/tuneinsight/lattigo/v4/rlwe"
	"github.com/tuneinsight/lattigo/v4/utils"

	"github.com/stretchr/testify/require"
	"github.com/tuneinsight/lattigo/v4/utils/sampling"
)

var flagPrintNoise = flag.Bool("print-noise", false, "print the residual noise")
var flagParamString = flag.String("params", "", "specify the test cryptographic parameters as a JSON string. Overrides -short.")

func GetTestName(opname string, p Parameters, lvl int) string {
	return fmt.Sprintf("%s/LogN=%d/logQ=%d/logP=%d/LogSlots=%dx%d/logT=%d/Qi=%d/Pi=%d/lvl=%d",
		opname,
		p.LogN(),
		int(math.Round(p.LogQ())),
		int(math.Round(p.LogP())),
		p.LogMaxDimensions().Rows,
		p.LogMaxDimensions().Cols,
		int(math.Round(p.LogT())),
		p.QCount(),
		p.PCount(),
		lvl)
}

func TestBGV(t *testing.T) {

	var err error

	paramsLiterals := testParams

	if *flagParamString != "" {
		var jsonParams ParametersLiteral
		if err = json.Unmarshal([]byte(*flagParamString), &jsonParams); err != nil {
			t.Fatal(err)
		}
		paramsLiterals = []ParametersLiteral{jsonParams} // the custom test suite reads the parameters from the -params flag
	}

	for _, p := range paramsLiterals[:] {

		for _, plaintextModulus := range testPlaintextModulus[:] {

			p.PlaintextModulus = plaintextModulus

			var params Parameters
			if params, err = NewParametersFromLiteral(p); err != nil {
				t.Error(err)
				t.Fail()
			}

			var tc *testContext
			if tc, err = genTestParams(params); err != nil {
				t.Error(err)
				t.Fail()
			}

			for _, testSet := range []func(tc *testContext, t *testing.T){
				testParameters,
				testEncoder,
				testEvaluator,
			} {
				testSet(tc, t)
				runtime.GC()
			}
		}
	}
}

type testContext struct {
	params      Parameters
	ringQ       *ring.Ring
	ringT       *ring.Ring
	prng        sampling.PRNG
	uSampler    *ring.UniformSampler
	encoder     *Encoder
	kgen        *rlwe.KeyGenerator
	sk          *rlwe.SecretKey
	pk          *rlwe.PublicKey
	encryptorPk *rlwe.Encryptor
	encryptorSk *rlwe.Encryptor
	decryptor   *rlwe.Decryptor
	evaluator   *Evaluator
	testLevel   []int
}

func genTestParams(params Parameters) (tc *testContext, err error) {

	tc = new(testContext)
	tc.params = params

	if tc.prng, err = sampling.NewPRNG(); err != nil {
		return nil, err
	}

	tc.ringQ = params.RingQ()
	tc.ringT = params.RingT()

	tc.uSampler = ring.NewUniformSampler(tc.prng, tc.ringT)
	tc.kgen = NewKeyGenerator(tc.params)
	tc.sk, tc.pk = tc.kgen.GenKeyPairNew()
	tc.encoder = NewEncoder(tc.params)

	tc.encryptorPk = NewEncryptor(tc.params, tc.pk)
	tc.encryptorSk = NewEncryptor(tc.params, tc.sk)
	tc.decryptor = NewDecryptor(tc.params, tc.sk)
	tc.evaluator = NewEvaluator(tc.params, rlwe.NewMemEvaluationKeySet(tc.kgen.GenRelinearizationKeyNew(tc.sk)))

	tc.testLevel = []int{0, params.MaxLevel()}

	return
}

func newTestVectorsLvl(level int, scale rlwe.Scale, tc *testContext, encryptor *rlwe.Encryptor) (coeffs ring.Poly, plaintext *rlwe.Plaintext, ciphertext *rlwe.Ciphertext) {
	coeffs = tc.uSampler.ReadNew()
	for i := range coeffs.Coeffs[0] {
		coeffs.Coeffs[0][i] = uint64(i)
	}

	plaintext = NewPlaintext(tc.params, level)
	plaintext.Scale = scale
	if err := tc.encoder.Encode(coeffs.Coeffs[0], plaintext); err != nil {
		panic(err)
	}
	if encryptor != nil {
		var err error
		ciphertext, err = encryptor.EncryptNew(plaintext)
		if err != nil {
			panic(err)
		}
	}

	return coeffs, plaintext, ciphertext
}

func verifyTestVectors(tc *testContext, decryptor *rlwe.Decryptor, coeffs ring.Poly, element rlwe.OperandInterface[ring.Poly], t *testing.T) {

	coeffsTest := make([]uint64, tc.params.MaxSlots())

	switch el := element.(type) {
	case *rlwe.Plaintext:
		require.NoError(t, tc.encoder.Decode(el, coeffsTest))
	case *rlwe.Ciphertext:

		pt := decryptor.DecryptNew(el)

		require.NoError(t, tc.encoder.Decode(pt, coeffsTest))

		if *flagPrintNoise {
			require.NoError(t, tc.encoder.Encode(coeffsTest, pt))
			ct, err := tc.evaluator.SubNew(el, pt)
			require.NoError(t, err)
			vartmp, _, _ := rlwe.Norm(ct, decryptor)
			t.Logf("STD(noise): %f\n", vartmp)
		}

	default:
		t.Error("invalid test object to verify")
	}

	require.True(t, utils.EqualSlice(coeffs.Coeffs[0], coeffsTest))
}

func testParameters(tc *testContext, t *testing.T) {
	t.Run(GetTestName("Parameters/Binary", tc.params, 0), func(t *testing.T) {

		bytes, err := tc.params.MarshalBinary()
		require.Nil(t, err)
		var p Parameters
		require.Nil(t, p.UnmarshalBinary(bytes))
		require.True(t, tc.params.Equal(p))

	})

	t.Run(GetTestName("Parameters/JSON", tc.params, 0), func(t *testing.T) {
		// checks that parameters can be marshalled without error
		data, err := json.Marshal(tc.params)
		require.Nil(t, err)
		require.NotNil(t, data)

		// checks that ckks.Parameters can be unmarshalled without error
		var paramsRec Parameters
		err = json.Unmarshal(data, &paramsRec)
		require.Nil(t, err)
		require.True(t, tc.params.Equal(paramsRec))

		// checks that ckks.Parameters can be unmarshalled with log-moduli definition without error
		dataWithLogModuli := []byte(fmt.Sprintf(`{"LogN":%d,"LogQ":[50,50],"LogP":[60], "PlaintextModulus":65537}`, tc.params.LogN()))
		var paramsWithLogModuli Parameters
		err = json.Unmarshal(dataWithLogModuli, &paramsWithLogModuli)
		require.Nil(t, err)
		require.Equal(t, 2, paramsWithLogModuli.QCount())
		require.Equal(t, 1, paramsWithLogModuli.PCount())
		require.Equal(t, rlwe.DefaultXe, paramsWithLogModuli.Xe()) // Omitting Xe should result in Default being used
		require.Equal(t, rlwe.DefaultXs, paramsWithLogModuli.Xs()) // Omitting Xe should result in Default being used

		// checks that one can provide custom parameters for the secret-key and error distributions
		dataWithCustomSecrets := []byte(fmt.Sprintf(`{"LogN":%d,"LogQ":[50,50],"LogP":[60], "PlaintextModulus":65537, "Xs": {"Type": "Ternary", "H": 192}, "Xe": {"Type": "DiscreteGaussian", "Sigma": 6.6, "Bound": 39.6}}`, tc.params.LogN()))
		var paramsWithCustomSecrets Parameters
		err = json.Unmarshal(dataWithCustomSecrets, &paramsWithCustomSecrets)
		require.Nil(t, err)
		require.Equal(t, ring.DiscreteGaussian{Sigma: 6.6, Bound: 39.6}, paramsWithCustomSecrets.Xe())
		require.Equal(t, ring.Ternary{H: 192}, paramsWithCustomSecrets.Xs())
	})
}

func testEncoder(tc *testContext, t *testing.T) {

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Encoder/Uint", tc.params, lvl), func(t *testing.T) {
			values, plaintext, _ := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, nil)
			verifyTestVectors(tc, nil, values, plaintext, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Encoder/Int", tc.params, lvl), func(t *testing.T) {

			T := tc.params.PlaintextModulus()
			THalf := T >> 1
			coeffs := tc.uSampler.ReadNew()
			coeffsInt := make([]int64, coeffs.N())
			for i, c := range coeffs.Coeffs[0] {
				c %= T
				if c >= THalf {
					coeffsInt[i] = -int64(T - c)
				} else {
					coeffsInt[i] = int64(c)
				}
			}

			plaintext := NewPlaintext(tc.params, lvl)
			tc.encoder.Encode(coeffsInt, plaintext)
			have := make([]int64, tc.params.MaxSlots())
			tc.encoder.Decode(plaintext, have)
			require.True(t, utils.EqualSlice(coeffsInt, have))
		})
	}
}

func testEvaluator(tc *testContext, t *testing.T) {

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Add/Ct/Ct/New", tc.params, lvl), func(t *testing.T) {

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			ciphertext2, err := tc.evaluator.AddNew(ciphertext0, ciphertext1)
			require.NoError(t, err)
			tc.ringT.Add(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext2, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Add/Ct/Ct/Inplace", tc.params, lvl), func(t *testing.T) {

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			require.NoError(t, tc.evaluator.Add(ciphertext0, ciphertext1, ciphertext0))
			tc.ringT.Add(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Add/Ct/Pt/Inplace", tc.params, lvl), func(t *testing.T) {

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, plaintext, _ := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(plaintext.Scale) != 0)

			require.NoError(t, tc.evaluator.Add(ciphertext0, plaintext, ciphertext0))
			tc.ringT.Add(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Add/Ct/Scalar/Inplace", tc.params, lvl), func(t *testing.T) {

			values, _, ciphertext := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)

			scalar := tc.params.PlaintextModulus() >> 1

			require.NoError(t, tc.evaluator.Add(ciphertext, scalar, ciphertext))
			tc.ringT.AddScalar(values, scalar, values)

			verifyTestVectors(tc, tc.decryptor, values, ciphertext, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Add/Ct/Vector/Inplace", tc.params, lvl), func(t *testing.T) {

			values, _, ciphertext := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)

			require.NoError(t, tc.evaluator.Add(ciphertext, values.Coeffs[0], ciphertext))
			tc.ringT.Add(values, values, values)

			verifyTestVectors(tc, tc.decryptor, values, ciphertext, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Sub/Ct/Ct/New", tc.params, lvl), func(t *testing.T) {

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			ciphertext0, err := tc.evaluator.SubNew(ciphertext0, ciphertext1)
			require.NoError(t, err)
			tc.ringT.Sub(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Sub/Ct/Ct/Inplace", tc.params, lvl), func(t *testing.T) {

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			require.NoError(t, tc.evaluator.Sub(ciphertext0, ciphertext1, ciphertext0))
			tc.ringT.Sub(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Sub/Ct/Pt/Inplace", tc.params, lvl), func(t *testing.T) {

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, plaintext, _ := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(plaintext.Scale) != 0)

			require.NoError(t, tc.evaluator.Sub(ciphertext0, plaintext, ciphertext0))
			tc.ringT.Sub(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Sub/Ct/Scalar/Inplace", tc.params, lvl), func(t *testing.T) {

			values, _, ciphertext := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)

			scalar := tc.params.PlaintextModulus() >> 1

			require.NoError(t, tc.evaluator.Sub(ciphertext, scalar, ciphertext))
			tc.ringT.SubScalar(values, scalar, values)

			verifyTestVectors(tc, tc.decryptor, values, ciphertext, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Sub/Ct/Vector/Inplace", tc.params, lvl), func(t *testing.T) {

			values, _, ciphertext := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)

			require.NoError(t, tc.evaluator.Sub(ciphertext, values.Coeffs[0], ciphertext))
			tc.ringT.Sub(values, values, values)

			verifyTestVectors(tc, tc.decryptor, values, ciphertext, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Mul/Ct/Ct/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			require.NoError(t, tc.evaluator.Mul(ciphertext0, ciphertext1, ciphertext0))
			tc.ringT.MulCoeffsBarrett(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Mul/Ct/Pt/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, plaintext, _ := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(plaintext.Scale) != 0)

			require.NoError(t, tc.evaluator.Mul(ciphertext0, plaintext, ciphertext0))
			tc.ringT.MulCoeffsBarrett(values0, values1, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Mul/Ct/Scalar/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values, _, ciphertext := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)

			scalar := tc.params.PlaintextModulus() >> 1

			require.NoError(t, tc.evaluator.Mul(ciphertext, scalar, ciphertext))
			tc.ringT.MulScalar(values, scalar, values)

			verifyTestVectors(tc, tc.decryptor, values, ciphertext, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Mul/Ct/Vector/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values, _, ciphertext := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)

			require.NoError(t, tc.evaluator.Mul(ciphertext, values.Coeffs[0], ciphertext))
			tc.ringT.MulCoeffsBarrett(values, values, values)

			verifyTestVectors(tc, tc.decryptor, values, ciphertext, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/Square/Ct/Ct/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)

			require.NoError(t, tc.evaluator.Mul(ciphertext0, ciphertext0, ciphertext0))
			tc.ringT.MulCoeffsBarrett(values0, values0, values0)

			verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/MulRelin/Ct/Ct/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			tc.ringT.MulCoeffsBarrett(values0, values1, values0)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			receiver := NewCiphertext(tc.params, 1, lvl)

			require.NoError(t, tc.evaluator.MulRelin(ciphertext0, ciphertext1, receiver))

			require.NoError(t, tc.evaluator.Rescale(receiver, receiver))

			verifyTestVectors(tc, tc.decryptor, values0, receiver, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/MulThenAdd/Ct/Ct/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, rlwe.NewScale(2), tc, tc.encryptorSk)
			values2, _, ciphertext2 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)
			require.True(t, ciphertext0.Scale.Cmp(ciphertext2.Scale) != 0)

			require.NoError(t, tc.evaluator.MulThenAdd(ciphertext0, ciphertext1, ciphertext2))
			tc.ringT.MulCoeffsBarrettThenAdd(values0, values1, values2)

			verifyTestVectors(tc, tc.decryptor, values2, ciphertext2, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/MulThenAdd/Ct/Pt/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)
			values1, plaintext1, _ := newTestVectorsLvl(lvl, rlwe.NewScale(2), tc, tc.encryptorSk)
			values2, _, ciphertext2 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(plaintext1.Scale) != 0)
			require.True(t, ciphertext0.Scale.Cmp(ciphertext2.Scale) != 0)

			require.NoError(t, tc.evaluator.MulThenAdd(ciphertext0, plaintext1, ciphertext2))
			tc.ringT.MulCoeffsBarrettThenAdd(values0, values1, values2)

			verifyTestVectors(tc, tc.decryptor, values2, ciphertext2, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/MulThenAdd/Ct/Scalar/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			scalar := tc.params.PlaintextModulus() >> 1

			require.NoError(t, tc.evaluator.MulThenAdd(ciphertext0, scalar, ciphertext1))
			tc.ringT.MulScalarThenAdd(values0, scalar, values1)

			verifyTestVectors(tc, tc.decryptor, values1, ciphertext1, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/MulThenAdd/Ct/Vector/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.NewScale(3), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)

			scale := ciphertext1.Scale

			require.NoError(t, tc.evaluator.MulThenAdd(ciphertext0, values1.Coeffs[0], ciphertext1))
			tc.ringT.MulCoeffsBarrettThenAdd(values0, values1, values1)

			// Checks that output scale isn't changed
			require.True(t, scale.Equal(ciphertext1.Scale))

			verifyTestVectors(tc, tc.decryptor, values1, ciphertext1, t)
		})
	}

	for _, lvl := range tc.testLevel {
		t.Run(GetTestName("Evaluator/MulRelinThenAdd/Ct/Ct/Inplace", tc.params, lvl), func(t *testing.T) {

			if lvl == 0 {
				t.Skip("Level = 0")
			}

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)
			values1, _, ciphertext1 := newTestVectorsLvl(lvl, rlwe.NewScale(2), tc, tc.encryptorSk)
			values2, _, ciphertext2 := newTestVectorsLvl(lvl, tc.params.NewScale(7), tc, tc.encryptorSk)

			require.True(t, ciphertext0.Scale.Cmp(ciphertext1.Scale) != 0)
			require.True(t, ciphertext0.Scale.Cmp(ciphertext2.Scale) != 0)

			require.NoError(t, tc.evaluator.MulRelinThenAdd(ciphertext0, ciphertext1, ciphertext2))
			tc.ringT.MulCoeffsBarrettThenAdd(values0, values1, values2)

			verifyTestVectors(tc, tc.decryptor, values2, ciphertext2, t)
		})
	}

	for _, lvl := range tc.testLevel[:] {
		t.Run(GetTestName("Evaluator/Rescale", tc.params, lvl), func(t *testing.T) {

			ringT := tc.params.RingT()

			values0, _, ciphertext0 := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorPk)

			printNoise := func(msg string, values []uint64, ct *rlwe.Ciphertext) {
				pt := NewPlaintext(tc.params, ct.Level())
				pt.MetaData = ciphertext0.MetaData
				require.NoError(t, tc.encoder.Encode(values0.Coeffs[0], pt))
				ct, err := tc.evaluator.SubNew(ct, pt)
				require.NoError(t, err)
				vartmp, _, _ := rlwe.Norm(ct, tc.decryptor)
				t.Logf("STD(noise) %s: %f\n", msg, vartmp)
			}

			if lvl != 0 {

				values1, _, ciphertext1 := newTestVectorsLvl(lvl, tc.params.DefaultScale(), tc, tc.encryptorSk)

				if *flagPrintNoise {
					printNoise("0x", values0.Coeffs[0], ciphertext0)
				}

				for i := 0; i < lvl; i++ {
					tc.evaluator.MulRelin(ciphertext0, ciphertext1, ciphertext0)

					ringT.MulCoeffsBarrett(values0, values1, values0)

					if *flagPrintNoise {
						printNoise(fmt.Sprintf("%dx", i+1), values0.Coeffs[0], ciphertext0)
					}

				}

				verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

				require.Nil(t, tc.evaluator.Rescale(ciphertext0, ciphertext0))

				verifyTestVectors(tc, tc.decryptor, values0, ciphertext0, t)

			} else {
				require.NotNil(t, tc.evaluator.Rescale(ciphertext0, ciphertext0))
			}
		})
	}
}
