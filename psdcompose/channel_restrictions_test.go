package psdcompose

import (
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestChannelRestrictionsRGBAndAllDisabled(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 20, G: 80, B: 180, A: 255})
	top := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 220, G: 40, B: 60, A: 255})
	document := composeTestDocument(bottom, top)
	top.ChannelRestrictions = []uint32{0, 2}
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{R: 20, G: 40, B: 180, A: 255}) {
		t.Fatalf("restricted RGB channels changed: %v", actual)
	}
	top.ChannelRestrictions = []uint32{0, 1, 2}
	if actual := composeTestPixel(t, document); actual != bottom.Pixels.NRGBAAt(0, 0) {
		t.Fatalf("all restricted channels did not leave backdrop intact: %v", actual)
	}
	top.ChannelRestrictions = []uint32{0, 0, 2}
	if actual := composeTestPixel(t, document); actual != (color.NRGBA{R: 20, G: 40, B: 180, A: 255}) {
		t.Fatalf("duplicate restricted channel changed semantics: %v", actual)
	}
	group := &psdmodel.Layer{Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{top},
		ChannelRestrictions: []uint32{1}}
	top.ChannelRestrictions = nil
	actual := composeTestPixel(t, composeTestDocument(bottom, group))
	if actual != (color.NRGBA{R: 220, G: 80, B: 60, A: 255}) {
		t.Fatalf("pass-through group restrictions not applied to group: %v", actual)
	}
}

func TestChannelRestrictionsUnknownAndBudget(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{A: 255})
	layer.ChannelRestrictions = []uint32{3}
	if err := Validate(composeTestDocument(layer)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown restricted channel was ignored: %v", err)
	}
	layer.Hidden = true
	if err := Validate(composeTestDocument(layer)); err != nil {
		t.Fatalf("hidden unknown restriction blocks visible content: %v", err)
	}
	layer.ChannelRestrictions = make([]uint32, 57)
	if err := Validate(composeTestDocument(layer)); !errors.Is(err, ErrLimit) {
		t.Fatalf("hidden restriction metadata escaped channel budget: %v", err)
	}
}

func TestChannelRestrictionsPreservePremultipliedBackdrop(t *testing.T) {
	bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 41, G: 101, B: 181, A: 128})
	top := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 221, G: 51, B: 91, A: 128})
	top.ChannelRestrictions = []uint32{0, 2}
	actual := composeTestPixel(t, composeTestDocument(bottom, top))
	if actual != (color.NRGBA{R: 27, G: 68, B: 121, A: 192}) {
		t.Fatalf("disabled channels changed premultiplied backdrop or shared alpha: %v", actual)
	}
}
