package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"testing"
)

func TestTIFFPrivateMaskRealHeaderBeforeParameters(t *testing.T) {
	for _, real := range []bool{false, true} {
		t.Run(fmt.Sprintf("real%t", real), func(t *testing.T) {
			original := maskParametersPayload(real)
			little := bytes.Clone(original)
			for _, offset := range []int{0, 4, 8, 12} {
				binary.LittleEndian.PutUint32(little[offset:], binary.BigEndian.Uint32(original[offset:]))
			}
			parameters := 18
			if real {
				parameters = 36
				for _, offset := range []int{20, 24, 28, 32} {
					binary.LittleEndian.PutUint32(little[offset:], binary.BigEndian.Uint32(original[offset:]))
				}
			}
			for _, offset := range []int{parameters + 2, parameters + 11} {
				binary.LittleEndian.PutUint64(little[offset:], binary.BigEndian.Uint64(original[offset:]))
			}
			builder := newMaskNormalizationBuilder(little)
			bounds, realBounds, hasReal, err := builder.normalizeMask(builder.span, real)
			if err != nil || bounds != image.Rect(2, 3, 4, 5) || hasReal != real ||
				real && realBounds != image.Rect(-2, 1, 0, 3) {
				t.Fatalf("private mask header: %v / %v / %t, %v", bounds, realBounds, hasReal, err)
			}
			reader, err := builder.orderedReader()
			if err != nil {
				t.Fatal(err)
			}
			normalized := make([]byte, len(little))
			if _, err := reader.ReadAt(normalized, 0); err != nil || !bytes.Equal(normalized, original) {
				t.Fatalf("private mask parameter bytes were not normalized: %x, %v", normalized, err)
			}
			cuts := []int{len(little) - 1}
			if real {
				cuts = append(cuts, 0, 18, 35)
			}
			for _, cut := range cuts {
				truncated := newMaskNormalizationBuilder(little[:cut])
				if _, _, _, err := truncated.normalizeMask(truncated.span, real); err == nil {
					t.Fatalf("private mask accepted truncation at byte %d", cut)
				}
			}
		})
	}
}

func newMaskNormalizationBuilder(data []byte) *tiffPrivateBuilder {
	limits := DefaultLimits()
	parent := &Document{reader: bytes.NewReader(data), size: int64(len(data)), limits: limits}
	return &tiffPrivateBuilder{ctx: context.Background(), parent: parent, limits: limits,
		span: Span{Length: int64(len(data))}}
}
