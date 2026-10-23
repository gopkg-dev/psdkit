package psdgradient

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"reflect"
	"slices"
	"testing"
	"unsafe"
)

func componentConstantFill(rgbConstant bool) Fill {
	fill, constant := gradientTestFill(), constantTestGradient()
	if rgbConstant {
		fill.ColorStops = constant.ColorStops
	} else {
		fill.OpacityStops = constant.OpacityStops
	}
	return fill
}

func TestConstantComponentPreservesNativeVaryingComponent(t *testing.T) {
	for _, rgbConstant := range []bool{false, true} {
		fill := componentConstantFill(rgbConstant)
		pixels, err := Render(nil, fill, image.Rect(0, 0, 64, 48))
		if err != nil {
			t.Fatal(err)
		}
		// These varying-component samples come from the existing independent Photoshop 26.1 reference.
		for _, sample := range []struct {
			x     int
			color [4]byte
		}{
			{1, [4]byte{22, 81, 198, 2}}, {16, [4]byte{63, 94, 161, 52}},
			{32, [4]byte{125, 115, 105, 128}}, {48, [4]byte{187, 136, 49, 203}},
			{63, [4]byte{228, 149, 12, 253}},
		} {
			if rgbConstant {
				sample.color[0], sample.color[1], sample.color[2] = 64, 128, 191
			} else {
				sample.color[3] = 128
			}
			offset := pixels.PixOffset(sample.x, 24)
			if !bytes.Equal(pixels.Pix[offset:offset+4], sample.color[:]) {
				t.Fatalf("constant RGB=%t changed native varying component at x=%d: %v != %v",
					rgbConstant, sample.x, pixels.Pix[offset:offset+4], sample.color)
			}
		}
	}
}

func TestConstantComponentROIAlignmentAndOwnership(t *testing.T) {
	bounds, alignment := image.Rect(-9, 3, 55, 51), image.Rect(1, 8, 43, 40)
	crop := image.Rect(2, 11, 20, 16)
	for _, rgbConstant := range []bool{false, true} {
		for _, kind := range []Kind{Linear, Radial} {
			fill := componentConstantFill(rgbConstant)
			fill.Kind, fill.Angle, fill.Scale, fill.Reverse = kind, 30, 80, true
			original := fill
			original.ColorStops, original.OpacityStops = slices.Clone(fill.ColorStops), slices.Clone(fill.OpacityStops)
			baseline := gradientTestFill()
			baseline.Kind, baseline.Angle, baseline.Scale, baseline.Reverse = kind, 30, 80, true
			varying, err := Render(nil, baseline, bounds, Options{AlignBounds: &alignment})
			if err != nil {
				t.Fatal(err)
			}
			full, err := Render(nil, fill, bounds, Options{AlignBounds: &alignment})
			if err != nil {
				t.Fatal(err)
			}
			regional, err := Render(nil, fill, bounds, Options{AlignBounds: &alignment, Crop: &crop})
			if err != nil || regional.Rect != crop {
				t.Fatalf("constant-component ROI changed bounds: %v", err)
			}
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					want := varying.NRGBAAt(x, y)
					if rgbConstant {
						want.R, want.G, want.B = 64, 128, 191
					} else {
						want.A = 128
					}
					if full.NRGBAAt(x, y) != want || image.Pt(x, y).In(crop) && regional.NRGBAAt(x, y) != want {
						t.Fatalf("constant component changed active geometry or ROI at (%d,%d)", x, y)
					}
				}
			}
			if !reflect.DeepEqual(fill, original) {
				t.Fatal("constant-component rendering sorted or changed caller stops")
			}
		}
	}
}

func TestConstantComponentKeepsSamplingValidationAndBudgets(t *testing.T) {
	for _, rgbConstant := range []bool{false, true} {
		fill := componentConstantFill(rgbConstant)
		bounds, crop := image.Rect(0, 0, 64, 48), image.Rect(1, 2, 3, 4)
		cost := int64(len(fill.ColorStops))*int64(unsafe.Sizeof(ColorStop{})) +
			int64(len(fill.OpacityStops))*int64(unsafe.Sizeof(OpacityStop{})) + 1024
		options := Options{Crop: &crop, MaxPixels: 4, MaxStops: 6, MaxWorkingBytes: cost + 16}
		if _, err := Render(nil, fill, bounds, options); err != nil {
			t.Fatal(err)
		}
		for _, change := range []func(*Options){
			func(value *Options) { value.MaxWorkingBytes-- },
			func(value *Options) { value.MaxPixels-- },
			func(value *Options) { value.MaxStops-- },
		} {
			invalid := options
			change(&invalid)
			if output, err := Render(nil, fill, bounds, invalid); output != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("constant component bypassed resource limit: %v", err)
			}
		}
		for _, change := range []func(*Fill){
			func(value *Fill) { value.Smoothness = 0 },
			func(value *Fill) { value.Scale = .01 },
			func(value *Fill) { value.Dither = true },
			func(value *Fill) { value.Unsupported = []string{"future"} },
			func(value *Fill) {
				if rgbConstant {
					value.OpacityStops[1].Midpoint = 0
				} else {
					value.ColorStops[1].Midpoint = 100
				}
			},
			func(value *Fill) {
				if rgbConstant {
					value.ColorStops[1].RGB[0] = math.Nextafter(.25, 1)
				} else {
					value.OpacityStops[1].Opacity = math.Nextafter(.5, 1)
				}
			},
		} {
			invalid := fill
			invalid.ColorStops, invalid.OpacityStops = slices.Clone(fill.ColorStops), slices.Clone(fill.OpacityStops)
			change(&invalid)
			if output, err := Render(nil, invalid, bounds); output != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("constant component masked unsupported varying controls: %v", err)
			}
		}
		invalid := fill
		invalid.ColorStops = slices.Clone(fill.ColorStops)
		invalid.ColorStops[0].RGB[0] = math.NaN()
		if output, err := Render(nil, invalid, bounds); output != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("constant component hid nonfinite RGB: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if output, err := Render(ctx, fill, bounds); output != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("constant component ignored cancellation: %v", err)
		}
	}
}
