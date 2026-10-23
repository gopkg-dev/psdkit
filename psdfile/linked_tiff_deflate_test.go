package psdfile

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"testing"
)

// deflateTIFFFixture assembles classic IFD fields without using the production TIFF decoder.
func deflateTIFFFixture(
	order binary.ByteOrder, width, height, rows uint32, samples, alpha, compression, predictor uint16,
	strips [][]byte, profile []byte,
) []byte {
	short := func(values ...uint16) []byte {
		result := make([]byte, 2*len(values))
		for index, value := range values {
			order.PutUint16(result[index*2:], value)
		}
		return result
	}
	long := func(value uint32) []byte {
		result := make([]byte, 4)
		order.PutUint32(result, value)
		return result
	}
	bits := bytes.Repeat(short(8), int(samples))
	fields := []tiffTestField{
		{256, 4, 1, long(width)}, {257, 4, 1, long(height)}, {258, 3, uint32(samples), bits},
		{259, 3, 1, short(compression)}, {262, 3, 1, short(2)},
		{273, 4, uint32(len(strips)), make([]byte, 4*len(strips))}, {277, 3, 1, short(samples)},
		{278, 4, 1, long(rows)}, {279, 4, uint32(len(strips)), make([]byte, 4*len(strips))},
		{317, 3, 1, short(predictor)},
	}
	if samples == 4 {
		fields = append(fields, tiffTestField{338, 3, 1, short(alpha)})
	}
	if profile != nil {
		fields = append(fields, tiffTestField{34675, 7, uint32(len(profile)), profile})
	}
	result := make([]byte, 8+2+12*len(fields)+4)
	copy(result, "II")
	if order == binary.BigEndian {
		copy(result, "MM")
	}
	order.PutUint16(result[2:], 42)
	order.PutUint32(result[4:], 8)
	order.PutUint16(result[8:], uint16(len(fields)))
	locations := make(map[uint16]int)
	for index, field := range fields {
		position := 10 + index*12
		order.PutUint16(result[position:], field.tag)
		order.PutUint16(result[position+2:], field.kind)
		order.PutUint32(result[position+4:], field.count)
		location := position + 8
		if len(field.data) > 4 {
			if len(result)%2 != 0 {
				result = append(result, 0)
			}
			location = len(result)
			order.PutUint32(result[position+8:], uint32(location))
			result = append(result, field.data...)
		} else {
			copy(result[location:], field.data)
		}
		locations[field.tag] = location
	}
	for index, strip := range strips {
		if len(result)%2 != 0 {
			result = append(result, 0)
		}
		order.PutUint32(result[locations[273]+index*4:], uint32(len(result)))
		order.PutUint32(result[locations[279]+index*4:], uint32(len(strip)))
		result = append(result, strip...)
	}
	return result
}

func deflateTIFFStream(t *testing.T, raw []byte) []byte {
	t.Helper()
	var result bytes.Buffer
	writer := zlib.NewWriter(&result)
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func deflateTIFFStrips(t *testing.T, raw []byte, width, rows, samples int, predict bool) [][]byte {
	t.Helper()
	var strips [][]byte
	rowBytes := width * samples
	for position := 0; position < len(raw); position += rows * rowBytes {
		data := bytes.Clone(raw[position:min(position+rows*rowBytes, len(raw))])
		if predict {
			for y := 0; y < len(data); y += rowBytes {
				for index := rowBytes - 1; index >= samples; index-- {
					data[y+index] -= data[y+index-samples]
				}
			}
		}
		strips = append(strips, deflateTIFFStream(t, data))
	}
	return strips
}

func TestLinkedTIFFDeflateBothTagsOrdersRGBRGBAAndPredictorRows(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, compression := range []uint16{8, 32946} {
			for _, samples := range []uint16{3, 4} {
				for _, predictor := range []uint16{1, 2} {
					for _, rows := range []uint32{1, 2, 3} {
						t.Run(fmt.Sprintf("%s/%d/s%d/p%d/r%d", order, compression, samples, predictor, rows),
							func(t *testing.T) {
								var raw, want []byte
								for index := range 25 {
									pixel := []byte{byte(237 - index*17), byte(index*41 + 39), byte(index*31 + 77)}
									opacity := byte(255)
									if samples == 4 {
										opacity = byte(index * 47)
									}
									pixel = append(pixel, opacity)
									want = append(want, pixel...)
									raw = append(raw, pixel[:samples]...)
								}
								strips := deflateTIFFStrips(t, raw, 5, int(rows), int(samples), predictor == 2)
								data := deflateTIFFFixture(order, 5, 5, rows, samples, 2,
									compression, predictor, strips, rasterICC())
								source, record := rasterSource(data)
								result, err := source.DecodeLinkedRaster(t.Context(), record)
								if err != nil {
									t.Fatal(err)
								}
								if !bytes.Equal(result.Pixels.Pix, want) || !bytes.Equal(result.ICCProfile, rasterICC()) {
									t.Fatal("Deflate did not preserve RGB/RGBA pixels or ICC profile")
								}
								if err := source.Close(); err != nil {
									t.Fatal(err)
								}
								if !bytes.Equal(result.Pixels.Pix, want) {
									t.Fatal("Deflate pixels alias the closed source")
								}
							})
					}
				}
			}
		}
	}
}

func TestLinkedTIFFDeflateAssociatedAlphaAndPredictor(t *testing.T) {
	raw := []byte{0, 0, 0, 0, 20, 10, 63, 63, 15, 30, 90, 128, 1, 0, 1, 1}
	want := []byte{0, 0, 0, 0, 81, 40, 255, 63, 30, 60, 179, 128, 255, 0, 255, 1}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		data := deflateTIFFFixture(order, 2, 2, 2, 4, 1, 8, 2,
			deflateTIFFStrips(t, raw, 2, 2, 4, true), nil)
		source, record := rasterSource(data)
		defer source.Close()
		result, err := source.DecodeLinkedRaster(t.Context(), record)
		if err != nil || !bytes.Equal(result.Pixels.Pix, want) {
			t.Fatalf("associated alpha after prediction differs: %v", err)
		}
	}
}

func TestLinkedTIFFDeflateRejectsMalformedStreamAndTrailingInput(t *testing.T) {
	raw := []byte{12, 34, 56, 78, 90, 123}
	valid := deflateTIFFStream(t, raw)
	checksum := bytes.Clone(valid)
	checksum[len(checksum)-1] ^= 1
	// A nil dictionary has Adler-32 1, but TIFF forbids FDICT regardless of dictionary contents.
	emptyDictionary := append(bytes.Clone(valid[:2]), 0, 0, 0, 1)
	emptyDictionary[1] = (emptyDictionary[1] & 0xc0) | 0x20
	check := 31 - (int(emptyDictionary[0])*256+int(emptyDictionary[1]))%31
	emptyDictionary[1] |= byte(check % 31)
	emptyDictionary = append(emptyDictionary, valid[2:]...)
	var dictionary bytes.Buffer
	writer, err := zlib.NewWriterLevelDict(&dictionary, zlib.DefaultCompression, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for name, stream := range map[string][]byte{
		"short-output": deflateTIFFStream(t, raw[:5]), "long-output": deflateTIFFStream(t, append(raw, 7)),
		"output-bomb": deflateTIFFStream(t, make([]byte, 1<<20)), "short-header": valid[:1],
		"truncated": valid[:len(valid)-1], "checksum": checksum, "dictionary": dictionary.Bytes(),
		"empty-dictionary": emptyDictionary,
		"tail-zero":        append(bytes.Clone(valid), 0), "tail-garbage": append(bytes.Clone(valid), 127, 255),
		"second-stream":       append(bytes.Clone(valid), valid...),
		"empty-second-stream": append(bytes.Clone(valid), deflateTIFFStream(t, nil)...),
	} {
		t.Run(name, func(t *testing.T) {
			for _, compression := range []uint16{8, 32946} {
				data := deflateTIFFFixture(binary.LittleEndian, 2, 1, 1, 3, 0, compression, 1,
					[][]byte{stream}, nil)
				source, record := rasterSource(data)
				defer source.Close()
				if _, err := source.DecodeLinkedRaster(t.Context(), record); !errors.Is(err, ErrMalformed) {
					t.Fatalf("invalid Deflate strip accepted: %v", err)
				}
			}
		})
	}
}

func TestLinkedTIFFDeflateUnsupportedPredictorsAndOrganizations(t *testing.T) {
	for _, compression := range []uint16{1, 8, 32946, 32773} {
		for _, predictor := range []uint16{0, 2, 3} {
			if predictor == 2 && (compression == 8 || compression == 32946) {
				continue
			}
			data := deflateTIFFFixture(binary.BigEndian, 2, 1, 1, 3, 0, compression, predictor,
				[][]byte{deflateTIFFStream(t, []byte{1, 2, 3, 4, 5, 6})}, nil)
			source, record := rasterSource(data)
			defer source.Close()
			if _, err := source.DecodeLinkedRaster(t.Context(), record); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("compression %d predictor %d: %v", compression, predictor, err)
			}
		}
	}
	for _, tag := range []uint16{322, 330, 37724} {
		data := deflateTIFFFixture(binary.LittleEndian, 2, 1, 1, 3, 0, 8, 2,
			[][]byte{deflateTIFFStream(t, []byte{1, 2, 3, 4, 5, 6})}, nil)
		binary.LittleEndian.PutUint16(tiffTestEntry(t, data, binary.LittleEndian, 317), tag)
		source, record := rasterSource(data)
		defer source.Close()
		if _, err := source.DecodeLinkedRaster(t.Context(), record); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("TIFF organization tag %d silently accepted: %v", tag, err)
		}
	}
}

type deflateTIFFCancelReader struct {
	source io.ReaderAt
	offset int64
	cancel context.CancelFunc
}

func (reader deflateTIFFCancelReader) ReadAt(data []byte, offset int64) (int, error) {
	count, err := reader.source.ReadAt(data, offset)
	if offset >= reader.offset {
		reader.cancel()
	}
	return count, err
}

func TestLinkedTIFFDeflateBudgetsLazyReadsCancellationAndIO(t *testing.T) {
	encoded := deflateTIFFFixture(binary.LittleEndian, 2, 2, 2, 3, 0, 8, 2,
		deflateTIFFStrips(t, []byte{12, 34, 56, 78, 90, 123, 222, 111, 0, 17, 19, 23}, 2, 2, 3, true), rasterICC())
	source, record := rasterSource(append(bytes.Clone(encoded), make([]byte, 1<<20)...))
	defer source.Close()
	counter := &tiffCountingReader{source: source.reader.(*bytes.Reader)}
	source.reader = counter
	result, err := source.DecodeLinkedRaster(t.Context(), record)
	if err != nil {
		t.Fatal(err)
	}
	if counter.bytes > len(encoded)+64 {
		t.Fatalf("Deflate read unrelated TIFF payload tail: %d", counter.bytes)
	}
	if result.WorkingBytes < 64<<10 {
		t.Fatal("Deflate decoder state is missing from the working budget")
	}
	if _, err := source.DecodeLinkedRaster(t.Context(), record,
		LinkedRasterOptions{MaxWorkingBytes: result.WorkingBytes}); err != nil {
		t.Fatalf("exact Deflate working budget rejected: %v", err)
	}
	for _, options := range []LinkedRasterOptions{
		{MaxWorkingBytes: result.WorkingBytes - 1}, {MaxPixels: 3}, {MaxProfileBytes: 131},
	} {
		if _, err := source.DecodeLinkedRaster(t.Context(), record, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("Deflate working, pixel or profile limit ignored: %v", err)
		}
	}
	field := tiffTestEntry(t, encoded, binary.LittleEndian, 273)
	stripOffset := int64(binary.LittleEndian.Uint32(field[8:])) + record.Data.Offset
	failure := errors.New("independent Deflate source failure")
	for _, cause := range []error{failure, errors.Join(io.EOF, failure), ErrClosed} {
		broken, entry := rasterSource(encoded)
		defer broken.Close()
		broken.reader = tiffFaultReader{source: broken.reader, offset: stripOffset, cause: cause}
		if _, err := broken.DecodeLinkedRaster(t.Context(), entry); !errors.Is(err, cause) {
			t.Fatalf("Deflate source failure was lost: %v", err)
		}
	}
	broken, entry := rasterSource(encoded)
	defer broken.Close()
	broken.reader = bytes.NewReader(append(make([]byte, entry.Data.Offset), encoded[:len(encoded)-1]...))
	if _, err := broken.DecodeLinkedRaster(t.Context(), entry); !errors.Is(err, ErrMalformed) ||
		!errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("Deflate short source was accepted: %v", err)
	}
	canceled, item := rasterSource(encoded)
	defer canceled.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canceled.reader = deflateTIFFCancelReader{source: canceled.reader, offset: stripOffset, cancel: cancel}
	if _, err := canceled.DecodeLinkedRaster(ctx, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("Deflate read cancellation was lost: %v", err)
	}
	if _, err := canceled.DecodeLinkedRaster(ctx, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Deflate decode was accepted: %v", err)
	}
}

func TestLinkedTIFFDeflateEncodedWorkBudget(t *testing.T) {
	stream := append(deflateTIFFStream(t, []byte{1, 2, 3}), make([]byte, 100<<10)...)
	source, record := rasterSource(deflateTIFFFixture(binary.LittleEndian, 1, 1, 1, 3, 0, 8, 1,
		[][]byte{stream}, nil))
	defer source.Close()
	if _, err := source.DecodeLinkedRaster(t.Context(), record,
		LinkedRasterOptions{MaxWorkingBytes: 96 << 10}); !errors.Is(err, ErrLimit) {
		t.Fatalf("Deflate encoded work bypassed working budget: %v", err)
	}
}

func FuzzLinkedTIFFDeflateBoundedDecode(f *testing.F) {
	// The zlib bytes encode two RGB pixels; the TIFF IFD is assembled independently above.
	stream := []byte{0x78, 0x01, 0x01, 0x06, 0x00, 0xf9, 0xff, 12, 34, 56, 78, 90, 123,
		0x03, 0xf1, 0x01, 0x8a}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, compression := range []uint16{8, 32946} {
			seed := deflateTIFFFixture(order, 2, 1, 1, 3, 0, compression, 2, [][]byte{stream}, nil)
			source, record := rasterSource(seed)
			if _, err := source.DecodeLinkedRaster(f.Context(), record); err != nil {
				f.Fatalf("invalid Deflate fuzz seed: %v", err)
			}
			_ = source.Close()
			f.Add(seed)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		source, record := rasterSource(data)
		defer source.Close()
		_, _ = source.DecodeLinkedRaster(t.Context(), record,
			LinkedRasterOptions{MaxPixels: 1024, MaxWorkingBytes: 96 << 10, MaxProfileBytes: 4096})
	})
}
