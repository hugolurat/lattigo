package rlwe

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/big"

	"github.com/tuneinsight/lattigo/v4/utils/bignum"
)

const (
	// ScalePrecision is the default precision of the scale.
	ScalePrecision = uint(128)
)

// Scale is a struct used to track the scaling factor
// of Plaintext and Ciphertext structs.
// The scale is managed as an 128-bit precision real and can
// be either a floating point value or a mod T
// prime integer, which is determined at instantiation.
type Scale struct {
	Value big.Float
	Mod   *big.Int
}

// NewScale instantiates a new floating point Scale.
// Accepted types for s are int, int64, uint64, float64, *big.Int, *big.Float and *Scale.
// If the input type is not an accepted type, returns an error.
func NewScale(s interface{}) Scale {
	return Scale{Value: *scaleToBigFloat(s)}
}

// NewScaleModT instantiates a new integer mod T Scale.
// Accepted types for s are int, int64, uint64, float64, *big.Int, *big.Float and *Scale.
// If the input type is not an accepted type, returns an error.
func NewScaleModT(s interface{}, mod uint64) Scale {
	scale := NewScale(s)
	if mod != 0 {
		scale.Mod = big.NewInt(0).SetUint64(mod)
	}
	return scale
}

// Float64 returns the underlying scale as a float64 value.
func (s Scale) Float64() float64 {
	f64, _ := s.Value.Float64()
	return f64
}

// Uint64 returns the underlying scale as an uint64 value.
func (s Scale) Uint64() uint64 {
	u64, _ := s.Value.Uint64()
	return u64
}

// Mul multiplies the target s with s1, returning the result in
// a new Scale struct. If mod is specified, performs the multiplication
// modulo mod.
func (s Scale) Mul(s1 Scale) Scale {

	res := new(big.Float)

	if s.Mod != nil {
		si, _ := s.Value.Int(nil)
		s1i, _ := s1.Value.Int(nil)
		s1i.Mul(si, s1i)
		s1i.Mod(s1i, s.Mod)
		res.SetPrec(ScalePrecision)
		res.SetInt(s1i)
	} else {
		res.Mul(&s.Value, &s1.Value)
	}

	return Scale{Value: *res, Mod: s.Mod}
}

// Div multiplies the target s with s1^-1, returning the result in
// a new Scale struct. If mod is specified, performs the multiplication
// modulo t with the multiplicative inverse of s1. Otherwise, performs
// the quotient operation.
func (s Scale) Div(s1 Scale) Scale {

	res := new(big.Float)

	if s.Mod != nil {
		s1i, _ := s.Value.Int(nil)
		s2i, _ := s1.Value.Int(nil)

		s2i.ModInverse(s2i, s.Mod)

		s1i.Mul(s1i, s2i)
		s1i.Mod(s1i, s.Mod)

		res.SetPrec(ScalePrecision)
		res.SetInt(s1i)
	} else {
		res.Quo(&s.Value, &s1.Value)
	}

	return Scale{Value: *res, Mod: s.Mod}
}

// Cmp compares the target scale with s1.
// Returns 0 if the scales are equal, 1 if
// the target scale is greater and -1 if
// the target scale is smaller.
func (s Scale) Cmp(s1 Scale) (cmp int) {
	return s.Value.Cmp(&s1.Value)
}

// Equal returns true if a == b.
func (s Scale) Equal(s1 Scale) bool {
	return s.Cmp(s1) == 0
}

// InDelta returns true if abs(a-b) <= 2^{-log2Delta}
func (s Scale) InDelta(s1 Scale, log2Delta float64) bool {
	return s.Log2Delta(s1) >= log2Delta
}

// Log2Delta returns -log2(abs(a-b)/max(a, b))
func (s Scale) Log2Delta(s1 Scale) float64 {
	d := new(big.Float).Sub(&s.Value, &s1.Value)
	d.Abs(d)
	max := s.Max(s1)
	d.Quo(d, &max.Value)
	d.Quo(bignum.Log(d), bignum.Log2(s.Value.Prec()))
	d.Neg(d)
	f64, _ := d.Float64()
	return f64
}

// Max returns the a new scale which is the maximum
// between the target scale and s1.
func (s Scale) Max(s1 Scale) (max Scale) {

	if s.Cmp(s1) < 0 {
		return s1
	}

	return s
}

// Min returns the a new scale which is the minimum
// between the target scale and s1.
func (s Scale) Min(s1 Scale) (max Scale) {

	if s.Cmp(s1) > 0 {
		return s1
	}

	return s
}

// MarshalBinary encodes the object into a binary form on a newly allocated slice of bytes.
func (s Scale) MarshalBinary() (p []byte, err error) {
	p = make([]byte, s.BinarySize())
	_, err = s.EncodeScale(p)
	return
}

// UnmarshalBinary decodes a slice of bytes generated by
// MarshalBinary or WriteTo on the object.
func (s Scale) UnmarshalBinary(p []byte) (err error) {
	_, err = s.DecodeScale(p)
	return
}

// MarshalJSON encodes the object into a binary form on a newly allocated slice of bytes.
func (s Scale) MarshalJSON() (p []byte, err error) {
	aux := &struct {
		Value *big.Float
		Mod   *big.Int
	}{
		Value: &s.Value,
		Mod:   s.Mod,
	}
	return json.Marshal(aux)
}

func (s *Scale) UnmarshalJSON(p []byte) (err error) {

	aux := &struct {
		Value *big.Float
		Mod   *big.Int
	}{
		Value: new(big.Float).SetPrec(ScalePrecision),
		Mod:   s.Mod,
	}

	if err = json.Unmarshal(p, aux); err != nil {
		return
	}

	s.Value = *aux.Value
	s.Mod = aux.Mod

	return
}

// BinarySize returns the serialized size of the object in bytes.
func (s Scale) BinarySize() int {
	return 48
}

// EncodeScale encodes the object into a binary form on a preallocated slice of bytes
// and returns the number of bytes written.
func (s Scale) EncodeScale(p []byte) (ptr int, err error) {
	var sBytes []byte
	if sBytes, err = s.Value.MarshalText(); err != nil {
		return
	}

	b := make([]byte, s.BinarySize())

	if len(p) < len(b) {
		return 0, fmt.Errorf("cannot encode scale: len(p) < %d", len(b))
	}

	b[0] = uint8(len(sBytes))
	copy(b[1:], sBytes)
	copy(p, b)

	if s.Mod != nil {
		binary.LittleEndian.PutUint64(p[40:], s.Mod.Uint64())
	}

	return s.BinarySize(), nil
}

// DecodeScale decodes a slice of bytes generated by EncodeScale
// on the object and returns the number of bytes read.
func (s *Scale) DecodeScale(p []byte) (ptr int, err error) {

	if dLen := s.BinarySize(); len(p) < dLen {
		return 0, fmt.Errorf("cannot Decode: len(p) < %d", dLen)
	}

	bLen := p[0]

	v := new(big.Float)

	if p[1] != 0x30 || bLen > 1 { // 0x30 indicates an empty big.Float
		if err = v.UnmarshalText(p[1 : bLen+1]); err != nil {
			return 0, err
		}

		v.SetPrec(ScalePrecision)
	}

	mod := binary.LittleEndian.Uint64(p[40:])

	s.Value = *v

	if mod != 0 {
		s.Mod = big.NewInt(0).SetUint64(mod)
	}

	return s.BinarySize(), nil
}

func scaleToBigFloat(scale interface{}) (s *big.Float) {

	switch scale := scale.(type) {
	case float64:
		if scale < 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
			panic(fmt.Errorf("scale cannot be negative, NaN or Inf, but is %f", scale))
		}

		s = new(big.Float).SetPrec(ScalePrecision)
		s.SetFloat64(scale)
		return
	case *big.Float:
		if scale.Cmp(new(big.Float).SetFloat64(0)) < 0 || scale.IsInf() {
			panic(fmt.Errorf("scale cannot be negative, but is %f", scale))
		}
		s = new(big.Float).SetPrec(ScalePrecision)
		s.Set(scale)
		return
	case big.Float:
		if scale.Cmp(new(big.Float).SetFloat64(0)) < 0 || scale.IsInf() {
			panic(fmt.Errorf("scale cannot be negative, but is %f", &scale))
		}
		s = new(big.Float).SetPrec(ScalePrecision)
		s.Set(&scale)
		return
	case *big.Int:
		if scale.Cmp(new(big.Int).SetInt64(0)) < 0 {
			panic(fmt.Errorf("scale cannot be negative, but is %f", scale))
		}
		s = new(big.Float).SetPrec(ScalePrecision)
		s.SetInt(scale)
		return
	case big.Int:
		if scale.Cmp(new(big.Int).SetInt64(0)) < 0 {
			panic(fmt.Errorf("scale cannot be negative, but is %f", &scale))
		}
		s = new(big.Float).SetPrec(ScalePrecision)
		s.SetInt(&scale)
		return
	case int:
		return scaleToBigFloat(new(big.Int).SetInt64(int64(scale)))
	case int64:
		return scaleToBigFloat(new(big.Int).SetInt64(scale))
	case uint64:
		return scaleToBigFloat(new(big.Int).SetUint64(scale))
	case Scale:
		return scaleToBigFloat(scale.Value)
	default:
		panic(fmt.Errorf("invalid scale.(type): must be int, int64, uint64, float64, *big.Int, *big.Float or *Scale but is %T", scale))
	}
}
