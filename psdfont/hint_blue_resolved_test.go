package psdfont

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestHintResolvedBlueUsesSourceZoneResolutionAndOffset(t *testing.T) {
	private := hintTestPrivate()
	private.BlueScale = .06
	private.BlueValues = []float64{-10, 0, 701, 711}
	private.OtherBlues = []float64{-111, -101}
	for _, row := range []struct {
		name        string
		edge, scale float64
		bottom      bool
		expected    float64
	}{
		{"below-half-pixel-top", 701, .049, false, 35},
		{"exact-half-pixel-top", 701, .05, false, 36.375},
		{"exact-half-pixel-overshoot-suppressed", 711, .05, false, 36.375},
		{"exact-half-pixel-bottom", -101, .05, true, -5.625},
		{"near-flat-resolved-relative-model", 707, .1, false, 71.375},
		{"resolved-relative-shift-boundary", 708, .1, false, 72.375},
	} {
		t.Run(row.name, func(t *testing.T) {
			work := hintWork{ctx: context.Background(), limit: 100}
			captured, actual, err := blueHintTargetPolicy(&work, private,
				row.edge, row.bottom, row.scale, .375, 0, CFFBlueResolvedOutward)
			if err != nil || !captured || math.Abs(actual-row.expected) > 1e-12 {
				t.Fatalf("resolved target %g, captured=%v, err=%v; want %g", actual, captured, err, row.expected)
			}
		})
	}
	if !reflect.DeepEqual(private.BlueValues, []float64{-10, 0, 701, 711}) ||
		!reflect.DeepEqual(private.OtherBlues, []float64{-111, -101}) {
		t.Fatal("fitting changed source alignment-zone slices")
	}
}

func TestHintSnapBaselineYUsesImageDownTiesAndIntegerTranslation(t *testing.T) {
	glyph := hintTestGlyph()
	glyph.CFFHints.Stems = nil
	before := append([]Command(nil), glyph.Path...)
	for _, row := range []struct{ imageY, expectedUp float64 }{
		{.5, -1}, {-.5, 0}, {1.5, -2}, {-1.5, 1},
		{.25, 0}, {.75, -1}, {-.25, 0}, {-.75, 1},
	} {
		for _, translation := range []float64{-8, 0, 9} {
			run := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: glyph, X: 1.125, Y: row.imageY}}}
			fitted, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, -3.75, translation}, HintOptions{
				Mode: CFFEdgeFractional, SnapBaselineY: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := fitted.Glyphs[0].Glyph.Path
			if path[0].Y != row.expectedUp-translation || path[0].X != -2.625 ||
				math.Abs(path[2].Y-path[0].Y-1.05) > 1e-12 {
				t.Fatalf("imageY %g translated %g: device path %+v", row.imageY, translation, path)
			}
		}
	}
	if !reflect.DeepEqual(glyph.Path, before) {
		t.Fatal("device baseline rounding modified source glyph geometry")
	}
}

func TestHintResolvedBlueAndBaselineOptionsRejectUnqualifiedCombinations(t *testing.T) {
	run := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: hintTestGlyph()}}}
	identity := [6]float64{1, 0, 0, 1, 0, 0}
	for _, active := range []HintOptions{
		{BluePolicy: CFFBlueResolvedOutward}, {SnapBaselineY: true},
		{BluePolicy: CFFBlueResolvedOutward, SnapBaselineY: true},
	} {
		for _, mode := range []CFFHintMode{CFFHintNone, CFFVertical, CFFFull} {
			options := active
			options.Mode = mode
			actual, err := HintRun(nil, run, identity, options)
			if actual != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unqualified mode %d returned a partial run or no capability error: %v", mode, err)
			}
		}
		for _, lattice := range []HintOptions{
			{Anchor: CFFHintBaseline}, {Grid: CFFHintCenters},
		} {
			options := active
			options.Mode, options.Anchor, options.Grid = CFFEdgeFractional, lattice.Anchor, lattice.Grid
			actual, err := HintRun(nil, run, identity, options)
			if actual != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unqualified lattice returned a partial run or no capability error: %v", err)
			}
		}
		for _, mode := range []CFFHintMode{CFFFractional, CFFEdgeFractional} {
			options := active
			options.Mode = mode
			if actual, err := HintRun(nil, run, identity, options); err != nil || actual == nil {
				t.Fatalf("qualified fractional world/edges combination failed: %v", err)
			}
		}
	}
	actual, err := HintRun(nil, run, identity, HintOptions{Mode: CFFEdgeFractional, BluePolicy: 2})
	if actual != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown blue policy returned a partial run or no capability error: %v", err)
	}
}
