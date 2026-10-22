package psdprecision

import (
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func precisionLayer(encoding Encoding, values ...float64) *Layer {
	return &Layer{Bounds: image.Rect(0, 0, len(values)/4, 1), Pixels: &FloatImage{
		Width: len(values) / 4, Height: 1, Encoding: encoding, Pix: values,
	}}
}

func precisionCheck(t *testing.T, result *FloatImage, expected []float64) {
	t.Helper()
	if len(result.Pix) != len(expected) {
		t.Fatalf("sample count = %d, expected %d", len(result.Pix), len(expected))
	}
	for index, value := range expected {
		if math.Abs(result.Pix[index]-value) > 1e-12 {
			t.Fatalf("sample %d = %.17g, expected %.17g", index, result.Pix[index], value)
		}
	}
}

func TestPrecisionSourceOverRetainsHDRAndSubBytePrecision(t *testing.T) {
	for _, encoding := range []Encoding{EncodedRGB, LinearRGB} {
		bottom := precisionLayer(encoding, .2, .4, .6, .5)
		top := precisionLayer(encoding, 2, -1, .125, .5)
		scene := &Scene{Width: 1, Height: 1, Encoding: encoding, Layers: []*Layer{bottom, top}}
		result, err := Render(nil, scene)
		if err != nil {
			t.Fatal(err)
		}
		precisionCheck(t, result, []float64{1.4, -8.0 / 15, 17.0 / 60, .75})
		if result.Encoding != encoding {
			t.Fatal("ordinary blending silently changed the color domain")
		}
		result.Pix[0] = 0
		if top.Pixels.Pix[0] != 2 || bottom.Pixels.Pix[0] != .2 {
			t.Fatal("returned samples alias borrowed layers")
		}
	}
	value := 32767.0 / 65535
	scene := &Scene{Width: 1, Height: 1, Layers: []*Layer{precisionLayer(EncodedRGB, value, value, value, 1)}}
	result, err := Render(nil, scene)
	if err != nil || result.Pix[0] != value || result.Pix[0]*255 == math.Round(result.Pix[0]*255) {
		t.Fatalf("16-bit rational value was quantized to eight bits: %v", err)
	}
}

func TestPrecisionIsolatedGroupOpacityAndPixelMask(t *testing.T) {
	blue := precisionLayer(EncodedRGB, 0, 0, 1, 1, 0, 0, 1, 1)
	red := precisionLayer(EncodedRGB, 1, 0, 0, .5, 1, 0, 0, .5)
	green := precisionLayer(EncodedRGB, 0, 1, 0, .5, 0, 1, 0, .5)
	opacity := .5
	group := &Layer{Children: []*Layer{red, green}, Opacity: &opacity,
		Mask: &Mask{Bounds: image.Rect(1, 0, 2, 1), Values: []float64{.25}}}
	scene := &Scene{Width: 2, Height: 1, Layers: []*Layer{blue, group}}
	result, err := Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{0, 0, 1, 1, .03125, .0625, .90625, 1})
	// Opacity applies to the fully combined group, rather than independently weakening its children.
	group.Mask = nil
	result, err = Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{.125, .25, .625, 1, .125, .25, .625, 1})
}

func TestPrecisionMaskOutsideInversionAndDensity(t *testing.T) {
	density := .5
	layer := precisionLayer(LinearRGB, 2, -.25, .123456789, .5, 2, -.25, .123456789, .5)
	layer.Mask = &Mask{Bounds: image.Rect(1, 0, 2, 1), Values: []float64{.25}, Invert: true, Density: &density}
	result, err := Render(nil, &Scene{Width: 2, Height: 1, Encoding: LinearRGB, Layers: []*Layer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{2, -.25, .123456789, .5, 2, -.25, .123456789, .4375})
}

func TestPrecisionExplicitNonFinitePoliciesPreserveInputs(t *testing.T) {
	nan := math.Float64frombits(0x7ff8000000001234)
	layer := precisionLayer(LinearRGB, nan, math.Inf(1), math.Inf(-1), .5)
	scene := &Scene{Width: 1, Height: 1, Encoding: LinearRGB, Layers: []*Layer{layer}}
	if _, err := Render(nil, scene); !errors.Is(err, ErrNonFinite) {
		t.Fatalf("default non-finite policy silently changed samples: %v", err)
	}
	for _, testCase := range []struct {
		policy   NonFinitePolicy
		expected []float64
	}{{NonFiniteZero, []float64{0, 0, 0, .5}}, {NonFiniteClamp, []float64{0, 1, 0, .5}}} {
		result, err := Render(nil, scene, Options{NonFinite: testCase.policy})
		if err != nil {
			t.Fatal(err)
		}
		precisionCheck(t, result, testCase.expected)
	}
	if math.Float64bits(layer.Pixels.Pix[0]) != 0x7ff8000000001234 {
		t.Fatal("rendering modified the original NaN payload")
	}
	layer.Pixels.Pix[3] = 1.01
	if _, err := Render(nil, scene, Options{NonFinite: NonFiniteClamp}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("finite invalid alpha was silently clipped: %v", err)
	}
}

func TestPrecisionBudgetsCyclesAndUnsupportedBlend(t *testing.T) {
	group := &Layer{Children: []*Layer{precisionLayer(EncodedRGB, 1, 0, 0, 1)}}
	scene := &Scene{Width: 1, Height: 1, Layers: []*Layer{group, group}}
	if _, err := Render(nil, scene, Options{MaxWorkingBytes: 63}); !errors.Is(err, ErrLimit) {
		t.Fatalf("group buffer was not included in working budget: %v", err)
	}
	if _, err := Render(nil, scene, Options{MaxWorkingBytes: 64}); err != nil {
		t.Fatalf("sibling group buffers were not reused within the exact budget: %v", err)
	}
	group.Children = []*Layer{group}
	if _, err := Render(nil, scene); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cyclic scene tree was traversed: %v", err)
	}
	group.Children = []*Layer{precisionLayer(EncodedRGB, 1, 0, 0, 1)}
	group.BlendMode = "overlay"
	if _, err := Render(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported high-depth blend was approximated: %v", err)
	}
	group.BlendMode = "pass"
	opacity := .5
	group.Opacity = &opacity
	result, err := Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{1, 0, 0, .75})
	group.Hidden = true
	if _, err := Render(nil, scene); err != nil {
		t.Fatalf("hidden unsupported semantics blocked a transparent scene: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, scene); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled scene allocated or rendered: %v", err)
	}
}
