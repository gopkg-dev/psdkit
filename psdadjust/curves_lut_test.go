package psdadjust

import (
	"bytes"
	"errors"
	"image"
	"reflect"
	"testing"
)

func TestNonmonotoneCurvesLUTMapsEveryByteAndRetainsAlpha(t *testing.T) {
	lut := make([]byte, 256)
	for index := range lut {
		lut[index] = byte(index*73 + 41)
	}
	for _, channel := range []uint16{0, 1, 2, 3} {
		adjustment := Adjustment{Kind: Curves, Curves: []CurveChannel{{Channel: channel, LUT: lut}}}
		source := image.NewNRGBA(image.Rect(-8, 3, 248, 4))
		for value := range 256 {
			copy(source.Pix[value*4:], []byte{byte(value), byte(value), byte(value), byte(value)})
		}
		result, err := Apply(nil, source, adjustment)
		if err != nil || result.Rect != source.Rect {
			t.Fatalf("nonmonotone mapped channel %d was rejected: %v", channel, err)
		}
		for value := range 256 {
			for component := range 3 {
				want := byte(value)
				if channel == 0 || int(channel) == component+1 {
					want = lut[value]
				}
				if result.Pix[value*4+component] != want {
					t.Fatalf("channel %d input %d component %d is %d, want %d",
						channel, value, component, result.Pix[value*4+component], want)
				}
			}
			if result.Pix[value*4+3] != byte(value) {
				t.Fatalf("mapped curve changed alpha at input %d", value)
			}
		}
		encoded, err := Encode(nil, adjustment)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(nil, "curv", encoded)
		if err != nil || !reflect.DeepEqual(decoded.Curves, adjustment.Curves) {
			t.Fatalf("nonmonotone table changed after encoding: %v", err)
		}
		transformed, err := Apply(nil, source, *decoded)
		if err != nil || !bytes.Equal(transformed.Pix, result.Pix) {
			t.Fatalf("saved mapping differs from requested mapping: %v", err)
		}
	}
	transform, err := Compile(Adjustment{Kind: Curves, Curves: []CurveChannel{{LUT: lut}}})
	if err != nil {
		t.Fatal(err)
	}
	want := transform.RGB([3]float64{0, 128.0 / 255, 1})
	clear(lut)
	if got := transform.RGB([3]float64{0, 128.0 / 255, 1}); got != want {
		t.Fatal("compiled mapped curve retained caller-owned bytes")
	}
}

func TestNonmonotoneLUTDoesNotRelaxPointOrRepresentationValidation(t *testing.T) {
	points := []CurvePoint{{Input: 0}, {Input: 128, Output: 255}, {Input: 255}}
	if _, err := Compile(Adjustment{Kind: Curves, Curves: []CurveChannel{{Points: points}}}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("uncalibrated point interpolation was enabled: %v", err)
	}
	for _, curve := range []CurveChannel{
		{LUT: make([]byte, 255)}, {LUT: make([]byte, 257)}, {Points: points, LUT: make([]byte, 256)},
	} {
		if _, err := Compile(Adjustment{Kind: Curves, Curves: []CurveChannel{curve}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid LUT representation accepted: %v", err)
		}
	}
}
