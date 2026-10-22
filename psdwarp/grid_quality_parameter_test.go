package psdwarp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"strconv"
	"testing"
)

func qualityParameterGrid(t *testing.T, order int, guard float64) *UniformGrid {
	t.Helper()
	placed := gridQualityPlaced(order)
	for axis := range placed.Warp.QuiltSlices {
		slice := &placed.Warp.QuiltSlices[axis]
		slice.Values[0] -= guard
		slice.Values[len(slice.Values)-1] += guard
	}
	grid, err := CompileUniformGrid(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	return grid
}

func TestUniformGridQualityExtendedUsesNormalizedSourceKnots(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, guard := range []float64{0, .6} {
			t.Run(strconv.Itoa(order)+"/"+strconv.FormatFloat(guard, 'f', 1, 64), func(t *testing.T) {
				grid := qualityParameterGrid(t, order, guard)
				for _, u := range []float64{-.21, 0, .14, .37, .79, 1, 1.21} {
					for _, v := range []float64{-.17, 0, .21, .53, .88, 1, 1.17} {
						column, localU := qualityParameterIndependent(grid.knots[0], grid.count, u)
						row, localV := qualityParameterIndependent(grid.knots[1], grid.count, v)
						x, y := (float64(column)+localU)/float64(grid.count), (float64(row)+localV)/float64(grid.count)
						expectedX, expectedY := 1.125+16*x, -1.375+12*y
						point, err := grid.forwardExtended(u, v)
						if err != nil || math.Hypot(point.X-expectedX, point.Y-expectedY) > 2e-12 {
							t.Fatalf("extended source knots at %g,%g: %+v expected %g,%g: %v",
								u, v, point, expectedX, expectedY, err)
						}
					}
				}
			})
		}
	}
}

func qualityParameterIndependent(knots [5]float64, count int, coordinate float64) (int, float64) {
	piece := 0
	for piece < count-1 && coordinate >= knots[piece+1] {
		piece++
	}
	return piece, (coordinate - knots[piece]) / (knots[piece+1] - knots[piece])
}

func TestUniformGridQualityAxisPinsEveryPieceBoundary(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, guard := range []float64{0, .6} {
			grid := qualityParameterGrid(t, order, guard)
			for axis := range 2 {
				for _, steps := range []int{grid.count, 17, 32} {
					margin := 3.5 / float64([]int{16, 12}[axis])
					previous := qualityAxis(&grid.knots[axis], grid.count, 0, steps, margin)
					if previous != -margin {
						t.Fatal("outside lower margin moved")
					}
					var seen [5]bool
					for index := 1; index <= steps; index++ {
						coordinate := qualityAxis(&grid.knots[axis], grid.count, index, steps, margin)
						if coordinate <= previous {
							t.Fatal("quality axis is not strictly monotone")
						}
						for seam := 1; seam < grid.count; seam++ {
							knot := grid.knots[axis][seam]
							if coordinate == knot {
								seen[seam] = true
							}
							if previous < knot && coordinate > knot {
								t.Fatal("quality cell crosses a C0 join")
							}
						}
						previous = coordinate
					}
					if previous != 1+margin {
						t.Fatal("outside upper margin moved")
					}
					for seam := 1; seam < grid.count; seam++ {
						if !seen[seam] {
							t.Fatal("source seam is not a cell edge")
						}
					}
				}
			}
		}
	}
}

func TestUniformGridQualityExtendedControlsUseSourceMargins(t *testing.T) {
	grid := qualityParameterGrid(t, 13, .6)
	size := image.Pt(16, 12)
	for _, position := range [][2]int{{0, 0}, {0, 3}, {3, 0}, {3, 3}} {
		row, column := position[0], position[1]
		lowU, highU, lowV, highV := 0.0, 1.0, 0.0, 1.0
		if column == 0 {
			lowU = -3.5 / 16 / (grid.knots[0][1] - grid.knots[0][0])
		}
		if column == 3 {
			highU += 3.5 / 16 / (grid.knots[0][4] - grid.knots[0][3])
		}
		if row == 0 {
			lowV = -3.5 / 12 / (grid.knots[1][1] - grid.knots[1][0])
		}
		if row == 3 {
			highV += 3.5 / 12 / (grid.knots[1][4] - grid.knots[1][3])
		}
		extended := grid.qualityExtendedControls(row, column, size)
		for _, u := range []float64{0, .19, .67, 1} {
			for _, v := range []float64{0, .31, .78, 1} {
				point := gridQualityCasteljauSurface(extended, u, v)
				expected := gridQualityCasteljauSurface(grid.pieces[row*grid.count+column].controls,
					lowU+u*(highU-lowU), lowV+v*(highV-lowV))
				if math.Hypot(point.X-expected.X, point.Y-expected.Y) > 2e-12 {
					t.Fatal("outside control reparameterization used a uniform count instead of source knots")
				}
			}
		}
	}
}

func TestUniformGridQualityGuardedCropBudgetAndCancellation(t *testing.T) {
	grid := qualityParameterGrid(t, 13, .6)
	before := grid.knots
	source := gridQualitySource()
	full, origin, err := grid.ProjectSupersampled(nil, source, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.Rect(3, 0, 14, 10)
	crop, cropOrigin, err := grid.ProjectSupersampled(nil, source, canvas)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < crop.Rect.Dy(); y++ {
		for x := 0; x < crop.Rect.Dx(); x++ {
			if crop.NRGBAAt(x, y) != full.NRGBAAt(x+cropOrigin.X-origin.X, y+cropOrigin.Y-origin.Y) {
				t.Fatal("quality source knots changed absolute crop phase")
			}
		}
	}
	bounds, nx, ny, err := grid.qualityGeometry(context.Background(), source.Rect.Size(), image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := grid.qualityRaster(context.Background(), source.Rect.Size(), bounds, nx, ny)
	if err != nil {
		t.Fatal(err)
	}
	required := grid.WorkingBytes() + int64(bounds.Dx())*int64(bounds.Dy())*4 + int64(nx*ny)*160 +
		int64(bounds.Dy()+1)*16 + 256 + int64(len(plan.indices))*4
	for _, budget := range []int64{required - 1, required} {
		pixels, _, err := grid.ProjectSupersampled(nil, source, image.Rectangle{}, Options{MaxBytes: budget})
		if budget < required {
			if pixels != nil || !errors.Is(err, ErrLimit) {
				t.Fatal("undersized exact budget accepted")
			}
		} else if err != nil || pixels == nil || !bytes.Equal(pixels.Pix, full.Pix) {
			t.Fatal("complete budget changed pixels")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pixels, _, err := grid.ProjectSupersampled(ctx, source, image.Rectangle{}); pixels != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatal("canceled guarded source projection returned pixels")
	}
	probe := &gridQualityCancelContext{Context: context.Background(), limit: math.MaxInt}
	if _, _, err := grid.ProjectSupersampled(probe, source, image.Rectangle{}); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{probe.calls / 2, probe.calls - 2} {
		operation := &gridQualityCancelContext{Context: context.Background(), limit: limit}
		if pixels, _, err := grid.ProjectSupersampled(operation, source, image.Rectangle{}); pixels != nil ||
			!errors.Is(err, context.Canceled) {
			t.Fatal("mid/late cancellation returned guarded partial pixels")
		}
	}
	if grid.knots != before {
		t.Fatal("quality operation modified immutable source knots")
	}
}

func TestUniformGridQualityGuardedNearOverflowPreflightOnly(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("near-MaxInt64 preflight needs 64-bit ints")
	}
	const width, height = 1_000_000_000, 2_305_843_009
	placed := gridQualityPlaced(10)
	placed.Width, placed.Height = 90, 78
	placed.Warp.Bounds.Right, placed.Warp.Bounds.Bottom = 90, 78
	for index := range placed.Warp.MeshPoints {
		placed.Warp.MeshPoints[index].X *= 90.0 / 16
		placed.Warp.MeshPoints[index].Y *= 78.0 / 12
	}
	for axis := range placed.Warp.QuiltSlices {
		extent := 90.0
		if axis == 1 {
			extent = 78
		}
		values := placed.Warp.QuiltSlices[axis].Values
		for index := range values {
			values[index] = float64(index) * extent / 3
		}
		values[0], values[3] = -.6, extent+.6
	}
	factorX := 7.0 / 90 * (90 + 1.2) / (90 + 1.8)
	factorY := 7.0 / 78 * (78 + 1.2) / (78 + 1.8)
	spanX, spanY := (float64(width)-2.5)/(1+factorX), (float64(height)-2.5)/(1+factorY)
	left, top := .25+factorX*spanX/2, .25+factorY*spanY/2
	placed.Transform = []float64{left, top, left + spanX, top, left + spanX, top + spanY, left, top + spanY}
	grid, err := CompileUniformGrid(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	bounds, nx, ny, err := grid.qualityGeometry(context.Background(), image.Pt(90, 78), image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	if bounds.Dx() != width || bounds.Dy() != height || nx != 4096 || ny != 4096 {
		t.Fatalf("near-overflow geometry changed: %v cells %d,%d", bounds, nx, ny)
	}
	output := int64(bounds.Dx()) * int64(bounds.Dy()) * 4
	remaining := int64(math.MaxInt64) - grid.WorkingBytes() - output
	cells := int64(nx) * int64(ny) * 160
	if remaining < 0 || remaining >= cells {
		t.Fatal("fixture does not reject before raster allocations")
	}
	t.Logf("preflight only: bounds=%v cells=%d,%d output=%d remaining=%d cellCost=%d",
		bounds, nx, ny, output, remaining, cells)
}
