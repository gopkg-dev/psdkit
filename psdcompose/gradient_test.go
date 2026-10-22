package psdcompose

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func liveGradientFixture() *psdgradient.Fill {
	return &psdgradient.Fill{Kind: psdgradient.Linear, Scale: 100, Smoothness: 4096,
		ColorStops: []psdgradient.ColorStop{{Location: 0, Midpoint: 50, RGB: [3]float64{1, 0, 0}},
			{Location: 4096, Midpoint: 50, RGB: [3]float64{0, 0, 1}}},
		OpacityStops: []psdgradient.OpacityStop{{Location: 0, Midpoint: 50, Opacity: 1},
			{Location: 4096, Midpoint: 50, Opacity: 1}}}
}

func TestLiveGradientIgnoresStaleRasterAndPreservesInput(t *testing.T) {
	fill := liveGradientFixture()
	stale := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	for index := 0; index < len(stale.Pix); index += 4 {
		stale.Pix[index+1], stale.Pix[index+3] = 255, 255
	}
	layer := &psdmodel.Layer{Bounds: stale.Rect, Pixels: stale, Gradient: fill, BlendMode: "norm"}
	actual, err := Render(context.Background(), &psdmodel.Document{Width: 8, Height: 4, Layers: []*psdmodel.Layer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	if actual.Pix[0] != 255 || actual.Pix[1] != 0 || stale.Pix[1] != 255 || fill.ColorStops[0].RGB[0] != 1 {
		t.Fatal("live gradient used stale raster or modified caller-owned state")
	}
	fill.Dither = true
	if _, err := Render(context.Background(), &psdmodel.Document{Width: 8, Height: 4,
		Layers: []*psdmodel.Layer{layer}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported live gradient = %v", err)
	}
	layer.Hidden = true
	if _, err := Render(context.Background(), &psdmodel.Document{Width: 8, Height: 4,
		Layers: []*psdmodel.Layer{layer}}); err != nil {
		t.Fatalf("hidden gradient should not compile: %v", err)
	}
}

func TestLiveGradientIncludesAllCachesInWorkingBudget(t *testing.T) {
	layer := &psdmodel.Layer{Bounds: image.Rect(0, 0, 8, 4), Gradient: liveGradientFixture(), BlendMode: "norm"}
	document := &psdmodel.Document{Width: 8, Height: 4, Layers: []*psdmodel.Layer{layer}}
	if _, err := Render(context.Background(), document, Options{MaxWorkingBytes: 1500}); !errors.Is(err, ErrLimit) {
		t.Fatalf("output/cache/stops budget was not enforced: %v", err)
	}
}
