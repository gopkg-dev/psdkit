package psdvector

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"
)

func vectorRecord(selector uint16) []byte {
	data := make([]byte, 26)
	binary.BigEndian.PutUint16(data, selector)
	return data
}

func vectorLength(closed bool, count uint16, operation int16) []byte {
	selector := uint16(3)
	if closed {
		selector = 0
	}
	data := vectorRecord(selector)
	binary.BigEndian.PutUint16(data[2:], count)
	binary.BigEndian.PutUint16(data[4:], uint16(operation))
	binary.BigEndian.PutUint16(data[6:], 1)
	return data
}

func vectorKnot(closed bool, x, y int32) []byte {
	selector := uint16(5)
	if closed {
		selector = 2
	}
	data := vectorRecord(selector)
	for offset := 2; offset < 26; offset += 8 {
		binary.BigEndian.PutUint32(data[offset:], uint32(y))
		binary.BigEndian.PutUint32(data[offset+4:], uint32(x))
	}
	return data
}

func vectorRectangle(flags uint32) []byte {
	data := binary.BigEndian.AppendUint32(nil, 3)
	data = binary.BigEndian.AppendUint32(data, flags)
	data = append(data, vectorRecord(6)...)
	data = append(data, vectorLength(true, 4, 1)...)
	for _, point := range [][2]int32{{1 << 22, 1 << 22}, {3 << 22, 1 << 22}, {3 << 22, 3 << 22}, {1 << 22, 3 << 22}} {
		data = append(data, vectorKnot(true, point[0], point[1])...)
	}
	return data
}

func TestDecodeMaskRetainsRawCoordinatesMetadataAndPadding(t *testing.T) {
	data := vectorRectangle(7)
	initial := vectorRecord(8)
	binary.BigEndian.PutUint16(initial[2:], 1)
	data = append(data, initial...)
	clipboard := vectorRecord(7)
	binary.BigEndian.PutUint32(clipboard[2:], uint32(0xff800000))
	binary.BigEndian.PutUint32(clipboard[18:], 72<<24)
	data = append(data, clipboard...)
	data = append(data, 0, 0)
	mask, err := DecodeMask(context.Background(), data, 100, 200, DecodeOptions{SourceOffset: 53})
	if err != nil {
		t.Fatal(err)
	}
	if !mask.Invert || !mask.NotLinked || !mask.Disabled || !mask.InitialFill || !mask.HasInitialFill ||
		!mask.HasFillRule || mask.FillRule != EvenOdd || mask.Span.Offset != 53 || mask.Span.Length != int64(len(data)) {
		t.Fatalf("flags, default fill or source spans lost: %#v", mask)
	}
	if len(mask.Subpaths) != 1 || len(mask.Subpaths[0].Knots) != 4 ||
		mask.Subpaths[0].Operation != 1 || mask.Subpaths[0].Unknown1 != 1 {
		t.Fatalf("subpath length metadata lost: %#v", mask.Subpaths)
	}
	point := mask.Subpaths[0].Knots[0].Anchor
	if point.X != 25 || point.Y != 50 || point.RawX != 1<<22 || point.RawY != 1<<22 {
		t.Fatalf("fixed-point normalization lost vertical/horizontal ordering: %#v", point)
	}
	if len(mask.Clipboards) != 1 || mask.Clipboards[0].Top != -1<<23 || mask.Clipboards[0].Resolution != 72<<24 ||
		len(mask.Padding) != 2 || mask.PaddingSpan.Offset != 53+int64(len(data)-2) {
		t.Fatal("clipboard or alignment bytes lost")
	}
	if mask.Records[2].Span.Offset != 53+8+2*26 || !bytes.Equal(mask.Records[2].Raw[:], data[8+2*26:8+3*26]) {
		t.Fatal("raw knot encoding or record span lost")
	}
	clear(data)
	if mask.Records[2].Raw[0] != 0 || mask.Records[2].Raw[1] != 2 || point.X != 25 || mask.Header[3] != 3 {
		t.Fatal("decoded raw records borrowed the supplied byte buffer")
	}
}

func TestDecodeUnknownRecordsPreserveSourceAndRequireExplicitSupport(t *testing.T) {
	data := append(vectorRectangle(0), vectorRecord(42)...)
	mask, err := DecodeMask(nil, data, 4, 4)
	if err != nil || mask.Records[len(mask.Records)-1].Selector != 42 || len(mask.Unsupported) == 0 {
		t.Fatalf("unknown selector was lost during typed decode: %v", err)
	}
	if _, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown selector silently rasterized: %v", err)
	}
}

func TestDecodeHeaderlessPathRetainsOriginalOffsets(t *testing.T) {
	data := vectorRectangle(0)[8:]
	mask, err := DecodePath(nil, data, 4, 4, DecodeOptions{SourceOffset: 101})
	if err != nil || mask.Version != 0 || mask.Flags != 0 || mask.HeaderSpan.Length != 0 ||
		mask.Records[0].Span.Offset != 101 || mask.Subpaths[0].Knots[0].Anchor.X != 1 {
		t.Fatalf("headerless path lost source identity: %#v, %v", mask, err)
	}
	coverage, err := Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
	if err != nil || coverage.Pix[5] != 255 {
		t.Fatalf("headerless saved path did not rasterize: %v", err)
	}
}

func TestDecodeMaskRejectsTruncationCountsAndLimits(t *testing.T) {
	valid := vectorRectangle(0)
	wrongVersion := append([]byte{}, valid...)
	binary.BigEndian.PutUint32(wrongVersion, 4)
	wrongCount := append([]byte{}, valid...)
	binary.BigEndian.PutUint16(wrongCount[8+26+2:], 5)
	orphan := append(valid[:8:8], vectorKnot(true, 0, 0)...)
	wrongKind := append([]byte{}, valid...)
	binary.BigEndian.PutUint16(wrongKind[8+2*26:], 5)
	for _, test := range []struct {
		name    string
		data    []byte
		options DecodeOptions
		kind    error
	}{
		{"header", []byte{1, 2}, DecodeOptions{}, ErrMalformed},
		{"version", wrongVersion, DecodeOptions{}, ErrUnsupported},
		{"truncated", valid[:len(valid)-4], DecodeOptions{}, ErrMalformed},
		{"count", wrongCount, DecodeOptions{}, ErrMalformed},
		{"orphan", orphan, DecodeOptions{}, ErrMalformed},
		{"kind", wrongKind, DecodeOptions{}, ErrMalformed},
		{"records", valid, DecodeOptions{MaxRecords: 1}, ErrLimit},
		{"bytes", valid, DecodeOptions{MaxBytes: 100}, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeMask(nil, test.data, 4, 4, test.options); !errors.Is(err, test.kind) {
				t.Fatalf("decode category = %v, want %v", err, test.kind)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeMask(ctx, valid, 4, 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled vector decode: %v", err)
	}
}

func FuzzDecodeMask(f *testing.F) {
	f.Add(vectorRectangle(0))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			return
		}
		mask, err := DecodeMask(nil, data, 8, 8, DecodeOptions{MaxBytes: 64 << 10, MaxRecords: 128})
		if err != nil {
			return
		}
		_, _ = Rasterize(nil, mask, image.Rect(0, 0, 8, 8), Options{
			MaxWorkingBytes: 128 << 10, MaxSegments: 512, MaxDepth: 8, MaxScanlineWork: 4096,
		})
	})
}
