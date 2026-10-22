package psdfont

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestHintBlueShiftKeepsActualEdgeRoundingAboveBlueScale(t *testing.T) {
	// Adobe Type 1 Font Format section 5.7 distinguishes suppressed, ordinary and enforced overshoots.
	private := hintTestPrivate()
	private.BlueValues = []float64{-15, 0, 700, 715}
	for _, row := range []struct {
		name                         string
		edge, scale, offset, lattice float64
		bottom                       bool
		expected                     float64
	}{
		{"top-below-threshold", 705, .03, .4, 0, false, 21},
		{"top-near-flat-crosses-pixel", 705, .1, .4, 0, false, 71},
		{"top-near-flat-negative-phase", 705, .1, -71.1, 0, false, -1},
		{"top-near-flat-same-pixel", 705, .05, .1, 0, false, 35},
		{"bottom-near-flat-crosses-pixel", -5, .1, .1, 0, true, 0},
		{"bottom-near-flat-negative-phase", -5, .1, -.1, 0, true, -1},
		{"top-at-cutoff-uses-actual", 705, .039625, -.3, 0, false, 28},
		{"top-at-shift-forces-pixel", 707, .05, .1, 0, false, 36},
		{"bottom-at-shift-forces-pixel", -7, .05, .1, 0, true, -1},
		{"top-device-phase-precedes-distance-rounding", 714, .1, .4, 0, false, 72},
		{"bottom-device-phase-precedes-distance-rounding", -14, .1, -.4, 0, true, -2},
		{"top-preserves-larger-device-distance", 714, .2, .4, 0, false, 143},
		{"bottom-preserves-larger-device-distance", -14, .2, .4, 0, true, -2},
		{"top-fractional-lattice", 705, .1, .4, .25, false, 71.25},
		{"bottom-fractional-lattice", -5, .1, .1, .25, true, -.75},
	} {
		t.Run(row.name, func(t *testing.T) {
			work := hintWork{ctx: context.Background(), limit: 100}
			captured, actual, err := blueHintTarget(&work, private,
				row.edge, row.bottom, row.scale, row.offset, row.lattice)
			if err != nil || !captured || math.Abs(actual-row.expected) > 1e-12 {
				t.Fatalf("blue target %g, captured=%v, err=%v; want %g", actual, captured, err, row.expected)
			}
		})
	}
}

func TestHintBlueShiftDoesNotQuantizeUncapturedEdges(t *testing.T) {
	private := hintTestPrivate()
	private.BlueValues = []float64{-15, 0, 700, 715}
	for _, row := range []struct {
		edge   float64
		bottom bool
	}{{698.99, false}, {716.01, false}, {-16.01, true}, {1.01, true}, {705, true}, {-5, false}} {
		work := hintWork{ctx: context.Background(), limit: 100}
		captured, _, err := blueHintTarget(&work, private, row.edge, row.bottom, .1, .4, 0)
		if err != nil || captured {
			t.Fatalf("source edge %+v captured outside its eligible blue zone: %v", row, err)
		}
	}
}

func TestHintBlueShiftActualTemplateFontsRemainBelowCutoff(t *testing.T) {
	directory := os.Getenv("PSDKIT_CFF_HINT_FONT_DIR")
	if directory == "" {
		t.Skip("set PSDKIT_CFF_HINT_FONT_DIR to caller-supplied Source Han Sans CN 2.005R fonts")
	}
	for _, row := range []struct {
		weight, text string
		pixelsPerEm  float64
	}{
		{"Medium", "INDEPENDENT KEYS, SENSITIVE ACTION ORIGINAL MOLD OPENING, PRECISE HOLE POSITION", 15},
		{"Bold", " SOFT SILICONE , EASY TO INSTALL & REMOVE", 17.40793 * 1.1489017680135},
		{"Heavy", "DETAIL DISPLAY", 38},
	} {
		t.Run(row.weight, func(t *testing.T) {
			face, err := Open(nil, filepath.Join(directory, "SourceHanSansCN-"+row.weight+".otf"))
			if err != nil {
				t.Fatal(err)
			}
			for _, character := range row.text {
				id, err := face.LookupRune(nil, character)
				if err != nil {
					t.Fatal(err)
				}
				glyph, err := face.Glyph(nil, id, GlyphOptions{PreserveCFFHints: true})
				if err != nil || glyph == nil || glyph.CFFHints == nil {
					t.Fatalf("%c has no source CFF1 hints: %v", character, err)
				}
				coefficient := row.pixelsPerEm / float64(face.UnitsPerEm) * glyph.CFFHints.Matrix[3]
				if coefficient >= glyph.CFFHints.Private.BlueScale {
					t.Fatalf("%c enters changed overshoot branch: scale=%g cutoff=%g", character,
						coefficient, glyph.CFFHints.Private.BlueScale)
				}
			}
		})
	}
}
