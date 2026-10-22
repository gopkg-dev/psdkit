package psdcompose

import (
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestKnockoutTraversalPreservesEmptyPassClippingBase(t *testing.T) {
	for _, childKind := range []string{"hidden", "orphanClipping"} {
		t.Run(childKind, func(t *testing.T) {
			bottom := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{R: 255, A: 255})
			child := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{B: 255, A: 255})
			child.Hidden = childKind == "hidden"
			child.Clipping = childKind == "orphanClipping"
			base := &psdmodel.Layer{
				Group: true, BlendMode: "pass", Children: []*psdmodel.Layer{child},
				Opacity: new(.5),
			}
			clipped := composeTestLayer(image.Rect(0, 0, 1, 1), color.NRGBA{G: 255, A: 255})
			clipped.Clipping = true
			document := composeTestDocument(bottom, base, clipped)
			expected := color.NRGBA{R: 255, A: 255}
			if actual := composeTestPixel(t, document); actual != expected {
				t.Fatalf("empty pass clipping base changed ordinary tree: %v", actual)
			}
			document.Layers = append(document.Layers, &psdmodel.Layer{
				Hidden: true, Knockout: psdmodel.KnockoutDeep,
			})
			if actual := composeTestPixel(t, document); actual != expected {
				t.Fatalf("hidden Knockout made empty pass clipping base erase backdrop: %v", actual)
			}
		})
	}
}
