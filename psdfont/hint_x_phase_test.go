package psdfont

import (
	"context"
	"errors"
	"image"
	"reflect"
	"testing"
)

func xPhaseGlyph() *Glyph {
	return &Glyph{
		ID: 42, AdvanceWidth: 309, LeftSideBearing: 97, CharstringWidth: 309,
		Path: []Command{{Op: Move, X: 97}, {Op: Line, X: 213},
			{Op: Line, X: 213, Y: 737}, {Op: Line, X: 97, Y: 737}, {Op: Close}},
		CFFHints: &CFFHints{
			Matrix:  [6]float64{1, 0, 0, 1, 0, 0},
			Private: CFFHintPrivate{BlueScale: .039625, BlueShift: 7, BlueFuzz: 1},
			Stems:   []CFFStemHint{{Low: 97, High: 213, Vertical: true}},
		},
	}
}

func TestHintXQuarterPositiveNegativeTiesAndIntegerTranslation(t *testing.T) {
	// With no active X hints, the device path directly exposes the baseline lattice.
	glyph := xPhaseGlyph()
	glyph.CFFHints.Stems = nil
	for _, row := range []struct{ phase, expected float64 }{
		{-.625, -.5}, {-.375, -.25}, {-.125, 0}, {.125, .25}, {.375, .5},
		{-.126, -.25}, {-.124, 0}, {.124, 0}, {.126, .25},
	} {
		for _, translation := range []float64{-19, 0, 23} {
			run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph, X: row.phase, Y: .41}}}
			actual, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, translation, .17},
				HintOptions{Mode: CFFEdgeFractional, SnapBaselineXQuarter: true})
			if err != nil {
				t.Fatal(err)
			}
			point := actual.Glyphs[0].Glyph.Path[0]
			if point.X != 97+row.expected+translation || point.Y != -(.41+.17) {
				t.Fatalf("phase %g translation %g: device point %+v, want X %g with unchanged Y",
					row.phase, translation, point, 97+row.expected+translation)
			}
		}
	}
}

func TestHintXQuarterPrecedesStemFitAndPreservesSourceMetrics(t *testing.T) {
	glyph := xPhaseGlyph()
	sourcePath := append([]Command(nil), glyph.Path...)
	run := &Run{Scale: .015, AdvanceWidth: 14.125, Leading: 18.25, UTF16Units: 2,
		Glyphs: []PositionedGlyph{
			{Glyph: glyph, X: .13, Y: .41, Advance: 4.635, ClusterStart: 0, ClusterLength: 1},
			{Glyph: glyph, X: 5.08, Y: .41, Advance: 4.635, ClusterStart: 1, ClusterLength: 1},
		}}
	for _, mode := range []CFFHintMode{CFFFractional, CFFEdgeFractional} {
		actual, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, 0, 0},
			HintOptions{Mode: mode, SnapBaselineXQuarter: true})
		if err != nil {
			t.Fatal(err)
		}
		// The first snapped baseline is .25; its 1.74px stem spans [1.705,3.445].
		// Edge fitting chooses low=2; midpoint fitting chooses center=3, low=2.13.
		expectedLow := 2.13
		if mode == CFFEdgeFractional {
			expectedLow = 2
		}
		path := actual.Glyphs[0].Glyph.Path
		if !hintSameCoordinate(path[0].X, expectedLow) ||
			!hintSameCoordinate(path[1].X-path[0].X, 1.74) ||
			!hintSameCoordinate(path[0].Y, -.41) || !hintSameCoordinate(path[2].Y, 10.645) {
			t.Fatalf("mode %d snapped after fitting, changed width or altered Y: %+v", mode, path)
		}
		if actual.AdvanceWidth != 14.125 || actual.Leading != 18.25 || actual.UTF16Units != 2 ||
			actual.Glyphs[0].Advance != 4.635 || actual.Glyphs[1].ClusterStart != 1 ||
			actual.Glyphs[1].ClusterLength != 1 || actual.Glyphs[0].Glyph.AdvanceWidth != 309 ||
			actual.Glyphs[0].Glyph.LeftSideBearing != 97 || actual.Glyphs[0].Glyph.CharstringWidth != 309 ||
			run.Glyphs[0].X != .13 || run.Glyphs[0].Y != .41 || !reflect.DeepEqual(glyph.Path, sourcePath) {
			t.Fatal("X fitting changed source paths, raw metrics, clusters or layout baselines")
		}
		secondX := actual.Glyphs[1].Glyph.Path[0].X
		actual.Glyphs[0].Glyph.Path[0].X = -999
		if actual.Glyphs[1].Glyph.Path[0].X != secondX || !reflect.DeepEqual(glyph.Path, sourcePath) {
			t.Fatal("reused source glyph aliases independent device outlines")
		}
	}
}

func TestHintXQuarterFontMatrixTranslationFollowsBaselineRounding(t *testing.T) {
	glyph := &Glyph{
		Path: []Command{{Op: Move, X: .73, Y: -.17}, {Op: Line, X: 1.33, Y: -.17},
			{Op: Line, X: 1.33, Y: 2.83}, {Op: Line, X: .73, Y: 2.83}, {Op: Close}},
		CFFHints: &CFFHints{
			Matrix:  [6]float64{2, 0, 0, 3, .13, -.17},
			Private: CFFHintPrivate{BlueScale: .039625, BlueShift: 7, BlueFuzz: 1},
		},
	}
	run := &Run{Scale: .5, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .11, Y: .3}}}
	transform := [6]float64{1.5, 0, 0, 2, .11, .19}
	options := HintOptions{Mode: CFFEdgeFractional, SnapBaselineXQuarter: true}
	plain, err := HintRun(nil, run, transform, options)
	if err != nil {
		t.Fatal(err)
	}
	// The device baseline .275 rounds to .25 before the .0975 FontMatrix translation.
	// Raw X .3 then contributes .45 device pixels; rounding the combined origin gives .7 instead.
	if !hintSameCoordinate(plain.Glyphs[0].Glyph.Path[0].X, .7975) {
		t.Fatalf("FontMatrix X translation was snapped or applied twice: %+v", plain.Glyphs[0].Glyph.Path)
	}
	glyph.CFFHints.Stems = []CFFStemHint{{Low: .3, High: .6, Vertical: true}}
	fitted, err := HintRun(nil, run, transform, options)
	if err != nil {
		t.Fatal(err)
	}
	// Correct map coordinates span [.7975,1.2475], whose closest eligible edge is low=1.
	path := fitted.Glyphs[0].Glyph.Path
	if !hintSameCoordinate(path[0].X, 1) || !hintSameCoordinate(path[1].X, 1.45) ||
		!hintSameCoordinate(path[0].Y, plain.Glyphs[0].Glyph.Path[0].Y) {
		t.Fatalf("path conversion and source-hint maps used different rounded origins: %+v", path)
	}
}

func TestHintXQuarterWorldTranslationAndRasterROI(t *testing.T) {
	glyph := xPhaseGlyph()
	run := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .13, Y: .41}}}
	options := HintOptions{Mode: CFFEdgeFractional, SnapBaselineXQuarter: true}
	for _, translation := range []image.Point{{}, {23, -17}, {-19, 11}} {
		fitted, err := HintRun(nil, run,
			[6]float64{1, 0, 0, 1, float64(translation.X), float64(translation.Y)}, options)
		if err != nil {
			t.Fatal(err)
		}
		bounds := image.Rect(0, -12, 6, 2).Add(translation)
		full, err := RasterizeRun(nil, fitted, bounds, RasterOptions{AreaCoverage: true})
		if err != nil {
			t.Fatal(err)
		}
		roiBounds := image.Rect(1, -9, 5, 0).Add(translation)
		roi, err := RasterizeRun(nil, fitted, roiBounds, RasterOptions{AreaCoverage: true})
		if err != nil {
			t.Fatal(err)
		}
		for y := roiBounds.Min.Y; y < roiBounds.Max.Y; y++ {
			for x := roiBounds.Min.X; x < roiBounds.Max.X; x++ {
				if full.AlphaAt(x, y) != roi.AlphaAt(x, y) {
					t.Fatal("ROI recomputed X phase relative to its crop")
				}
			}
		}
		// The fitted source bar is [2,3.74], so an interior row has exact 1px plus .74px coverage.
		if full.AlphaAt(2+translation.X, -5+translation.Y).A != 255 ||
			full.AlphaAt(3+translation.X, -5+translation.Y).A != 189 ||
			full.AlphaAt(1+translation.X, -5+translation.Y).A != 0 {
			t.Fatal("integer document translation changed fractional bar coverage")
		}
	}
}

func TestHintXQuarterValidationBudgetsCancellationAndZeroCompatibility(t *testing.T) {
	glyph := xPhaseGlyph()
	run := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .13}}}
	transform := [6]float64{1, 0, 0, 1, 0, 0}
	for _, options := range []HintOptions{
		{SnapBaselineXQuarter: true},
		{Mode: CFFVertical, SnapBaselineXQuarter: true},
		{Mode: CFFFull, SnapBaselineXQuarter: true},
		{Mode: CFFEdgeFractional, Anchor: CFFHintBaseline, SnapBaselineXQuarter: true},
		{Mode: CFFEdgeFractional, Grid: CFFHintCenters, SnapBaselineXQuarter: true},
	} {
		if _, err := HintRun(nil, run, transform, options); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported X phase combination accepted: %+v %v", options, err)
		}
	}
	options := HintOptions{Mode: CFFEdgeFractional, SnapBaselineXQuarter: true, MaxWorkingBytes: 1}
	if _, err := HintRun(nil, run, transform, options); !errors.Is(err, ErrLimit) {
		t.Fatalf("X phase option bypassed working memory limit: %v", err)
	}
	options.MaxWorkingBytes, options.MaxOperations = 0, 1
	if _, err := HintRun(nil, run, transform, options); !errors.Is(err, ErrLimit) {
		t.Fatalf("X phase option bypassed operation limit: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HintRun(ctx, run, transform,
		HintOptions{Mode: CFFEdgeFractional, SnapBaselineXQuarter: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("X phase option bypassed context cancellation: %v", err)
	}
	implicit, err := HintRun(nil, run, transform, HintOptions{Mode: CFFEdgeFractional})
	if err != nil {
		t.Fatal(err)
	}
	explicit, err := HintRun(nil, run, transform,
		HintOptions{Mode: CFFEdgeFractional, SnapBaselineXQuarter: false})
	if err != nil || !reflect.DeepEqual(implicit, explicit) ||
		!hintSameCoordinate(implicit.Glyphs[0].Glyph.Path[0].X, 1.26) {
		t.Fatalf("false X phase option changed old device output: %v", err)
	}
}
