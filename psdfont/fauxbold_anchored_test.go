package psdfont

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdvector"
)

func TestAnchoredFauxBoldIndependentUnionAndCounter(t *testing.T) {
	for _, origin := range [][2]float64{{-.375, .25}, {.125, -.5}} {
		glyph := fauxBoldRectangleGlyph(image.Rect(-3, -2, 3, 2), image.Rect(-1, -1, 1, 1))
		commands := append([]Command(nil), glyph.Path...)
		run := &Run{Scale: 1, AdvanceWidth: 8, Glyphs: []PositionedGlyph{{
			Glyph: glyph, X: origin[0], Y: origin[1], Advance: 8,
		}}}
		radius := .625
		bounds := image.Rect(-5, -4, 6, 5)
		coverage, err := RasterizeRun(nil, run, bounds,
			RasterOptions{FauxBoldRadius: radius, FauxBoldAnchored: true})
		if err != nil {
			t.Fatal(err)
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				count := 0
				for sampleY := range 8 {
					for sampleX := range 8 {
						xp := float64(x) + (float64(sampleX)+.5)/8 - origin[0]
						yp := float64(y) + (float64(sampleY)+.5)/8 - origin[1]
						if anchoredRectangleInside(xp, yp, radius) {
							count++
						}
					}
				}
				expected := byte((count*255 + 32) / 64)
				if coverage.AlphaAt(x, y).A != expected {
					t.Fatalf("origin=%v (%d,%d): got %d; independent anchored union %d", origin, x, y,
						coverage.AlphaAt(x, y).A, expected)
				}
			}
		}
		if !reflect.DeepEqual(glyph.Path, commands) || run.AdvanceWidth != 8 ||
			run.Glyphs[0].X != origin[0] || run.Glyphs[0].Advance != 8 {
			t.Fatal("anchored raster modified source font commands, baseline or advances")
		}
	}
}

func anchoredRectangleInside(x, y, radius float64) bool {
	for component := -1; component < 8; component++ {
		ox, oy := 0.0, 0.0
		if component != -1 {
			angle := float64(component) * math.Pi / 4
			ox, oy = radius*(1+math.Cos(angle)), radius*(1+math.Sin(angle))
		}
		qx, qy := x-ox, y-oy
		body := qx >= -3 && qx < 3 && qy >= -2 && qy < 2
		hole := qx >= -1 && qx < 1 && qy >= -1 && qy < 1
		if body && !hole {
			return true
		}
	}
	return false
}

func TestAnchoredFauxBoldZeroROIAndOwnedOutlines(t *testing.T) {
	glyph := fauxBoldRectangleGlyph(image.Rect(-3, -2, 3, 2), image.Rect(-1, -1, 1, 1))
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph, X: -.375, Y: .25}}}
	bounds := image.Rect(-5, -4, 6, 5)
	plain, err := RasterizeRun(nil, run, bounds)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := RasterizeRun(nil, run, bounds, RasterOptions{FauxBoldAnchored: true})
	if err != nil || !bytes.Equal(plain.Pix, zero.Pix) {
		t.Fatalf("zero anchored radius changed the unmodified outline: %v", err)
	}
	options := RasterOptions{FauxBoldRadius: .625, FauxBoldAnchored: true}
	full, err := RasterizeRun(nil, run, bounds, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, roi := range []image.Rectangle{image.Rect(-4, -3, 0, 1), image.Rect(-2, -1, 5, 4)} {
		crop, err := RasterizeRun(nil, run, roi, options)
		if err != nil {
			t.Fatal(err)
		}
		for y := roi.Min.Y; y < roi.Max.Y; y++ {
			for x := roi.Min.X; x < roi.Max.X; x++ {
				if crop.AlphaAt(x, y) != full.AlphaAt(x, y) {
					t.Fatalf("anchored ROI changed at (%d,%d)", x, y)
				}
			}
		}
	}
	paths := []psdvector.Subpath{{Operation: 1, Knots: []psdvector.Knot{{Anchor: psdvector.Point{X: 2, Y: 3}}}}}
	owned, err := fauxBoldPathsWithAnchor(context.Background(), paths, .5, true)
	if err != nil || len(owned) != 9 {
		t.Fatalf("anchored outline copies: %v", err)
	}
	owned[0].Knots[0].Anchor.X = 99
	if paths[0].Knots[0].Anchor.X != 2 || owned[1].Knots[0].Anchor.X != 3 {
		t.Fatal("anchored outlines share mutable caller or sibling knot storage")
	}
}

func TestAnchoredFauxBoldLimitsAndCancellation(t *testing.T) {
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{
		Glyph: fauxBoldRectangleGlyph(image.Rect(-3, -2, 3, 2), image.Rect(-1, -1, 1, 1)),
	}}}
	bounds := image.Rect(-5, -4, 6, 5)
	for _, test := range []struct {
		options RasterOptions
		kind    error
	}{
		{RasterOptions{FauxBoldRadius: -.1, FauxBoldAnchored: true}, ErrMalformed},
		{RasterOptions{FauxBoldRadius: math.NaN(), FauxBoldAnchored: true}, ErrMalformed},
		{RasterOptions{FauxBoldRadius: math.Inf(1), FauxBoldAnchored: true}, ErrMalformed},
		{RasterOptions{FauxBoldRadius: 65, FauxBoldAnchored: true}, ErrLimit},
		{RasterOptions{FauxBoldRadius: .5, FauxBoldAnchored: true, MaxWorkingBytes: 4096}, ErrLimit},
		{RasterOptions{FauxBoldRadius: .5, FauxBoldAnchored: true, MaxSegments: 4}, ErrLimit},
		{RasterOptions{FauxBoldRadius: .5, FauxBoldAnchored: true, MaxScanlineWork: 1}, ErrLimit},
	} {
		coverage, err := RasterizeRun(nil, run, bounds, test.options)
		if coverage != nil || !errors.Is(err, test.kind) {
			t.Fatalf("anchored quota or geometry escaped: coverage=%v error=%v expected=%v",
				coverage != nil, err, test.kind)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	coverage, err := RasterizeRun(ctx, run, bounds, RasterOptions{FauxBoldRadius: .5, FauxBoldAnchored: true})
	if coverage != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("anchored coverage cancellation: coverage=%v error=%v", coverage != nil, err)
	}
	paths, err := glyphPaths(ctx, run.Glyphs[0].Glyph.Path, 0, 0, 1)
	if paths != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("anchored input path cancellation: paths=%v error=%v", paths != nil, err)
	}
}
