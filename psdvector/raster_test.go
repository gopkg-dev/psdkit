package psdvector

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"sync"
	"testing"
)

func rectangleSubpath(left, top, right, bottom float64, operation int16) Subpath {
	path := Subpath{Closed: true, Operation: operation}
	for _, point := range []Point{{X: left, Y: top}, {X: right, Y: top}, {X: right, Y: bottom}, {X: left, Y: bottom}} {
		path.Knots = append(path.Knots, Knot{Preceding: point, Anchor: point, Leaving: point})
	}
	return path
}

func TestRasterizeRectangleCropInversionAndDisabled(t *testing.T) {
	mask, err := DecodeMask(nil, vectorRectangle(0), 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	expected := []byte{0, 0, 0, 0, 0, 255, 255, 0, 0, 255, 255, 0, 0, 0, 0, 0}
	for _, samples := range []int{4, 8} {
		pixels, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4), Options{Samples: samples})
		if err != nil || !bytes.Equal(pixels.Pix, expected) {
			t.Fatalf("rectangle samples=%d: %v, %v", samples, pixels, err)
		}
		clear(pixels.Pix)
		crop, err := Rasterize(nil, mask, image.Rect(1, 1, 3, 3), Options{Samples: samples})
		if err != nil || !bytes.Equal(crop.Pix, []byte{255, 255, 255, 255}) {
			t.Fatalf("absolute cropped mask: %v, %v", crop, err)
		}
	}
	mask.Invert = true
	pixels, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
	if err != nil {
		t.Fatal(err)
	}
	for index, value := range pixels.Pix {
		if value != 255-expected[index] {
			t.Fatal("inversion did not invert the final vector coverage")
		}
	}
	mask.Disabled = true
	pixels, err = Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range pixels.Pix {
		if value != 255 {
			t.Fatal("disabled vector mask still removed layer coverage")
		}
	}
}

func TestRasterizeBooleanComponentsAndContinuationFillRules(t *testing.T) {
	for _, test := range []struct {
		operation int16
		expected  []byte
	}{
		{0, []byte{255, 0, 255, 0}},
		{1, []byte{255, 255, 255, 0}},
		{2, []byte{255, 0, 0, 0}},
		{3, []byte{0, 255, 0, 0}},
	} {
		mask := &Mask{Width: 4, Height: 1, Subpaths: []Subpath{
			rectangleSubpath(0, 0, 2, 1, 1), rectangleSubpath(1, 0, 3, 1, test.operation),
		}}
		pixels, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 1))
		if err != nil || !bytes.Equal(pixels.Pix, test.expected) {
			t.Fatalf("operation=%d: %v, %v", test.operation, pixels, err)
		}
	}
	mask := &Mask{Width: 4, Height: 4, Subpaths: []Subpath{
		rectangleSubpath(0, 0, 4, 4, 1), rectangleSubpath(1, 1, 3, 3, -1),
	}}
	odd, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
	if err != nil || odd.Pix[5] != 0 || odd.Pix[0] != 255 {
		t.Fatalf("even-odd merged contours did not create a hole: %v", err)
	}
	rule := NonZero
	winding, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4), Options{FillRule: &rule})
	if err != nil || winding.Pix[5] != 255 {
		t.Fatalf("nonzero winding ignored contour direction: %v", err)
	}
	slicesReverseKnots(mask.Subpaths[1].Knots)
	winding, err = Rasterize(nil, mask, image.Rect(0, 0, 4, 4), Options{FillRule: &rule})
	if err != nil || winding.Pix[5] != 0 {
		t.Fatalf("opposite winding did not create a hole: %v", err)
	}
}

func slicesReverseKnots(knots []Knot) {
	for left, right := 0, len(knots)-1; left < right; left, right = left+1, right-1 {
		knots[left], knots[right] = knots[right], knots[left]
	}
}

func TestRasterizeFractionalEdgesCubicAndGrayscaleOwnership(t *testing.T) {
	mask := &Mask{Width: 4, Height: 4, Subpaths: []Subpath{rectangleSubpath(0.5, 0.5, 2.5, 2.5, 1)}}
	alpha, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
	if err != nil || alpha.Pix[0] != 64 || alpha.Pix[1] != 128 || alpha.Pix[5] != 255 {
		t.Fatalf("sample coverage at fractional rectangle edges: %v, %v", alpha, err)
	}
	gray, err := RasterizeNRGBA(nil, mask, image.Rect(0, 0, 4, 4))
	if err != nil || !bytes.Equal(gray.Pix[:4], []byte{64, 64, 64, 255}) {
		t.Fatalf("grayscale coverage or alpha is wrong: %v", err)
	}
	clear(gray.Pix)
	if alpha.Pix[0] != 64 || mask.Subpaths[0].Knots[0].Anchor.X != 0.5 {
		t.Fatal("raster outputs shared owned pixels or changed path metadata")
	}
	curve := Subpath{Closed: true, Operation: 1, Knots: []Knot{
		{Anchor: Point{X: 0, Y: 0}, Preceding: Point{X: 0, Y: 0}, Leaving: Point{X: 0, Y: 4}},
		{Anchor: Point{X: 4, Y: 0}, Preceding: Point{X: 4, Y: 4}, Leaving: Point{X: 4, Y: 0}},
	}}
	mask.Subpaths = []Subpath{curve}
	alpha, err = Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
	if err != nil || alpha.Pix[1] != 255 || alpha.Pix[13] != 0 || alpha.Pix[4] == 0 {
		t.Fatalf("cubic controls were flattened into an anchor-only polygon: %v, %v", alpha, err)
	}
}

func TestRasterizeUnsupportedBudgetsAndCancellation(t *testing.T) {
	mask := &Mask{Width: 4, Height: 4, Subpaths: []Subpath{rectangleSubpath(0, 0, 4, 4, 1)}}
	for _, options := range []Options{
		{MaxWorkingBytes: 8}, {MaxSegments: 1}, {MaxScanlineWork: 1}, {MaxDepth: 33},
	} {
		if _, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4), options); !errors.Is(err, ErrLimit) {
			t.Fatalf("resource option did not produce a limit error: %#v, %v", options, err)
		}
	}
	mask.Subpaths[0].Unknown2 = 1
	if _, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown subpath metadata was ignored: %v", err)
	}
	mask.Subpaths[0].Unknown2 = 0
	mask.Subpaths[0].Knots[0].Anchor.X = math.NaN()
	if _, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("nonfinite point was accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Rasterize(ctx, mask, image.Rect(0, 0, 4, 4)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rasterization: %v", err)
	}
}

func TestRasterizeRejectsAbsoluteCoordinatesThatLoseSubpixelPrecision(t *testing.T) {
	coordinate := int64(1) << 54
	if int64(int(coordinate)) != coordinate {
		t.Skip("absolute coordinate does not fit this platform's integer type")
	}
	mask := &Mask{Width: 4, Height: 4, Subpaths: []Subpath{rectangleSubpath(0, 0, 1, 1, 2)}}
	bounds := image.Rectangle{
		Min: image.Point{X: int(coordinate), Y: 0}, Max: image.Point{X: int(coordinate + 1), Y: 1},
	}
	if _, err := Rasterize(nil, mask, bounds); !errors.Is(err, ErrMalformed) {
		t.Fatalf("huge absolute crop silently lost its one-pixel coverage: %v", err)
	}
}

func TestRasterizeConcurrentOwnedCoverage(t *testing.T) {
	mask, err := DecodeMask(nil, vectorRectangle(0), 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			coverage, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
			if err != nil {
				t.Error(err)
				return
			}
			if coverage.Pix[5] != 255 || coverage.Pix[0] != 0 {
				t.Errorf("parallel coverage changed: %v", coverage.Pix)
			}
			clear(coverage.Pix)
		})
	}
	workers.Wait()
	if mask.Subpaths[0].Knots[0].Anchor.X != 1 || mask.Records[2].Raw[1] != 2 {
		t.Fatal("parallel rasterization changed decoded metadata")
	}
}
