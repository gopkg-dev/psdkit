package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"os"
	"testing"
	"time"
)

func TestMonochromeMixerArithmeticIndependentOracle(t *testing.T) {
	source, err := os.ReadFile("testdata/mono-mixer/source.rgba")
	if err != nil {
		t.Fatal(err)
	}
	rows := [][4]int16{{34, 66, 0, 0}, {-70, 200, -30, 0}, {125, -50, 25, 17}}
	for index, row := range rows {
		expected, err := os.ReadFile([]string{"testdata/mono-mixer/control1.rgba",
			"testdata/mono-mixer/control2.rgba", "testdata/mono-mixer/control3.rgba"}[index])
		if err != nil {
			t.Fatal(err)
		}
		transform := &Transform{monochrome: &row}
		actual := bytes.Clone(source)
		for offset := 0; offset < len(source); offset += 4 {
			value := transform.monochromeByte(source[offset : offset+3])
			for channel := range 3 {
				actual[offset+channel] = value
			}
		}
		if !bytes.Equal(actual, expected) {
			t.Fatalf("control %d differs from independent exact rational RGB8 oracle", index)
		}
		color := [3]float64{.1234567, .2345678, .3456789}
		actualFloat := transform.RGB(color)
		expectedFloat := clamp((float64(row[0])*color[0] + float64(row[1])*color[1] +
			float64(row[2])*color[2] + float64(row[3])) / 100)
		for _, component := range actualFloat {
			if math.Abs(component-expectedFloat) > 1e-12 {
				t.Fatal("arbitrary encoded float inputs were quantized to source bytes")
			}
		}
	}
}

func monoMixerAdjustment(row [4]int16) Adjustment {
	parameters := &ChannelMixerParams{Monochrome: true}
	parameters.Rows[0] = MixerChannel{Inputs: [4]int16{row[0], row[1], row[2]}, Constant: row[3]}
	return Adjustment{Kind: ChannelMixer, ChannelMixer: parameters}
}

func TestMonochromeMixerApplyOracleAlphaStrideOwnershipAndBudgets(t *testing.T) {
	raw, err := os.ReadFile("testdata/mono-mixer/source.rgba")
	if err != nil {
		t.Fatal(err)
	}
	stride := 256*4 + 13
	source := &image.NRGBA{Rect: image.Rect(-17, 29, 239, 33), Stride: stride, Pix: make([]byte, 4*stride)}
	for y := range 4 {
		copy(source.Pix[y*stride:y*stride+1024], raw[y*1024:(y+1)*1024])
		for offset := y*stride + 1024; offset < (y+1)*stride; offset++ {
			source.Pix[offset] = 0xa7
		}
	}
	original := bytes.Clone(source.Pix)
	rows := [][4]int16{{34, 66, 0, 0}, {-70, 200, -30, 0}, {125, -50, 25, 17}}
	for index, row := range rows {
		adjustment := monoMixerAdjustment(row)
		expected, err := os.ReadFile([]string{"testdata/mono-mixer/control1.rgba",
			"testdata/mono-mixer/control2.rgba", "testdata/mono-mixer/control3.rgba"}[index])
		if err != nil {
			t.Fatal(err)
		}
		pixels, err := Apply(nil, source, adjustment, Options{MaxPixels: 1024, MaxWorkingBytes: (16 << 10) + 4096})
		if err != nil || pixels.Rect != source.Rect || !bytes.Equal(pixels.Pix, expected) ||
			!bytes.Equal(source.Pix, original) {
			t.Fatalf("control %d exact source, alpha, geometry or quota differs: %v", index, err)
		}
		for _, options := range []Options{{MaxPixels: 1023}, {MaxWorkingBytes: (16 << 10) + 4095}} {
			if pixels, err := Apply(nil, source, adjustment, options); pixels != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("control %d escaped output/transform quota: %v", index, err)
			}
		}
	}
	ctx := &monoMixerCancelContext{cancelAt: 7}
	if pixels, err := Apply(ctx, source, monoMixerAdjustment(rows[0])); pixels != nil ||
		!errors.Is(err, context.Canceled) || !bytes.Equal(source.Pix, original) {
		t.Fatalf("mid-loop cancellation returned partial pixels or changed input: %v", err)
	}
}

type monoMixerCancelContext struct{ calls, cancelAt int }

func (*monoMixerCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*monoMixerCancelContext) Done() <-chan struct{}       { return nil }
func (*monoMixerCancelContext) Value(any) any               { return nil }
func (ctx *monoMixerCancelContext) Err() error {
	ctx.calls++
	if ctx.calls >= ctx.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestMonochromeMixerInactiveRowsOwnedTransformAndWirePreservation(t *testing.T) {
	adjustment := monoMixerAdjustment([4]int16{34, 66, 0, 0})
	adjustment.ChannelMixer.Rows[1] = MixerChannel{Inputs: [4]int16{200, -199, 17}, Constant: -83}
	adjustment.ChannelMixer.Rows[2] = MixerChannel{Inputs: [4]int16{-113, 79, 151}, Constant: 61}
	adjustment.ChannelMixer.Rows[3] = MixerChannel{Inputs: [4]int16{29, 47, -181}, Constant: 13}
	encoded, err := Encode(nil, adjustment)
	if err != nil || len(encoded) != 44 || binary.BigEndian.Uint16(encoded[2:]) != 1 {
		t.Fatalf("monochrome wire representation differs: %v", err)
	}
	encoded = append(encoded, 0, 0)
	decoded, err := Decode(nil, "mixr", encoded, DecodeOptions{SourceOffset: 211})
	if err != nil || *decoded.ChannelMixer != *adjustment.ChannelMixer ||
		decoded.Span != (Span{Offset: 211, Length: 46}) {
		t.Fatalf("inactive fields or original encoding were not retained: %v", err)
	}
	transform, err := Compile(*decoded)
	if err != nil {
		t.Fatal(err)
	}
	color := [3]float64{.1234, .4567, .7891}
	expected := transform.RGB(color)
	decoded.ChannelMixer.Rows[2].Constant = 72
	patched, err := Encode(nil, *decoded)
	if err != nil || binary.BigEndian.Uint16(patched[32:]) != 72 ||
		!bytes.Equal(patched[:32], encoded[:32]) || !bytes.Equal(patched[34:], encoded[34:]) {
		t.Fatalf("inactive-only edit changed another raw field: %v", err)
	}
	clear(decoded.ChannelMixer.Rows[:])
	decoded.ChannelMixer.Monochrome = false
	clear(decoded.Raw)
	if actual := transform.RGB(color); actual != expected {
		t.Fatal("compiled monochrome transform borrowed mutable parameter or Raw storage")
	}
	if expected[0] != expected[1] || expected[0] != expected[2] || math.Abs(expected[0]-.343378) > 1e-12 {
		t.Fatal("inactive RGB rows changed the continuous gray output")
	}
}

func TestMonochromeMixerAllFieldsInvalidBeforeScopeAndGlobalPriorities(t *testing.T) {
	for field := range 20 {
		for _, invalid := range []int16{-201, 201} {
			adjustment := monoMixerAdjustment([4]int16{100, 0, 0, 0})
			adjustment.ChannelMixer.Rows[0].Inputs[3] = 1
			row, column := field/5, field%5
			if column == 4 {
				adjustment.ChannelMixer.Rows[row].Constant = invalid
			} else {
				adjustment.ChannelMixer.Rows[row].Inputs[column] = invalid
			}
			if _, err := Compile(adjustment); !errors.Is(err, ErrInvalid) {
				t.Fatalf("field %d=%d did not validate before early CMYK scope: %v", field, invalid, err)
			}
		}
	}
	for row := range 4 {
		adjustment := monoMixerAdjustment([4]int16{100, 0, 0, 0})
		adjustment.ChannelMixer.Rows[row].Inputs[3] = 1
		if _, err := Compile(adjustment); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("row %d CMYK black input was silently ignored: %v", row, err)
		}
	}
	adjustment := monoMixerAdjustment([4]int16{201, 0, 0, 0})
	adjustment.Version = 2
	if _, err := Encode(nil, adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("new typed version priority changed: %v", err)
	}
	valid := monoMixerAdjustment([4]int16{100, 0, 0, 0})
	adjustment.Raw, _ = Encode(nil, valid)
	if _, err := Encode(nil, adjustment); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Raw-backed numeric priority changed: %v", err)
	}
	valid.Version = 2
	if _, err := Compile(valid); err != nil {
		t.Fatalf("Compile's existing typed version behavior was tightened: %v", err)
	}
	source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	if _, err := Apply(nil, source, adjustment, Options{MaxWorkingBytes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("Apply working quota priority changed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pixels, err := Apply(ctx, nil, adjustment); pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply initial cancellation priority changed: %v", err)
	}
}

func TestMonochromeMixerExactHalfAndClampArithmetic(t *testing.T) {
	row := [4]int16{-70, 200, -30, 0}
	transform := &Transform{monochrome: &row}
	if value := transform.monochromeByte([]byte{2, 20, 127}); value != 1 {
		t.Fatalf("exact half-byte sample rounded below one: %d", value)
	}
	if value := transform.monochromeByte([]byte{255, 0, 255}); value != 0 {
		t.Fatalf("negative source sum was not clamped: %d", value)
	}
	if value := transform.monochromeByte([]byte{0, 255, 0}); value != 255 {
		t.Fatalf("high source sum was not clamped: %d", value)
	}
}
