package psdrender_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
)

type placedReferenceMatrix [9]float64

func (matrix placedReferenceMatrix) mapPoint(x, y float64) (float64, float64) {
	denominator := matrix[6]*x + matrix[7]*y + matrix[8]
	return (matrix[0]*x + matrix[1]*y + matrix[2]) / denominator,
		(matrix[3]*x + matrix[4]*y + matrix[5]) / denominator
}

// inverse uses the explicit adjugate, independent of the production normalized Gauss-Jordan solver.
func (matrix placedReferenceMatrix) inverse() placedReferenceMatrix {
	a, b, c := matrix[0], matrix[1], matrix[2]
	d, e, f := matrix[3], matrix[4], matrix[5]
	g, h, i := matrix[6], matrix[7], matrix[8]
	determinant := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	return placedReferenceMatrix{(e*i - f*h) / determinant, (c*h - b*i) / determinant,
		(b*f - c*e) / determinant, (f*g - d*i) / determinant, (a*i - c*g) / determinant,
		(c*d - a*f) / determinant, (d*h - e*g) / determinant, (b*g - a*h) / determinant,
		(a*e - b*d) / determinant}
}

func approximatePlacedFixture(matrix placedReferenceMatrix) (*psdmodel.Layer, *image.NRGBA) {
	backing := image.NewNRGBA(image.Rect(-3, -2, 9, 8))
	source := backing.SubImage(image.Rect(-1, 0, 7, 6)).(*image.NRGBA)
	for y := 0; y < source.Rect.Dy(); y++ {
		for x := 0; x < source.Rect.Dx(); x++ {
			pixel := color.NRGBA{R: byte((x*47 + y*13 + 29) % 256), G: byte((x*17 + y*61 + 113) % 256),
				B: byte((x*73 + y*31 + 7) % 256)}
			if x > 0 && x < source.Rect.Dx()-1 && y > 0 && y < source.Rect.Dy()-1 {
				pixel.A = byte(29 + (x*37+y*43)%227)
			}
			source.SetNRGBA(source.Rect.Min.X+x, source.Rect.Min.Y+y, pixel)
		}
	}
	var transform []float64
	for _, point := range [4][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}} {
		x, y := matrix.mapPoint(point[0], point[1])
		transform = append(transform, x, y)
	}
	return &psdmodel.Layer{Placed: &psdmodel.Placed{Width: source.Rect.Dx(), Height: source.Rect.Dy(),
		Transform: transform}}, source
}

func placedReferenceBilinear(source *image.NRGBA, u, v float64) color.NRGBA {
	if math.IsNaN(u) || math.IsNaN(v) || math.IsInf(u, 0) || math.IsInf(v, 0) || u < 0 || v < 0 || u > 1 || v > 1 {
		return color.NRGBA{}
	}
	x, y := u*float64(source.Rect.Dx())-.5, v*float64(source.Rect.Dy())-.5
	xFloor, yFloor := math.Floor(x), math.Floor(y)
	fx, fy := x-xFloor, y-yFloor
	var channels [3]float64
	alpha := float64(0)
	for row := range 2 {
		for column := range 2 {
			sx := int(xFloor) + column
			sy := int(yFloor) + row
			if sx < 0 {
				sx = 0
			} else if sx >= source.Rect.Dx() {
				sx = source.Rect.Dx() - 1
			}
			if sy < 0 {
				sy = 0
			} else if sy >= source.Rect.Dy() {
				sy = source.Rect.Dy() - 1
			}
			wx, wy := 1-fx, 1-fy
			if column == 1 {
				wx = fx
			}
			if row == 1 {
				wy = fy
			}
			pixel := source.NRGBAAt(source.Rect.Min.X+sx, source.Rect.Min.Y+sy)
			weight := wx * wy * float64(pixel.A) / 255
			alpha += weight
			for channel, value := range []byte{pixel.R, pixel.G, pixel.B} {
				channels[channel] += float64(value) * weight
			}
		}
	}
	if alpha == 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{R: byte(math.RoundToEven(channels[0] / alpha)),
		G: byte(math.RoundToEven(channels[1] / alpha)), B: byte(math.RoundToEven(channels[2] / alpha)),
		A: byte(math.RoundToEven(alpha * 255))}
}

func checkPlacedReference(
	t *testing.T, pixels *image.NRGBA, origin image.Point, source *image.NRGBA, matrix placedReferenceMatrix,
	skip func(int, int) bool,
) {
	t.Helper()
	inverse := matrix.inverse()
	for y := 0; y < pixels.Rect.Dy(); y++ {
		for x := 0; x < pixels.Rect.Dx(); x++ {
			u, v := inverse.mapPoint(float64(origin.X+x)+.5, float64(origin.Y+y)+.5)
			expected := placedReferenceBilinear(source, u, v)
			if skip != nil && skip(x, y) {
				expected = color.NRGBA{}
			}
			if actual := pixels.NRGBAAt(x, y); actual != expected {
				t.Fatalf("independent canvas sample %d,%d: %v != %v", origin.X+x, origin.Y+y, actual, expected)
			}
		}
	}
}

func TestPlacedApproximateTransparentBorderIndependentAffineAndPerspective(t *testing.T) {
	for _, matrix := range []placedReferenceMatrix{{9, 2, -4, 1, 7, -3, 0, 0, 1},
		{9, 2, -4, 1, 7, -3, .18, -.12, 1}} {
		layer, source := approximatePlacedFixture(matrix)
		canvas := image.Rect(-12, -12, 12, 12)
		plan, err := psdrender.CompileProjectionRegion(layer, canvas, canvas)
		if err != nil {
			t.Fatal(err)
		}
		strict, _, err := psdrender.ProjectPlacedContentsContext(nil, layer, source, plan)
		if strict != nil || !errors.Is(err, psdrender.ErrUnsupportedCrop) {
			t.Fatalf("the strict transparent-border projection contract changed: %v", err)
		}
		pixels, origin, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, plan,
			psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: true})
		if err != nil || origin != image.Pt(plan.Left, plan.Top) || origin.X >= 0 || origin.Y >= 0 {
			t.Fatalf("complete prototype UV or negative document origin changed: %v, %v", origin, err)
		}
		checkPlacedReference(t, pixels, origin, source, matrix, nil)
	}
}

func TestPlacedApproximateTransparentBorderRegionsKeepOriginalCanvasUV(t *testing.T) {
	matrix := placedReferenceMatrix{9, 2, -4, 1, 7, -3, .18, -.12, 1}
	layer, source := approximatePlacedFixture(matrix)
	canvas := image.Rect(-12, -12, 12, 12)
	fullPlan, err := psdrender.CompileProjectionRegion(layer, canvas, canvas)
	if err != nil {
		t.Fatal(err)
	}
	options := psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: true}
	full, fullOrigin, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, fullPlan, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range []image.Rectangle{image.Rect(-3, -3, 3, 2), image.Rect(-1, 0, 7, 5),
		image.Rect(-5, -4, 0, 1)} {
		plan, err := psdrender.CompileProjectionRegion(layer, canvas, window)
		if err != nil {
			t.Fatal(err)
		}
		pixels, origin, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, plan, options)
		if err != nil {
			t.Fatal(err)
		}
		checkPlacedReference(t, pixels, origin, source, matrix, nil)
		for y := 0; y < pixels.Rect.Dy(); y++ {
			for x := 0; x < pixels.Rect.Dx(); x++ {
				if pixels.NRGBAAt(x, y) != full.NRGBAAt(x+origin.X-fullOrigin.X, y+origin.Y-fullOrigin.Y) {
					t.Fatal("output cropping remapped or trimmed the prototype canvas")
				}
			}
		}
	}
}

func TestPlacedApproximateTransparentBorderRetainsNonAffineAndMaskFrame(t *testing.T) {
	matrix := placedReferenceMatrix{9, 2, -4, 1, 7, -3, 0, 0, 1}
	layer, source := approximatePlacedFixture(matrix)
	layer.Placed.NonAffineTransform = append([]float64(nil), layer.Placed.Transform...)
	layer.Placed.Transform = []float64{0, 0, 8, 0, 8, 6, 0, 6}
	layer.Placed.Warp = &psdmodel.Warp{Style: "none", ClassID: "warp"}
	originalPlacement, originalWarp := layer.Placed, layer.Placed.Warp
	canvas := image.Rect(-12, -12, 12, 12)
	plan, err := psdrender.CompileProjectionRegion(layer, canvas, canvas)
	if err != nil {
		t.Fatal(err)
	}
	maskBounds := image.Rect(plan.Left, plan.Top, plan.Left+plan.Width, plan.Top+plan.Height)
	maskPixels := image.NewNRGBA(maskBounds)
	for y := maskBounds.Min.Y; y < maskBounds.Max.Y; y++ {
		for x := maskBounds.Min.X; x < maskBounds.Max.X; x++ {
			value := uint8(127)
			if (x+y)%3 == 0 {
				value = 0
			}
			maskPixels.SetNRGBA(x, y, color.NRGBA{R: value})
		}
	}
	layer.Mask = &psdmodel.Mask{Bounds: maskBounds, Pixels: maskPixels}
	plan.MaskSpans = make([]int, plan.Height*2)
	for row := 0; row < plan.Height; row++ {
		plan.MaskSpans[row*2], plan.MaskSpans[row*2+1] = 1, plan.Width-1
	}
	beforeSpans := append([]int(nil), plan.MaskSpans...)
	beforeTransform := append([]float64(nil), layer.Placed.NonAffineTransform...)
	pixels, origin, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, plan,
		psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: true})
	if err != nil {
		t.Fatal(err)
	}
	checkPlacedReference(t, pixels, origin, source, matrix, func(x, y int) bool {
		return x < 1 || x >= plan.Width-1 || maskPixels.NRGBAAt(origin.X+x, origin.Y+y).R == 0
	})
	if !reflect.DeepEqual(plan.MaskSpans, beforeSpans) ||
		!reflect.DeepEqual(layer.Placed.NonAffineTransform, beforeTransform) ||
		layer.Placed != originalPlacement || layer.Placed.Warp != originalWarp {
		t.Fatal("sampling changed projection masks or selected placement geometry")
	}
}

func TestPlacedApproximateTransparentBorderZeroOptionOpaqueAndEmptyKeepStrictContract(t *testing.T) {
	layer, source := approximatePlacedFixture(placedReferenceMatrix{9, 2, -4, 1, 7, -3, .18, -.12, 1})
	canvas := image.Rect(-12, -12, 12, 12)
	plan, err := psdrender.CompileProjectionRegion(layer, canvas, canvas)
	if err != nil {
		t.Fatal(err)
	}
	strict, strictOrigin, strictError := psdrender.ProjectPlacedContentsContext(nil, layer, source, plan)
	zero, zeroOrigin, zeroError := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, plan,
		psdrender.PlacedSamplingOptions{})
	if strict != nil || zero != nil || strictOrigin != zeroOrigin ||
		!errors.Is(strictError, psdrender.ErrUnsupportedCrop) || !errors.Is(zeroError, psdrender.ErrUnsupportedCrop) {
		t.Fatal("zero-valued options did not preserve the strict transparent-crop error and origin")
	}
	for _, alpha := range []byte{173, 0} {
		for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
			for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
				pixel := source.NRGBAAt(x, y)
				pixel.A = alpha
				source.SetNRGBA(x, y, pixel)
			}
		}
		strict, strictOrigin, err = psdrender.ProjectPlacedContentsContext(nil, layer, source, plan)
		if err != nil {
			t.Fatal(err)
		}
		selected, selectedOrigin, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, plan,
			psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: true})
		if err != nil || strictOrigin != selectedOrigin || !bytes.Equal(strict.Pix, selected.Pix) {
			t.Fatalf("full ink/fully transparent input changed a supported old branch: alpha=%d err=%v", alpha, err)
		}
	}
}

func TestPlacedApproximateTransparentBorderHiddenRGBOwnershipAndLegacyPaths(t *testing.T) {
	matrix := placedReferenceMatrix{9, 2, -4, 1, 7, -3, 0, 0, 1}
	layer, source := approximatePlacedFixture(matrix)
	canvas := image.Rect(-12, -12, 12, 12)
	plan, err := psdrender.CompileProjectionRegion(layer, canvas, canvas)
	if err != nil {
		t.Fatal(err)
	}
	before := bytes.Clone(source.Pix)
	options := psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: true}
	pixels, _, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, plan, options)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source.Pix, before) {
		t.Fatal("projection changed caller-owned strided source data")
	}
	for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
		for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
			if source.NRGBAAt(x, y).A == 0 {
				source.SetNRGBA(x, y, color.NRGBA{R: 255, G: 7, B: 189})
			}
		}
	}
	changedHidden, _, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, plan, options)
	if err != nil || !bytes.Equal(pixels.Pix, changedHidden.Pix) {
		t.Fatalf("transparent border RGB leaked into premultiplied canvas sampling: %v", err)
	}
	before = bytes.Clone(source.Pix)
	pixels.Pix[0] = 219
	if !bytes.Equal(source.Pix, before) {
		t.Fatal("projection output aliases the prototype backing")
	}
	axisLayer, axisSource := croppedPlacementSource()
	axisPlan, err := psdrender.CompileProjection(axisLayer, 16, 16, false)
	if err != nil {
		t.Fatal(err)
	}
	strict, strictOrigin, err := psdrender.ProjectPlacedContentsContext(nil, axisLayer, axisSource, axisPlan)
	if err != nil {
		t.Fatal(err)
	}
	selected, selectedOrigin, err := psdrender.ProjectPlacedContentsWithOptions(
		nil, axisLayer, axisSource, axisPlan, options)
	if err != nil || strictOrigin != selectedOrigin || !bytes.Equal(strict.Pix, selected.Pix) {
		t.Fatalf("selecting approximation changed the already supported axis-aligned sampler: %v", err)
	}
}

type placedCancelContext struct {
	context.Context
	checks int
	at     int
	cancel context.CancelFunc
}

func (ctx *placedCancelContext) Err() error {
	ctx.checks++
	if ctx.checks == ctx.at {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestPlacedApproximateTransparentBorderCancellationAndInvalidPlans(t *testing.T) {
	layer, source := approximatePlacedFixture(placedReferenceMatrix{9, 2, -4, 1, 7, -3, 0, 0, 1})
	canvas := image.Rect(-12, -12, 12, 12)
	plan, err := psdrender.CompileProjectionRegion(layer, canvas, canvas)
	if err != nil {
		t.Fatal(err)
	}
	options := psdrender.PlacedSamplingOptions{ApproximateTransparentBorder: true}
	for _, at := range []int{1, 4, 15} {
		base, cancel := context.WithCancel(context.Background())
		ctx := &placedCancelContext{Context: base, at: at, cancel: cancel}
		longPlan := *plan
		longPlan.Height = 160
		pixels, _, err := psdrender.ProjectPlacedContentsWithOptions(ctx, layer, source, &longPlan, options)
		cancel()
		if pixels != nil || !errors.Is(err, context.Canceled) || ctx.checks > at+1 {
			t.Fatalf("cancellation during scan/projection returned partial data: at=%d checks=%d err=%v", at, ctx.checks, err)
		}
	}
	for _, spans := range [][]int{make([]int, plan.Height*2-1), make([]int, plan.Height*2)} {
		invalidPlan := *plan
		invalidPlan.MaskSpans = spans
		if len(spans) == plan.Height*2 {
			spans[0], spans[1] = -1, plan.Width+1
		}
		pixels, _, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, &invalidPlan, options)
		if pixels != nil || !errors.Is(err, psdrender.ErrImageSize) {
			t.Fatalf("invalid mask row spans were accepted by the fallback: %v", err)
		}
	}
	invalidPlan := *plan
	invalidPlan.Width = psdrender.MaxDimension + 1
	if pixels, _, err := psdrender.ProjectPlacedContentsWithOptions(nil, layer, source, &invalidPlan, options); pixels != nil || !errors.Is(err, psdrender.ErrImageSize) {
		t.Fatalf("oversized approximate projection bypassed allocation limits: %v", err)
	}
}
