package psdcolor

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"
)

func TestProfileTransformIdentityAlphaStrideOwnership(t *testing.T) {
	profile := profileRGB(4, profileParametric(4,
		2.4, 1/1.055, 0.055/1.055, 1/12.92, 0.04045, 0, 0), profileSRGBColumns)
	input := &image.NRGBA{Rect: image.Rect(-3, 7, 1, 9), Stride: 20, Pix: []byte{
		0, 12, 255, 0, 128, 64, 192, 127, 5, 90, 200, 255, 255, 255, 255, 64, 1, 2, 3, 4,
		255, 0, 255, 128, 70, 120, 160, 255, 33, 22, 11, 1, 1, 2, 3, 4,
	}}
	originalImage, originalProfile := bytes.Clone(input.Pix), bytes.Clone(profile)
	result, err := TransformProfile(nil, input, profile, profile)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rect != input.Rect || result.Stride != 16 {
		t.Fatalf("rectangle or tight stride changed: %+v", result)
	}
	for y := input.Rect.Min.Y; y < input.Rect.Max.Y; y++ {
		for x := input.Rect.Min.X; x < input.Rect.Max.X; x++ {
			if result.NRGBAAt(x, y) != input.NRGBAAt(x, y) {
				t.Fatalf("identity/alpha mismatch at %d,%d: %v/%v", x, y, result.NRGBAAt(x, y), input.NRGBAAt(x, y))
			}
		}
	}
	result.Pix[0] = 90
	if !bytes.Equal(input.Pix, originalImage) || !bytes.Equal(profile, originalProfile) {
		t.Fatal("conversion changed caller-owned input or profile bytes")
	}
}

func TestProfileTransformIndependentGammaAndMatrix(t *testing.T) {
	columns := [3][3]float64{{1, 0, 0}, {0, 1, 0}, {0, 0, 1}}
	source := profileRGB(2, profileGamma(2), columns)
	destination := profileRGB(4, profileGamma(1), columns)
	input := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	input.SetNRGBA(0, 0, color.NRGBA{128, 64, 192, 127})
	result, err := TransformProfile(nil, input, source, destination)
	if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{64, 16, 145, 127}) {
		t.Fatalf("independent gamma calculation differs: %v/%v", result, err)
	}
	// Source primary columns swap R/G; destination scales its red XYZ column by 2.
	source = profileRGB(4, profileGamma(1), [3][3]float64{{0, 1, 0}, {1, 0, 0}, {0, 0, 1}})
	destination = profileRGB(2, profileGamma(1), [3][3]float64{{2, 0, 0}, {0, 1, 0}, {0, 0, 1}})
	result, err = TransformProfile(nil, input, source, destination)
	if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{32, 128, 192, 127}) {
		t.Fatalf("independent matrix calculation differs: %v/%v", result, err)
	}
	// Float64 PCS retention matters for gamma .5: intermediate 8-bit rounding would change 1 to 0.
	input.SetNRGBA(0, 0, color.NRGBA{1, 1, 1, 255})
	result, err = TransformProfile(nil, input, profileRGB(4, profileGamma(2), columns),
		profileRGB(4, profileGamma(2), columns))
	if err != nil || result.NRGBAAt(0, 0) != (color.NRGBA{1, 1, 1, 255}) {
		t.Fatalf("PCS was quantized before destination encoding: %v/%v", result, err)
	}
}

func TestProfileTransformSampledInverseAndParametricBranches(t *testing.T) {
	table := make([]byte, 20)
	copy(table, "curv")
	binary.BigEndian.PutUint32(table[8:12], 4)
	for index, value := range []uint16{100, 2000, 20000, 65000} {
		binary.BigEndian.PutUint16(table[12+index*2:], value)
	}
	curve, err := parseCurve(context.Background(), table, 64)
	if err != nil || invertibleProfileCurve(context.Background(), curve) != nil {
		t.Fatal(err)
	}
	for _, value := range []float64{0, 0.1, 0.3, 0.5, 0.75, 1} {
		if got := inverseProfileCurve(curve, curve.evaluate(value)); math.Abs(got-value) > 1e-12 {
			t.Fatalf("sampled inverse differs at %g: %g", value, got)
		}
	}
	if inverseProfileCurve(curve, -1) != 0 || inverseProfileCurve(curve, 2) != 1 {
		t.Fatal("inverse did not use the nearest range endpoint")
	}
	for _, testCase := range []struct {
		kind       uint16
		parameters []float64
	}{
		{0, []float64{2}},
		{1, []float64{2, 0.75, 0.25}},
		{2, []float64{2, 0.5, 0, 0.125}},
		{3, []float64{2, 1, 0, 0.25, 0.25}},
		{4, []float64{2, 0.75, 0, 0.125, 0.25, 0.125, 0.0625}},
	} {
		curve, err := parseCurve(context.Background(), profileParametric(testCase.kind, testCase.parameters...), 128)
		if err != nil || invertibleProfileCurve(context.Background(), curve) != nil {
			t.Fatalf("valid type %d rejected: %v", testCase.kind, err)
		}
		for _, value := range []float64{0, 0.1, 0.249, 0.25, 0.5, 0.9, 1} {
			if got := inverseProfileCurve(curve, curve.evaluate(value)); math.Abs(got-value) > 1e-10 {
				t.Fatalf("type %d inverse differs at %g: %g", testCase.kind, value, got)
			}
		}
	}
}

func TestProfileTransformRejectsUnimplementedAndNoninvertibleProfiles(t *testing.T) {
	base := profileRGB(4, profileGamma(1), profileSRGBColumns)
	input := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	table := make([]byte, 18)
	copy(table, "curv")
	binary.BigEndian.PutUint32(table[8:12], 3)
	binary.BigEndian.PutUint16(table[16:18], 65535)
	profiles := map[string][]byte{
		"plateau":             profileRGB(4, table, profileSRGBColumns),
		"constant-param":      profileRGB(4, profileParametric(1, 2, 1, -0.25), profileSRGBColumns),
		"clipped-param":       profileRGB(4, profileParametric(1, 2, 2, 0), profileSRGBColumns),
		"decreasing-boundary": profileRGB(4, profileParametric(3, 2, 1, 0, 1, 0.5), profileSRGBColumns),
		"singular":            profileRGB(4, profileGamma(1), [3][3]float64{{1, 0, 0}, {1, 0, 0}, {0, 0, 1}}),
		"lut":                 profileFixture("RGB ", "XYZ ", 4, profileFixtureTag{"A2B0", make([]byte, 12)}),
		"gray": profileFixture("GRAY", "XYZ ", 4,
			profileFixtureTag{"wtpt", profileXYZ(iccWhite)}, profileFixtureTag{"kTRC", profileGamma(1)}),
	}
	for name, profile := range profiles {
		t.Run(name, func(t *testing.T) {
			if result, err := TransformProfile(nil, input, base, profile); result != nil ||
				!errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsupported profile accepted: %v/%v", result, err)
			}
		})
	}
	for _, name := range []string{"singular", "lut", "gray"} {
		if _, err := TransformProfile(nil, input, profiles[name], base); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported source %s accepted: %v", name, err)
		}
	}
	if _, err := TransformProfile(nil, input, nil, base); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty profile was silently assumed: %v", err)
	}
	input.Pix = []byte{12, 128, 255, 33}
	result, err := TransformProfile(nil, input, nil, nil, ProfileTransformOptions{AssumeSRGB: true})
	if err != nil || !bytes.Equal(result.Pix, input.Pix) {
		t.Fatalf("explicit assumed sRGB identity differs: %v/%v", result, err)
	}
}

func TestProfileTransformBudgetsBadBuffersAndCancellation(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	base := profileRGB(4, profileGamma(1), profileSRGBColumns)
	settings, _ := profileTransformSettings(nil)
	cost, err := profileTransformCost(context.Background(), base, settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		options ProfileTransformOptions
		want    error
	}{
		{ProfileTransformOptions{MaxPixels: 3}, ErrLimit},
		{ProfileTransformOptions{MaxProfileBytes: int64(len(base) - 1)}, ErrLimit},
		{ProfileTransformOptions{MaxWorkingBytes: cost*2 + 15}, ErrLimit},
		{ProfileTransformOptions{MaxWorkingBytes: -1}, ErrInvalid},
	} {
		if result, err := TransformProfile(nil, input, base, base, testCase.options); result != nil ||
			!errors.Is(err, testCase.want) {
			t.Fatalf("budget contract differs: %v/%v", result, err)
		}
	}
	if _, err := TransformProfile(nil, input, base, base,
		ProfileTransformOptions{MaxWorkingBytes: cost*2 + 16}); err != nil {
		t.Fatalf("exact aggregate budget rejected: %v", err)
	}
	estimate, err := ProfileTransformWorkingBytes(nil, input, base, base)
	if err != nil || estimate != cost*2+16 {
		t.Fatalf("aggregate estimate differs: %d/%v", estimate, err)
	}
	if _, err := ProfileTransformWorkingBytes(nil, input, base, base,
		ProfileTransformOptions{MaxWorkingBytes: estimate - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("estimator accepted insufficient peak budget: %v", err)
	}
	for _, invalid := range []*image.NRGBA{
		nil,
		{Rect: image.Rect(0, 0, 2, 2), Stride: 8, Pix: make([]byte, 15)},
		{Rect: image.Rect(0, 0, 2, 2), Stride: 7, Pix: make([]byte, 16)},
		{Rect: image.Rectangle{Min: image.Pt(3, 0), Max: image.Pt(0, 1)}},
	} {
		if _, err := TransformProfile(nil, invalid, base, base); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid buffer accepted: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := TransformProfile(ctx, input, base, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if _, err := ProfileTransformWorkingBytes(ctx, input, base, base); !errors.Is(err, context.Canceled) {
		t.Fatalf("estimate cancellation ignored: %v", err)
	}
	if _, err := TransformProfile(nil, input, base, base, ProfileTransformOptions{},
		ProfileTransformOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("multiple option sets accepted: %v", err)
	}
}

type profileCancelContext struct {
	context.Context
	checks, cancelAt int
}

func (ctx *profileCancelContext) Err() error {
	ctx.checks++
	if ctx.checks >= ctx.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestProfileTransformCancellationDuringPixelsAndCurveValidation(t *testing.T) {
	input := image.NewNRGBA(image.Rect(0, 0, 2048, 1))
	original := bytes.Clone(input.Pix)
	ctx := &profileCancelContext{Context: context.Background(), cancelAt: 4}
	if result, err := TransformProfile(ctx, input, nil, nil,
		ProfileTransformOptions{AssumeSRGB: true}); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pixel-loop cancellation ignored: %v/%v", result, err)
	}
	if !bytes.Equal(input.Pix, original) {
		t.Fatal("cancelled conversion changed input")
	}
	curve := toneCurve{table: make([]uint16, 8192)}
	for index := range curve.table {
		curve.table[index] = uint16(index * 8)
	}
	ctx = &profileCancelContext{Context: context.Background(), cancelAt: 1}
	if err := invertibleProfileCurve(ctx, curve); !errors.Is(err, context.Canceled) {
		t.Fatalf("large inverse-curve validation ignored cancellation: %v", err)
	}
	// Empty width must not scan an arbitrarily large count of empty rows.
	input = &image.NRGBA{Rect: image.Rect(0, 0, 0, int(^uint(0)>>1)), Stride: 0}
	result, err := TransformProfile(nil, input, nil, nil, ProfileTransformOptions{AssumeSRGB: true})
	if err != nil || len(result.Pix) != 0 {
		t.Fatalf("empty image contract differs: %v/%v", result, err)
	}
}
