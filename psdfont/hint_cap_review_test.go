package psdfont

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func capReviewMatrixGlyph(edge float64, ghost bool) *Glyph {
	// These path coordinates already include the source FontMatrix [2 0 0 3 7 -11].
	pointY := func(raw float64) float64 { return 3*raw - 11 }
	stem := CFFStemHint{Low: 680, High: edge}
	if ghost {
		stem = CFFStemHint{Low: edge, High: edge, Edge: true, Ghost: -20}
	}
	return &Glyph{
		ID: 6, AdvanceWidth: 640, LeftSideBearing: 15, CharstringWidth: 630,
		Path: []Command{
			{Op: Move, X: 15, Y: pointY(680)},
			{Op: Line, X: 95, Y: pointY(680)},
			{Op: Cubic, X: 95, Y: pointY(edge), ControlX: 95, ControlY: pointY(690),
				Control2X: 95, Control2Y: pointY(700)},
			{Op: Line, X: 15, Y: pointY(edge)},
			{Op: Close},
		},
		CFFHints: &CFFHints{
			Matrix: [6]float64{2, 0, 0, 3, 7, -11},
			Private: CFFHintPrivate{BlueScale: .03, BlueShift: 7, BlueFuzz: 1,
				BlueValues: []float64{-10, 0, 700, 712}},
			Stems: []CFFStemHint{stem},
		},
	}
}

func TestHintCapReviewHighScaleUsesActualEdgeThroughBothMatrices(t *testing.T) {
	for _, row := range []struct {
		name         string
		edge, offset float64
		target       float64
	}{
		// At .09 device pixels per raw unit, six units already cross a pixel boundary.
		// BlueShift is in raw units; it must not force the six-unit edge back to flat 700.
		{"near-flat", 706, 10.125, 74},
		// The actual edge is 74.57 and rounds to 75. Rounded flat 73 plus one is only a minimum.
		{"overshoot", 712, 10.49, 75},
	} {
		for _, ghost := range []bool{false, true} {
			for _, phase := range []float64{0, -100} {
				glyph := capReviewMatrixGlyph(row.edge, ghost)
				original := append([]Command(nil), glyph.Path...)
				positioned := PositionedGlyph{Glyph: glyph, X: 2.125, Advance: 9.75,
					ClusterStart: 7, ClusterLength: 2}
				run := &Run{Scale: .01, AdvanceWidth: 9.75, Glyphs: []PositionedGlyph{positioned}}
				// Source-to-device Y is .01*3*3=.09, and the source translation contributes -.33.
				transform := [6]float64{2, 0, 0, 3, -5.375, -(row.offset + .33 + phase)}
				actual, err := HintRun(nil, run, transform, HintOptions{Mode: CFFEdgeFractional})
				if err != nil {
					t.Fatalf("%s ghost=%t phase=%g: %v", row.name, ghost, phase, err)
				}
				path := actual.Glyphs[0].Glyph.Path
				target := row.target + phase
				if math.Abs(path[2].Y-target) > 1e-11 || math.Abs(path[3].Y-target) > 1e-11 ||
					math.Abs(path[0].Y-(target-(row.edge-680)*.09)) > 1e-11 ||
					math.Abs(path[2].ControlY-(target-(row.edge-690)*.09)) > 1e-11 ||
					math.Abs(path[2].Control2Y-(target-(row.edge-700)*.09)) > 1e-11 {
					t.Fatalf("%s ghost=%t phase=%g changed actual cap, source width or cubic controls: %+v",
						row.name, ghost, phase, path)
				}
				// No vertical stem was declared, so X keeps the original fractional affine conversion.
				if math.Abs(path[0].X-(-.825)) > 1e-12 || math.Abs(path[1].X-.775) > 1e-12 {
					t.Fatalf("source FontMatrix was applied twice or an undeclared X hint was introduced: %+v", path)
				}
				if !reflect.DeepEqual(glyph.Path, original) || glyph.CFFHints.Stems[0].High != row.edge ||
					glyph.CFFHints.Private.BlueValues[2] != 700 || actual.Glyphs[0].Advance != 9.75 ||
					actual.Glyphs[0].ClusterStart != 7 || actual.Glyphs[0].ClusterLength != 2 ||
					actual.Glyphs[0].Glyph.AdvanceWidth != 640 || run.Glyphs[0].X != 2.125 {
					t.Fatal("device cap fitting changed source geometry, raw blue metadata or original layout metrics")
				}
			}
		}
	}
}

func TestHintCapReviewZeroBlueShiftDoesNotExpandExactFlats(t *testing.T) {
	for _, ghost := range []bool{false, true} {
		for _, mode := range []CFFHintMode{CFFVertical, CFFEdgeFractional} {
			stems := []CFFStemHint{{Low: 0, High: 700}}
			if ghost {
				stems = []CFFStemHint{
					{Low: 0, High: 0, Edge: true, Ghost: -21},
					{Low: 700, High: 700, Edge: true, Ghost: -20},
				}
			}
			glyph := &Glyph{
				Path: []Command{{Op: Move}, {Op: Line, X: 80}, {Op: Line, X: 80, Y: 700},
					{Op: Line, Y: 700}, {Op: Close}},
				CFFHints: &CFFHints{
					Matrix: [6]float64{1, 0, 0, 1, 0, 0},
					Private: CFFHintPrivate{BlueScale: .03, BlueShift: 0, BlueFuzz: 1,
						BlueValues: []float64{-10, 0, 700, 712}},
					Stems: stems,
				},
			}
			run := &Run{Scale: .1, Glyphs: []PositionedGlyph{{Glyph: glyph, Y: .25}}}
			actual, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: mode})
			if err != nil {
				t.Fatalf("ghost=%t mode=%d rejected exact flat edges at legal BlueShift zero: %v", ghost, mode, err)
			}
			path := actual.Glyphs[0].Glyph.Path
			// Actual edges -.25 and 69.75 round to 0 and 70. Neither edge overshoots its source flat.
			if path[0].Y != 0 || path[1].Y != 0 || path[2].Y != 70 || path[3].Y != 70 {
				t.Fatalf("ghost=%t mode=%d manufactured overshoot at exact flat edges: %+v", ghost, mode, path)
			}
		}
	}
}

func capReviewResolvedGlyph(ghost bool) *Glyph {
	// Source FontMatrix maps raw Y [10, 41] to normalized design Y [25, 87].
	stem := CFFStemHint{Low: 10, High: 41}
	if ghost {
		stem = CFFStemHint{Low: 41, High: 41, Edge: true, Ghost: -20}
	}
	return &Glyph{
		ID: 8, AdvanceWidth: 610,
		Path: []Command{{Op: Move, X: 13, Y: 25}, {Op: Line, X: 73, Y: 25},
			{Op: Line, X: 73, Y: 87}, {Op: Line, X: 13, Y: 87}, {Op: Close}},
		CFFHints: &CFFHints{Matrix: [6]float64{3, 0, 0, 2, 7, 5},
			Private: CFFHintPrivate{BlueScale: .2, BlueShift: 7, BlueFuzz: 1,
				BlueValues: []float64{-4, 0, 41, 45}},
			Stems: []CFFStemHint{stem}},
	}
}

func TestHintCapReviewResolvedThresholdUsesRawZoneAndPreservesTranslation(t *testing.T) {
	for _, scale := range []float64{math.Nextafter(.125, 0), .125, math.Nextafter(.125, math.Inf(1))} {
		for _, ghost := range []bool{false, true} {
			for _, mode := range []CFFHintMode{CFFFractional, CFFEdgeFractional} {
				glyph := capReviewResolvedGlyph(ghost)
				before := append([]Command(nil), glyph.Path...)
				run := &Run{Scale: scale / 4, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .125}}}
				transform := [6]float64{1.5, 0, 0, 2, .0625, .375}
				actual, err := HintRun(nil, run, transform,
					HintOptions{Mode: mode, BluePolicy: CFFBlueResolvedOutward})
				if err != nil {
					t.Fatalf("scale=%g ghost=%t mode=%d: %v", scale, ghost, mode, err)
				}
				// The raw zone width is 4, not its normalized width 8. The inclusive gate is 4*scale >= .5.
				// At the gate the source flat is 5.125 and its offset is -.0625, so outward top is 5.9375.
				expected := 5.0
				if scale >= .125 {
					expected = 6 - .375 + 2.5*scale
				}
				path := actual.Glyphs[0].Glyph.Path
				if math.Abs(path[2].Y-expected) > 1e-12 || math.Abs(path[3].Y-expected) > 1e-12 ||
					math.Abs(path[0].Y-(expected-31*scale)) > 1e-12 {
					t.Fatalf("scale=%g ghost=%t mode=%d changed inclusive gate, raw width or source offset: %+v",
						scale, ghost, mode, path)
				}
				if math.Abs(path[0].X-(.25+13*1.5*run.Scale)) > 1e-12 ||
					!reflect.DeepEqual(glyph.Path, before) || glyph.CFFHints.Private.BlueValues[3] != 45 {
					t.Fatal("resolved capture snapped undeclared X or modified owned source geometry")
				}
			}
		}
	}
}

func TestHintCapReviewResolvedCaptureRequiresAnActiveSourceStem(t *testing.T) {
	for _, row := range []struct {
		name        string
		masks       []CFFHintMask
		omitStem    bool
		bottom, top float64
	}{
		{"inactive", []CFFHintMask{{StemCount: 1, Active: []byte{0}}}, false, 1.1875, 5.0625},
		{"active", []CFFHintMask{{StemCount: 1, Active: []byte{0x80}}}, false, 2.0625, 5.9375},
		{"activate-before-top", []CFFHintMask{{StemCount: 1, Active: []byte{0}},
			{CommandIndex: 2, StemCount: 1, Active: []byte{0x80}}}, false, 1.1875, 5.9375},
		{"counter-is-not-active-mask", []CFFHintMask{{StemCount: 1, Active: []byte{0}, Counter: true}},
			false, 2.0625, 5.9375},
		{"bbox-is-not-source-blue-hint", nil, true, 1.1875, 5.0625},
	} {
		glyph := capReviewResolvedGlyph(true)
		glyph.CFFHints.Masks = row.masks
		if row.omitStem {
			glyph.CFFHints.Stems = nil
		}
		run := &Run{Scale: .03125, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
		actual, err := HintRun(nil, run, [6]float64{1.5, 0, 0, 2, .0625, .375},
			HintOptions{Mode: CFFEdgeFractional, BluePolicy: CFFBlueResolvedOutward})
		if err != nil {
			t.Fatalf("%s: %v", row.name, err)
		}
		path := actual.Glyphs[0].Glyph.Path
		if path[0].Y != row.bottom || path[2].Y != row.top ||
			!reflect.DeepEqual(glyph.CFFHints.Masks, row.masks) {
			t.Fatalf("%s inferred cap anchors from bbox or ignored source active-mask role: %+v", row.name, path)
		}
	}
}

func TestHintCapReviewResolvedPairedCapturesRejectWidthChanges(t *testing.T) {
	for _, capHeight := range []float64{40, 41} {
		glyph := capReviewResolvedGlyph(false)
		glyph.Path[0].Y, glyph.Path[1].Y = 5, 5
		glyph.Path[2].Y, glyph.Path[3].Y = 2*capHeight+5, 2*capHeight+5
		glyph.CFFHints.Stems[0].Low, glyph.CFFHints.Stems[0].High = 0, capHeight
		glyph.CFFHints.Private.BlueValues = []float64{-4, 0, capHeight, capHeight + 4}
		run := &Run{Scale: .03125, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
		actual, err := HintRun(nil, run, [6]float64{1.5, 0, 0, 2, .0625, .375},
			HintOptions{Mode: CFFEdgeFractional, BluePolicy: CFFBlueResolvedOutward})
		if capHeight == 41 {
			if actual != nil || !errors.Is(err, ErrUnsupported) {
				t.Fatalf("paired capture silently expanded original 5.125px stem to 6px: %v", err)
			}
			continue
		}
		if err != nil || actual == nil {
			t.Fatalf("compatible five-pixel source stem was rejected: %v", err)
		}
		path := actual.Glyphs[0].Glyph.Path
		if path[0].Y != -.0625 || path[2].Y != 4.9375 || path[2].Y-path[0].Y != 5 {
			t.Fatalf("compatible paired capture changed source width or source translation: %+v", path)
		}
	}
}

func TestHintCapReviewBaselineSnapUsesImageDirectionWithoutSnappingSourceTranslation(t *testing.T) {
	for _, imageBaseline := range []float64{-.5, .5, -1.5, 1.5} {
		for _, translation := range []float64{0, 13, -17} {
			glyph := capReviewResolvedGlyph(true)
			glyph.CFFHints.Stems = nil
			run := &Run{Scale: .03125, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .125}}}
			transform := [6]float64{1.5, 0, 0, 2, .0625, imageBaseline + translation}
			actual, err := HintRun(nil, run, transform, HintOptions{Mode: CFFEdgeFractional,
				BluePolicy: CFFBlueResolvedOutward, SnapBaselineY: true})
			if err != nil {
				t.Fatal(err)
			}
			// In downward image coordinates, ties round toward positive infinity, including -.5 -> 0.
			// The FontMatrix Y translation contributes .3125 after baseline snapping and stays fractional.
			expectedBaseline := -math.Floor(imageBaseline+.5) - translation
			path := actual.Glyphs[0].Glyph.Path
			if path[0].Y != expectedBaseline+1.5625 || path[2].Y != expectedBaseline+5.4375 ||
				path[0].X != .859375 {
				t.Fatalf("image baseline %g shifted by %g snapped source translation or X: %+v",
					imageBaseline, translation, path)
			}
		}
	}
}

func TestHintCapReviewResolvedBudgetAndCancellationReturnNoPartialRun(t *testing.T) {
	glyph := capReviewResolvedGlyph(true)
	run := &Run{Scale: .03125, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
	transform := [6]float64{1.5, 0, 0, 2, .0625, .375}
	options := HintOptions{Mode: CFFEdgeFractional, BluePolicy: CFFBlueResolvedOutward, SnapBaselineY: true}
	actual, err := HintRun(nil, run, transform, options)
	if err != nil {
		t.Fatal(err)
	}
	options.MaxWorkingBytes = actual.WorkingBytes()
	if exact, err := HintRun(nil, run, transform, options); exact == nil || err != nil {
		t.Fatalf("exact recorded memory budget failed: %v", err)
	}
	options.MaxWorkingBytes--
	if partial, err := HintRun(nil, run, transform, options); partial != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("memory minus one returned partial output or unrelated error: %v", err)
	}
	options.MaxWorkingBytes = 0
	counting := &seacCountContext{Context: context.Background()}
	if _, err := HintRun(counting, run, transform, options); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("independent resolved cap cancellation")
	for _, at := range []int{1, counting.count / 2, counting.count} {
		ctx := &seacCountContext{Context: context.Background(), cancelAt: at, cause: cause}
		if partial, err := HintRun(ctx, run, transform, options); partial != nil || !errors.Is(err, cause) {
			t.Fatalf("cancellation at %d returned a partial run or changed the error: %v", at, err)
		}
	}
	options.MaxOperations = 1
	if partial, err := HintRun(nil, run, transform, options); partial != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("work exhaustion returned partial output or unrelated error: %v", err)
	}
}

func TestHintCapReviewResolvedZeroValuesKeepDefaultsAndRejectUnsupportedPolicies(t *testing.T) {
	glyph := capReviewResolvedGlyph(true)
	run := &Run{Scale: .03125, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .125}}}
	transform := [6]float64{1.5, 0, 0, 2, .0625, .375}
	for _, mode := range []CFFHintMode{CFFHintNone, CFFEdgeFractional} {
		implicit, err := HintRun(nil, run, transform, HintOptions{Mode: mode})
		if err != nil {
			t.Fatal(err)
		}
		explicit, err := HintRun(nil, run, transform,
			HintOptions{Mode: mode, BluePolicy: CFFBlueGrid, SnapBaselineY: false})
		if err != nil || !reflect.DeepEqual(implicit, explicit) {
			t.Fatalf("zero-valued policy changed existing mode %d: %v", mode, err)
		}
	}
	for _, options := range []HintOptions{
		{Mode: CFFHintNone, BluePolicy: CFFBlueResolvedOutward},
		{Mode: CFFVertical, BluePolicy: CFFBlueResolvedOutward},
		{Mode: CFFFull, SnapBaselineY: true},
		{Mode: CFFFractional, Anchor: CFFHintBaseline, BluePolicy: CFFBlueResolvedOutward},
		{Mode: CFFEdgeFractional, Grid: CFFHintCenters, SnapBaselineY: true},
		{Mode: CFFEdgeFractional, BluePolicy: CFFBluePolicy(255)},
	} {
		if partial, err := HintRun(nil, run, transform, options); partial != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported policy guessed output instead of returning ErrUnsupported: %+v %v", options, err)
		}
	}
}
