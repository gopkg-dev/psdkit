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
)

func TestDissolveDefaultIsStrictAndExplicitSeedIsOwned(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 64, 64), color.NRGBA{R: 220, G: 40, B: 60, A: 128})
	layer.BlendMode = "diss"
	document := &psdmodel.Document{Width: 64, Height: 64, Layers: []*psdmodel.Layer{layer}}
	if _, err := Render(context.Background(), document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Dissolve must remain explicitly opt-in: %v", err)
	}
	seed := uint64(42)
	before := bytes.Clone(layer.Pixels.Pix)
	first, err := Render(context.Background(), document, Options{DissolveSeed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(context.Background(), document, Options{DissolveSeed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Pix, second.Pix) || !bytes.Equal(before, layer.Pixels.Pix) {
		t.Fatal("Dissolve was not reproducible or modified borrowed source pixels")
	}
	plan, err := preparePlan(document, []Options{{DissolveSeed: &seed}})
	if err != nil {
		t.Fatal(err)
	}
	seed++
	if *plan.options.DissolveSeed != 42 {
		t.Fatal("render plan borrowed the caller's seed pointer")
	}
	third, err := Render(context.Background(), document, Options{DissolveSeed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first.Pix, third.Pix) {
		t.Fatal("changing the seed did not change the noise field")
	}
}

func TestDissolveCoverageDistributionMonotonicityAndSalt(t *testing.T) {
	const side = 128
	for _, coverage := range []float64{0, .1, .25, .5, .75, .9, 1} {
		painted := 0
		for y := range side {
			for x := range side {
				if dissolvePainted(42, 1, x, y, coverage) {
					painted++
					if !dissolvePainted(42, 1, x, y, min(1, coverage+.1)) {
						t.Fatal("increasing alpha reshuffled previously painted pixels")
					}
				}
			}
		}
		actual := float64(painted) / (side * side)
		if math.Abs(actual-coverage) > .015 {
			t.Fatalf("coverage %g produced paint fraction %g", coverage, actual)
		}
	}
	changes := 0
	for y := range side {
		for x := range side {
			if dissolvePainted(42, 1, x, y, .5) != dissolvePainted(42, 2, x, y, .5) {
				changes++
			}
		}
	}
	if changes < side*side/3 {
		t.Fatal("different layers unexpectedly share their noise field")
	}
}

func TestDissolveOpaqueAndZeroEdgesAndCompoundedOpacity(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 64, 64), color.NRGBA{B: 255, A: 128})
	top := composeTestLayer(image.Rect(0, 0, 64, 64), color.NRGBA{R: 255, A: 255})
	top.BlendMode = "diss"
	seed := uint64(7)
	document := &psdmodel.Document{Width: 64, Height: 64, Layers: []*psdmodel.Layer{bottom, top}}
	result, err := Render(context.Background(), document, Options{DissolveSeed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Pix, top.Pixels.Pix) {
		t.Fatal("opaque Dissolve must be exactly the source pixels")
	}
	top.Opacity = new(float64(0))
	result, err = Render(context.Background(), document, Options{DissolveSeed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Pix, bottom.Pixels.Pix) {
		t.Fatal("zero-opacity Dissolve must preserve the backdrop exactly")
	}
	top.Opacity, top.FillOpacity = new(.5), new(.5)
	top.Mask = &psdmodel.Mask{DefaultColor: new(uint8(128))}
	result, err = Render(context.Background(), document, Options{DissolveSeed: &seed})
	if err != nil {
		t.Fatal(err)
	}
	for y := range 64 {
		for x := range 64 {
			actual := result.NRGBAAt(x, y)
			painted := dissolvePainted(seed, 2, x, y, .5*.5*128/255)
			expected := color.NRGBA{B: 255, A: 128}
			if painted {
				expected = color.NRGBA{R: 255, A: 255}
			}
			if actual != expected {
				t.Fatalf("Dissolve produced partial color or ignored compounded opacity at %d,%d: %v", x, y, actual)
			}
		}
	}
}
