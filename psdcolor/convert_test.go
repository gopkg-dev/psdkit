package psdcolor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func colorFixture(mode, depth uint16, width int, values ...[]float64) *psdfile.CompositePixels {
	pixels := &psdfile.CompositePixels{Width: width, Height: 1, ColorMode: mode, Depth: depth}
	rowBytes := width/8 + (width%8+7)/8
	if depth != 1 {
		rowBytes = width * int(depth/8)
	}
	for index, samples := range values {
		plane := psdfile.Plane{ID: int16(index), Width: width, Height: 1, Depth: depth, RowBytes: rowBytes}
		plane.Data = make([]byte, rowBytes)
		for sample, value := range samples {
			switch depth {
			case 1:
				if value != 0 {
					plane.Data[sample/8] |= 1 << (7 - sample%8)
				}
			case 8:
				plane.Data[sample] = byte(value)
			case 16:
				binary.BigEndian.PutUint16(plane.Data[sample*2:], uint16(value))
			case 32:
				binary.BigEndian.PutUint32(plane.Data[sample*4:], math.Float32bits(float32(value)))
			}
		}
		pixels.Channels = append(pixels.Channels, plane)
	}
	return pixels
}

func TestConvertOriginalPrecisionAndOwnership(t *testing.T) {
	for _, depth := range []uint16{1, 8, 16, 32} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			values := []float64{0, 1, 1}
			if depth == 8 {
				values = []float64{0, 128, 255}
			}
			if depth == 16 {
				values = []float64{0, 32768, 65535}
			}
			if depth == 32 {
				values = []float64{0, 0.5, 1}
			}
			pixels := colorFixture(3, depth, 3, values, values, values)
			original := bytes.Clone(pixels.Channels[0].Data)
			converted, err := Convert(context.Background(), pixels)
			if err != nil {
				t.Fatal(err)
			}
			expectedMidpoint := uint8(128)
			if depth == 1 {
				expectedMidpoint = 255
			}
			if depth == 32 {
				expectedMidpoint = 188
			}
			if converted.NRGBAAt(1, 0) != (color.NRGBA{expectedMidpoint, expectedMidpoint, expectedMidpoint, 255}) {
				t.Fatalf("unexpected midpoint %v", converted.NRGBAAt(1, 0))
			}
			converted.Pix[0] = 123
			if !bytes.Equal(original, pixels.Channels[0].Data) {
				t.Fatal("display conversion changed source samples")
			}
		})
	}
}

func TestConvertBitmapGrayAndLab(t *testing.T) {
	bitmap := colorFixture(0, 1, 2, []float64{0, 1})
	gray := colorFixture(1, 1, 2, []float64{0, 1})
	for _, testCase := range []struct {
		pixels *psdfile.CompositePixels
		first  uint8
		second uint8
	}{{bitmap, 255, 0}, {gray, 0, 255}} {
		converted, err := Convert(nil, testCase.pixels)
		if err != nil {
			t.Fatal(err)
		}
		if converted.NRGBAAt(0, 0).R != testCase.first || converted.NRGBAAt(1, 0).R != testCase.second {
			t.Fatalf("unexpected bitmap/gray values %v", converted.Pix)
		}
	}
	lab := colorFixture(9, 8, 3, []float64{0, 128, 255}, []float64{128, 128, 128}, []float64{128, 128, 128})
	converted, err := Convert(nil, lab)
	if err != nil {
		t.Fatal(err)
	}
	if converted.NRGBAAt(0, 0) != (color.NRGBA{0, 0, 0, 255}) ||
		converted.NRGBAAt(1, 0) != (color.NRGBA{119, 119, 119, 255}) ||
		converted.NRGBAAt(2, 0) != (color.NRGBA{255, 255, 255, 255}) {
		t.Fatalf("unexpected D50 Lab neutrals %v", converted.Pix)
	}
	lab16 := colorFixture(9, 16, 1, []float64{32768}, []float64{32768}, []float64{32768})
	converted, err = Convert(nil, lab16)
	if err != nil || converted.NRGBAAt(0, 0) != (color.NRGBA{119, 119, 119, 255}) {
		t.Fatalf("native Photoshop Lab16 neutral encoding differs: %v, %v", converted, err)
	}
}

func TestConvertIndexedAndExplicitTransparency(t *testing.T) {
	palette := make([]byte, 768)
	palette[0], palette[256], palette[512] = 10, 20, 30
	palette[1], palette[257], palette[513] = 200, 150, 100
	transparent := uint8(1)
	pixels := colorFixture(2, 8, 2, []float64{0, 1})
	converted, err := Convert(nil, pixels, Options{Palette: palette, TransparencyIndex: &transparent})
	if err != nil {
		t.Fatal(err)
	}
	if converted.NRGBAAt(0, 0) != (color.NRGBA{10, 20, 30, 255}) ||
		converted.NRGBAAt(1, 0) != (color.NRGBA{}) {
		t.Fatalf("unexpected indexed pixels %v", converted.Pix)
	}
	if _, err := Convert(nil, pixels); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing palette accepted: %v", err)
	}
}

func TestConvertAlphaAndWhiteMatte(t *testing.T) {
	pixels := colorFixture(3, 8, 2,
		[]float64{159, 255}, []float64{191, 255}, []float64{223, 255}, []float64{128, 0})
	straight, err := Convert(nil, pixels, Options{Alpha: AlphaStraight})
	if err != nil {
		t.Fatal(err)
	}
	if straight.NRGBAAt(0, 0) != (color.NRGBA{159, 191, 223, 128}) {
		t.Fatalf("unexpected straight pixels %v", straight.Pix)
	}
	converted, err := Convert(nil, pixels, Options{Alpha: AlphaWhiteMatte})
	if err != nil {
		t.Fatal(err)
	}
	if converted.NRGBAAt(0, 0) != (color.NRGBA{64, 128, 191, 128}) ||
		converted.NRGBAAt(1, 0) != (color.NRGBA{}) {
		t.Fatalf("unexpected unmatted pixels %v", converted.Pix)
	}
	opaque, err := Convert(nil, pixels)
	if err != nil || opaque.NRGBAAt(0, 0).A != 255 {
		t.Fatalf("extra channel implicitly interpreted as transparency: %v", err)
	}
}

func TestConvertCMYKRequiresExplicitFallback(t *testing.T) {
	pixels := colorFixture(4, 8, 1, []float64{255}, []float64{0}, []float64{255}, []float64{128})
	if _, err := Convert(nil, pixels); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unmanaged CMYK silently accepted: %v", err)
	}
	converted, err := Convert(nil, pixels, Options{AllowUnmanagedCMYK: true})
	if err != nil {
		t.Fatal(err)
	}
	if converted.NRGBAAt(0, 0) != (color.NRGBA{128, 0, 128, 255}) {
		t.Fatalf("incorrect inverted CMYK fallback %v", converted.Pix)
	}
}

func TestConvertFloatPoliciesDoNotChangeBits(t *testing.T) {
	pixels := colorFixture(3, 32, 4,
		[]float64{math.NaN(), math.Inf(1), -1, 2}, []float64{0, 0, 0, 0}, []float64{0, 0, 0, 0})
	binary.BigEndian.PutUint32(pixels.Channels[0].Data, 0x7fc01234)
	original := bytes.Clone(pixels.Channels[0].Data)
	if _, err := Convert(nil, pixels); !errors.Is(err, ErrNonFinite) {
		t.Fatalf("NaN silently accepted: %v", err)
	}
	if _, err := Convert(nil, pixels, Options{NonFinite: NonFiniteClamp}); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("finite HDR silently clipped: %v", err)
	}
	converted, err := Convert(nil, pixels, Options{NonFinite: NonFiniteClamp, ToneMap: ToneMapClamp})
	if err != nil {
		t.Fatal(err)
	}
	if converted.NRGBAAt(0, 0).R != 0 || converted.NRGBAAt(1, 0).R != 255 ||
		converted.NRGBAAt(2, 0).R != 0 || converted.NRGBAAt(3, 0).R != 255 {
		t.Fatalf("incorrect explicit float policy %v", converted.Pix)
	}
	if !bytes.Equal(original, pixels.Channels[0].Data) {
		t.Fatal("NaN payload, infinity or HDR bits were modified")
	}
	encoded := colorFixture(3, 32, 1, []float64{0.5}, []float64{0.5}, []float64{0.5})
	converted, err = Convert(nil, encoded, Options{FloatEncoding: FloatEncodedRGB})
	if err != nil || converted.NRGBAAt(0, 0).R != 128 {
		t.Fatalf("explicit encoded float option ignored: %v", err)
	}
}

func TestConvertBudgetsCancellationAndInvalidBuffers(t *testing.T) {
	pixels := colorFixture(3, 8, 2, []float64{1, 2}, []float64{3, 4}, []float64{5, 6})
	for _, settings := range []Options{{MaxPixels: 1}, {MaxOutputBytes: 7}} {
		if _, err := Convert(nil, pixels, settings); !errors.Is(err, ErrLimit) {
			t.Fatalf("conversion exceeded budget: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Convert(ctx, pixels); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	for _, change := range []func(*psdfile.CompositePixels){
		func(value *psdfile.CompositePixels) { value.Channels[0].RowBytes++ },
		func(value *psdfile.CompositePixels) { value.Channels[0].Data = value.Channels[0].Data[:1] },
		func(value *psdfile.CompositePixels) { value.Channels[0].Width++ },
		func(value *psdfile.CompositePixels) { value.Channels[0].Depth = 16 },
	} {
		invalid := colorFixture(3, 8, 2, []float64{1, 2}, []float64{3, 4}, []float64{5, 6})
		change(invalid)
		if _, err := Convert(nil, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid buffer accepted: %v", err)
		}
	}
	for _, settings := range []Options{{Alpha: AlphaStraight}, {MaxPixels: -1}, {NonFinite: 100}} {
		if _, err := Convert(nil, pixels, settings); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid options accepted: %v", err)
		}
	}
	if _, err := Convert(nil, pixels, Options{}, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multiple options accepted: %v", err)
	}
}
