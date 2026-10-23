package psdgradient

import (
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"reflect"
	"slices"
	"testing"
	"unsafe"
)

func constantTestGradient() Fill {
	fill := Fill{Kind: Radial, Scale: .01, Smoothness: 0, Align: true,
		Interpolation: "classic", Form: "custom-stops", Angle: 37, Reverse: true,
		Offset: Offset{X: 27, Y: -19}}
	for index, location := range []uint32{4096, 2048, 0, 2048} {
		fill.ColorStops = append(fill.ColorStops, ColorStop{Location: location, Midpoint: uint16(index%2) * 100,
			RGB: [3]float64{.25, .5, .75}})
		fill.OpacityStops = append(fill.OpacityStops, OpacityStop{Location: location,
			Midpoint: uint16(index%2) * 100, Opacity: .5})
	}
	return fill
}

func TestConstantClassicGradientIgnoresOnlyInactiveSamplingControls(t *testing.T) {
	for _, kind := range []Kind{Linear, Radial} {
		for _, smoothness := range []float64{0, 128.5, 2048, 4096} {
			for _, opaque := range []bool{false, true} {
				fill := constantTestGradient()
				fill.Kind, fill.Smoothness = kind, smoothness
				alpha := byte(128)
				if opaque {
					fill.OpacityStops, alpha = nil, 255
				}
				original := fill
				original.ColorStops = slices.Clone(fill.ColorStops)
				original.OpacityStops = slices.Clone(fill.OpacityStops)
				for _, bounds := range []image.Rectangle{image.Rect(0, 0, 1, 1), image.Rect(-4, -3, 7, 8)} {
					pixels, err := Render(nil, fill, bounds)
					if err != nil {
						t.Fatalf("constant %s with smoothness %g: %v", kind, smoothness, err)
					}
					for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
						for x := bounds.Min.X; x < bounds.Max.X; x++ {
							if pixels.NRGBAAt(x, y) != (color.NRGBA{64, 128, 191, alpha}) {
								t.Fatal("constant controls changed the independently quantized RGBA value")
							}
						}
					}
					clear(pixels.Pix)
				}
				if !reflect.DeepEqual(fill, original) {
					t.Fatal("constant rendering mutated retained stop order or parameters")
				}
			}
		}
	}
}

func TestConstantGradientCropBudgetAndCancellationRemainStrict(t *testing.T) {
	fill := constantTestGradient()
	bounds, crop, aligned := image.Rect(-4, -3, 7, 8), image.Rect(-1, 0, 2, 3), image.Rect(10, 20, 11, 21)
	stopBytes := int64(len(fill.ColorStops))*int64(unsafe.Sizeof(ColorStop{})) +
		int64(len(fill.OpacityStops))*int64(unsafe.Sizeof(OpacityStop{})) + 1024
	settings := Options{Crop: &crop, AlignBounds: &aligned, MaxPixels: 9,
		MaxWorkingBytes: stopBytes + 36, MaxStops: 8}
	result, err := Render(nil, fill, bounds, settings)
	if err != nil || result.Rect != crop || result.NRGBAAt(-1, 0) != (color.NRGBA{64, 128, 191, 128}) {
		t.Fatalf("constant crop changed its origin or required geometry: %v", err)
	}
	for _, change := range []func(*Options){
		func(value *Options) { value.MaxWorkingBytes-- },
		func(value *Options) { value.MaxPixels-- },
		func(value *Options) { value.MaxStops-- },
	} {
		invalid := settings
		change(&invalid)
		if output, err := Render(nil, fill, bounds, invalid); output != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("constant rendering bypassed a resource budget: %v", err)
		}
	}
	for _, rectangle := range []image.Rectangle{{}, {Min: image.Pt(2, 2), Max: image.Pt(1, 1)}} {
		invalid := settings
		invalid.AlignBounds = &rectangle
		if output, err := Render(nil, fill, bounds, invalid); output != nil || !errors.Is(err, ErrInvalid) {
			t.Fatalf("constant rendering hid invalid active alignment bounds: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := Render(ctx, fill, bounds); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled constant rendering produced output: %v", err)
	}
	midway := &constantCancelContext{Context: context.Background(), cancelAt: 5}
	output, err := Render(midway, fill, image.Rect(0, 0, 3000, 2))
	if output != nil || !errors.Is(err, context.Canceled) || midway.calls != 5 {
		t.Fatalf("mid-row constant cancellation exposed partial output: %v", err)
	}
}

type constantCancelContext struct {
	context.Context
	calls, cancelAt int
}

func (c *constantCancelContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestConstantGradientCannotHideMalformedOrUnknownControls(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*Fill)
		want   error
	}{
		{"empty colors", func(fill *Fill) { fill.ColorStops = nil }, ErrInvalid},
		{"NaN angle", func(fill *Fill) { fill.Angle = math.NaN() }, ErrInvalid},
		{"infinite offset", func(fill *Fill) { fill.Offset.X = math.Inf(1) }, ErrInvalid},
		{"zero scale", func(fill *Fill) { fill.Scale = 0 }, ErrInvalid},
		{"invalid smoothness", func(fill *Fill) { fill.Smoothness = 4097 }, ErrInvalid},
		{"invalid color location", func(fill *Fill) { fill.ColorStops[2].Location = 4097 }, ErrInvalid},
		{"invalid color midpoint", func(fill *Fill) { fill.ColorStops[2].Midpoint = 101 }, ErrInvalid},
		{"NaN color", func(fill *Fill) { fill.ColorStops[2].RGB[1] = math.NaN() }, ErrInvalid},
		{"invalid opacity location", func(fill *Fill) { fill.OpacityStops[2].Location = 4097 }, ErrInvalid},
		{"invalid opacity midpoint", func(fill *Fill) { fill.OpacityStops[2].Midpoint = 101 }, ErrInvalid},
		{"NaN opacity", func(fill *Fill) { fill.OpacityStops[2].Opacity = math.NaN() }, ErrInvalid},
		{"opacity overflow", func(fill *Fill) { fill.OpacityStops[2].Opacity = 1.01 }, ErrInvalid},
		{"dither", func(fill *Fill) { fill.Dither = true }, ErrUnsupported},
		{"shape", func(fill *Fill) { fill.Kind = "diamond" }, ErrUnsupported},
		{"interpolation", func(fill *Fill) { fill.Interpolation = "perceptual" }, ErrUnsupported},
		{"form", func(fill *Fill) { fill.Form = "noise" }, ErrUnsupported},
		{"unknown metadata", func(fill *Fill) { fill.Unsupported = []string{"future"} }, ErrUnsupported},
		{"near color", func(fill *Fill) { fill.ColorStops[1].RGB[0] = math.Nextafter(.25, 1) }, ErrUnsupported},
		{"near alpha", func(fill *Fill) { fill.OpacityStops[1].Opacity = math.Nextafter(.5, 1) }, ErrUnsupported},
		{"stop count", func(fill *Fill) { fill.ColorStops = make([]ColorStop, 4097) }, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			fill := constantTestGradient()
			test.change(&fill)
			if output, err := Render(nil, fill, image.Rect(0, 0, 1, 1)); output != nil || !errors.Is(err, test.want) {
				t.Fatalf("constant fast path accepted invalid or unsupported controls: %v, want %v", err, test.want)
			}
		})
	}
}
