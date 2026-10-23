package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdrendered"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func placedOverlayFixture() (*psdmodel.Document, *psdmodel.Layer) {
	bounds := image.Rect(2, 1, 9, 6)
	layer := composeTestLayer(bounds, color.NRGBA{})
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			layer.Pixels.SetNRGBA(x, y, color.NRGBA{
				R: byte(31 + x*19), G: byte(227 - y*23), B: byte(x*13 + y*17), A: byte(x*29 + y*7),
			})
		}
	}
	overlay := coreEffect(psdeffects.ColorOverlay)
	overlay.BlendMode, overlay.Opacity = "colr", .18
	overlay.Color = [3]float64{.811767578125, .788238525390625, .725494384765625}
	layer.Effects = coreEffectSet(overlay)
	layer.Placed = &psdmodel.Placed{ID: "live-template-source", Width: 7, Height: 5,
		Transform: []float64{2, 1, 9, 1, 9, 6, 2, 6}}
	layer.RenderedEffects = psdrendered.New(layer.Pixels, layer.Bounds, layer.Placed)
	backdrop := composeTestLayer(image.Rect(0, 0, 11, 8), color.NRGBA{R: 73, G: 109, B: 193, A: 137})
	return &psdmodel.Document{Width: 11, Height: 8, Layers: []*psdmodel.Layer{backdrop, layer}}, layer
}

func ordinaryOverlayReference(document *psdmodel.Document, layer *psdmodel.Layer) *psdmodel.Document {
	referenceLayer := *layer
	referenceLayer.Placed, referenceLayer.RenderedEffects = nil, nil
	reference := *document
	reference.Layers = []*psdmodel.Layer{document.Layers[0], &referenceLayer}
	return &reference
}

func TestPlacedColorOverlayExplicitPolicyMatchesQualifiedRasterStages(t *testing.T) {
	document, layer := placedOverlayFixture()
	input := bytes.Clone(layer.Pixels.Pix)
	placement := layer.Placed
	if pixels, err := Render(nil, document); pixels != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("default rendering admitted placed overlay: %v", err)
	}
	if pixels, err := Render(nil, document, Options{ApproximateDropShadow: true}); pixels != nil ||
		!errors.Is(err, ErrUnsupported) {
		t.Fatalf("drop-shadow policy admitted a different effect family: %v", err)
	}
	reference, err := Render(nil, ordinaryOverlayReference(document, layer))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := Render(nil, document, Options{ApproximatePlacedColorOverlay: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual.Pix, reference.Pix) {
		t.Fatal("fresh placed overlay changed the qualified Normal raster overlay stages")
	}
	if layer.Placed != placement || !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) ||
		!bytes.Equal(input, layer.Pixels.Pix) {
		t.Fatal("rendering discarded placement identity or modified caller pixels")
	}
	actual.Pix[0] ^= 255
	if !bytes.Equal(input, layer.Pixels.Pix) {
		t.Fatal("owned output aliases caller pixels")
	}
}

func TestPlacedColorOverlayMaskFillOpacityAndRegionMatchRaster(t *testing.T) {
	for _, global := range []*bool{nil, new(false), new(true)} {
		for _, interior := range []bool{false, true} {
			document, layer := placedOverlayFixture()
			layer.FillOpacity, layer.Opacity = new(.27), new(.63)
			layer.BlendInterior, layer.MaskAsGlobal = &interior, global
			maskPixels := image.NewNRGBA(image.Rect(3, 0, 8, 7))
			for y := maskPixels.Rect.Min.Y; y < maskPixels.Rect.Max.Y; y++ {
				for x := maskPixels.Rect.Min.X; x < maskPixels.Rect.Max.X; x++ {
					maskPixels.SetNRGBA(x, y, color.NRGBA{R: byte(x*31 + y*11), A: 255})
				}
			}
			density, outside, feather := uint8(193), uint8(61), .75
			layer.Mask = &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels, Density: &density,
				DefaultColor: &outside, Feather: &feather, Invert: true}
			original := bytes.Clone(maskPixels.Pix)
			reference, err := Render(nil, ordinaryOverlayReference(document, layer))
			if err != nil {
				t.Fatal(err)
			}
			option := Options{ApproximatePlacedColorOverlay: true}
			actual, err := Render(nil, document, option)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(actual.Pix, reference.Pix) || !bytes.Equal(maskPixels.Pix, original) {
				t.Fatalf("mask/Fill/opacity stages differ: global=%v interior=%t", global, interior)
			}
			region := image.Rect(4, 2, 8, 5)
			regional, err := RenderRegion(nil, document, region, option)
			if err != nil {
				t.Fatal(err)
			}
			for y := region.Min.Y; y < region.Max.Y; y++ {
				for x := region.Min.X; x < region.Max.X; x++ {
					if regional.NRGBAAt(x, y) != actual.NRGBAAt(x, y) {
						t.Fatalf("placed overlay ROI differs at (%d,%d)", x, y)
					}
				}
			}
		}
	}
}

func TestPlacedColorOverlayRequiresFreshIdentityAndLimitedInteractions(t *testing.T) {
	mutations := []struct {
		name   string
		change func(*psdmodel.Layer)
	}{
		{"cached", func(layer *psdmodel.Layer) { layer.RenderedEffects = nil }},
		{"new-placement", func(layer *psdmodel.Layer) { layer.Placed = &psdmodel.Placed{ID: layer.Placed.ID} }},
		{"changed-transform", func(layer *psdmodel.Layer) { layer.Placed.Transform[0]++ }},
		{"new-pixels", func(layer *psdmodel.Layer) { layer.Pixels = image.NewNRGBA(layer.Pixels.Rect) }},
		{"new-backing", func(layer *psdmodel.Layer) { layer.Pixels.Pix = bytes.Clone(layer.Pixels.Pix) }},
		{"changed-bounds", func(layer *psdmodel.Layer) { layer.Bounds = layer.Bounds.Add(image.Pt(1, 0)) }},
		{"layer-blend", func(layer *psdmodel.Layer) { layer.BlendMode = "mul " }},
		{"vector-mask", func(layer *psdmodel.Layer) { layer.VectorMask = &psdvector.Mask{} }},
		{"clipping", func(layer *psdmodel.Layer) { layer.Clipping = true }},
		{"blend-if", func(layer *psdmodel.Layer) {
			layer.BlendRanges = []psdmodel.BlendRange{{Source: psdmodel.BlendSlider{WhiteLow: 128, WhiteHigh: 255}}}
		}},
		{"channels", func(layer *psdmodel.Layer) { layer.ChannelRestrictions = []uint32{1} }},
		{"knockout", func(layer *psdmodel.Layer) { layer.Knockout = psdmodel.KnockoutDeep }},
		{"alternate-shape", func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) }},
		{"mixed-effects", func(layer *psdmodel.Layer) {
			layer.Effects.Items = append(layer.Effects.Items, coreEffect(psdeffects.DropShadow))
		}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			document, layer := placedOverlayFixture()
			mutation.change(layer)
			pixels, err := Render(nil, document, Options{ApproximatePlacedColorOverlay: true})
			if pixels != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unqualified placed overlay returned pixels: %v", err)
			}
		})
	}
}

func TestPlacedColorOverlayKeepsExactQuotaAndCancellation(t *testing.T) {
	document, layer := placedOverlayFixture()
	option := Options{ApproximatePlacedColorOverlay: true}
	plan, err := preparePlan(document, []Options{option})
	if err != nil {
		t.Fatal(err)
	}
	option.MaxWorkingBytes = plan.workingBytes
	if pixels, err := Render(nil, document, option); err != nil || pixels == nil {
		t.Fatalf("exact placed overlay budget rejected: %v", err)
	}
	option.MaxWorkingBytes--
	if pixels, err := Render(nil, document, option); pixels != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("placed overlay exceeded allocation budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pixels, err := Render(ctx, document, Options{ApproximatePlacedColorOverlay: true}); pixels != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled placed overlay returned partial output: %v", err)
	}
	if !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		t.Fatal("budget or cancellation failure modified placement")
	}
}

func TestPlacedColorOverlayDoesNotAdmitClippingBase(t *testing.T) {
	document, layer := placedOverlayFixture()
	clipped := composeTestLayer(layer.Bounds, color.NRGBA{G: 211, A: 137})
	clipped.Clipping = true
	document.Layers = append(document.Layers, clipped)
	if pixels, err := Render(nil, document, Options{ApproximatePlacedColorOverlay: true}); pixels != nil ||
		!errors.Is(err, ErrUnsupported) {
		t.Fatalf("placed overlay on a clipping base was admitted: %v", err)
	}
}
