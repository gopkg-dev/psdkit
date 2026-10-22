package psdwarp_test

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
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func reviewGridFixture(order int) *psdmodel.Placed {
	const width, height = 90, 78
	warp := &psdmodel.Warp{
		ClassID: "quiltWarp", Style: "custom", Rotate: "horizontal", UOrder: 4, VOrder: 4,
		DeformRows: order, DeformColumns: order, HasDeformRows: true, HasDeformColumns: true,
		Split: true, HasEnvelope: true, MeshUnits: [2]string{"#Pxl", "#Pxl"},
		Bounds: &psdmodel.WarpBounds{Left: 0, Top: 0, Right: width, Bottom: height, Pixels: true},
	}
	pieces := (order - 1) / 3
	for _, axis := range []string{"quiltSliceX", "quiltSliceY"} {
		extent := float64(width)
		if axis == "quiltSliceY" {
			extent = height
		}
		values := make([]float64, pieces+1)
		for index := range values {
			values[index] = float64(index) * extent / float64(pieces)
		}
		values[0], values[len(values)-1] = -0.6, extent+0.6
		warp.QuiltSlices = append(warp.QuiltSlices, psdmodel.WarpSlice{
			Axis: axis, Location: "envelope", ClassID: "UntF", Field: axis,
			Kind: "UnFl", Unit: "#Pxl", Prefix: uint32(len(values)), Values: values,
		})
	}
	for row := range order {
		for column := range order {
			warp.MeshPoints = append(warp.MeshPoints, psdmodel.Point{
				X: float64(column) * width / float64(order-1),
				Y: float64(row) * height / float64(order-1),
			})
		}
	}
	return &psdmodel.Placed{Width: width, Height: height, Warp: warp,
		Transform: []float64{11, -7, 101, -7, 101, 71, 11, 71}}
}

// reviewCasteljau evaluates a cubic by recursive affine interpolation rather than Bernstein weights.
func reviewCasteljau(points [4]psdmodel.Point, amount float64) psdmodel.Point {
	for remaining := 3; remaining > 0; remaining-- {
		for index := range remaining {
			points[index].X += amount * (points[index+1].X - points[index].X)
			points[index].Y += amount * (points[index+1].Y - points[index].Y)
		}
	}
	return points[0]
}

func reviewGridPoint(points []psdmodel.Point, order int, u, v float64) psdmodel.Point {
	pieces := (order - 1) / 3
	column := min(int(u*float64(pieces)), pieces-1)
	row := min(int(v*float64(pieces)), pieces-1)
	localU, localV := u*float64(pieces)-float64(column), v*float64(pieces)-float64(row)
	var rows [4]psdmodel.Point
	for offsetY := range 4 {
		var controls [4]psdmodel.Point
		for offsetX := range 4 {
			controls[offsetX] = points[(row*3+offsetY)*order+column*3+offsetX]
		}
		rows[offsetY] = reviewCasteljau(controls, localU)
	}
	return reviewCasteljau(rows, localV)
}

func reviewPlacedPoint(placed *psdmodel.Placed, u, v float64) psdmodel.Point {
	canonicalU := reviewSourceCanonical(placed, u, false)
	canonicalV := reviewSourceCanonical(placed, v, true)
	point := reviewGridPoint(placed.Warp.MeshPoints, placed.Warp.DeformColumns, canonicalU, canonicalV)
	left, top, right, bottom := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, control := range placed.Warp.MeshPoints {
		left, top = math.Min(left, control.X), math.Min(top, control.Y)
		right, bottom = math.Max(right, control.X), math.Max(bottom, control.Y)
	}
	x, y := (point.X-left)/(right-left), (point.Y-top)/(bottom-top)
	quad := placed.Transform
	return psdmodel.Point{
		X: quad[0] + x*(quad[2]-quad[0]) + y*(quad[6]-quad[0]),
		Y: quad[1] + x*(quad[3]-quad[1]) + y*(quad[7]-quad[1]),
	}
}

func reviewDistance(first, second psdmodel.Point) float64 {
	return math.Hypot(first.X-second.X, first.Y-second.Y)
}

func TestUniformGridIndependentCasteljauAndC0Seams(t *testing.T) {
	for _, order := range []int{10, 13} {
		t.Run(strconv.Itoa(order), func(t *testing.T) {
			placed := reviewGridFixture(order)
			for row := range order {
				for column := range order {
					index := row*order + column
					if row > 0 && row < order-1 && column > 0 && column < order-1 {
						placed.Warp.MeshPoints[index].Y += 0.8 * math.Sin(float64(column))
					}
					// Unequal handles preserve the surface while its derivative jumps across the first join.
					switch column {
					case 2:
						placed.Warp.MeshPoints[index].X -= 0.7
					case 4:
						placed.Warp.MeshPoints[index].X += 1.9
					}
				}
			}
			placed.Transform = []float64{11, -7, 119, 20, 103.4, 98, -4.6, 71}
			grid, err := psdwarp.CompileUniformGrid(nil, placed)
			if err != nil {
				t.Fatal(err)
			}
			parameters := []float64{0, 0.017, 0.143, 0.537, 0.861, 1}
			pieces := (order - 1) / 3
			for index := 1; index < pieces; index++ {
				for _, alongY := range []bool{false, true} {
					join := reviewCanonicalSourceUV(placed, float64(index)/float64(pieces), alongY)
					parameters = append(parameters, math.Nextafter(join, 0), join, math.Nextafter(join, 1))
				}
			}
			for _, u := range parameters {
				for _, v := range parameters {
					expected := reviewPlacedPoint(placed, u, v)
					actual, err := grid.Forward(u, v)
					if err != nil || reviewDistance(actual, expected) > 2e-10 {
						t.Fatalf("de Casteljau differs at %g,%g: %v vs %v, %v", u, v, actual, expected, err)
					}
					actualU, actualV, inside, err := grid.Inverse(nil, expected)
					if err != nil || !inside || math.Abs(actualU-u) > 2e-7 || math.Abs(actualV-v) > 2e-7 {
						t.Fatalf("inverse differs at %g,%g: %g,%g,%t,%v", u, v, actualU, actualV, inside, err)
					}
				}
			}
			join, epsilon := reviewCanonicalSourceUV(placed, 1/float64(pieces), false), 1e-6
			left := reviewPlacedPoint(placed, join-epsilon, 0.5)
			center := reviewPlacedPoint(placed, join, 0.5)
			right := reviewPlacedPoint(placed, join+epsilon, 0.5)
			leftSlope := psdmodel.Point{X: (center.X - left.X) / epsilon, Y: (center.Y - left.Y) / epsilon}
			rightSlope := psdmodel.Point{X: (right.X - center.X) / epsilon, Y: (right.Y - center.Y) / epsilon}
			if reviewDistance(leftSlope, rightSlope) < 1 {
				t.Fatal("review fixture accidentally became C1 at its first join")
			}
		})
	}
}

func reviewOverlapGrid() *psdmodel.Placed {
	placed := reviewGridFixture(13)
	const angle = 5 * math.Pi / 8
	handle := 4 * math.Tan(angle/4) / 3
	var circle [13]psdmodel.Point
	for piece := range 4 {
		start, end := float64(piece)*angle, float64(piece+1)*angle
		circle[piece*3] = psdmodel.Point{X: math.Cos(start), Y: math.Sin(start)}
		circle[piece*3+1] = psdmodel.Point{
			X: math.Cos(start) - handle*math.Sin(start), Y: math.Sin(start) + handle*math.Cos(start),
		}
		circle[piece*3+2] = psdmodel.Point{
			X: math.Cos(end) + handle*math.Sin(end), Y: math.Sin(end) - handle*math.Cos(end),
		}
		circle[piece*3+3] = psdmodel.Point{X: math.Cos(end), Y: math.Sin(end)}
	}
	for row := range 13 {
		radius := 2 - float64(row)/12
		for column := range 13 {
			placed.Warp.MeshPoints[row*13+column] = psdmodel.Point{
				X: radius * circle[column].X, Y: radius * circle[column].Y,
			}
		}
	}
	return placed
}

func TestUniformGridRejectsGlobalOverlapWithPositiveLocalJacobians(t *testing.T) {
	placed := reviewOverlapGrid()
	// An annular strip winding 450 degrees has positive local orientation, but overlaps itself.
	for piece := range 4 {
		for _, localU := range []float64{0.1, 0.3, 0.6, 0.9} {
			for _, v := range []float64{0.1, 0.4, 0.9} {
				u, epsilon := (float64(piece)+localU)/4, 1e-6
				left := reviewGridPoint(placed.Warp.MeshPoints, 13, u-epsilon, v)
				right := reviewGridPoint(placed.Warp.MeshPoints, 13, u+epsilon, v)
				top := reviewGridPoint(placed.Warp.MeshPoints, 13, u, v-epsilon)
				bottom := reviewGridPoint(placed.Warp.MeshPoints, 13, u, v+epsilon)
				jacobian := ((right.X-left.X)*(bottom.Y-top.Y) -
					(right.Y-left.Y)*(bottom.X-top.X)) / (4 * epsilon * epsilon)
				if jacobian <= 1 {
					t.Fatalf("overlap fixture has a local fold at %g,%g: %g", u, v, jacobian)
				}
			}
		}
	}
	first, last := 0.0, 1.0
	for range 64 {
		middle := (first + last) / 2
		point := reviewGridPoint(placed.Warp.MeshPoints, 13, (3+middle)/4, 0)
		if point.Y < 0 {
			first = middle
		} else {
			last = middle
		}
	}
	u := (3 + (first+last)/2) / 4
	outer := reviewGridPoint(placed.Warp.MeshPoints, 13, u, 0)
	v := 2 - 3/outer.X
	one := reviewGridPoint(placed.Warp.MeshPoints, 13, 0, 0.5)
	two := reviewGridPoint(placed.Warp.MeshPoints, 13, u, v)
	if v <= 0 || v >= 1 || reviewDistance(one, two) > 1e-12 {
		t.Fatalf("independent overlap construction failed: u=%g v=%g, %v vs %v", u, v, one, two)
	}
	grid, err := psdwarp.CompileUniformGrid(nil, placed)
	if grid != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("globally overlapping positive-Jacobian atlas returned geometry and %v", err)
	}
}

type reviewCancelContext struct {
	context.Context
	remaining int
}

func (ctx *reviewCancelContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestUniformGridOwnedGeometryBudgetAndCancellation(t *testing.T) {
	for _, order := range []int{10, 13} {
		placed := reviewGridFixture(order)
		grid, err := psdwarp.CompileUniformGrid(nil, placed)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := grid.Forward(0.271, 0.693)
		if err != nil {
			t.Fatal(err)
		}
		placed.Warp.MeshPoints[0].X = 1e8
		placed.Warp.QuiltSlices[0].Values[1] = 1e8
		placed.Transform[0] = 1e8
		actual, err := grid.Forward(0.271, 0.693)
		if err != nil || actual != expected {
			t.Fatal("compiled geometry retained mutable caller slices")
		}
		source := image.NewNRGBA(image.Rect(7, -3, 97, 75))
		for row := source.Rect.Min.Y; row < source.Rect.Max.Y; row++ {
			for column := source.Rect.Min.X; column < source.Rect.Max.X; column++ {
				source.SetNRGBA(column, row, color.NRGBA{R: byte(column), G: byte(row + 3), B: 91, A: 255})
			}
		}
		before := bytes.Clone(source.Pix)
		result, origin, err := grid.Project(nil, source, image.Rectangle{})
		if err != nil || origin != image.Pt(11, -7) || result == nil ||
			result.Rect.Size() != source.Rect.Size() || !bytes.Equal(result.Pix, source.Pix) {
			t.Fatalf("identity grid raster differs at order %d: origin=%v, %v", order, origin, err)
		}
		result.Pix[0] ^= 255
		if !bytes.Equal(source.Pix, before) {
			t.Fatal("projection changed caller-owned source pixels")
		}
		for _, options := range []psdwarp.Options{
			{MaxPixels: 1}, {MaxBytes: grid.WorkingBytes() - 1},
			{MaxBytes: grid.WorkingBytes() + int64(source.Rect.Dx()*source.Rect.Dy()*4) - 1},
		} {
			limited, _, err := grid.ProjectWithOptions(nil, source, image.Rectangle{}, options)
			if limited != nil || !errors.Is(err, psdwarp.ErrLimit) {
				t.Fatalf("budget failure returned partial pixels and %v", err)
			}
		}
		compileContext := &reviewCancelContext{Context: context.Background(), remaining: 5}
		canceledGrid, err := psdwarp.CompileUniformGrid(compileContext, reviewGridFixture(order))
		if canceledGrid != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-compile cancellation returned geometry and %v", err)
		}
		projectContext := &reviewCancelContext{Context: context.Background(), remaining: 5}
		canceledImage, _, err := grid.Project(projectContext, source, image.Rectangle{})
		if canceledImage != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-projection cancellation returned pixels and %v", err)
		}
	}
}

func TestUniformGridRetainsNativeProjectivePlacementGate(t *testing.T) {
	for _, order := range []int{10, 13} {
		placed := reviewGridFixture(order)
		placed.NonAffineTransform = []float64{11, -7, 101, -6, 99, 71, 11, 69}
		grid, err := psdwarp.CompileUniformGrid(nil, placed)
		if grid != nil || !errors.Is(err, psdwarp.ErrUnsupported) {
			t.Fatalf("unverified native projective composition returned geometry and %v", err)
		}
	}
}

func TestUniformGridRejectsRasterBudgetAdditionOverflow(t *testing.T) {
	if strconv.IntSize != 64 {
		t.Skip("The near-MaxInt64 arithmetic case needs a 64-bit int.")
	}
	const width, height = 1_000_000_000, 2_305_843_009
	limits := psdwarp.Options{MaxPixels: math.MaxInt64, MaxBytes: math.MaxInt64}
	source := image.NewNRGBA(image.Rect(0, 0, 90, 78))
	for _, supersampled := range []bool{false, true} {
		placed := reviewGridFixture(10)
		spanX, spanY := float64(width)-0.5, float64(height)-0.5
		left, top := 0.25, 0.25
		if supersampled {
			// The full-source edge interval includes one 0.6px guard before its interior slice.
			factorX := 7.0 / 90 * (90 + 1.2) / (90 + 3*0.6)
			factorY := 7.0 / 78 * (78 + 1.2) / (78 + 3*0.6)
			spanX = (float64(width) - 2.5) / (1 + factorX)
			spanY = (float64(height) - 2.5) / (1 + factorY)
			left += factorX * spanX / 2
			top += factorY * spanY / 2
		}
		placed.Transform = []float64{
			left, top, left + spanX, top, left + spanX, top + spanY, left, top + spanY,
		}
		grid, err := psdwarp.CompileUniformGrid(nil, placed)
		if err != nil {
			t.Fatal(err)
		}
		if supersampled {
			// Preflight the public piecewise-affine surface before any near-MaxInt64 raster call.
			bounds := reviewExtendedAffineBounds(t, grid, source.Rect.Size())
			if bounds.Dx() != width || bounds.Dy() != height {
				t.Fatalf("overflow fixture no longer has its intended output: %v", bounds)
			}
			outputBytes := int64(bounds.Dx()) * int64(bounds.Dy()) * 4
			available := int64(math.MaxInt64) - grid.WorkingBytes() - outputBytes
			const cellBytes = int64(4096) * 4096 * 160
			if available <= 0 || available >= cellBytes {
				t.Fatalf("fixture must exhaust remaining budget before allocating cells: %d", available)
			}
		}
		var result *image.NRGBA
		if supersampled {
			result, _, err = grid.ProjectSupersampled(nil, source, image.Rectangle{}, limits)
		} else {
			result, _, err = grid.ProjectWithOptions(nil, source, image.Rectangle{}, limits)
		}
		if result != nil || !errors.Is(err, psdwarp.ErrLimit) {
			t.Fatalf("near-MaxInt64 output plus raster scratch bypassed the budget: %v", err)
		}
	}
}

func reviewExtendedAffineBounds(t *testing.T, grid *psdwarp.UniformGrid, size image.Point) image.Rectangle {
	t.Helper()
	point := func(u, v float64) psdmodel.Point {
		result, err := grid.Forward(u, v)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	first, lastX, lastY := point(0, 0), point(1, 0), point(0, 1)
	lowX, highX := point(0.125, 0), point(0.875, 0)
	lowY, highY := point(0, 0.125), point(0, 0.875)
	left := first.X - 3.5/float64(size.X)*(lowX.X-first.X)/0.125
	right := lastX.X + 3.5/float64(size.X)*(lastX.X-highX.X)/0.125
	top := first.Y - 3.5/float64(size.Y)*(lowY.Y-first.Y)/0.125
	bottom := lastY.Y + 3.5/float64(size.Y)*(lastY.Y-highY.Y)/0.125
	return image.Rect(int(math.Floor(left))-1, int(math.Floor(top))-1,
		int(math.Ceil(right))+1, int(math.Ceil(bottom))+1)
}
