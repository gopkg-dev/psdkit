package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"
)

func lzwReferencePixels(width, height, samples int) []byte {
	state := uint32(0x9e3779b9)
	var pixels []byte
	for range width * height {
		for range samples {
			state = 1664525*state + 1013904223
			pixels = append(pixels, byte(state>>24))
		}
		if samples == 3 {
			pixels = append(pixels, 255)
		}
	}
	return pixels
}

func TestLinkedTIFFLZWIndependentLibTIFFReferences(t *testing.T) {
	for _, fixture := range []struct {
		name                   string
		width, height, samples int
	}{
		{"rgb-lzw.tif", 257, 20, 3},
		{"rgba-lzw-predictor.tif", 257, 20, 4},
		{"rgba-lzw-small.tif", 2, 2, 4},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + fixture.name)
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(data)
			decoded, err := DecodeRasterContents(t.Context(), data)
			if err != nil {
				t.Fatal(err)
			}
			want := lzwReferencePixels(fixture.width, fixture.height, fixture.samples)
			if !bytes.Equal(decoded.Pixels.Pix, want) || !bytes.Equal(data, original) {
				t.Fatal("LZW pixels differ from the independent reference or input changed")
			}
			if decoded.WorkingBytes < 32<<10 {
				t.Fatal("LZW dictionary was omitted from its working reservation")
			}
			if result, err := DecodeRasterContents(t.Context(), data,
				LinkedRasterOptions{MaxWorkingBytes: decoded.WorkingBytes - 1}); result != nil || !errors.Is(err, ErrLimit) {
				t.Fatalf("LZW ignored its exact peak memory bound: %v", err)
			}
		})
	}
}

// literalLZW packs independently chosen nine-bit codes, so malformed cases do not use the production encoder.
func literalLZW(codes ...uint16) []byte {
	var result []byte
	var bits uint32
	var count uint
	for _, code := range codes {
		bits = bits<<9 | uint32(code)
		count += 9
		for count >= 8 {
			count -= 8
			result = append(result, byte(bits>>count))
		}
		bits &= (1 << count) - 1
	}
	if count != 0 {
		result = append(result, byte(bits<<(8-count)))
	}
	return result
}

func TestLinkedTIFFLZWClearKwKwKPredictionAndStrictTerminator(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, predictor := range []uint16{1, 2} {
			stream := literalLZW(256, 65, 258, 259, 257)
			data := deflateTIFFFixture(order, 2, 1, 1, 3, 0, 5, predictor, [][]byte{stream}, rasterICC())
			result, err := DecodeRasterContents(t.Context(), data)
			want := []byte{65, 65, 65, 255, 65, 65, 65, 255}
			if predictor == 2 {
				want[4], want[5], want[6] = 130, 130, 130
			}
			if err != nil || !bytes.Equal(result.Pixels.Pix, want) || !bytes.Equal(result.ICCProfile, rasterICC()) {
				t.Fatalf("%s predictor %d: special LZW code or ICC changed: %v", order, predictor, err)
			}
		}
	}
	for name, stream := range map[string][]byte{
		"missing clear":  literalLZW(65, 65, 65, 257),
		"forward code":   literalLZW(256, 65, 260, 257),
		"first code":     literalLZW(256, 258, 257),
		"missing eoi":    literalLZW(256, 1, 2, 3),
		"short output":   literalLZW(256, 1, 2, 257),
		"long output":    literalLZW(256, 1, 2, 3, 4, 257),
		"trailing byte":  append(literalLZW(256, 1, 2, 3, 257), 0),
		"clear then eoi": literalLZW(256, 256, 257),
	} {
		t.Run(name, func(t *testing.T) {
			data := deflateTIFFFixture(binary.LittleEndian, 1, 1, 1, 3, 0, 5, 1, [][]byte{stream}, nil)
			if result, err := DecodeRasterContents(t.Context(), data); result != nil || !errors.Is(err, ErrMalformed) {
				t.Fatalf("malformed LZW returned pixels: %v", err)
			}
		})
	}
}

func TestLinkedTIFFLZWReaderResetCancellationAndSourceErrors(t *testing.T) {
	stream := literalLZW(256, 1, 2, 256, 3, 257)
	decoder := &linkedTIFFLZW{context: t.Context(), reader: bytes.NewReader(stream)}
	got, err := io.ReadAll(decoder)
	if err != nil || !bytes.Equal(got, []byte{1, 2, 3}) || decoder.finish() != nil {
		t.Fatalf("LZW reset did not restore its literal table: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	decoder = &linkedTIFFLZW{context: ctx, reader: bytes.NewReader(stream)}
	if output, err := io.ReadAll(decoder); len(output) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("LZW ignored cancellation: %v", err)
	}
	cause := fmt.Errorf("%w: original strip source", io.ErrClosedPipe)
	decoder = &linkedTIFFLZW{context: t.Context(), reader: lzwErrorReader{cause}}
	if _, err := io.ReadAll(decoder); !errors.Is(err, cause) {
		t.Fatalf("LZW lost original reader error: %v", err)
	}
}

type lzwErrorReader struct{ cause error }

func (reader lzwErrorReader) ReadByte() (byte, error) { return 0, reader.cause }

func FuzzTIFFLZWBoundedReader(f *testing.F) {
	f.Add(literalLZW(256, 65, 258, 259, 257))
	f.Add(literalLZW(256, 1, 2, 256, 3, 257))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip()
		}
		decoder := &linkedTIFFLZW{context: t.Context(), reader: bytes.NewReader(data)}
		_, _ = io.Copy(io.Discard, io.LimitReader(decoder, 64<<10))
	})
}
