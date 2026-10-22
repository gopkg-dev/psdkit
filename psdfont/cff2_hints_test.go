package psdfont

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
	"unsafe"
)

func cff2HintTestVM() *cff2VM {
	return &cff2VM{sink: type2Reader{ctx: context.Background(), glyph: &Glyph{},
		limits: GlyphOptions{MaxOperations: 100000, MaxWorkingBytes: 1 << 20, MaxPoints: 1000,
			MaxContours: 100, MaxComponents: 100, MaxDepth: 10}, private: cffPrivate{matrix: cffIdentityMatrix}}}
}

func cff2HintTestArgs(r *cff2VM, values, encoded []float64, blended []bool) {
	r.count = len(values)
	copy(r.stack[:], values)
	if encoded == nil {
		encoded = values
	}
	copy(r.encodedStack[:], encoded)
	clear(r.operandBlended[:])
	copy(r.operandBlended[:], blended)
}

func cff2HintTestStem(t *testing.T, r *cff2VM, operator int, values []float64) {
	t.Helper()
	cff2HintTestArgs(r, values, nil, nil)
	if consumed, err := r.hintOperator(operator, nil, 100); err != nil || consumed != 0 {
		t.Fatalf("stem operator=%d consumed=%d error=%v", operator, consumed, err)
	}
}

func TestCFF2HintsEdgeWidthsActualSortAndNeverOverlap(t *testing.T) {
	r := cff2HintTestVM()
	cff2HintTestStem(t, r, 1, []float64{0, 1000})
	cff2HintTestStem(t, r, 1, []float64{121, -21, 400, -20})
	if r.hints != 3 || !r.hintState.stems[1].edge || r.hintState.stems[1].low != 100 ||
		!r.hintState.stems[2].edge || r.hintState.stems[2].low != 500 || r.hintState.overlapping {
		t.Fatalf("edge hints=%+v", r.hintState.stems[:r.hints])
	}
	if consumed, err := r.hintOperator(20, []byte{0xe0}, 200); err != nil || consumed != 1 {
		t.Fatalf("counter with non-overlapping edge hints consumed=%d error=%v", consumed, err)
	}
	r = cff2HintTestVM()
	cff2HintTestStem(t, r, 18, []float64{121, -21, 5, 10})
	if r.hintState.stems[0].encodedLow != 100 || r.hintState.stems[1].encodedLow != 105 {
		t.Fatalf("bottom edge sorted by encoded first instead of actual edge: %+v", r.hintState.stems[:2])
	}
}

func TestCFF2HintsIndependentOperatorsResetCoordinatesAndKeepEncodedOrder(t *testing.T) {
	r := cff2HintTestVM()
	cff2HintTestStem(t, r, 18, []float64{0, 10})
	cff2HintTestStem(t, r, 18, []float64{30, 10})
	if r.hintState.stems[1].encodedLow != 30 || r.hintState.stems[1].low != 30 {
		t.Fatalf("independent operator did not reset to zero: %+v", r.hintState.stems[1])
	}
	cff2HintTestArgs(r, []float64{20, 5}, nil, nil)
	if _, err := r.hintOperator(18, nil, 300); !errors.Is(err, ErrMalformed) {
		t.Fatalf("cross-operator encoded descending order accepted: %v", err)
	}
}

func TestCFF2HintsEncodedOrderAndDerivedBlendReordering(t *testing.T) {
	for _, maskOperator := range []int{19, 20} {
		t.Run(string(rune('a'+maskOperator)), func(t *testing.T) {
			r := cff2HintTestVM()
			cff2HintTestArgs(r, []float64{0, 10, -50, 10}, []float64{0, 10, 30, 10},
				[]bool{true, false, true, false})
			if _, err := r.hintOperator(18, nil, 100); err != nil {
				t.Fatal(err)
			}
			if r.hintState.stems[1].low != -40 || r.hintState.stems[1].encodedLow != 40 {
				t.Fatalf("encoded identity lost: %+v", r.hintState.stems[:2])
			}
			if consumed, err := r.hintOperator(maskOperator, []byte{0xc0}, 200); err != nil || consumed != 1 {
				t.Fatalf("legal derived descending order mask=%d consumed=%d error=%v", maskOperator, consumed, err)
			}
		})
	}
	r := cff2HintTestVM()
	cff2HintTestArgs(r, []float64{0, 10, 30, 10}, []float64{0, 10, -20, 10}, []bool{true})
	if _, err := r.hintOperator(18, nil, 100); !errors.Is(err, ErrMalformed) {
		t.Fatalf("encoded descending order hidden by instance accepted: %v", err)
	}
}

func TestCFF2HintsStemWidthAndEdgeBlendRestrictions(t *testing.T) {
	rows := []struct {
		name    string
		values  []float64
		encoded []float64
		blended []bool
		valid   bool
	}{
		{"literal-bottom-edge", []float64{121, -21}, nil, nil, true},
		{"literal-top-edge", []float64{500, -20}, nil, nil, true},
		{"blended-first-edge-position", []float64{131, -21}, []float64{121, -21}, []bool{true, false}, true},
		{"blended-zero-stem-width", []float64{0, 0}, []float64{0, 10}, []bool{false, true}, true},
		{"negative-ordinary-width", []float64{0, -1}, nil, nil, false},
		{"negative-derived-width", []float64{0, -1}, []float64{0, 10}, []bool{false, true}, false},
		{"blended-edge-width-no-change", []float64{121, -21}, nil, []bool{false, true}, false},
		{"stem-derived-as-edge-width", []float64{0, -20}, []float64{0, 10}, []bool{false, true}, false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := cff2HintTestVM()
			cff2HintTestArgs(r, row.values, row.encoded, row.blended)
			_, err := r.hintOperator(18, nil, 100)
			if row.valid && err != nil || !row.valid && !errors.Is(err, ErrMalformed) {
				t.Fatalf("valid=%t error=%v", row.valid, err)
			}
		})
	}
}

func TestCFF2HintsHintMasksRequireHMAndNoActiveOverlapStrictProfile(t *testing.T) {
	for _, operator := range []int{1, 3} {
		r := cff2HintTestVM()
		cff2HintTestStem(t, r, operator, []float64{0, 10, 30, 10})
		if _, err := r.hintOperator(19, []byte{0xc0}, 200); !errors.Is(err, ErrMalformed) {
			t.Fatalf("regular stem operator=%d with hintmask accepted: %v", operator, err)
		}
	}
	r := cff2HintTestVM()
	cff2HintTestStem(t, r, 18, []float64{0, 10, -5, 10})
	if _, err := r.hintOperator(19, []byte{0xc0}, 200); !errors.Is(err, ErrMalformed) {
		t.Fatalf("strict profile active overlapping HM stems accepted: %v", err)
	}
	if consumed, err := r.hintOperator(19, []byte{0x80}, 201); err != nil || consumed != 1 {
		t.Fatalf("non-overlapping active subset error=%v", err)
	}
	if consumed, err := r.hintOperator(19, []byte{0}, 202); err != nil || consumed != 1 {
		t.Fatalf("all hints inactive should be permitted: %v", err)
	}
	r = cff2HintTestVM()
	cff2HintTestArgs(r, []float64{0, 10, -5, 10}, nil, nil)
	if _, err := r.hintOperator(1, nil, 100); !errors.Is(err, ErrMalformed) {
		t.Fatalf("regular overlapping stems accepted: %v", err)
	}
}

func TestCFF2HintsCounterGroupSameOrientationAndStrictNonOverlap(t *testing.T) {
	for _, family := range []int{1, 18} {
		t.Run(string(rune('a'+family)), func(t *testing.T) {
			r := cff2HintTestVM()
			cff2HintTestStem(t, r, family, []float64{0, 10, 30, 10})
			vertical := 3
			if family == 18 {
				vertical = 23
			}
			cff2HintTestStem(t, r, vertical, []float64{0, 10})
			for _, mask := range []byte{0xc0, 0xe0} {
				if consumed, err := r.hintOperator(20, []byte{mask}, 200); err != nil || consumed != 1 {
					t.Fatalf("counter mask=%x with at least two H error=%v", mask, err)
				}
			}
		})
	}
	for _, mask := range []byte{0, 0x80, 0xc0} {
		r := cff2HintTestVM()
		cff2HintTestStem(t, r, 18, []float64{0, 10})
		cff2HintTestStem(t, r, 23, []float64{0, 10})
		if _, err := r.hintOperator(20, []byte{mask}, 200); !errors.Is(err, ErrMalformed) {
			t.Fatalf("counter without two same-orientation stems mask=%x error=%v", mask, err)
		}
	}
	r := cff2HintTestVM()
	cff2HintTestStem(t, r, 18, []float64{0, 10, -5, 10})
	if _, err := r.hintOperator(20, []byte{0xc0}, 200); !errors.Is(err, ErrMalformed) {
		t.Fatalf("overlapping selected counter stems accepted: %v", err)
	}
}

func TestCFF2HintsMaskBytesUnusedBitsAndMaximum96(t *testing.T) {
	for _, count := range []int{1, 8, 9, 96} {
		t.Run(string(rune('a'+count)), func(t *testing.T) {
			r := cff2HintTestVM()
			args := make([]float64, count*2)
			for index := range count {
				args[index*2+1] = 2
				if index > 0 {
					args[index*2] = 10
				}
			}
			cff2HintTestStem(t, r, 18, args)
			mask := make([]byte, (count+7)/8)
			mask[0] = 0x80
			if consumed, err := r.hintOperator(19, mask, 200); err != nil || consumed != len(mask) || r.hints != count {
				t.Fatalf("count=%d consumed=%d error=%v", count, consumed, err)
			}
			if _, err := r.hintOperator(19, mask[:len(mask)-1], 201); !errors.Is(err, ErrMalformed) {
				t.Fatalf("truncated mask count=%d error=%v", count, err)
			}
			if count%8 != 0 {
				mask[len(mask)-1] |= 1
				if _, err := r.hintOperator(19, mask, 202); !errors.Is(err, ErrMalformed) {
					t.Fatalf("unused bit count=%d error=%v", count, err)
				}
			}
		})
	}
	r := cff2HintTestVM()
	args := make([]float64, 194)
	for index := range args {
		args[index] = 1
	}
	cff2HintTestArgs(r, args, nil, nil)
	if _, err := r.hintOperator(18, nil, 100); !errors.Is(err, ErrLimit) || r.hints != 0 {
		t.Fatalf("97 hints should fail before any hint record: count=%d error=%v", r.hints, err)
	}
	if _, err := cff2HintTestVM().hintOperator(19, []byte{0}, 100); !errors.Is(err, ErrMalformed) {
		t.Fatalf("mask with no stems accepted: %v", err)
	}
}

func TestCFF2HintsOrganizationImplicitStemsAndPathMasks(t *testing.T) {
	rows := []struct {
		name  string
		setup func(*testing.T, *cff2VM)
		op    int
		args  []float64
		mask  []byte
		valid bool
	}{
		{"horizontal-after-vertical", func(t *testing.T, r *cff2VM) { cff2HintTestStem(t, r, 23, []float64{0, 10}) },
			18, []float64{0, 10}, nil, false},
		{"implicit-HM-vertical", func(t *testing.T, r *cff2VM) { cff2HintTestStem(t, r, 18, []float64{0, 10}) },
			19, []float64{0, 10}, []byte{0xc0}, true},
		{"implicit-regular-vertical-counter", func(t *testing.T, r *cff2VM) {
			cff2HintTestStem(t, r, 1, []float64{0, 10, 30, 10})
		},
			20, []float64{0, 10, 30, 10}, []byte{0xf0}, true},
		{"implicit-without-horizontal", func(t *testing.T, r *cff2VM) {},
			19, []float64{0, 10}, []byte{0x80}, false},
		{"odd-stem-arguments", func(t *testing.T, r *cff2VM) {}, 18, []float64{0, 10, 5}, nil, false},
		{"empty-stem-arguments", func(t *testing.T, r *cff2VM) {}, 18, nil, nil, false},
		{"stem-after-counter", func(t *testing.T, r *cff2VM) {
			cff2HintTestStem(t, r, 18, []float64{0, 10, 30, 10})
			if _, err := r.hintOperator(20, []byte{0xc0}, 200); err != nil {
				t.Fatal(err)
			}
		},
			23, []float64{0, 10}, nil, false},
		{"counter-after-hintmask", func(t *testing.T, r *cff2VM) {
			cff2HintTestStem(t, r, 18, []float64{0, 10, 30, 10})
			if _, err := r.hintOperator(19, []byte{0xc0}, 200); err != nil {
				t.Fatal(err)
			}
		},
			20, nil, []byte{0xc0}, false},
		{"path-hintmask-no-new-stems", func(t *testing.T, r *cff2VM) {
			cff2HintTestStem(t, r, 18, []float64{0, 10})
			r.hintState.path, r.sink.open = true, true
		}, 19, nil, []byte{0x80}, true},
		{"path-hintmask-with-new-stems", func(t *testing.T, r *cff2VM) {
			cff2HintTestStem(t, r, 18, []float64{0, 10})
			r.hintState.path, r.sink.open = true, true
		}, 19, []float64{30, 10}, []byte{0xc0}, false},
		{"path-counter-mask", func(t *testing.T, r *cff2VM) {
			cff2HintTestStem(t, r, 18, []float64{0, 10, 30, 10})
			r.hintState.path, r.sink.open = true, true
		}, 20, nil, []byte{0xc0}, false},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := cff2HintTestVM()
			row.setup(t, r)
			cff2HintTestArgs(r, row.args, nil, nil)
			_, err := r.hintOperator(row.op, row.mask, 300)
			if row.valid && err != nil || !row.valid && !errors.Is(err, ErrMalformed) {
				t.Fatalf("valid=%t error=%v", row.valid, err)
			}
		})
	}
}

func cff2HintTestNumber(program []byte, value int) []byte {
	if value >= -107 && value <= 107 {
		return append(program, byte(value+139))
	}
	return append(program, 28, byte(uint16(value)>>8), byte(value))
}

func cff2HintTestNumbers(program []byte, values ...int) []byte {
	for _, value := range values {
		program = cff2HintTestNumber(program, value)
	}
	return program
}

func cff2HintTestProgram() []byte {
	program := cff2HintTestNumbers(nil, 0, 10, 30, 10)
	program = append(program, 18)
	program = cff2HintTestNumbers(program, 0, 10, 30, 10)
	program = append(program, 23, 20, 0xf0, 19, 0xf0)
	program = cff2HintTestNumbers(program, 0, 0)
	program = append(program, 21)
	program = cff2HintTestNumbers(program, 40, 0)
	program = append(program, 5, 19, 0xc0)
	program = cff2HintTestNumbers(program, 0, 40)
	return append(program, 5, 19, 0x30)
}

func cff2HintTestFace(program []byte, store *itemVariationStore) *Face {
	return &Face{NumGlyphs: 1, UnitsPerEm: 1000, horizontalMetrics: 1,
		raw: append([]byte{0, 100, 0, 0}, program...), tables: map[string]Span{"hmtx": {0, 4}},
		cff2: &cff2Font{charstrings: []Span{{4, int64(len(program))}}, private: []cff2Private{{}}, store: store}}
}

func TestCFF2HintsPublicGlyphUnhintedPathMatchesPlainProgram(t *testing.T) {
	face := cff2HintTestFace(cff2HintTestProgram(), nil)
	glyph, err := face.Glyph(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	plain := cff2HintTestNumbers(nil, 0, 0)
	plain = append(plain, 21)
	plain = cff2HintTestNumbers(plain, 40, 0, 0, 40)
	plain = append(plain, 5)
	want, err := cff2HintTestFace(plain, nil).Glyph(context.Background(), 0)
	if err != nil || !reflect.DeepEqual(glyph.Path, want.Path) || !reflect.DeepEqual(glyph.Contours, want.Contours) ||
		glyph.Bounds != want.Bounds || glyph.AdvanceWidth != 100 || glyph.Source != face.cff2.charstrings[0] {
		t.Fatalf("unhinted geometry changed: got=%+v want=%+v error=%v", glyph, want, err)
	}
}

func TestCFF2HintsVMBlendProvenanceAndSubroutineState(t *testing.T) {
	store := &itemVariationStore{regionCount: 1, scalars: []float64{1},
		data: []itemVariationData{{regionIDs: []uint16{0}}}}
	program := cff2HintTestNumbers(nil, 0, 10, 30, 10, 0, 0, -80, 0, 4)
	program = append(program, 16, 18, 20, 0xc0, 19, 0xc0)
	face := cff2HintTestFace(program, store)
	if glyph, err := face.Glyph(context.Background(), 0); err != nil || glyph == nil {
		t.Fatalf("legal source-ordered blended stems rejected: glyph=%v error=%v", glyph, err)
	}
	edge := cff2HintTestNumbers(nil, 121, -21, 0, 0, 2)
	edge = append(edge, 16, 18)
	if glyph, err := cff2HintTestFace(edge, store).Glyph(context.Background(), 0); glyph != nil ||
		!errors.Is(err, ErrMalformed) {
		t.Fatalf("blended edge-width provenance missed: glyph=%v error=%v", glyph, err)
	}
	root := cff2HintTestNumbers(nil, -107)
	root = append(root, 10, 18, 19, 0x80)
	subroutine := cff2HintTestNumbers(nil, 0, 10)
	face = cff2HintTestFace(root, nil)
	face.cff2.private[0].subrs = []Span{{int64(len(face.raw)), int64(len(subroutine))}}
	face.raw = append(face.raw, subroutine...)
	if glyph, err := face.Glyph(context.Background(), 0); err != nil || glyph == nil {
		t.Fatalf("subroutine-pushed stem operands lost provenance: glyph=%v error=%v", glyph, err)
	}
	root = cff2HintTestNumbers(nil, -107)
	root = append(root, 10)
	root = cff2HintTestNumbers(root, 0, 10)
	root = append(root, 23, 19, 0xc0)
	subroutine = append(cff2HintTestNumbers(nil, 0, 10), 18)
	face = cff2HintTestFace(root, nil)
	face.cff2.private[0].subrs = []Span{{int64(len(face.raw)), int64(len(subroutine))}}
	face.raw = append(face.raw, subroutine...)
	if glyph, err := face.Glyph(context.Background(), 0); err != nil || glyph == nil {
		t.Fatalf("subroutine stem definitions did not share order and mask state: glyph=%v error=%v", glyph, err)
	}
}

func TestCFF2HintsPublicMalformedOrderingAndStrictProfileReturnsNilGlyph(t *testing.T) {
	rows := []struct {
		name    string
		program []byte
	}{
		{"odd-stem-operands", append(cff2HintTestNumbers(nil, 0, 10, 5), 18)},
		{"regular-stems-with-hintmask", append(cff2HintTestNumbers(nil, 0, 10), 1, 19, 0x80)},
		{"unused-mask-bits", append(cff2HintTestNumbers(nil, 0, 10), 18, 19, 0x81)},
		{"truncated-mask", append(cff2HintTestNumbers(nil, 0, 10), 18, 19)},
		{"counter-one-stem", append(cff2HintTestNumbers(nil, 0, 10), 18, 20, 0x80)},
	}
	program := append(cff2HintTestNumbers(nil, 0, 10), 23)
	program = append(cff2HintTestNumbers(program, 0, 10), 18)
	rows = append(rows, struct {
		name    string
		program []byte
	}{"horizontal-after-vertical", program})
	program = append(cff2HintTestNumbers(nil, 0, 10, -5, 10), 18)
	program = append(cff2HintTestNumbers(program, 0, 0), 21)
	rows = append(rows, struct {
		name    string
		program []byte
	}{"strict-overlap-needs-initial-mask", program})
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			glyph, err := cff2HintTestFace(row.program, nil).Glyph(context.Background(), 0)
			var typed *Error
			if glyph != nil || !errors.Is(err, ErrMalformed) || !errors.As(err, &typed) ||
				typed.Path != "cff2.charstring" {
				t.Fatalf("malformed hint output glyph=%v error=%v", glyph, err)
			}
		})
	}
}

func TestCFF2HintsPublicLimitsBytesPointsAndSharedWork(t *testing.T) {
	face := cff2HintTestFace(cff2HintTestProgram(), nil)
	baseline, err := face.Glyph(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int64{-1, 0, 1} {
		glyph, err := face.Glyph(context.Background(), 0, GlyphOptions{MaxWorkingBytes: baseline.reservation + delta})
		if delta < 0 && (glyph != nil || !errors.Is(err, ErrLimit)) || delta >= 0 && (err != nil || glyph == nil) {
			t.Fatalf("MaxWorkingBytes delta=%d glyph=%v error=%v", delta, glyph, err)
		}
	}
	for _, delta := range []int{-1, 0, 1} {
		glyph, err := face.Glyph(context.Background(), 0, GlyphOptions{MaxPoints: 3 + delta})
		if delta < 0 && (glyph != nil || !errors.Is(err, ErrLimit)) || delta >= 0 && (err != nil || glyph == nil) {
			t.Fatalf("MaxPoints delta=%d glyph=%v error=%v", delta, glyph, err)
		}
	}
	r := cff2HintTestVM()
	r.sink.face, r.font = face, face.cff2
	r.sink.glyph.Source = face.cff2.charstrings[0]
	if err := r.sink.reserve(int64(unsafe.Sizeof(cff2VM{})) + int64(unsafe.Sizeof(Glyph{}))); err != nil {
		t.Fatal(err)
	}
	if err := r.execute(r.sink.glyph.Source, 0); err != nil {
		t.Fatal(err)
	}
	work := r.sink.operations
	for _, delta := range []int{-1, 0, 1} {
		glyph, err := face.Glyph(context.Background(), 0, GlyphOptions{MaxOperations: work + delta})
		if delta < 0 && (glyph != nil || !errors.Is(err, ErrLimit)) || delta >= 0 && (err != nil || glyph == nil) {
			t.Fatalf("shared MaxOperations work=%d delta=%d glyph=%v error=%v", work, delta, glyph, err)
		}
	}
	t.Logf("VMSize=%d hintStateSize=%d glyphReservation=%d hintsAndPathWork=%d points=3",
		unsafe.Sizeof(cff2VM{}), unsafe.Sizeof(cff2HintState{}), baseline.reservation, work)
}

type cff2HintCancelContext struct {
	calls, cancelAt int
	cause           error
}

func (c *cff2HintCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cff2HintCancelContext) Done() <-chan struct{}       { return nil }
func (c *cff2HintCancelContext) Value(key any) any           { return nil }
func (c *cff2HintCancelContext) Err() error {
	c.calls++
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return c.cause
	}
	return nil
}

func TestCFF2HintsPublicCountedCancellationReturnsNilGlyph(t *testing.T) {
	face := cff2HintTestFace(cff2HintTestProgram(), nil)
	baseline := &cff2HintCancelContext{}
	if _, err := face.Glyph(baseline, 0); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{1, baseline.calls / 2, baseline.calls} {
		cause := errors.New("owned CFF2 hint cancellation cause")
		ctx := &cff2HintCancelContext{cancelAt: at, cause: cause}
		glyph, err := face.Glyph(ctx, 0)
		if glyph != nil || !errors.Is(err, cause) || ctx.calls != at {
			t.Fatalf("cancel at=%d calls=%d glyph=%v error=%v", at, ctx.calls, glyph, err)
		}
	}
}
