package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"
)

func variationTestFvar() []byte {
	data := make([]byte, 16+20+8)
	binary.BigEndian.PutUint16(data, 1)
	binary.BigEndian.PutUint16(data[4:], 16)
	binary.BigEndian.PutUint16(data[6:], 2)
	binary.BigEndian.PutUint16(data[8:], 1)
	binary.BigEndian.PutUint16(data[10:], 20)
	binary.BigEndian.PutUint16(data[12:], 1)
	binary.BigEndian.PutUint16(data[14:], 8)
	copy(data[16:], "wght")
	binary.BigEndian.PutUint32(data[20:], 100<<16)
	binary.BigEndian.PutUint32(data[24:], 400<<16)
	binary.BigEndian.PutUint32(data[28:], 900<<16)
	binary.BigEndian.PutUint16(data[34:], 256)
	binary.BigEndian.PutUint16(data[36:], 257)
	binary.BigEndian.PutUint32(data[40:], 650<<16)
	return data
}

func TestVariationMetadataFvarOwnedBoundsAndSharedWork(t *testing.T) {
	data := variationTestFvar()
	face := &Face{raw: data, tables: map[string]Span{"fvar": {Length: int64(len(data))}}}
	budget := &fontMetadataBudget{face: face, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: 10000, MaxCFFOperations: 3}}
	result, err := readFvarMetadata(budget.ctx, face, budget)
	if err != nil || result == nil || len(result.axes) != 1 || len(result.named) != 1 || budget.operations != 3 {
		t.Fatalf("actual fvar rows/work: %v", err)
	}
	if result.axes[0].axis.Tag != "wght" || result.axes[0].axis.Default != 400 ||
		result.named[0].Coordinates[0] != 650 || result.axes[0].axis.Source != (Span{Offset: 16, Length: 20}) {
		t.Fatal("typed fvar metadata differs from literal input")
	}
	if err := budget.step("nextFamily", 0); !errors.Is(err, ErrLimit) {
		t.Fatal("metadata work reset instead of sharing total")
	}
	for index := range data {
		data[index] = 0
	}
	if result.axes[0].axis.Tag != "wght" || result.named[0].Coordinates[0] != 650 {
		t.Fatal("returned metadata borrowed caller values")
	}
	for _, length := range []int{0, 15, 16, 35, 43} {
		data := variationTestFvar()[:length]
		face := &Face{raw: data, tables: map[string]Span{"fvar": {Length: int64(length)}}}
		budget := &fontMetadataBudget{face: face, ctx: context.Background(),
			limits: DecodeOptions{MaxBytes: 10000, MaxCFFOperations: 100}}
		if partial, err := readFvarMetadata(budget.ctx, face, budget); partial != nil || !errors.Is(err, ErrMalformed) {
			t.Fatalf("truncated fvar accepted length%d: %v", length, err)
		}
	}
}

func TestVariationMetadataItemStoreLongWordsAndLimits(t *testing.T) {
	// One axis, one region, one row and a signed 32-bit delta.
	data := make([]byte, 34)
	binary.BigEndian.PutUint16(data, 1)
	binary.BigEndian.PutUint32(data[2:], 12)
	binary.BigEndian.PutUint16(data[6:], 1)
	binary.BigEndian.PutUint32(data[8:], 22)
	binary.BigEndian.PutUint16(data[12:], 1)
	binary.BigEndian.PutUint16(data[14:], 1)
	binary.BigEndian.PutUint16(data[18:], 16384)
	binary.BigEndian.PutUint16(data[20:], 16384)
	binary.BigEndian.PutUint16(data[22:], 1)
	binary.BigEndian.PutUint16(data[24:], 0x8001)
	binary.BigEndian.PutUint16(data[26:], 1)
	binary.BigEndian.PutUint32(data[30:], 100000)
	face := &Face{}
	budget := &fontMetadataBudget{face: face, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: 10000, MaxCFFOperations: 4}}
	store, err := readItemVariationStore(data, 73, 0, 1, budget)
	if err != nil || store == nil || store.source.Offset != 73 || len(store.data) != 1 ||
		!store.data[0].longWords || store.data[0].rowSize != 4 || u32(store.data[0].deltaBytes, 0) != 100000 {
		t.Fatalf("long-word item store: %v", err)
	}
	for _, extra := range []int64{-1, 0, 1} {
		candidateFace := &Face{}
		candidate := &fontMetadataBudget{face: candidateFace, ctx: context.Background(),
			limits: DecodeOptions{MaxBytes: face.reservation + extra, MaxCFFOperations: 4}}
		actual, err := readItemVariationStore(data, 73, 0, 1, candidate)
		if extra < 0 {
			if actual != nil || !errors.Is(err, ErrLimit) {
				t.Fatal("store bytes minus1 not nil/ErrLimit")
			}
		} else if actual == nil || err != nil {
			t.Fatalf("store exact/+1 budget:%v", err)
		}
	}
	broken := &fontMetadataBudget{face: &Face{reservation: 11}, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: 10}}
	if err := broken.array(1, 8, "negative.remaining", 0); !errors.Is(err, ErrLimit) {
		t.Fatal("negative remaining bytes were converted to unsigned availability")
	}
}

func TestVariationMetadataNullDataAndReservedRegionBit(t *testing.T) {
	data := make([]byte, 16)
	binary.BigEndian.PutUint16(data, 1)
	binary.BigEndian.PutUint32(data[2:], 12)
	binary.BigEndian.PutUint16(data[6:], 1)
	budget := &fontMetadataBudget{face: &Face{}, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: 10000, MaxCFFOperations: 2}}
	store, err := readItemVariationStore(data, 61, 0, 0, budget)
	if err != nil || store == nil || len(store.data) != 1 || store.data[0].present ||
		store.data[0].source != (Span{Offset: 69, Length: 4}) || budget.operations != 2 {
		t.Fatalf("NULL item offset read a store header as data: %v", err)
	}
	binary.BigEndian.PutUint16(data[14:], 0x8000)
	budget = &fontMetadataBudget{face: &Face{}, ctx: context.Background(),
		limits: DecodeOptions{MaxBytes: 10000, MaxCFFOperations: 2}}
	if partial, err := readItemVariationStore(data, 61, 0, 0, budget); partial != nil || !errors.Is(err, ErrMalformed) {
		t.Fatal("reserved region-count bit accepted")
	}
}
