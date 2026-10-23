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
	"github.com/gopkg-dev/psdkit/psdvector"
)

func TestEffectsDisabledMasksMatchUnmaskedPixelsAndRegion(t *testing.T) {
	for _, family := range []psdeffects.Kind{
		psdeffects.DropShadow, psdeffects.OuterGlow, psdeffects.InnerShadow,
		psdeffects.InnerGlow, psdeffects.Stroke, psdeffects.ColorOverlay,
	} {
		t.Run(string(family), func(t *testing.T) {
			layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
			effect := coreEffect(family)
			effect.Size, effect.Distance = 2, 3
			if family == psdeffects.OuterGlow {
				effect.Spread = 1
			}
			layer.Effects = coreEffectSet(effect)
			document := coreEffectDocument(layer)
			expected, err := Render(t.Context(), document)
			if err != nil {
				t.Fatal(err)
			}
			maskPixels := composeTestLayer(layer.Bounds, color.NRGBA{A: 255}).Pixels
			original := bytes.Clone(maskPixels.Pix)
			layer.Mask = &psdmodel.Mask{Bounds: layer.Bounds, Pixels: maskPixels, Disabled: true,
				DefaultColor: new(uint8(0)), Density: new(uint8(193)), Feather: new(float64(99))}
			layer.VectorMask = &psdvector.Mask{Version: 3, Width: document.Width, Height: document.Height,
				Disabled: true, HasInitialFill: true}
			layer.VectorFeather = new(float64(99))
			actual, err := Render(t.Context(), document)
			if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
				t.Fatalf("disabled masks changed effect pixels: %v", err)
			}
			region := image.Rect(7, 5, 25, 20)
			cropped, err := RenderRegion(t.Context(), document, region)
			if err != nil {
				t.Fatal(err)
			}
			for y := region.Min.Y; y < region.Max.Y; y++ {
				for x := region.Min.X; x < region.Max.X; x++ {
					if cropped.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
						t.Fatalf("disabled-mask region differs at %d,%d", x, y)
					}
				}
			}
			if !bytes.Equal(maskPixels.Pix, original) || !layer.Mask.Disabled || !layer.VectorMask.Disabled {
				t.Fatal("rendering mutated retained masks")
			}
			if output, err := Render(t.Context(), document, Options{MaxWorkingBytes: 1}); output != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("disabled masks bypassed the effect budget: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if output, err := Render(ctx, document); output != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled render returned pixels: %v", err)
			}
			layer.VectorMask.Disabled = false
			layer.VectorFeather = nil
			output, err := Render(t.Context(), document)
			if family == psdeffects.ColorOverlay {
				if err != nil || !bytes.Equal(output.Pix, make([]byte, len(output.Pix))) {
					t.Fatalf("active zero-fill vector did not mask the pure Color Overlay: %v", err)
				}
			} else if output != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("active vector mask was accepted with another effect family: %v", err)
			}
		})
	}
}

func TestEffectsDisabledVectorPreservesExplicitPoliciesAndSourceIdentity(t *testing.T) {
	document, layer := placedOverlayFixture()
	options := Options{ApproximatePlacedColorOverlay: true}
	expected, err := Render(t.Context(), document, options)
	if err != nil {
		t.Fatal(err)
	}
	layer.VectorMask = &psdvector.Mask{Version: 3, Disabled: true}
	actual, err := Render(t.Context(), document, options)
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("disabled vector blocked explicit placed overlay: %v", err)
	}
	if output, err := Render(t.Context(), document); output != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("disabled vector silently enabled placed-overlay approximation: %v", err)
	}
	layer.RenderedEffects = nil
	if output, err := Render(t.Context(), document, options); output != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("disabled vector bypassed fresh source identity: %v", err)
	}
	layer = composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Size, shadow.Distance = .5, .5
	layer.Effects = coreEffectSet(shadow)
	document = coreEffectDocument(layer)
	options = Options{ApproximateDropShadow: true}
	expected, err = Render(t.Context(), document, options)
	if err != nil {
		t.Fatal(err)
	}
	layer.VectorMask = &psdvector.Mask{Version: 3, Disabled: true}
	actual, err = Render(t.Context(), document, options)
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("disabled vector blocked explicit shadow: %v", err)
	}
	if output, err := Render(t.Context(), document); output != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("disabled vector silently enabled fractional shadow: %v", err)
	}
	layer.VectorMask.Disabled = false
	layer.VectorMask.Width, layer.VectorMask.Height = document.Width, document.Height
	if output, err := Render(t.Context(), document, options); err != nil ||
		!bytes.Equal(output.Pix, make([]byte, len(expected.Pix))) {
		t.Fatalf("active zero-coverage vector did not mask the explicit shadow: %v", err)
	}
}
