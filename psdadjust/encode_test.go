package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
)

func encodeCurveFixture(mapped bool, channel uint16, extra bool) []byte {
	flag := byte(0)
	if mapped {
		flag = 1
	}
	data := binary.BigEndian.AppendUint32([]byte{flag, 0, 1}, 1<<channel)
	var curve []byte
	if mapped {
		for value := range 256 {
			curve = append(curve, byte(value))
		}
	} else {
		curve = adjustmentCurvePoints(CurvePoint{Input: 0, Output: 0}, CurvePoint{Input: 255, Output: 255})
	}
	data = append(data, curve...)
	if extra {
		data = append(data, "Crv "...)
		data = binary.BigEndian.AppendUint16(data, 4)
		data = binary.BigEndian.AppendUint32(data, 1)
		data = binary.BigEndian.AppendUint16(data, channel)
		data = append(data, curve...)
	}
	return append(data, make([]byte, (4-len(data)%4)%4)...)
}

func TestEncodeUnchangedAdjustmentPreservesExactOwnedBytes(t *testing.T) {
	exposure := binary.BigEndian.AppendUint16(nil, 1)
	for _, value := range []float32{.5, .02, 1.2} {
		exposure = binary.BigEndian.AppendUint32(exposure, math.Float32bits(value))
	}
	for _, test := range []struct {
		key  string
		data []byte
	}{
		{"nvrt", []byte{0, 0}}, {"thrs", []byte{0, 128, 0, 0}}, {"post", []byte{0, 4, 0}},
		{"levl", adjustmentLevelsFixture()}, {"curv", encodeCurveFixture(false, 2, true)},
		{"expA", exposure}, {"brit", []byte{0, 0, 0, 0, 0, 128, 0, 0}},
	} {
		t.Run(test.key, func(t *testing.T) {
			adjustment, err := Decode(nil, test.key, test.data)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := Encode(nil, *adjustment)
			if err != nil || !bytes.Equal(encoded, test.data) {
				t.Fatalf("unchanged bytes differ: %v", err)
			}
			if len(encoded) != 0 {
				encoded[0] ^= 0xff
				if !bytes.Equal(adjustment.Raw, test.data) {
					t.Fatal("encoded payload borrowed its original")
				}
			}
		})
	}
}

func TestEncodeScalarPatchesPreservePaddingAndWireFloatPrecision(t *testing.T) {
	for _, test := range []struct {
		key  string
		kind Kind
	}{
		{"thrs", Threshold}, {"post", Posterize},
	} {
		adjustment, err := Decode(nil, test.key, []byte{0, 4, 0, 0})
		if err != nil {
			t.Fatal(err)
		}
		adjustment.Threshold, adjustment.Posterize = 200, 7
		encoded, err := Encode(nil, *adjustment)
		value := uint16(200)
		if test.kind == Posterize {
			value = 7
		}
		if err != nil || binary.BigEndian.Uint16(encoded) != value || !bytes.Equal(encoded[2:], []byte{0, 0}) {
			t.Fatalf("scalar or padding changed: %v", err)
		}
	}
	encoded, err := Encode(nil, Adjustment{Kind: Exposure,
		Exposure: &ExposureParams{Exposure: .25, Offset: -.01, Gamma: 1.1}})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(nil, "expA", encoded)
	if err != nil || decoded.Exposure.Offset != float64(float32(-.01)) ||
		decoded.Exposure.Gamma != float64(float32(1.1)) || decoded.Version != 1 {
		t.Fatalf("wire float precision differs: %v", err)
	}
}

func TestEncodeLevelsPatchesOnlyRGBAndRetainsExtraRecords(t *testing.T) {
	raw := adjustmentLevelsFixture()
	adjustment, err := Decode(nil, "levl", raw)
	if err != nil {
		t.Fatal(err)
	}
	adjustment.Levels[0] = LevelChannel{Channel: 0, InputBlack: 12, InputWhite: 230,
		OutputBlack: 5, OutputWhite: 245, Gamma: 1.15}
	adjustment.Levels[2].Gamma = .85
	encoded, err := Encode(nil, *adjustment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded[42:], raw[42:]) || !bytes.Equal(adjustment.Raw, raw) {
		t.Fatal("non-RGB levels, extra-marker metadata or source bytes changed")
	}
	decoded, err := Decode(nil, "levl", encoded)
	if err != nil || !reflect.DeepEqual(decoded.Levels, adjustment.Levels) {
		t.Fatalf("edited typed values differ after encoding: %v", err)
	}
	adjustment.Levels[0].Gamma = 1.234
	if _, err := Encode(nil, *adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unrepresentable hundredth precision silently rounded: %v", err)
	}
	adjustment.Levels[0].Gamma = 1.15
	adjustment.Levels[27] = LevelChannel{Channel: 27, InputWhite: 255, OutputWhite: 255, Gamma: 1}
	if _, err := Encode(nil, *adjustment); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("non-RGB reserved source record was edited: %v", err)
	}
}

func TestEncodeCurvesRetainsRepresentationAndRebuildsChangedChannelLengths(t *testing.T) {
	for _, mapped := range []bool{false, true} {
		adjustment, err := Decode(nil, "curv", encodeCurveFixture(mapped, 2, true))
		if err != nil {
			t.Fatal(err)
		}
		if mapped {
			for index := range adjustment.Curves[0].LUT {
				adjustment.Curves[0].LUT[index] = byte(255 - index)
			}
		} else {
			adjustment.Curves[0].Points = []CurvePoint{
				{Input: 0, Output: 10}, {Input: 128, Output: 160}, {Input: 255, Output: 240},
			}
		}
		adjustment.Curves = append(adjustment.Curves, identityCurveChannel(1, mapped))
		encoded, err := Encode(nil, *adjustment)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(nil, "curv", encoded)
		if err != nil || decoded.CurveMap != mapped || decoded.Version != 1 || decoded.ExtraVersion != 4 ||
			decoded.CountMap != 6 || len(decoded.Curves) != 2 || len(decoded.LegacyCurves) != 2 ||
			decoded.Curves[1].Channel != 2 || !reflect.DeepEqual(decoded.Curves[1], adjustment.Curves[0]) {
			t.Fatalf("curve set/representation changed: %v", err)
		}
		adjustment.Curves = []CurveChannel{identityCurveChannel(2, !mapped)}
		if _, err := Encode(nil, *adjustment); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("source representation silently changed: %v", err)
		}
	}
}

func TestEncodeRejectsUnknownVersionsBudgetsAndCancellation(t *testing.T) {
	for _, adjustment := range []Adjustment{
		{Kind: Threshold, Threshold: 128, Version: 7},
		{Kind: Levels, Version: 4}, {Kind: Invert, Key: "CgEd"},
		{Kind: Curves, Curves: []CurveChannel{identityCurveChannel(0, false)}, ExtraVersion: 8},
		{Kind: Threshold, Threshold: 128, Raw: []byte{0, 128, 0x99}},
	} {
		if _, err := Encode(nil, adjustment); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown semantics accepted: %v", err)
		}
	}
	if _, err := Encode(nil, Adjustment{Kind: Posterize, Posterize: 4},
		EncodeOptions{MaxBytes: 8192}); !errors.Is(err, ErrLimit) {
		t.Fatalf("transform scratch escaped encoding budget: %v", err)
	}
	if _, err := Encode(nil, Adjustment{Kind: Threshold, Threshold: 128},
		EncodeOptions{MaxBytes: 257}); !errors.Is(err, ErrLimit) {
		t.Fatalf("owned output escaped encoding budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Encode(ctx, Adjustment{Kind: Invert}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled encoder accepted: %v", err)
	}
}
