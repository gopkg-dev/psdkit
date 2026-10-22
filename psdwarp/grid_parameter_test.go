package psdwarp_test

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func parameterGridFixture(order, width, height int, guard float64) *psdmodel.Placed {
	placed := reviewGridFixture(order)
	placed.Width, placed.Height = width, height
	placed.Transform = []float64{11, -7, 11 + float64(width), -7,
		11 + float64(width), -7 + float64(height), 11, -7 + float64(height)}
	placed.Warp.Bounds = &psdmodel.WarpBounds{
		Left: 61.25, Top: -47.75, Right: 61.25 + float64(width), Bottom: -47.75 + float64(height), Pixels: true,
	}
	count := (order - 1) / 3
	for axis, extent := range []float64{float64(width), float64(height)} {
		start := placed.Warp.Bounds.Left
		if axis == 1 {
			start = placed.Warp.Bounds.Top
		}
		values := placed.Warp.QuiltSlices[axis].Values
		for index := range values {
			values[index] = start + extent*float64(index)/float64(count)
		}
		values[0], values[count] = start-guard, start+extent+guard
	}
	for row := range order {
		for column := range order {
			placed.Warp.MeshPoints[row*order+column] = psdmodel.Point{
				X: -209.75 + float64(width)*float64(column)/float64(order-1),
				Y: 317.5 + float64(height)*float64(row)/float64(order-1),
			}
		}
	}
	return placed
}

func TestUniformGridNormalizedSliceWindow(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, size := range []image.Point{image.Pt(127, 95), image.Pt(256, 192), image.Pt(257, 193)} {
			for _, guard := range []float64{0, 0.6} {
				name := strconv.Itoa(order) + "/" + size.String() + "/guard=" + strconv.FormatFloat(guard, 'g', -1, 64)
				t.Run(name, func(t *testing.T) {
					placed := parameterGridFixture(order, size.X, size.Y, guard)
					grid, err := psdwarp.CompileUniformGrid(nil, placed)
					if err != nil {
						t.Fatal(err)
					}
					count := (order - 1) / 3
					for index := 0; index <= count; index++ {
						fraction := float64(index) / float64(count)
						u := (float64(size.X)*fraction + guard) / (float64(size.X) + 2*guard)
						v := (float64(size.Y)*fraction + guard) / (float64(size.Y) + 2*guard)
						if index == 0 || index == count {
							u, v = fraction, fraction
						}
						point, err := grid.Forward(u, v)
						want := psdmodel.Point{X: 11 + float64(size.X)*fraction, Y: -7 + float64(size.Y)*fraction}
						if err != nil || math.Hypot(point.X-want.X, point.Y-want.Y) > 3e-10 {
							t.Fatalf("source seam %d: %v, want %v, error %v", index, point, want, err)
						}
						actualU, actualV, inside, err := grid.Inverse(nil, want)
						if err != nil || !inside || math.Abs(actualU-u) > 2e-8 || math.Abs(actualV-v) > 2e-8 {
							t.Fatalf("source seam inverse %d: %g,%g,%v,%v", index, actualU, actualV, inside, err)
						}
					}
					before, err := grid.Forward(0.19, 0.37)
					if err != nil {
						t.Fatal(err)
					}
					placed.Warp.QuiltSlices[0].Values[1] += 20
					placed.Warp.Bounds.Left += 50
					after, err := grid.Forward(0.19, 0.37)
					if err != nil || after != before {
						t.Fatal("compiled source knots alias caller metadata")
					}
				})
			}
		}
	}
}

// parameterSourcePixel uses document position and the physical slice window, without the compiled inverse.
func parameterSourcePixel(position, extent float64, count int) float64 {
	piece := min(count-1, int(math.Floor(position*float64(count)/extent)))
	left := (float64(piece)*extent/float64(count) + 0.6) / (extent + 1.2)
	right := (float64(piece+1)*extent/float64(count) + 0.6) / (extent + 1.2)
	if piece == 0 {
		left = 0
	}
	if piece == count-1 {
		right = 1
	}
	local := position*float64(count)/extent - float64(piece)
	return (left+(right-left)*local)*extent - 0.5
}

func parameterTriangle(coordinate float64) float64 {
	phase := math.Mod(coordinate, 32)
	if phase > 16 {
		phase = 32 - phase
	}
	return phase * 15
}

func TestUniformGridNormalizedSliceRasterCoordinateOracle(t *testing.T) {
	for _, order := range []int{10, 13} {
		placed := parameterGridFixture(order, 256, 192, 0.6)
		grid, err := psdwarp.CompileUniformGrid(nil, placed)
		if err != nil {
			t.Fatal(err)
		}
		backing := image.NewNRGBA(image.Rect(-31, 5, 241, 213))
		source := backing.SubImage(image.Rect(-23, 13, 233, 205)).(*image.NRGBA)
		for row := range 192 {
			for column := range 256 {
				source.SetNRGBA(column-23, row+13, color.NRGBA{
					R: byte(parameterTriangle(float64(column))), G: byte(parameterTriangle(float64(row))), B: 127, A: 255,
				})
			}
		}
		before := bytes.Clone(source.Pix)
		pixels, origin, err := grid.Project(nil, source, image.Rectangle{})
		if err != nil {
			t.Fatal(err)
		}
		compared := 0
		for row := 10; row < 182; row++ {
			for column := 10; column < 246; column++ {
				x := parameterSourcePixel(float64(column)+0.5, 256, (order-1)/3)
				y := parameterSourcePixel(float64(row)+0.5, 192, (order-1)/3)
				if int(math.Floor(x))%16 == 15 || int(math.Floor(y))%16 == 15 {
					continue
				}
				actual := pixels.NRGBAAt(11+column-origin.X, -7+row-origin.Y)
				if math.Abs(float64(actual.R)-parameterTriangle(x)) > 0.5000001 ||
					math.Abs(float64(actual.G)-parameterTriangle(y)) > 0.5000001 || actual.B != 127 || actual.A != 255 {
					t.Fatalf("order %d pixel %d,%d: %v differs from source-window oracle %g,%g",
						order, column, row, actual, x, y)
				}
				compared++
			}
		}
		if compared < 30_000 || !bytes.Equal(before, source.Pix) {
			t.Fatal("source ownership or independent coordinate-oracle coverage failed")
		}
	}
}
