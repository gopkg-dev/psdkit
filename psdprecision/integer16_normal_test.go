package psdprecision_test

import (
	"context"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdprecision"
)

func normalRawPixel(color, alpha uint16) *psdprecision.FloatImage {
	return &psdprecision.FloatImage{Width: 1, Height: 1, SourceDepth: 16,
		Pix: []float64{float64(color) / 65535, float64(color) / 65535,
			float64(color) / 65535, float64(alpha) / 65535}}
}

type normalFinalCancellation struct {
	context.Context
	cancel context.CancelFunc
	calls  int
	after  int
}

func (ctx *normalFinalCancellation) Err() error {
	ctx.calls++
	if ctx.after > 0 && ctx.calls == ctx.after {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestRGB16NormalLateCancellationDoesNotExposeResult(t *testing.T) {
	scene := &psdprecision.Scene{Width: 3, Height: 1, Layers: []*psdprecision.Layer{
		{Bounds: image.Rect(1, 0, 2, 1), Pixels: normalRawPixel(32767, 0)},
	}}
	probe := &normalFinalCancellation{Context: context.Background()}
	if _, err := psdprecision.RenderRGB16Normal(probe, scene); err != nil || probe.calls < 2 {
		t.Fatalf("could not measure final cancellation checkpoint: calls%d, %v", probe.calls, err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &normalFinalCancellation{Context: parent, cancel: cancel, after: probe.calls}
	result, err := psdprecision.RenderRGB16Normal(ctx, scene)
	if result != nil || !errors.Is(err, context.Canceled) || ctx.calls != probe.calls {
		t.Fatalf("final cancellation exposed result: %v, calls%d/%d, %v", result, ctx.calls, probe.calls, err)
	}
}

func TestRGB16NormalEmptyFirstRemainsUnsupported(t *testing.T) {
	empty := &psdprecision.Layer{Bounds: image.Rect(0, 0, 0, 0), Pixels: &psdprecision.FloatImage{}}
	top := &psdprecision.Layer{Bounds: image.Rect(1, 0, 2, 1), Pixels: normalRawPixel(32767, 0)}
	scene := &psdprecision.Scene{Width: 2, Height: 1, Layers: []*psdprecision.Layer{empty, top}}
	if _, err := psdprecision.RenderRGB16Normal(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("unproven empty first was accepted: %v", err)
	}
	if _, err := psdprecision.RenderRGB16NormalRegion(
		nil, scene, top.Bounds,
	); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("ROI locally removed empty first: %v", err)
	}
	generic, err := psdprecision.Render(nil, scene)
	if err != nil || generic.Pix[4] != 0 || generic.Pix[7] != 0 {
		t.Fatalf("generic empty-first mathematical contract changed: %v, %v", generic, err)
	}
}

func TestRGB16NormalPreservesAlphaZeroAndCroppedPaint(t *testing.T) {
	canvas := image.Rect(0, 0, 2, 1)
	base := &psdprecision.Layer{Bounds: canvas, BlendMode: "norm",
		Pixels: &psdprecision.FloatImage{Width: 2, Height: 1,
			Pix: []float64{float64(32767) / 65535, 0, 1, 0, float64(32767) / 65535, 0, 1, 0}}}
	opacity := float64(161) / 255
	top := &psdprecision.Layer{Bounds: image.Rect(1, 0, 2, 1), BlendMode: "norm",
		Pixels: normalRawPixel(65535, 32768), Opacity: &opacity}
	scene := &psdprecision.Scene{Width: 2, Height: 1, Layers: []*psdprecision.Layer{base, top}}
	result, err := psdprecision.RenderRGB16Normal(nil, scene)
	if err != nil || result.Pix[0] != .5 || result.Pix[1] != 0 || result.Pix[2] != 1 || result.Pix[3] != 0 ||
		result.Pix[4] != 1 || result.Pix[7] != float64(10345)/32768 {
		t.Fatalf("Normal transparent RGB or partial paint = %v, %v", result, err)
	}
	region, err := psdprecision.RenderRGB16NormalRegion(nil, scene, image.Rect(1, 0, 2, 1))
	if err != nil || region.Bounds != top.Bounds {
		t.Fatalf("Normal absolute region = %v, %v", region, err)
	}
	for component := range 4 {
		if region.Image.Pix[component] != result.Pix[4+component] {
			t.Fatalf("component%d differs between full and region", component)
		}
	}
	generic, err := psdprecision.Render(nil, scene)
	if err != nil || generic.Pix[0] != 0 || generic.Pix[7] == result.Pix[7] {
		t.Fatalf("generic mathematical floating contract changed: %v, %v", generic, err)
	}
	if _, err := psdprecision.RenderRGB16OverOpaque(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("old opaque-backdrop contract expanded: %v", err)
	}
}

func TestRGB16NormalPartialBottomUsesGlobalBBoxAndFirstLayer(t *testing.T) {
	zero := float64(0)
	bottom := &psdprecision.Layer{Bounds: image.Rect(1, 0, 2, 1), Pixels: normalRawPixel(32767, 0)}
	top := &psdprecision.Layer{Bounds: image.Rect(4, 0, 5, 1), Pixels: normalRawPixel(0, 0)}
	hidden := &psdprecision.Layer{Bounds: image.Rect(0, 0, 1, 1), Hidden: true, BlendMode: "diss",
		Pixels: normalRawPixel(0, 65535)}
	skipped := &psdprecision.Layer{Bounds: image.Rect(5, 0, 6, 1), Opacity: &zero, BlendMode: "diss",
		Pixels: normalRawPixel(0, 65535)}
	scene := &psdprecision.Scene{Width: 6, Height: 1, Layers: []*psdprecision.Layer{hidden, bottom, top, skipped}}
	full, err := psdprecision.RenderRGB16Normal(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	for x, wanted := range []float64{0, .5, 1, 1, 1, 0} {
		for component := range 4 {
			expected := wanted
			if component == 3 {
				expected = 0
			}
			if full.Pix[x*4+component] != expected {
				t.Fatalf("partial bbox pixel%d component%d = %v, want%v", x, component, full.Pix[x*4+component], expected)
			}
		}
	}
	region, err := psdprecision.RenderRGB16NormalRegion(nil, scene, image.Rect(2, 0, 5, 1))
	if err != nil || region.Image.Pix[8] != 1 {
		t.Fatalf("global first outside ROI was replaced by the transparent top: %v, %v", region, err)
	}
	for component, value := range region.Image.Pix {
		if value != full.Pix[8+component] {
			t.Fatalf("region component%d changed global bbox/first", component)
		}
	}
	if _, err := psdprecision.RenderRGB16OverOpaque(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("old OverOpaque contract expanded: %v", err)
	}
	generic, err := psdprecision.Render(nil, scene)
	if err != nil || generic.Pix[8] != 0 {
		t.Fatalf("generic float inherited native hidden white RGB: %v, %v", generic, err)
	}
}

func TestRGB16NormalTinyROIUsesWholeSceneMetadataWithoutCanvasAllocation(t *testing.T) {
	scene := &psdprecision.Scene{Width: 500_000_000, Height: 1, Layers: []*psdprecision.Layer{
		{Bounds: image.Rect(0, 0, 1, 1), Pixels: normalRawPixel(0, 0)},
		{Bounds: image.Rect(50_000_000, 0, 50_000_001, 1), Pixels: normalRawPixel(0, 0)},
	}}
	region, err := psdprecision.RenderRGB16NormalRegion(nil, scene, image.Rect(10000, 0, 10001, 1),
		psdprecision.Options{MaxPixels: 1, MaxWorkingBytes: 32})
	if err != nil || region.Image.Pix[0] != 1 || region.Image.Pix[3] != 0 {
		t.Fatalf("one-pixel ROI allocated the global bbox or changed its initialization: %v, %v", region, err)
	}
	if _, err := psdprecision.RenderRGB16Normal(nil, scene,
		psdprecision.Options{MaxPixels: 1, MaxWorkingBytes: 32}); !errors.Is(err, psdprecision.ErrLimit) {
		t.Fatalf("full canvas output budget lost: %v", err)
	}
	scene.Layers[1].BlendMode = "mul "
	if _, err := psdprecision.RenderRGB16NormalRegion(nil, scene,
		image.Rect(10000, 0, 10001, 1)); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("non-Normal outside ROI was locally ignored: %v", err)
	}
}

func TestRGB16NormalCanonicalCodeContract(t *testing.T) {
	bounds := image.Rect(0, 0, 1, 1)
	for code := 0; code <= 65535; code++ {
		layer := &psdprecision.Layer{Bounds: bounds, Pixels: normalRawPixel(uint16(code), uint16(code))}
		scene := &psdprecision.Scene{Width: 1, Height: 1, Layers: []*psdprecision.Layer{layer}}
		if _, err := psdprecision.RenderRGB16Normal(nil, scene); err != nil {
			t.Fatalf("canonical code%d rejected: %v", code, err)
		}
	}
	for _, component := range []int{0, 3} {
		pixels := normalRawPixel(32767, 32767)
		pixels.Pix[component] = math.Nextafter(pixels.Pix[component], 1)
		scene := &psdprecision.Scene{Width: 1, Height: 1,
			Layers: []*psdprecision.Layer{{Bounds: bounds, Pixels: pixels}}}
		if _, err := psdprecision.RenderRGB16Normal(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
			t.Fatalf("Nextafter component%d accepted: %v", component, err)
		}
		if _, err := psdprecision.Render(nil, scene); err != nil {
			t.Fatalf("generic rational component%d rejected: %v", component, err)
		}
	}
	opacity := math.Nextafter(float64(128)/255, 1)
	scene := &psdprecision.Scene{Width: 1, Height: 1,
		Layers: []*psdprecision.Layer{{Bounds: bounds, Pixels: normalRawPixel(0, 65535), Opacity: &opacity}}}
	if _, err := psdprecision.RenderRGB16Normal(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("noncanonical opacity accepted: %v", err)
	}
}

func TestRGB16NormalBudgetsCancellationAndNonFinite(t *testing.T) {
	scene := &psdprecision.Scene{Width: 1, Height: 1,
		Layers: []*psdprecision.Layer{{Bounds: image.Rect(0, 0, 1, 1), Pixels: normalRawPixel(32767, 65535)}}}
	if _, err := psdprecision.RenderRGB16Normal(nil, scene,
		psdprecision.Options{MaxWorkingBytes: 31}); !errors.Is(err, psdprecision.ErrLimit) {
		t.Fatalf("output budget was not enforced: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := psdprecision.RenderRGB16Normal(ctx, scene); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	scene.Layers[0].Pixels.Pix[0] = math.NaN()
	if _, err := psdprecision.RenderRGB16Normal(nil, scene,
		psdprecision.Options{NonFinite: psdprecision.NonFiniteZero}); !errors.Is(err, psdprecision.ErrNonFinite) {
		t.Fatalf("invalid native source silently substituted: %v", err)
	}
}
