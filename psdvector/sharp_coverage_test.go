package psdvector

import (
	"bytes"
	"context"
	"errors"
	"image"
	"testing"
)

func TestCoverageLevels16RawHorizontalIntegral(t *testing.T) {
	for level := range 16 {
		width := (float64(level) + .5) / 16
		mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(0, 0, width, 1, 1)}}
		actual, err := Rasterize(nil, mask, image.Rect(0, 0, 1, 1),
			Options{AreaCoverage: true, CoverageLevels16: true})
		if err != nil || actual.AlphaAt(0, 0).A != byte(level*17) {
			t.Fatalf("level %d: alpha %v error %v", level, actual, err)
		}
	}
	// This fraction rounds to byte 64, but remains below the raw quarter-coverage threshold.
	width := (4 - 1e-4) / 16
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(0, 0, width, 1, 1)}}
	legacy, err := Rasterize(nil, mask, image.Rect(0, 0, 1, 1), Options{AreaCoverage: true})
	if err != nil || legacy.AlphaAt(0, 0).A != 64 {
		t.Fatalf("byte-rounding control: alpha %v error %v", legacy, err)
	}
	actual, err := Rasterize(nil, mask, image.Rect(0, 0, 1, 1),
		Options{AreaCoverage: true, CoverageLevels16: true})
	if err != nil || actual.AlphaAt(0, 0).A != 51 {
		t.Fatalf("16-level transfer rounded coverage to a byte first: alpha %v error %v", actual, err)
	}
	opaque := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(0, 0, 1, 1, 1)}}
	full, err := Rasterize(nil, opaque, image.Rect(0, 0, 1, 1), Options{CoverageLevels16: true})
	if err != nil || full.AlphaAt(0, 0).A != 255 {
		t.Fatalf("full coverage was not saturated: alpha %v error %v", full, err)
	}
}

func TestCoverageLevels16UnionBeforeQuantization(t *testing.T) {
	bounds := image.Rect(0, 0, 1, 1)
	mask := &Mask{Width: 1, Height: 1, FillRule: NonZero, Subpaths: []Subpath{
		rectangleSubpath(0, 0, .25, 1, 1), rectangleSubpath(.2, 0, .5, 1, 1),
	}}
	for _, configuration := range []Options{
		{CoverageLevels16: true},
		{AreaCoverage: true, CoverageLevels16: true},
	} {
		actual, err := Rasterize(nil, mask, bounds, configuration)
		if err != nil || actual.AlphaAt(0, 0).A != 136 {
			t.Fatalf("two overlapping intervals must cover exactly half a pixel: alpha %v error %v", actual, err)
		}
		duplicates := *mask
		duplicates.Subpaths = append([]Subpath(nil), mask.Subpaths...)
		for range 10 {
			duplicates.Subpaths = append(duplicates.Subpaths, mask.Subpaths...)
		}
		repeated, err := Rasterize(nil, &duplicates, bounds, configuration)
		if err != nil || !bytes.Equal(repeated.Pix, actual.Pix) {
			t.Fatalf("overlapping union counted translucent outlines more than once: %v", err)
		}
	}
}

func TestCoverageLevels16BooleanAbsoluteROIAndDefaults(t *testing.T) {
	bounds := image.Rect(-4, -3, 5, 4)
	for _, operation := range []int16{0, 1, 2, 3} {
		for _, inverted := range []bool{false, true} {
			mask := &Mask{Width: 1, Height: 1, FillRule: NonZero, Invert: inverted, Subpaths: []Subpath{
				rectangleSubpath(-2.375, -1.3, 2.125, 2.75, 1),
				rectangleSubpath(-.875, -2.2, 3.375, .7, operation),
			}}
			for _, samples := range []int{0, 4, 8, 16, 64} {
				configuration := Options{CoverageLevels16: true}
				if samples != 0 {
					configuration.AreaCoverage, configuration.AreaSamples = true, samples
				}
				full, err := Rasterize(nil, mask, bounds, configuration)
				if err != nil {
					t.Fatal(err)
				}
				for _, value := range full.Pix {
					if value%17 != 0 {
						t.Fatalf("coverage emitted an alpha outside the 16-level transfer: %d", value)
					}
				}
				for _, roi := range []image.Rectangle{image.Rect(-3, -2, 0, 1), image.Rect(-1, 0, 4, 3)} {
					crop, err := Rasterize(nil, mask, roi, configuration)
					if err != nil {
						t.Fatal(err)
					}
					for y := roi.Min.Y; y < roi.Max.Y; y++ {
						for x := roi.Min.X; x < roi.Max.X; x++ {
							if crop.AlphaAt(x, y) != full.AlphaAt(x, y) {
								t.Fatalf("operation=%d invert=%v samples=%d ROI phase changed at (%d,%d)",
									operation, inverted, samples, x, y)
							}
						}
					}
				}
			}
			for _, explicit := range []Options{{Samples: 8}, {AreaCoverage: true, AreaSamples: 64}} {
				implicit := Options{AreaCoverage: explicit.AreaCoverage}
				legacy, err := Rasterize(nil, mask, bounds, implicit)
				if err != nil {
					t.Fatal(err)
				}
				actual, err := Rasterize(nil, mask, bounds, explicit)
				if err != nil || !bytes.Equal(legacy.Pix, actual.Pix) {
					t.Fatalf("explicit defaults changed previous coverage: %v", err)
				}
			}
		}
	}
}

func TestCoverageLevels16AreaSamplingWorkAndValidation(t *testing.T) {
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(-.3, -.25, 2.7, 2.25, 1)}}
	bounds := image.Rect(-1, -1, 4, 4)
	for _, samples := range []int{4, 8, 16, 64} {
		work := int64(4 * bounds.Dy() * samples)
		configuration := Options{AreaCoverage: true, AreaSamples: samples,
			CoverageLevels16: true, MaxScanlineWork: work}
		actual, err := Rasterize(nil, mask, bounds, configuration)
		if err != nil || actual == nil {
			t.Fatalf("sample rate %d rejected its exact edge-work budget: %v", samples, err)
		}
		configuration.MaxScanlineWork--
		actual, err = Rasterize(nil, mask, bounds, configuration)
		if actual != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("sample rate %d escaped work budget: coverage=%v error=%v", samples, actual != nil, err)
		}
	}
	for _, configuration := range []Options{
		{CoverageLevels16: true, NoAntialias: true}, {AreaSamples: 4},
		{AreaCoverage: true, AreaSamples: -1},
	} {
		actual, err := Rasterize(nil, mask, bounds, configuration)
		if actual != nil || !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid coverage policy escaped: coverage=%v error=%v", actual != nil, err)
		}
	}
	actual, err := Rasterize(nil, mask, bounds, Options{AreaCoverage: true, AreaSamples: 32})
	if actual != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported sample rate escaped: coverage=%v error=%v", actual != nil, err)
	}
	actual, err = Rasterize(nil, mask, bounds, Options{CoverageLevels16: true, MaxWorkingBytes: 64})
	if actual != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("new transfer bypassed the existing memory limit: coverage=%v error=%v", actual != nil, err)
	}
}

type coverage16CancelContext struct {
	context.Context
	cancel context.CancelFunc
	calls  int
	stop   int
}

func (ctx *coverage16CancelContext) Err() error {
	ctx.calls++
	if ctx.calls == ctx.stop {
		ctx.cancel()
		return nil
	}
	return ctx.Context.Err()
}

func TestCoverageLevels16FinalSampleCancellation(t *testing.T) {
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(-.3, -.25, 2.7, 2.25, 1)}}
	bounds := image.Rect(-1, -1, 4, 4)
	for _, samples := range []int{0, 4, 8, 16, 64} {
		configuration := Options{CoverageLevels16: true}
		vertical := 8
		if samples != 0 {
			configuration.AreaCoverage, configuration.AreaSamples, vertical = true, samples, samples
		}
		counter := &coverage16CancelContext{Context: context.Background(), stop: -1}
		if _, err := prepareRaster(counter, mask, bounds, []Options{configuration}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		stopping := &coverage16CancelContext{Context: ctx, cancel: cancel,
			stop: 1 + counter.calls + bounds.Dy()*vertical}
		actual, err := Rasterize(stopping, mask, bounds, configuration)
		cancel()
		if actual != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("sample rate %d returned coverage after final-sample cancellation: coverage=%v error=%v",
				samples, actual != nil, err)
		}
	}
}
