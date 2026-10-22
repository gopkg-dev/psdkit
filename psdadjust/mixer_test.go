package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"testing"
)

func mixerFixture() []byte {
	data := []byte{0, 1, 0, 0}
	for _, value := range []int16{70, 20, 10, 0, 5, -10, 90, 20, 0, -5, 20, 10, 70, 0, 0, 0, 0, 0, 0, 0} {
		data = binary.BigEndian.AppendUint16(data, uint16(value))
	}
	return append(data, 0, 0)
}

func TestChannelMixerSignedMetadataOwnedTransformAndRawPatch(t *testing.T) {
	data := mixerFixture()
	original := bytes.Clone(data)
	adjustment, err := Decode(nil, "mixr", data, DecodeOptions{SourceOffset: 211})
	if err != nil || adjustment.Kind != ChannelMixer || adjustment.Version != 1 ||
		adjustment.ChannelMixer.Rows[1].Inputs != ([4]int16{-10, 90, 20, 0}) ||
		adjustment.ChannelMixer.Rows[0].Constant != 5 || adjustment.ChannelMixer.Monochrome ||
		adjustment.Span != (Span{Offset: 211, Length: 46}) {
		t.Fatalf("signed channel coefficients or wire metadata differ: %v", err)
	}
	clear(data)
	transform, err := Compile(*adjustment)
	if err != nil {
		t.Fatal(err)
	}
	color := [3]float64{.1, .2, .3}
	expected := [3]float64{.19, .18, .25}
	actual := transform.RGB(color)
	for component := range actual {
		if math.Abs(actual[component]-expected[component]) > 1e-12 {
			t.Fatal("encoded RGB matrix and percentage constant differ from the numeric control")
		}
	}
	encoded, err := Encode(nil, *adjustment)
	if err != nil || !bytes.Equal(encoded, original) {
		t.Fatalf("unchanged mixer raw bytes or padding changed: %v", err)
	}
	adjustment.ChannelMixer.Rows[0].Inputs[0] = 80
	if transform.RGB(color) != actual {
		t.Fatal("compiled transform retained mutable caller coefficients")
	}
	encoded, err = Encode(nil, *adjustment)
	if err != nil || binary.BigEndian.Uint16(encoded[4:]) != 80 ||
		!bytes.Equal(encoded[6:], original[6:]) || !bytes.Equal(adjustment.Raw, original) {
		t.Fatalf("controlled coefficient patch changed an unrelated field or source bytes: %v", err)
	}
}

func TestChannelMixerRGBSwapClampAlphaAndBudgets(t *testing.T) {
	parameters := &ChannelMixerParams{}
	for output, input := range []int{2, 1, 0} {
		parameters.Rows[output].Inputs[input] = 100
	}
	adjustment := Adjustment{Kind: ChannelMixer, ChannelMixer: parameters}
	source := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	copy(source.Pix, []byte{10, 20, 30, 255, 80, 90, 100, 128, 120, 130, 140, 0})
	original := bytes.Clone(source.Pix)
	pixels, err := Apply(nil, source, adjustment)
	if err != nil || !bytes.Equal(pixels.Pix, []byte{30, 20, 10, 255, 100, 90, 80, 128, 140, 130, 120, 0}) ||
		!bytes.Equal(source.Pix, original) {
		t.Fatalf("RGB swap, caller ownership or alpha preservation differs: %v", err)
	}
	parameters.Rows[0] = MixerChannel{Inputs: [4]int16{200, -100, 0, 0}, Constant: 50}
	parameters.Rows[1].Constant = -200
	parameters.Rows[2].Constant = 200
	transform, err := Compile(adjustment)
	if err != nil || transform.RGB([3]float64{1, 0, 0}) != ([3]float64{1, 0, 1}) {
		t.Fatalf("matrix and constant outputs were not clamped: %v", err)
	}
	if _, err := Apply(nil, source, adjustment, Options{MaxWorkingBytes: (16 << 10) + 11}); !errors.Is(err, ErrLimit) {
		t.Fatalf("compiled matrix and owned output escaped the working budget: %v", err)
	}
	if _, err := Encode(nil, adjustment, EncodeOptions{MaxBytes: 100}); !errors.Is(err, ErrLimit) {
		t.Fatalf("encoding peak allocation budget ignored: %v", err)
	}
}

func TestChannelMixerUnverifiedModesVersionsAndTruncation(t *testing.T) {
	adjustment, err := Decode(nil, "mixr", mixerFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ChannelMixerParams){
		func(parameters *ChannelMixerParams) { parameters.Rows[0].Inputs[3] = 1 },
		func(parameters *ChannelMixerParams) { parameters.Rows[3].Inputs[3] = 100 },
	} {
		parameters := *adjustment.ChannelMixer
		mutate(&parameters)
		if _, err := Compile(Adjustment{Kind: ChannelMixer, ChannelMixer: &parameters}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unverified channel mode was approximated: %v", err)
		}
	}
	adjustment.ChannelMixer.Rows[0].Inputs[0] = 201
	if _, err := Compile(*adjustment); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid coefficient accepted: %v", err)
	}
	data := mixerFixture()
	for length := range 44 {
		if _, err := Decode(nil, "mixr", data[:length]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("truncated mixer length %d accepted: %v", length, err)
		}
	}
	for _, index := range []int{1, 3} {
		unknown := bytes.Clone(data)
		unknown[index] = 2
		retained, err := Decode(nil, "mixr", unknown)
		if !errors.Is(err, ErrUnsupported) || retained == nil || !bytes.Equal(retained.Raw, unknown) {
			t.Fatalf("unknown mixer field was guessed or original bytes lost: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, "mixr", data); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mixer metadata read accepted: %v", err)
	}
}
