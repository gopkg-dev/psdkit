package psdvector

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func TestAreaCoverageFractionalRectangleHorizontalIntegral(t *testing.T) {
	bounds := image.Rect(-3, -2, 5, 4)
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(-1.3, -.25, 2.7, 2.125, 1)}}
	actual, err := Rasterize(nil, mask, bounds, Options{AreaCoverage: true})
	if err != nil {
		t.Fatal(err)
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			xAmount := math.Max(0, math.Min(float64(x+1), 2.7)-math.Max(float64(x), -1.3))
			yAmount := math.Max(0, math.Min(float64(y+1), 2.125)-math.Max(float64(y), -.25))
			expected := byte(math.Round(xAmount * yAmount * 255))
			if actual.AlphaAt(x, y).A != expected {
				t.Fatalf("(%d,%d): got %d; rectangle area %d", x, y, actual.AlphaAt(x, y).A, expected)
			}
		}
	}
	legacy, err := Rasterize(nil, mask, bounds)
	if err != nil || bytes.Equal(legacy.Pix, actual.Pix) {
		t.Fatalf("horizontal fractional edge did not distinguish area coverage: %v", err)
	}
}

func TestAreaCoverageTriangleAnalyticIntegral(t *testing.T) {
	// The diagonal goes through opposing pixel corners, whose exact half coverage is 128.
	knots := []Knot{{Anchor: Point{X: -1, Y: -1}}, {Anchor: Point{X: 3, Y: -1}}, {Anchor: Point{X: 3, Y: 3}}}
	for index := range knots {
		knots[index].Preceding, knots[index].Leaving = knots[index].Anchor, knots[index].Anchor
	}
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{{Closed: true, Operation: 1, Knots: knots}}}
	actual, err := Rasterize(nil, mask, image.Rect(-2, -2, 4, 4), Options{AreaCoverage: true})
	if err != nil {
		t.Fatal(err)
	}
	for coordinate := -1; coordinate < 3; coordinate++ {
		if actual.AlphaAt(coordinate, coordinate).A != 128 {
			t.Fatalf("diagonal cell (%d,%d): got %d", coordinate, coordinate,
				actual.AlphaAt(coordinate, coordinate).A)
		}
	}
	if actual.AlphaAt(2, 0).A != 255 || actual.AlphaAt(0, 2).A != 0 {
		t.Fatal("triangle filled side changed")
	}
}

func TestAreaCoverageBooleanHolesOverlapsAndROI(t *testing.T) {
	for _, rule := range []FillRule{NonZero, EvenOdd} {
		for _, invert := range []bool{false, true} {
			outer := rectangleSubpath(-2.3, -1.5, 2.7, 3.25, 1)
			hole := rectangleSubpath(-1.1, -.5, 1.4, 1.5, -1)
			if rule == NonZero {
				slicesReverseKnots(hole.Knots)
			}
			other := rectangleSubpath(.9, -2.25, 3.7, .5, 1)
			mask := &Mask{Width: 1, Height: 1, FillRule: rule, Invert: invert,
				Subpaths: []Subpath{outer, hole, other}}
			bounds := image.Rect(-4, -4, 5, 5)
			full, err := Rasterize(nil, mask, bounds, Options{AreaCoverage: true})
			if err != nil {
				t.Fatal(err)
			}
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					amount := 0.0
					// Split horizontal intervals at independent known rectangle edges.
					cuts := []float64{float64(x), -2.3, -1.1, .9, 1.4, 2.7, 3.7, float64(x + 1)}
					for sample := range 64 {
						yp := float64(y) + (float64(sample)+.5)/64
						for interval := 0; interval+1 < len(cuts); interval++ {
							left := math.Max(float64(x), cuts[interval])
							right := math.Min(float64(x+1), cuts[interval+1])
							if right <= left {
								continue
							}
							xp := (left + right) / 2
							body := xp >= -2.3 && xp < 2.7 && yp >= -1.5 && yp < 3.25
							counter := xp >= -1.1 && xp < 1.4 && yp >= -.5 && yp < 1.5
							second := xp >= .9 && xp < 3.7 && yp >= -2.25 && yp < .5
							if ((body && !counter) || second) != invert {
								amount += right - left
							}
						}
					}
					expected := byte(math.Round(amount * 255 / 64))
					if full.AlphaAt(x, y).A != expected {
						t.Fatalf("rule %d invert %v (%d,%d): got %d; oracle %d", rule, invert, x, y,
							full.AlphaAt(x, y).A, expected)
					}
				}
			}
			roi := image.Rect(-2, -1, 3, 3)
			crop, err := Rasterize(nil, mask, roi, Options{AreaCoverage: true})
			if err != nil {
				t.Fatal(err)
			}
			for y := roi.Min.Y; y < roi.Max.Y; y++ {
				for x := roi.Min.X; x < roi.Max.X; x++ {
					if crop.AlphaAt(x, y) != full.AlphaAt(x, y) {
						t.Fatalf("ROI phase changed at (%d,%d)", x, y)
					}
				}
			}
		}
	}
}

func TestAreaCoverageWorkBudgetCancellationAndDefaults(t *testing.T) {
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(-.3, -.25, 2.7, 2.25, 1)}}
	bounds := image.Rect(-1, -1, 4, 4)
	// Four edges times five rows times 64 midpoint rows uses 1280 edge steps.
	actual, err := Rasterize(nil, mask, bounds, Options{AreaCoverage: true, MaxScanlineWork: 1280})
	if err != nil || actual == nil {
		t.Fatalf("exact edge budget failed: %v", err)
	}
	for _, options := range []Options{
		{AreaCoverage: true, MaxScanlineWork: 1279},
		{AreaCoverage: true, MaxWorkingBytes: 64},
		{AreaCoverage: true, MaxSegments: 3},
	} {
		coverage, err := Rasterize(nil, mask, bounds, options)
		if coverage != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("quota escaped: coverage %v error %v", coverage != nil, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	coverage, err := Rasterize(ctx, mask, bounds, Options{AreaCoverage: true})
	if coverage != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: coverage %v error %v", coverage != nil, err)
	}
	legacy, err := Rasterize(nil, mask, bounds)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := Rasterize(nil, mask, bounds, Options{AreaCoverage: false})
	if err != nil || !bytes.Equal(zero.Pix, legacy.Pix) {
		t.Fatalf("explicit zero changed the default: %v", err)
	}
}

func TestNoAntialiasPixelCentersROIAndQuota(t *testing.T) {
	mask := &Mask{Width: 1, Height: 1, Subpaths: []Subpath{rectangleSubpath(-.3, -.25, 2.7, 2.25, 1)}}
	bounds := image.Rect(-1, -1, 4, 4)
	coverage, err := Rasterize(nil, mask, bounds, Options{NoAntialias: true, MaxScanlineWork: 20})
	if err != nil {
		t.Fatal(err)
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			expected := byte(0)
			if xp, yp := float64(x)+.5, float64(y)+.5; xp >= -.3 && xp < 2.7 && yp >= -.25 && yp < 2.25 {
				expected = 255
			}
			if coverage.AlphaAt(x, y).A != expected {
				t.Fatalf("(%d,%d): got %d; center oracle %d", x, y, coverage.AlphaAt(x, y).A, expected)
			}
		}
	}
	roi := image.Rect(0, 0, 3, 3)
	crop, err := Rasterize(nil, mask, roi, Options{NoAntialias: true})
	if err != nil {
		t.Fatal(err)
	}
	for y := roi.Min.Y; y < roi.Max.Y; y++ {
		for x := roi.Min.X; x < roi.Max.X; x++ {
			if crop.AlphaAt(x, y) != coverage.AlphaAt(x, y) {
				t.Fatalf("center ROI changed at (%d,%d)", x, y)
			}
		}
	}
	result, err := Rasterize(nil, mask, bounds, Options{NoAntialias: true, MaxScanlineWork: 19})
	if result != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("pixel center edge budget escaped: output %v error %v", result != nil, err)
	}
	result, err = Rasterize(nil, mask, bounds, Options{NoAntialias: true, AreaCoverage: true})
	if result != nil || !errors.Is(err, ErrMalformed) {
		t.Fatalf("incompatible coverage accepted: output %v error %v", result != nil, err)
	}
}
