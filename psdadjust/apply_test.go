package psdadjust

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func adjustmentPixels(values ...uint8) *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, len(values)/4, 1))
	copy(result.Pix, values)
	return result
}

func adjustmentExpect(t *testing.T, source *image.NRGBA, adjustment Adjustment, expected []byte) {
	t.Helper()
	before := bytes.Clone(source.Pix)
	result, err := Apply(nil, source, adjustment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Pix, expected) {
		t.Fatalf("adjustment pixels=%v want=%v", result.Pix, expected)
	}
	if !bytes.Equal(source.Pix, before) || result == source || result.Rect != source.Rect {
		t.Fatal("adjustment changed source pixels or borrowed its output")
	}
	clear(result.Pix)
	if !bytes.Equal(source.Pix, before) {
		t.Fatal("output mutation changed source pixels")
	}
}

func TestApplyInvertThresholdPosterizeBoundariesAndAlpha(t *testing.T) {
	adjustmentExpect(t, adjustmentPixels(0, 64, 255, 0, 127, 128, 200, 133),
		Adjustment{Kind: Invert}, []byte{255, 191, 0, 0, 128, 127, 55, 133})
	adjustmentExpect(t, adjustmentPixels(127, 127, 127, 9, 128, 128, 128, 220),
		Adjustment{Kind: Threshold, Threshold: 128}, []byte{0, 0, 0, 9, 255, 255, 255, 220})
	adjustmentExpect(t, adjustmentPixels(63, 64, 127, 44, 128, 191, 192, 99, 0, 255, 255, 255),
		Adjustment{Kind: Posterize, Posterize: 4}, []byte{0, 85, 85, 44, 170, 170, 255, 99, 0, 255, 255, 255})
	adjustmentExpect(t, adjustmentPixels(255, 0, 0, 12, 0, 255, 0, 210, 0, 0, 255, 128),
		Adjustment{Kind: Threshold, Threshold: 100}, []byte{0, 0, 0, 12, 255, 255, 255, 210, 0, 0, 0, 128})
}

func TestApplyLevelsOrderGammaOutputInversionAndReservedChannels(t *testing.T) {
	adjustmentExpect(t, adjustmentPixels(200, 100, 50, 128), Adjustment{Kind: Levels, Levels: []LevelChannel{
		{Channel: 0, InputWhite: 128, OutputWhite: 255, Gamma: 1},
		{Channel: 1, InputWhite: 255, OutputWhite: 128, Gamma: 1},
		{Channel: 27},
	}}, []byte{200, 199, 100, 128})
	adjustmentExpect(t, adjustmentPixels(0, 64, 255, 99), Adjustment{Kind: Levels, Levels: []LevelChannel{
		{Channel: 0, InputWhite: 255, OutputWhite: 255, Gamma: 2},
	}}, []byte{0, 128, 255, 99})
	adjustmentExpect(t, adjustmentPixels(0, 128, 255, 44), Adjustment{Kind: Levels, Levels: []LevelChannel{
		{Channel: 0, InputWhite: 255, OutputBlack: 255, Gamma: 1},
	}}, []byte{255, 127, 0, 44})
	adjustment := Adjustment{Kind: Levels, Levels: []LevelChannel{
		{Channel: 1, InputWhite: 255, OutputWhite: 128, Gamma: 1},
	}}
	transform, err := Compile(adjustment)
	if err != nil {
		t.Fatal(err)
	}
	adjustment.Levels[0].OutputWhite = 255
	if got := transform.RGB([3]float64{1, .5, .25}); got != ([3]float64{128.0 / 255, .5, .25}) {
		t.Fatalf("compiled transform retained caller-owned level records: %v", got)
	}
}

func TestApplyNaturalCurvesLUTAndEndpointClamping(t *testing.T) {
	adjustmentExpect(t, adjustmentPixels(64, 128, 192, 155), Adjustment{Kind: Curves, Curves: []CurveChannel{
		{Channel: 0, Points: []CurvePoint{{Input: 0, Output: 0}, {Input: 128, Output: 192}, {Input: 255, Output: 255}}},
	}}, []byte{108, 192, 236, 155})
	adjustmentExpect(t, adjustmentPixels(0, 110, 255, 155), Adjustment{Kind: Curves, Curves: []CurveChannel{
		{Channel: 0, Points: []CurvePoint{{Input: 20, Output: 10}, {Input: 200, Output: 240}}},
	}}, []byte{10, 125, 240, 155})
	lut := make([]byte, 256)
	for index := range lut {
		lut[index] = byte(255 - index)
	}
	adjustment := Adjustment{Kind: Curves, Curves: []CurveChannel{{Channel: 0, LUT: lut}}}
	transform, err := Compile(adjustment)
	if err != nil {
		t.Fatal(err)
	}
	clear(lut)
	color := transform.RGB([3]float64{0, 64.0 / 255, 1})
	if color != ([3]float64{1, 191.0 / 255, 0}) {
		t.Fatalf("compiled LUT retained caller memory: %v", color)
	}
}

func TestApplyExposureNativeRGBPowerTransferAndClampPolicy(t *testing.T) {
	adjustmentExpect(t, adjustmentPixels(255, 255, 255, 27), Adjustment{Kind: Exposure,
		Exposure: &ExposureParams{Exposure: -1, Gamma: 1}}, []byte{186, 186, 186, 27})
	adjustmentExpect(t, adjustmentPixels(0, 128, 255, 255), Adjustment{Kind: Exposure,
		Exposure: &ExposureParams{Exposure: 20, Offset: -.5, Gamma: 1}}, []byte{0, 255, 255, 255})
	adjustmentExpect(t, adjustmentPixels(0, 1, 12, 0, 13, 128, 255, 111), Adjustment{Kind: Exposure,
		Exposure: &ExposureParams{Gamma: 1}}, []byte{0, 1, 12, 0, 13, 128, 255, 111})
	// Native Photoshop's assigned-sRGB RGB8 adjustment gives these exact straight-alpha colors.
	adjustmentExpect(t, adjustmentPixels(20, 80, 180, 99, 200, 60, 20, 137), Adjustment{Kind: Exposure,
		Exposure: &ExposureParams{Exposure: .5, Offset: float64(float32(.02)), Gamma: float64(float32(1.2))}},
		[]byte{63, 118, 220, 99, 240, 97, 63, 137})
}

func TestApplyParametersGeometryBudgetsAndCancellation(t *testing.T) {
	for _, adjustment := range []Adjustment{
		{Kind: "future"},
		{Kind: BrightnessContrast, BrightnessContrast: &BrightnessContrastParams{Brightness: 1, Legacy: true}},
		{Kind: Levels, Levels: []LevelChannel{{Channel: 4, InputWhite: 255, OutputWhite: 128, Gamma: 1}}},
		{Kind: Curves, Curves: []CurveChannel{{Points: []CurvePoint{
			{Input: 0, Output: 0}, {Input: 100, Output: 180}, {Input: 255, Output: 100},
		}}}},
	} {
		if _, err := Compile(adjustment); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unimplemented semantics were silently approximated: %v", err)
		}
	}
	for _, adjustment := range []Adjustment{
		{Kind: Threshold, Threshold: 256}, {Kind: Posterize, Posterize: 1},
		{Kind: Exposure, Exposure: &ExposureParams{Gamma: 0}},
		{Kind: Exposure, Exposure: &ExposureParams{Exposure: math.NaN(), Gamma: 1}},
		{Kind: Levels, Levels: []LevelChannel{{InputBlack: 128, InputWhite: 100, Gamma: 1}}},
		{Kind: Levels, Levels: []LevelChannel{{InputWhite: 255, OutputWhite: 255, Gamma: math.Inf(1)}}},
		{Kind: Levels, Levels: []LevelChannel{
			{InputWhite: 255, OutputWhite: 255, Gamma: 1}, {InputWhite: 255, OutputWhite: 255, Gamma: 1},
		}},
		{Kind: Curves, Curves: []CurveChannel{{Points: []CurvePoint{{Input: 0}, {Input: 0, Output: 255}}}}},
	} {
		if _, err := Compile(adjustment); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid parameters accepted: %v", err)
		}
	}
	for _, source := range []*image.NRGBA{
		nil, {Rect: image.Rect(0, 0, 1, 2), Stride: math.MaxInt, Pix: make([]byte, 8)},
		{Rect: image.Rect(0, 0, 1, 1), Stride: -4, Pix: make([]byte, 4)},
		{Rect: image.Rectangle{Min: image.Pt(2, 1), Max: image.Pt(1, 2)}},
		{Rect: image.Rect(0, 0, 2, 1), Stride: 8, Pix: make([]byte, 7)},
	} {
		if _, err := Apply(nil, source, Adjustment{Kind: Invert}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid backing geometry accepted: %v", err)
		}
	}
	source := adjustmentPixels(1, 2, 3, 4, 5, 6, 7, 8)
	for _, options := range []Options{{MaxPixels: 1}, {MaxWorkingBytes: 8}} {
		if _, err := Apply(nil, source, Adjustment{Kind: Invert}, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("working or pixel limit was ignored: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Apply(ctx, source, Adjustment{Kind: Invert}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled adjustment: %v", err)
	}
}

func TestApplyNonzeroRectangleAndPaddedStride(t *testing.T) {
	source := &image.NRGBA{Rect: image.Rect(13, -4, 15, -2), Stride: 12, Pix: []byte{
		0, 10, 20, 111, 30, 40, 50, 222, 0xa5, 0xa5, 0xa5, 0xa5,
		60, 70, 80, 123, 90, 100, 110, 231,
	}}
	adjustmentExpect(t, source, Adjustment{Kind: Invert}, []byte{
		255, 245, 235, 111, 225, 215, 205, 222, 195, 185, 175, 123, 165, 155, 145, 231,
	})
}
