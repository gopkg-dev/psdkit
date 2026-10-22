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

func TestFauxBoldOutlineUnionIndependentCoverage(t *testing.T) {
	for _, test := range []struct {
		name    string
		outer   image.Rectangle
		hole    image.Rectangle
		x, y    float64
		radius  float64
		samples int
		bounds  image.Rectangle
	}{
		{"fractionalRectangle", image.Rect(0, 0, 4, 4), image.Rectangle{}, -.25, -.375,
			.5, 8, image.Rect(-3, -3, 7, 7)},
		{"negativeOrigin", image.Rect(-3, -2, 2, 3), image.Rectangle{}, -.375, .25,
			.3125, 4, image.Rect(-5, -4, 4, 5)},
		{"counterHole", image.Rect(-4, -4, 4, 4), image.Rect(-2, -2, 2, 2), 0, 0,
			.75, 8, image.Rect(-6, -6, 6, 6)},
	} {
		t.Run(test.name, func(t *testing.T) {
			glyph := fauxBoldRectangleGlyph(test.outer, test.hole)
			sourcePath := append([]Command(nil), glyph.Path...)
			run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{X: test.x, Y: test.y, Glyph: glyph}}}
			actual, err := RasterizeRun(nil, run, test.bounds,
				RasterOptions{FauxBoldRadius: test.radius, Samples: test.samples})
			if err != nil {
				t.Fatal(err)
			}
			denominator := test.samples * test.samples
			for y := test.bounds.Min.Y; y < test.bounds.Max.Y; y++ {
				for x := test.bounds.Min.X; x < test.bounds.Max.X; x++ {
					count := 0
					for subY := range test.samples {
						for subX := range test.samples {
							xp := float64(x) + (float64(subX)+.5)/float64(test.samples) - test.x
							yp := float64(y) + (float64(subY)+.5)/float64(test.samples) - test.y
							if fauxBoldRectangleUnionInside(xp, yp, test.radius, test.outer, test.hole) {
								count++
							}
						}
					}
					expected := byte((count*255 + denominator/2) / denominator)
					if actual.AlphaAt(x, y).A != expected {
						t.Fatalf("pixel (%d,%d): got %d; independent union has %d", x, y,
							actual.AlphaAt(x, y).A, expected)
					}
				}
			}
			if !reflect.DeepEqual(glyph.Path, sourcePath) || run.Glyphs[0].X != test.x || run.Glyphs[0].Y != test.y {
				t.Fatal("rasterization changed source outline or baseline")
			}
			if !test.hole.Empty() && actual.AlphaAt(0, 0).A != 0 {
				t.Fatal("the counter's surviving interior was filled")
			}
		})
	}
}

func TestFauxBoldZeroAndROI(t *testing.T) {
	glyph := fauxBoldRectangleGlyph(image.Rect(-4, -4, 4, 4), image.Rect(-2, -2, 2, 2))
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph, X: -.375, Y: .25}}}
	bounds := image.Rect(-7, -7, 7, 7)
	plain, err := RasterizeRun(nil, run, bounds)
	if err != nil {
		t.Fatal(err)
	}
	zero, err := RasterizeRun(nil, run, bounds, RasterOptions{FauxBoldRadius: 0})
	if err != nil || !bytes.Equal(plain.Pix, zero.Pix) {
		t.Fatalf("zero changed default glyph coverage: %v", err)
	}
	full, err := RasterizeRun(nil, run, bounds, RasterOptions{FauxBoldRadius: .7916666666666666})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(full.Pix, plain.Pix) {
		t.Fatal("positive radius did not render extra geometry")
	}
	for _, roi := range []image.Rectangle{image.Rect(-6, -5, -2, 1), image.Rect(-2, -3, 5, 6)} {
		partial, err := RasterizeRun(nil, run, roi, RasterOptions{FauxBoldRadius: .7916666666666666})
		if err != nil {
			t.Fatal(err)
		}
		for y := roi.Min.Y; y < roi.Max.Y; y++ {
			for x := roi.Min.X; x < roi.Max.X; x++ {
				if partial.AlphaAt(x, y) != full.AlphaAt(x, y) {
					t.Fatalf("ROI differs at (%d,%d)", x, y)
				}
			}
		}
	}
	// The coverage union is opaque once, rather than repeatedly blending nine partially covered images.
	if full.AlphaAt(-3, 0).A != 255 || full.AlphaAt(-1, 0).A != 0 {
		t.Fatalf("outline union changed the opaque body or counter: body=%d counter=%d",
			full.AlphaAt(-3, 0).A, full.AlphaAt(-1, 0).A)
	}
}

func TestFauxBoldBoundedWorkAndCancellation(t *testing.T) {
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{
		Glyph: fauxBoldRectangleGlyph(image.Rect(0, 0, 4, 4), image.Rectangle{}),
	}}}
	bounds := image.Rect(-2, -2, 6, 6)
	for _, test := range []struct {
		name    string
		options RasterOptions
		kind    error
	}{
		{"negative", RasterOptions{FauxBoldRadius: -.1}, ErrMalformed},
		{"nan", RasterOptions{FauxBoldRadius: math.NaN()}, ErrMalformed},
		{"infinite", RasterOptions{FauxBoldRadius: math.Inf(1)}, ErrMalformed},
		{"radius", RasterOptions{FauxBoldRadius: 65}, ErrLimit},
		{"ownedPaths", RasterOptions{FauxBoldRadius: .5, MaxWorkingBytes: 4096}, ErrLimit},
		{"segments", RasterOptions{FauxBoldRadius: .5, MaxSegments: 4}, ErrLimit},
		{"scanlines", RasterOptions{FauxBoldRadius: .5, MaxScanlineWork: 1000}, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			coverage, err := RasterizeRun(nil, run, bounds, test.options)
			if coverage != nil || !errors.Is(err, test.kind) {
				t.Fatalf("got coverage=%v error=%v; expected %v", coverage != nil, err, test.kind)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	coverage, err := RasterizeRun(ctx, run, bounds, RasterOptions{FauxBoldRadius: .5})
	if coverage != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: coverage=%v error=%v", coverage != nil, err)
	}
	paths := []psdvector.Subpath{{Operation: 1, Knots: []psdvector.Knot{{Anchor: psdvector.Point{X: 2, Y: 3}}}}}
	owned, err := fauxBoldPaths(context.Background(), paths, .5)
	if err != nil || len(owned) != 9 {
		t.Fatalf("independent outline expansion: %v", err)
	}
	owned[0].Knots[0].Anchor.X = 99
	if paths[0].Knots[0].Anchor.X != 2 || owned[1].Knots[0].Anchor.X != 2.5 {
		t.Fatal("synthetic outlines share caller or sibling knot storage")
	}
	owned, err = fauxBoldPaths(ctx, paths, .5)
	if owned != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("outline expansion cancellation: owned=%v error=%v", owned != nil, err)
	}
}

func fauxBoldRectangleGlyph(outer, hole image.Rectangle) *Glyph {
	commands := []Command{{Op: Move, X: float64(outer.Min.X), Y: -float64(outer.Min.Y)},
		{Op: Line, X: float64(outer.Max.X), Y: -float64(outer.Min.Y)},
		{Op: Line, X: float64(outer.Max.X), Y: -float64(outer.Max.Y)},
		{Op: Line, X: float64(outer.Min.X), Y: -float64(outer.Max.Y)}, {Op: Close}}
	if !hole.Empty() {
		commands = append(commands, Command{Op: Move, X: float64(hole.Min.X), Y: -float64(hole.Min.Y)},
			Command{Op: Line, X: float64(hole.Min.X), Y: -float64(hole.Max.Y)},
			Command{Op: Line, X: float64(hole.Max.X), Y: -float64(hole.Max.Y)},
			Command{Op: Line, X: float64(hole.Max.X), Y: -float64(hole.Min.Y)}, Command{Op: Close})
	}
	return &Glyph{Path: commands, AdvanceWidth: uint16(outer.Dx() + 2)}
}

func fauxBoldRectangleUnionInside(x, y, radius float64, outer, hole image.Rectangle) bool {
	// This oracle tests sample membership against translated rectangles without using vector paths.
	diagonal := radius / math.Sqrt2
	for _, offset := range [9][2]float64{{}, {radius, 0}, {-radius, 0}, {0, radius}, {0, -radius},
		{diagonal, diagonal}, {diagonal, -diagonal}, {-diagonal, diagonal}, {-diagonal, -diagonal}} {
		xp, yp := x-offset[0], y-offset[1]
		inside := xp >= float64(outer.Min.X) && xp < float64(outer.Max.X) &&
			yp >= float64(outer.Min.Y) && yp < float64(outer.Max.Y)
		counter := !hole.Empty() && xp >= float64(hole.Min.X) && xp < float64(hole.Max.X) &&
			yp >= float64(hole.Min.Y) && yp < float64(hole.Max.Y)
		if inside && !counter {
			return true
		}
	}
	return false
}
