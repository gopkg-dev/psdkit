// Package psdrgb16normal contains the calibrated encoded RGB16 Normal pixel arithmetic.
package psdrgb16normal

import (
	"errors"
	"math"
)

// One is full coverage in Photoshop's encoded RGB16 integer domain.
const One uint32 = 32768

// Pixel stores straight encoded RGB and alpha in the 0..32768 domain.
type Pixel [4]uint32

var (
	// ErrNonFinite identifies an input that cannot represent a raw source code.
	ErrNonFinite = errors.New("non-finite RGB16 source code")
	// ErrNonCanonical identifies a value that is not an exact normalized raw source code.
	ErrNonCanonical = errors.New("noncanonical RGB16 source code")
)

// FromNormalized reads four exact uint16 source codes divided by 65535.
func FromNormalized(values []float64) (Pixel, error) {
	var result Pixel
	if len(values) < 4 {
		return result, ErrNonCanonical
	}
	for component := range result {
		value := values[component]
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return Pixel{}, ErrNonFinite
		}
		if value < 0 || value > 1 {
			return Pixel{}, ErrNonCanonical
		}
		raw := uint32(math.Round(value * 65535))
		if float64(raw)/65535 != value {
			return Pixel{}, ErrNonCanonical
		}
		result[component] = (raw + 1) / 2
	}
	return result, nil
}

// ByteOpacity verifies an exact byte divided by 255.
func ByteOpacity(value float64) (uint8, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, ErrNonFinite
	}
	if value < 0 || value > 1 {
		return 0, ErrNonCanonical
	}
	code := uint32(math.Round(value * 255))
	if float64(code)/255 != value {
		return 0, ErrNonCanonical
	}
	return uint8(code), nil
}

func effectiveAlpha(alpha uint32, opacity uint8) uint32 {
	opacityQ := (uint32(opacity)*One + 127) / 255
	return (alpha*opacityQ + One/2) / One
}

// Base initializes the first active raster, retaining its straight RGB even at zero effective alpha.
func Base(source Pixel, opacity uint8) Pixel {
	source[3] = effectiveAlpha(source[3], opacity)
	return source
}

// Over applies Normal source-over with a rounded normalized source weight.
// Zero output alpha retains the current backdrop RGB; missing geometric coverage is handled by the caller.
func Over(backdrop, source Pixel, opacity uint8) Pixel {
	alphaSource := effectiveAlpha(source[3], opacity)
	alphaOutput := alphaSource + backdrop[3] - (alphaSource*backdrop[3]+One/2)/One
	result := backdrop
	result[3] = alphaOutput
	if alphaOutput == 0 {
		return result
	}
	weight := (alphaSource*One + alphaOutput/2) / alphaOutput
	for component := range 3 {
		result[component] = (backdrop[component]*(One-weight) + source[component]*weight + One/2) / One
	}
	return result
}
