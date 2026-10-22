package psdprecision

import (
	"context"
	"errors"
	"image"
	"math"
	"slices"
	"testing"
)

func TestPrecisionClippingKeepsBaseAlphaAndAppliesOpacityOnce(t *testing.T) {
	for _, encoding := range []Encoding{EncodedRGB, LinearRGB} {
		base := precisionLayer(encoding, 1, 0, 0, .5)
		top := precisionLayer(encoding, 0, 1, 0, .5)
		top.Clipping = true
		opacity := .5
		base.Opacity = &opacity
		scene := &Scene{Width: 1, Height: 1, Encoding: encoding, Layers: []*Layer{base, top}}
		before := slices.Clone(top.Pixels.Pix)
		result, err := Render(nil, scene, Options{MaxWorkingBytes: 32})
		if err != nil {
			t.Fatal(err)
		}
		precisionCheck(t, result, []float64{.5, .5, 0, .25})
		if !slices.Equal(before, top.Pixels.Pix) {
			t.Fatal("clipping mutated borrowed top samples")
		}
		result.Pix[0] = 100
		if base.Pixels.Pix[0] != 1 {
			t.Fatal("clipping result aliases base samples")
		}
		if _, err := Render(nil, scene, Options{MaxWorkingBytes: 31}); !errors.Is(err, ErrLimit) {
			t.Fatalf("clipping chain bypassed output budget: %v", err)
		}
	}
}

func TestPrecisionGroupedAndUngroupedClippingBlendAtDifferentStages(t *testing.T) {
	backdrop := precisionLayer(LinearRGB, .2, .4, .6, 1)
	base := precisionLayer(LinearRGB, .8, .5, .25, .5)
	base.BlendMode = "mul "
	top := precisionLayer(LinearRGB, 1.5, -.25, .75, .5)
	top.Clipping = true
	scene := &Scene{Width: 1, Height: 1, Encoding: LinearRGB, Layers: []*Layer{backdrop, base, top}}
	result, err := Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{.215, .225, .45, 1})
	grouped := false
	base.BlendClipped = &grouped
	result, err = Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{.515, .1875, .525, 1})
}

func TestPrecisionClippingMasksMultipleClipsAndOrphanLayers(t *testing.T) {
	base := precisionLayer(EncodedRGB, 1, 0, 0, .5, 1, 0, 0, 0)
	base.Mask = &Mask{Bounds: image.Rect(0, 0, 1, 1), Values: []float64{.5}}
	first := precisionLayer(EncodedRGB, 0, 1, 0, .5, 0, 1, 0, 1)
	second := precisionLayer(EncodedRGB, 0, 0, 1, .5, 0, 0, 1, 1)
	first.Clipping, second.Clipping = true, true
	scene := &Scene{Width: 2, Height: 1, Layers: []*Layer{base, first, second}}
	result, err := Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{.25, .25, .5, .25, 0, 0, 0, 0})
	base.Hidden = true
	result, err = Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, make([]float64, 8))
	scene.Layers = []*Layer{first, second}
	result, err = Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, make([]float64, 8))
}

func TestPrecisionBlendIfRetainsSubByteThresholdInputAndTransparentBackdrop(t *testing.T) {
	identity := BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	base := precisionLayer(EncodedRGB, .5, .5, .5, .5)
	top := precisionLayer(EncodedRGB, 127.99/255, 0, 0, 1)
	top.BlendRanges = []BlendRange{{Source: identity, Destination: identity},
		{Source: BlendSlider{BlackLow: 128, BlackHigh: 128, WhiteLow: 255, WhiteHigh: 255}, Destination: identity}}
	scene := &Scene{Width: 1, Height: 1, Layers: []*Layer{base, top}}
	result, err := Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{.5, .5, .5, .5})
	top.Pixels.Pix[0] = 128.01 / 255
	result, err = Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	precisionCheck(t, result, []float64{128.01 / 255, 0, 0, 1})
	top.BlendRanges = []BlendRange{{Source: identity, Destination: BlendSlider{
		BlackLow: 200, BlackHigh: 200, WhiteLow: 255, WhiteHigh: 255}}}
	result, err = Render(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(result.Pix[3]-.75) > 1e-12 {
		t.Fatalf("destination Blend If incorrectly erased transparent backdrop contribution: %v", result.Pix)
	}
}

func TestPrecisionRangesValidationGroupGuardsAndCancellation(t *testing.T) {
	base := precisionLayer(LinearRGB, 2, -.25, .125, .5)
	base.BlendRanges = []BlendRange{{Source: BlendSlider{BlackLow: 100, BlackHigh: 50}}}
	scene := &Scene{Width: 1, Height: 1, Encoding: LinearRGB, Layers: []*Layer{base}}
	if _, err := Render(nil, scene); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unordered range thresholds were rendered: %v", err)
	}
	base.BlendRanges = nil
	group := &Layer{Children: []*Layer{base}}
	top := precisionLayer(LinearRGB, 1, 0, 0, 1)
	top.Clipping = true
	scene.Layers = []*Layer{group, top}
	if _, err := Render(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("uncalibrated clipping-base group was silently rendered: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, scene); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled clipping allocation proceeded: %v", err)
	}
}
