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

func coreEffect(kind psdeffects.Kind) psdeffects.Effect {
	return psdeffects.Effect{Kind: kind, Enabled: true, Color: [3]float64{0, 0, 1}, BlendMode: "norm",
		Opacity: 1, LayerConceals: true, Range: 0.5, Technique: "SfBL", Position: psdeffects.Outside}
}

func coreEffectSet(effects ...psdeffects.Effect) *psdeffects.Set {
	return &psdeffects.Set{Enabled: true, EffectsVisible: true, Scale: 1, GlobalAngle: 90, Items: effects}
}

func coreEffectDocument(layer *psdmodel.Layer) *psdmodel.Document {
	return &psdmodel.Document{Width: 32, Height: 24, Layers: []*psdmodel.Layer{layer}}
}

func TestCoreEffectsPaintExpandedShadowWithoutFillAndOwnResult(t *testing.T) {
	layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Opacity, shadow.Distance = 0.75, 3
	layer.Effects = coreEffectSet(shadow)
	original := bytes.Clone(layer.Pixels.Pix)
	result, err := Render(nil, coreEffectDocument(layer))
	if err != nil {
		t.Fatal(err)
	}
	if result.NRGBAAt(7, 10) != (color.NRGBA{B: 255, A: 191}) ||
		result.NRGBAAt(10, 10) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatal("expanded hard shadow or opaque body differs from independently observed native pixels")
	}
	layer.FillOpacity, layer.Opacity = new(float64(0)), new(0.5)
	result, err = Render(nil, coreEffectDocument(layer))
	if err != nil || result.NRGBAAt(7, 10) != (color.NRGBA{B: 255, A: 96}) ||
		result.NRGBAAt(10, 10) != (color.NRGBA{}) {
		t.Fatalf("Fill-zero body suppressed layer effects or layer opacity was not applied: %v", err)
	}
	result.Pix[0] = 9
	if !bytes.Equal(original, layer.Pixels.Pix) || layer.Effects.Items[0].Opacity != 0.75 {
		t.Fatal("core effect rendering changed source pixels or typed parameters")
	}
}

func TestCoreStrokeKnocksOutBodyBeforeEffectOpacityAndKeepsFillSeparate(t *testing.T) {
	layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
	stroke := coreEffect(psdeffects.Stroke)
	stroke.Position, stroke.Size, stroke.Opacity = psdeffects.Inside, 2, 0.8
	stroke.Color = [3]float64{1, 1, 0}
	layer.Effects = coreEffectSet(stroke)
	layer.FillOpacity = new(float64(128) / 255)
	result, err := Render(nil, coreEffectDocument(layer))
	if err != nil || result.NRGBAAt(10, 7) != (color.NRGBA{R: 255, G: 255, A: 204}) ||
		result.NRGBAAt(16, 12) != (color.NRGBA{R: 255, A: 128}) {
		t.Fatalf("stroke should replace the unfilled shape before painting its eighty-percent color: %v", err)
	}
	layer.Opacity = new(0.5)
	result, err = Render(nil, coreEffectDocument(layer))
	if err != nil || result.NRGBAAt(10, 7) != (color.NRGBA{R: 255, G: 255, A: 102}) ||
		result.NRGBAAt(16, 12) != (color.NRGBA{R: 255, A: 64}) {
		t.Fatalf("layer opacity should affect both stroke and body; Fill should affect only body: %v", err)
	}
}

func TestCoreEffectsStrictlyRejectApproximationAndUncalibratedInteractions(t *testing.T) {
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Distance = 3
	for _, mutate := range []func(*psdmodel.Layer){
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].Size = 0.5 },
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].Distance = 0.5 },
		func(layer *psdmodel.Layer) { layer.Effects.Items[0].BlendMode = "mul " },
		func(layer *psdmodel.Layer) { layer.BlendMode = "mul " },
		func(layer *psdmodel.Layer) { layer.Mask = &psdmodel.Mask{} },
		func(layer *psdmodel.Layer) { layer.VectorMask = &psdvector.Mask{} },
		func(layer *psdmodel.Layer) { layer.TransparencyShapes = new(false) },
		func(layer *psdmodel.Layer) { layer.Clipping = true },
		func(layer *psdmodel.Layer) { layer.PatternTile = image.NewNRGBA(image.Rect(0, 0, 1, 1)) },
	} {
		layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
		layer.Effects = coreEffectSet(shadow)
		mutate(layer)
		document := coreEffectDocument(layer)
		if layer.Clipping {
			base := composeTestLayer(layer.Bounds, color.NRGBA{B: 255, A: 255})
			document.Layers = []*psdmodel.Layer{base, layer}
		}
		if err := Validate(document); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("uncalibrated effect interaction was accepted by Validate: %v", err)
		}
		if output, err := Render(nil, document); !errors.Is(err, ErrUnsupported) || output != nil {
			t.Fatalf("uncalibrated effect interaction returned a partial/default approximation: %v", err)
		}
	}
	layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
	layer.Effects = coreEffectSet(shadow)
	clipped := composeTestLayer(layer.Bounds, color.NRGBA{G: 255, A: 255})
	clipped.Clipping = true
	document := coreEffectDocument(layer)
	document.Layers = append(document.Layers, clipped)
	if _, err := Render(nil, document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an effect on the base of a clipping chain was accepted: %v", err)
	}
}

func TestCoreEffectsRejectPartialGlowButIgnoreHiddenOrDisabledEffects(t *testing.T) {
	layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 128})
	glow := coreEffect(psdeffects.OuterGlow)
	glow.Size, glow.Spread = 2, 1
	layer.Effects = coreEffectSet(glow)
	if err := Validate(coreEffectDocument(layer)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("partially transparent glow shape used an uncalibrated primitive by default: %v", err)
	}
	layer.Effects.Enabled = false
	if _, err := Render(nil, coreEffectDocument(layer)); err != nil {
		t.Fatalf("master-disabled effects should leave the ordinary layer renderable: %v", err)
	}
	layer.Effects.Enabled, layer.Hidden = true, true
	layer.Effects.Items[0].Noise = 0.5
	if _, err := Render(nil, coreEffectDocument(layer)); err != nil {
		t.Fatalf("hidden unsupported effects should not block the visible document: %v", err)
	}
}

func TestCoreEffectBudgetIncludesOwnedShapePlanesAndPeakScratch(t *testing.T) {
	layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
	shadow := coreEffect(psdeffects.DropShadow)
	shadow.Distance = 3
	layer.Effects = coreEffectSet(shadow)
	document := coreEffectDocument(layer)
	// 3072 canvas + 1024 tree + 120 shape + 632 planes + 968 simultaneous scratch.
	if _, err := Render(nil, document, Options{MaxWorkingBytes: 5815}); !errors.Is(err, ErrLimit) {
		t.Fatalf("effect peak scratch escaped the otherwise sufficient final-cache budget: %v", err)
	}
	if _, err := Render(nil, document, Options{MaxWorkingBytes: 5816}); err != nil {
		t.Fatalf("the independently calculated exact working bound should be sufficient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := Render(ctx, document); !errors.Is(err, context.Canceled) || output != nil {
		t.Fatalf("canceled effect render returned partial output: %v", err)
	}
}

func TestCoreEffectsBudgetAlsoIncludesAnotherLayersFeatherCache(t *testing.T) {
	masked := composeTestLayer(image.Rect(0, 0, 10, 10), color.NRGBA{R: 255, A: 255})
	masked.Mask = &psdmodel.Mask{Bounds: masked.Bounds, Feather: new(0.5),
		Pixels:       composeTestLayer(masked.Bounds, color.NRGBA{R: 255, A: 255}).Pixels,
		DefaultColor: new(uint8(0))}
	styled := composeTestLayer(masked.Bounds, color.NRGBA{G: 255, A: 255})
	glow := coreEffect(psdeffects.OuterGlow)
	glow.Size, glow.Spread = 2, 1
	styled.Effects = coreEffectSet(glow)
	document := &psdmodel.Document{Width: 10, Height: 10, Layers: []*psdmodel.Layer{masked, styled}}
	if _, err := Render(nil, document, Options{MaxWorkingBytes: 4800}); !errors.Is(err, ErrLimit) {
		t.Fatalf("other-layer retained feather data was excluded from the effect scratch peak: %v", err)
	}
	if _, err := Render(nil, document, Options{MaxWorkingBytes: 6000}); err != nil {
		t.Fatalf("effects and an unrelated feathered layer failed with sufficient work budget: %v", err)
	}
}
