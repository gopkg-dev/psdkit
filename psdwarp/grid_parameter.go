package psdwarp

import (
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// gridSourceKnots normalizes the complete typed slice window, including its guards.
// The outer knots remain exactly zero and one; guarded interior knots need not be i/count.
// Validation has already restricted both axes to increasing, paired, supported slices.
func gridSourceKnots(warp *psdmodel.Warp, count int) [2][5]float64 {
	var result [2][5]float64
	for _, slice := range warp.QuiltSlices {
		axis, start, extent := 0, warp.Bounds.Left, warp.Bounds.Right-warp.Bounds.Left
		if slice.Axis == "quiltSliceY" {
			axis, start, extent = 1, warp.Bounds.Top, warp.Bounds.Bottom-warp.Bounds.Top
		}
		first, last := slice.Values[0], slice.Values[count]
		unguarded := math.Abs(first-start) <= math.Max(1e-8, extent*1e-10)
		for index := 0; index <= count; index++ {
			if unguarded {
				result[axis][index] = float64(index) / float64(count)
			} else {
				result[axis][index] = (slice.Values[index] - first) / (last - first)
			}
		}
		result[axis][0], result[axis][count] = 0, 1
	}
	return result
}

func gridAxisUniform(knots *[5]float64, count int) bool {
	return knots[1] == 1/float64(count) && knots[count-1] == float64(count-1)/float64(count)
}

// gridAxisParameter selects a half-open source interval, extrapolating only the outside pieces.
func gridAxisParameter(knots *[5]float64, count int, coordinate float64) (int, float64) {
	if gridAxisUniform(knots, count) {
		scaled := coordinate * float64(count)
		piece := min(count-1, max(0, int(math.Floor(scaled))))
		return piece, scaled - float64(piece)
	}
	piece := 0
	for piece+1 < count && coordinate >= knots[piece+1] {
		piece++
	}
	return piece, (coordinate - knots[piece]) / (knots[piece+1] - knots[piece])
}

func gridAxisCoordinate(knots *[5]float64, count, piece int, local float64) float64 {
	if gridAxisUniform(knots, count) {
		return (float64(piece) + local) / float64(count)
	}
	return knots[piece] + local*(knots[piece+1]-knots[piece])
}

// gridAxisLattice keeps each piece boundary on a cell edge without moving the document-space lattice.
func gridAxisLattice(knots *[5]float64, count, index, steps int) float64 {
	if gridAxisUniform(knots, count) {
		return float64(index) / float64(steps)
	}
	perPiece := steps / count
	piece := min(count-1, index/perPiece)
	local := float64(index-piece*perPiece) / float64(perPiece)
	return gridAxisCoordinate(knots, count, piece, local)
}
