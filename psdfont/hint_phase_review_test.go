package psdfont

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func phaseReviewGlyph() *Glyph {
	return &Glyph{
		ID: 19, AdvanceWidth: 613, LeftSideBearing: 7, CharstringWidth: 607,
		Path: []Command{{Op: Move}, {Op: Line, X: .5}, {Op: Line, X: .5, Y: .5},
			{Op: Line, Y: .5}, {Op: Close}},
		CFFHints: &CFFHints{
			Matrix:  cffIdentityMatrix,
			Private: CFFHintPrivate{BlueScale: .039625, BlueShift: 7, BlueFuzz: 1},
			Stems:   []CFFStemHint{{Low: 0, High: .5}, {Low: 0, High: .5, Vertical: true}},
		},
	}
}

func TestHintPhaseReviewNearestEdgeTiesAndIntegerTranslation(t *testing.T) {
	// Each row independently lists the shortest translation to make either endpoint integral.
	// Equal distances choose the positive translation, including negative document coordinates.
	for _, row := range []struct{ phase, movement float64 }{
		{0, 0}, {.125, -.125}, {.25, .25}, {.375, .125},
		{.5, 0}, {.625, -.125}, {.75, .25}, {.875, .125},
	} {
		for _, translation := range []float64{-37, 0, 128} {
			for _, lattice := range []float64{0, .125, .5} {
				low := row.phase + translation + lattice
				high := low + .5
				movement := hintClosestEdgeShift(low, high, lattice)
				if movement != row.movement {
					t.Fatalf("phase=%g translation=%g lattice=%g: movement %g, want %g",
						row.phase, translation, lattice, movement, row.movement)
				}
				fittedLow, fittedHigh := low+movement, high+movement
				if fittedHigh-fittedLow != .5 ||
					math.Abs(fittedLow-lattice-math.Round(fittedLow-lattice)) != 0 &&
						math.Abs(fittedHigh-lattice-math.Round(fittedHigh-lattice)) != 0 {
					t.Fatal("nearest-edge fitting changed width or left both endpoints off the lattice")
				}
			}
		}
	}
}

func TestHintPhaseReviewSharedSourceGlyphKeepsIndependentDevicePhases(t *testing.T) {
	glyph := phaseReviewGlyph()
	original := append([]Command(nil), glyph.Path...)
	run := &Run{Scale: 1, AdvanceWidth: 40.125, Leading: 17.25, UTF16Units: 4}
	for index, phase := range []float64{.125, .25, .375, .75} {
		run.Glyphs = append(run.Glyphs, PositionedGlyph{Glyph: glyph, X: phase, Y: -phase,
			Advance: 10.03125, ClusterStart: int64(index), ClusterLength: 1})
	}
	for _, translation := range [][2]float64{{}, {-13, 17}} {
		fitted, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, translation[0], translation[1]},
			HintOptions{Mode: CFFEdgeFractional})
		if err != nil {
			t.Fatal(err)
		}
		for index, target := range []float64{0, .5, .5, 1} {
			positioned := fitted.Glyphs[index]
			path := positioned.Glyph.Path
			if path[0].X != target+translation[0] || path[0].Y != target-translation[1] ||
				path[2].X-path[0].X != .5 || path[2].Y-path[0].Y != .5 {
				t.Fatalf("glyph %d reused another fractional phase or changed the preserved stem width: %+v", index, path)
			}
			if positioned.Glyph == glyph || positioned.X != 0 || positioned.Y != 0 ||
				positioned.Advance != 10.03125 || positioned.ClusterStart != int64(index) || positioned.ClusterLength != 1 ||
				positioned.Glyph.AdvanceWidth != 613 || positioned.Glyph.CharstringWidth != 607 {
				t.Fatal("device fitting borrowed source geometry, retained an extra baseline or changed layout metrics")
			}
		}
		fitted.Glyphs[0].Glyph.Path[0].X = -999
		if fitted.Glyphs[1].Glyph.Path[0].X != .5+translation[0] || !reflect.DeepEqual(glyph.Path, original) ||
			fitted.AdvanceWidth != 40.125 || fitted.Leading != 17.25 || fitted.UTF16Units != 4 {
			t.Fatal("device glyphs share mutable paths or fitting changed original run metrics")
		}
	}
}

func TestHintPhaseReviewMaskReactivationDoesNotRetainPreviousPhase(t *testing.T) {
	glyph := phaseReviewGlyph()
	glyph.CFFHints.Stems = []CFFStemHint{{Low: 0, High: .5}, {Low: 1.125, High: 1.75}}
	glyph.CFFHints.Masks = []CFFHintMask{
		{CommandIndex: 0, StemCount: 2, Active: []byte{0x80}},
		{CommandIndex: 5, StemCount: 2, Active: []byte{0x40}},
		{CommandIndex: 6, StemCount: 2, Active: []byte{0x80}, Counter: true},
		{CommandIndex: 10, StemCount: 2, Active: []byte{0x80}},
	}
	contour := append([]Command(nil), glyph.Path...)
	glyph.Path = append(glyph.Path, contour...)
	glyph.Path = append(glyph.Path, contour...)
	original := append([]Command(nil), glyph.Path...)
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .125, Y: -.25}}}
	for _, translation := range [][2]float64{{}, {-13, 17}} {
		fitted, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, translation[0], translation[1]},
			HintOptions{Mode: CFFEdgeFractional})
		if err != nil {
			t.Fatal(err)
		}
		path := fitted.Glyphs[0].Glyph.Path
		for contourIndex, expectedBottom := range []float64{.5, .25, .5} {
			start := contourIndex * 5
			for commandIndex := range 4 {
				command := path[start+commandIndex]
				expectedY := expectedBottom - translation[1]
				if commandIndex >= 2 {
					expectedY += .5
				}
				if command.Y != expectedY || command.X != .125+translation[0]+contour[commandIndex].X {
					t.Fatalf("contour %d command %d retained a previous mask, activated a counter mask or snapped X: %+v",
						contourIndex, commandIndex, command)
				}
			}
		}
		if !reflect.DeepEqual(glyph.Path, original) || glyph.CFFHints.Masks[1].Active[0] != 0x40 ||
			glyph.CFFHints.Masks[2].Active[0] != 0x80 {
			t.Fatal("phase fitting changed source commands or original mask identity")
		}
	}
}

func TestHintPhaseReviewQuarterFittingRetainsExactLimitsAndCancellation(t *testing.T) {
	glyph := phaseReviewGlyph()
	run := &Run{Scale: 1, Glyphs: []PositionedGlyph{
		{Glyph: glyph, X: .1875, Y: -.25}, {Glyph: glyph, X: .5625, Y: -.375},
	}}
	options := HintOptions{Mode: CFFEdgeFractional, SnapBaselineXQuarter: true}
	complete, err := HintRun(nil, run, cffIdentityMatrix, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int64{-1, 0} {
		bounded := options
		bounded.MaxWorkingBytes = complete.WorkingBytes() + delta
		actual, actualErr := HintRun(nil, run, cffIdentityMatrix, bounded)
		if delta < 0 {
			if actual != nil || !errors.Is(actualErr, ErrLimit) {
				t.Fatalf("quarter fitting returned partial output below its exact owned memory peak: %v", actualErr)
			}
			continue
		}
		if actualErr != nil || !reflect.DeepEqual(actual, complete) {
			t.Fatalf("quarter fitting failed at its reported exact memory peak: %v", actualErr)
		}
	}
	low, high := 1, 10000
	for low < high {
		middle := low + (high-low)/2
		bounded := options
		bounded.MaxOperations = middle
		actual, actualErr := HintRun(nil, run, cffIdentityMatrix, bounded)
		if actual != nil && actualErr == nil {
			high = middle
			continue
		}
		if actual == nil && errors.Is(actualErr, ErrLimit) {
			low = middle + 1
			continue
		}
		t.Fatalf("quarter operation bound returned an unrelated failure: %v", actualErr)
	}
	options.MaxOperations = low - 1
	if actual, actualErr := HintRun(nil, run, cffIdentityMatrix, options); actual != nil ||
		!errors.Is(actualErr, ErrLimit) {
		t.Fatalf("quarter fitting returned partial output below its exact operation bound: %v", actualErr)
	}
	options.MaxOperations = low
	counting := &seacCountContext{Context: context.Background()}
	if actual, actualErr := HintRun(counting, run, cffIdentityMatrix, options); actualErr != nil || actual == nil {
		t.Fatalf("quarter fitting failed at its exact operation bound: %v", actualErr)
	}
	cause := errors.New("independent quarter phase cancellation")
	for _, at := range []int{1, counting.count / 2, counting.count} {
		ctx := &seacCountContext{Context: context.Background(), cancelAt: at, cause: cause}
		actual, actualErr := HintRun(ctx, run, cffIdentityMatrix, options)
		if actual != nil || !errors.Is(actualErr, cause) {
			t.Fatalf("quarter fitting returned partial output or changed the original context error: %v", actualErr)
		}
	}
}
