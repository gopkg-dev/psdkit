package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func variationScalarTestBudget(bytes int64, work int) *fontMetadataBudget {
	return &fontMetadataBudget{face: &Face{}, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: bytes, MaxCFFOperations: work}}
}

func variationScalarTestStore() *itemVariationStore {
	ramp, ignored := variationRegionAxis{0, 16384, 16384}, variationRegionAxis{-16384, 0, 16384}
	return &itemVariationStore{axisCount: 2, regionCount: 5, source: Span{Offset: 77},
		regions: []variationRegionAxis{
			ramp, ramp,
			ramp, ignored,
			ignored, ramp,
			{-16384, -16384, 0}, ramp,
			{8192, 4096, 16384}, {-8192, 4096, 8192},
		}}
}

func TestVariationStoreAxisScalarLiteralDyadicRules(t *testing.T) {
	rows := []struct {
		name       string
		axis       variationRegionAxis
		coordinate int16
		want       float64
	}{
		{"positive-ramp-eighth", variationRegionAxis{0, 16384, 16384}, 2048, 0.125},
		{"positive-ramp-half", variationRegionAxis{0, 16384, 16384}, 8192, 0.5},
		{"positive-ramp-peak-end", variationRegionAxis{0, 16384, 16384}, 16384, 1},
		{"positive-ramp-start", variationRegionAxis{0, 16384, 16384}, 0, 0},
		{"positive-ramp-outside", variationRegionAxis{0, 16384, 16384}, -1, 0},
		{"negative-ramp-half", variationRegionAxis{-16384, -16384, 0}, -8192, 0.5},
		{"negative-ramp-peak-start", variationRegionAxis{-16384, -16384, 0}, -16384, 1},
		{"negative-ramp-end", variationRegionAxis{-16384, -16384, 0}, 0, 0},
		{"negative-ramp-outside", variationRegionAxis{-16384, -16384, 0}, 1, 0},
		{"triangle-left", variationRegionAxis{0, 8192, 16384}, 4096, 0.5},
		{"triangle-right", variationRegionAxis{0, 8192, 16384}, 12288, 0.5},
		{"triangle-peak", variationRegionAxis{0, 8192, 16384}, 8192, 1},
		{"triangle-start", variationRegionAxis{0, 8192, 16384}, 0, 0},
		{"triangle-end", variationRegionAxis{0, 8192, 16384}, 16384, 0},
		{"degenerate-point-at-peak", variationRegionAxis{8192, 8192, 8192}, 8192, 1},
		{"degenerate-point-outside", variationRegionAxis{8192, 8192, 8192}, 8191, 0},
		{"invalid-start-peak-order-ignored", variationRegionAxis{8192, 4096, 16384}, -16384, 1},
		{"invalid-peak-end-order-ignored", variationRegionAxis{0, 16384, 8192}, 0, 1},
		{"nonzero-peak-cross-zero-ignored", variationRegionAxis{-8192, 4096, 8192}, 0, 1},
		{"zero-peak-min-ignored", variationRegionAxis{-16384, 0, 16384}, -16384, 1},
		{"zero-peak-max-ignored", variationRegionAxis{-16384, 0, 16384}, 16384, 1},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got := variationAxisScalar(row.axis, row.coordinate)
			if math.Float64bits(got) != math.Float64bits(row.want) {
				t.Fatalf("scalar=%g expected literal dyadic=%g", got, row.want)
			}
		})
	}
}

func TestVariationStorePrepareScalarsAxisProductAndCompleteWork(t *testing.T) {
	store := variationScalarTestStore()
	normalized := []int16{8192, 4096}
	budget := variationScalarTestBudget(40, 18)
	if err := store.prepareScalars(normalized, budget); err != nil {
		t.Fatal(err)
	}
	want := []float64{0.125, 0.5, 0.25, 0, 1}
	if !reflect.DeepEqual(store.scalars, want) || budget.face.reservation != 40 || budget.operations != 18 {
		t.Fatalf("scalars=%v reservation=%d work=%d", store.scalars, budget.face.reservation, budget.operations)
	}
	normalized[0], normalized[1] = -16384, -16384
	if !reflect.DeepEqual(store.scalars, want) {
		t.Fatal("scalars alias normalized coordinate request")
	}
	zeroAxis := &itemVariationStore{axisCount: 0, regionCount: 3}
	zeroBudget := variationScalarTestBudget(24, 4)
	if err := zeroAxis.prepareScalars(nil, zeroBudget); err != nil ||
		!reflect.DeepEqual(zeroAxis.scalars, []float64{1, 1, 1}) || zeroBudget.operations != 4 {
		t.Fatalf("zero-axis empty products=%v work=%d error=%v", zeroAxis.scalars, zeroBudget.operations, err)
	}
}

func TestVariationStorePrepareScalarReservationAndWorkBoundaries(t *testing.T) {
	for _, delta := range []int64{-1, 0, 1} {
		t.Run("bytes"+string(rune('1'+delta)), func(t *testing.T) {
			store := variationScalarTestStore()
			budget := variationScalarTestBudget(17+40+delta, 18)
			budget.face.reservation = 17
			err := store.prepareScalars([]int16{8192, 4096}, budget)
			if delta < 0 {
				if !errors.Is(err, ErrLimit) || store.scalars != nil || budget.face.reservation != 17 {
					t.Fatalf("under-budget scalars=%v reservation=%d error=%v", store.scalars, budget.face.reservation, err)
				}
				return
			}
			if err != nil || len(store.scalars) != 5 || budget.face.reservation != 57 {
				t.Fatalf("exact-budget scalars=%v reservation=%d error=%v", store.scalars, budget.face.reservation, err)
			}
		})
	}
	for _, delta := range []int{-1, 0, 1} {
		t.Run("work"+string(rune('1'+delta)), func(t *testing.T) {
			store := variationScalarTestStore()
			budget := variationScalarTestBudget(40, 18+delta)
			err := store.prepareScalars([]int16{8192, 4096}, budget)
			if delta < 0 {
				if !errors.Is(err, ErrLimit) || store.scalars != nil || budget.operations != 17 {
					t.Fatalf("under-work scalars=%v work=%d error=%v", store.scalars, budget.operations, err)
				}
				return
			}
			if err != nil || len(store.scalars) != 5 || budget.operations != 18 {
				t.Fatalf("exact-work scalars=%v work=%d error=%v", store.scalars, budget.operations, err)
			}
		})
	}
}

func TestVariationStorePrepareScalarsMalformedDimensionsAndAtomicPublish(t *testing.T) {
	rows := []struct {
		name       string
		store      *itemVariationStore
		normalized []int16
	}{
		{"nil-store", nil, nil},
		{"axis-count-mismatch", &itemVariationStore{axisCount: 1}, nil},
		{"missing-region-axis", &itemVariationStore{axisCount: 2, regionCount: 1,
			regions: []variationRegionAxis{{0, 16384, 16384}}}, []int16{0, 0}},
		{"negative-region-count", &itemVariationStore{regionCount: -1}, nil},
		{"extra-region-axis", &itemVariationStore{axisCount: 1, regionCount: 0,
			regions: []variationRegionAxis{{0, 0, 0}}}, []int16{0}},
		{"coordinate-below-min", &itemVariationStore{axisCount: 1}, []int16{-16385}},
		{"coordinate-above-max", &itemVariationStore{axisCount: 1}, []int16{16385}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			err := row.store.prepareScalars(row.normalized, variationScalarTestBudget(1024, 100))
			var typed *Error
			if !errors.Is(err, ErrMalformed) || !errors.As(err, &typed) ||
				row.store != nil && row.store.scalars != nil {
				t.Fatalf("malformed dimensions error=%v", err)
			}
		})
	}
	store := variationScalarTestStore()
	old := []float64{0.25, 0.25, 0.25, 0.25, 0.25}
	store.scalars = old
	err := store.prepareScalars([]int16{8192, 4096}, variationScalarTestBudget(40, 17))
	if !errors.Is(err, ErrLimit) || &store.scalars[0] != &old[0] || !reflect.DeepEqual(store.scalars, old) {
		t.Fatalf("failed recomputation published partial scalars=%v error=%v", store.scalars, err)
	}
}

func variationScalarTestEncodeRows(longWords bool, wordCount uint16, rows [][]int32) []byte {
	data := []byte{}
	for _, row := range rows {
		for column, value := range row {
			width := 1
			if longWords {
				width = 2
			}
			if column < int(wordCount) {
				width *= 2
			}
			for octet := width - 1; octet >= 0; octet-- {
				data = append(data, byte(uint32(value)>>uint(8*octet)))
			}
		}
	}
	return data
}

func variationScalarTestDeltaStore(longWords bool, wordCount uint16, rows [][]int32) *itemVariationStore {
	rowSize := 4 + int(wordCount)
	if longWords {
		rowSize *= 2
	}
	return &itemVariationStore{regionCount: 3, scalars: []float64{0.5, 0.25, 0.125}, source: Span{Offset: 77},
		data: []itemVariationData{{present: true, itemCount: uint16(len(rows)), wordCount: wordCount,
			longWords: longWords, regionIDs: []uint16{2, 0, 1, 1}, rowSize: rowSize,
			deltaBytes: variationScalarTestEncodeRows(longWords, wordCount, rows), source: Span{Offset: 100}}}}
}

type variationScalarTestWork struct {
	operations, limit int
	ctx               context.Context
	paths             []string
	offsets           []int64
}

func (w *variationScalarTestWork) step(path string, offset int64) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if w.operations >= w.limit {
		return fontError(ErrLimit, path, offset, "owned delta execution work limit")
	}
	w.operations++
	w.paths = append(w.paths, path)
	w.offsets = append(w.offsets, offset)
	return nil
}

func variationScalarTestRuntimeWork(limit int) *variationScalarTestWork {
	return &variationScalarTestWork{limit: limit, ctx: context.Background()}
}

func TestVariationStoreDeltaMixedWidthsSignedExtremesAndFractions(t *testing.T) {
	rows := []struct {
		name      string
		longWords bool
		words     uint16
		values    [][]int32
		want      []float64
	}{
		{"short-mixed", false, 2, [][]int32{{300, -400, -5, 7}, {32767, -32768, -128, 127}},
			[]float64{-162, -12288.375}},
		{"long-mixed", true, 2, [][]int32{{2147483647, -2147483648, -32768, 32767}},
			[]float64{-805306368.375}},
		{"short-all-bytes", false, 0, [][]int32{{-128, 127, -5, 7}}, []float64{48}},
		{"short-all-words", false, 4, [][]int32{{32767, -32768, -128, 127}}, []float64{-12288.375}},
		{"long-all-shorts", true, 0, [][]int32{{32767, -32768, -128, 127}}, []float64{-12288.375}},
		{"long-all-words", true, 4, [][]int32{{2147483647, -2147483648, -128, 127}},
			[]float64{-805306368.375}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			store := variationScalarTestDeltaStore(row.longWords, row.words, row.values)
			for inner, want := range row.want {
				work := variationScalarTestRuntimeWork(6)
				got, err := store.delta(0, uint16(inner), work.step)
				if err != nil || math.Float64bits(got) != math.Float64bits(want) || work.operations != 6 {
					t.Fatalf("inner=%d delta=%g want literal=%g work=%d error=%v", inner, got, want, work.operations, err)
				}
			}
		})
	}
}

func TestVariationStoreDeltaByteOffsetsAndSharedRuntimeWork(t *testing.T) {
	store := variationScalarTestDeltaStore(false, 2, [][]int32{{300, -400, -5, 7}, {32767, -32768, -128, 127}})
	work := variationScalarTestRuntimeWork(12)
	got, err := store.delta(0, 1, work.step)
	wantOffsets := []int64{77, 120, 122, 124, 125, 126}
	if err != nil || got != -12288.375 || !reflect.DeepEqual(work.offsets, wantOffsets) {
		t.Fatalf("row offsets=%v delta=%g error=%v", work.offsets, got, err)
	}
	if _, err := store.delta(0, 0, work.step); err != nil || work.operations != 12 {
		t.Fatalf("shared work=%d error=%v", work.operations, err)
	}
	got, err = store.delta(0, 0, work.step)
	if got != 0 || !errors.Is(err, ErrLimit) || work.operations != 12 {
		t.Fatalf("getter reset shared work: delta=%g work=%d error=%v", got, work.operations, err)
	}
	for _, delta := range []int{-1, 0, 1} {
		work := variationScalarTestRuntimeWork(6 + delta)
		got, err := store.delta(0, 0, work.step)
		if delta < 0 && (got != 0 || !errors.Is(err, ErrLimit) || work.operations != 5) ||
			delta >= 0 && (err != nil || got != -162 || work.operations != 6) {
			t.Fatalf("runtime work boundary delta=%d result=%g work=%d error=%v", delta, got, work.operations, err)
		}
	}
}

func TestVariationStoreDeltaNullNoVariationAndEmptyRows(t *testing.T) {
	store := &itemVariationStore{data: []itemVariationData{{present: false, source: Span{Offset: 900}}}}
	for _, inner := range []uint16{0, 65535} {
		work := variationScalarTestRuntimeWork(2)
		got, err := store.delta(0, inner, work.step)
		if got != 0 || err != nil || work.operations != 2 {
			t.Fatalf("NULL offset inner=%d result=%g error=%v", inner, got, err)
		}
	}
	var absent *itemVariationStore
	work := variationScalarTestRuntimeWork(2)
	got, err := absent.delta(65535, 65535, work.step)
	if got != 0 || err != nil || work.operations != 2 {
		t.Fatalf("NoVariationIndex result=%g error=%v", got, err)
	}
	empty := &itemVariationStore{data: []itemVariationData{{present: true, itemCount: 2}}}
	got, err = empty.delta(0, 1, variationScalarTestRuntimeWork(2).step)
	if got != 0 || err != nil {
		t.Fatalf("zero-region row result=%g error=%v", got, err)
	}
}

func TestVariationStoreDeltaMalformedReturnsNoPartialAdjustment(t *testing.T) {
	rows := []struct {
		name   string
		change func(*itemVariationStore)
		outer  uint16
		inner  uint16
	}{
		{"outer-invalid", func(s *itemVariationStore) {}, 1, 0},
		{"inner-invalid", func(s *itemVariationStore) {}, 0, 1},
		{"missing-precomputed-scalars", func(s *itemVariationStore) { s.scalars = nil }, 0, 0},
		{"word-count-above-region-count", func(s *itemVariationStore) { s.data[0].wordCount = 5 }, 0, 0},
		{"row-size-mismatch", func(s *itemVariationStore) { s.data[0].rowSize-- }, 0, 0},
		{"row-truncated", func(s *itemVariationStore) { s.data[0].deltaBytes = s.data[0].deltaBytes[:5] }, 0, 0},
		{"late-region-index-invalid", func(s *itemVariationStore) { s.data[0].regionIDs[3] = 3 }, 0, 0},
		{"late-scalar-NaN", func(s *itemVariationStore) { s.scalars[1] = math.NaN() }, 0, 0},
		{"late-scalar-infinite", func(s *itemVariationStore) { s.scalars[1] = math.Inf(1) }, 0, 0},
		{"late-scalar-negative", func(s *itemVariationStore) { s.scalars[1] = -0.125 }, 0, 0},
		{"late-scalar-above-one", func(s *itemVariationStore) { s.scalars[1] = 1.125 }, 0, 0},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			store := variationScalarTestDeltaStore(false, 2, [][]int32{{300, -400, -5, 7}})
			row.change(store)
			got, err := store.delta(row.outer, row.inner, variationScalarTestRuntimeWork(100).step)
			var typed *Error
			if got != 0 || !errors.Is(err, ErrMalformed) || !errors.As(err, &typed) || typed.Path != "variationStore.delta" {
				t.Fatalf("malformed delta=%g error=%v", got, err)
			}
		})
	}
	store := variationScalarTestDeltaStore(false, 2, [][]int32{{300, -400, -5, 7}})
	if got, err := store.delta(0, 0, nil); got != 0 || !errors.Is(err, ErrMalformed) {
		t.Fatalf("nil work callback result=%g error=%v", got, err)
	}
}

type variationScalarCancelContext struct {
	calls, cancelAt int
	cause           error
}

func (c *variationScalarCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *variationScalarCancelContext) Done() <-chan struct{}       { return nil }
func (c *variationScalarCancelContext) Value(key any) any           { return nil }
func (c *variationScalarCancelContext) Err() error {
	c.calls++
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return c.cause
	}
	return nil
}

func TestVariationStoreScalarAndDeltaCountedCancellation(t *testing.T) {
	store := variationScalarTestStore()
	baseline := &variationScalarCancelContext{}
	budget := variationScalarTestBudget(40, 18)
	budget.ctx = baseline
	if err := store.prepareScalars([]int16{8192, 4096}, budget); err != nil {
		t.Fatal(err)
	}
	for _, at := range []int{1, baseline.calls / 2, baseline.calls} {
		t.Run("scalars"+string(rune('a'+at)), func(t *testing.T) {
			cause := errors.New("owned scalar precompute cancellation")
			ctx := &variationScalarCancelContext{cancelAt: at, cause: cause}
			store := variationScalarTestStore()
			budget := variationScalarTestBudget(40, 18)
			budget.ctx = ctx
			if err := store.prepareScalars([]int16{8192, 4096}, budget); !errors.Is(err, cause) || store.scalars != nil {
				t.Fatalf("scalar cancel at=%d scalars=%v error=%v", at, store.scalars, err)
			}
		})
	}
	for _, at := range []int{1, 3, 6} {
		t.Run("delta"+string(rune('0'+at)), func(t *testing.T) {
			cause := errors.New("owned delta execution cancellation")
			ctx := &variationScalarCancelContext{cancelAt: at, cause: cause}
			store := variationScalarTestDeltaStore(false, 2, [][]int32{{300, -400, -5, 7}})
			work := variationScalarTestRuntimeWork(6)
			work.ctx = ctx
			got, err := store.delta(0, 0, work.step)
			if got != 0 || !errors.Is(err, cause) || ctx.calls != at {
				t.Fatalf("delta cancel at=%d calls=%d result=%g error=%v", at, ctx.calls, got, err)
			}
		})
	}
}

func variationScalarTestStoreBytes() []byte {
	data := make([]byte, 78)
	binary.BigEndian.PutUint16(data, 1)
	binary.BigEndian.PutUint32(data[2:], 16)
	binary.BigEndian.PutUint16(data[6:], 2)
	binary.BigEndian.PutUint32(data[8:], 56)
	binary.BigEndian.PutUint16(data[16:], 2)
	binary.BigEndian.PutUint16(data[18:], 3)
	axisWords := []int16{0, 16384, 16384, 0, 16384, 16384,
		0, 16384, 16384, -16384, 0, 16384, -16384, 0, 16384, 0, 16384, 16384}
	for index, value := range axisWords {
		binary.BigEndian.PutUint16(data[20+2*index:], uint16(value))
	}
	binary.BigEndian.PutUint16(data[56:], 2)
	binary.BigEndian.PutUint16(data[58:], 2)
	binary.BigEndian.PutUint16(data[60:], 3)
	binary.BigEndian.PutUint16(data[62:], 0)
	binary.BigEndian.PutUint16(data[64:], 1)
	binary.BigEndian.PutUint16(data[66:], 2)
	copy(data[68:], []byte{1, 44, 254, 112, 251, 127, 255, 128, 0, 127})
	return data
}

func TestVariationStoreParsedLiteralStoreScalarDeltaIntegration(t *testing.T) {
	data := variationScalarTestStoreBytes()
	budget := variationScalarTestBudget(1<<20, 1000)
	store, err := readItemVariationStore(data, 1234, 0, 2, budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.prepareScalars([]int16{8192, 4096}, budget); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.scalars, []float64{0.125, 0.5, 0.25}) || store.source != (Span{1234, 78}) {
		t.Fatalf("parsed store scalars=%v source=%v", store.scalars, store.source)
	}
	work := variationScalarTestRuntimeWork(10)
	for inner, want := range []float64{-163.75, -12256.375} {
		got, err := store.delta(0, uint16(inner), work.step)
		if err != nil || math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("parsed inner=%d result=%g want=%g error=%v", inner, got, want, err)
		}
	}
	if got, err := store.delta(1, 65535, variationScalarTestRuntimeWork(2).step); got != 0 || err != nil {
		t.Fatalf("parsed NULL offset result=%g error=%v", got, err)
	}
}
