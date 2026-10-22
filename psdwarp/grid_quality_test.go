package psdwarp

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestUniformGridSupersampledMatchesAnalyticAffineN4(t *testing.T) {
	for _, order := range []int{10, 13} {
		t.Run(strconv.Itoa(order), func(t *testing.T) {
			placed := gridQualityPlaced(order)
			placed.Transform = []float64{-3.375, 2.625, 16.125, 4.875, 13.125, 20.625, -6.375, 18.375}
			grid, err := CompileUniformGrid(nil, placed)
			if err != nil {
				t.Fatal(err)
			}
			source := gridQualitySource()
			before := append([]byte(nil), source.Pix...)
			result, offset, err := grid.ProjectSupersampled(nil, source, image.Rectangle{})
			if err != nil {
				t.Fatal(err)
			}
			quad := placed.Transform
			ax, ay, bx, by := quad[2]-quad[0], quad[3]-quad[1], quad[6]-quad[0], quad[7]-quad[1]
			determinant := ax*by - ay*bx
			marginU, marginV := 3.5/float64(source.Rect.Dx()), 3.5/float64(source.Rect.Dy())
			for y := range result.Rect.Dy() {
				for x := range result.Rect.Dx() {
					var sums [4]float64
					count := 0
					for subY := range 4 {
						for subX := range 4 {
							dx := float64(x+offset.X) + (float64(subX)+0.5)/4 - quad[0]
							dy := float64(y+offset.Y) + (float64(subY)+0.5)/4 - quad[1]
							u, v := (dx*by-dy*bx)/determinant, (ay*-dx+ax*dy)/determinant
							if u < -marginU-1e-10 || u > 1+marginU+1e-10 ||
								v < -marginV-1e-10 || v > 1+marginV+1e-10 {
								continue
							}
							sample := gridOracleSample(source, u, v, false)
							for component := range sums {
								sums[component] += sample[component]
							}
							count++
						}
					}
					index := y*result.Stride + x*4
					for component := range sums {
						expected := 0.0
						if count > 0 {
							expected = math.RoundToEven(max(0, min(255, sums[component]/float64(count))))
						}
						if math.Abs(float64(result.Pix[index+component])-expected) > 1 {
							t.Fatalf("pixel=%v channel=%d got=%d want=%v", image.Pt(x, y).Add(offset),
								component, result.Pix[index+component], expected)
						}
					}
				}
			}
			if !bytes.Equal(source.Pix, before) {
				t.Fatal("supersampling modified the source")
			}
		})
	}
}

func TestUniformGridSupersampledCropKeepsGlobalPhaseAndC0Coverage(t *testing.T) {
	placed := gridQualityPlaced(13)
	for row := range 13 {
		placed.Warp.MeshPoints[row*13+2].X -= 0.35
		placed.Warp.MeshPoints[row*13+4].X += 0.55
	}
	grid, err := CompileUniformGrid(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	source := gridQualitySource()
	full, fullOffset, err := grid.ProjectSupersampled(nil, source, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.Rect(3, 0, 14, 12)
	crop, cropOffset, err := grid.ProjectSupersampled(nil, source, canvas)
	if err != nil {
		t.Fatal(err)
	}
	for y := range crop.Rect.Dy() {
		for x := range crop.Rect.Dx() {
			point := image.Pt(x, y).Add(cropOffset).Sub(fullOffset)
			if got, expected := crop.NRGBAAt(x, y), full.NRGBAAt(point.X, point.Y); got != expected {
				t.Fatalf("crop phase differs at %v: %v != %v", image.Pt(x, y).Add(cropOffset), got, expected)
			}
		}
	}
	opaque := image.NewNRGBA(source.Rect)
	for y := opaque.Rect.Min.Y; y < opaque.Rect.Max.Y; y++ {
		for x := opaque.Rect.Min.X; x < opaque.Rect.Max.X; x++ {
			opaque.SetNRGBA(x, y, color.NRGBA{R: 210, G: 120, B: 80, A: 255})
		}
	}
	result, offset, err := grid.ProjectSupersampled(nil, opaque, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	for y := 3; y < 10; y++ {
		for x := 4; x < 15; x++ {
			point := image.Pt(x, y).Sub(offset)
			if pixel := result.NRGBAAt(point.X, point.Y); pixel != (color.NRGBA{R: 210, G: 120, B: 80, A: 255}) {
				t.Fatalf("internal piece seam became visible at %v: %v", image.Pt(x, y), pixel)
			}
		}
	}
}

func TestUniformGridSupersampledReparameterizationBoundsAndExtrapolation(t *testing.T) {
	placed := gridQualityPlaced(10)
	for index := range placed.Warp.MeshPoints {
		point := &placed.Warp.MeshPoints[index]
		point.Y += 0.12 * math.Sin(float64(index%10))
	}
	grid, err := CompileUniformGrid(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	for _, interval := range [][2]float64{{-0.35, 1}, {0, 1.4}, {-0.2, 1.3}} {
		controls := grid.pieces[0].controls
		transformed := gridReparameterize(controls, interval[0], interval[1], -0.31, 1.19)
		for row := range 13 {
			for column := range 13 {
				u, v := float64(column)/12, float64(row)/12
				got := gridQualityCasteljauSurface(transformed, u, v)
				expected := gridQualityCasteljauSurface(controls,
					interval[0]+u*(interval[1]-interval[0]), -0.31+v*1.5)
				if math.Hypot(got.X-expected.X, got.Y-expected.Y) > 1e-12 {
					t.Fatalf("extended reparameterization changed the cubic: %v != %v", got, expected)
				}
			}
		}
	}
	source := gridQualitySource()
	bounds, _, _, err := grid.qualityGeometry(context.Background(), source.Rect.Size(), image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	marginU, marginV := 3.5/float64(source.Rect.Dx()), 3.5/float64(source.Rect.Dy())
	for row := range 129 {
		for column := range 129 {
			u := -marginU + (1+2*marginU)*float64(column)/128
			v := -marginV + (1+2*marginV)*float64(row)/128
			point, err := grid.forwardExtended(u, v)
			if err != nil || point.X < float64(bounds.Min.X) || point.Y < float64(bounds.Min.Y) ||
				point.X > float64(bounds.Max.X) || point.Y > float64(bounds.Max.Y) {
				t.Fatalf("extended surface escaped conservative bounds at %v,%v: %v, %v", u, v, point, err)
			}
		}
	}
}

func TestUniformGridSupersampledBudgetCancellationAndEmptyCanvas(t *testing.T) {
	grid, err := CompileUniformGrid(nil, gridQualityPlaced(10))
	if err != nil {
		t.Fatal(err)
	}
	source := gridQualitySource()
	bounds, nx, ny, err := grid.qualityGeometry(context.Background(), source.Rect.Size(), image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := grid.qualityRaster(context.Background(), source.Rect.Size(), bounds, nx, ny)
	if err != nil {
		t.Fatal(err)
	}
	pixels := int64(bounds.Dx()) * int64(bounds.Dy())
	required := grid.WorkingBytes() + pixels*4 + int64(nx*ny)*160 + int64(bounds.Dy()+1)*16 +
		256 + int64(len(plan.indices))*4
	for _, limits := range []Options{{MaxPixels: pixels - 1}, {MaxBytes: required - 1}} {
		result, _, err := grid.ProjectSupersampled(nil, source, image.Rectangle{}, limits)
		if result != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("incomplete resource budget returned pixels: image=%v err=%v", result != nil, err)
		}
	}
	result, _, err := grid.ProjectSupersampled(nil, source, image.Rectangle{}, Options{MaxBytes: required})
	if err != nil || result == nil {
		t.Fatalf("complete budget rejected: %v", err)
	}
	probe := &gridQualityCancelContext{Context: context.Background(), limit: math.MaxInt}
	if _, _, err := grid.ProjectSupersampled(probe, source, image.Rectangle{}); err != nil {
		t.Fatal(err)
	}
	for _, limit := range []int{1, probe.calls / 3, probe.calls - 2} {
		ctx := &gridQualityCancelContext{Context: context.Background(), limit: limit}
		result, _, err := grid.ProjectSupersampled(ctx, source, image.Rectangle{})
		if result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel at Err call %d returned partial pixels: image=%v err=%v", limit, result != nil, err)
		}
	}
	canvas := image.Rect(900, -1200, 901, -1199)
	result, offset, err := grid.ProjectSupersampled(nil, source, canvas,
		Options{MaxBytes: grid.WorkingBytes() + 4, MaxPixels: 1})
	if err != nil || result == nil || result.Rect != image.Rect(0, 0, 1, 1) || offset != canvas.Min ||
		!bytes.Equal(result.Pix, []byte{0, 0, 0, 0}) {
		t.Fatalf("empty clipped projection = %v %v %v", result, offset, err)
	}
	for _, badSource := range []*image.NRGBA{nil, {}, {Rect: image.Rect(0, 0, 2, 2), Stride: 8, Pix: make([]byte, 15)}} {
		if result, _, err := grid.ProjectSupersampled(nil, badSource, canvas); result != nil ||
			!errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed source accepted: %v", err)
		}
	}
}

func TestUniformGridSupersampledOptionsRemainOperationLocal(t *testing.T) {
	grid, err := CompileUniformGrid(nil, gridQualityPlaced(13), Options{MaxPixels: 1})
	if err != nil {
		t.Fatal(err)
	}
	source := gridQualitySource()
	initialReference := grid.reference
	for _, malformed := range []*UniformGrid{nil, {}} {
		result, _, err := malformed.ProjectSupersampled(nil, source, image.Rectangle{}, Options{})
		if result != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("uninitialized grid accepted: %v", err)
		}
	}
	if result, _, err := grid.ProjectSupersampled(nil, source, image.Rectangle{}); result != nil ||
		!errors.Is(err, ErrLimit) {
		t.Fatalf("compiled limit did not apply: %v", err)
	}
	if result, _, err := grid.ProjectSupersampled(nil, source, image.Rectangle{}, Options{}); result == nil ||
		err != nil {
		t.Fatalf("operation-local limits were not accepted: %v", err)
	}
	if grid.options.MaxPixels != 1 {
		t.Fatal("operation modified the immutable compiled grid")
	}
	if grid.reference != initialReference {
		t.Fatal("operation modified the compiled grid's certificate reference")
	}
	result, _, err := grid.ProjectSupersampled(nil, source, image.Rectangle{}, Options{}, Options{})
	if result != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("multiple option structs accepted: %v", err)
	}
	result, _, err = grid.ProjectSupersampled(nil, source, image.Rectangle{}, Options{MaxBytes: -1})
	if result != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("negative limit accepted: %v", err)
	}
}

func TestUniformGridSupersampledRejectsUnsafeExterior(t *testing.T) {
	placed := gridQualityPlaced(10)
	for row := range 10 {
		for column := range 10 {
			piece := min(column/3, 2)
			low, high := float64(piece)/3, float64(piece+1)/3
			f := func(value float64) float64 { return value + value*value/2 }
			values := [4]float64{f(low), f(low) + (high-low)*(1+low)/3,
				f(high) - (high-low)*(1+high)/3, f(high)}
			placed.Warp.MeshPoints[row*10+column].X = 16 * values[column-piece*3]
		}
	}
	grid, err := CompileUniformGrid(nil, placed)
	if err != nil {
		t.Fatalf("source domain should remain certifiable: %v", err)
	}
	source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	source.SetNRGBA(0, 0, color.NRGBA{R: 210, A: 255})
	if result, _, err := grid.ProjectSupersampled(nil, source, image.Rectangle{}); result != nil ||
		!errors.Is(err, ErrUnsupported) {
		t.Fatalf("folded extension returned pixels: image=%v err=%v", result != nil, err)
	}
}

func TestUniformGridSupersampledCellRequiresCommonGlobalPMatrix(t *testing.T) {
	grid, err := CompileUniformGrid(nil, gridQualityPlaced(10))
	if err != nil {
		t.Fatal(err)
	}
	cell := rasterCell{du: 0.1, dv: 0.1}
	for index, parameter := range [4][2]float64{{0, 0}, {0.1, 0}, {0.1, 0.1}, {0, 0.1}} {
		cell.points[index], err = grid.forwardExtended(parameter[0], parameter[1])
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := grid.qualityCellCertificate(&cell); err != nil {
		t.Fatal(err)
	}
	// Convexity alone permits the reflected rectangle; it does not prove one global lattice reference.
	cell.points[0], cell.points[1] = cell.points[1], cell.points[0]
	cell.points[2], cell.points[3] = cell.points[3], cell.points[2]
	if err := grid.qualityCellCertificate(&cell); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("reflected cell accepted: %v", err)
	}
	if strconv.IntSize < 64 {
		return
	}
	operation := *grid
	operation.options.MaxBytes = math.MaxInt64
	oversizedHeight := int64(2_305_843_009)
	if result, err := operation.qualityRaster(context.Background(), image.Pt(16, 12),
		image.Rect(0, 0, 1_000_000_000, int(oversizedHeight)), 4096, 4096); result != nil ||
		!errors.Is(err, ErrLimit) {
		t.Fatalf("near-MaxInt64 summed budget accepted: %v", err)
	}
}

type gridQualityCancelContext struct {
	context.Context
	limit int
	calls int
}

func (ctx *gridQualityCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.limit {
		return context.Canceled
	}
	return nil
}

func gridQualityPlaced(order int) *psdmodel.Placed {
	const width, height = 16, 12
	warp := &psdmodel.Warp{ClassID: "quiltWarp", Style: "custom", Rotate: "horizontal", UOrder: 4, VOrder: 4,
		DeformRows: order, DeformColumns: order, HasDeformRows: true, HasDeformColumns: true,
		Split: true, HasEnvelope: true, MeshUnits: [2]string{"#Pxl", "#Pxl"},
		Bounds: &psdmodel.WarpBounds{Right: width, Bottom: height, Pixels: true}}
	count := (order - 1) / 3
	for _, axis := range []string{"quiltSliceX", "quiltSliceY"} {
		extent := float64(width)
		if axis == "quiltSliceY" {
			extent = height
		}
		values := make([]float64, count+1)
		for index := range values {
			values[index] = float64(index) * extent / float64(count)
		}
		warp.QuiltSlices = append(warp.QuiltSlices, psdmodel.WarpSlice{Axis: axis, Location: "envelope",
			ClassID: "UntF", Field: axis, Kind: "UnFl", Unit: "#Pxl", Prefix: uint32(len(values)), Values: values})
	}
	for row := range order {
		for column := range order {
			warp.MeshPoints = append(warp.MeshPoints, psdmodel.Point{X: float64(column) * width / float64(order-1),
				Y: float64(row) * height / float64(order-1)})
		}
	}
	return &psdmodel.Placed{Width: width, Height: height, Warp: warp,
		Transform: []float64{1.125, -1.375, 17.125, -1.375, 17.125, 10.625, 1.125, 10.625}}
}

func gridQualitySource() *image.NRGBA {
	backing := image.NewNRGBA(image.Rect(-21, 4, 12, 27))
	source := backing.SubImage(image.Rect(-11, 9, 5, 21)).(*image.NRGBA)
	for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
		for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: byte(x*31 + y*17), G: byte(x*13 - y*19),
				B: byte(x*7 + y*23), A: byte(x*47 + y*29)})
		}
	}
	return source
}

func gridQualityCasteljauSurface(controls [16]psdmodel.Point, u, v float64) psdmodel.Point {
	evaluate := func(curve [4]psdmodel.Point, amount float64) psdmodel.Point {
		for remaining := 3; remaining > 0; remaining-- {
			for index := range remaining {
				curve[index].X += amount * (curve[index+1].X - curve[index].X)
				curve[index].Y += amount * (curve[index+1].Y - curve[index].Y)
			}
		}
		return curve[0]
	}
	var rows [4]psdmodel.Point
	for row := range 4 {
		var curve [4]psdmodel.Point
		for column := range 4 {
			curve[column] = controls[row*4+column]
		}
		rows[row] = evaluate(curve, u)
	}
	return evaluate(rows, v)
}
