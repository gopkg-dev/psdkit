package psdcompose

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestAdjustmentRetainsBackdropAlphaAndMask(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	pixels := image.NewNRGBA(bounds)
	pixels.SetNRGBA(0, 0, color.NRGBA{R: 20, G: 80, B: 200, A: 127})
	pixels.SetNRGBA(1, 0, color.NRGBA{R: 50, G: 70, B: 90, A: 0})
	mask := image.NewNRGBA(bounds)
	mask.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	opacity := .5
	document := &psdmodel.Document{Width: 2, Height: 1, Layers: []*psdmodel.Layer{
		{Bounds: bounds, Pixels: pixels, BlendMode: "norm"},
		{BlendMode: "norm", Opacity: &opacity, Adjustment: &psdadjust.Adjustment{Kind: psdadjust.Invert},
			Mask: &psdmodel.Mask{Bounds: bounds, Pixels: mask}},
	}}
	result, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.NRGBAAt(0, 0); got != (color.NRGBA{R: 128, G: 128, B: 128, A: 127}) {
		t.Fatalf("half inverted translucent backdrop = %v", got)
	}
	if got := result.NRGBAAt(1, 0); got != (color.NRGBA{}) {
		t.Fatalf("adjustment creates coverage over transparency: %v", got)
	}
	if got := pixels.NRGBAAt(0, 0); got != (color.NRGBA{R: 20, G: 80, B: 200, A: 127}) {
		t.Fatalf("borrowed source changed: %v", got)
	}
}

func TestAdjustmentClippedToRasterBase(t *testing.T) {
	bounds := image.Rect(0, 0, 1, 1)
	pixels := image.NewNRGBA(bounds)
	pixels.SetNRGBA(0, 0, color.NRGBA{R: 10, G: 20, B: 30, A: 99})
	document := &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{
		{Bounds: bounds, Pixels: pixels, BlendMode: "norm"},
		{Clipping: true, BlendMode: "norm", Adjustment: &psdadjust.Adjustment{Kind: psdadjust.Invert}},
	}}
	result, err := Render(context.Background(), document)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.NRGBAAt(0, 0); got != (color.NRGBA{R: 245, G: 235, B: 225, A: 99}) {
		t.Fatalf("clipped inversion = %v", got)
	}
}

func TestAdjustmentBudgetsAndHiddenCapabilities(t *testing.T) {
	layer := &psdmodel.Layer{BlendMode: "norm", Adjustment: &psdadjust.Adjustment{Kind: psdadjust.Invert}}
	document := &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{layer}}
	if _, err := Render(context.Background(), document, Options{MaxWorkingBytes: 16 << 10}); !errors.Is(err, ErrLimit) {
		t.Fatalf("transform plus output must exceed budget: %v", err)
	}
	layer.Hidden = true
	layer.Adjustment.Kind = "unknown"
	if _, err := Render(context.Background(), document, Options{MaxWorkingBytes: 2048}); err != nil {
		t.Fatalf("hidden unsupported adjustment must not allocate a transform: %v", err)
	}
	layer.Hidden = false
	if _, err := Render(context.Background(), document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("visible unknown adjustment = %v", err)
	}
}

func TestLiveSolidFillWithoutCachedPixels(t *testing.T) {
	rgb := [3]float64{.2, .4, .8}
	opacity := .5
	layer := &psdmodel.Layer{FillColor: &rgb, Opacity: &opacity, BlendMode: "norm"}
	result, err := Render(context.Background(), &psdmodel.Document{Width: 2, Height: 1,
		Layers: []*psdmodel.Layer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	for x := range 2 {
		if got := result.NRGBAAt(x, 0); got != (color.NRGBA{R: 51, G: 102, B: 204, A: 128}) {
			t.Fatalf("solid fill at %d = %v", x, got)
		}
	}
	rgb[0] = 2
	if _, err := Render(context.Background(), &psdmodel.Document{Width: 1, Height: 1,
		Layers: []*psdmodel.Layer{layer}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid fill component = %v", err)
	}
}
