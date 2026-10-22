package psdfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"testing"
)

func TestRegionPreservesSourcePrecision(t *testing.T) {
	for _, testCase := range []struct {
		name               string
		width              int
		depth, compression uint16
		encoded, expected  string
		rectangle          image.Rectangle
	}{
		{"bitmap-unaligned", 9, 1, 0, "aa80ff40", "54f8", image.Rect(3, 0, 9, 2)},
		{"bitmap-cross-byte", 9, 1, 0, "aa80ff40", "4080", image.Rect(7, 0, 9, 2)},
		{"raw8", 3, 8, 0, "0a1400050404", "14000404", image.Rect(1, 0, 3, 2)},
		{"raw16", 3, 16, 0, "fffe00018000000100000001", "0001800000000001", image.Rect(1, 0, 3, 2)},
		{"raw32-nan-bits", 3, 32, 0, "3f8000007fc0123480000000ffffffff000000003f000001",
			"7fc0123480000000000000003f000001", image.Rect(1, 0, 3, 2)},
		{"zip8", 3, 8, 2, "789c6364626661650300003e0016", "02030506", image.Rect(1, 0, 3, 2)},
		{"prediction8", 3, 8, 3, "789ce3e27ac3fa9f010006310205", "14000404", image.Rect(1, 0, 3, 2)},
		{"prediction16", 3, 16, 3, "789cfbff8f81b9fe3f03e37f20060028f5057f",
			"0001800000000001", image.Rect(1, 0, 3, 2)},
		{"prediction32", 3, 32, 3, "789cb3776064707060107ac76072e63fa3fd014686ff20c4080051180702",
			"7fc0123480000000000000003f000001", image.Rect(1, 0, 3, 2)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := decodeTestDocument(decodeHex(t, testCase.encoded), testCase.width, 2,
				testCase.depth, testCase.compression, 2)
			document.Layers[0].Channels[0].Bounds = image.Rect(-20, 70, testCase.width-20, 72)
			plane, err := document.DecodeChannelRegion(nil, 0, 0, testCase.rectangle)
			if err != nil {
				t.Fatal(err)
			}
			if plane.ID != -1 || plane.Depth != testCase.depth || plane.Width != testCase.rectangle.Dx() ||
				plane.Height != testCase.rectangle.Dy() || plane.RowBytes != (plane.Width*int(plane.Depth)+7)/8 {
				t.Fatalf("region metadata changed: %+v", plane)
			}
			expected := decodeHex(t, testCase.expected)
			if !bytes.Equal(plane.Data, expected) {
				t.Fatalf("source region = %x, expected %x", plane.Data, expected)
			}
			plane.Data[0] ^= 255
			again, err := document.DecodeChannelRegion(nil, 0, 0, testCase.rectangle)
			if err != nil || !bytes.Equal(again.Data, expected) {
				t.Fatalf("region output is not independently owned: %v", err)
			}
		})
	}
}

func TestRegionRLEPSDRowsAndPSBCounts(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, testCase := range []struct {
			name      string
			width     int
			depth     uint16
			rows      string
			counts    []uint32
			expected  string
			rectangle image.Rectangle
		}{
			{"bitmap", 17, 1, "fe078002010203", []uint32{3, 4}, "e0802000", image.Rect(5, 0, 14, 2)},
			{"bytes", 3, 8, "fe078002010203", []uint32{3, 4}, "0702", image.Rect(1, 0, 2, 2)},
			{"words", 3, 16, "05fffe00018000fb00", []uint32{7, 2}, "0001800000000000", image.Rect(1, 0, 3, 2)},
			{"float-bits", 3, 32, "f5fff500", []uint32{2, 2}, "ffffffffffffffff0000000000000000",
				image.Rect(1, 0, 3, 2)},
		} {
			t.Run(fmt.Sprintf("version%d/%s", version, testCase.name), func(t *testing.T) {
				data := regionRLECounts(testCase.counts, version)
				data = append(data, decodeHex(t, testCase.rows)...)
				document := decodeTestDocument(data, testCase.width, 2, testCase.depth, 1, version)
				plane, err := document.DecodeChannelRegion(nil, 0, 0, testCase.rectangle)
				if err != nil || !bytes.Equal(plane.Data, decodeHex(t, testCase.expected)) {
					t.Fatalf("RLE region differs: %+v, %v", plane, err)
				}
			})
		}
	}
}

func regionRLECounts(counts []uint32, version uint16) []byte {
	var data []byte
	for _, count := range counts {
		if version == 2 {
			data = append(data, byte(count>>24), byte(count>>16))
		}
		data = append(data, byte(count>>8), byte(count))
	}
	return data
}

type regionRead struct {
	offset int64
	length int
}

type regionRecordingReader struct {
	reader io.ReaderAt
	reads  []regionRead
	err    error
	cancel context.CancelFunc
}

func (reader *regionRecordingReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.reads = append(reader.reads, regionRead{offset, len(data)})
	if reader.err != nil {
		return 0, reader.err
	}
	count, err := reader.reader.ReadAt(data, offset)
	if reader.cancel != nil {
		reader.cancel()
	}
	return count, err
}

func TestRegionRawReadsOnlyRequestedSegments(t *testing.T) {
	document := decodeTestDocument(bytes.Repeat([]byte{73}, 10_000), 100, 100, 8, 0, 2)
	document.limits.MaxDecodePixels = 4
	document.limits.MaxDecodeBytes = 4
	reader := &regionRecordingReader{reader: document.reader}
	document.reader = reader
	if _, err := document.DecodeChannel(nil, 0, 0); !errors.Is(err, ErrLimit) || len(reader.reads) != 0 {
		t.Fatalf("full source should exceed the configured budget: %v", err)
	}
	plane, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(3, 70, 5, 72))
	if err != nil || !bytes.Equal(plane.Data, []byte{73, 73, 73, 73}) {
		t.Fatalf("small tile from a large source was rejected: %v", err)
	}
	if len(reader.reads) != 2 || reader.reads[0] != (regionRead{7003, 2}) ||
		reader.reads[1] != (regionRead{7103, 2}) {
		t.Fatalf("unrequested Raw samples were read: %+v", reader.reads)
	}
}

func TestRegionRLESkipsOtherRowsAndLiteralSegments(t *testing.T) {
	data := regionRLECounts([]uint32{9, 9, 9}, 2)
	data = append(data, []byte{7, 1, 2, 3, 4, 5, 6, 7, 8}...)
	data = append(data, []byte{7, 11, 12, 13, 14, 15, 16, 17, 18}...)
	data = append(data, []byte{7, 21, 22, 23, 24, 25, 26, 27, 28}...)
	document := decodeTestDocument(data, 8, 3, 8, 1, 2)
	document.limits.MaxDecodePixels = 2
	document.limits.MaxDecodeBytes = 10
	reader := &regionRecordingReader{reader: document.reader}
	document.reader = reader
	plane, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(2, 1, 4, 2))
	if err != nil || !bytes.Equal(plane.Data, []byte{13, 14}) {
		t.Fatalf("RLE literal region differs: %+v, %v", plane, err)
	}
	expectedReads := []regionRead{{0, 4}, {4, 4}, {8, 4}, {21, 1}, {24, 2}}
	if fmt.Sprint(reader.reads) != fmt.Sprint(expectedReads) {
		t.Fatalf("RLE read unrequested literal data or rows: %+v", reader.reads)
	}
}

func TestRegionZIPValidatesUnselectedDataAndTerminator(t *testing.T) {
	for _, testCase := range []struct {
		name string
		data []byte
	}{
		{"short-before-region", decodeTestZIP(t, []byte{1, 2})},
		{"short-after-region", decodeTestZIP(t, []byte{1, 2, 3, 4, 5})},
		{"long", decodeTestZIP(t, []byte{1, 2, 3, 4, 5, 6, 7})},
		{"trailing", append(decodeTestZIP(t, []byte{1, 2, 3, 4, 5, 6}), 0)},
		{"concatenated", append(decodeTestZIP(t, []byte{1, 2, 3, 4, 5, 6}), decodeTestZIP(t, nil)...)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := decodeTestDocument(testCase.data, 3, 2, 8, 2, 2)
			plane, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 1, 1))
			if plane != nil || !errors.Is(err, ErrMalformed) {
				t.Fatalf("invalid remainder of ZIP stream was ignored: %v", err)
			}
		})
	}
	valid := decodeTestZIP(t, []byte{1, 2, 3, 4, 5, 6})
	valid[len(valid)-1] ^= 1
	document := decodeTestDocument(valid, 3, 2, 8, 2, 2)
	if _, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("checksum outside the selected region was ignored: %v", err)
	}
}
