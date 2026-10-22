package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestColorOverlayUsesNativeBackgroundFillAndOriginalShapeStages(t *testing.T) {
	// RGB and alpha references were measured in Photoshop color-overlay-native controls.
	for _, item := range []struct {
		name                       string
		mode                       string
		source, backdrop, expected color.NRGBA
		fill, opacity, effect      float64
	}{
		{"multiply with partial Fill", "mul ", color.NRGBA{A: 255}, color.NRGBA{R: 127, G: 91, B: 96, A: 255},
			color.NRGBA{R: 95, G: 61, B: 74, A: 255}, 105.0 / 255, 120.0 / 255, .63},
		{"screen with partial Fill", "scrn", color.NRGBA{A: 255}, color.NRGBA{R: 127, G: 91, B: 96, A: 255},
			color.NRGBA{R: 130, G: 84, B: 117, A: 255}, 74.0 / 255, 186.0 / 255, .37},
		{"difference opacity is special Fill", "diff", color.NRGBA{R: 127, G: 128, B: 129, A: 18}, color.NRGBA{},
			color.NRGBA{R: 18, G: 91, B: 4, A: 18}, 1, 1, .63},
		{"Fill zero preserves partial shape", "norm", color.NRGBA{R: 127, G: 128, B: 129, A: 18}, color.NRGBA{},
			color.NRGBA{R: 173, G: 59, B: 211, A: 11}, 0, 1, .63},
	} {
		t.Run(item.name, func(t *testing.T) {
			bounds := image.Rect(0, 0, 1, 1)
			layer := composeTestLayer(bounds, item.source)
			overlay := coreEffect(psdeffects.ColorOverlay)
			overlay.Color, overlay.Opacity, overlay.BlendMode = [3]float64{173.0 / 255, 59.0 / 255, 211.0 / 255},
				item.effect, item.mode
			layer.Effects, layer.FillOpacity, layer.Opacity = coreEffectSet(overlay),
				new(item.fill), new(item.opacity)
			layers := []*psdmodel.Layer{layer}
			if item.backdrop.A != 0 {
				layers = append([]*psdmodel.Layer{composeTestLayer(bounds, item.backdrop)}, layers...)
			}
			pixels, err := Render(nil, &psdmodel.Document{Width: 1, Height: 1, Layers: layers})
			if err != nil {
				t.Fatal(err)
			}
			actual := pixels.NRGBAAt(0, 0)
			if actual.A != item.expected.A || effectByteDifference(actual.R, item.expected.R) > 2 ||
				effectByteDifference(actual.G, item.expected.G) > 2 || effectByteDifference(actual.B, item.expected.B) > 2 {
				t.Fatalf("native Color Overlay stage differs: %v, %v, expected %v", pixels, err, item.expected)
			}
		})
	}
}

func effectByteDifference(left, right byte) int {
	if left < right {
		return int(right - left)
	}
	return int(left - right)
}

func TestColorOverlayOwnsResultAndChargesCoverageAtExactBudget(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 190, G: 27, B: 61, A: 93})
	overlay := coreEffect(psdeffects.ColorOverlay)
	overlay.Opacity = .47
	layer.Effects = coreEffectSet(overlay)
	document := &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{layer}}
	original := bytes.Clone(layer.Pixels.Pix)
	// One RGBA output, tree metadata, one source alpha byte and one owned effect plane with metadata.
	if pixels, err := Render(nil, document, Options{MaxWorkingBytes: 1541}); !errors.Is(err, ErrLimit) || pixels != nil {
		t.Fatalf("overlay escaped peak coverage budget: %v", err)
	}
	pixels, err := Render(nil, document, Options{MaxWorkingBytes: 1542})
	if err != nil {
		t.Fatalf("exact Color Overlay working bound failed: %v", err)
	}
	pixels.Pix[0] = 1
	if !bytes.Equal(layer.Pixels.Pix, original) || layer.Effects.Items[0].Opacity != .47 {
		t.Fatal("Color Overlay borrowed or changed source data")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Render(ctx, document); !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("canceled Color Overlay render returned pixels: %v", err)
	}
}

func TestColorOverlaySourceFirstInstanceIsNativeTopmost(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	top, bottom := coreEffect(psdeffects.ColorOverlay), coreEffect(psdeffects.ColorOverlay)
	top.Color, top.Opacity = [3]float64{173.0 / 255, 59.0 / 255, 211.0 / 255}, .63
	bottom.Color, bottom.Opacity, bottom.BlendMode = [3]float64{20.0 / 255, 220.0 / 255, 80.0 / 255}, .37, "mul "
	layer.Effects = coreEffectSet(top, bottom)
	pixels, err := Render(nil, &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{layer}})
	if err != nil {
		t.Fatal(err)
	}
	actual := pixels.NRGBAAt(0, 0)
	// Photoshop's white-cell sample is 171,126,203,255 with the same ordered source effects.
	if actual.A != 255 || effectByteDifference(actual.R, 171) > 2 || effectByteDifference(actual.G, 126) > 2 ||
		effectByteDifference(actual.B, 203) > 2 {
		t.Fatalf("multiple Color Overlay order differs from native: %v", actual)
	}
}
