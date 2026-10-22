package psdfilter

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"
)

func spatialOptions() Options {
	return Options{ApproximateGaussian: true, ApproximateSurfaceBlur: true, ApproximateSmartSharpen: true}
}

func surfaceFilter() Filter {
	return Filter{Kind: SurfaceBlur, Enabled: true, Radius: 3, Threshold: 5, Opacity: 1, BlendMode: "norm"}
}

func sharpenFilter() Filter {
	return Filter{Kind: SmartSharpen, Enabled: true, Radius: 1.9, Amount: 3.76,
		NoiseReduction: .22, BlurModel: "lens-blur", Opacity: 1, BlendMode: "norm"}
}

func TestSurfaceBlurPreservesHardEdgesAndThresholdControlsNoise(t *testing.T) {
	source := image.NewNRGBA(image.Rect(-3, 5, 24, 12))
	for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
		for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
			value := uint8(80 + (x+3)%2*4)
			if x >= 10 {
				value = 180
			}
			source.SetNRGBA(x, y, color.NRGBA{R: value, G: value, B: value, A: 173})
		}
	}
	filter := surfaceFilter()
	result, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{filter}}, spatialOptions())
	if err != nil {
		t.Fatal(err)
	}
	if pixel := result.NRGBAAt(3, 8); pixel.R <= 80 || pixel.R >= 84 || pixel.A != 173 {
		t.Fatalf("small noise was not smoothed with center alpha retained: %v", pixel)
	}
	if pixel := result.NRGBAAt(10, 8); pixel != (color.NRGBA{R: 180, G: 180, B: 180, A: 173}) {
		t.Fatalf("range gate mixed the hard edge: %v", pixel)
	}
	filter.Threshold = 1
	narrow, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{filter}}, spatialOptions())
	if err != nil || !bytes.Equal(narrow.Pix, source.Pix) {
		t.Fatalf("lower threshold mixed colors beyond its range: %v", err)
	}
}

func TestSmartSharpenActualControlsAndNoiseReduction(t *testing.T) {
	source := image.NewNRGBA(image.Rect(2, -3, 23, 11))
	for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
		for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
			value := uint8(116 + (x+y+20)%2*8)
			source.SetNRGBA(x, y, color.NRGBA{R: value, G: value + 21, B: value + 42, A: 137})
		}
	}
	before := bytes.Clone(source.Pix)
	for _, controls := range [][2]float64{{1.9, 3.76}, {.5, .7}} {
		filter := sharpenFilter()
		filter.Radius, filter.Amount = controls[0], controls[1]
		result, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{filter}}, spatialOptions())
		if err != nil || bytes.Equal(result.Pix, source.Pix) {
			t.Fatalf("actual radius/amount did not execute: %v, %v", controls, err)
		}
		withoutReduction := filter
		withoutReduction.NoiseReduction = 0
		stronger, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{withoutReduction}}, spatialOptions())
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(result.Pix, stronger.Pix) {
			t.Fatal("noise reduction parameter had no observable effect")
		}
		for offset := 0; offset < len(source.Pix); offset += 4 {
			if result.Pix[offset+3] != source.Pix[offset+3] {
				t.Fatal("sharpen changed source alpha")
			}
			if math.Abs(float64(result.Pix[offset])-float64(source.Pix[offset])) >
				math.Abs(float64(stronger.Pix[offset])-float64(source.Pix[offset])) {
				t.Fatal("noise reduction increased low-amplitude sharpening")
			}
		}
	}
	if !bytes.Equal(before, source.Pix) {
		t.Fatal("sharpen mutated caller-owned pixels")
	}
}

func TestSpatialTransparentColorIsolationUniformAndStridedOwnership(t *testing.T) {
	container := image.NewNRGBA(image.Rect(-5, -4, 17, 19))
	for offset := 0; offset < len(container.Pix); offset += 4 {
		copy(container.Pix[offset:offset+4], []byte{40, 80, 120, 91})
	}
	source := container.SubImage(image.Rect(1, 2, 12, 14)).(*image.NRGBA)
	source.SetNRGBA(6, 8, color.NRGBA{R: 255, G: 0, B: 255, A: 0})
	before := bytes.Clone(container.Pix)
	for _, filter := range []Filter{surfaceFilter(), sharpenFilter()} {
		filter.Threshold = 255
		result, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{filter}}, spatialOptions())
		if err != nil {
			t.Fatal(err)
		}
		for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
			for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
				if result.NRGBAAt(x, y) != source.NRGBAAt(x, y) {
					t.Fatalf("uniform visible RGB or hidden center changed at (%d,%d) by %s", x, y, filter.Kind)
				}
			}
		}
		result.Pix[0] ^= 255
		if result.Rect != source.Rect || !bytes.Equal(before, container.Pix) {
			t.Fatal("output origin changed, aliased source or mutated input")
		}
	}
}

func TestSpatialStackOrderAndRegionHalo(t *testing.T) {
	source := image.NewNRGBA(image.Rect(-20, 7, 54, 58))
	for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
		for x := source.Rect.Min.X; x < source.Rect.Max.X; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: uint8(90 + (x*y+10000)%23),
				G: uint8(80 + (x*x+y+10000)%27), B: uint8(120 + (x+y+10000)%19), A: uint8(64 + (x+y+1000)%191)})
		}
	}
	stack := Stack{Enabled: true, Filters: []Filter{surfaceFilter(), sharpenFilter(), gaussianStack(.5).Filters[0]}}
	options := spatialOptions()
	full, err := Apply(nil, source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	sequential := source
	for _, filter := range stack.Filters {
		sequential, err = Apply(nil, sequential, Stack{Enabled: true, Filters: []Filter{filter}}, options)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(full.Pix, sequential.Pix) {
		t.Fatal("stack order differs from individually applied operations")
	}
	halo, err := RequiredHalo(stack, options)
	if err != nil || halo != 13 {
		t.Fatalf("surface3 + lens3 + gaussian7 halo: %d, %v", halo, err)
	}
	interior := image.Rect(2, 25, 19, 39)
	window := interior.Inset(-halo)
	regionalInput := source.SubImage(window).(*image.NRGBA)
	regional, err := ApplyRegion(nil, regionalInput, stack, []bool{false, false, false}, options)
	if err != nil {
		t.Fatal(err)
	}
	for y := interior.Min.Y; y < interior.Max.Y; y++ {
		for x := interior.Min.X; x < interior.Max.X; x++ {
			if regional.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatalf("full/region seam at (%d,%d)", x, y)
			}
		}
	}
}

func TestSpatialPoliciesPreflightLimitsAndCancellation(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 32, 24))
	for offset := 0; offset < len(source.Pix); offset += 4 {
		copy(source.Pix[offset:offset+4], []byte{80, 82, 84, 255})
	}
	for _, filter := range []Filter{surfaceFilter(), sharpenFilter()} {
		stack := Stack{Enabled: true, Filters: []Filter{filter}}
		if output, err := Apply(nil, source, stack); output != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("missing explicit policy accepted %s: %v", filter.Kind, err)
		}
		options := spatialOptions()
		estimate, err := WorkingEstimate(source, stack, options)
		if err != nil || estimate.WorkingBytes < int64(len(source.Pix))*2 {
			t.Fatalf("output buffers absent from estimate: %+v, %v", estimate, err)
		}
		options.MaxWorkingBytes = estimate.WorkingBytes
		if _, err := Apply(nil, source, stack, options); err != nil {
			t.Fatal(err)
		}
		options.MaxWorkingBytes--
		if output, err := Apply(nil, source, stack, options); output != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("minus-one memory budget accepted %s: %v", filter.Kind, err)
		}
		options = spatialOptions()
		options.MaxSampleOperations = 1
		if output, err := Apply(nil, source, stack, options); output != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("sample-operation budget accepted %s: %v", filter.Kind, err)
		}
		ctx := newCountingCancelContext(30)
		if output, err := Apply(ctx, source, stack, spatialOptions()); output != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-operation cancellation returned output for %s: %v", filter.Kind, err)
		}
	}
	invalid := sharpenFilter()
	invalid.NoiseReduction = math.NaN()
	stack := Stack{Enabled: true, Filters: []Filter{surfaceFilter(), invalid}}
	if output, err := Apply(nil, source, stack, spatialOptions()); output != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid later operation returned a partial result: %v", err)
	}
	for _, change := range []func(*Filter){
		func(filter *Filter) { filter.UseLegacy = true },
		func(filter *Filter) { filter.BlurModel = "motion-blur" },
	} {
		unsupported := sharpenFilter()
		change(&unsupported)
		if _, err := SupportRadius(unsupported, spatialOptions()); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported native semantics ignored: %v", err)
		}
	}
}

type countingCancelContext struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func newCountingCancelContext(checks int) *countingCancelContext {
	base, cancel := context.WithCancel(context.Background())
	return &countingCancelContext{Context: base, cancel: cancel, checks: checks}
}

func (ctx *countingCancelContext) Err() error {
	ctx.checks--
	if ctx.checks <= 0 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}
