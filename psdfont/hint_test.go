package psdfont

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
)

func hintTestPrivate() CFFHintPrivate {
	return CFFHintPrivate{BlueScale: .039625, BlueShift: 7, BlueFuzz: 1}
}

func hintTestGlyph() *Glyph {
	return &Glyph{ID: 4, AdvanceWidth: 600, LeftSideBearing: 19, CharstringWidth: 610,
		Path: []Command{{Op: Move, X: 0, Y: 0}, {Op: Line, X: 80, Y: 0},
			{Op: Cubic, X: 80, Y: 70, ControlX: 80, ControlY: 20, Control2X: 80, Control2Y: 50},
			{Op: Line, X: 0, Y: 70}, {Op: Close}},
		CFFHints: &CFFHints{Matrix: [6]float64{1, 0, 0, 1, 0, 0}, Private: hintTestPrivate(),
			Stems: []CFFStemHint{{Low: 0, High: 70}, {Low: 0, High: 80, Vertical: true}}}}
}

func TestHintDeviceVerticalLatticeAndOwnedOriginalMetrics(t *testing.T) {
	for _, anchor := range []CFFHintAnchor{CFFHintWorld, CFFHintBaseline} {
		for _, grid := range []CFFHintGrid{CFFHintEdges, CFFHintCenters} {
			glyph := hintTestGlyph()
			before := append([]Command(nil), glyph.Path...)
			run := &Run{Scale: .015, AdvanceWidth: 9.5, Glyphs: []PositionedGlyph{{
				Glyph: glyph, X: .25, Y: .375, Advance: 9.5}}}
			options := HintOptions{Mode: CFFVertical, Anchor: anchor, Grid: grid}
			actual, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, -2, 3}, options)
			if err != nil {
				t.Fatal(err)
			}
			fitted := actual.Glyphs[0].Glyph
			lattice := 0.0
			if anchor == CFFHintBaseline {
				lattice = -3.375
			}
			if grid == CFFHintCenters {
				lattice += .5
			}
			// The 1.05px stem is fitted to 1px; its lower edge is the closest eligible lattice edge.
			expectedBottom := lattice + math.Floor((-3.375+.525-.5-lattice)+.5)
			if fitted.Path[0].Y != expectedBottom || fitted.Path[2].Y != expectedBottom+1 ||
				fitted.Path[2].X != -1.75+1.2 || fitted.Path[2].ControlY != expectedBottom+20.0/70 ||
				fitted.Path[2].Control2Y != expectedBottom+50.0/70 {
				t.Fatalf("anchor=%d grid=%d fitted path=%+v expectedBottom=%g", anchor, grid, fitted.Path,
					expectedBottom)
			}
			if actual.Scale != 1 || actual.Glyphs[0].X != 0 || actual.Glyphs[0].Y != 0 ||
				actual.AdvanceWidth != 9.5 || actual.Glyphs[0].Advance != 9.5 ||
				fitted.AdvanceWidth != 600 || fitted.LeftSideBearing != 19 || fitted.CharstringWidth != 610 ||
				!reflect.DeepEqual(glyph.Path, before) || run.Glyphs[0].X != .25 || run.Glyphs[0].Y != .375 {
				t.Fatal("fitting changed source commands, original metrics or cluster layout")
			}
			fitted.Path[0].X = 100
			if glyph.Path[0].X != 0 || actual.WorkingBytes() == 0 {
				t.Fatal("result path aliases the input or has no bounded reservation")
			}
		}
	}
}

func TestHintSourceMatrixBlueScaleShiftFamilyAndGhost(t *testing.T) {
	private := hintTestPrivate()
	private.BlueValues = []float64{-10, 0, 700, 712}
	private.FamilyBlues = []float64{-8, 2, 702, 714}
	work := hintWork{ctx: context.Background(), limit: 1_000_000}
	// A 0.03px/source-unit scale is below BlueScale, suppressing the 12-unit top overshoot.
	captured, target, err := blueHintTarget(&work, private, 712, false, .03, -.625, 0)
	if err != nil || !captured || target != 20 {
		t.Fatalf("source family top-zone alignment or overshoot suppression: %v %g %v", captured, target, err)
	}
	// At 0.06px/source-unit the same overshoot exceeds BlueShift and gets at least one device pixel.
	captured, target, err = blueHintTarget(&work, private, 712, false, .06, -.625, 0)
	if err != nil || !captured || target != 42 {
		t.Fatalf("large-size BlueShift overshoot enforcement: %v %g %v", captured, target, err)
	}
	private.BlueValues, private.FamilyBlues = []float64{-10, 0, 700, 712}, nil
	glyph := hintTestGlyph()
	glyph.CFFHints.Private = private
	glyph.CFFHints.Matrix = [6]float64{2, 0, 3, 3, 5, -11}
	run := &Run{Scale: .01, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
	if _, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, 0, 0},
		HintOptions{Mode: CFFVertical}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a sheared source matrix must not be treated as vertical: %v", err)
	}
	glyph.CFFHints.Matrix[2] = 0
	glyph.Path = []Command{{Op: Move, X: 5, Y: 2125}, {Op: Line, X: 25, Y: 2125},
		{Op: Line, X: 25, Y: -11}, {Op: Close}}
	glyph.CFFHints.Stems = []CFFStemHint{{Low: 712, High: 712, Edge: true, Ghost: -20},
		{Low: 0, High: 0, Edge: true, Ghost: -21}}
	actual, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, .375, .625}, HintOptions{Mode: CFFVertical})
	if err != nil || math.Abs(actual.Glyphs[0].Glyph.Path[0].Y-20) > 1e-9 ||
		math.Abs(actual.Glyphs[0].Glyph.Path[2].Y+1) > 1e-9 {
		t.Fatalf("raw source matrix/translation and ghost blue-zone fitting: %v %+v", err, actual)
	}
}

func TestHintNoHintsRejectMissingSourceAndFittingMapOrder(t *testing.T) {
	glyph := hintTestGlyph()
	run := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
	glyph.CFFHints = nil
	if _, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, 0, 0},
		HintOptions{Mode: CFFVertical}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing source hints must not silently select another hinter: %v", err)
	}
	glyph.CFFHints = &CFFHints{Matrix: [6]float64{1, 0, 0, 1, 0, 0}, Private: hintTestPrivate()}
	plain, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, 0, 0}, HintOptions{Mode: CFFVertical})
	if err != nil || plain.Glyphs[0].Glyph.Path[2].Y != 1.05 {
		t.Fatalf("explicit hintless CFF1 glyph should retain scaled geometry: %v", err)
	}
	work := hintWork{ctx: context.Background(), limit: 100}
	axis := hintAxisMap{count: 2}
	axis.anchors[0], axis.anchors[1] = hintAnchor{0, 1}, hintAnchor{1, 0}
	if err := axis.prepare(&work); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("reversed fitted edge order must be rejected: %v", err)
	}
}

func TestHintFractionalMidpointRetainsSourceWidths(t *testing.T) {
	glyph := hintTestGlyph()
	for index := range glyph.Path {
		command := &glyph.Path[index]
		if command.X == 80 {
			command.X = 116
		}
		if command.ControlX == 80 {
			command.ControlX = 116
		}
		if command.Control2X == 80 {
			command.Control2X = 116
		}
	}
	glyph.CFFHints.Stems[1].High = 116
	// Unrelated declared standard widths must not replace exact source widths in this policy.
	glyph.CFFHints.Private.StdHW, glyph.CFFHints.Private.StdVW = 10, 10
	glyph.CFFHints.Private.StemSnapH, glyph.CFFHints.Private.StemSnapV = []float64{80}, []float64{200}
	run := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: glyph, Advance: 9}}}
	actual, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, 0, 0}, HintOptions{Mode: CFFFractional})
	if err != nil {
		t.Fatal(err)
	}
	path := actual.Glyphs[0].Glyph.Path
	if math.Abs(path[1].X-path[0].X-1.74) > 1e-9 || math.Abs(path[2].Y-path[1].Y-1.05) > 1e-9 ||
		math.Abs((path[1].X+path[0].X)/2-1) > 1e-9 || math.Abs((path[2].Y+path[1].Y)/2-1) > 1e-9 ||
		actual.Glyphs[0].Advance != 9 || glyph.Path[1].X != 116 {
		t.Fatalf("fractional source width, lattice midpoint or source identity changed: %+v", path)
	}
	// A stem thinner than a pixel remains thin instead of being forced to one pixel.
	run.Scale = .005
	actual, err = HintRun(nil, run, [6]float64{1, 0, 0, 1, 0, 0}, HintOptions{Mode: CFFFractional})
	if err != nil {
		t.Fatal(err)
	}
	path = actual.Glyphs[0].Glyph.Path
	if math.Abs(path[2].Y-path[1].Y-.35) > 1e-9 {
		t.Fatalf("thin stem expanded to a minimum width: %+v", path)
	}
}

func TestHintClosestEdgeFractionalMinimumMovementAndWidths(t *testing.T) {
	for _, row := range []struct{ low, high, lattice, shift float64 }{
		{.25, 1.99, 0, .01}, {.01, 1.75, 0, -.01}, {-.5, .5, 0, .5}, {-.375, .675, 0, .325},
		{.375, 2.125, .375, 0},
	} {
		actual := hintClosestEdgeShift(row.low, row.high, row.lattice)
		if math.Abs(actual-row.shift) > 1e-9 {
			t.Fatalf("closest fractional edge shift %+v got %g", row, actual)
		}
		low, high := row.low+actual-row.lattice, row.high+actual-row.lattice
		if math.Abs(low-math.Round(low)) > 1e-9 && math.Abs(high-math.Round(high)) > 1e-9 {
			t.Fatalf("neither edge reached the fitting lattice: %g/%g", low, high)
		}
	}
	glyph := hintTestGlyph()
	glyph.CFFHints.Private.StdHW = 500
	run := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .25, Y: -.375}}}
	actual, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, 0, 0}, HintOptions{Mode: CFFEdgeFractional})
	if err != nil {
		t.Fatal(err)
	}
	path := actual.Glyphs[0].Glyph.Path
	if math.Abs(path[2].Y-path[0].Y-1.05) > 1e-9 || math.Abs(path[1].X-path[0].X-1.2) > 1e-9 ||
		math.Abs(path[0].Y) > 1e-9 || math.Abs(path[1].X-1.2) > 1e-9 {
		t.Fatalf("closest edge fit changed raw width or selected a farther edge: %+v", path)
	}
}
