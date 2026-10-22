package psdcompose

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func knockoutTestLayer(value color.NRGBA, mode psdmodel.Knockout, fill float64) *psdmodel.Layer {
	layer := composeTestLayer(image.Rect(0, 0, 1, 1), value)
	layer.Knockout, layer.FillOpacity = mode, new(fill)
	return layer
}

func TestKnockoutRootFillZeroBackgroundAndTransparency(t *testing.T) {
	for _, mode := range []psdmodel.Knockout{psdmodel.KnockoutShallow, psdmodel.KnockoutDeep} {
		for _, background := range []bool{false, true} {
			bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, A: 255})
			bottom.Background = background
			middle := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255})
			target := knockoutTestLayer(color.NRGBA{B: 255, A: 255}, mode, 0)
			expected := color.NRGBA{}
			if background {
				expected = color.NRGBA{G: 255, A: 255}
			}
			if actual := composeTestPixel(t, composeTestDocument(bottom, middle, target)); actual != expected {
				t.Fatalf("mode=%d background=%t fill0=%v expected=%v", mode, background, actual, expected)
			}
			target.FillOpacity = new(.5)
			expected = color.NRGBA{B: 255, A: 128}
			if background {
				expected = color.NRGBA{G: 128, B: 128, A: 255}
			}
			if actual := composeTestPixel(t, composeTestDocument(bottom, middle, target)); actual != expected {
				t.Fatalf("mode=%d background=%t fill50=%v expected=%v", mode, background, actual, expected)
			}
		}
	}
}

func TestKnockoutPassGroupShallowAndDeepTargets(t *testing.T) {
	background := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 255, A: 255})
	background.Background = true
	outside := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, A: 255})
	body := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255})
	target := knockoutTestLayer(color.NRGBA{A: 255}, psdmodel.KnockoutShallow, 0)
	group := &psdmodel.Layer{Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{body, target}}
	document := composeTestDocument(background, outside, group)
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("shallow group did not reveal group backdrop: %v", actual)
	}
	target.Knockout = psdmodel.KnockoutDeep
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{B: 255, A: 255}) {
		t.Fatalf("deep group did not reveal document Background: %v", actual)
	}
	background.Background = false
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{}) {
		t.Fatalf("ordinary opaque bottom was mistaken for Background: %v", actual)
	}
	group.BlendMode = "norm"
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("isolated group did not retain its boundary: %v", actual)
	}
}

func TestKnockoutCoverageFillAndVisibility(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255})
	target := knockoutTestLayer(color.NRGBA{B: 255, A: 128}, psdmodel.KnockoutDeep, 0)
	document := composeTestDocument(bottom, target)
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{R: 255, A: 127}) {
		t.Fatalf("saved pixel alpha did not shape Fill0 hole: %v", actual)
	}
	target.Opacity = new(.5)
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{R: 255, A: 191}) {
		t.Fatalf("layer opacity did not fade hole: %v", actual)
	}
	target.Hidden = true
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("hidden Knockout changed backdrop: %v", actual)
	}
	target.Hidden, target.Opacity = false, nil
	target.Mask = &psdmodel.Mask{DefaultColor: new(uint8(0))}
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("zero mask did not suppress hole: %v", actual)
	}
}

func TestKnockoutUnknownAndStructuralValidation(t *testing.T) {
	layer := knockoutTestLayer(color.NRGBA{A: 255}, psdmodel.Knockout(255), 0)
	if err := Validate(composeTestDocument(layer)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown Knockout setting silently ignored: %v", err)
	}
	layer.Hidden = true
	if err := Validate(composeTestDocument(layer)); err != nil {
		t.Fatalf("hidden unknown Knockout setting blocks visible content: %v", err)
	}
	layer.Hidden, layer.Knockout = false, psdmodel.KnockoutNone
	group := &psdmodel.Layer{Group: true, Background: true, Children: []*psdmodel.Layer{layer}}
	if err := Validate(composeTestDocument(group)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("group accepted as document Background: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, composeTestDocument(layer)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Knockout render: %v", err)
	}
}

func TestKnockoutTraversalPreservesUnchangedClippingGroups(t *testing.T) {
	for _, pass := range []bool{false, true} {
		for _, blendClipped := range []bool{false, true} {
			bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 20, G: 80, B: 180, A: 128})
			child := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 220, G: 40, B: 60, A: 128})
			child.BlendMode = "mul "
			base := &psdmodel.Layer{Group: true, Children: []*psdmodel.Layer{child}, BlendMode: "norm",
				Opacity: new(.5), Mask: &psdmodel.Mask{DefaultColor: new(uint8(128))},
				BlendClipped: new(blendClipped)}
			if pass {
				base.BlendMode = "pass"
			}
			clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 40, G: 200, B: 80, A: 192})
			clipped.Clipping, clipped.BlendMode = true, "scrn"
			document := composeTestDocument(bottom, base, clipped)
			expected := composeTestPixel(t, document)
			hidden := knockoutTestLayer(color.NRGBA{}, psdmodel.KnockoutDeep, 0)
			hidden.Hidden = true
			document.Layers = append(document.Layers, hidden)
			if actual := composeTestPixel(t, document); actual != expected {
				t.Fatalf("pass=%t blendClipped=%t old=%v Knockout traversal=%v", pass, blendClipped, expected, actual)
			}
		}
	}
}

func TestKnockoutRegionMatchesFullCanvasInAbsoluteCoordinates(t *testing.T) {
	canvas := image.Rect(0, 0, 5, 4)
	background := composeTestLayer(canvas, color.NRGBA{R: 21, G: 71, B: 131, A: 255})
	background.Background = true
	body := composeTestLayer(canvas, color.NRGBA{R: 201, G: 121, B: 41, A: 255})
	target := composeTestLayer(image.Rect(1, 1, 4, 3), color.NRGBA{R: 240, G: 40, B: 80, A: 128})
	target.Knockout, target.FillOpacity = psdmodel.KnockoutDeep, new(float64(0))
	group := &psdmodel.Layer{Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{body, target},
		Opacity: new(.5)}
	document := &psdmodel.Document{Width: 5, Height: 4, Layers: []*psdmodel.Layer{background, group}}
	full, err := Render(nil, document)
	if err != nil {
		t.Fatal(err)
	}
	window := image.Rect(2, 1, 5, 4)
	result, err := RenderRegion(nil, document, window)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rect != window {
		t.Fatalf("regional output lost document coordinates: %v", result.Rect)
	}
	for y := window.Min.Y; y < window.Max.Y; y++ {
		for x := window.Min.X; x < window.Max.X; x++ {
			if actual, expected := result.NRGBAAt(x, y), full.NRGBAAt(x, y); actual != expected {
				t.Fatalf("regional Knockout at (%d,%d)=%v full=%v", x, y, actual, expected)
			}
		}
	}
}

func TestKnockoutTransparencyShapesFalseCoversDocument(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 2, 1), color.NRGBA{R: 201, G: 121, B: 41, A: 255})
	target := knockoutTestLayer(color.NRGBA{A: 255}, psdmodel.KnockoutDeep, 0)
	target.TransparencyShapes = new(false)
	document := &psdmodel.Document{Width: 2, Height: 1, Layers: []*psdmodel.Layer{bottom, target}}
	output, err := Render(nil, document)
	if err != nil {
		t.Fatal(err)
	}
	if output.NRGBAAt(0, 0).A != 0 || output.NRGBAAt(1, 0).A != 0 {
		t.Fatalf("tsly=false Knockout was clipped to saved pixel bounds: %v", output.Pix)
	}
}

func TestKnockoutOnGroupUsesIntrinsicChildShape(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 201, G: 121, B: 41, A: 255})
	child := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 240, G: 40, B: 80, A: 128})
	group := &psdmodel.Layer{Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{child},
		Knockout: psdmodel.KnockoutDeep, FillOpacity: new(float64(0))}
	actual := composeTestPixel(t, composeTestDocument(bottom, group))
	if actual != (color.NRGBA{R: 201, G: 121, B: 41, A: 127}) {
		t.Fatalf("group Knockout used its enclosing opaque backdrop as shape: %v", actual)
	}
	clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, A: 255})
	clipped.Clipping = true
	actual = composeTestPixel(t, composeTestDocument(bottom, group, clipped))
	if actual != (color.NRGBA{R: 201, G: 121, B: 41, A: 127}) {
		t.Fatalf("group-self clipping base lost its already isolated intrinsic shape: %v", actual)
	}
	child.Hidden = true
	if actual := composeTestPixel(t, composeTestDocument(bottom, group)); actual != bottom.Pixels.NRGBAAt(0, 0) {
		t.Fatalf("empty pass group Knockout erased its enclosing backdrop: %v", actual)
	}
}
