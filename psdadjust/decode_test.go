package psdadjust

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func adjustmentLevelBytes(inputBlack, inputWhite, outputBlack, outputWhite, gamma uint16) []byte {
	var data []byte
	for _, value := range []uint16{inputBlack, inputWhite, outputBlack, outputWhite, gamma} {
		data = binary.BigEndian.AppendUint16(data, value)
	}
	return data
}

func adjustmentLevelsFixture() []byte {
	data := binary.BigEndian.AppendUint16(nil, 2)
	data = append(data, adjustmentLevelBytes(20, 220, 10, 240, 130)...)
	for range 26 {
		data = append(data, adjustmentLevelBytes(0, 255, 0, 255, 100)...)
	}
	data = append(data, make([]byte, 20)...)
	data = append(data, []byte("Lvls")...)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint16(data, 30)
	data = append(data, adjustmentLevelBytes(0, 255, 0, 255, 100)...)
	return data
}

func adjustmentCurvePoints(points ...CurvePoint) []byte {
	data := binary.BigEndian.AppendUint16(nil, uint16(len(points)))
	for _, point := range points {
		data = binary.BigEndian.AppendUint16(data, point.Output)
		data = binary.BigEndian.AppendUint16(data, point.Input)
	}
	return data
}

func TestDecodeAdjustmentSimpleFieldsRawOwnershipAndSourceOffsets(t *testing.T) {
	for _, test := range []struct {
		key  string
		data []byte
		kind Kind
	}{
		{"nvrt", nil, Invert}, {"thrs", []byte{0, 128, 0, 0}, Threshold}, {"post", []byte{0, 4}, Posterize},
		{"brit", []byte{0xff, 0xe2, 0, 20, 0, 128, 0, 0}, BrightnessContrast},
	} {
		t.Run(test.key, func(t *testing.T) {
			data := bytes.Clone(test.data)
			adjustment, err := Decode(nil, test.key, data, DecodeOptions{SourceOffset: 117})
			if err != nil || adjustment.Kind != test.kind || adjustment.Key != test.key ||
				adjustment.Span != (Span{Offset: 117, Length: int64(len(data))}) || !bytes.Equal(adjustment.Raw, data) {
				t.Fatalf("typed fields or retained original encoding differs: %v", err)
			}
			if test.key == "thrs" && adjustment.Threshold != 128 || test.key == "post" && adjustment.Posterize != 4 {
				t.Fatal("simple numeric controls lost")
			}
			if test.key == "brit" && (adjustment.BrightnessContrast.Brightness != -30 ||
				adjustment.BrightnessContrast.Contrast != 20 || adjustment.BrightnessContrast.Mean != 128 ||
				!adjustment.BrightnessContrast.Legacy) {
				t.Fatal("signed legacy brightness/contrast fields lost")
			}
			clear(data)
			if !bytes.Equal(adjustment.Raw, test.data) {
				t.Fatal("raw adjustment bytes borrowed their input")
			}
		})
	}
}

func TestDecodeLevelsLegacyAndExtraRecords(t *testing.T) {
	data := adjustmentLevelsFixture()
	adjustment, err := Decode(nil, "levl", data)
	if err != nil || adjustment.Version != 2 || adjustment.ExtraVersion != 3 || len(adjustment.Levels) != 30 {
		t.Fatalf("legacy and extra level records differ: %v", err)
	}
	level := adjustment.Levels[0]
	if level.Channel != 0 || level.InputBlack != 20 || level.InputWhite != 220 || level.OutputBlack != 10 ||
		level.OutputWhite != 240 || level.Gamma != 1.3 || adjustment.Levels[29].Channel != 29 {
		t.Fatal("level channel order or gamma precision lost")
	}
	if _, err := Compile(*adjustment); err != nil {
		t.Fatalf("default extra/reserved levels were not recognized: %v", err)
	}
}

func TestDecodeCurvesBitmapExtraOverridesAndMappedLUT(t *testing.T) {
	data := []byte{0, 0, 1}
	data = binary.BigEndian.AppendUint32(data, 1<<2)
	legacy := []CurvePoint{{Input: 0, Output: 0}, {Input: 255, Output: 255}}
	data = append(data, adjustmentCurvePoints(legacy...)...)
	data = append(data, []byte("Crv ")...)
	data = binary.BigEndian.AppendUint16(data, 4)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint16(data, 2)
	data = append(data, adjustmentCurvePoints(CurvePoint{Input: 0, Output: 10}, CurvePoint{Input: 255, Output: 230})...)
	adjustment, err := Decode(nil, "curv", data)
	if err != nil || adjustment.CountMap != 4 || adjustment.Curves[0].Channel != 2 ||
		adjustment.LegacyCurves[0].Channel != 2 || adjustment.Curves[0].Points[0].Output != 10 ||
		adjustment.LegacyCurves[0].Points[0].Output != 0 {
		t.Fatalf("bitmap channel mapping or extra-marker preservation differs: %v", err)
	}
	data = []byte{1, 0, 4}
	data = binary.BigEndian.AppendUint32(data, 1)
	for value := range 256 {
		data = append(data, uint8(value))
	}
	adjustment, err = Decode(nil, "curv", data)
	if err != nil || !adjustment.CurveMap || len(adjustment.Curves[0].LUT) != 256 {
		t.Fatalf("mapped curve lookup data was not retained: %v", err)
	}
	clear(data)
	if adjustment.Curves[0].LUT[255] != 255 {
		t.Fatal("mapped lookup table borrowed its input")
	}
}

func TestDecodeExposurePreservesFloat32AndRejectsUnknownSemantics(t *testing.T) {
	data := binary.BigEndian.AppendUint16(nil, 1)
	for _, value := range []float32{.5, .02, 1.2} {
		data = binary.BigEndian.AppendUint32(data, math.Float32bits(value))
	}
	adjustment, err := Decode(nil, "expA", data)
	if err != nil || adjustment.Exposure.Exposure != .5 || adjustment.Exposure.Offset != float64(float32(.02)) ||
		adjustment.Exposure.Gamma != float64(float32(1.2)) {
		t.Fatalf("exposure source-precision values differ: %v", err)
	}
	for _, test := range []struct {
		key  string
		data []byte
	}{
		{"CgEd", []byte{0, 0, 0, 16}}, {"hue2", []byte{0, 3}},
		{"post", []byte{0, 4, 0x87}}, {"levl", []byte{0, 3}}, {"expA", []byte{0, 2}},
	} {
		adjustment, err := Decode(nil, test.key, test.data)
		if !errors.Is(err, ErrUnsupported) || adjustment == nil || !bytes.Equal(adjustment.Raw, test.data) {
			t.Fatalf("unknown semantics were approximated or original data discarded: %v", err)
		}
	}
}

func TestDecodeAdjustmentTruncationBudgetsAndCancellation(t *testing.T) {
	valid := adjustmentLevelsFixture()
	for length := range 292 {
		if _, err := Decode(nil, "levl", valid[:length]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("truncated levels length=%d accepted: %v", length, err)
		}
	}
	if _, err := Decode(nil, "levl", valid, DecodeOptions{MaxBytes: 100}); !errors.Is(err, ErrLimit) {
		t.Fatalf("decode budget ignored: %v", err)
	}
	_, err := Decode(nil, "thrs", []byte{0, 128}, DecodeOptions{SourceOffset: math.MaxInt64})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("source span overflow accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, "nvrt", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled decoder ignored cancellation: %v", err)
	}
}

func FuzzDecodeAdjustment(f *testing.F) {
	f.Add("levl", adjustmentLevelsFixture())
	f.Add("thrs", []byte{0, 128})
	f.Add("curv", []byte{0, 0, 1, 0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, key string, data []byte) {
		if len(data) > 8192 {
			return
		}
		adjustment, err := Decode(nil, key, data, DecodeOptions{MaxBytes: 64 << 10})
		if err == nil {
			if _, err := Compile(*adjustment); err == nil {
				encoded, err := Encode(nil, *adjustment, EncodeOptions{MaxBytes: 64 << 10})
				if err == nil {
					if _, err := Decode(nil, key, encoded, DecodeOptions{MaxBytes: 64 << 10}); err != nil {
						t.Fatalf("successful encoder emitted invalid adjustment data: %v", err)
					}
				}
			}
		}
	})
}
