package psdvector

import (
	"context"
	"errors"
	"image"
	"math"
	"slices"
	"testing"
	"unsafe"
)

type typographyReviewCancelContext struct {
	context.Context
	cancel context.CancelFunc
	calls  int
	stop   int
}

func TestTypographyReviewCoverageBooleanOperationsAndAbsolutePhase(t *testing.T) {
	bounds := image.Rect(-4, -3, 5, 4)
	first := [4]float64{-2.375, -1.25, 2.125, 2.75}
	second := [4]float64{-.875, -2.125, 3.375, .875}
	for _, operation := range []int16{0, 1, 2, 3} {
		for _, inverted := range []bool{false, true} {
			for _, noAA := range []bool{false, true} {
				mask := &Mask{Width: 1, Height: 1, FillRule: NonZero, Invert: inverted,
					Subpaths: []Subpath{
						rectangleSubpath(first[0], first[1], first[2], first[3], 1),
						rectangleSubpath(second[0], second[1], second[2], second[3], operation),
					}}
				configuration := Options{AreaCoverage: !noAA, NoAntialias: noAA}
				coverage, err := Rasterize(nil, mask, bounds, configuration)
				if err != nil {
					t.Fatal(err)
				}
				for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
					for x := bounds.Min.X; x < bounds.Max.X; x++ {
						expected := typographyReviewBooleanAlpha(x, y, first, second, operation, inverted, noAA)
						if coverage.AlphaAt(x, y).A != expected {
							t.Fatalf("operation=%d inverted=%v noAA=%v (%d,%d): got %d expected %d",
								operation, inverted, noAA, x, y, coverage.AlphaAt(x, y).A, expected)
						}
					}
				}
				for _, roi := range []image.Rectangle{image.Rect(-3, -2, 0, 1), image.Rect(-1, 0, 4, 3)} {
					crop, err := Rasterize(nil, mask, roi, configuration)
					if err != nil {
						t.Fatal(err)
					}
					for y := roi.Min.Y; y < roi.Max.Y; y++ {
						for x := roi.Min.X; x < roi.Max.X; x++ {
							if crop.AlphaAt(x, y) != coverage.AlphaAt(x, y) {
								t.Fatalf("ROI phase changed at (%d,%d)", x, y)
							}
						}
					}
				}
			}
		}
	}
}

func typographyReviewBooleanAlpha(
	x, y int, first, second [4]float64, operation int16, inverted, noAA bool,
) byte {
	inside := func(xp, yp float64) bool {
		inFirst := xp >= first[0] && xp < first[2] && yp >= first[1] && yp < first[3]
		inSecond := xp >= second[0] && xp < second[2] && yp >= second[1] && yp < second[3]
		value := inFirst
		switch operation {
		case 0:
			value = inFirst != inSecond
		case 1:
			value = inFirst || inSecond
		case 2:
			value = inFirst && !inSecond
		case 3:
			value = inFirst && inSecond
		}
		return value != inverted
	}
	if noAA {
		if inside(float64(x)+.5, float64(y)+.5) {
			return 255
		}
		return 0
	}
	xCuts := []float64{float64(x), float64(x + 1), first[0], first[2], second[0], second[2]}
	yCuts := []float64{float64(y), float64(y + 1), first[1], first[3], second[1], second[3]}
	slices.Sort(xCuts)
	slices.Sort(yCuts)
	area := 0.0
	for xi := 0; xi+1 < len(xCuts); xi++ {
		left, right := math.Max(float64(x), xCuts[xi]), math.Min(float64(x+1), xCuts[xi+1])
		if right <= left {
			continue
		}
		for yi := 0; yi+1 < len(yCuts); yi++ {
			top, bottom := math.Max(float64(y), yCuts[yi]), math.Min(float64(y+1), yCuts[yi+1])
			if bottom > top && inside((left+right)/2, (top+bottom)/2) {
				area += (right - left) * (bottom - top)
			}
		}
	}
	return byte(math.Round(area * 255))
}

func (ctx *typographyReviewCancelContext) Err() error {
	ctx.calls++
	if ctx.calls == ctx.stop {
		ctx.cancel()
		return nil
	}
	return ctx.Context.Err()
}

func TestTypographyReviewFinalSampleCancellation(t *testing.T) {
	mask := &Mask{Width: 1, Height: 1,
		Subpaths: []Subpath{rectangleSubpath(-.3, -.25, 2.7, 2.25, 1)}}
	bounds := image.Rect(-1, -1, 4, 4)
	for _, configuration := range []Options{{AreaCoverage: true}, {NoAntialias: true}} {
		baseline, baselineCancel := context.WithCancel(context.Background())
		counter := &typographyReviewCancelContext{Context: baseline, cancel: baselineCancel, stop: -1}
		_, err := prepareRaster(counter, mask, bounds, []Options{configuration})
		baselineCancel()
		if err != nil {
			t.Fatalf("count final-sample checks: %v", err)
		}
		verticalSamples := 1
		if configuration.AreaCoverage {
			verticalSamples = int(areaVerticalSamples)
		}
		// Rasterize first checks the context, then prepares geometry and checks each sample.
		stop := 1 + counter.calls + bounds.Dy()*verticalSamples
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		during := &typographyReviewCancelContext{Context: ctx, cancel: cancel, stop: stop}
		coverage, err := Rasterize(during, mask, bounds, configuration)
		if coverage != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("area=%v canceled before final sample but returned coverage=%v error=%v checks=%d",
				configuration.AreaCoverage, coverage != nil, err, during.calls)
		}
	}
}

func TestTypographyReviewAreaLiveBufferBudget(t *testing.T) {
	path := rectangleSubpath(0, 0, 1, 1, 1)
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{path}}
	configuration := Options{AreaCoverage: true, MaxDepth: 1, MaxWorkingBytes: 500}
	plan, err := prepareRaster(context.Background(), mask, image.Rect(0, 0, 1, 1), []Options{configuration})
	if errors.Is(err, ErrLimit) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	// These buffers coexist when coverage is computed; input paths and allocator headers are excluded.
	liveBytes := int64(1+8) + int64(cap(plan.lines))*int64(unsafe.Sizeof(vectorLine{})) +
		int64(len(plan.lines))*int64(unsafe.Sizeof(vectorCrossing{})) +
		int64(cap(plan.components))*int64(unsafe.Sizeof(vectorComponent{})) +
		int64(len(plan.components))*int64(unsafe.Sizeof(int(0)))
	if liveBytes > configuration.MaxWorkingBytes {
		t.Fatalf("area live buffers need %d bytes, but budget %d accepted %d edges at capacity %d",
			liveBytes, configuration.MaxWorkingBytes, len(plan.lines), cap(plan.lines))
	}
}
