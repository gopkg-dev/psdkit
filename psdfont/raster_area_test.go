package psdfont

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"reflect"
	"testing"
)

func TestRunAreaCoverageIndependentRectangleAndCounter(t *testing.T) {
	glyph := fauxBoldRectangleGlyph(image.Rect(-3, -2, 3, 2), image.Rect(-1, -1, 1, 1))
	original := append([]Command(nil), glyph.Path...)
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .3, Y: -.25}}}
	bounds := image.Rect(-5, -4, 5, 4)
	coverage, err := RasterizeRun(nil, run, bounds, RasterOptions{AreaCoverage: true})
	if err != nil {
		t.Fatal(err)
	}
	intersectionArea := func(x, y int, rectangle image.Rectangle) float64 {
		horizontal := math.Max(0, math.Min(float64(x+1), float64(rectangle.Max.X)+.3)-
			math.Max(float64(x), float64(rectangle.Min.X)+.3))
		vertical := math.Max(0, math.Min(float64(y+1), float64(rectangle.Max.Y)-.25)-
			math.Max(float64(y), float64(rectangle.Min.Y)-.25))
		return horizontal * vertical
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			amount := intersectionArea(x, y, image.Rect(-3, -2, 3, 2)) -
				intersectionArea(x, y, image.Rect(-1, -1, 1, 1))
			if coverage.AlphaAt(x, y).A != byte(math.Round(amount*255+1e-9)) {
				t.Fatalf("(%d,%d): got %d; area %f", x, y, coverage.AlphaAt(x, y).A, amount)
			}
		}
	}
	if !reflect.DeepEqual(original, glyph.Path) {
		t.Fatal("area coverage mutated font commands")
	}
	coverage.Pix[0] = 123
	again, err := RasterizeRun(nil, run, bounds, RasterOptions{AreaCoverage: true})
	if err != nil || again.Pix[0] != 0 {
		t.Fatalf("area coverage shares caller output: %v", err)
	}
}

func TestRunAreaCoverageFauxBoldROIAndOverlapUnion(t *testing.T) {
	glyph := fauxBoldRectangleGlyph(image.Rect(-3, -2, 3, 2), image.Rect(-1, -1, 1, 1))
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .3, Y: -.25}}}
	bounds := image.Rect(-5, -4, 5, 4)
	options := RasterOptions{AreaCoverage: true, FauxBoldRadius: .375}
	full, err := RasterizeRun(nil, run, bounds, options)
	if err != nil {
		t.Fatal(err)
	}
	roi := image.Rect(-3, -2, 3, 2)
	crop, err := RasterizeRun(nil, run, roi, options)
	if err != nil {
		t.Fatal(err)
	}
	for y := roi.Min.Y; y < roi.Max.Y; y++ {
		for x := roi.Min.X; x < roi.Max.X; x++ {
			if crop.AlphaAt(x, y) != full.AlphaAt(x, y) {
				t.Fatalf("area ROI changed at (%d,%d)", x, y)
			}
		}
	}
	// Duplicating the same positioned glyph must retain the original coverage instead of source-over accumulation.
	run.Glyphs = append(run.Glyphs, run.Glyphs[0])
	doubled, err := RasterizeRun(nil, run, bounds, options)
	if err != nil || !bytes.Equal(doubled.Pix, full.Pix) {
		t.Fatalf("overlapping outlines were painted more than once: %v", err)
	}
}

func TestRunNoAntialiasIndependentPixelCenters(t *testing.T) {
	glyph := fauxBoldRectangleGlyph(image.Rect(0, 0, 4, 4), image.Rect(1, 1, 3, 3))
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .6, Y: -.3}}}
	bounds := image.Rect(-1, -1, 6, 5)
	coverage, err := RasterizeRun(nil, run, bounds, RasterOptions{NoAntialias: true})
	if err != nil {
		t.Fatal(err)
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			xp, yp := float64(x)+.5-.6, float64(y)+.5+.3
			body := xp >= 0 && xp < 4 && yp >= 0 && yp < 4
			counter := xp >= 1 && xp < 3 && yp >= 1 && yp < 3
			expected := byte(0)
			if body && !counter {
				expected = 255
			}
			if coverage.AlphaAt(x, y).A != expected {
				t.Fatalf("pixel-center membership at (%d,%d): got %d; expected %d",
					x, y, coverage.AlphaAt(x, y).A, expected)
			}
		}
	}
}

func TestRunCoverageModesQuotaCancelAndDefault(t *testing.T) {
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{
		Glyph: fauxBoldRectangleGlyph(image.Rect(0, 0, 4, 4), image.Rectangle{}), X: .3,
	}}}
	bounds := image.Rect(-1, -1, 6, 6)
	for _, options := range []RasterOptions{
		{AreaCoverage: true, MaxWorkingBytes: 1024},
		{AreaCoverage: true, MaxScanlineWork: 1791},
		{NoAntialias: true, MaxScanlineWork: 27},
	} {
		coverage, err := RasterizeRun(nil, run, bounds, options)
		if coverage != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("coverage mode quota escaped: coverage=%v error=%v", coverage != nil, err)
		}
	}
	for _, options := range []RasterOptions{{AreaCoverage: true}, {NoAntialias: true}} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		coverage, err := RasterizeRun(ctx, run, bounds, options)
		if coverage != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled coverage mode: coverage=%v error=%v", coverage != nil, err)
		}
	}
	coverage, err := RasterizeRun(nil, run, bounds, RasterOptions{AreaCoverage: true, NoAntialias: true})
	if coverage != nil || !errors.Is(err, ErrMalformed) {
		t.Fatalf("ambiguous coverage mode: coverage=%v error=%v", coverage != nil, err)
	}
	legacy, err := RasterizeRun(nil, run, bounds)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := RasterizeRun(nil, run, bounds, RasterOptions{})
	if err != nil || !bytes.Equal(legacy.Pix, zero.Pix) {
		t.Fatalf("zero coverage selection changed default: %v", err)
	}
}
