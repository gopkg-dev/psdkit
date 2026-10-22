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

func encodeTestPlane(data []byte, width, height int, depth uint16) Plane {
	return Plane{ID: -1, Width: width, Height: height, Depth: depth, RowBytes: (width*int(depth) + 7) / 8, Data: data}
}

func encodeTestInflate(t *testing.T, encoded []byte) []byte {
	t.Helper()
	reader, err := zlib.NewReader(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return data
}

func TestEncodeFixedSourceSamples(t *testing.T) {
	for _, test := range []struct {
		name          string
		width, height int
		depth         uint16
		original      string
		prediction    string
	}{
		{"packed-row-padding", 9, 2, 1, "aa80ff40", ""},
		{"eight-row-reset", 3, 2, 8, "0a1400050404", "0a0aec05ff00"},
		{"sixteen-wrap", 3, 2, 16, "fffe00018000000100000001", "fffe00037fff0001ffff0001"},
		{"float-bits", 3, 2, 32, "3f8000007fc0123480000000ffffffff000000003f000001",
			"3f40010040400012ee0034ccff013fc00100ff0100ff0101"},
	} {
		for _, compression := range []uint16{0, 2, 3} {
			if compression == 3 && test.depth == 1 {
				continue
			}
			t.Run(fmt.Sprintf("%s/compression%d", test.name, compression), func(t *testing.T) {
				original := decodeHex(t, test.original)
				plane := encodeTestPlane(bytes.Clone(original), test.width, test.height, test.depth)
				encoded, err := EncodePlane(nil, plane, compression, 1)
				if err != nil {
					t.Fatal(err)
				}
				if binary.BigEndian.Uint16(encoded) != compression {
					t.Fatalf("compression word = %x", encoded[:2])
				}
				payload := encoded[2:]
				if compression != 0 {
					payload = encodeTestInflate(t, payload)
				}
				expected := original
				if compression == 3 {
					// Fixed byte-plane deltas independently follow psd-tools encode_prediction.
					// https://github.com/psd-tools/psd-tools/blob/main/src/psd_tools/compression/__init__.py.
					expected = decodeHex(t, test.prediction)
				}
				if !bytes.Equal(payload, expected) {
					t.Fatalf("payload = %x, expected independent fixture %x", payload, expected)
				}
				if !bytes.Equal(plane.Data, original) {
					t.Fatal("source samples were mutated")
				}
			})
		}
	}
}

func TestEncodePackBitsFixedRows(t *testing.T) {
	// Adobe defines per-row Macintosh PackBits and PSD/PSB two/four-byte row counts.
	// https://www.adobe.com/devnet-apps/photoshop/fileformatashtml/.
	for _, version := range []uint16{1, 2} {
		for _, test := range []struct {
			name          string
			width, height int
			depth         uint16
			original      string
			rows          string
			counts        []uint32
		}{
			{"eight", 5, 2, 8, "07070701020001020304", "fe07010102040001020304", []uint32{5, 6}},
			{"one-padding", 17, 2, 1, "aaaaaa012345", "feaa02012345", []uint32{2, 4}},
			{"sixteen", 3, 2, 16, "fffe00018000000000000000", "05fffe00018000fb00", []uint32{7, 2}},
			{"float-bits", 3, 2, 32, "ffffffffffffffffffffffff000000000000000000000000",
				"f5fff500", []uint32{2, 2}},
		} {
			t.Run(fmt.Sprintf("version%d/%s", version, test.name), func(t *testing.T) {
				plane := encodeTestPlane(decodeHex(t, test.original), test.width, test.height, test.depth)
				encoded, err := EncodePlane(nil, plane, 1, version)
				if err != nil {
					t.Fatal(err)
				}
				expected := []byte{0, 1}
				for _, count := range test.counts {
					if version == 1 {
						expected = binary.BigEndian.AppendUint16(expected, uint16(count))
					} else {
						expected = binary.BigEndian.AppendUint32(expected, count)
					}
				}
				expected = append(expected, decodeHex(t, test.rows)...)
				if !bytes.Equal(encoded, expected) {
					t.Fatalf("RLE = %x, expected fixed PackBits bytes %x", encoded, expected)
				}
			})
		}
	}
}

func TestEncodePackBitsPacketBoundaries(t *testing.T) {
	for _, length := range []int{1, 2, 3, 127, 128, 129, 255, 256, 257} {
		for _, repeated := range []bool{true, false} {
			data := make([]byte, length)
			for index := range data {
				data[index] = 7
				if !repeated {
					data[index] = byte(index % 251)
				}
			}
			plane := encodeTestPlane(data, length, 1, 8)
			encoded, err := EncodePlane(nil, plane, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			// Read packets independently rather than calling the production PSD decoder.
			var output []byte
			for payload := encoded[4:]; len(payload) != 0; {
				control := int(int8(payload[0]))
				payload = payload[1:]
				if control == -128 {
					t.Fatal("encoder emitted no-op")
				}
				if control < 0 {
					output = append(output, bytes.Repeat(payload[:1], 1-control)...)
					payload = payload[1:]
					continue
				}
				output = append(output, payload[:control+1]...)
				payload = payload[control+1:]
			}
			if !bytes.Equal(output, data) {
				t.Fatalf("packet boundary %d, repeated %v changed samples", length, repeated)
			}
		}
	}
}

func TestEncodeCompositePlanarLayout(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			pixels := CompositePixels{Width: 2, Height: 2, Depth: 8, ColorMode: 3}
			for index := range 3 {
				data := []byte{byte(index*4 + 1), byte(index*4 + 2), byte(index*4 + 3), byte(index*4 + 4)}
				plane := encodeTestPlane(data, 2, 2, 8)
				plane.ID = int16(index)
				pixels.Channels = append(pixels.Channels, plane)
			}
			encoded, err := EncodeComposite(nil, pixels, compression, version)
			if err != nil {
				t.Fatal(err)
			}
			var expected []byte
			switch compression {
			case 0, 2:
				expected = decodeHex(t, "0102030405060708090a0b0c")
			case 1:
				for range 6 {
					if version == 1 {
						expected = binary.BigEndian.AppendUint16(expected, 3)
					} else {
						expected = binary.BigEndian.AppendUint32(expected, 3)
					}
				}
				expected = append(expected, decodeHex(t, "01010201030401050601070801090a010b0c")...)
			case 3:
				expected = decodeHex(t, "010103010501070109010b01")
			}
			payload := encoded[2:]
			if compression >= 2 {
				// One zlib reader must produce every channel in planar order.
				payload = encodeTestInflate(t, payload)
			}
			if !bytes.Equal(payload, expected) {
				t.Fatalf("version %d compression %d = %x; expected %x", version, compression, payload, expected)
			}
		}
	}
}

func TestEncodePSDRowCountLimit(t *testing.T) {
	data := make([]byte, 68_000)
	for index := range data {
		data[index] = byte(index % 251)
	}
	plane := encodeTestPlane(data, 17_000, 1, 32)
	if encoded, err := EncodePlane(nil, plane, 1, 1); encoded != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("PSD oversized row = %d bytes, %v", len(encoded), err)
	}
	encoded, err := EncodePlane(nil, plane, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if count := binary.BigEndian.Uint32(encoded[2:6]); count <= 0xffff || int(count) != len(encoded)-6 {
		t.Fatalf("PSB row count = %d, payload length %d", count, len(encoded)-6)
	}
}

func TestEncodeRejectsInvalidShapesAndLimits(t *testing.T) {
	valid := encodeTestPlane([]byte{1, 2, 3, 4}, 2, 2, 8)
	for _, test := range []struct {
		name        string
		plane       Plane
		compression uint16
		version     uint16
		options     EncoderOptions
		kind        error
	}{
		{"version", valid, 0, 0, EncoderOptions{}, ErrUnsupported},
		{"compression", valid, 4, 1, EncoderOptions{}, ErrUnsupported},
		{"prediction-bitmap", encodeTestPlane([]byte{0}, 1, 1, 1), 3, 1, EncoderOptions{}, ErrUnsupported},
		{"unsupported-depth", Plane{Width: 1, Height: 1, Depth: 24}, 0, 1, EncoderOptions{}, ErrUnsupported},
		{"negative", Plane{Width: -1, Height: 1, Depth: 8}, 0, 1, EncoderOptions{}, ErrMalformed},
		{"stride", Plane{Width: 2, Height: 2, Depth: 8, RowBytes: 3, Data: valid.Data}, 0, 1, EncoderOptions{}, ErrMalformed},
		{"data-short", Plane{Width: 2, Height: 2, Depth: 8, RowBytes: 2, Data: []byte{0}},
			0, 1, EncoderOptions{}, ErrMalformed},
		{"invalid-id", Plane{ID: -4, Width: 2, Height: 2, Depth: 8, RowBytes: 2, Data: valid.Data},
			0, 1, EncoderOptions{}, ErrMalformed},
		{"pixels", valid, 0, 1, EncoderOptions{Limits: Limits{MaxDecodePixels: 3}}, ErrLimit},
		{"samples", valid, 0, 1, EncoderOptions{Limits: Limits{MaxDecodeBytes: 3}}, ErrLimit},
		{"output-word", valid, 0, 1, EncoderOptions{MaxEncodedBytes: 1}, ErrLimit},
		{"output-raw", valid, 0, 1, EncoderOptions{MaxEncodedBytes: 5}, ErrLimit},
		{"output-rle-table", valid, 1, 2, EncoderOptions{MaxEncodedBytes: 8}, ErrLimit},
		{"output-zip", valid, 2, 1, EncoderOptions{MaxEncodedBytes: 3}, ErrLimit},
		{"negative-option", valid, 0, 1, EncoderOptions{MaxEncodedBytes: -1}, ErrLimit},
		{"integer-overflow", Plane{Width: int(^uint(0) >> 1), Height: 2, Depth: 32}, 0, 2, EncoderOptions{}, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := EncodePlaneWithOptions(nil, test.plane, test.compression, test.version, test.options)
			if encoded != nil || !errors.Is(err, test.kind) {
				t.Fatalf("output = %x, error = %v; expected %v", encoded, err, test.kind)
			}
			var detail *Error
			if !errors.As(err, &detail) || detail.Feature == "" || detail.Path == "" {
				t.Fatalf("missing structured error: %v", err)
			}
		})
	}
}

func TestEncodeCompositeValidation(t *testing.T) {
	plane := encodeTestPlane([]byte{1, 2, 3, 4}, 2, 2, 8)
	plane.ID = 0
	valid := CompositePixels{Width: 2, Height: 2, Depth: 8, ColorMode: 3, Channels: []Plane{plane}}
	for _, test := range []struct {
		name    string
		modify  func(*CompositePixels)
		options EncoderOptions
		kind    error
	}{
		{"no-channels", func(p *CompositePixels) { p.Channels = nil }, EncoderOptions{}, ErrMalformed},
		{"channel-id", func(p *CompositePixels) { p.Channels[0].ID = 1 }, EncoderOptions{}, ErrMalformed},
		{"channel-width", func(p *CompositePixels) { p.Channels[0].Width = 1 }, EncoderOptions{}, ErrMalformed},
		{"channel-height", func(p *CompositePixels) { p.Channels[0].Height = 1 }, EncoderOptions{}, ErrMalformed},
		{"channel-depth", func(p *CompositePixels) { p.Channels[0].Depth = 16 }, EncoderOptions{}, ErrMalformed},
		{"empty-canvas", func(p *CompositePixels) { p.Height = 0 }, EncoderOptions{}, ErrMalformed},
		{"PSD-canvas-limit", func(p *CompositePixels) { p.Width = 30_001 }, EncoderOptions{}, ErrLimit},
		{"aggregate-budget", func(p *CompositePixels) {
			second := p.Channels[0]
			second.ID = 1
			p.Channels = append(p.Channels, second)
		}, EncoderOptions{Limits: Limits{MaxDecodeBytes: 7}}, ErrLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			pixels := valid
			pixels.Channels = append([]Plane(nil), valid.Channels...)
			test.modify(&pixels)
			encoded, err := EncodeCompositeWithOptions(nil, pixels, 0, 1, test.options)
			if encoded != nil || !errors.Is(err, test.kind) {
				t.Fatalf("output = %x, error = %v", encoded, err)
			}
		})
	}
}

func TestEncodeEmptyChannels(t *testing.T) {
	for _, dimensions := range [][2]int{{0, 0}, {0, 3}, {3, 0}} {
		for _, depth := range []uint16{1, 8, 16, 32} {
			for _, compression := range []uint16{0, 1, 2, 3} {
				if depth == 1 && compression == 3 {
					continue
				}
				plane := encodeTestPlane(nil, dimensions[0], dimensions[1], depth)
				encoded, err := EncodePlane(nil, plane, compression, 1)
				if err != nil {
					t.Fatalf("empty %v depth %d compression %d: %v", dimensions, depth, compression, err)
				}
				if compression == 0 && len(encoded) != 2 {
					t.Fatalf("empty raw = %x", encoded)
				}
				if compression == 1 && len(encoded) != 2+dimensions[1]*2 {
					t.Fatalf("empty RLE = %x", encoded)
				}
				if compression >= 2 && len(encodeTestInflate(t, encoded[2:])) != 0 {
					t.Fatalf("empty ZIP produced samples")
				}
			}
		}
	}
	largeEmpty := Plane{Height: int(^uint(0) >> 1), Depth: 8}
	if data, err := EncodePlane(nil, largeEmpty, 0, 1); err != nil || !bytes.Equal(data, []byte{0, 0}) {
		t.Fatalf("zero-width raw = %x, %v", data, err)
	}
	if data, err := EncodePlane(nil, largeEmpty, 1, 1); data != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("unbounded empty RLE table = %x, %v", data, err)
	}
}

type encodeCancelContext struct {
	context.Context
	remaining int
}

func (ctx *encodeCancelContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestEncodeCancellationAndOwnership(t *testing.T) {
	plane := encodeTestPlane(bytes.Repeat([]byte{7}, 128<<10), 256, 512, 8)
	for _, compression := range []uint16{0, 1, 2, 3} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if data, err := EncodePlane(ctx, plane, compression, 1); data != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-canceled compression %d = %x, %v", compression, data, err)
		}
		midway := &encodeCancelContext{Context: context.Background(), remaining: 10}
		if data, err := EncodePlane(midway, plane, compression, 1); data != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-canceled compression %d = %d bytes, %v", compression, len(data), err)
		}
	}
	encoded, err := EncodePlane(nil, plane, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded[2] = 9
	if plane.Data[0] != 7 {
		t.Fatal("returned encoding aliases source samples")
	}
}
