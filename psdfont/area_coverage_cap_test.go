package psdfont

import (
	"bytes"
	"image"
	"testing"
)

func TestAreaCoverageAnchoredFauxBoldFractionalCap(t *testing.T) {
	for _, translation := range []image.Point{{}, {X: -17, Y: -23}} {
		glyph := fauxBoldRectangleGlyph(image.Rect(0, 0, 4, 3), image.Rectangle{})
		run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph,
			X: float64(translation.X) + .3, Y: float64(translation.Y) + 47.0/64,
		}}}
		bounds := image.Rect(-1, -1, 7, 6).Add(translation)
		options := RasterOptions{AreaCoverage: true, AreaSamples: 64,
			FauxBoldAnchored: true, FauxBoldRadius: .5}
		coverage, err := RasterizeRun(nil, run, bounds, options)
		if err != nil {
			t.Fatal(err)
		}
		// Every positive anchored translate starts at or below the original cap.
		// These interior columns contain exactly 17/64 of a pixel of the original rectangle.
		// Their analytic area is 17/64, which rounds once to alpha 68.
		for x := 1; x <= 3; x++ {
			if actual := coverage.AlphaAt(x+translation.X, translation.Y).A; actual != 68 {
				t.Fatalf("translation=%v cap column %d: got alpha %d; analytic area alpha 68",
					translation, x, actual)
			}
			if coverage.AlphaAt(x+translation.X, translation.Y-1).A != 0 ||
				coverage.AlphaAt(x+translation.X, translation.Y+1).A != 255 {
				t.Fatalf("translation=%v: positive faux union changed cap exterior or interior", translation)
			}
		}
		legacy, err := RasterizeRun(nil, run, bounds,
			RasterOptions{Samples: 8, FauxBoldAnchored: true, FauxBoldRadius: .5})
		if err != nil || legacy.AlphaAt(translation.X+2, translation.Y).A != 64 {
			t.Fatalf("legacy midpoint-grid control: alpha=%v error=%v", legacy, err)
		}
		roi := image.Rect(1, -1, 4, 2).Add(translation)
		crop, err := RasterizeRun(nil, run, roi, options)
		if err != nil {
			t.Fatal(err)
		}
		for y := roi.Min.Y; y < roi.Max.Y; y++ {
			for x := roi.Min.X; x < roi.Max.X; x++ {
				if crop.AlphaAt(x, y) != coverage.AlphaAt(x, y) {
					t.Fatalf("absolute ROI changed cap coverage at (%d,%d)", x, y)
				}
			}
		}
		run.Glyphs = append(run.Glyphs, run.Glyphs[0])
		repeated, err := RasterizeRun(nil, run, bounds, options)
		if err != nil || !bytes.Equal(repeated.Pix, coverage.Pix) {
			t.Fatalf("duplicating the anchored outline darkened cap coverage: %v", err)
		}
	}
}
