package psdfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestTIFFPrivateReaderEveryPartialUnitMatchesCanonicalBytes(t *testing.T) {
	data := make([]byte, 80)
	for index := range data {
		data[index] = byte(index)
	}
	parent := &Document{reader: bytes.NewReader(data), size: int64(len(data)), limits: DefaultLimits()}
	defer parent.Close()
	before := bytes.Clone(data)
	swaps := []tiffPrivateSwap{{span: Span{Offset: 7, Length: 8}, width: 4},
		{span: Span{Offset: 20, Length: 16}, width: 8}, {span: Span{Offset: 44, Length: 6}, width: 2}}
	reader := &tiffPrivateReader{parent: parent, span: Span{Offset: 5, Length: 60}, swaps: swaps}
	expected := bytes.Clone(data)
	for _, swap := range swaps {
		width := int(swap.width)
		for offset := int(swap.span.Offset); offset < int(swap.span.Offset+swap.span.Length); offset += width {
			for component := range width {
				expected[offset+component] = data[offset+width-1-component]
			}
		}
	}
	for offset := 5; offset <= 65; offset++ {
		for length := 0; length <= 65-offset; length++ {
			result := make([]byte, length)
			count, err := reader.ReadAt(result, int64(offset))
			if err != nil || count != length || !bytes.Equal(result, expected[offset:offset+length]) {
				t.Fatalf("partial unit differs: offset=%d length=%d count=%d error=%v", offset, length, count, err)
			}
		}
	}
	if !bytes.Equal(data, before) {
		t.Fatal("normalization changed source bytes")
	}
}

type tiffPrivateUnitFault struct {
	reader io.ReaderAt
	cause  error
}

func (reader tiffPrivateUnitFault) ReadAt(data []byte, offset int64) (int, error) {
	count, err := reader.reader.ReadAt(data, offset)
	if len(data) > 1 && err == nil && count > 0 {
		return count, reader.cause
	}
	return count, err
}

func TestTIFFPrivateReaderRetainsPartialUnitFaultsAndParentClose(t *testing.T) {
	failure := errors.New("independent partial normalization source fault")
	for _, cause := range []error{failure, errors.Join(io.EOF, failure)} {
		parent := &Document{reader: tiffPrivateUnitFault{reader: bytes.NewReader([]byte{1, 2, 3, 4}), cause: cause},
			size: 4, limits: DefaultLimits()}
		defer parent.Close()
		reader := &tiffPrivateReader{parent: parent, span: Span{Length: 4},
			swaps: []tiffPrivateSwap{{span: Span{Length: 4}, width: 4}}}
		if count, err := reader.ReadAt(make([]byte, 1), 1); count != 0 || !errors.Is(err, failure) {
			t.Fatalf("partial normalized read swallowed real source cause: %d/%v", count, err)
		}
		if err := parent.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := reader.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrClosed) {
			t.Fatalf("normalization reader outlived its original source: %v", err)
		}
	}
}

func TestTIFFPrivateNormalizationRejectsOverlapAndInvalidUnits(t *testing.T) {
	parent := &Document{reader: bytes.NewReader(make([]byte, 32)), size: 32, limits: DefaultLimits()}
	defer parent.Close()
	builder := tiffPrivateBuilder{ctx: context.Background(), parent: parent,
		span: Span{Length: 32}, limits: DefaultLimits()}
	if err := builder.addSwap(Span{Length: 8}, 4, "first"); err != nil {
		t.Fatal(err)
	}
	if err := builder.addSwap(Span{Offset: 4, Length: 8}, 4, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := builder.orderedReader(); !errors.Is(err, ErrMalformed) {
		t.Fatalf("overlapping private normalization was accepted: %v", err)
	}
	if err := builder.addSwap(Span{Offset: 16, Length: 5}, 2, "misaligned"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("partial private normalization unit was accepted: %v", err)
	}
}

func FuzzTIFFPrivateLayerSourceBoundedIndex(f *testing.F) {
	f.Add(tiffPrivateTestFixture(0, "", tiffPrivateTestChild()).data)
	f.Add(tiffPrivateTestFixture(1, "", tiffPrivateTestChild()).data)
	f.Fuzz(func(t *testing.T, data []byte) {
		parent, record := rasterSource(data)
		defer parent.Close()
		source, err := parent.OpenLinkedTIFFLayers(context.Background(), record, LoadOptions{Limits: Limits{
			MaxMetadataBytes: 512 << 10, MaxDecodeBytes: 512 << 10, MaxDecodePixels: 1024,
			MaxLayers: 8, MaxBlocks: 64, MaxDescriptorItems: 128, MaxDescriptorDepth: 12,
		}})
		if err == nil {
			defer source.Close()
			_, _ = source.LayerRecord(nil, 0)
			_, _ = source.ReadSmartObjectView(nil)
			_, _ = source.ReadLinkedFileView(nil)
		}
	})
}
