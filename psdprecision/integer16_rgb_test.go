package psdprecision_test

import (
	"context"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdprecision"
)

func differenceScene() *psdprecision.Scene {
	canvas := image.Rect(0, 0, 1, 1)
	return &psdprecision.Scene{Width: 1, Height: 1, Layers: []*psdprecision.Layer{
		{Bounds: canvas, Pixels: normalRawPixel(32767, 32768), BlendMode: "normal"},
		{Bounds: canvas, Pixels: normalRawPixel(32768, 65535), BlendMode: "difference"},
	}}
}

func TestRGB16LimitedDifferenceContractAndLegacySeparation(t *testing.T) {
	scene := differenceScene()
	result, err := psdprecision.RenderRGB16(nil, scene)
	if err != nil || result == nil {
		t.Fatal(err)
	}
	if _, err := psdprecision.RenderRGB16Normal(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("Normal-only API accepted Difference: %v", err)
	}
	for _, mutate := range []func(*psdprecision.Scene){
		func(s *psdprecision.Scene) { s.Layers[0].BlendMode = "diff" },
		func(s *psdprecision.Scene) { s.Layers[1].BlendMode = "screen" },
		func(s *psdprecision.Scene) {
			s.Layers[1].Bounds = image.Rectangle{}
			s.Layers[1].Pixels = &psdprecision.FloatImage{}
		},
		func(s *psdprecision.Scene) { s.Layers[1].Mask = &psdprecision.Mask{} },
		func(s *psdprecision.Scene) { s.Layers[1].Clipping = true },
		func(s *psdprecision.Scene) { s.Layers[1].Children = []*psdprecision.Layer{}; s.Layers[1].Pixels = nil },
		func(s *psdprecision.Scene) { s.Layers[1].Opacity = new(0.3) },
		func(s *psdprecision.Scene) { s.Layers[1].Pixels.Pix[0] = 0.5 },
	} {
		source := differenceScene()
		mutate(source)
		if result, err := psdprecision.RenderRGB16(nil, source); result != nil ||
			!errors.Is(err, psdprecision.ErrUnsupported) {
			t.Fatalf("out-of-scope input accepted: %v", err)
		}
	}
	for _, mutate := range []func(*psdprecision.Scene){
		func(s *psdprecision.Scene) { s.Layers[1].Hidden = true; s.Layers[1].BlendMode = "screen" },
		func(s *psdprecision.Scene) { s.Layers[1].Opacity = new(float64(0)); s.Layers[1].BlendMode = "screen" },
	} {
		source := differenceScene()
		mutate(source)
		if _, err := psdprecision.RenderRGB16(nil, source); err != nil {
			t.Fatalf("inactive unsupported mode affected active qualification: %v", err)
		}
	}
}

func TestRGB16DifferenceAlphaZeroCanonicalAndQuota(t *testing.T) {
	scene := differenceScene()
	scene.Layers[0].Pixels = normalRawPixel(32767, 0)
	scene.Layers[1].Pixels = normalRawPixel(65535, 0)
	result, err := psdprecision.RenderRGB16(nil, scene, psdprecision.Options{MaxWorkingBytes: 32, MaxPixels: 1})
	if err != nil || result.Pix[0] != 0.5 || result.Pix[3] != 0 {
		t.Fatalf("hidden alpha0 RGB or exact output quota failed: %v %v", result, err)
	}
	if result, err := psdprecision.RenderRGB16(nil, scene, psdprecision.Options{MaxWorkingBytes: 31}); result != nil ||
		!errors.Is(err, psdprecision.ErrLimit) {
		t.Fatalf("quota31 accepted one32-byte pixel: %v", err)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1)} {
		source := differenceScene()
		source.Layers[1].Pixels.Pix[0] = value
		if result, err := psdprecision.RenderRGB16(nil, source); result != nil || !errors.Is(err, psdprecision.ErrNonFinite) {
			t.Fatalf("nonfinite accepted: %v", err)
		}
	}
	probe := &normalFinalCancellation{Context: context.Background()}
	if _, err := psdprecision.RenderRGB16(probe, scene); err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	late := &normalFinalCancellation{Context: parent, cancel: cancel, after: probe.calls}
	if result, err := psdprecision.RenderRGB16(late, scene); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("late cancellation exposed partial output: %v", err)
	}
}

func TestRGB16DifferenceWholeSceneQualificationBeforeROI(t *testing.T) {
	scene := &psdprecision.Scene{Width: 2, Height: 1, Layers: []*psdprecision.Layer{
		{Bounds: image.Rect(0, 0, 2, 1), Pixels: &psdprecision.FloatImage{Width: 2, Height: 1, SourceDepth: 16,
			Pix: []float64{0, 0, 0, 1, 0.5, 0, 0, 1}}},
	}}
	if result, err := psdprecision.RenderRGB16Region(nil, scene, image.Rect(0, 0, 1, 1)); result != nil ||
		!errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("ROI hid noncanonical source code outside output: %v", err)
	}
}
