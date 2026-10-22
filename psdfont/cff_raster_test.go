package psdfont

import (
	"bytes"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdvector"
)

func TestCFFCubicBothControlsReachRaster(t *testing.T) {
	glyph := &Glyph{Path: []Command{{Op: Move},
		{Op: Cubic, X: 100, ControlX: 0, ControlY: 100, Control2X: 100, Control2Y: 100},
		{Op: Line}, {Op: Close}}}
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Y: 128, Glyph: glyph}}}
	bounds := image.Rect(-4, 20, 104, 132)
	actual, err := RasterizeRun(nil, run, bounds, RasterOptions{Samples: 4})
	if err != nil {
		t.Fatal(err)
	}
	mask := &psdvector.Mask{Width: 1, Height: 1, HasFillRule: true, FillRule: psdvector.NonZero,
		Subpaths: []psdvector.Subpath{{Closed: true, Operation: 1, Knots: []psdvector.Knot{
			{Anchor: psdvector.Point{X: 0, Y: 128}, Preceding: psdvector.Point{X: 0, Y: 128},
				Leaving: psdvector.Point{X: 0, Y: 28}},
			{Anchor: psdvector.Point{X: 100, Y: 128}, Preceding: psdvector.Point{X: 100, Y: 28},
				Leaving: psdvector.Point{X: 100, Y: 128}},
		}}}}
	expected, err := psdvector.Rasterize(nil, mask, bounds, psdvector.Options{Samples: 4})
	if err != nil || expected.Rect != actual.Rect || !bytes.Equal(expected.Pix, actual.Pix) {
		t.Fatalf("direct cubic raster differs from independently constructed vector controls: %v", err)
	}
	glyph.Path[1].Control2Y = 0
	changed, err := RasterizeRun(nil, run, bounds, RasterOptions{Samples: 4})
	if err != nil || bytes.Equal(changed.Pix, actual.Pix) {
		t.Fatal("second cubic control did not change real coverage")
	}
}
