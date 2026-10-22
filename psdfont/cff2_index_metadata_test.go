package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
	"time"
)

func cff2MetadataTestBudget(byteLimit int64, workLimit int) *fontMetadataBudget {
	return &fontMetadataBudget{
		face: &Face{}, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: byteLimit, MaxCFFOperations: workLimit},
	}
}

func cff2MetadataTestIndex(width int, offsets []uint32, payload []byte) []byte {
	data := make([]byte, 5+(len(offsets))*width+len(payload))
	binary.BigEndian.PutUint32(data, uint32(len(offsets)-1))
	data[4] = byte(width)
	for index, value := range offsets {
		for octet := range width {
			data[5+index*width+octet] = byte(value >> uint(8*(width-1-octet)))
		}
	}
	copy(data[5+len(offsets)*width:], payload)
	return data
}

func cff2MetadataTestRanges(format byte, first []uint32, selectors []uint16, sentinel uint32) []byte {
	countWidth, recordWidth := 2, 3
	if format == 4 {
		countWidth, recordWidth = 4, 6
	}
	data := make([]byte, 1+countWidth+len(first)*recordWidth+countWidth)
	data[0] = format
	if format == 4 {
		binary.BigEndian.PutUint32(data[1:], uint32(len(first)))
	} else {
		binary.BigEndian.PutUint16(data[1:], uint16(len(first)))
	}
	for index, glyph := range first {
		at := 1 + countWidth + index*recordWidth
		if format == 4 {
			binary.BigEndian.PutUint32(data[at:], glyph)
			binary.BigEndian.PutUint16(data[at+4:], selectors[index])
		} else {
			binary.BigEndian.PutUint16(data[at:], uint16(glyph))
			data[at+2] = byte(selectors[index])
		}
	}
	at := len(data) - countWidth
	if format == 4 {
		binary.BigEndian.PutUint32(data[at:], sentinel)
	} else {
		binary.BigEndian.PutUint16(data[at:], uint16(sentinel))
	}
	return data
}

func TestCFF2MetadataIndexEmptyOffsetSizesAndOriginalSpans(t *testing.T) {
	for width := 1; width <= 4; width++ {
		t.Run(string(rune('0'+width)), func(t *testing.T) {
			index := cff2MetadataTestIndex(width, []uint32{1, 1, 4, 4, 6}, []byte("abcde"))
			data := append([]byte{99, 98}, index...)
			data = append(data, 97)
			budget := cff2MetadataTestBudget(64, 5)
			spans, next, err := readCFF2Index(data, 100, 2, budget)
			begin := int64(100 + 2 + 5 + 5*width)
			want := []Span{{begin, 0}, {begin, 3}, {begin + 3, 0}, {begin + 3, 2}}
			if err != nil || !reflect.DeepEqual(spans, want) || next != 2+len(index) ||
				budget.operations != 5 || budget.face.reservation != 64 {
				t.Fatalf("spans=%v next=%d reservation=%d work=%d error=%v", spans, next,
					budget.face.reservation, budget.operations, err)
			}
		})
	}
	budget := cff2MetadataTestBudget(0, 1)
	spans, next, err := readCFF2Index([]byte{99, 0, 0, 0, 0, 98}, 100, 1, budget)
	if spans != nil || next != 5 || err != nil || budget.face.reservation != 0 || budget.operations != 1 {
		t.Fatalf("zero count must consume exactly four bytes: spans=%v next=%d error=%v", spans, next, err)
	}
}

func TestCFF2MetadataIndexUint32CountWithoutSubroutineCap(t *testing.T) {
	for width := 1; width <= 4; width++ {
		t.Run(string(rune('0'+width)), func(t *testing.T) {
			offsets := make([]uint32, 65537)
			for index := range offsets {
				offsets[index] = 1
			}
			data := cff2MetadataTestIndex(width, offsets, nil)
			budget := cff2MetadataTestBudget(65536*16, 65537)
			spans, next, err := readCFF2Index(data, 7000, 0, budget)
			if err != nil || len(spans) != 65536 || next != len(data) || budget.operations != 65537 ||
				budget.face.reservation != 65536*16 {
				t.Fatalf("large INDEX count=%d next=%d work=%d error=%v", len(spans), next, budget.operations, err)
			}
			want := Span{Offset: 7000 + int64(len(data))}
			for index, span := range spans {
				if span != want {
					t.Fatalf("large empty object %d: %v != %v", index, span, want)
				}
			}
		})
	}
}

func TestCFF2MetadataIndexUsesEveryOffsetOctet(t *testing.T) {
	for width := 1; width <= 4; width++ {
		t.Run(string(rune('0'+width)), func(t *testing.T) {
			last := uint32(0)
			for octet := 1; octet <= width; octet++ {
				last = last<<8 | uint32(octet)
			}
			payload := make([]byte, int(last)-1)
			data := cff2MetadataTestIndex(width, []uint32{1, last}, payload)
			budget := cff2MetadataTestBudget(16, 2)
			spans, next, err := readCFF2Index(data, 4321, 0, budget)
			want := []Span{{Offset: 4321 + int64(5+2*width), Length: int64(last) - 1}}
			if err != nil || !reflect.DeepEqual(spans, want) || next != len(data) {
				t.Fatalf("offsetSize %d spans=%v next=%d error=%v", width, spans, next, err)
			}
		})
	}
}

func TestCFF2MetadataIndexMalformedAndTableBoundary(t *testing.T) {
	valid := cff2MetadataTestIndex(1, []uint32{1, 2, 4}, []byte("abc"))
	backing := append(append([]byte(nil), valid...), []byte("outside-CFF2")...)
	rows := []struct {
		name   string
		data   []byte
		base   int64
		offset int
	}{
		{"negative-offset", valid, 0, -1},
		{"past-end-offset", valid, 0, len(valid)},
		{"short-count", []byte{0, 0, 0}, 0, 0},
		{"nonempty-no-offsetSize", []byte{0, 0, 0, 1}, 0, 0},
		{"offsetSize-zero", []byte{0, 0, 0, 1, 0, 1, 1}, 0, 0},
		{"offsetSize-five", []byte{0, 0, 0, 1, 5}, 0, 0},
		{"uint32-count-does-not-fit", []byte{255, 255, 255, 255, 4, 0, 0, 0, 1}, 0, 0},
		{"first-zero", cff2MetadataTestIndex(1, []uint32{0, 1}, nil), 0, 0},
		{"first-two", cff2MetadataTestIndex(1, []uint32{2, 2}, []byte{1}), 0, 0},
		{"last-zero", cff2MetadataTestIndex(1, []uint32{1, 0}, nil), 0, 0},
		{"reversed", cff2MetadataTestIndex(1, []uint32{1, 3, 2}, []byte{1, 2}), 0, 0},
		{"intermediate-zero", cff2MetadataTestIndex(1, []uint32{1, 0, 2}, []byte{1}), 0, 0},
		{"intermediate-outside", cff2MetadataTestIndex(1, []uint32{1, 5, 2}, []byte{1}), 0, 0},
		{"final-outside", cff2MetadataTestIndex(1, []uint32{1, 5}, []byte{1}), 0, 0},
		{"table-len-not-source-cap", backing[:len(valid)-1], 0, 0},
		{"negative-source-base", valid, -1, 0},
		{"source-offset-overflow", valid, int64(^uint64(0) >> 1), 0},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			spans, next, err := readCFF2Index(row.data, row.base, row.offset, cff2MetadataTestBudget(1<<20, 100))
			var typed *Error
			if spans != nil || next != 0 || !errors.Is(err, ErrMalformed) || !errors.As(err, &typed) ||
				typed.Path != "cff2.index" {
				t.Fatalf("malformed output spans=%v next=%d error=%v", spans, next, err)
			}
		})
	}
}

func TestCFF2MetadataFDSelectFormatsAndUint16Selectors(t *testing.T) {
	rows := []struct {
		name  string
		data  []byte
		fonts uint32
		want  []uint16
		work  int
	}{
		{"format-zero", []byte{0, 0, 255, 1, 255}, 256, []uint16{0, 255, 1, 255}, 5},
		{"format-three", cff2MetadataTestRanges(3, []uint32{0, 2}, []uint16{255, 0}, 4),
			256, []uint16{255, 255, 0, 0}, 7},
		{"format-four", cff2MetadataTestRanges(4, []uint32{0, 2}, []uint16{300, 65535}, 4),
			65536, []uint16{300, 300, 65535, 65535}, 7},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			data := append([]byte{9, 8}, row.data...)
			budget := cff2MetadataTestBudget(8, row.work)
			selectors, err := readCFF2FDSelect(data, 200, 2, 4, row.fonts, budget)
			if err != nil || !reflect.DeepEqual(selectors, row.want) || budget.face.reservation != 8 ||
				budget.operations != row.work {
				t.Fatalf("selectors=%v reservation=%d work=%d error=%v", selectors,
					budget.face.reservation, budget.operations, err)
			}
			for index := range data {
				data[index] = 0
			}
			if !reflect.DeepEqual(selectors, row.want) {
				t.Fatal("selectors alias caller data")
			}
		})
	}
}

func TestCFF2MetadataFDSelectMaxpMaximumAndUint32RangeCount(t *testing.T) {
	for _, format := range []byte{3, 4} {
		t.Run(string(rune('0'+format)), func(t *testing.T) {
			first, selectors := []uint32{0}, []uint16{255}
			fonts, work := uint32(256), 65537
			if format == 4 {
				first, selectors = make([]uint32, 65535), make([]uint16, 65535)
				for index := range first {
					first[index], selectors[index] = uint32(index), 65535
				}
				fonts, work = 65536, 131071
			}
			data := cff2MetadataTestRanges(format, first, selectors, 65535)
			budget := cff2MetadataTestBudget(65535*2, work)
			result, err := readCFF2FDSelect(data, 0, 0, 65535, fonts, budget)
			if err != nil || len(result) != 65535 || budget.face.reservation != 65535*2 ||
				budget.operations != work {
				t.Fatalf("maxp maximum count=%d work=%d error=%v", len(result), budget.operations, err)
			}
			for index, selector := range result {
				if selector != selectors[0] {
					t.Fatalf("selector %d=%d", index, selector)
				}
			}
		})
	}
}

func TestCFF2MetadataFDSelectMalformedAndTableBoundary(t *testing.T) {
	valid := cff2MetadataTestRanges(4, []uint32{0, 2}, []uint16{0, 1}, 4)
	backing := append(append([]byte(nil), valid...), []byte("outside-CFF2")...)
	rows := []struct {
		name   string
		data   []byte
		glyphs uint16
		fonts  uint32
		kind   error
	}{
		{"missing-format", nil, 4, 2, ErrMalformed},
		{"zero-glyphs", []byte{0}, 0, 2, ErrMalformed},
		{"zero-font-dicts", []byte{0, 0}, 1, 0, ErrMalformed},
		{"unknown-format", []byte{2}, 4, 2, ErrUnsupported},
		{"short-format-zero", []byte{0, 0, 1}, 4, 2, ErrMalformed},
		{"absent-format-zero-selector", []byte{0, 0, 2, 0, 1}, 4, 2, ErrMalformed},
		{"short-range-count-three", []byte{3, 0}, 4, 2, ErrMalformed},
		{"short-range-count-four", []byte{4, 0, 0, 0}, 4, 2, ErrMalformed},
		{"zero-range-count", cff2MetadataTestRanges(3, nil, nil, 4), 4, 2, ErrMalformed},
		{"uint32-range-count-does-not-fit", []byte{4, 255, 255, 255, 255, 0, 0, 0, 0}, 4, 2, ErrMalformed},
		{"too-many-ranges", cff2MetadataTestRanges(4, []uint32{0, 1, 2}, []uint16{0, 0, 0}, 2),
			2, 1, ErrMalformed},
		{"first-not-zero", cff2MetadataTestRanges(3, []uint32{1, 2}, []uint16{0, 1}, 4),
			4, 2, ErrMalformed},
		{"range-order-equal", cff2MetadataTestRanges(4, []uint32{0, 0}, []uint16{0, 1}, 4),
			4, 2, ErrMalformed},
		{"range-order-reversed", cff2MetadataTestRanges(3, []uint32{0, 3, 2}, []uint16{0, 1, 0}, 4),
			4, 2, ErrMalformed},
		{"sentinel-below", cff2MetadataTestRanges(4, []uint32{0, 2}, []uint16{0, 1}, 3),
			4, 2, ErrMalformed},
		{"sentinel-above", cff2MetadataTestRanges(3, []uint32{0, 2}, []uint16{0, 1}, 5),
			4, 2, ErrMalformed},
		{"format-four-sentinel-65536", cff2MetadataTestRanges(4, []uint32{0}, []uint16{0}, 65536),
			65535, 1, ErrMalformed},
		{"absent-format-three-selector", cff2MetadataTestRanges(3, []uint32{0}, []uint16{255}, 4),
			4, 255, ErrMalformed},
		{"absent-format-four-selector", cff2MetadataTestRanges(4, []uint32{0}, []uint16{65535}, 4),
			4, 65535, ErrMalformed},
		{"table-len-not-source-cap", backing[:len(valid)-1], 4, 2, ErrMalformed},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			result, err := readCFF2FDSelect(row.data, 90, 0, row.glyphs, row.fonts,
				cff2MetadataTestBudget(1<<20, 200000))
			var typed *Error
			if result != nil || !errors.Is(err, row.kind) || !errors.As(err, &typed) ||
				typed.Path != "cff2.fdSelect" {
				t.Fatalf("malformed selectors=%v error=%v", result, err)
			}
		})
	}
	for _, row := range []struct {
		name   string
		base   int64
		offset int
	}{
		{"negative-offset", 0, -1},
		{"past-end-offset", 0, len(valid)},
		{"negative-source-base", -1, 0},
		{"source-offset-overflow", int64(^uint64(0) >> 1), 0},
	} {
		t.Run(row.name, func(t *testing.T) {
			result, err := readCFF2FDSelect(valid, row.base, row.offset, 4, 2,
				cff2MetadataTestBudget(1024, 100))
			if result != nil || !errors.Is(err, ErrMalformed) {
				t.Fatalf("invalid source selectors=%v error=%v", result, err)
			}
		})
	}
}

func TestCFF2MetadataReservationAndWorkBoundariesSharedBudget(t *testing.T) {
	index := cff2MetadataTestIndex(1, []uint32{1, 2, 3}, []byte("ab"))
	selectData := cff2MetadataTestRanges(4, []uint32{0, 2}, []uint16{300, 0}, 4)
	for _, delta := range []int64{-1, 0, 1} {
		t.Run("bytes"+string(rune('1'+delta)), func(t *testing.T) {
			budget := cff2MetadataTestBudget(17+32+8+delta, 10)
			budget.face.reservation = 17
			spans, _, err := readCFF2Index(index, 0, 0, budget)
			if err != nil || len(spans) != 2 || budget.face.reservation != 49 {
				t.Fatalf("INDEX predecessor failed: %v", err)
			}
			result, err := readCFF2FDSelect(selectData, 0, 0, 4, 301, budget)
			if delta < 0 {
				if result != nil || !errors.Is(err, ErrLimit) || budget.face.reservation != 49 {
					t.Fatalf("under-budget output=%v reservation=%d error=%v", result, budget.face.reservation, err)
				}
				return
			}
			if err != nil || len(result) != 4 || budget.face.reservation != 57 || budget.operations != 10 {
				t.Fatalf("exact-budget output=%v reservation=%d work=%d error=%v", result,
					budget.face.reservation, budget.operations, err)
			}
		})
	}
	for _, delta := range []int{-1, 0, 1} {
		t.Run("work"+string(rune('1'+delta)), func(t *testing.T) {
			budget := cff2MetadataTestBudget(40, 10+delta)
			if _, _, err := readCFF2Index(index, 0, 0, budget); err != nil {
				t.Fatal(err)
			}
			result, err := readCFF2FDSelect(selectData, 0, 0, 4, 301, budget)
			if delta < 0 {
				if result != nil || !errors.Is(err, ErrLimit) || budget.operations != 9 {
					t.Fatalf("under-work output=%v work=%d error=%v", result, budget.operations, err)
				}
				return
			}
			if err != nil || len(result) != 4 || budget.operations != 10 {
				t.Fatalf("exact-work output=%v work=%d error=%v", result, budget.operations, err)
			}
		})
	}
	for _, delta := range []int64{-1, 0, 1} {
		budget := cff2MetadataTestBudget(32+delta, 3)
		spans, _, err := readCFF2Index(index, 0, 0, budget)
		if delta < 0 && (spans != nil || !errors.Is(err, ErrLimit) || budget.face.reservation != 0) ||
			delta >= 0 && (err != nil || len(spans) != 2 || budget.face.reservation != 32) {
			t.Fatalf("INDEX byte boundary delta=%d spans=%v reservation=%d error=%v", delta,
				spans, budget.face.reservation, err)
		}
	}
	for _, delta := range []int{-1, 0, 1} {
		budget := cff2MetadataTestBudget(32, 3+delta)
		spans, _, err := readCFF2Index(index, 0, 0, budget)
		if delta < 0 && (spans != nil || !errors.Is(err, ErrLimit) || budget.operations != 2) ||
			delta >= 0 && (err != nil || len(spans) != 2 || budget.operations != 3) {
			t.Fatalf("INDEX work boundary delta=%d spans=%v work=%d error=%v", delta,
				spans, budget.operations, err)
		}
	}
}

type cff2MetadataCancelContext struct {
	calls, cancelAt int
	cause           error
}

func (c *cff2MetadataCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *cff2MetadataCancelContext) Done() <-chan struct{}       { return nil }
func (c *cff2MetadataCancelContext) Value(key any) any           { return nil }
func (c *cff2MetadataCancelContext) Err() error {
	c.calls++
	if c.cancelAt > 0 && c.calls >= c.cancelAt {
		return c.cause
	}
	return nil
}

func TestCFF2MetadataCountedCancellationReturnsNoPartialOutputs(t *testing.T) {
	index := cff2MetadataTestIndex(1, []uint32{1, 1, 2, 3}, []byte("ab"))
	fdData := cff2MetadataTestRanges(4, []uint32{0, 2}, []uint16{300, 0}, 4)
	for _, kind := range []string{"index", "fdSelect"} {
		t.Run(kind, func(t *testing.T) {
			baseline := &cff2MetadataCancelContext{}
			budget := cff2MetadataTestBudget(1024, 100)
			budget.ctx = baseline
			if kind == "index" {
				if _, _, err := readCFF2Index(index, 0, 0, budget); err != nil {
					t.Fatal(err)
				}
			} else if _, err := readCFF2FDSelect(fdData, 0, 0, 4, 301, budget); err != nil {
				t.Fatal(err)
			}
			for _, at := range []int{1, baseline.calls / 2, baseline.calls} {
				cause := errors.New("owned CFF2 metadata cancellation cause")
				ctx := &cff2MetadataCancelContext{cancelAt: at, cause: cause}
				budget := cff2MetadataTestBudget(1024, 100)
				budget.ctx = ctx
				if kind == "index" {
					spans, next, err := readCFF2Index(index, 0, 0, budget)
					if spans != nil || next != 0 || !errors.Is(err, cause) || ctx.calls != at {
						t.Fatalf("INDEX cancel at=%d calls=%d spans=%v next=%d error=%v", at, ctx.calls, spans, next, err)
					}
				} else {
					selectors, err := readCFF2FDSelect(fdData, 0, 0, 4, 301, budget)
					if selectors != nil || !errors.Is(err, cause) || ctx.calls != at {
						t.Fatalf("FDSelect cancel at=%d calls=%d output=%v error=%v", at, ctx.calls, selectors, err)
					}
				}
			}
		})
	}
}

func TestCFF2MetadataEmptyIndexFinalCancellationClearsNextOffset(t *testing.T) {
	cause := errors.New("owned empty INDEX final cancellation")
	ctx := &cff2MetadataCancelContext{cancelAt: 3, cause: cause}
	budget := cff2MetadataTestBudget(0, 1)
	budget.ctx = ctx
	spans, next, err := readCFF2Index([]byte{0, 0, 0, 0}, 300, 0, budget)
	if spans != nil || next != 0 || !errors.Is(err, cause) || ctx.calls != 3 {
		t.Fatalf("empty INDEX final cancellation: spans=%v next=%d calls=%d error=%v", spans, next, ctx.calls, err)
	}
}
