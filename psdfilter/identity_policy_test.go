package psdfilter

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

func exactSpatialIdentities() []Filter {
	return []Filter{
		{Kind: GaussianBlur, Enabled: true, Opacity: 1, BlendMode: "norm"},
		{Kind: SurfaceBlur, Enabled: true, Threshold: 5, Opacity: 1, BlendMode: "norm"},
		{Kind: SurfaceBlur, Enabled: true, Radius: 3, Opacity: 1, BlendMode: "norm"},
		{Kind: SmartSharpen, Enabled: true, Amount: 2, BlurModel: "lens-blur", Opacity: 1, BlendMode: "norm"},
		{Kind: SmartSharpen, Enabled: true, Radius: 2, BlurModel: "lens-blur", Opacity: 1, BlendMode: "norm"},
	}
}

func TestSpatialIdentityDefaultsPreserveExactStridedPixelsAndBudget(t *testing.T) {
	source := &image.NRGBA{Rect: image.Rect(-3, 4, -1, 6), Stride: 12, Pix: []byte{
		255, 1, 77, 0, 80, 90, 100, 128, 91, 92, 93, 94,
		20, 30, 40, 1, 250, 251, 252, 255,
	}}
	original := bytes.Clone(source.Pix)
	expected := append(bytes.Clone(source.Pix[:8]), source.Pix[12:20]...)
	for _, operation := range exactSpatialIdentities() {
		for _, opacity := range []float64{.25, 1} {
			operation.Opacity = opacity
			stack := Stack{Enabled: true, Filters: []Filter{operation}}
			if radius, err := RequiredHalo(stack); err != nil || radius != 0 {
				t.Fatalf("validated identity has spatial support: %d, %v", radius, err)
			}
			estimate, err := WorkingEstimate(source, stack)
			if err != nil || estimate.WorkingBytes != 16 || estimate.RetainedBytes != 16 {
				t.Fatalf("identity required algorithm scratch: %+v, %v", estimate, err)
			}
			output, err := Apply(nil, source, stack, Options{MaxWorkingBytes: 16, MaxSampleOperations: 1})
			if err != nil || output.Rect != source.Rect || !bytes.Equal(output.Pix, expected) {
				t.Fatalf("default spatial identity changed pixels: %v", err)
			}
			clear(output.Pix)
			if !bytes.Equal(source.Pix, original) {
				t.Fatal("identity output borrowed or changed source storage")
			}
			if output, err := Apply(nil, source, stack, Options{MaxWorkingBytes: 15}); output != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("identity bypassed output budget: %v", err)
			}
		}
	}
}

func TestSpatialIdentityDefaultsKeepFullValidationAndBlendPolicy(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for _, operation := range exactSpatialIdentities() {
		for _, mode := range []string{"mul ", "scrn", "unknown"} {
			operation.BlendMode, operation.Opacity = mode, .25
			if output, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{operation}}); output != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("identity algorithm bypassed active filter blending: %v", err)
			}
		}
	}
	for _, item := range []struct {
		operation Filter
		want      error
	}{
		{Filter{Kind: GaussianBlur, Adjustment: &psdadjust.Adjustment{}}, ErrInvalid},
		{Filter{Kind: SurfaceBlur, Threshold: math.NaN()}, ErrInvalid},
		{Filter{Kind: SurfaceBlur, Radius: 33}, ErrLimit},
		{Filter{Kind: SmartSharpen, BlurModel: "lens-blur", NoiseReduction: math.NaN()}, ErrInvalid},
		{Filter{Kind: SmartSharpen, BlurModel: "motion-blur"}, ErrUnsupported},
		{Filter{Kind: SmartSharpen, BlurModel: "lens-blur", UseLegacy: true}, ErrUnsupported},
		{Filter{Kind: SmartSharpen, BlurModel: "lens-blur", Radius: 33}, ErrLimit},
	} {
		item.operation.Enabled, item.operation.Opacity, item.operation.BlendMode = true, 1, "norm"
		if _, err := SupportRadius(item.operation); !errors.Is(err, item.want) {
			t.Fatalf("invalid or unsupported identity parameters bypassed validation: %v", err)
		}
	}
	for _, operation := range []Filter{gaussianStack(1).Filters[0], surfaceFilter(), sharpenFilter()} {
		if _, err := SupportRadius(operation); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("active spatial algorithm lost explicit approximation gate: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if output, err := Apply(ctx, source, Stack{Enabled: true, Filters: exactSpatialIdentities()}); output != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("identity stack ignored cancellation: %v", err)
	}
}

func TestSmartFilterPlainLightnessUsesDefaultVerifiedTransferAfterIdentities(t *testing.T) {
	source := image.NewNRGBA(image.Rect(5, -1, 7, 0))
	copy(source.Pix, []byte{255, 128, 0, 128, 89, 166, 157, 255})
	stack := Stack{Enabled: true, Filters: exactSpatialIdentities()}
	lightness := hueFilter(&psdadjust.HueSaturationParams{
		Master: psdadjust.HueSaturationValues{Lightness: -53},
	})
	stack.Filters = append(stack.Filters, lightness)
	output, err := Apply(nil, source, stack)
	if err != nil || !bytes.Equal(output.Pix, []byte{120, 60, 0, 128, 42, 78, 74, 255}) {
		t.Fatalf("smart filter did not inherit verified default Lightness: %v, %v", err, output)
	}
	lightness.Adjustment.HueSaturation.Ranges[0].Values.Lightness = 1
	if result, err := Apply(nil, source, stack); result != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("identity prefix let unverified local controls through: %v", err)
	}
}
