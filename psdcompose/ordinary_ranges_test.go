package psdcompose

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func ordinaryRangeNativeDocument(
	mode string, backdrop, source color.NRGBA, opacity, fill byte,
	sourceRange, destinationRange psdmodel.BlendSlider,
) *psdmodel.Document {
	rectangle := image.Rect(0, 0, 1, 1)
	bottom := composeTestLayer(rectangle, backdrop)
	top := composeTestLayer(rectangle, source)
	top.BlendMode = mode
	top.Opacity, top.FillOpacity = new(float64(opacity)/255), new(float64(fill)/255)
	top.BlendRanges = []psdmodel.BlendRange{{Source: sourceRange, Destination: destinationRange}}
	top.Mask = &psdmodel.Mask{
		Bounds: rectangle, Pixels: composeTestLayer(rectangle, color.NRGBA{R: 255, A: 255}).Pixels,
		DefaultColor: new(uint8(0)),
	}
	return composeTestDocument(bottom, top)
}

func assertOrdinaryNativePixel(t *testing.T, actual, expected color.NRGBA) {
	t.Helper()
	alphaDifference := int(actual.A) - int(expected.A)
	if alphaDifference < -2 || alphaDifference > 2 {
		t.Fatalf("native alpha = %v, want %v", actual, expected)
	}
	for component, value := range []byte{actual.R, actual.G, actual.B} {
		expectedValue := []byte{expected.R, expected.G, expected.B}[component]
		difference := int(value)*int(actual.A) - int(expectedValue)*int(expected.A)
		if difference < -510 || difference > 510 {
			t.Fatalf("raw native premultiplied component %d = %v, want %v", component, actual, expected)
		}
	}
}

func TestOrdinaryRangesNativeSourceFillAndDestinationCoverage(t *testing.T) {
	identity := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	seed3Source := psdmodel.BlendSlider{BlackLow: 24, BlackHigh: 100, WhiteLow: 175, WhiteHigh: 230}
	seed3Destination := psdmodel.BlendSlider{BlackLow: 10, BlackHigh: 75, WhiteLow: 165, WhiteHigh: 245}
	holdoutSource := psdmodel.BlendSlider{BlackLow: 17, BlackHigh: 92, WhiteLow: 183, WhiteHigh: 241}
	holdoutDestination := psdmodel.BlendSlider{BlackLow: 5, BlackHigh: 69, WhiteLow: 178, WhiteHigh: 250}
	// Native pixels independently distinguish coverage, source Fill and destination Fill hypotheses.
	for _, test := range []struct {
		name, mode                    string
		backdrop, source, expected    color.NRGBA
		opacity, fill                 byte
		sourceRange, destinationRange psdmodel.BlendSlider
	}{
		{
			name: "difference source seed3 at 10,3", mode: "diff", opacity: 186, fill: 255,
			backdrop:    color.NRGBA{R: 135, G: 97, B: 99, A: 246},
			source:      color.NRGBA{R: 202, G: 211, B: 125, A: 206},
			expected:    color.NRGBA{R: 69, G: 55, B: 58, A: 249},
			sourceRange: seed3Source, destinationRange: identity,
		},
		{
			name: "difference destination seed3 at 2,2", mode: "diff", opacity: 186, fill: 255,
			backdrop:    color.NRGBA{R: 183, G: 217, B: 85, A: 224},
			source:      color.NRGBA{R: 247, G: 64, B: 217, A: 151},
			expected:    color.NRGBA{R: 151, G: 192, B: 104, A: 233},
			sourceRange: seed3Source, destinationRange: seed3Destination,
		},
		{
			name: "hard mix Fill47 holdout at 4,6", mode: "hMix", opacity: 156, fill: 120,
			backdrop:    color.NRGBA{R: 244, G: 40, B: 254, A: 216},
			source:      color.NRGBA{R: 53, G: 13, B: 135, A: 204},
			expected:    color.NRGBA{R: 244, G: 25, B: 253, A: 218},
			sourceRange: holdoutSource, destinationRange: identity,
		},
		{
			name: "color burn Fill47 both holdout at 4,6", mode: "idiv", opacity: 156, fill: 120,
			backdrop:    color.NRGBA{R: 244, G: 40, B: 254, A: 216},
			source:      color.NRGBA{R: 53, G: 13, B: 135, A: 204},
			expected:    color.NRGBA{R: 242, G: 25, B: 253, A: 219},
			sourceRange: holdoutSource, destinationRange: holdoutDestination,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := ordinaryRangeNativeDocument(test.mode, test.backdrop, test.source, test.opacity,
				test.fill, test.sourceRange, test.destinationRange)
			before, err := json.Marshal(document, json.Deterministic(true))
			if err != nil {
				t.Fatal(err)
			}
			assertOrdinaryNativePixel(t, composeTestPixel(t, document), test.expected)
			region, err := RenderRegion(context.Background(), document, image.Rect(0, 0, 1, 1))
			if err != nil {
				t.Fatal(err)
			}
			assertOrdinaryNativePixel(t, region.NRGBAAt(0, 0), test.expected)
			after, err := json.Marshal(document, json.Deterministic(true))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("full and region rendering changed borrowed sources: %v", err)
			}
		})
	}
}

func TestOrdinaryRangesUncalibratedContextKeepsPriorPixel(t *testing.T) {
	for _, contextName := range []string{"cached raster warning", "isolated group", "disabled saved mask"} {
		t.Run(contextName, func(t *testing.T) {
			document := ordinaryRangeNativeDocument("diff", color.NRGBA{R: 135, G: 97, B: 99, A: 246},
				color.NRGBA{R: 202, G: 211, B: 125, A: 206}, 186, 255,
				psdmodel.BlendSlider{BlackLow: 24, BlackHigh: 100, WhiteLow: 175, WhiteHigh: 230},
				psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255})
			switch contextName {
			case "cached raster warning":
				document.Warnings = []string{"Text layer is rendered from its cached raster"}
			case "isolated group":
				document.Layers = []*psdmodel.Layer{{Group: true, BlendMode: "norm", Children: document.Layers}}
			case "disabled saved mask":
				document.Layers[1].Mask.Disabled = true
			}
			actual, err := Render(context.Background(), document)
			if err != nil {
				t.Fatal(err)
			}
			// This byte expectation is bound to the unchanged pre-stage production renderer.
			if actual.NRGBAAt(0, 0) != (color.NRGBA{R: 113, G: 104, B: 75, A: 249}) {
				t.Fatalf("previous uncalibrated range pixel changed: %v", actual.NRGBAAt(0, 0))
			}
		})
	}
}

func TestOrdinaryRangesActiveEffectsKeepUnsupportedContract(t *testing.T) {
	document := ordinaryRangeNativeDocument("norm", color.NRGBA{A: 255}, color.NRGBA{R: 202, A: 206},
		186, 255, psdmodel.BlendSlider{BlackLow: 24, BlackHigh: 100, WhiteLow: 175, WhiteHigh: 230},
		psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255})
	document.Layers[1].Effects = coreEffectSet(coreEffect(psdeffects.ColorOverlay))
	var typed *Error
	if err := Validate(document); !errors.Is(err, ErrUnsupported) || !errors.As(err, &typed) ||
		typed.Path != "document.layers[1].effects" {
		t.Fatalf("active effect and own ranges lost their unsupported contract: %v", err)
	}
}
