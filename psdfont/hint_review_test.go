package psdfont

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"reflect"
	"testing"
	"unsafe"
)

// These literal Type2 bytes have hand-calculated stem edges and path positions.
// The mask data follows Adobe Type2 technical note 5177's most-significant-bit order.
func hintReviewFace() *Face {
	program := []byte{
		199, 139, 159, 169, 149, 18,
		32, 10,
		20, 0xc0,
		19, 0xa0,
		143, 139, 21,
		139, 159, 5,
		19, 0x40,
		146, 139, 139, 119, 132, 139, 5,
		14,
	}
	return cffTestProgram(program, [][]byte{{143, 146, 23, 11}})
}

func hintReviewDecode(t *testing.T, face *Face) *Glyph {
	t.Helper()
	glyph, err := face.Glyph(nil, 0, GlyphOptions{PreserveCFFHints: true})
	if err != nil || glyph == nil || glyph.CFFHints == nil {
		t.Fatalf("preserved source hints are unavailable: %v", err)
	}
	return glyph
}

func TestHintReviewSourceWidthsMasksAndSubroutines(t *testing.T) {
	face := hintReviewFace()
	glyph := hintReviewDecode(t, face)
	if glyph.CharstringWidth != 160 || glyph.AdvanceWidth != 600 || glyph.LeftSideBearing != 13 {
		t.Fatal("the first stem's width operand changed hmtx or became a stem coordinate")
	}
	stems := glyph.CFFHints.Stems
	if len(stems) != 3 {
		t.Fatalf("hand-calculated stem count: got %d, want 3", len(stems))
	}
	for index, expected := range [][3]float64{{0, 20, 0}, {50, 60, 0}, {4, 11, 1}} {
		actual := stems[index]
		if actual.Low != expected[0] || actual.High != expected[1] ||
			actual.Vertical != (expected[2] == 1) || actual.Edge || actual.Ghost != 0 {
			t.Fatalf("source stem %d differs: %+v", index, actual)
		}
		original, err := face.ReadSpan(nil, actual.Source)
		wantOperator := byte(18)
		if index == 2 {
			wantOperator = 23
		}
		if err != nil || len(original) != 1 || original[0] != wantOperator {
			t.Fatalf("stem %d did not retain the original subroutine/operator span: %v", index, err)
		}
	}
	masks := glyph.CFFHints.Masks
	if len(masks) != 3 {
		t.Fatalf("counter and active mask events: got %d, want 3", len(masks))
	}
	for index, expected := range []struct {
		command int
		active  byte
		counter bool
	}{{0, 0xc0, true}, {0, 0xa0, false}, {2, 0x40, false}} {
		actual := masks[index]
		if actual.CommandIndex != expected.command || actual.StemCount != 3 ||
			actual.Counter != expected.counter || !bytes.Equal(actual.Active, []byte{expected.active}) {
			t.Fatalf("mask %d did not retain its command position and role: %+v", index, actual)
		}
		original, err := face.ReadSpan(nil, actual.Source)
		wantOperator := byte(19)
		if expected.counter {
			wantOperator = 20
		}
		if err != nil || !bytes.Equal(original, []byte{wantOperator, expected.active}) {
			t.Fatalf("mask %d did not retain its exact source bytes: %v", index, err)
		}
	}
	if len(glyph.Path) != 6 || glyph.Path[0].X != 4 || glyph.Path[0].Y != 0 ||
		glyph.Path[1].X != 4 || glyph.Path[1].Y != 20 || glyph.Path[2].X != 11 || glyph.Path[2].Y != 20 {
		t.Fatal("hintmask moved the current point or rewrote the source outline")
	}
}

func TestHintReviewGhostEdgesAndFontMatrixDomain(t *testing.T) {
	// The official Figure 4 example is 121 -21 400 -20 hstem; its controlled edges are 100 and 500.
	face := cffTestProgram([]byte{247, 13, 118, 248, 36, 119, 1, 139, 139, 21, 14}, nil)
	face.cff.private[0].matrix = [6]float64{2, 0, 0, 3, 7, -11}
	glyph := hintReviewDecode(t, face)
	if len(glyph.CFFHints.Stems) != 2 {
		t.Fatal("ghost stems were not retained")
	}
	for index, expected := range []struct {
		edge  float64
		ghost int8
	}{{100, -21}, {500, -20}} {
		stem := glyph.CFFHints.Stems[index]
		if !stem.Edge || stem.Ghost != expected.ghost || stem.Vertical ||
			stem.Low != expected.edge || stem.High != expected.edge {
			t.Fatalf("ghost %d differs from the independent Figure 4 coordinates: %+v", index, stem)
		}
	}
	if glyph.CFFHints.Matrix != face.cff.private[0].matrix || glyph.Path[0].X != 7 || glyph.Path[0].Y != -11 {
		t.Fatal("source hints were silently converted while FontMatrix remained a second transform")
	}
}

func TestHintReviewPrivateDeltaArraysStayInSourceUnits(t *testing.T) {
	face := hintReviewFace()
	// BlueValues decode to [-10, 0, 490, 500]; StemSnapH decode to [20, 50, 80].
	// A real operand encodes .05 as decimal nibbles A 0 5 F, independently from binary float encoding.
	privateBytes := []byte{
		129, 149, 248, 126, 149, 6,
		159, 169, 169, 12, 12,
		159, 10,
		30, 0xa0, 0x5f, 12, 9,
		146, 12, 10,
		140, 12, 11,
		140, 12, 14,
		140, 12, 17,
	}
	span := Span{Offset: int64(len(face.raw)), Length: int64(len(privateBytes))}
	face.raw = append(face.raw, privateBytes...)
	face.cff.private[0].hintSource = span
	face.cff.private[0].matrix = [6]float64{2, 0, 0, 3, 7, -11}
	glyph := hintReviewDecode(t, face)
	private := glyph.CFFHints.Private
	if !reflect.DeepEqual(private.BlueValues, []float64{-10, 0, 490, 500}) ||
		!reflect.DeepEqual(private.StemSnapH, []float64{20, 50, 80}) || private.StdHW != 20 ||
		private.BlueScale != .05 || private.BlueShift != 7 || private.BlueFuzz != 1 ||
		!private.ForceBold || private.LanguageGroup != 1 || private.ExpansionFactor != .06 {
		t.Fatal("delta arrays, scalar defaults or source units changed during metadata preservation")
	}
	if private.Source.PrivateDICT != span || private.Source.StdVW != (Span{}) {
		t.Fatal("the full original dictionary or omitted-field distinction was lost")
	}
	for _, expected := range []struct {
		span  Span
		bytes []byte
	}{
		{private.Source.BlueValues, privateBytes[:6]},
		{private.Source.StemSnapH, privateBytes[6:11]},
		{private.Source.StdHW, privateBytes[11:13]},
		{private.Source.BlueScale, privateBytes[13:18]},
	} {
		actual, err := face.ReadSpan(nil, expected.span)
		if err != nil || !bytes.Equal(actual, expected.bytes) {
			t.Fatalf("Private field span does not identify its original operands and operator: %v", err)
		}
	}
	glyph.CFFHints.Private.BlueValues[0] = -999
	glyph.CFFHints.Private.StemSnapH[0] = 999
	fresh := hintReviewDecode(t, face)
	if fresh.CFFHints.Private.BlueValues[0] != -10 || fresh.CFFHints.Private.StemSnapH[0] != 20 {
		t.Fatal("Private hint arrays are shared between separately owned glyphs")
	}
}

func TestHintReviewDefaultCoverageOwnershipAndBudget(t *testing.T) {
	face := hintReviewFace()
	plain, err := face.Glyph(nil, 0)
	if err != nil || plain == nil || plain.CFFHints != nil {
		t.Fatalf("default glyph interpretation changed: %v", err)
	}
	preserved := hintReviewDecode(t, face)
	if !reflect.DeepEqual(plain.Path, preserved.Path) || !reflect.DeepEqual(plain.Contours, preserved.Contours) {
		t.Fatal("opt-in metadata collection modified source geometry")
	}
	run := func(glyph *Glyph) *Run {
		return &Run{Scale: .15, Glyphs: []PositionedGlyph{{Glyph: glyph, X: -2.375, Y: 5.125}}}
	}
	bounds := image.Rect(-5, -2, 5, 9)
	first, err := RasterizeRun(nil, run(plain), bounds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RasterizeRun(nil, run(preserved), bounds)
	if err != nil || !bytes.Equal(first.Pix, second.Pix) {
		t.Fatalf("default unhinted pixels depend on whether source hint metadata was requested: %v", err)
	}
	retained := int64(unsafe.Sizeof(CFFHints{})) +
		int64(cap(preserved.CFFHints.Stems))*int64(unsafe.Sizeof(CFFStemHint{})) +
		int64(cap(preserved.CFFHints.Masks))*int64(unsafe.Sizeof(CFFHintMask{}))
	for _, mask := range preserved.CFFHints.Masks {
		retained += int64(cap(mask.Active))
	}
	if preserved.WorkingBytes()-plain.WorkingBytes() < retained {
		t.Fatal("owned hint metadata is missing from the glyph's peak reservation")
	}
	for _, delta := range []int64{-1, 0} {
		actual, err := face.Glyph(nil, 0, GlyphOptions{
			PreserveCFFHints: true, MaxWorkingBytes: preserved.WorkingBytes() + delta,
		})
		if delta < 0 {
			if actual != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("exact hint budget minus one returned a partial glyph: %v", err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(actual, preserved) {
			t.Fatalf("exact hint budget could not reproduce the owned glyph: %v", err)
		}
	}
	before := append([]Command(nil), preserved.Path...)
	preserved.CFFHints.Stems[0].Low = -1000
	preserved.CFFHints.Masks[1].Active[0] = 0
	fresh := hintReviewDecode(t, face)
	if fresh.CFFHints.Stems[0].Low != 0 || fresh.CFFHints.Masks[1].Active[0] != 0xa0 ||
		!reflect.DeepEqual(fresh.Path, before) {
		t.Fatal("caller hint metadata mutation leaked into the face or the next glyph")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledGlyph, cancellationErr := face.Glyph(ctx, 0, GlyphOptions{PreserveCFFHints: true})
	if cancelledGlyph != nil || !errors.Is(cancellationErr, context.Canceled) {
		t.Fatalf("cancelled metadata decode returned a partial glyph: %v", cancellationErr)
	}
}

func TestHintReviewActiveMasksDoNotBecomeGlobalStemMaps(t *testing.T) {
	// The two overlapping HM hints are [0, 40] and [20, 60]. Each active mask selects only one.
	program := []byte{
		139, 179, 119, 179, 18,
		20, 0xc0,
		19, 0x80,
		139, 139, 21,
		139, 179, 5,
		19, 0x40,
		179, 139, 139, 99, 99, 139, 5,
		14,
	}
	face := cffTestProgram(program, nil)
	glyph := hintReviewDecode(t, face)
	run := &Run{Scale: .025, AdvanceWidth: 15, Leading: 20,
		Glyphs: []PositionedGlyph{{Glyph: glyph, X: 3.375, Y: .125, Advance: 15}}}
	sourcePath := append([]Command(nil), glyph.Path...)
	fitted, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: CFFVertical})
	if err != nil || fitted == nil {
		t.Fatalf("counter mask or inactive overlapping stem entered the active map: %v", err)
	}
	if fitted.Scale != 1 || fitted.AdvanceWidth != run.AdvanceWidth || fitted.Leading != run.Leading ||
		fitted.Glyphs[0].Advance != run.Glyphs[0].Advance || !reflect.DeepEqual(glyph.Path, sourcePath) {
		t.Fatal("outline fitting changed logical advances, leading or source path")
	}
	for index, command := range fitted.Glyphs[0].Glyph.Path {
		if command.Op == Close {
			continue
		}
		if command.X != 3.375+sourcePath[index].X*.025 {
			t.Fatal("vertical fitting changed the fractional design X coordinate")
		}
	}
	shifted, err := HintRun(nil, run, [6]float64{1, 0, 0, 1, -13, 17}, HintOptions{Mode: CFFVertical})
	if err != nil {
		t.Fatal(err)
	}
	bounds := fitted.Bounds.Inset(-2)
	first, err := RasterizeRun(nil, fitted, bounds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RasterizeRun(nil, shifted, bounds.Add(image.Pt(-13, 17)))
	if err != nil || !bytes.Equal(first.Pix, second.Pix) {
		t.Fatalf("integer document translation changed hint phase or ROI pixels: %v", err)
	}
	invalid := hintReviewDecode(t, face)
	invalid.CFFHints.Masks = nil
	run.Glyphs[0].Glyph = invalid
	partial, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: CFFVertical})
	if partial != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unresolved overlapping active stems returned a guessed map: %v", err)
	}
}

func TestHintReviewAffineCapabilityAndUnhintedRotation(t *testing.T) {
	glyph := hintReviewDecode(t, hintReviewFace())
	run := &Run{Scale: .5, Glyphs: []PositionedGlyph{{Glyph: glyph, X: 100.25, Y: 200.75}}}
	rotation := [6]float64{0, 1, -1, 0, 5, 7}
	for _, transform := range [][6]float64{
		rotation, {1, .1, 0, 1, 0, 0}, {-1, 0, 0, 1, 0, 0}, {1, 0, 0, -1, 0, 0},
	} {
		partial, err := HintRun(nil, run, transform, HintOptions{Mode: CFFVertical})
		if partial != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported fitting affine returned a partial run: %v", err)
		}
	}
	converted, err := HintRun(nil, run, rotation, HintOptions{Mode: CFFHintNone})
	if err != nil || converted == nil {
		t.Fatalf("explicit unhinted conversion rejected a finite rotation: %v", err)
	}
	// Source Move is (4, 0), producing device (-195.75, -109.25) in the upward-positive path convention.
	// Source first Line is (4, 20), producing device (-185.75, -109.25).
	path := converted.Glyphs[0].Glyph.Path
	if path[0].X != -195.75 || path[0].Y != -109.25 || path[1].X != -185.75 || path[1].Y != -109.25 {
		t.Fatal("unhinted rotation lost source baseline, translation or upward-positive outline convention")
	}
	if converted.Glyphs[0].Glyph == glyph || converted.Glyphs[0].X != 0 || converted.Glyphs[0].Y != 0 {
		t.Fatal("device path output borrows source glyphs or retains a second baseline transform")
	}
	path[0].X = -999
	if glyph.Path[0].X != 4 {
		t.Fatal("owned device path mutation changed source design geometry")
	}
}

func TestHintReviewFittingWorkBudgetAndCancellation(t *testing.T) {
	glyph := hintReviewDecode(t, hintReviewFace())
	run := &Run{Scale: .025, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .25, Y: .375}}}
	fitted, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: CFFVertical})
	if err != nil || fitted == nil {
		t.Fatalf("bounded independent fitting: %v", err)
	}
	for _, delta := range []int64{-1, 0} {
		actual, err := HintRun(nil, run, cffIdentityMatrix,
			HintOptions{Mode: CFFVertical, MaxWorkingBytes: fitted.WorkingBytes() + delta})
		if delta < 0 {
			if actual != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("owned device run peak minus one returned partial output: %v", err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(actual, fitted) {
			t.Fatalf("recorded exact peak cannot reproduce fitting: %v", err)
		}
	}
	low, high := 1, 10000
	for low < high {
		middle := low + (high-low)/2
		actual, err := HintRun(nil, run, cffIdentityMatrix,
			HintOptions{Mode: CFFVertical, MaxOperations: middle})
		if actual != nil && err == nil {
			high = middle
			continue
		}
		if actual == nil && errors.Is(err, ErrLimit) {
			low = middle + 1
			continue
		}
		t.Fatalf("fitting operation-bound search returned an unrelated failure: %v", err)
	}
	if actual, err := HintRun(nil, run, cffIdentityMatrix,
		HintOptions{Mode: CFFVertical, MaxOperations: low - 1}); actual != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("exact work minus one returned a partial hinted run: %v", err)
	}
	counting := &seacCountContext{Context: context.Background()}
	countedRun, countingErr := HintRun(counting, run, cffIdentityMatrix, HintOptions{Mode: CFFVertical})
	if countedRun == nil || countingErr != nil || counting.count < 8 {
		t.Fatalf("fitting did not check context during real work: %v", countingErr)
	}
	cause := errors.New("independent hint cancellation")
	for _, at := range []int{1, counting.count / 2, counting.count} {
		ctx := &seacCountContext{Context: context.Background(), cancelAt: at, cause: cause}
		actual, err := HintRun(ctx, run, cffIdentityMatrix, HintOptions{Mode: CFFVertical})
		if actual != nil || !errors.Is(err, cause) {
			t.Fatalf("cancelled fitting returned partial output or changed context error: %v", err)
		}
	}
}

func TestHintReviewBlueScaleIncludesSourceMatrixAndWorldPhase(t *testing.T) {
	// Ghost source edges are 100 and 500. The rectangle also reaches those exact source Y coordinates.
	face := cffTestProgram([]byte{
		247, 13, 118, 248, 36, 119, 1,
		139, 239, 21,
		179, 139, 139, 248, 36, 99, 139, 139, 252, 36, 5,
		14,
	}, nil)
	// Blue flat is 490, overshoot extends to 510, and the source BlueScale threshold is .05.
	privateBytes := []byte{129, 149, 248, 126, 159, 6, 30, 0xa0, 0x5f, 12, 9}
	face.cff.private[0].hintSource = Span{Offset: int64(len(face.raw)), Length: int64(len(privateBytes))}
	face.raw = append(face.raw, privateBytes...)
	face.cff.private[0].matrix = [6]float64{2, 0, 0, 3, 7, -11}
	glyph := hintReviewDecode(t, face)
	transform := [6]float64{2, 0, 0, 3, 110.125, -50.125}
	for _, expected := range []struct {
		scale float64
		top   float64
	}{{.001, 24}, {.01, 65}} {
		run := &Run{Scale: expected.scale, Glyphs: []PositionedGlyph{{Glyph: glyph, Y: 10.125}}}
		fitted, err := HintRun(nil, run, transform, HintOptions{Mode: CFFVertical})
		if err != nil {
			t.Fatal(err)
		}
		// Raw-to-device Y scale is .009 or .09, including both 3x matrices.
		// The fractional world offsets are 19.717 or 19.42; source flat 490 rounds to 24 or 64.
		// .009 suppresses the ten-unit overshoot, whereas .09 retains one device pixel.
		path := fitted.Glyphs[0].Glyph.Path
		if math.Abs(path[2].Y-expected.top) > 1e-12 || math.Abs(path[3].Y-expected.top) > 1e-12 {
			t.Fatalf("scale %.3g: source-matrix threshold/world phase top is %v/%v, want %v",
				expected.scale, path[2].Y, path[3].Y, expected.top)
		}
		if path[0].X != 110.125+2*expected.scale*7 {
			t.Fatal("FontMatrix translation was reapplied to an already normalized glyph path")
		}
	}
	if glyph.Path[2].Y != 1489 || glyph.CFFHints.Private.BlueValues[2] != 490 {
		t.Fatal("fitting changed normalized design geometry or raw source blue metadata")
	}
}

func TestHintReviewRejectsMutatedSourceIdentityWithoutPartialOutput(t *testing.T) {
	for _, control := range []struct {
		name   string
		mutate func(*CFFHints)
	}{
		{"ordinary-ghost-marker", func(hints *CFFHints) { hints.Stems[0].Ghost = 1 }},
		{"mask-past-outline", func(hints *CFFHints) { hints.Masks[2].CommandIndex = 1000 }},
		{"mask-unused-bits", func(hints *CFFHints) { hints.Masks[1].Active[0] |= 1 }},
		{"mask-stem-count", func(hints *CFFHints) { hints.Masks[1].StemCount = 4 }},
		{"negative-standard-width", func(hints *CFFHints) { hints.Private.StdHW = -1 }},
	} {
		t.Run(control.name, func(t *testing.T) {
			glyph := hintReviewDecode(t, hintReviewFace())
			control.mutate(glyph.CFFHints)
			run := &Run{Scale: .025, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
			actual, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: CFFVertical})
			if actual != nil || !errors.Is(err, ErrMalformed) {
				t.Fatalf("mutated public source metadata returned partial or guessed fitted output: %v", err)
			}
		})
	}
}

func TestHintReviewFractionalStemsPreserveTinyWidthsAndIndependentAxes(t *testing.T) {
	// H0 covers source Y [0, 20], V0 covers source X [4, 11]; both are less than one pixel at this scale.
	face := cffTestProgram([]byte{
		139, 159, 18,
		143, 146, 23,
		19, 0xc0,
		143, 139, 21,
		139, 159, 146, 139, 139, 119, 132, 139, 5,
		14,
	}, nil)
	for _, selected := range []byte{0xc0, 0x80, 0x40} {
		glyph := hintReviewDecode(t, face)
		glyph.CFFHints.Masks[0].Active[0] = selected
		// Each standard/snap width deliberately differs from the actual source stem width.
		glyph.CFFHints.Private.StdHW, glyph.CFFHints.Private.StdVW = 22, 9
		glyph.CFFHints.Private.StemSnapH = []float64{30}
		glyph.CFFHints.Private.StemSnapV = []float64{10}
		run := &Run{Scale: .025, Glyphs: []PositionedGlyph{{Glyph: glyph, X: .0625, Y: .125}}}
		before := append([]Command(nil), glyph.Path...)
		actual, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: CFFFractional})
		if err != nil {
			t.Fatal(err)
		}
		path := actual.Glyphs[0].Glyph.Path
		if math.Abs(path[1].Y-path[0].Y-.5) > 1e-12 || math.Abs(path[2].X-path[1].X-.175) > 1e-12 {
			t.Fatalf("active mask 0x%02x inflated a tiny stem or replaced it with a standard width", selected)
		}
		if selected == 0x80 && path[0].X != .1625 {
			t.Fatal("inactive vertical stem changed the original fractional X coordinate")
		}
		if selected == 0x40 && path[0].Y != -.125 {
			t.Fatal("inactive horizontal stem changed the original fractional Y coordinate")
		}
		if !reflect.DeepEqual(glyph.Path, before) || glyph.AdvanceWidth != 600 {
			t.Fatal("fractional fitting changed owned source geometry or hmtx")
		}
	}
}

func TestHintReviewFractionalCubicControlsUseTheActivePiecewiseMap(t *testing.T) {
	// H0 is [0, 20], H1 is [50, 60], and V0 is [4, 11]. Both horizontal stems are active.
	// The first cubic's Y values are 0, 10, 40, 60, crossing the gap between the two hint zones.
	face := cffTestProgram([]byte{
		139, 159, 169, 149, 18,
		143, 146, 23,
		19, 0xe0,
		143, 139, 21,
		139, 149, 146, 169, 139, 159, 8,
		139, 129, 132, 109, 139, 119, 8,
		14,
	}, nil)
	glyph := hintReviewDecode(t, face)
	run := &Run{Scale: .025, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
	actual, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: CFFFractional})
	if err != nil {
		t.Fatal(err)
	}
	path := actual.Glyphs[0].Glyph.Path
	if len(path) != 4 || path[1].Op != Cubic {
		t.Fatal("fitting replaced the source cubic or changed contour topology")
	}
	// H0 maps device [0, .5] to [-.25, .25]; H1 maps [1.25, 1.5] to [.875, 1.125].
	// The between-zone control at device 1 maps to 2/3, not the single-zone translation result .75.
	if math.Abs(path[0].Y+.25) > 1e-12 || math.Abs(path[1].ControlY) > 1e-12 ||
		math.Abs(path[1].Control2Y-2.0/3) > 1e-12 || math.Abs(path[1].Y-1.125) > 1e-12 {
		t.Fatalf("cubic controls did not use their active piecewise hint intervals: %+v", path[1])
	}
	// This policy maps the original controls and endpoints individually; it remains an approximate cubic outline.
	if glyph.Path[1].ControlY != 10 || glyph.Path[1].Control2Y != 40 || glyph.Path[1].Y != 60 {
		t.Fatal("device cubic fitting changed the original Type2 controls")
	}
}

func TestHintReviewFractionalBlueLocksCannotSilentlyChangeSourceWidth(t *testing.T) {
	// The ordinary stem spans source baseline 0 to cap height 700; both edges are in blue zones.
	face := cffTestProgram([]byte{
		139, 249, 80, 1,
		139, 139, 21,
		179, 139, 139, 249, 80, 99, 139, 139, 253, 80, 5,
		14,
	}, nil)
	privateBytes := []byte{129, 149, 249, 80, 151, 6}
	face.cff.private[0].hintSource = Span{Offset: int64(len(face.raw)), Length: int64(len(privateBytes))}
	face.raw = append(face.raw, privateBytes...)
	glyph := hintReviewDecode(t, face)
	for _, mode := range []CFFHintMode{CFFFractional, CFFEdgeFractional} {
		conflicting := &Run{Scale: .015, Glyphs: []PositionedGlyph{{Glyph: glyph, Y: .2}}}
		// The source stem is 10.5 pixels wide; its separately rounded blue edges would be 0 and 10.
		actual, err := HintRun(nil, conflicting, cffIdentityMatrix, HintOptions{Mode: mode})
		if actual != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("mode %d silently changed source width to satisfy incompatible blue locks: %v", mode, err)
		}
		compatible := &Run{Scale: .01, Glyphs: []PositionedGlyph{{Glyph: glyph, Y: .2}}}
		actual, err = HintRun(nil, compatible, cffIdentityMatrix, HintOptions{Mode: mode})
		if err != nil || actual == nil {
			t.Fatalf("mode %d rejected compatible seven-pixel blue locks: %v", mode, err)
		}
		path := actual.Glyphs[0].Glyph.Path
		if math.Abs(path[2].Y-path[0].Y-7) > 1e-12 {
			t.Fatal("compatible blue captures changed the original fractional stem width")
		}
	}
}

func TestHintReviewEdgeFractionalChoosesOneNearestEdgeWithoutRecentring(t *testing.T) {
	// Horizontal stem [6, 26] scales to [.15, .65]; vertical [20, 40] scales to [.5, 1].
	face := cffTestProgram([]byte{
		145, 159, 1,
		159, 159, 3,
		159, 145, 21,
		139, 159, 159, 139, 139, 119, 119, 139, 5,
		14,
	}, nil)
	glyph := hintReviewDecode(t, face)
	run := &Run{Scale: .025, Glyphs: []PositionedGlyph{{Glyph: glyph}}}
	actual, err := HintRun(nil, run, cffIdentityMatrix, HintOptions{Mode: CFFEdgeFractional})
	if err != nil {
		t.Fatal(err)
	}
	path := actual.Glyphs[0].Glyph.Path
	// H shifts by -.15, the smallest eligible shift. V already has an integer upper edge and remains fixed.
	if math.Abs(path[0].Y) > 1e-12 || math.Abs(path[1].Y-.5) > 1e-12 ||
		math.Abs(path[0].X-.5) > 1e-12 || math.Abs(path[2].X-1) > 1e-12 {
		t.Fatalf("nearest-edge policy changed source widths or snapped an already eligible stem midpoint: %+v", path)
	}
}
