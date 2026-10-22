package psdcompose

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// unclippedReferenceStack retains eager group evaluation for these ordinary, unclipped fixtures.
func unclippedReferenceStack(
	plan *composePlan, layers []*psdmodel.Layer, backdrop composePixel, x, y int,
) composePixel {
	for _, layer := range layers {
		if layer.Hidden || composeOpacity(layer.Opacity) == 0 {
			continue
		}
		var source composePixel
		if layer.Group {
			initial := composePixel{}
			if layer.BlendMode == "pass" {
				initial = backdrop
			}
			source = unclippedReferenceStack(plan, layer.Children, initial, x, y)
		} else {
			source = plan.source(layer, backdrop, x, y)
		}
		backdrop = plan.paint(layer, source, backdrop, x, y, 1)
	}
	return backdrop
}

func TestMaskedGroupOptimizationPreservesEagerComposition(t *testing.T) {
	for _, vector := range []bool{false, true} {
		for _, mode := range []string{"norm", "pass"} {
			for _, alpha := range []byte{128, 255} {
				for _, sourceAlpha := range []byte{128, 255} {
					for _, variant := range []string{"ordinary", "disabled", "invert", "density"} {
						name := fmt.Sprintf("vector=%t/%s/backdrop=%d/source=%d/%s", vector, mode, alpha, sourceAlpha, variant)
						t.Run(name, func(t *testing.T) {
							verifyMaskedGroupReference(t, vector, mode, alpha, sourceAlpha, variant)
						})
					}
				}
			}
		}
	}
}

func verifyMaskedGroupReference(t *testing.T, vector bool, mode string, alpha, sourceAlpha byte, variant string) {
	t.Helper()
	bounds := image.Rect(0, 0, 8, 4)
	background := composeTestLayer(bounds, color.NRGBA{R: 33, G: 121, B: 177, A: alpha})
	child := composeTestLayer(bounds, color.NRGBA{R: 235, G: 71, B: 99, A: sourceAlpha})
	group := &psdmodel.Layer{Group: true, BlendMode: mode, Children: []*psdmodel.Layer{child},
		Opacity: new(.7), FillOpacity: new(.6)}
	identity := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	background.BlendRanges = []psdmodel.BlendRange{{Source: identity, Destination: identity}}
	if vector {
		group.VectorMask = vectorFeatherTestMask(image.Rect(2, 1, 6, 3))
		group.VectorMask.Width, group.VectorMask.Height = 8, 4
		group.VectorMask.Disabled = variant == "disabled"
		group.VectorMask.Invert = variant == "invert"
		if variant == "density" {
			group.VectorDensity = new(uint8(96))
		}
	} else {
		group.Mask = &psdmodel.Mask{Bounds: image.Rect(2, 1, 6, 3), DefaultColor: new(uint8(0)),
			Pixels:   composeTestLayer(image.Rect(0, 0, 4, 2), color.NRGBA{R: 128, A: 255}).Pixels,
			Disabled: variant == "disabled", Invert: variant == "invert"}
		if variant == "density" {
			group.Mask.Density = new(uint8(96))
		}
	}
	document := &psdmodel.Document{Width: 8, Height: 4, Layers: []*psdmodel.Layer{background, group}}
	plan, err := preparePlan(document, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan.renderContext = t.Context()
	if err := plan.prepareVectors(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Keep the legacy identity-gate and Normal arithmetic in the reference paint path.
	plan.hasCustomRanges = true
	expected := image.NewNRGBA(bounds)
	for y := range 4 {
		for x := range 8 {
			pixel := unclippedReferenceStack(plan, document.Layers, composePixel{}, x, y)
			for component, value := range pixel.color {
				expected.Pix[y*expected.Stride+x*4+component] = composeByte(value)
			}
			expected.Pix[y*expected.Stride+x*4+3] = composeByte(pixel.alpha)
		}
	}
	actual, err := Render(t.Context(), document)
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("masked group optimization changed eager pixels: %v", err)
	}
}
