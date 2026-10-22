package psdcompose

import (
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestInnerShadowPaintsInsideCoverageAndKeepsFillAndLayerOpacitySeparate(t *testing.T) {
	backdrop := composeTestLayer(image.Rect(0, 0, 32, 24), color.NRGBA{G: 255, A: 255})
	layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
	shadow := coreEffect(psdeffects.InnerShadow)
	shadow.Distance = 3
	layer.Effects = coreEffectSet(shadow)
	document := &psdmodel.Document{Width: 32, Height: 24, Layers: []*psdmodel.Layer{backdrop, layer}}
	for _, fill := range []float64{0, float64(128) / 255, 1} {
		layer.FillOpacity = &fill
		result, err := Render(nil, document)
		if err != nil || result.NRGBAAt(21, 12) != (color.NRGBA{B: 255, A: 255}) ||
			result.NRGBAAt(23, 12) != (color.NRGBA{G: 255, A: 255}) {
			t.Fatalf("inner shadow depended on body Fill or escaped geometry: %v", err)
		}
	}
	layer.Opacity = new(float64(128) / 255)
	result, err := Render(nil, document)
	if err != nil || result.NRGBAAt(21, 12) != (color.NRGBA{G: 127, B: 128, A: 255}) {
		t.Fatalf("inner-shadow layer opacity was applied more than once: %v", err)
	}
}

func TestInnerGlowCenterAndEdgeUseSpatialOpacityBeforeBlend(t *testing.T) {
	layer := composeTestLayer(image.Rect(10, 7, 22, 17), color.NRGBA{R: 255, A: 255})
	glow := coreEffect(psdeffects.InnerGlow)
	glow.Size, glow.Spread, glow.GlowSource = 2, 1, psdeffects.Edge
	layer.Effects = coreEffectSet(glow)
	edge, err := Render(nil, coreEffectDocument(layer))
	if err != nil || edge.NRGBAAt(10, 12) != (color.NRGBA{B: 255, A: 255}) ||
		edge.NRGBAAt(16, 12) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("inner glow ignored its per-pixel edge coverage: %v", err)
	}
	layer.Effects.Items[0].GlowSource = psdeffects.CenterSource
	layer.Effects.Items[0].BlendMode = "mul "
	center, err := Render(nil, coreEffectDocument(layer))
	if err != nil || center.NRGBAAt(16, 12) != (color.NRGBA{A: 255}) ||
		center.NRGBAAt(10, 12) != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("inner glow center source or RGB blend mode was lost: %v", err)
	}
}
