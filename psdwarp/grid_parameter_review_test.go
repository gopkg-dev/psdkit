package psdwarp_test

import (
	"math"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

// reviewSourceCanonical derives a patch parameter from physical source coordinates.
// It deliberately uses the original typed slice window, without production knot helpers.
func reviewSourceCanonical(placed *psdmodel.Placed, sourceUV float64, alongY bool) float64 {
	values := reviewPhysicalSlices(placed, alongY)
	physical := values[0] + sourceUV*(values[len(values)-1]-values[0])
	piece := 0
	for piece+2 < len(values) && physical >= values[piece+1] {
		piece++
	}
	local := (physical - values[piece]) / (values[piece+1] - values[piece])
	return (float64(piece) + local) / float64(len(values)-1)
}

// reviewCanonicalSourceUV solves the source-window relation in the opposite direction.
func reviewCanonicalSourceUV(placed *psdmodel.Placed, canonical float64, alongY bool) float64 {
	values := reviewPhysicalSlices(placed, alongY)
	scaled := canonical * float64(len(values)-1)
	piece := min(len(values)-2, max(0, int(math.Floor(scaled))))
	local := scaled - float64(piece)
	physical := values[piece] + local*(values[piece+1]-values[piece])
	return (physical - values[0]) / (values[len(values)-1] - values[0])
}

func reviewPhysicalSlices(placed *psdmodel.Placed, alongY bool) []float64 {
	axis := "quiltSliceX"
	if alongY {
		axis = "quiltSliceY"
	}
	for _, slice := range placed.Warp.QuiltSlices {
		if slice.Axis == axis {
			return slice.Values
		}
	}
	panic("review fixture has no physical source slices")
}

func TestUniformGridParameterReviewWholeSourceUVAndOwnedPhysicalKnots(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, guard := range []float64{0, 0.6} {
			name := strconv.Itoa(order) + "/guard=" + strconv.FormatFloat(guard, 'g', -1, 64)
			t.Run(name, func(t *testing.T) {
				affine := expandedGridTransforms()[3]
				placed := expandedGridFixture(order, affine, false)
				for index := range placed.Warp.QuiltSlices {
					slice := &placed.Warp.QuiltSlices[index]
					start, extent := placed.Warp.Bounds.Left, float64(placed.Width)
					if slice.Axis == "quiltSliceY" {
						start, extent = placed.Warp.Bounds.Top, float64(placed.Height)
					}
					slice.Values[0], slice.Values[len(slice.Values)-1] = start-guard, start+extent+guard
				}
				grid, err := psdwarp.CompileUniformGrid(nil, placed)
				if err != nil {
					t.Fatal(err)
				}
				pieces := (order - 1) / 3
				parameters := []float64{0, 0.013, 0.19, 0.58, 0.91, 1}
				for seam := 1; seam < pieces; seam++ {
					parameters = append(parameters, float64(seam)/float64(pieces))
				}
				for _, canonicalU := range parameters {
					for _, canonicalV := range parameters {
						u := reviewCanonicalSourceUV(placed, canonicalU, false)
						v := reviewCanonicalSourceUV(placed, canonicalV, true)
						wanted := affine.point(canonicalU, canonicalV)
						actual, err := grid.Forward(u, v)
						if err != nil || reviewDistance(actual, wanted) > 2e-10 {
							t.Fatalf("physical full-source UV=(%g,%g): %v != %v, %v", u, v, actual, wanted, err)
						}
						actualU, actualV, inside, err := grid.Inverse(nil, wanted)
						if err != nil || !inside || math.Abs(actualU-u) > 2e-7 || math.Abs(actualV-v) > 2e-7 {
							t.Fatalf("inverse did not return full-source UV: %g,%g != %g,%g, %v", actualU, actualV, u, v, err)
						}
					}
				}
				before, err := grid.Forward(0.017, 0.869)
				if err != nil {
					t.Fatal(err)
				}
				placed.Warp.QuiltSlices[0].Values[1] += 12345
				placed.Warp.QuiltSlices[1].Values[0] -= 999
				placed.Warp.Bounds.Left += 123
				placed.Warp.MeshPoints[0].X += 456
				placed.Transform[0] -= 789
				after, err := grid.Forward(0.017, 0.869)
				if err != nil || after != before {
					t.Fatalf("compiled physical knots retain mutable metadata: %v != %v, %v", after, before, err)
				}
			})
		}
	}
}
