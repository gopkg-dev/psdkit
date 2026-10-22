package psdcompose

import (
	"math"
	"math/bits"
)

func dissolveMode(mode string) bool {
	mode = normalizeBlend(mode)
	return mode == "diss" || mode == "dissolve"
}

// dissolvePainted uses an explicitly selected psdkit noise field, not Photoshop's private field.
func dissolvePainted(seed, layerSalt uint64, x, y int, coverage float64) bool {
	if coverage <= 0 {
		return false
	}
	if coverage >= 1 {
		return true
	}
	field := dissolveMix(seed ^ dissolveMix(layerSalt) ^ dissolveMix(uint64(x)) ^
		bits.RotateLeft64(dissolveMix(uint64(y)), 32))
	threshold := uint64(math.Round(coverage * (1 << 24)))
	return field>>40 < threshold
}

func dissolveMix(value uint64) uint64 {
	value += 0x9e3779b97f4a7c15
	value = (value ^ value>>30) * 0xbf58476d1ce4e5b9
	value = (value ^ value>>27) * 0x94d049bb133111eb
	return value ^ value>>31
}
