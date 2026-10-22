package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func vectorFeatherTestMask(bounds image.Rectangle) *psdvector.Mask {
	points := []psdvector.Point{{X: float64(bounds.Min.X), Y: float64(bounds.Min.Y)},
		{X: float64(bounds.Max.X), Y: float64(bounds.Min.Y)},
		{X: float64(bounds.Max.X), Y: float64(bounds.Max.Y)},
		{X: float64(bounds.Min.X), Y: float64(bounds.Max.Y)}}
	knots := make([]psdvector.Knot, len(points))
	for index, point := range points {
		knots[index] = psdvector.Knot{Anchor: point, Preceding: point, Leaving: point}
	}
	return &psdvector.Mask{Version: 3, Width: 32, Height: 16, HasInitialFill: true,
		Subpaths: []psdvector.Subpath{{Closed: true, Operation: 1, Unknown1: 1, Knots: knots}}}
}

func vectorFeatherTestDocument(radius float64) (*psdmodel.Document, *psdmodel.Layer) {
	layer := composeTestLayer(image.Rect(0, 0, 32, 16), color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	layer.VectorMask = vectorFeatherTestMask(image.Rect(4, 3, 26, 13))
	layer.VectorFeather = new(radius)
	return &psdmodel.Document{Width: 32, Height: 16, Layers: []*psdmodel.Layer{layer}}, layer
}

func TestVectorFeatherNativeIntegerRectangleReferenceSamplesAndExternalGeometry(t *testing.T) {
	// Reference alpha bytes come from the independently retained Photoshop-native PNG controls.
	points := []image.Point{image.Pt(0, 0), image.Pt(3, 8), image.Pt(4, 8), image.Pt(15, 8), image.Pt(9, 7)}
	for _, test := range []struct {
		radius   float64
		outside  bool
		invert   bool
		expected []byte
	}{
		{radius: .5, expected: []byte{0, 56, 199, 255, 255}},
		{radius: 1.5, expected: []byte{0, 95, 160, 255, 255}},
		{radius: 3, expected: []byte{7, 101, 130, 231, 224}},
		{radius: 3, outside: true, expected: []byte{223, 223, 223, 28, 189}},
		{radius: 1.5, invert: true, expected: []byte{255, 160, 95, 0, 0}},
	} {
		document, layer := vectorFeatherTestDocument(test.radius)
		if test.outside {
			layer.VectorMask = vectorFeatherTestMask(image.Rect(-4, -4, 12, 12))
		}
		layer.VectorMask.Invert = test.invert
		before := bytes.Clone(layer.Pixels.Pix)
		result, err := Render(nil, document)
		if err != nil {
			t.Fatal(err)
		}
		for index, point := range points {
			actual := result.NRGBAAt(point.X, point.Y).A
			if math.Abs(float64(int(actual)-int(test.expected[index]))) > 2 {
				t.Fatalf("radius=%g outside=%t invert=%t at%v: alpha=%d native=%d",
					test.radius, test.outside, test.invert, point, actual, test.expected[index])
			}
		}
		if !bytes.Equal(before, layer.Pixels.Pix) {
			t.Fatal("vector feather mutated its borrowed raster")
		}
	}
}

func TestVectorFeatherDensityDisabledAndConstantDefaultsFollowGeometryBlur(t *testing.T) {
	document, layer := vectorFeatherTestDocument(1.5)
	base, err := Render(nil, document)
	if err != nil {
		t.Fatal(err)
	}
	layer.VectorDensity = new(uint8(128))
	dense, err := Render(nil, document)
	if err != nil {
		t.Fatal(err)
	}
	for y := range document.Height {
		for x := range document.Width {
			expected := 255 - math.Round(float64(255-base.NRGBAAt(x, y).A)*128/255)
			if math.Abs(float64(dense.NRGBAAt(x, y).A)-expected) > 1 {
				t.Fatalf("density does not follow geometry feather at %d,%d", x, y)
			}
		}
	}
	layer.VectorMask.Disabled = true
	layer.VectorFeather = new(float64(1000))
	result, err := Render(nil, document, Options{MaxWorkingBytes: 4096, MaxFeatherRadius: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.NRGBAAt(0, 0).A != 255 || result.NRGBAAt(15, 8).A != 255 {
		t.Fatal("disabled vector mask still applied feather or density")
	}
	layer.VectorMask.Disabled = false
	layer.VectorFeather = new(1.5)
	layer.VectorMask.Subpaths = nil
	layer.VectorMask.InitialFill = true
	for _, inverted := range []bool{false, true} {
		layer.VectorMask.Invert = inverted
		result, err := Render(nil, document)
		if err != nil {
			t.Fatal(err)
		}
		expected := byte(255)
		if inverted {
			expected = 127
		}
		for y := range document.Height {
			for x := range document.Width {
				if result.NRGBAAt(x, y).A != expected {
					t.Fatal("constant initial/inverse coverage was truncated at the canvas edge")
				}
			}
		}
	}
}

func TestVectorFeatherSharedGeometryUsesDistinctRadiusCachesAndPeakBudget(t *testing.T) {
	document, first := vectorFeatherTestDocument(.5)
	second := composeTestLayer(first.Bounds, color.NRGBA{B: 255, A: 255})
	second.VectorMask, second.VectorFeather = first.VectorMask, new(float64(3))
	document.Layers = append(document.Layers, second)
	combined, err := Render(nil, document)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := preparePlan(document, nil)
	if err != nil || len(plan.vectors) != 1 || len(plan.vectors[first.VectorMask].feathers) != 2 {
		t.Fatalf("shared geometry confused per-layer feather radii: %v", err)
	}
	left, err := Render(nil, &psdmodel.Document{Width: 32, Height: 16, Layers: []*psdmodel.Layer{first}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := Render(nil, &psdmodel.Document{Width: 32, Height: 16, Layers: []*psdmodel.Layer{second}})
	if err != nil {
		t.Fatal(err)
	}
	for y := range document.Height {
		for x := range document.Width {
			a, b := float64(left.NRGBAAt(x, y).A)/255, float64(right.NRGBAAt(x, y).A)/255
			if math.Abs(float64(combined.NRGBAAt(x, y).A)-math.Round((b+a*(1-b))*255)) > 1 {
				t.Fatalf("shared vector geometry reused the wrong feather field at %d,%d", x, y)
			}
		}
	}
	low, high := int64(1), int64(1<<20)
	for low < high {
		middle := low + (high-low)/2
		if err := Validate(document, Options{MaxWorkingBytes: middle}); err == nil {
			high = middle
		} else if errors.Is(err, ErrLimit) {
			low = middle + 1
		} else {
			t.Fatal(err)
		}
	}
	if _, err := Render(nil, document, Options{MaxWorkingBytes: low}); err != nil {
		t.Fatalf("Validate accepted a budget that cannot cover raster/blur peak: bytes=%d err=%v", low, err)
	}
	if _, err := Render(nil, document, Options{MaxWorkingBytes: low - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("vector feather peak budget escaped one-byte boundary: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, document); !errors.Is(err, context.Canceled) {
		t.Fatalf("vector feather ignored context cancellation: %v", err)
	}
	if err := plan.prepareVectors(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("vector geometry preparation ignored cancellation: %v", err)
	}
}

func TestVectorFeatherUncalibratedRadiusCurveAndInteractionsReject(t *testing.T) {
	for index, change := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.VectorFeather = new(float64(2)) },
		func(layer *psdmodel.Layer) { layer.VectorMask.Subpaths[0].Knots[0].Leaving.X++ },
		func(layer *psdmodel.Layer) {
			layer.VectorMask.Subpaths[0].Knots[0].Anchor.X += .25
		},
		func(layer *psdmodel.Layer) { layer.Clipping = true },
		func(layer *psdmodel.Layer) { layer.Mask = &psdmodel.Mask{} },
		func(layer *psdmodel.Layer) { layer.BlendMode = "mul " },
	} {
		document, layer := vectorFeatherTestDocument(1.5)
		change(layer)
		if layer.Clipping {
			base := composeTestLayer(layer.Bounds, color.NRGBA{R: 255, A: 255})
			document.Layers = append([]*psdmodel.Layer{base}, document.Layers...)
		}
		if err := Validate(document); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("uncalibrated vector feather change %d became supported: %v", index, err)
		}
	}
	document, _ := vectorFeatherTestDocument(3)
	if err := Validate(document, Options{MaxFeatherRadius: 1.5}); !errors.Is(err, ErrLimit) {
		t.Fatalf("vector feather bypassed max radius: %v", err)
	}
	if int64(^uint(0)>>1) == math.MaxInt64 {
		document.Width, document.Height = int(math.MaxInt64/20), 1
		if err := Validate(document, Options{MaxWorkingBytes: math.MaxInt64}); !errors.Is(err, ErrLimit) {
			t.Fatalf("expanded geometry arithmetic overflowed instead of failing its budget: %v", err)
		}
	}
}
