package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/color"
	"io"
	"testing"
)

type tiffTestField struct {
	tag, kind uint16
	count     uint32
	data      []byte
}

// tiffTestData builds a tiny TIFF directly from the published IFD layout.
func tiffTestData(
	order binary.ByteOrder, samples, compression, alpha uint16, strips [][]byte, profile []byte,
) []byte {
	short := func(values ...uint16) []byte {
		var data []byte
		for _, value := range values {
			var encoded [2]byte
			order.PutUint16(encoded[:], value)
			data = append(data, encoded[:]...)
		}
		return data
	}
	long := func(value uint32) []byte {
		data := make([]byte, 4)
		order.PutUint32(data, value)
		return data
	}
	bits := []uint16{8, 8, 8}
	if samples == 4 {
		bits = append(bits, 8)
	}
	fields := []tiffTestField{{256, 3, 1, short(3)}, {257, 3, 1, short(2)},
		{258, 3, uint32(samples), short(bits...)}, {259, 3, 1, short(compression)}, {262, 3, 1, short(2)},
		{273, 4, uint32(len(strips)), make([]byte, 4*len(strips))}, {277, 3, 1, short(samples)},
		{278, 4, 1, long(uint32(2 / len(strips)))},
		{279, 4, uint32(len(strips)), make([]byte, 4*len(strips))}}
	if samples == 4 {
		fields = append(fields, tiffTestField{338, 3, 1, short(alpha)})
	}
	if profile != nil {
		fields = append(fields, tiffTestField{34675, 7, uint32(len(profile)), profile})
	}
	data := make([]byte, 8+2+len(fields)*12+4)
	if order == binary.LittleEndian {
		copy(data[:2], "II")
	} else {
		copy(data[:2], "MM")
	}
	order.PutUint16(data[2:4], 42)
	order.PutUint32(data[4:8], 8)
	order.PutUint16(data[8:10], uint16(len(fields)))
	positions := make([]int, len(fields))
	for index, field := range fields {
		entry := data[10+index*12 : 22+index*12]
		order.PutUint16(entry[:2], field.tag)
		order.PutUint16(entry[2:4], field.kind)
		order.PutUint32(entry[4:8], field.count)
		if len(field.data) <= 4 {
			positions[index] = 18 + index*12
			copy(entry[8:], field.data)
			continue
		}
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
		positions[index] = len(data)
		order.PutUint32(entry[8:], uint32(len(data)))
		data = append(data, field.data...)
	}
	for index, strip := range strips {
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
		order.PutUint32(data[positions[5]+index*4:], uint32(len(data)))
		order.PutUint32(data[positions[8]+index*4:], uint32(len(strip)))
		data = append(data, strip...)
	}
	return data
}

func tiffTestEntry(t *testing.T, data []byte, order binary.ByteOrder, tag uint16) []byte {
	t.Helper()
	count := int(order.Uint16(data[8:10]))
	for index := range count {
		entry := data[10+index*12 : 22+index*12]
		if order.Uint16(entry[:2]) == tag {
			return entry
		}
	}
	t.Fatalf("test TIFF tag %d is absent", tag)
	return nil
}

func TestLinkedTIFFBothOrdersMultipleRawStripsAndOwnedPixels(t *testing.T) {
	rows := [][]byte{{10, 30, 50, 70, 90, 110, 130, 150, 170},
		{190, 210, 230, 11, 31, 51, 71, 91, 111}}
	expected := []byte{10, 30, 50, 255, 70, 90, 110, 255, 130, 150, 170, 255,
		190, 210, 230, 255, 11, 31, 51, 255, 71, 91, 111, 255}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		data := tiffTestData(order, 3, 1, 0, rows, nil)
		source, record := rasterSource(data)
		format, err := source.LinkedRasterFormat(nil, record)
		if err != nil || format != "tiff" {
			t.Fatalf("TIFF format identification %q/%v", format, err)
		}
		result, err := source.DecodeLinkedRaster(nil, record)
		if err != nil || result.Format != "tiff" || !bytes.Equal(result.Pixels.Pix, expected) {
			t.Fatalf("TIFF raw RGB differs: %v", err)
		}
		if _, err := source.DecodeLinkedRaster(nil, record,
			LinkedRasterOptions{MaxWorkingBytes: result.WorkingBytes}); err != nil {
			t.Fatal(err)
		}
		if _, err := source.DecodeLinkedRaster(nil, record,
			LinkedRasterOptions{MaxWorkingBytes: result.WorkingBytes - 1}); !errors.Is(err, ErrLimit) {
			t.Fatalf("TIFF working deficit accepted: %v", err)
		}
		if _, err := source.DecodeLinkedRaster(nil, record,
			LinkedRasterOptions{MaxPixels: 5}); !errors.Is(err, ErrLimit) {
			t.Fatalf("TIFF pixel limit accepted: %v", err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(result.Pixels.Pix, expected) {
			t.Fatal("TIFF pixels were invalidated after closing the source")
		}
	}
}

func TestLinkedTIFFStraightAndAssociatedAlphaHaveExplicitSemantics(t *testing.T) {
	straight := []byte{80, 190, 45, 0, 125, 25, 240, 63, 255, 40, 200, 255,
		30, 60, 180, 128, 8, 240, 17, 237, 255, 40, 200, 1}
	associated := []byte{0, 0, 0, 0, 20, 10, 63, 63, 255, 40, 200, 255,
		15, 30, 90, 128, 8, 220, 17, 237, 1, 0, 1, 1}
	for _, testCase := range []struct {
		alpha uint16
		data  []byte
		want  []byte
	}{{2, straight, straight}, {1, associated,
		[]byte{0, 0, 0, 0, 81, 40, 255, 63, 255, 40, 200, 255,
			30, 60, 179, 128, 9, 237, 18, 237, 255, 0, 255, 1}}} {
		encoded := tiffTestData(binary.LittleEndian, 4, 1, testCase.alpha, [][]byte{testCase.data}, nil)
		source, record := rasterSource(encoded)
		defer source.Close()
		result, err := source.DecodeLinkedRaster(nil, record)
		if err != nil || !bytes.Equal(result.Pixels.Pix, testCase.want) {
			t.Fatalf("TIFF alpha %d differs: %v/%v", testCase.alpha, result, err)
		}
	}
}

func TestLinkedTIFFPackBitsPublishedPacketsAndRowBoundaries(t *testing.T) {
	// -128 does nothing, 2 copies three literal bytes, and -5 repeats one byte six times.
	strips := [][]byte{{128, 2, 11, 21, 31, 251, 77, 128},
		{8, 5, 15, 25, 35, 45, 55, 65, 75, 85}}
	encoded := tiffTestData(binary.BigEndian, 3, 32773, 0, strips, nil)
	source, record := rasterSource(encoded)
	defer source.Close()
	result, err := source.DecodeLinkedRaster(nil, record)
	want := []byte{11, 21, 31, 255, 77, 77, 77, 255, 77, 77, 77, 255,
		5, 15, 25, 255, 35, 45, 55, 255, 65, 75, 85, 255}
	if err != nil || !bytes.Equal(result.Pixels.Pix, want) {
		t.Fatalf("TIFF PackBits differs: %v", err)
	}
	for _, invalid := range [][][]byte{
		{{9, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, strips[1]},
		{{8, 1, 2, 3}, strips[1]},
		{{2, 11, 21, 31, 251, 77, 0, 3}, strips[1]},
	} {
		data := tiffTestData(binary.LittleEndian, 3, 32773, 0, invalid, nil)
		broken, entry := rasterSource(data)
		defer broken.Close()
		if _, err := broken.DecodeLinkedRaster(nil, entry); !errors.Is(err, ErrMalformed) {
			t.Fatalf("malformed PackBits row was accepted: %v", err)
		}
	}
}

func TestLinkedTIFFICCIsBoundedOwnedAndDoesNotChangePixelEncoding(t *testing.T) {
	profile := rasterICC()
	raw := bytes.Repeat([]byte{22, 44, 88}, 6)
	source, record := rasterSource(tiffTestData(binary.LittleEndian, 3, 1, 0, [][]byte{raw}, profile))
	defer source.Close()
	result, err := source.DecodeLinkedRaster(nil, record)
	if err != nil || !bytes.Equal(result.ICCProfile, profile) ||
		result.Pixels.NRGBAAt(0, 0) != (color.NRGBA{22, 44, 88, 255}) {
		t.Fatalf("TIFF ICC changed encoding or data: %v", err)
	}
	result.ICCProfile[0] = 0xff
	again, err := source.DecodeLinkedRaster(nil, record)
	if err != nil || !bytes.Equal(again.ICCProfile, profile) {
		t.Fatalf("TIFF ICC output aliases source: %v", err)
	}
	for _, options := range []LinkedRasterOptions{{MaxProfileBytes: 131},
		{MaxWorkingBytes: result.WorkingBytes - 1}} {
		if _, err := source.DecodeLinkedRaster(nil, record, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("TIFF ICC limit ignored: %v", err)
		}
	}
	profile[3]--
	broken, entry := rasterSource(tiffTestData(binary.LittleEndian, 3, 1, 0, [][]byte{raw}, profile))
	defer broken.Close()
	if _, err := broken.DecodeLinkedRaster(nil, entry); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid TIFF ICC size accepted: %v", err)
	}
}

func TestLinkedTIFFMalformedAndUnsupportedRemainDistinct(t *testing.T) {
	base := tiffTestData(binary.LittleEndian, 3, 1, 0, [][]byte{bytes.Repeat([]byte{10, 20, 30}, 6)}, nil)
	for _, testCase := range []struct {
		name   string
		kind   error
		mutate func([]byte)
	}{
		{"zero_width", ErrMalformed, func(data []byte) {
			binary.LittleEndian.PutUint16(tiffTestEntry(t, data, binary.LittleEndian, 256)[8:], 0)
		}},
		{"wrong_scalar_type", ErrMalformed, func(data []byte) {
			binary.LittleEndian.PutUint16(tiffTestEntry(t, data, binary.LittleEndian, 256)[2:], 8)
		}},
		{"strip_count", ErrMalformed, func(data []byte) {
			binary.LittleEndian.PutUint32(tiffTestEntry(t, data, binary.LittleEndian, 279)[8:], 19)
		}},
		{"outside_strip", ErrMalformed, func(data []byte) {
			binary.LittleEndian.PutUint32(tiffTestEntry(t, data, binary.LittleEndian, 273)[8:], uint32(len(data)))
		}},
		{"outside_field", ErrMalformed, func(data []byte) {
			binary.LittleEndian.PutUint32(tiffTestEntry(t, data, binary.LittleEndian, 258)[8:], uint32(len(data)+1))
		}},
		{"duplicate", ErrMalformed, func(data []byte) { copy(data[22:24], data[10:12]) }},
		{"directory_cycle", ErrMalformed, func(data []byte) {
			binary.LittleEndian.PutUint32(data[10+9*12:], 8)
		}},
		{"lzw", ErrUnsupported, func(data []byte) {
			binary.LittleEndian.PutUint16(tiffTestEntry(t, data, binary.LittleEndian, 259)[8:], 5)
		}},
		{"gray", ErrUnsupported, func(data []byte) {
			binary.LittleEndian.PutUint16(tiffTestEntry(t, data, binary.LittleEndian, 262)[8:], 1)
		}},
		{"16_bit", ErrUnsupported, func(data []byte) {
			entry := tiffTestEntry(t, data, binary.LittleEndian, 258)
			binary.LittleEndian.PutUint16(data[binary.LittleEndian.Uint32(entry[8:]):], 16)
		}},
		{"photoshop_layered", ErrUnsupported, func(data []byte) {
			binary.LittleEndian.PutUint16(tiffTestEntry(t, data, binary.LittleEndian, 279)[:2], 37724)
		}},
		{"bigtiff", ErrUnsupported, func(data []byte) { binary.LittleEndian.PutUint16(data[2:4], 43) }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			data := bytes.Clone(base)
			testCase.mutate(data)
			source, record := rasterSource(data)
			defer source.Close()
			if _, err := source.DecodeLinkedRaster(nil, record); !errors.Is(err, testCase.kind) {
				t.Fatalf("TIFF error category %v, expected %v", err, testCase.kind)
			}
		})
	}
}

func TestLinkedTIFFRejectsUnknownAlphaAndInvalidAssociatedColor(t *testing.T) {
	for _, testCase := range []struct {
		alpha uint16
		data  []byte
		kind  error
	}{{0, bytes.Repeat([]byte{0, 0, 0, 0}, 6), ErrUnsupported},
		{1, bytes.Repeat([]byte{1, 0, 0, 0}, 6), ErrMalformed}} {
		source, record := rasterSource(tiffTestData(binary.LittleEndian, 4, 1,
			testCase.alpha, [][]byte{testCase.data}, nil))
		defer source.Close()
		if _, err := source.DecodeLinkedRaster(nil, record); !errors.Is(err, testCase.kind) {
			t.Fatalf("TIFF alpha error %v, expected %v", err, testCase.kind)
		}
	}
}

type tiffCountingReader struct {
	source *bytes.Reader
	bytes  int
	reads  int
	cancel context.CancelFunc
}

type tiffFaultReader struct {
	source io.ReaderAt
	offset int64
	cause  error
}

func (reader tiffFaultReader) ReadAt(data []byte, offset int64) (int, error) {
	count, err := reader.source.ReadAt(data, offset)
	if offset >= reader.offset && count > 0 && err == nil {
		return count, reader.cause
	}
	return count, err
}

func (reader *tiffCountingReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.reads++
	count, err := reader.source.ReadAt(data, offset)
	reader.bytes += count
	if reader.cancel != nil && reader.reads == 8 {
		reader.cancel()
	}
	return count, err
}

func TestLinkedTIFFLazyReadsCancellationAndIOCauses(t *testing.T) {
	encoded := tiffTestData(binary.LittleEndian, 3, 1, 0,
		[][]byte{bytes.Repeat([]byte{10, 20, 30}, 6)}, nil)
	data := append(bytes.Clone(encoded), make([]byte, 8<<20)...)
	source, record := rasterSource(data)
	counter := &tiffCountingReader{source: source.reader.(*bytes.Reader)}
	source.reader = counter
	defer source.Close()
	if _, err := source.DecodeLinkedRaster(nil, record); err != nil {
		t.Fatal(err)
	}
	if counter.bytes > len(encoded)+64 {
		t.Fatalf("TIFF decoder read the unrelated payload tail: %d", counter.bytes)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counter.reads, counter.cancel = 0, cancel
	if _, err := source.DecodeLinkedRaster(ctx, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("TIFF cancellation was lost: %v", err)
	}
	failure := errors.New("independent TIFF reader failure")
	broken, entry := rasterSource(encoded)
	strip := tiffTestEntry(t, encoded, binary.LittleEndian, 273)
	stripOffset := int64(binary.LittleEndian.Uint32(strip[8:])) + entry.Data.Offset
	broken.reader = tiffFaultReader{source: broken.reader, offset: stripOffset, cause: failure}
	defer broken.Close()
	if _, err := broken.DecodeLinkedRaster(nil, entry); !errors.Is(err, ErrMalformed) || !errors.Is(err, failure) {
		t.Fatalf("TIFF full-read non-EOF cause was lost: %v", err)
	}
	truncated, item := rasterSource(encoded[:len(encoded)-1])
	defer truncated.Close()
	if _, err := truncated.DecodeLinkedRaster(nil, item); !errors.Is(err, ErrMalformed) {
		t.Fatalf("TIFF truncated strip was accepted: %v", err)
	}
	shortRead, shortRecord := rasterSource(encoded)
	defer shortRead.Close()
	shortRead.reader = bytes.NewReader(append(make([]byte, 13), encoded[:len(encoded)-1]...))
	if _, err := shortRead.DecodeLinkedRaster(nil, shortRecord); !errors.Is(err, ErrMalformed) ||
		!errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("TIFF short-read cause was lost: %v", err)
	}
}

func TestLinkedTIFFPackBitsPreservesJoinedEOFFaultAfterFullStripRead(t *testing.T) {
	encoded := tiffTestData(binary.LittleEndian, 3, 32773, 0,
		[][]byte{{2, 11, 21, 31, 251, 77}, {8, 5, 15, 25, 35, 45, 55, 65, 75, 85}}, nil)
	source, record := rasterSource(encoded)
	defer source.Close()
	field := tiffTestEntry(t, encoded, binary.LittleEndian, 273)
	tableOffset := binary.LittleEndian.Uint32(field[8:])
	stripOffset := int64(binary.LittleEndian.Uint32(encoded[tableOffset:])) + record.Data.Offset
	failure := errors.New("independent source fault accompanying EOF")
	source.reader = tiffFaultReader{source: source.reader, offset: stripOffset,
		cause: errors.Join(io.EOF, failure)}
	if _, err := source.DecodeLinkedRaster(nil, record); !errors.Is(err, ErrMalformed) || !errors.Is(err, failure) {
		t.Fatalf("TIFF PackBits discarded a joined EOF fault after decoding all rows: %v", err)
	}
}

func FuzzLinkedTIFFBoundedDecode(f *testing.F) {
	f.Add(tiffTestData(binary.LittleEndian, 3, 1, 0,
		[][]byte{bytes.Repeat([]byte{12, 34, 56}, 6)}, nil))
	f.Add(tiffTestData(binary.BigEndian, 4, 1, 2,
		[][]byte{bytes.Repeat([]byte{12, 34, 56, 78}, 6)}, nil))
	f.Add(tiffTestData(binary.LittleEndian, 3, 32773, 0,
		[][]byte{{248, 12}, {248, 34}}, nil))
	f.Fuzz(func(t *testing.T, data []byte) {
		source, record := rasterSource(data)
		defer source.Close()
		_, _ = source.DecodeLinkedRaster(context.Background(), record,
			LinkedRasterOptions{MaxPixels: 1024, MaxWorkingBytes: 64 << 10, MaxProfileBytes: 4096})
	})
}
