package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"
)

func colorBalanceFixture() []byte {
	var data []byte
	for _, value := range []int16{30, -20, 15, -25, 35, -10, 15, -30, 40} {
		data = binary.BigEndian.AppendUint16(data, uint16(value))
	}
	return append(data, 1, 0, 0, 0)
}

func TestColorBalanceMetadataSignedOwnedAndUnchangedRoundTrip(t *testing.T) {
	data := colorBalanceFixture()
	original := bytes.Clone(data)
	adjustment, err := Decode(nil, "blnc", data, DecodeOptions{SourceOffset: 81})
	if err != nil || adjustment.Kind != ColorBalance || adjustment.ColorBalance == nil ||
		adjustment.ColorBalance.Shadows != ([3]int16{30, -20, 15}) ||
		adjustment.ColorBalance.Midtones != ([3]int16{-25, 35, -10}) ||
		adjustment.ColorBalance.Highlights != ([3]int16{15, -30, 40}) ||
		!adjustment.ColorBalance.PreserveLuminosity || adjustment.Span != (Span{Offset: 81, Length: 22}) {
		t.Fatalf("typed signed fields, luminosity flag or source span differs: %v", err)
	}
	clear(data)
	if !bytes.Equal(adjustment.Raw, original) {
		t.Fatal("color balance raw encoding borrowed caller bytes")
	}
	if _, err := Compile(*adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unverified Color Balance transfer was approximated: %v", err)
	}
	encoded, err := Encode(nil, *adjustment)
	if err != nil || !bytes.Equal(encoded, original) {
		t.Fatalf("known metadata did not preserve its exact original bytes: %v", err)
	}
	encoded[0] ^= 0xff
	if !bytes.Equal(adjustment.Raw, original) {
		t.Fatal("metadata round-trip borrowed source bytes")
	}
	adjustment.Unsupported = []string{"unrecognized semantic field"}
	if _, err := Encode(nil, *adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("explicit unsupported metadata was ignored by raw preservation: %v", err)
	}
	adjustment.Unsupported = nil
	adjustment.ColorBalance.Shadows[0]++
	encoded, err = Encode(nil, *adjustment)
	expected := bytes.Clone(original)
	expected[1]++
	if err != nil || !bytes.Equal(encoded, expected) {
		t.Fatalf("known Color Balance parameter edit changed unrelated bytes: %v", err)
	}
	if _, err := Compile(*adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("metadata editing enabled an unverified Color Balance transfer: %v", err)
	}
}

func TestColorBalanceNeutralAlphaAndStrictMetadata(t *testing.T) {
	parameters := &ColorBalanceParams{PreserveLuminosity: true}
	source := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	copy(source.Pix, []byte{10, 20, 30, 0, 80, 90, 100, 128})
	adjustment := Adjustment{Kind: ColorBalance, ColorBalance: parameters}
	pixels, err := Apply(nil, source, adjustment)
	if err != nil || !bytes.Equal(pixels.Pix, source.Pix) {
		t.Fatalf("neutral Color Balance changed RGB or alpha: %v", err)
	}
	encoded, err := Encode(nil, adjustment)
	if err != nil || len(encoded) != 20 || encoded[18] != 1 || encoded[19] != 0 {
		t.Fatalf("neutral metadata wire encoding differs: %v", err)
	}
	for length := range 19 {
		if _, err := Decode(nil, "blnc", encoded[:length]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("truncated Color Balance length %d accepted: %v", length, err)
		}
	}
	unknown := bytes.Clone(encoded)
	unknown[18] = 2
	retained, err := Decode(nil, "blnc", unknown)
	if !errors.Is(err, ErrUnsupported) || retained == nil || !bytes.Equal(retained.Raw, unknown) {
		t.Fatalf("unknown luminosity flag was lost or guessed: %v", err)
	}
	parameters.Shadows[0] = 101
	if _, err := Compile(adjustment); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid offset accepted: %v", err)
	}
	if _, err := Decode(nil, "blnc", encoded, DecodeOptions{MaxBytes: 20}); !errors.Is(err, ErrLimit) {
		t.Fatalf("metadata allocation budget ignored: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, "blnc", encoded); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled metadata read accepted: %v", err)
	}
}
