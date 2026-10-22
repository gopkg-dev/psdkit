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

type expandedGridAffine struct {
	name           string
	xx, xy, yx, yy float64
	translationX   float64
	translationY   float64
}

func (a expandedGridAffine) point(x, y float64) psdmodel.Point {
	return psdmodel.Point{
		X: a.translationX + a.xx*x + a.xy*y,
		Y: a.translationY + a.yx*x + a.yy*y,
	}
}

func expandedGridTransforms() []expandedGridAffine {
	angle := -22 * math.Pi / 180
	return []expandedGridAffine{
		{name: "identity", xx: 73, yy: 61},
		{name: "fractional-negative", xx: 73, yy: 61, translationX: -23.25, translationY: -17.75},
		{name: "rotation", xx: 73 * math.Cos(angle), xy: -61 * math.Sin(angle),
			yx: 73 * math.Sin(angle), yy: 61 * math.Cos(angle), translationX: -13.25, translationY: 29.75},
		{name: "shear", xx: 73, xy: 18.5, yx: -9.75, yy: 61, translationX: 14.25, translationY: -29.75},
		{name: "nonuniform-scale", xx: 109.5, yy: 30.5, translationX: -37.75, translationY: 11.25},
	}
}

func expandedGridFixture(order int, affine expandedGridAffine, curved bool) *psdmodel.Placed {
	const width, height = 73, 61
	const sourceLeft, sourceTop = 61.25, -47.75
	const meshLeft, meshTop = -209.75, 317.5
	pieces := (order - 1) / 3
	warp := &psdmodel.Warp{
		ClassID: "quiltWarp", Style: "custom", Rotate: "horizontal", UOrder: 4, VOrder: 4,
		DeformRows: order, DeformColumns: order, HasDeformRows: true, HasDeformColumns: true,
		Split: true, HasEnvelope: true, MeshUnits: [2]string{"#Pxl", "#Pxl"},
		Bounds: &psdmodel.WarpBounds{
			Left: sourceLeft, Top: sourceTop, Right: sourceLeft + width, Bottom: sourceTop + height, Pixels: true,
		},
	}
	for axis, extent := range []float64{width, height} {
		start, name := sourceLeft, "quiltSliceX"
		if axis == 1 {
			start, name = sourceTop, "quiltSliceY"
		}
		values := make([]float64, pieces+1)
		for index := range values {
			values[index] = start + extent*float64(index)/float64(pieces)
		}
		values[0] -= 0.6
		values[pieces] += 0.6
		warp.QuiltSlices = append(warp.QuiltSlices, psdmodel.WarpSlice{
			Axis: name, Location: "envelope", ClassID: "UntF", Field: name,
			Kind: "UnFl", Unit: "#Pxl", Prefix: uint32(len(values)), Values: values,
		})
	}
	xHandles, yHandles := [4]float64{0, 1.0 / 3, 2.0 / 3, 1}, [4]float64{0, 1.0 / 3, 2.0 / 3, 1}
	if curved {
		xHandles, yHandles = [4]float64{0, 0.2, 0.58, 1}, [4]float64{0, 0.42, 0.72, 1}
	}
	for row := range order {
		rowPiece, rowHandle := row/3, row%3
		if row == order-1 {
			rowPiece, rowHandle = pieces-1, 3
		}
		for column := range order {
			columnPiece, columnHandle := column/3, column%3
			if column == order-1 {
				columnPiece, columnHandle = pieces-1, 3
			}
			warp.MeshPoints = append(warp.MeshPoints, psdmodel.Point{
				X: meshLeft + width*(float64(columnPiece)+xHandles[columnHandle])/float64(pieces),
				Y: meshTop + height*(float64(rowPiece)+yHandles[rowHandle])/float64(pieces),
			})
		}
	}
	placed := &psdmodel.Placed{Width: width, Height: height, Warp: warp}
	for _, coordinates := range [4][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
		point := affine.point(coordinates[0], coordinates[1])
		placed.Transform = append(placed.Transform, point.X, point.Y)
	}
	return placed
}

// expandedGridPolynomial uses the power basis of a separable piecewise cubic.
// It neither reads the mesh nor calls the production Bernstein or inverse routines.
func expandedGridPolynomial(parameter float64, pieces int, alongY bool) float64 {
	scaled := parameter * float64(pieces)
	piece := int(math.Floor(scaled))
	if piece == pieces {
		return 1
	}
	local := scaled - float64(piece)
	value := local * (0.6 + local*(0.54-0.14*local))
	if alongY {
		value = local * (1.26 + local*(-0.36+0.10*local))
	}
	return (float64(piece) + value) / float64(pieces)
}

func TestUniformGridExpandedAnalyticAffineAndC0Matrix(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, affine := range expandedGridTransforms() {
			t.Run(strconv.Itoa(order)+"/"+affine.name, func(t *testing.T) {
				placed := expandedGridFixture(order, affine, true)
				grid, err := psdwarp.CompileUniformGrid(nil, placed)
				if err != nil {
					t.Fatal(err)
				}
				pieces := (order - 1) / 3
				parameters := []float64{0, 0.013, 0.139, 0.549, 0.938, 1}
				for seam := 1; seam < pieces; seam++ {
					for _, alongY := range []bool{false, true} {
						parameter := reviewCanonicalSourceUV(placed, float64(seam)/float64(pieces), alongY)
						parameters = append(parameters, math.Nextafter(parameter, 0), parameter,
							math.Nextafter(parameter, 1))
					}
				}
				for _, u := range parameters {
					for _, v := range parameters {
						canonicalU, canonicalV := reviewSourceCanonical(placed, u, false), reviewSourceCanonical(placed, v, true)
						expected := affine.point(expandedGridPolynomial(canonicalU, pieces, false),
							expandedGridPolynomial(canonicalV, pieces, true))
						actual, err := grid.Forward(u, v)
						if err != nil || math.Hypot(actual.X-expected.X, actual.Y-expected.Y) > 3e-10 {
							t.Fatalf("UV=(%g,%g): point=%v want=%v, error=%v", u, v, actual, expected, err)
						}
						actualU, actualV, inside, err := grid.Inverse(nil, expected)
						if err != nil || !inside || math.Abs(actualU-u) > 2e-7 || math.Abs(actualV-v) > 2e-7 {
							t.Fatalf("UV=(%g,%g): inverse=(%g,%g,%v), error=%v", u, v, actualU, actualV, inside, err)
						}
					}
				}
				seam, epsilon := 1/float64(pieces), 1e-7
				left := expandedGridPolynomial(seam-epsilon, pieces, false)
				right := expandedGridPolynomial(seam+epsilon, pieces, false)
				if math.Abs((seam-left)/epsilon-1.26) > 2e-6 ||
					math.Abs((right-seam)/epsilon-0.6) > 2e-6 {
					t.Fatal("the analytic fixture must retain its C0-only derivative jump")
				}
			})
		}
	}
}

func expandedGridSource() (*image.NRGBA, *image.NRGBA) {
	backing := image.NewNRGBA(image.Rect(-31, 17, 58, 94))
	source := backing.SubImage(image.Rect(-23, 25, 50, 86)).(*image.NRGBA)
	packed := image.NewNRGBA(image.Rect(0, 0, source.Rect.Dx(), source.Rect.Dy()))
	for row := range source.Rect.Dy() {
		for column := range source.Rect.Dx() {
			pixel := color.NRGBA{R: byte(3*column + 7*row), G: byte(11*column + 5*row),
				B: byte(17*column + 13*row), A: byte(43 + (column*5+row*3)%211)}
			source.SetNRGBA(column+source.Rect.Min.X, row+source.Rect.Min.Y, pixel)
			packed.SetNRGBA(column, row, pixel)
		}
	}
	return source, packed
}

func expandedGridAssertClipped(
	t *testing.T, complete *image.NRGBA, origin image.Point, clipped *image.NRGBA, clippedOrigin image.Point,
	expected image.Rectangle,
) {
	t.Helper()
	if clippedOrigin != expected.Min || clipped.Rect.Size() != expected.Size() {
		t.Fatalf("clipped frame=%v+%v, want=%v", clippedOrigin, clipped.Rect.Size(), expected)
	}
	for row := range clipped.Rect.Dy() {
		for column := range clipped.Rect.Dx() {
			actual := clipped.NRGBAAt(column, row)
			wanted := complete.NRGBAAt(column+clippedOrigin.X-origin.X, row+clippedOrigin.Y-origin.Y)
			if actual != wanted {
				t.Fatalf("ROI changed global pixel phase at (%d,%d): %v != %v", column, row, actual, wanted)
			}
		}
	}
}

func TestUniformGridExpandedSourceStrideOriginAndROI(t *testing.T) {
	source, packed := expandedGridSource()
	before := bytes.Clone(source.Pix)
	for _, order := range []int{10, 13} {
		for _, affine := range expandedGridTransforms()[1:4] {
			for _, supersampled := range []bool{false, true} {
				name := strconv.Itoa(order) + "/" + affine.name + "/supersampled=" + strconv.FormatBool(supersampled)
				t.Run(name, func(t *testing.T) {
					grid, err := psdwarp.CompileUniformGrid(nil, expandedGridFixture(order, affine, true))
					if err != nil {
						t.Fatal(err)
					}
					project := func(pixels *image.NRGBA, canvas image.Rectangle) (*image.NRGBA, image.Point, error) {
						if supersampled {
							return grid.ProjectSupersampled(nil, pixels, canvas)
						}
						return grid.Project(nil, pixels, canvas)
					}
					complete, origin, err := project(source, image.Rectangle{})
					if err != nil {
						t.Fatal(err)
					}
					packedResult, packedOrigin, err := project(packed, image.Rectangle{})
					if err != nil || packedOrigin != origin || packedResult.Rect != complete.Rect ||
						!bytes.Equal(packedResult.Pix, complete.Pix) {
						t.Fatalf("source origin or stride changed output: %v", err)
					}
					frame := complete.Rect.Add(origin)
					centerX, centerY := frame.Min.X+frame.Dx()/2, frame.Min.Y+frame.Dy()/2
					regions := []image.Rectangle{
						image.Rect(frame.Min.X-7, frame.Min.Y-9, centerX+1, centerY+1),
						image.Rect(centerX-1, centerY-1, frame.Max.X+7, frame.Max.Y+9),
						image.Rect(centerX-5, frame.Min.Y+3, centerX+7, frame.Max.Y-3),
					}
					for _, region := range regions {
						clipped, clippedOrigin, err := project(source, region)
						if err != nil {
							t.Fatal(err)
						}
						expandedGridAssertClipped(t, complete, origin, clipped, clippedOrigin, frame.Intersect(region))
					}
					if !bytes.Equal(source.Pix, before) {
						t.Fatal("projection mutated a caller-owned source or stride padding")
					}
				})
			}
		}
	}
}

func TestUniformGridExpandedIntegerTranslationKeepsFractionalPhase(t *testing.T) {
	_, source := expandedGridSource()
	shift := image.Pt(-97, 64)
	for _, order := range []int{10, 13} {
		for _, phase := range []float64{0, 0.25, 0.5, 0.75} {
			affine := expandedGridAffine{xx: 73, xy: 18.5, yx: -9.75, yy: 61,
				translationX: -15 + phase, translationY: 7.125 + phase}
			for _, supersampled := range []bool{false, true} {
				name := strconv.Itoa(order) + "/phase=" + strconv.FormatFloat(phase, 'g', -1, 64) +
					"/supersampled=" + strconv.FormatBool(supersampled)
				t.Run(name, func(t *testing.T) {
					var results [2]*image.NRGBA
					var origins [2]image.Point
					for index := range 2 {
						placement := affine
						if index == 1 {
							placement.translationX += float64(shift.X)
							placement.translationY += float64(shift.Y)
						}
						grid, err := psdwarp.CompileUniformGrid(nil, expandedGridFixture(order, placement, true))
						if err != nil {
							t.Fatal(err)
						}
						if supersampled {
							results[index], origins[index], err = grid.ProjectSupersampled(nil, source, image.Rectangle{})
						} else {
							results[index], origins[index], err = grid.Project(nil, source, image.Rectangle{})
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					if origins[1] != origins[0].Add(shift) || results[0].Rect != results[1].Rect {
						t.Fatalf("integer translation changed frame: %v+%v vs %v+%v",
							origins[0], results[0].Rect, origins[1], results[1].Rect)
					}
					if !bytes.Equal(results[0].Pix, results[1].Pix) {
						for index, value := range results[0].Pix {
							if value != results[1].Pix[index] {
								t.Fatalf("integer shift changed fractional-phase byte %d: %d != %d",
									index, value, results[1].Pix[index])
							}
						}
					}
				})
			}
		}
	}
}

func TestUniformGridExpandedAffineRasterHasIndependentLinearColorOracle(t *testing.T) {
	backing := image.NewNRGBA(image.Rect(-29, 13, 60, 90))
	source := backing.SubImage(image.Rect(-21, 21, 52, 82)).(*image.NRGBA)
	for row := range source.Rect.Dy() {
		for column := range source.Rect.Dx() {
			source.SetNRGBA(source.Rect.Min.X+column, source.Rect.Min.Y+row, color.NRGBA{
				R: byte(2*column + row), G: byte(column + 2*row), B: byte(column + row), A: 255,
			})
		}
	}
	for _, order := range []int{10, 13} {
		for _, affine := range expandedGridTransforms() {
			t.Run(strconv.Itoa(order)+"/"+affine.name, func(t *testing.T) {
				placed := expandedGridFixture(order, affine, false)
				grid, err := psdwarp.CompileUniformGrid(nil, placed)
				if err != nil {
					t.Fatal(err)
				}
				result, origin, err := grid.Project(nil, source, image.Rectangle{})
				if err != nil {
					t.Fatal(err)
				}
				compared := 0
				determinant := affine.xx*affine.yy - affine.xy*affine.yx
				for row := range result.Rect.Dy() {
					for column := range result.Rect.Dx() {
						dx := float64(origin.X+column) + 0.5 - affine.translationX
						dy := float64(origin.Y+row) + 0.5 - affine.translationY
						// Solve the affine system directly, without the compiled inverse or raster lattice.
						u := (affine.yy*dx - affine.xy*dy) / determinant
						v := (affine.xx*dy - affine.yx*dx) / determinant
						sourceU := reviewCanonicalSourceUV(placed, u, false)
						sourceV := reviewCanonicalSourceUV(placed, v, true)
						x, y := sourceU*73-0.5, sourceV*61-0.5
						// Restrict the color oracle to footprints wholly inside the opaque source domain.
						if x < 1.5 || x > 70.5 || y < 1.5 || y > 58.5 {
							continue
						}
						actual := result.NRGBAAt(column, row)
						expected := [4]float64{2*x + y, x + 2*y, x + y, 255}
						for component, value := range [4]byte{actual.R, actual.G, actual.B, actual.A} {
							if math.Abs(float64(value)-expected[component]) > 0.50000001 {
								t.Fatalf("pixel=(%d,%d), component=%d: %d differs from analytic %g",
									column, row, component, value, expected[component])
							}
						}
						compared++
					}
				}
				if compared < 2_000 {
					t.Fatalf("linear-color oracle covered too few interior pixels: %d", compared)
				}
			})
		}
	}
}
