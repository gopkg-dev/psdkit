package psdadjust

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMetadataAdjustmentsEncodeSignedBoundsWithoutEnablingRendering(t *testing.T) {
	for _, adjustment := range []Adjustment{
		{Kind: ColorBalance, ColorBalance: &ColorBalanceParams{
			Shadows: [3]int16{-100, 0, 100}, Midtones: [3]int16{100, -100, 0},
			Highlights: [3]int16{0, 100, -100}, PreserveLuminosity: true}},
		{Kind: BrightnessContrast, BrightnessContrast: &BrightnessContrastParams{
			Brightness: -100, Contrast: 100, Mean: 255, Legacy: true}},
		{Kind: BrightnessContrast, BrightnessContrast: &BrightnessContrastParams{
			Brightness: 100, Contrast: -100, Legacy: true}},
	} {
		encoded, err := Encode(nil, adjustment)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(nil, adjustmentKey(adjustment.Kind), encoded)
		if err != nil || !sameAdjustment(adjustment, *decoded) {
			t.Fatalf("metadata limits, signed fields or flags changed: %v", err)
		}
		if _, err := Compile(*decoded); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("encoding enabled unverified transfer: %v", err)
		}
		if _, err := Encode(nil, adjustment, EncodeOptions{MaxBytes: 257}); !errors.Is(err, ErrLimit) {
			t.Fatalf("metadata output exceeded budget: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Encode(ctx, adjustment); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled metadata encoding was not rejected: %v", err)
		}
		adjustment.Unsupported = []string{"unknown semantics"}
		if _, err := Encode(nil, adjustment); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown metadata was silently discarded: %v", err)
		}
	}
}

func TestLegacyBrightnessEncodingPatchesOnlyKnownFields(t *testing.T) {
	raw := []byte{0xff, 0xe2, 0, 20, 0, 128, 0, 0, 0, 0}
	adjustment, err := Decode(nil, "brit", raw)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := Encode(nil, *adjustment)
	if err != nil || !bytes.Equal(unchanged, raw) {
		t.Fatalf("non-neutral unchanged legacy bytes were not preserved: %v", err)
	}
	adjustment.BrightnessContrast.Brightness = 40
	adjustment.BrightnessContrast.Contrast = -50
	adjustment.BrightnessContrast.Mean = 99
	encoded, err := Encode(nil, *adjustment)
	if err != nil || !bytes.Equal(encoded, []byte{0, 40, 0xff, 0xce, 0, 99, 0, 0, 0, 0}) ||
		!bytes.Equal(adjustment.Raw, raw) {
		t.Fatalf("legacy controls, padding or original bytes changed incorrectly: %v", err)
	}
	decoded, err := Decode(nil, "brit", encoded)
	if err != nil || !reflect.DeepEqual(decoded.BrightnessContrast, adjustment.BrightnessContrast) {
		t.Fatalf("edited legacy controls changed after reopening: %v", err)
	}
}

func TestMetadataEncodingRetainsStrictModeVersionAndValueValidation(t *testing.T) {
	for _, parameters := range []*BrightnessContrastParams{
		nil, {Brightness: -101, Legacy: true}, {Brightness: 101, Legacy: true},
		{Contrast: -101, Legacy: true}, {Contrast: 101, Legacy: true}, {Mean: 256, Legacy: true},
	} {
		if _, err := Encode(nil, Adjustment{Kind: BrightnessContrast, BrightnessContrast: parameters}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid legacy metadata accepted: %v", err)
		}
	}
	for tone := range 3 {
		for component := range 3 {
			for _, value := range []int16{-101, 101} {
				parameters := &ColorBalanceParams{}
				ranges := []*[3]int16{&parameters.Shadows, &parameters.Midtones, &parameters.Highlights}
				ranges[tone][component] = value
				if _, err := Encode(nil, Adjustment{Kind: ColorBalance, ColorBalance: parameters}); !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid color offset accepted for tone %d component %d: %v", tone, component, err)
				}
			}
		}
	}
	for _, adjustment := range []Adjustment{
		{Kind: BrightnessContrast, BrightnessContrast: &BrightnessContrastParams{}},
		{Kind: BrightnessContrast, BrightnessContrast: &BrightnessContrastParams{Legacy: true, LabOnly: true}},
		{Kind: BrightnessContrast, Version: 1, BrightnessContrast: &BrightnessContrastParams{Legacy: true}},
		{Kind: ColorBalance, ExtraVersion: 1, ColorBalance: &ColorBalanceParams{}},
		{Kind: BrightnessContrast, Raw: []byte{0, 0, 0, 0, 0, 0, 0, 77},
			BrightnessContrast: &BrightnessContrastParams{Legacy: true}},
	} {
		if _, err := Encode(nil, adjustment); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported source mode, version or tail was normalized: %v", err)
		}
	}
}
