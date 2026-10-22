package psdgradient

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func gradientTestFill() Fill {
	return Fill{Kind: Linear, Scale: 100, Align: true, Smoothness: 4096, Interpolation: "classic",
		ColorStops: []ColorStop{
			{Location: 0, Midpoint: 50, RGB: [3]float64{20.0 / 255, 80.0 / 255, 200.0 / 255}},
			{Location: 4096, Midpoint: 50, RGB: [3]float64{230.0 / 255, 150.0 / 255, 10.0 / 255}},
		},
		OpacityStops: []OpacityStop{{Location: 0, Midpoint: 50}, {Location: 4096, Midpoint: 50, Opacity: 1}},
	}
}

func TestClassicHorizontalNativeDefaultMidpointGolden(t *testing.T) {
	pixels, err := Render(nil, gradientTestFill(), image.Rect(0, 0, 64, 48))
	if err != nil {
		t.Fatal(err)
	}
	// Independent Photoshop 26.1 RGB8 samples from a full-scale classic gradient at zero degrees.
	for _, sample := range []struct {
		x    int
		rgba []byte
	}{
		{1, []byte{22, 81, 198, 2}}, {4, []byte{28, 83, 193, 9}}, {8, []byte{38, 86, 184, 21}},
		{12, []byte{49, 90, 173, 36}}, {16, []byte{63, 94, 161, 52}}, {32, []byte{125, 115, 105, 128}},
		{48, []byte{187, 136, 49, 203}}, {63, []byte{228, 149, 12, 253}},
	} {
		pixel := 24*pixels.Stride + sample.x*4
		if !bytes.Equal(pixels.Pix[pixel:pixel+4], sample.rgba) {
			t.Fatalf("native default midpoint x=%d: %v want %v", sample.x, pixels.Pix[pixel:pixel+4], sample.rgba)
		}
	}
}

func TestClassicNativeIncomingMidpointsAndRadialGeometry(t *testing.T) {
	fill := gradientTestFill()
	fill.ColorStops[1].Midpoint, fill.OpacityStops[1].Midpoint = 30, 70
	for _, test := range []struct {
		kind    Kind
		angle   float64
		scale   float64
		samples []struct {
			x, y int
			rgba [4]byte
		}
	}{
		{kind: Linear, scale: 100, samples: []struct {
			x, y int
			rgba [4]byte
		}{
			{8, 24, [4]byte{54, 91, 170, 14}}, {16, 24, [4]byte{103, 108, 125, 34}},
			{32, 24, [4]byte{162, 127, 72, 83}}, {48, 24, [4]byte{202, 141, 35, 154}},
		}},
		{kind: Radial, scale: 100, samples: []struct {
			x, y int
			rgba [4]byte
		}{
			{16, 24, [4]byte{162, 127, 72, 83}}, {24, 24, [4]byte{103, 108, 125, 34}},
			{48, 24, [4]byte{162, 127, 72, 83}}, {63, 24, [4]byte{227, 149, 12, 247}},
		}},
		{kind: Radial, angle: 30, scale: 80, samples: []struct {
			x, y int
			rgba [4]byte
		}{
			{16, 24, [4]byte{172, 131, 62, 96}}, {24, 24, [4]byte{116, 112, 113, 39}},
			{48, 24, [4]byte{172, 131, 62, 96}}, {63, 24, [4]byte{230, 150, 10, 255}},
		}},
	} {
		fill.Kind, fill.Angle, fill.Scale = test.kind, test.angle, test.scale
		pixels, err := Render(nil, fill, image.Rect(0, 0, 64, 48))
		if err != nil {
			t.Fatal(err)
		}
		// Photoshop's RGB8 classic interpolation includes a final one-level quantization allowance.
		for _, sample := range test.samples {
			offset := sample.y*pixels.Stride + sample.x*4
			for component, expected := range sample.rgba {
				if difference := int(pixels.Pix[offset+component]) - int(expected); difference < -1 || difference > 1 {
					t.Fatalf("native incoming midpoint/geometry differs at (%d,%d): %v want %v",
						sample.x, sample.y, pixels.Pix[offset:offset+4], sample.rgba)
				}
			}
		}
	}
}

func TestRenderCropAndStopOwnership(t *testing.T) {
	fill := gradientTestFill()
	fill.ColorStops[0], fill.ColorStops[1] = fill.ColorStops[1], fill.ColorStops[0]
	before := append([]ColorStop(nil), fill.ColorStops...)
	bounds, crop := image.Rect(-9, 3, 55, 51), image.Rect(2, 11, 20, 16)
	full, err := Render(nil, fill, bounds)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Render(nil, fill, bounds, Options{Crop: &crop})
	if err != nil || result.Rect != crop {
		t.Fatalf("crop geometry differs: %v", err)
	}
	for y := crop.Min.Y; y < crop.Max.Y; y++ {
		for x := crop.Min.X; x < crop.Max.X; x++ {
			if result.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatalf("crop rescaled the source ramp at (%d,%d)", x, y)
			}
		}
	}
	if !reflect.DeepEqual(before, fill.ColorStops) {
		t.Fatal("renderer sorted caller-owned stops")
	}
	first := full.NRGBAAt(crop.Min.X, crop.Min.Y)
	clear(result.Pix)
	if full.NRGBAAt(crop.Min.X, crop.Min.Y) != first {
		t.Fatal("cropped output borrowed another render's pixels")
	}
}

func TestClassicThreeStopNativeNeighborTangents(t *testing.T) {
	fill := gradientTestFill()
	fill.Angle = 45
	fill.ColorStops = []ColorStop{fill.ColorStops[0],
		{Location: 2048, Midpoint: 45, RGB: [3]float64{64.0 / 255, 192.0 / 255, 1}}, fill.ColorStops[1]}
	fill.ColorStops[2].Midpoint, fill.OpacityStops[1].Midpoint = 30, 70
	pixels, err := Render(nil, fill, image.Rect(0, 0, 64, 48))
	if err != nil {
		t.Fatal(err)
	}
	// Native Photoshop crosses the blue stop early because the next stop contributes to its tangent.
	for _, sample := range []struct {
		x    int
		rgba [4]byte
	}{
		{2, [4]byte{53, 185, 255, 69}}, {15, [4]byte{100, 192, 211, 99}},
		{20, [4]byte{133, 183, 162, 110}}, {27, [4]byte{164, 173, 114, 127}},
	} {
		for component, expected := range sample.rgba {
			difference := int(pixels.Pix[sample.x*4+component]) - int(expected)
			if difference < -2 || difference > 2 {
				t.Fatalf("native neighbor-tangent x=%d: %v want %v", sample.x,
					pixels.Pix[sample.x*4:sample.x*4+4], sample.rgba)
			}
		}
	}
}

func TestClassicUnequalStopSpacingKeepsUniformNeighborTangents(t *testing.T) {
	fill := gradientTestFill()
	fill.ColorStops = []ColorStop{fill.ColorStops[0],
		{Location: 1024, Midpoint: 45, RGB: [3]float64{64.0 / 255, 192.0 / 255, 1}}, fill.ColorStops[1]}
	fill.ColorStops[2].Midpoint = 30
	fill.OpacityStops = []OpacityStop{fill.OpacityStops[0],
		{Location: 3072, Midpoint: 60, Opacity: .35}, {Location: 4096, Midpoint: 70, Opacity: 1}}
	pixels, err := Render(nil, fill, image.Rect(0, 0, 64, 48))
	if err != nil {
		t.Fatal(err)
	}
	// A derivative weighted by location distances differs from native by RGB8 and alpha10 on this fixture.
	for _, sample := range []struct {
		x    int
		rgba [4]byte
	}{
		{12, [4]byte{45, 175, 255, 11}}, {24, [4]byte{106, 190, 202, 27}},
		{40, [4]byte{178, 168, 92, 64}}, {56, [4]byte{217, 154, 30, 149}},
	} {
		for component, expected := range sample.rgba {
			difference := int(pixels.Pix[24*pixels.Stride+sample.x*4+component]) - int(expected)
			if difference < -2 || difference > 2 {
				t.Fatalf("native unequal spacing x=%d differs from golden", sample.x)
			}
		}
	}
}

func TestGradientAlignmentOffsetAndReverse(t *testing.T) {
	fill := gradientTestFill()
	fill.ColorStops[0].RGB, fill.ColorStops[1].RGB = [3]float64{}, [3]float64{1, 1, 1}
	fill.OpacityStops = nil
	bounds, alignment := image.Rect(0, 0, 8, 1), image.Rect(2, 0, 6, 1)
	pixels, err := Render(nil, fill, bounds, Options{AlignBounds: &alignment})
	if err != nil {
		t.Fatal(err)
	}
	for x, expected := range []byte{0, 0, 0, 52, 128, 203, 255, 255} {
		if pixels.NRGBAAt(x, 0).R != expected || pixels.NRGBAAt(x, 0).A != 255 {
			t.Fatal("layer alignment did not control gradient endpoints")
		}
	}
	fill.Align = false
	documentAligned, err := Render(nil, fill, bounds, Options{AlignBounds: &alignment})
	if err != nil || documentAligned.NRGBAAt(2, 0).R != 52 {
		t.Fatalf("disabled alignment still used the layer reference bounds: %v", err)
	}
	fill.Offset.X = 25
	shifted, err := Render(nil, fill, bounds)
	if err != nil || shifted.NRGBAAt(2, 0).R != 0 || shifted.NRGBAAt(6, 0).R != 128 {
		t.Fatalf("percentage offset did not use document coordinates: %v", err)
	}
	fill.Offset.X, fill.Reverse = 0, true
	reversed, err := Render(nil, fill, bounds)
	if err != nil || reversed.NRGBAAt(0, 0).R != 255 || reversed.NRGBAAt(4, 0).R != 128 {
		t.Fatalf("reverse did not invert the ramp position: %v", err)
	}
}

func TestGradientLimitsInvalidFieldsUnsupportedAndCancellation(t *testing.T) {
	fill, bounds := gradientTestFill(), image.Rect(0, 0, 2, 2)
	for _, options := range []Options{{MaxPixels: 1}, {MaxWorkingBytes: 4}, {MaxStops: 1}} {
		if _, err := Render(nil, fill, bounds, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("pixel, working or stop budget ignored: %v", err)
		}
	}
	for _, mutate := range []func(*Fill){
		func(value *Fill) { value.Angle = math.Inf(1) },
		func(value *Fill) { value.Scale = 0 },
		func(value *Fill) { value.ColorStops[0].Midpoint = 101 },
		func(value *Fill) { value.ColorStops[0].Location = 4097 },
		func(value *Fill) { value.ColorStops[0].RGB[0] = math.NaN() },
		func(value *Fill) { value.OpacityStops[0].Opacity = 2 },
	} {
		invalid := gradientTestFill()
		mutate(&invalid)
		if _, err := Render(nil, invalid, bounds); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid fields accepted: %v", err)
		}
	}
	for _, mutate := range []func(*Fill){
		func(value *Fill) { value.Kind = "diamond" },
		func(value *Fill) { value.Form = "noise" },
		func(value *Fill) { value.Interpolation = "perceptual" },
		func(value *Fill) { value.Dither = true },
		func(value *Fill) { value.Smoothness = 0 },
		func(value *Fill) { value.Unsupported = []string{"unknown"} },
		func(value *Fill) { value.ColorStops[1].Location = 0 },
	} {
		unsupported := gradientTestFill()
		mutate(&unsupported)
		if _, err := Render(nil, unsupported, bounds); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported semantics silently approximated: %v", err)
		}
	}
	degenerate := gradientTestFill()
	degenerate.Kind = Radial
	if _, err := Render(nil, degenerate, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("valid but unverified degenerate geometry was mislabeled as malformed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Render(ctx, fill, bounds); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled rendering accepted: %v", err)
	}
	if strconv.IntSize == 64 {
		huge := int64(1 << 54)
		if _, err := Render(nil, fill, image.Rect(int(huge), 0, int(huge)+1, 1)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("absolute pixel coordinate precision loss accepted: %v", err)
		}
	}
}
