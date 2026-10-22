package psdcompose

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func singlePassNativeGroup(source color.NRGBA, mode string, childOpacity, groupOpacity byte) *psdmodel.Layer {
	child := composeTestLayer(image.Rect(0, 0, 1, 1), source)
	child.BlendMode = mode
	child.Opacity = new(float64(childOpacity) / 255)
	return &psdmodel.Layer{
		Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{child},
		Opacity: new(float64(groupOpacity) / 255),
		BlendRanges: []psdmodel.BlendRange{{
			Source:      psdmodel.BlendSlider{BlackLow: 24, BlackHigh: 100, WhiteLow: 175, WhiteHigh: 230},
			Destination: psdmodel.BlendSlider{BlackLow: 10, BlackHigh: 75, WhiteLow: 165, WhiteHigh: 245},
		}},
	}
}

func TestSinglePassRangesNativeGrayAndSourceStages(t *testing.T) {
	// These anchors are actual pixels from the independent holdout and seed2 Photoshop oracles.
	for _, test := range []struct {
		name                       string
		mode                       string
		backdrop, source, expected color.NRGBA
		childOpacity, groupOpacity byte
	}{
		{
			name: "holdout normal at 4,3", mode: "norm",
			backdrop:     color.NRGBA{R: 43, G: 149, B: 82, A: 188},
			source:       color.NRGBA{R: 191, G: 227, B: 13, A: 252},
			expected:     color.NRGBA{R: 100, G: 179, B: 55, A: 209},
			childOpacity: 191, groupOpacity: 161,
		},
		{
			name: "holdout screen at 13,1", mode: "scrn",
			backdrop:     color.NRGBA{R: 136, G: 4, B: 39, A: 74},
			source:       color.NRGBA{R: 203, G: 224, B: 154, A: 230},
			expected:     color.NRGBA{R: 159, G: 74, B: 77, A: 95},
			childOpacity: 191, groupOpacity: 161,
		},
		{
			name: "seed2 difference at 11,2", mode: "diff",
			backdrop:     color.NRGBA{R: 242, G: 86, B: 238, A: 245},
			source:       color.NRGBA{R: 6, G: 120, B: 32, A: 169},
			expected:     color.NRGBA{R: 239, G: 76, B: 230, A: 247},
			childOpacity: 158, groupOpacity: 120,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			group := singlePassNativeGroup(test.source, test.mode, test.childOpacity, test.groupOpacity)
			bottom := composeTestLayer(image.Rect(0, 0, 1, 1), test.backdrop)
			before := bytes.Clone(group.Children[0].Pixels.Pix)
			beforeRange := group.BlendRanges[0]
			actual := composeTestPixel(t, composeTestDocument(bottom, group))
			alphaDifference := int(actual.A) - int(test.expected.A)
			if alphaDifference < -2 || alphaDifference > 2 {
				t.Fatalf("native alpha = %v, expected %v", actual, test.expected)
			}
			for component, value := range []byte{actual.R, actual.G, actual.B} {
				expected := []byte{test.expected.R, test.expected.G, test.expected.B}[component]
				difference := int(value)*int(actual.A) - int(expected)*int(test.expected.A)
				if difference < -510 || difference > 510 {
					t.Fatalf("native premultiplied component %d = %v, expected %v", component, actual, test.expected)
				}
			}
			if !bytes.Equal(before, group.Children[0].Pixels.Pix) || group.BlendRanges[0] != beforeRange {
				t.Fatal("rendering changed the borrowed child or ranges")
			}
		})
	}
}

func TestSinglePassRangesRejectsNativeComplexCounterexamples(t *testing.T) {
	// Source structures and controls match the failed prospective seed2 native cases.
	for _, mode := range []string{"multi-normal-screen", "nested-normal-screen-normal", "color-dodge-fill37"} {
		t.Run(mode, func(t *testing.T) {
			group := singlePassNativeGroup(color.NRGBA{R: 253, G: 205, B: 148, A: 196}, "scrn", 158, 120)
			switch mode {
			case "multi-normal-screen":
				middle := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 134, G: 77, B: 153, A: 180})
				middle.Opacity = new(110.0 / 255)
				group.Children = append([]*psdmodel.Layer{middle}, group.Children...)
			case "nested-normal-screen-normal":
				group.Children = []*psdmodel.Layer{{
					Group: true, BlendMode: "norm", Opacity: new(181.0 / 255),
					Children: group.Children,
				}}
			default:
				group.Children[0].BlendMode = "div "
				group.Children[0].FillOpacity = new(95.0 / 255)
			}
			err := Validate(composeTestDocument(group))
			var typed *Error
			if !errors.Is(err, ErrUnsupported) || !errors.As(err, &typed) ||
				typed.Path != "document.layers[0].blendRanges" {
				t.Fatalf("native counterexample was approximated: %v", err)
			}
		})
	}
}

func TestSinglePassRangesKnockoutAndCachedProvenanceStayRestricted(t *testing.T) {
	group := singlePassNativeGroup(color.NRGBA{R: 203, G: 224, B: 154, A: 230}, "scrn", 191, 161)
	knockout := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 200, A: 128})
	knockout.Knockout = psdmodel.KnockoutShallow
	document := composeTestDocument(group, knockout)
	var typed *Error
	if err := Validate(document); !errors.Is(err, ErrUnsupported) || !errors.As(err, &typed) ||
		typed.Path != "document.layers[0].blendRanges" {
		t.Fatalf("a later Knockout sibling admitted the new source stage: %v", err)
	}
	group.Children[0].BlendMode = "norm"
	plan, err := preparePlan(document, nil)
	if err != nil || plan.passRanges[group] != passRangeLegacyNormal {
		t.Fatalf("Knockout context changed the legacy Normal stage: %v", err)
	}
	document.Layers = document.Layers[:1]
	document.Warnings = []string{"Text layer is rendered from its cached raster"}
	group.Children[0].BlendMode = "scrn"
	if err := Validate(document); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("cached source provenance admitted the new raster subset: %v", err)
	}
}

func TestSinglePassRangesRegionsKeepOriginsBudgetsAndCancellation(t *testing.T) {
	group := singlePassNativeGroup(color.NRGBA{R: 203, G: 224, B: 154, A: 230}, "scrn", 191, 161)
	rectangle := image.Rect(123451, 299994, 123454, 299996)
	group.Children[0].Bounds = rectangle
	group.Children[0].Pixels = composeTestLayer(rectangle, color.NRGBA{R: 203, G: 224, B: 154, A: 230}).Pixels
	mask := composeTestLayer(rectangle, color.NRGBA{R: 255, A: 255}).Pixels
	group.Mask = &psdmodel.Mask{Bounds: rectangle, Pixels: mask, DefaultColor: new(uint8(0))}
	bottom := composeTestLayer(rectangle, color.NRGBA{R: 136, G: 4, B: 39, A: 74})
	document := &psdmodel.Document{Width: 300000, Height: 300000, Layers: []*psdmodel.Layer{bottom, group}}
	options := Options{MaxWorkingBytes: 64 << 10}
	if _, err := Render(nil, document, options); !errors.Is(err, ErrLimit) {
		t.Fatalf("full canvas incorrectly fit the region budget: %v", err)
	}
	window, err := RenderRegion(nil, document, rectangle, options)
	if err != nil || window.Rect != rectangle || len(window.Pix) != 24 {
		t.Fatalf("region did not preserve absolute geometry and bounded allocation: %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderRegion(canceled, document, rectangle, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled range rendering proceeded: %v", err)
	}
	localGroup := singlePassNativeGroup(color.NRGBA{R: 203, G: 224, B: 154, A: 230}, "scrn", 191, 161)
	localGroup.Children[0] = composeTestLayer(image.Rect(0, 0, 11, 9), color.NRGBA{R: 203, G: 224, B: 154, A: 230})
	localGroup.Children[0].BlendMode = "scrn"
	localGroup.Children[0].Opacity = new(191.0 / 255)
	localBottom := composeTestLayer(image.Rect(0, 0, 11, 9), color.NRGBA{R: 136, G: 4, B: 39, A: 74})
	compareComposeRegions(t, &psdmodel.Document{Width: 11, Height: 9, Layers: []*psdmodel.Layer{localBottom, localGroup}})
}
