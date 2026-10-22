package psdvector

import "math"

// coverageLevel16 applies the explicit 16-bin transfer after all filled intervals have been combined.
// Amount and denominator remain floating point so horizontal coverage is not first rounded to a byte.
func coverageLevel16(amount, denominator float64) byte {
	level := math.Floor(amount * 16 / denominator)
	return byte(math.Max(0, math.Min(15, level))) * 17
}
