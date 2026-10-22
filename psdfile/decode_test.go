package psdfile

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"sync"
	"testing"
)

func decodeHex(t *testing.T, value string) []byte {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeTestDocument(data []byte, width, height int, depth, compression, version uint16) *Document {
	span := Span{Length: int64(len(data))}
	return &Document{
		Header: Header{Version: version, Width: uint32(width), Height: uint32(height), Channels: 1, Depth: depth},
		Layers: []LayerRecord{{Bounds: image.Rect(0, 0, width, height), Channels: []ChannelRecord{{
			ID: -1, Bounds: image.Rect(0, 0, width, height), Compression: compression, Data: span,
		}}}},
		Composite: CompositeRecord{Compression: compression, Data: span}, ActiveLayerSet: -1,
		reader: bytes.NewReader(data), size: int64(len(data)), limits: DefaultLimits(),
	}
}

func TestDecodeSourceSamples(t *testing.T) {
	for _, test := range []struct {
		name               string
		width, height      int
		depth, compression uint16
		encoded, expected  string
	}{
		{"bitmap-row-padding", 9, 2, 1, 0, "aa80ff40", "aa80ff40"},
		{"raw8", 3, 2, 8, 0, "0a1400050404", "0a1400050404"},
		{"raw16", 3, 2, 16, 0, "fffe00018000000100000001", "fffe00018000000100000001"},
		{"raw32-bits", 3, 2, 32, 0, "3f8000007fc0123480000000ffffffff000000003f000001",
			"3f8000007fc0123480000000ffffffff000000003f000001"},
		{"zip8", 3, 2, 8, 2, "789c6364626661650300003e0016", "010203040506"},
		{"prediction8-row-reset", 3, 2, 8, 3, "789ce3e27ac3fa9f010006310205", "0a1400050404"},
		{"prediction16-wrap", 3, 2, 16, 3, "789cfbff8f81b9fe3f03e37f20060028f5057f",
			"fffe00018000000100000001"},
		{"prediction32-shuffle-bits", 3, 2, 32, 3,
			"789cb3776064707060107ac76072e63fa3fd014686ff20c4080051180702",
			"3f8000007fc0123480000000ffffffff000000003f000001"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := decodeTestDocument(
				decodeHex(t, test.encoded), test.width, test.height, test.depth, test.compression, 1,
			)
			plane, err := document.DecodeChannel(nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			if plane.ID != -1 || plane.Depth != test.depth || plane.Width != test.width || plane.Height != test.height {
				t.Fatalf("sample metadata changed: %+v", plane)
			}
			if !bytes.Equal(plane.Data, decodeHex(t, test.expected)) {
				t.Fatalf("samples = %x, expected %s", plane.Data, test.expected)
			}
			if plane.RowBytes != (test.width*int(test.depth)+7)/8 {
				t.Fatalf("row size = %d", plane.RowBytes)
			}
			plane.Data[0] ^= 0xff
			other, err := document.DecodeChannel(context.Background(), 0, 0)
			if err != nil || !bytes.Equal(other.Data, decodeHex(t, test.expected)) {
				t.Fatalf("decoded data is not independently owned: %v", err)
			}
		})
	}
}

func TestDecodeRLEFixedRows(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, depth := range []uint16{1, 8, 16, 32} {
			t.Run(fmt.Sprintf("version%d/depth%d", version, depth), func(t *testing.T) {
				width := 3
				expected := []byte{7, 7, 7, 1, 2, 3}
				rows := []byte{0xfe, 7, 0x80, 2, 1, 2, 3}
				counts := []byte{0, 3, 0, 4}
				if version == 2 {
					counts = []byte{0, 0, 0, 3, 0, 0, 0, 4}
				}
				if depth == 1 {
					width = 17
				}
				if depth == 16 {
					rows = []byte{5, 0xff, 0xfe, 0, 1, 0x80, 0, 0xfb, 0}
					expected = []byte{0xff, 0xfe, 0, 1, 0x80, 0, 0, 0, 0, 0, 0, 0}
					counts = []byte{0, 7, 0, 2}
					if version == 2 {
						counts = []byte{0, 0, 0, 7, 0, 0, 0, 2}
					}
				}
				if depth == 32 {
					rows = []byte{0xf5, 0xff, 0xf5, 0}
					expected = append(bytes.Repeat([]byte{0xff}, 12), make([]byte, 12)...)
					counts = []byte{0, 2, 0, 2}
					if version == 2 {
						counts = []byte{0, 0, 0, 2, 0, 0, 0, 2}
					}
				}
				document := decodeTestDocument(append(counts, rows...), width, 2, depth, 1, version)
				pixels, err := document.DecodeLayer(nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(pixels.Channels[0].Data, expected) {
					t.Fatalf("RLE result = %x; expected %x", pixels.Channels[0].Data, expected)
				}
			})
		}
	}
}

func TestDecodeZIPAllDepths(t *testing.T) {
	for _, test := range []struct {
		depth uint16
		width int
		data  []byte
	}{
		{1, 9, []byte{0xaa, 0x80, 0xff, 0x40}},
		{8, 2, []byte{0, 0x80, 0xff, 1}},
		{16, 2, []byte{0, 1, 0x80, 0, 0xff, 0xfe, 0xff, 0xff}},
		{32, 2, []byte{0x7f, 0xc0, 0x12, 0x34, 0x80, 0, 0, 0, 0x3f, 0x80, 0, 0, 0xff, 0xff, 0xff, 0xff}},
	} {
		t.Run(fmt.Sprintf("depth%d", test.depth), func(t *testing.T) {
			document := decodeTestDocument(decodeTestZIP(t, test.data), test.width, 2, test.depth, 2, 2)
			plane, err := document.DecodeChannel(nil, 0, 0)
			if err != nil || !bytes.Equal(plane.Data, test.data) {
				t.Fatalf("source samples changed: %+v, %v", plane, err)
			}
		})
	}
}

func decodeTestZIP(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zlib.NewWriter(&buffer)
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestDecodeCompositeSingleZIPStream(t *testing.T) {
	for _, compression := range []uint16{0, 1, 2, 3} {
		for _, version := range []uint16{1, 2} {
			original := []byte{1, 2, 3, 4, 5, 6}
			encoded := original
			switch compression {
			case 1:
				counts := []byte{0, 3, 0, 3, 0, 3}
				if version == 2 {
					counts = []byte{0, 0, 0, 3, 0, 0, 0, 3, 0, 0, 0, 3}
				}
				encoded = append(counts, []byte{1, 1, 2, 1, 3, 4, 1, 5, 6}...)
			case 2:
				encoded = decodeHex(t, "789c6364626661650300003e0016")
			case 3:
				encoded = decodeTestZIP(t, []byte{1, 1, 3, 1, 5, 1})
			}
			document := decodeTestDocument(encoded, 2, 1, 8, compression, version)
			document.Header.Channels = 3
			document.Header.ColorMode = 9
			pixels, err := document.DecodeComposite(nil)
			if err != nil {
				t.Fatalf("compression %d version %d: %v", compression, version, err)
			}
			if pixels.ColorMode != 9 || pixels.Depth != 8 || len(pixels.Channels) != 3 {
				t.Fatalf("composite metadata changed: %+v", pixels)
			}
			for index, full := range pixels.Channels {
				plane, err := document.DecodeCompositeChannel(nil, index)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(plane.Data, original[index*2:(index+1)*2]) || !bytes.Equal(full.Data, plane.Data) {
					t.Fatalf("channel %d = %x; full %x", index, plane.Data, full.Data)
				}
			}
		}
	}
}

func TestDecodeRejectsMalformedPayloads(t *testing.T) {
	validZIP := decodeHex(t, "789c6364626661650300003e0016")
	for _, test := range []struct {
		name        string
		compression uint16
		data        []byte
	}{
		{"raw-short", 0, []byte{1, 2}},
		{"raw-extra", 0, []byte{1, 2, 3, 4, 5, 6, 7}},
		{"rle-table-short", 1, []byte{0, 4}},
		{"rle-table-payload-mismatch", 1, []byte{0, 2, 0, 2, 0, 7, 0, 8, 0}},
		{"rle-literal-short", 1, []byte{0, 2, 0, 2, 2, 7, 2, 8}},
		{"rle-repeat-missing", 1, []byte{0, 1, 0, 1, 0xfe, 0xfe}},
		{"rle-output-short", 1, []byte{0, 2, 0, 2, 0, 7, 0, 8}},
		{"rle-output-overrun", 1, []byte{0, 2, 0, 2, 0xfd, 7, 0xfd, 8}},
		{"zip-short-output", 2, decodeTestZIP(t, []byte{1, 2, 3})},
		{"zip-long-output", 2, decodeTestZIP(t, []byte{1, 2, 3, 4, 5, 6, 7})},
		{"zip-truncated-checksum", 2, validZIP[:len(validZIP)-1]},
		{"zip-wrong-checksum", 2, append(bytes.Clone(validZIP[:len(validZIP)-1]), 0)},
		{"zip-trailing-byte", 2, append(bytes.Clone(validZIP), 0)},
		{"zip-concatenated-stream", 2, append(bytes.Clone(validZIP), validZIP...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := decodeTestDocument(test.data, 3, 2, 8, test.compression, 1)
			plane, err := document.DecodeChannel(nil, 0, 0)
			if plane != nil || !errors.Is(err, ErrMalformed) {
				t.Fatalf("result = %+v, error = %v", plane, err)
			}
			var detail *Error
			if !errors.As(err, &detail) || detail.Path != "layers[0].channels[0]" || detail.Feature == "" {
				t.Fatalf("missing structured decode error: %v", err)
			}
		})
	}
}

func TestDecodeLimitsBeforeSourceReads(t *testing.T) {
	for _, test := range []struct {
		name   string
		method func(*Document) error
		limits Limits
	}{
		{"channel-pixels", func(d *Document) error { _, err := d.DecodeChannel(nil, 0, 0); return err },
			Limits{MaxDecodePixels: 5}},
		{"channel-bytes", func(d *Document) error { _, err := d.DecodeChannel(nil, 0, 0); return err },
			Limits{MaxDecodeBytes: 5}},
		{"layer-total-bytes", func(d *Document) error { _, err := d.DecodeLayer(nil, 0); return err },
			Limits{MaxDecodeBytes: 11}},
		{"composite-total-bytes", func(d *Document) error { _, err := d.DecodeComposite(nil); return err },
			Limits{MaxDecodeBytes: 11}},
		{"composite-single-zip-work", func(d *Document) error { _, err := d.DecodeCompositeChannel(nil, 0); return err },
			Limits{MaxDecodeBytes: 11}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 2, 1)
			document.Layers[0].Channels = append(document.Layers[0].Channels, document.Layers[0].Channels[0])
			document.Header.Channels = 2
			reader := &decodeReadCounter{reader: document.reader}
			document.reader = reader
			limits, err := loadLimits([]LoadOptions{{Limits: test.limits}})
			if err != nil {
				t.Fatal(err)
			}
			document.limits = limits
			if err := test.method(document); !errors.Is(err, ErrLimit) || reader.reads != 0 {
				t.Fatalf("error = %v, source reads = %d", err, reader.reads)
			}
		})
	}
	// RGB channels share one six-pixel geometry; MaxDecodePixels is not multiplied by channel count.
	document := decodeTestDocument(bytes.Repeat([]byte{1}, 18), 3, 2, 8, 0, 1)
	document.Header.Channels = 3
	document.limits.MaxDecodePixels = 6
	if _, err := document.DecodeComposite(nil); err != nil {
		t.Fatal(err)
	}
}

type decodeReadCounter struct {
	reader io.ReaderAt
	reads  int
	err    error
	cancel context.CancelFunc
}

func (r *decodeReadCounter) ReadAt(data []byte, offset int64) (int, error) {
	r.reads++
	if r.err != nil {
		return 0, r.err
	}
	count, err := r.reader.ReadAt(data, offset)
	if r.cancel != nil {
		r.cancel()
	}
	return count, err
}

func TestDecodeCanceledClosedAndIOErrors(t *testing.T) {
	for _, compression := range []uint16{0, 1, 2, 3} {
		data := []byte{1, 2, 3, 4, 5, 6}
		if compression == 1 {
			data = []byte{0, 4, 0, 4, 2, 1, 2, 3, 2, 4, 5, 6}
		}
		if compression >= 2 {
			data = decodeTestZIP(t, data)
		}
		document := decodeTestDocument(data, 3, 2, 8, compression, 1)
		ctx, cancel := context.WithCancel(context.Background())
		document.reader = &decodeReadCounter{reader: document.reader, cancel: cancel}
		plane, err := document.DecodeChannel(ctx, 0, 0)
		cancel()
		if plane != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("compression %d: result = %+v, error = %v", compression, plane, err)
		}
	}
	document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 0, 1)
	underlying := errors.New("source I/O failure")
	document.reader = &decodeReadCounter{err: underlying}
	if plane, err := document.DecodeChannel(nil, 0, 0); plane != nil ||
		!errors.Is(err, ErrMalformed) || !errors.Is(err, underlying) {
		t.Fatalf("underlying error was not preserved: %v", err)
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	for _, method := range []func() error{
		func() error { _, err := document.DecodeChannel(nil, 0, 0); return err },
		func() error { _, err := document.DecodeLayer(nil, 0); return err },
		func() error { _, err := document.DecodeComposite(nil); return err },
		func() error { _, err := document.DecodeCompositeChannel(nil, 0); return err },
	} {
		if err := method(); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed decode error = %v", err)
		}
	}
}

func TestDecodeMaskGeometryAndEmptyChannels(t *testing.T) {
	document := decodeTestDocument([]byte{0xab, 0xcd}, 2, 1, 8, 0, 1)
	document.Layers[0].Bounds = image.Rect(-20, -30, 40, 50)
	document.Layers[0].Channels[0].ID = -3
	document.Layers[0].Channels[0].Bounds = image.Rect(-2, 7, -1, 9)
	plane, err := document.DecodeChannel(nil, 0, 0)
	if err != nil || plane.ID != -3 || plane.Width != 1 || plane.Height != 2 || plane.RowBytes != 1 {
		t.Fatalf("mask geometry decode = %+v, %v", plane, err)
	}
	for _, compression := range []uint16{0, 1, 2, 3} {
		data := []byte{}
		if compression >= 2 {
			data = decodeHex(t, "789c030000000001")
		}
		document = decodeTestDocument(data, 0, 0, 8, compression, 1)
		plane, err = document.DecodeChannel(nil, 0, 0)
		if err != nil || plane == nil || len(plane.Data) != 0 {
			t.Fatalf("empty compression %d: result = %+v, error = %v", compression, plane, err)
		}
	}
}

func TestDecodeOddPayloadAlignment(t *testing.T) {
	for _, test := range []struct {
		compression uint16
		data        []byte
	}{
		{0, []byte{1, 2, 3, 0xab}},
		{1, []byte{0, 5, 1, 1, 2, 0, 3, 0xab}},
	} {
		// The encoded RLE row has an odd five-byte payload and one original alignment byte.
		document := decodeTestDocument(test.data, 3, 1, 8, test.compression, 1)
		plane, err := document.DecodeChannel(nil, 0, 0)
		if err != nil || !bytes.Equal(plane.Data, []byte{1, 2, 3}) {
			t.Fatalf("compression %d padding: %+v, %v", test.compression, plane, err)
		}
		pixels, err := document.DecodeComposite(nil)
		if err != nil || !bytes.Equal(pixels.Channels[0].Data, []byte{1, 2, 3}) {
			t.Fatalf("compression %d composite padding: %+v, %v", test.compression, pixels, err)
		}
	}
}

func TestDecodeUnsupportedAndOverflow(t *testing.T) {
	for _, test := range []struct {
		name   string
		update func(*Document)
		kind   error
	}{
		{"invalid-depth", func(d *Document) { d.Header.Depth = 2 }, ErrUnsupported},
		{"invalid-compression", func(d *Document) { d.Layers[0].Channels[0].Compression = 4 }, ErrUnsupported},
		{"bitmap-prediction", func(d *Document) {
			d.Header.Depth = 1
			d.Layers[0].Channels[0].Compression = 3
		}, ErrUnsupported},
		{"negative-bounds", func(d *Document) {
			d.Layers[0].Channels[0].Bounds = image.Rectangle{Min: image.Pt(2, 2), Max: image.Pt(1, 1)}
		}, ErrMalformed},
		{"overflow-bounds", func(d *Document) {
			intLimit := int(^uint(0) >> 1)
			d.Layers[0].Channels[0].Bounds = image.Rectangle{
				Min: image.Pt(-intLimit, -intLimit), Max: image.Pt(intLimit, intLimit),
			}
		}, ErrLimit},
		{"span-outside-source", func(d *Document) {
			d.Layers[0].Channels[0].Data = Span{Offset: 100, Length: 6}
		}, ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 0, 1)
			test.update(document)
			plane, err := document.DecodeChannel(nil, 0, 0)
			if plane != nil || !errors.Is(err, test.kind) {
				t.Fatalf("result = %+v, error = %v, expected %v", plane, err, test.kind)
			}
		})
	}
	for _, indexes := range [][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		document := decodeTestDocument([]byte{1}, 1, 1, 8, 0, 1)
		if _, err := document.DecodeChannel(nil, indexes[0], indexes[1]); !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid indexes %v: %v", indexes, err)
		}
	}
}

func TestDecodeZIPBombBoundedByDimensions(t *testing.T) {
	data := decodeTestZIP(t, bytes.Repeat([]byte{0}, 1<<20))
	document := decodeTestDocument(data, 3, 2, 8, 2, 1)
	if plane, err := document.DecodeChannel(nil, 0, 0); plane != nil || !errors.Is(err, ErrMalformed) {
		t.Fatalf("oversized ZIP output: %+v, %v", plane, err)
	}
}

func TestDecodeConcurrentOwnedResults(t *testing.T) {
	document := decodeTestDocument(decodeHex(t, "789c6364626661650300003e0016"), 3, 2, 8, 2, 1)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			plane, err := document.DecodeChannel(nil, 0, 0)
			if err != nil {
				t.Error(err)
				return
			}
			plane.Data[0] = 0xff
		})
	}
	workers.Wait()
	plane, err := document.DecodeChannel(nil, 0, 0)
	if err != nil || plane.Data[0] != 1 {
		t.Fatalf("concurrent result changed source: %+v, %v", plane, err)
	}
}
