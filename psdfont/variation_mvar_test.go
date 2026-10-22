package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"testing"
	"time"
	"unsafe"
)

type mvarLiteralRecord struct {
	tag          string
	outer, inner uint16
	delta        int16
}

func mvarLiteralTable(records []mvarLiteralRecord, size uint16, constant, nullRow bool) []byte {
	data := make([]byte, 12+len(records)*int(size))
	binary.BigEndian.PutUint16(data, 1)
	binary.BigEndian.PutUint16(data[6:], size)
	binary.BigEndian.PutUint16(data[8:], uint16(len(records)))
	if len(records) == 0 {
		return data
	}
	binary.BigEndian.PutUint16(data[10:], uint16(len(data)))
	for index, record := range records {
		position := 12 + index*int(size)
		copy(data[position:], record.tag)
		binary.BigEndian.PutUint16(data[position+4:], record.outer)
		binary.BigEndian.PutUint16(data[position+6:], record.inner)
		for extension := 8; extension < int(size); extension++ {
			data[position+extension] = byte(0xa0 + extension)
		}
	}
	store := make([]byte, 30+len(records)*2)
	binary.BigEndian.PutUint16(store, 1)
	binary.BigEndian.PutUint32(store[2:], 12)
	binary.BigEndian.PutUint16(store[6:], 1)
	if !nullRow {
		binary.BigEndian.PutUint32(store[8:], 22)
	}
	binary.BigEndian.PutUint16(store[12:], 1)
	binary.BigEndian.PutUint16(store[14:], 1)
	if !constant {
		binary.BigEndian.PutUint16(store[18:], 16384)
		binary.BigEndian.PutUint16(store[20:], 16384)
	}
	binary.BigEndian.PutUint16(store[22:], uint16(len(records)))
	binary.BigEndian.PutUint16(store[24:], 1)
	binary.BigEndian.PutUint16(store[26:], 1)
	for index, record := range records {
		binary.BigEndian.PutUint16(store[30+index*2:], uint16(record.delta))
	}
	return append(data, store...)
}

func mvarLiteralFace(table []byte, normalized int16, targets map[string][]byte) *Face {
	face := &Face{raw: make([]byte, 13), tables: map[string]Span{}, Ascender: 799, Descender: -211, LineGap: 31,
		variation: &fontVariationData{axes: make([]variationAxisRecord, 1), normalized: []int16{normalized},
			copyLimit: 1000000, copyWork: 1000000}}
	face.tables["MVAR"] = Span{Offset: int64(len(face.raw)), Length: int64(len(table))}
	face.raw = append(face.raw, table...)
	tags := make([]string, 0, len(targets))
	for tag := range targets {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	for _, tag := range tags {
		data := targets[tag]
		face.tables[tag] = Span{Offset: int64(len(face.raw)), Length: int64(len(data))}
		face.raw = append(face.raw, data...)
	}
	return face
}

func mvarLiteralBudget(face *Face, bytes int64, work int) *fontMetadataBudget {
	return &fontMetadataBudget{face: face, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: bytes, MaxCFFOperations: work}}
}

func TestMVARLiteralRecordSourceUnknownAndPreciseDefault(t *testing.T) {
	records := []mvarLiteralRecord{{tag: "ABCD", inner: 0, delta: 3}, {tag: "hasc", inner: 1, delta: -1}}
	data := mvarLiteralTable(records, 12, true, false)
	os2 := make([]byte, 96)
	binary.BigEndian.PutUint16(os2, 2)
	binary.BigEndian.PutUint16(os2[68:], 700)
	face := mvarLiteralFace(data, 0, map[string][]byte{"OS/2": os2})
	budget := mvarLiteralBudget(face, 10000, 1000)
	if err := face.readMetricVariations(budget); err != nil {
		t.Fatal(err)
	}
	metrics, err := face.VariationMetrics(context.Background())
	if err != nil || len(metrics) != 2 {
		t.Fatalf("owned MVAR copy: %+v %v", metrics, err)
	}
	if metrics[0].Tag != "ABCD" || metrics[0].Delta != 3 || metrics[0].HasRawValue ||
		metrics[0].SourceTable != "" || metrics[0].EffectiveValue != 0 ||
		metrics[0].Source != (Span{Offset: 25, Length: 12}) {
		t.Fatalf("private tag or constant region was lost: %+v", metrics[0])
	}
	metric := metrics[1]
	if metric.Tag != "hasc" || metric.Delta != -1 || metric.RawValue != 700 || metric.EffectiveValue != 699 ||
		!metric.HasRawValue || metric.SourceTable != "OS/2" || metric.SourceField != "sTypoAscender" ||
		metric.Source != (Span{Offset: 37, Length: 12}) || metric.ValueRecordSize != 12 ||
		metric.DeltaSetOuterIndex != 0 || metric.DeltaSetInnerIndex != 1 ||
		metric.RawSource != (Span{Offset: face.tables["OS/2"].Offset + 68, Length: 2}) {
		t.Fatalf("typed/source MVAR metric differs: %+v", metric)
	}
	if face.raw[metric.Source.Offset+8] != 0xa8 || face.raw[metric.Source.Offset+11] != 0xab {
		t.Fatal("extended MVAR record was not retained in source span")
	}
	if face.Ascender != 799 || face.Descender != -211 || face.LineGap != 31 {
		t.Fatal("OS/2 MVAR deltas altered existing hhea leading values")
	}
	metrics[1].RawValue, metrics[1].Tag = 1, "xxxx"
	again, err := face.VariationMetrics(context.Background())
	if err != nil || again[1].RawValue != 700 || again[1].Tag != "hasc" {
		t.Fatal("caller altered decoded MVAR records")
	}
	face = mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: "ABCD", delta: 1},
		{tag: "hasc", inner: 1, delta: -1}}, 8, false, false),
		8192, map[string][]byte{"OS/2": os2})
	if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil {
		t.Fatal(err)
	}
	if face.mvar.metrics[0].Delta != 0.5 {
		t.Fatal("positive half delta rounded prematurely")
	}
	if actual := face.mvar.metrics[1]; actual.Delta != -0.5 || actual.EffectiveValue != 699.5 {
		t.Fatalf("negative half delta rounded prematurely: %+v", actual)
	}
}

func TestMVARLiteralAllThirtyEightTargets(t *testing.T) {
	type literalTarget struct {
		tag, table, field string
		offset            int
		unsigned          bool
	}
	expected := []literalTarget{
		{"hasc", "OS/2", "sTypoAscender", 68, false}, {"hdsc", "OS/2", "sTypoDescender", 70, false},
		{"hlgp", "OS/2", "sTypoLineGap", 72, false}, {"hcla", "OS/2", "usWinAscent", 74, true},
		{"hcld", "OS/2", "usWinDescent", 76, true}, {"xhgt", "OS/2", "sxHeight", 86, false},
		{"cpht", "OS/2", "sCapHeight", 88, false}, {"sbxs", "OS/2", "ySubscriptXSize", 10, false},
		{"sbys", "OS/2", "ySubscriptYSize", 12, false}, {"sbxo", "OS/2", "ySubscriptXOffset", 14, false},
		{"sbyo", "OS/2", "ySubscriptYOffset", 16, false}, {"spxs", "OS/2", "ySuperscriptXSize", 18, false},
		{"spys", "OS/2", "ySuperscriptYSize", 20, false}, {"spxo", "OS/2", "ySuperscriptXOffset", 22, false},
		{"spyo", "OS/2", "ySuperscriptYOffset", 24, false}, {"strs", "OS/2", "yStrikeoutSize", 26, false},
		{"stro", "OS/2", "yStrikeoutPosition", 28, false}, {"hcrs", "hhea", "caretSlopeRise", 18, false},
		{"hcrn", "hhea", "caretSlopeRun", 20, false}, {"hcof", "hhea", "caretOffset", 22, false},
		{"vasc", "vhea", "ascent", 4, false}, {"vdsc", "vhea", "descent", 6, false},
		{"vlgp", "vhea", "lineGap", 8, false}, {"vcrs", "vhea", "caretSlopeRise", 18, false},
		{"vcrn", "vhea", "caretSlopeRun", 20, false}, {"vcof", "vhea", "caretOffset", 22, false},
		{"undo", "post", "underlinePosition", 8, false}, {"unds", "post", "underlineThickness", 10, false},
		{"gsp0", "gasp", "gaspRange[0].rangeMaxPPEM", 4, true},
		{"gsp1", "gasp", "gaspRange[1].rangeMaxPPEM", 8, true},
		{"gsp2", "gasp", "gaspRange[2].rangeMaxPPEM", 12, true},
		{"gsp3", "gasp", "gaspRange[3].rangeMaxPPEM", 16, true},
		{"gsp4", "gasp", "gaspRange[4].rangeMaxPPEM", 20, true},
		{"gsp5", "gasp", "gaspRange[5].rangeMaxPPEM", 24, true},
		{"gsp6", "gasp", "gaspRange[6].rangeMaxPPEM", 28, true},
		{"gsp7", "gasp", "gaspRange[7].rangeMaxPPEM", 32, true},
		{"gsp8", "gasp", "gaspRange[8].rangeMaxPPEM", 36, true},
		{"gsp9", "gasp", "gaspRange[9].rangeMaxPPEM", 40, true},
	}
	slices.SortFunc(expected, func(left, right literalTarget) int {
		if left.tag < right.tag {
			return -1
		}
		if left.tag > right.tag {
			return 1
		}
		return 0
	})
	targets := map[string][]byte{"OS/2": make([]byte, 96), "hhea": make([]byte, 36),
		"vhea": make([]byte, 36), "post": make([]byte, 32), "gasp": make([]byte, 48)}
	binary.BigEndian.PutUint16(targets["OS/2"], 2)
	binary.BigEndian.PutUint32(targets["vhea"], 0x10000)
	binary.BigEndian.PutUint16(targets["gasp"][2:], 11)
	binary.BigEndian.PutUint16(targets["gasp"][44:], 65535)
	records := make([]mvarLiteralRecord, len(expected))
	for index, target := range expected {
		rawValue := int16(-300 - index)
		if target.unsigned {
			rawValue = int16(-1000 - index)
		}
		binary.BigEndian.PutUint16(targets[target.table][target.offset:], uint16(rawValue))
		records[index] = mvarLiteralRecord{tag: target.tag, inner: uint16(index), delta: int16(index + 1)}
	}
	face := mvarLiteralFace(mvarLiteralTable(records, 8, true, false), 0, targets)
	if err := face.readMetricVariations(mvarLiteralBudget(face, 100000, 1000)); err != nil {
		t.Fatal(err)
	}
	if len(face.mvar.metrics) != 38 {
		t.Fatal("registered target count differs from primary registry")
	}
	for index, target := range expected {
		t.Run(target.tag, func(t *testing.T) {
			actual := face.mvar.metrics[index]
			rawValue := int64(-300 - index)
			if target.unsigned {
				rawValue = int64(65536 - 1000 - index)
			}
			if actual.Tag != target.tag || actual.SourceTable != target.table || actual.SourceField != target.field ||
				!actual.HasRawValue || actual.RawValue != rawValue || actual.Delta != float64(index+1) ||
				actual.EffectiveValue != float64(rawValue)+float64(index+1) ||
				actual.RawSource != (Span{Offset: face.tables[target.table].Offset + int64(target.offset), Length: 2}) {
				t.Fatalf("registry/source/raw/effective target differs: %+v", actual)
			}
		})
	}
}

func TestMVARLiteralMissingOldOS2AndVersionedVhea(t *testing.T) {
	for _, tag := range []string{"hasc", "cpht", "vasc", "gsp0"} {
		t.Run("missing-"+tag, func(t *testing.T) {
			face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: tag, delta: 3}}, 8, true, false), 0, nil)
			if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil {
				t.Fatal(err)
			}
			actual := face.mvar.metrics[0]
			if actual.Delta != 3 || actual.HasRawValue || actual.RawValue != 0 || actual.EffectiveValue != 0 ||
				actual.SourceTable == "" || actual.SourceField == "" || actual.RawSource != (Span{}) {
				t.Fatalf("absent target acquired invented raw/effective values: %+v", actual)
			}
		})
	}
	for _, version := range []uint16{0, 1} {
		t.Run("old-os2-"+string(rune('0'+version)), func(t *testing.T) {
			os2 := make([]byte, 96)
			binary.BigEndian.PutUint16(os2, version)
			binary.BigEndian.PutUint16(os2[86:], 777)
			binary.BigEndian.PutUint16(os2[88:], 888)
			face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{
				{tag: "cpht", inner: 0, delta: 1}, {tag: "xhgt", inner: 1, delta: 2}}, 8, true, false),
				0, map[string][]byte{"OS/2": os2})
			if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil {
				t.Fatal(err)
			}
			for _, metric := range face.mvar.metrics {
				if metric.HasRawValue || metric.RawSource != (Span{}) || metric.EffectiveValue != 0 {
					t.Fatal("old OS/2 trailing bytes interpreted as missing version-two fields")
				}
			}
		})
	}
	vhea := make([]byte, 36)
	binary.BigEndian.PutUint32(vhea, 0x11000)
	binary.BigEndian.PutUint16(vhea[4:], 500)
	face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: "vasc", delta: 1}}, 8, true, false),
		0, map[string][]byte{"vhea": vhea})
	if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil ||
		face.mvar.metrics[0].SourceField != "vertTypoAscender" {
		t.Fatalf("vhea1.1 target name: %v", err)
	}
}

func TestMVARLiteralNullRowsNoVariationAndCompatibleMinor(t *testing.T) {
	for _, record := range []mvarLiteralRecord{{tag: "ABCD", outer: 0, inner: 65535},
		{tag: "ABCD", outer: 65535, inner: 65535}} {
		face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{record}, 12, true, true), 0, nil)
		binary.BigEndian.PutUint16(face.raw[face.tables["MVAR"].Offset+2:], 2)
		if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil ||
			face.mvar.metrics[0].Delta != 0 || face.mvar.metrics[0].ValueRecordSize != 12 {
			t.Fatalf("NULL/no-variation or minor extension: %v", err)
		}
	}
	face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: "ABCD", inner: 0, delta: 1},
		{tag: "ABCD", inner: 1, delta: 2}}, 8, true, false), 0, nil)
	if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil ||
		len(face.mvar.metrics) != 2 || face.mvar.metrics[0].Delta != 1 || face.mvar.metrics[1].Delta != 2 {
		t.Fatalf("nondecreasing duplicate records were merged or lost: %v", err)
	}
	for _, size := range []uint16{1, 4, 8, 12} {
		face := mvarLiteralFace(mvarLiteralTable(nil, size, true, false), 0, nil)
		if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil || face.mvar == nil ||
			len(face.mvar.metrics) != 0 {
			t.Fatalf("empty MVAR size %d: %v", size, err)
		}
	}
}

func TestMVARLiteralMalformedNoPartial(t *testing.T) {
	valid := mvarLiteralTable([]mvarLiteralRecord{{tag: "ABCD", inner: 0, delta: 1}}, 8, true, false)
	cases := []struct {
		name    string
		mutate  func([]byte) []byte
		failure error
	}{
		{"header", func(data []byte) []byte { return data[:11] }, ErrMalformed},
		{"major", func(data []byte) []byte { binary.BigEndian.PutUint16(data, 2); return data }, ErrUnsupported},
		{"reserved", func(data []byte) []byte { data[5] = 1; return data }, ErrMalformed},
		{"zero-size", func(data []byte) []byte { data[7] = 0; return data }, ErrMalformed},
		{"short-size", func(data []byte) []byte { data[7] = 7; return data }, ErrMalformed},
		{"overflow-records", func(data []byte) []byte { data[8], data[9] = 255, 255; return data }, ErrMalformed},
		{"null-required-store", func(data []byte) []byte { data[10], data[11] = 0, 0; return data }, ErrMalformed},
		{"outside-store", func(data []byte) []byte { data[10], data[11] = 255, 255; return data }, ErrMalformed},
		{"bad-outer", func(data []byte) []byte { data[16], data[17] = 0, 1; return data }, ErrMalformed},
		{"bad-inner", func(data []byte) []byte { data[18], data[19] = 0, 1; return data }, ErrMalformed},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			data := testCase.mutate(slices.Clone(valid))
			face := mvarLiteralFace(data, 0, nil)
			err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000))
			if !errors.Is(err, testCase.failure) || face.mvar != nil {
				t.Fatalf("invalid MVAR published partial record array: %v", err)
			}
		})
	}
	for _, table := range [][]byte{
		mvarLiteralTable([]mvarLiteralRecord{{tag: "zzzz"}, {tag: "ABCD"}}, 8, true, false),
		mvarLiteralTable(nil, 8, true, false),
	} {
		if len(table) == 12 {
			binary.BigEndian.PutUint16(table[10:], 12)
		}
		face := mvarLiteralFace(table, 0, nil)
		readErr := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000))
		if !errors.Is(readErr, ErrMalformed) || face.mvar != nil {
			t.Fatalf("record order/empty store disagreement accepted: %v", readErr)
		}
	}
	face := mvarLiteralFace(valid, 0, nil)
	face.variation = nil
	if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); !errors.Is(err, ErrMalformed) {
		t.Fatal("MVAR without required fvar accepted")
	}
	for _, target := range []struct{ tag, table string }{{"hasc", "OS/2"}, {"vasc", "vhea"}, {"undo", "post"},
		{"gsp0", "gasp"}} {
		t.Run("truncated-"+target.table, func(t *testing.T) {
			face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: target.tag}}, 8, true, false),
				0, map[string][]byte{target.table: {0}})
			readErr := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000))
			if !errors.Is(readErr, ErrMalformed) || face.mvar != nil {
				t.Fatalf("truncated mapped field accepted: %v", readErr)
			}
		})
	}
	for _, tag := range []string{"gsp0", "gsp9"} {
		gasp := []byte{0, 1, 0, 1, 255, 255, 0, 3}
		face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: tag}}, 8, true, false),
			0, map[string][]byte{"gasp": gasp})
		if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); !errors.Is(err, ErrMalformed) {
			t.Fatal("final or absent gasp range accepted")
		}
	}
	gasp := []byte{0, 1, 0, 2, 0, 8, 0, 3, 255, 255, 0, 3}
	face = mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: "gsp0"}, {tag: "gsp0"}}, 8, true, false),
		0, map[string][]byte{"gasp": gasp})
	if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); !errors.Is(err, ErrMalformed) {
		t.Fatal("gasp record count greater than numRanges minus one accepted")
	}
}

type mvarCancelAtContext struct {
	calls, cancelAt int
}

func (c *mvarCancelAtContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *mvarCancelAtContext) Done() <-chan struct{}       { return nil }
func (c *mvarCancelAtContext) Value(key any) any           { return nil }
func (c *mvarCancelAtContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestMVARLiteralSharedBudgetAndCancellation(t *testing.T) {
	table := mvarLiteralTable([]mvarLiteralRecord{{tag: "ABCD", inner: 0, delta: 1},
		{tag: "hasc", inner: 1, delta: 2}}, 12, false, false)
	face := mvarLiteralFace(table, 8192, nil)
	budget := mvarLiteralBudget(face, 10000, 1000)
	budget.operations = 7
	if err := face.readMetricVariations(budget); err != nil {
		t.Fatal(err)
	}
	bytes, work := face.reservation, budget.operations
	if work <= 7 || bytes <= 0 {
		t.Fatal("MVAR failed to consume shared caller workspace")
	}
	for _, extra := range []int64{-1, 0, 1} {
		for _, workExtra := range []int{-1, 0, 1} {
			candidate := mvarLiteralFace(table, 8192, nil)
			limits := mvarLiteralBudget(candidate, bytes+extra, work+workExtra)
			limits.operations = 7
			err := candidate.readMetricVariations(limits)
			if extra < 0 || workExtra < 0 {
				if !errors.Is(err, ErrLimit) || candidate.mvar != nil {
					t.Fatalf("minus-one bytes/work produced partial MVAR: %v", err)
				}
			} else if err != nil || candidate.mvar == nil || limits.operations != work {
				t.Fatalf("exact/+1 shared bytes/work failed: %v", err)
			}
		}
	}
	probe := mvarLiteralFace(table, 8192, nil)
	ctx := &mvarCancelAtContext{cancelAt: math.MaxInt}
	probeBudget := mvarLiteralBudget(probe, 10000, 1000)
	probeBudget.ctx = ctx
	if err := probe.readMetricVariations(probeBudget); err != nil {
		t.Fatal(err)
	}
	for cancelAt := 1; cancelAt <= ctx.calls; cancelAt++ {
		candidate := mvarLiteralFace(table, 8192, nil)
		limits := mvarLiteralBudget(candidate, 10000, 1000)
		limits.ctx = &mvarCancelAtContext{cancelAt: cancelAt}
		if err := candidate.readMetricVariations(limits); !errors.Is(err, context.Canceled) || candidate.mvar != nil {
			t.Fatalf("cancellation callback %d returned partial success: %v", cancelAt, err)
		}
	}
}

func TestMVARLiteralCopyLimitsCancellationAndNil(t *testing.T) {
	face := mvarLiteralFace(mvarLiteralTable([]mvarLiteralRecord{{tag: "ABCD", delta: 1}}, 8, true, false), 0, nil)
	if err := face.readMetricVariations(mvarLiteralBudget(face, 10000, 1000)); err != nil {
		t.Fatal(err)
	}
	fixed := int64(unsafe.Sizeof(Face{})) + int64(unsafe.Sizeof(fontMetadataBudget{}))
	for _, extra := range []int64{-1, 0, 1} {
		face.variation.copyLimit = fixed + int64(unsafe.Sizeof(VariationMetric{})) + extra
		actual, err := face.VariationMetrics(context.Background())
		if extra < 0 {
			if actual != nil || !errors.Is(err, ErrLimit) {
				t.Fatal("copy bytes minus one returned partial")
			}
		} else if err != nil || len(actual) != 1 {
			t.Fatalf("copy bytes exact/+1 failed: %v", err)
		}
	}
	face.variation.copyLimit = 10000
	face.variation.copyWork = 1
	if actual, err := face.VariationMetrics(nil); err != nil || len(actual) != 1 {
		t.Fatalf("one work step/nil context copy failed: %v", err)
	}
	face.variation.copyWork = 0
	if actual, err := face.VariationMetrics(context.Background()); actual != nil || !errors.Is(err, ErrLimit) {
		t.Fatal("copy work zero did not return nil/limit")
	}
	face.variation.copyWork = 1000
	probeContext := &mvarCancelAtContext{cancelAt: math.MaxInt}
	if _, err := face.VariationMetrics(probeContext); err != nil {
		t.Fatal(err)
	}
	for cancelAt := 1; cancelAt <= probeContext.calls; cancelAt++ {
		actual, err := face.VariationMetrics(&mvarCancelAtContext{cancelAt: cancelAt})
		if actual != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("copy callback %d returned partial success: %v", cancelAt, err)
		}
	}
	var absent *Face
	if actual, err := absent.VariationMetrics(context.Background()); actual != nil || !errors.Is(err, ErrMalformed) {
		t.Fatal("nil face metrics did not fail explicitly")
	}
	if actual, err := (&Face{}).VariationMetrics(context.Background()); err != nil || actual != nil {
		t.Fatal("static face acquired MVAR records")
	}
}
