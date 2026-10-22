package psdcompose

import (
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func wholeColorAlphaTestDocument() *psdmodel.Document {
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	pixels.SetNRGBA(0, 0, color.NRGBA{R: 211, G: 163, B: 79, A: 112})
	opacity, fill := float64(43)/255, float64(196)/255
	return &psdmodel.Document{Width: 1, Height: 1, Layers: []*psdmodel.Layer{{
		Bounds: pixels.Rect, Pixels: pixels, BlendMode: "lgCl", Opacity: &opacity, FillOpacity: &fill,
	}}}
}

func TestWholeColorAlphaNativeAtlasStage(t *testing.T) {
	for _, mode := range []string{"dkCl", "lgCl"} {
		t.Run(mode, func(t *testing.T) {
			document := wholeColorAlphaTestDocument()
			document.Layers[0].BlendMode = mode
			actual, err := Render(context.Background(), document)
			if err != nil {
				t.Fatal(err)
			}
			// Native atlas x=112 has alpha 14 for the byte controls 43 and 196, in either order.
			if actual.NRGBAAt(0, 0).A != 14 {
				t.Fatalf("native atlas alpha=%d, want 14", actual.NRGBAAt(0, 0).A)
			}
		})
	}
}

func TestWholeColorAlphaComplexContextKeepsPriorStage(t *testing.T) {
	for _, contextName := range []string{"warnings", "group", "disabled mask", "tsly false"} {
		t.Run(contextName, func(t *testing.T) {
			document := wholeColorAlphaTestDocument()
			switch contextName {
			case "warnings":
				document.Warnings = []string{"saved text raster"}
			case "group":
				document.Layers = []*psdmodel.Layer{{Group: true, BlendMode: "norm", Children: document.Layers}}
			case "disabled mask":
				document.Layers[0].Mask = &psdmodel.Mask{Disabled: true}
			case "tsly false":
				shapes := false
				document.Layers[0].TransparencyShapes = &shapes
			}
			actual, err := Render(context.Background(), document)
			if err != nil {
				t.Fatal(err)
			}
			if actual.NRGBAAt(0, 0).A != 15 {
				t.Fatalf("prior alpha=%d, want 15", actual.NRGBAAt(0, 0).A)
			}
		})
	}
}
