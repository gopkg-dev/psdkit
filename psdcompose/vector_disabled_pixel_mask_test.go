package psdcompose

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestVectorFeatherIgnoresDisabledPixelMaskWithoutExtraWorkingStorage(t *testing.T) {
	for _, radius := range []float64{.5, 1.5, 3} {
		document, layer := vectorFeatherTestDocument(radius)
		baseline, err := Render(t.Context(), document)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := preparePlan(document, nil)
		if err != nil {
			t.Fatal(err)
		}
		maskPixels := image.NewNRGBA(image.Rect(2, 1, 9, 10))
		layer.Mask = &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels, Disabled: true,
			Invert: true, DefaultColor: new(uint8(39)), Density: new(uint8(127)), Feather: new(1000.0),
			Unsupported: []string{"retained inactive semantics"}}
		maskedPlan, err := preparePlan(document, nil)
		if err != nil || maskedPlan.workingBytes != plan.workingBytes {
			t.Fatalf("radius %g: disabled mask changed retained working storage: %v", radius, err)
		}
		masked, err := Render(t.Context(), document, Options{MaxWorkingBytes: 1 << 20})
		if err != nil || !bytes.Equal(masked.Pix, baseline.Pix) {
			t.Fatalf("radius %g: disabled mask changed feather or increased the peak budget: %v", radius, err)
		}
		region := image.Rect(2, 2, 18, 13)
		partial, err := RenderRegion(t.Context(), document, region)
		if err != nil {
			t.Fatal(err)
		}
		for y := region.Min.Y; y < region.Max.Y; y++ {
			for x := region.Min.X; x < region.Max.X; x++ {
				if partial.NRGBAAt(x, y) != baseline.NRGBAAt(x, y) {
					t.Fatalf("radius %g: disabled mask changed region pixels at %d,%d", radius, x, y)
				}
			}
		}
		layer.Mask.Disabled, layer.Mask.Invert = false, false
		layer.Mask.Feather, layer.Mask.Unsupported = nil, nil
		if pixels, err := Render(t.Context(), document); pixels != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("radius %g: active dual mask escaped its rendering boundary: %v", radius, err)
		}
	}
}
