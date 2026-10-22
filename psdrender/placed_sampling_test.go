package psdrender_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func croppedPlacementSource() (*psdmodel.Layer, *image.NRGBA) {
	source := image.NewNRGBA(image.Rect(5, 6, 17, 12))
	for row := 1; row < 5; row++ {
		for column := 2; column < 10; column++ {
			pixel := color.NRGBA{R: 35, G: 195, B: 220, A: 255}
			if column < 5 && row < 3 {
				pixel = color.NRGBA{R: 225, G: 45, B: 65, A: 255}
			}
			if column >= 7 && row >= 3 {
				pixel = color.NRGBA{R: 230, G: 210, B: 40, A: 255}
			}
			source.SetNRGBA(column+5, row+6, pixel)
		}
	}
	layer := &psdmodel.Layer{Placed: &psdmodel.Placed{Width: 12, Height: 6,
		Transform: []float64{4, 4, 12, 4, 12, 12, 4, 12}}}
	return layer, source
}

func TestPlacedSamplingRetainsCanvasAndClampsFractionalInkEdges(t *testing.T) {
	layer, source := croppedPlacementSource()
	before := bytes.Clone(source.Pix)
	plan, err := psdrender.CompileProjection(layer, 16, 16, false)
	if err != nil {
		t.Fatal(err)
	}
	pixels, origin, err := psdrender.ProjectPlacedContentsContext(nil, layer, source, plan)
	if err != nil || origin != image.Pt(4, 4) || pixels.Rect.Size() != image.Pt(8, 8) {
		t.Fatalf("cropped ink changed the original placement canvas: %v, %v", origin, err)
	}
	// Independent native observations for source canvas 12x6, ink (2,1)-(10,5), target (4,4)-(12,12).
	for _, test := range []struct {
		point image.Point
		pixel color.NRGBA
	}{
		{image.Pt(5, 5), color.NRGBA{R: 225, G: 45, B: 65, A: 255}},
		{image.Pt(10, 5), color.NRGBA{R: 35, G: 195, B: 220, A: 255}},
		{image.Pt(8, 10), color.NRGBA{R: 84, G: 199, B: 175, A: 255}},
		{image.Pt(10, 10), color.NRGBA{R: 230, G: 210, B: 40, A: 255}},
		{image.Pt(4, 5), color.NRGBA{}},
		{image.Pt(10, 11), color.NRGBA{}},
	} {
		pixel := pixels.NRGBAAt(test.point.X-origin.X, test.point.Y-origin.Y)
		if pixel != test.pixel {
			t.Fatalf("canvas-preserving ink sample %v = %v, expected %v", test.point, pixel, test.pixel)
		}
	}
	pixels.Pix[0] = 199
	if !bytes.Equal(source.Pix, before) {
		t.Fatal("cropped source sampling changed or aliased caller-owned pixels")
	}
}

func TestPlacedSamplingCropIsInvariantUnderDocumentClipping(t *testing.T) {
	layer, source := croppedPlacementSource()
	fullPlan, err := psdrender.CompileProjection(layer, 16, 16, false)
	if err != nil {
		t.Fatal(err)
	}
	full, origin, err := psdrender.ProjectPlacedContentsContext(nil, layer, source, fullPlan)
	if err != nil {
		t.Fatal(err)
	}
	clippedPlan, err := psdrender.CompileProjection(layer, 9, 10, false)
	if err != nil {
		t.Fatal(err)
	}
	clipped, clippedOrigin, err := psdrender.ProjectPlacedContentsContext(nil, layer, source, clippedPlan)
	if err != nil {
		t.Fatal(err)
	}
	for row := range clipped.Rect.Dy() {
		for column := range clipped.Rect.Dx() {
			if clipped.NRGBAAt(column, row) != full.NRGBAAt(column+clippedOrigin.X-origin.X,
				row+clippedOrigin.Y-origin.Y) {
				t.Fatal("document clipping changed the source canvas or ink support")
			}
		}
	}
}

func TestPlacedSamplingRejectsUncalibratedCropAndSupportsTransparentCanvas(t *testing.T) {
	layer, source := croppedPlacementSource()
	layer.Placed.Transform = []float64{2, 3, 13, 4, 12, 15, 1, 14}
	plan, err := psdrender.CompileProjection(layer, 16, 16, false)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := psdrender.ProjectPlacedContentsContext(nil, layer, source, plan)
	if !errors.Is(err, psdrender.ErrUnsupportedCrop) || result != nil {
		t.Fatalf("uncalibrated non-axis crop produced pixels and %v", err)
	}
	clear(source.Pix)
	result, _, err = psdrender.ProjectPlacedContentsContext(nil, layer, source, plan)
	if err != nil || result == nil || !bytes.Equal(result.Pix, make([]byte, len(result.Pix))) {
		t.Fatalf("empty canvas could not produce owned transparent pixels: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, _, err = psdrender.ProjectPlacedContentsContext(ctx, layer, source, plan)
	if !errors.Is(err, context.Canceled) || result != nil {
		t.Fatalf("canceled cropped source produced pixels and %v", err)
	}
}
