package psdcompose_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdrendered"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func livePlacedFXFixture() (*psdmodel.Document, *psdmodel.Layer) {
	layer := reviewShadowLayer(image.Rect(-2, 1, 8, 7))
	layer.Effects = reviewShadowSet(mixedReviewEffects(false, true)...)
	layer.Placed = &psdmodel.Placed{ID: "fresh-source", Width: 10, Height: 6,
		Transform: []float64{-2, 1, 8, 1, 8, 7, -2, 7}}
	layer.RenderedEffects = psdrendered.New(layer.Pixels, layer.Bounds, layer.Placed)
	layer.Opacity, layer.FillOpacity = new(.61), new(.37)
	layer.Mask = &psdmodel.Mask{Bounds: image.Rect(1, 0, 7, 9),
		DefaultColor: new(uint8(173)), Density: new(uint8(193)), Invert: true}
	layer.Mask.Pixels = image.NewNRGBA(layer.Mask.Bounds)
	for y := layer.Mask.Bounds.Min.Y; y < layer.Mask.Bounds.Max.Y; y++ {
		for x := layer.Mask.Bounds.Min.X; x < layer.Mask.Bounds.Max.X; x++ {
			layer.Mask.Pixels.Pix[layer.Mask.Pixels.PixOffset(x, y)] = byte(x*31 + y*67 + 23)
		}
	}
	background := reviewShadowLayer(image.Rect(0, 0, 12, 10))
	for index := 0; index < len(background.Pixels.Pix); index += 4 {
		copy(background.Pixels.Pix[index:], []byte{73, 119, 211, 111})
	}
	return &psdmodel.Document{Width: 12, Height: 10, Layers: []*psdmodel.Layer{background, layer}}, layer
}

func livePlacedFXOptions() psdcompose.Options {
	return psdcompose.Options{ApproximateDropShadow: true}
}

// This oracle evaluates artwork, shadow and overlay branches with independent scalar associated RGBA.
func TestLivePlacedFXIndependentSourceAndStyleStages(t *testing.T) {
	document, layer := livePlacedFXFixture()
	sourcePixels, maskPixels := bytes.Clone(layer.Pixels.Pix), bytes.Clone(layer.Mask.Pixels.Pix)
	maskAt := func(x, y int) float64 {
		value := byte(173)
		if image.Pt(x, y).In(layer.Mask.Bounds) {
			value = layer.Mask.Pixels.NRGBAAt(x, y).R
		}
		return 1 - float64(value)*193/(255*255)
	}
	for _, global := range []bool{false, true} {
		layer.MaskAsGlobal = new(global)
		shapeAt := func(x, y int) float64 {
			alpha := float64(mixedReviewSourceAt(layer, x, y).A)
			if !global {
				alpha = math.Floor(alpha*maskAt(x, y) + .5)
			}
			return alpha / 255
		}
		for _, grouped := range []bool{false, true} {
			layer.BlendInterior = new(grouped)
			for _, fill := range []float64{0, .37, 1} {
				layer.FillOpacity = new(fill)
				actual, err := psdcompose.Render(t.Context(), document, livePlacedFXOptions())
				if err != nil {
					t.Fatal(err)
				}
				for y := range document.Height {
					for x := range document.Width {
						alpha := 111.0 / 255
						original := reviewShadowPremult{73.0 / 255 * alpha, 119.0 / 255 * alpha,
							211.0 / 255 * alpha, alpha}
						pixel := mixedReviewSourceAt(layer, x, y)
						rgb := [3]float64{float64(pixel.R) / 255, float64(pixel.G) / 255, float64(pixel.B) / 255}
						expected := mixedReviewPixel(original, rgb, shapeAt(x, y), fill, .61, grouped,
							layer.Effects.Items, func(effect psdeffects.Effect) float64 {
								if effect.Angle == 90 {
									return shapeAt(x, y-int(effect.Distance))
								}
								return shapeAt(x+int(effect.Distance), y)
							})
						if global {
							expected = mixedReviewMix(original, expected, maskAt(x, y))
						}
						mixedReviewCompare(t, actual.NRGBAAt(x, y), expected, x, y)
					}
				}
				region := image.Rect(0, 3, 5, 9)
				partial, err := psdcompose.RenderRegion(t.Context(), document, region, livePlacedFXOptions())
				if err != nil {
					t.Fatal(err)
				}
				for y := region.Min.Y; y < region.Max.Y; y++ {
					for x := region.Min.X; x < region.Max.X; x++ {
						if partial.NRGBAAt(x, y) != actual.NRGBAAt(x, y) {
							t.Fatalf("placed mixed effects ROI differs at %d,%d", x, y)
						}
					}
				}
			}
		}
	}
	if !bytes.Equal(sourcePixels, layer.Pixels.Pix) || !bytes.Equal(maskPixels, layer.Mask.Pixels.Pix) ||
		!layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		t.Fatal("placed effects changed borrowed artwork, mask or live provenance")
	}
}

func TestLivePlacedFXExplicitPoliciesProvenanceAndAdvancedGuards(t *testing.T) {
	for _, options := range []psdcompose.Options{{}, {ApproximatePlacedColorOverlay: true}} {
		document, _ := livePlacedFXFixture()
		pixels, err := psdcompose.Render(t.Context(), document, options)
		if pixels != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
			t.Fatalf("placed mixed effects inferred another approximation: %+v: %v", options, err)
		}
	}
	mutations := []struct {
		name   string
		change func(*psdmodel.Layer)
	}{
		{"saved-cache", func(layer *psdmodel.Layer) { layer.RenderedEffects = nil }},
		{"new-backing", func(layer *psdmodel.Layer) { layer.Pixels.Pix = bytes.Clone(layer.Pixels.Pix) }},
		{"moved", func(layer *psdmodel.Layer) { layer.Bounds = layer.Bounds.Add(image.Pt(1, 0)) }},
		{"transform", func(layer *psdmodel.Layer) { layer.Placed.Transform[0]++ }},
		{"multiply-layer", func(layer *psdmodel.Layer) { layer.BlendMode = "mul " }},
		{"vector-mask", func(layer *psdmodel.Layer) { layer.VectorMask = &psdvector.Mask{Version: 3} }},
		{"clipping", func(layer *psdmodel.Layer) { layer.Clipping = true }},
		{"channels", func(layer *psdmodel.Layer) { layer.ChannelRestrictions = []uint32{0} }},
		{"knockout", func(layer *psdmodel.Layer) { layer.Knockout = psdmodel.KnockoutDeep }},
		{"alternate-transparency", func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			document, layer := livePlacedFXFixture()
			mutation.change(layer)
			pixels, err := psdcompose.Render(t.Context(), document, livePlacedFXOptions())
			if pixels != nil || !errors.Is(err, psdcompose.ErrUnsupported) {
				t.Fatalf("advanced or stale placed mixed effects admitted: %v", err)
			}
		})
	}
	document, _ := livePlacedFXFixture()
	options := livePlacedFXOptions()
	options.MaxWorkingBytes = 1
	if pixels, err := psdcompose.Render(t.Context(), document, options); pixels != nil ||
		!errors.Is(err, psdcompose.ErrLimit) {
		t.Fatalf("placed mixed effects bypassed working budget: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if pixels, err := psdcompose.Render(ctx, document, livePlacedFXOptions()); pixels != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled placed mixed effects returned output: %v", err)
	}
}
