package psdprecision

import (
	"errors"
	"image"
	"math"
	"testing"
)

func TestRGB16OverOpaqueSourceAlphaUsesIndependentNearestStages(t *testing.T) {
	bounds := image.Rect(0, 0, 2, 1)
	base := opaqueRGB16Layer(bounds, [3]uint16{}, 255, "norm")
	top := opaqueRGB16Layer(bounds, [3]uint16{65535, 65535, 65535}, 128, "norm")
	top.Pixels.Pix[3] = float64(1) / 65535
	top.Pixels.Pix[7] = float64(32767) / 65535
	scene := &Scene{Width: 2, Height: 1, Layers: []*Layer{base, top}}
	if _, err := RenderRGB16Opaque(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("strict opaque contract accepted partial source alpha: %v", err)
	}
	result, err := RenderRGB16OverOpaque(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	for pixel, wanted := range []uint32{1, 8224} {
		if result.Pix[pixel*4] != float64(wanted)/32768 || result.Pix[pixel*4+3] != 1 {
			t.Fatalf("partial source alpha pixel%d = %v, expected internal code %d and opaque alpha",
				pixel, result.Pix[pixel*4:pixel*4+4], wanted)
		}
	}
	window := image.Rect(1, 0, 2, 1)
	region, err := RenderRGB16OverOpaqueRegion(nil, scene, window)
	if err != nil || region.Bounds != window || region.Image.Pix[0] != result.Pix[4] {
		t.Fatalf("partial source alpha absolute ROI changed integer stages: %v, %v", region, err)
	}
}

func TestRGB16OverOpaqueAllCanonicalAlphaCodesAndGeneralFloatContract(t *testing.T) {
	bounds := image.Rect(0, 0, 65536, 1)
	base := opaqueRGB16Layer(bounds, [3]uint16{}, 255, "norm")
	top := opaqueRGB16Layer(bounds, [3]uint16{65535, 65535, 65535}, 255, "norm")
	for pixel := range 65536 {
		top.Pixels.Pix[pixel*4+3] = float64(pixel) / 65535
	}
	scene := &Scene{Width: 65536, Height: 1, Layers: []*Layer{base, top}}
	result, err := RenderRGB16OverOpaque(nil, scene)
	if err != nil {
		t.Fatal(err)
	}
	for pixel := range 65536 {
		wanted := float64((pixel+1)/2) / 32768
		if result.Pix[pixel*4] != wanted || result.Pix[pixel*4+3] != 1 {
			t.Fatalf("raw alpha%d was rejected or quantized inconsistently: %v", pixel, result.Pix[pixel*4:pixel*4+4])
		}
	}
	top.Pixels.Pix[3] = math.Nextafter(float64(32767)/65535, 1)
	if _, err := RenderRGB16OverOpaque(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("noncanonical alpha entered native RGB16 stages: %v", err)
	}
	if _, err := Render(nil, scene); err != nil {
		t.Fatalf("general float contract rejected finite arbitrary source alpha: %v", err)
	}
	base.Pixels.Pix[3] = .5
	if _, err := RenderRGB16OverOpaque(nil, scene); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("partial backdrop escaped explicit uncalibrated guard: %v", err)
	}
}
