package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"testing"
)

func TestRegionCompositeSharedStreams(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			t.Run(fmt.Sprintf("version%d/compression%d", version, compression), func(t *testing.T) {
				original := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18}
				data := original
				switch compression {
				case 1:
					data = regionRLECounts([]uint32{4, 4, 4, 4, 4, 4}, version)
					for row := range 6 {
						data = append(data, 2)
						data = append(data, original[row*3:row*3+3]...)
					}
				case 2:
					data = decodeTestZIP(t, original)
				case 3:
					data = decodeTestZIP(t, []byte{1, 1, 1, 4, 1, 1, 7, 1, 1, 10, 1, 1, 13, 1, 1, 16, 1, 1})
				}
				document := decodeTestDocument(data, 3, 2, 8, compression, version)
				document.Header.Channels = 3
				document.limits.MaxDecodePixels = 4
				plane, err := document.DecodeCompositeChannelRegion(nil, 1, image.Rect(1, 0, 3, 2))
				if err != nil || plane.ID != 1 || !bytes.Equal(plane.Data, []byte{8, 9, 11, 12}) {
					t.Fatalf("shared composite region differs: %+v, %v", plane, err)
				}
			})
		}
	}
}

func TestRegionBudgetsIncludeWorkingStorageBeforeReads(t *testing.T) {
	zip8 := decodeTestZIP(t, []byte{1, 2, 3, 4, 5, 6})
	zip32 := decodeHex(t, "789cb3776064707060107ac76072e63fa3fd014686ff20c4080051180702")
	for _, testCase := range []struct {
		name               string
		width              int
		depth, compression uint16
		data               []byte
		rectangle          image.Rectangle
		pixels, bytes      int64
	}{
		{"tile-pixels", 3, 8, 0, []byte{1, 2, 3, 4, 5, 6}, image.Rect(0, 0, 3, 2), 5, 100},
		{"tile-output", 3, 8, 0, []byte{1, 2, 3, 4, 5, 6}, image.Rect(1, 0, 3, 2), 100, 3},
		{"bitmap-repack", 9, 1, 0, []byte{0xaa, 0x80, 0xff, 0x40}, image.Rect(7, 0, 9, 2), 100, 3},
		{"rle-requested-counts", 3, 8, 1, []byte{0, 3, 0, 3, 0xfe, 7, 0x80, 0xfe, 8, 0x80},
			image.Rect(1, 0, 2, 1), 100, 8},
		{"zip-row-and-input", 3, 8, 2, zip8, image.Rect(0, 0, 1, 1), 100, int64(len(zip8)) + 3},
		{"zip-prediction-shuffle", 3, 32, 3, zip32, image.Rect(0, 0, 1, 1), 100, int64(len(zip32)) + 27},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			document := decodeTestDocument(testCase.data, testCase.width, 2, testCase.depth, testCase.compression, 1)
			document.limits.MaxDecodePixels = testCase.pixels
			document.limits.MaxDecodeBytes = testCase.bytes
			reader := &regionRecordingReader{reader: document.reader}
			document.reader = reader
			plane, err := document.DecodeChannelRegion(nil, 0, 0, testCase.rectangle)
			if plane != nil || !errors.Is(err, ErrLimit) || len(reader.reads) != 0 {
				t.Fatalf("working budget was checked after source I/O: %v, %+v", err, reader.reads)
			}
		})
	}
	document := decodeTestDocument(zip8, 3, 2, 8, 2, 2)
	document.limits.MaxDecodePixels = 1
	document.limits.MaxDecodeBytes = int64(len(zip8)) + 4
	plane, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(2, 1, 3, 2))
	if err != nil || !bytes.Equal(plane.Data, []byte{6}) {
		t.Fatalf("exact output/row/buffer budget rejected: %v", err)
	}
}

func TestRegionInvalidRectanglesAndEmptyRequests(t *testing.T) {
	document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 0, 2)
	reader := &regionRecordingReader{reader: document.reader}
	document.reader = reader
	for _, rectangle := range []image.Rectangle{
		image.Rect(-1, 0, 1, 1), image.Rect(0, -1, 1, 1), image.Rect(2, 0, 4, 1), image.Rect(0, 1, 1, 3),
		{Min: image.Pt(2, 1), Max: image.Pt(1, 2)},
	} {
		if plane, err := document.DecodeChannelRegion(nil, 0, 0, rectangle); plane != nil || !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid channel-local rectangle accepted: %v", err)
		}
	}
	for _, rectangle := range []image.Rectangle{image.Rect(1, 1, 1, 2), image.Rect(1, 2, 3, 2)} {
		plane, err := document.DecodeChannelRegion(nil, 0, 0, rectangle)
		if err != nil || len(plane.Data) != 0 || plane.Width != rectangle.Dx() || plane.Height != rectangle.Dy() {
			t.Fatalf("empty region metadata differs: %+v, %v", plane, err)
		}
	}
	if len(reader.reads) != 0 {
		t.Fatal("empty/invalid region accessed sample bytes")
	}
	for _, call := range []func() error{
		func() error { _, err := document.DecodeChannelRegion(nil, -1, 0, image.Rect(0, 0, 1, 1)); return err },
		func() error { _, err := document.DecodeChannelRegion(nil, 0, -1, image.Rect(0, 0, 1, 1)); return err },
		func() error {
			_, err := document.DecodeCompositeChannelRegion(nil, 1, image.Rect(0, 0, 1, 1))
			return err
		},
	} {
		if err := call(); !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid channel index accepted: %v", err)
		}
	}
}

func TestRegionCanceledClosedAndReaderErrors(t *testing.T) {
	underlying := errors.New("region source failure")
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
		reader := &regionRecordingReader{reader: document.reader, cancel: cancel}
		document.reader = reader
		plane, err := document.DecodeChannelRegion(ctx, 0, 0, image.Rect(0, 0, 1, 1))
		if plane != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("compression %d ignored mid-read cancellation: %v", compression, err)
		}
		cancel()
		reader.cancel = nil
		reader.err = underlying
		plane, err = document.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 1, 1))
		if plane != nil || !errors.Is(err, underlying) || !errors.Is(err, ErrMalformed) {
			t.Fatalf("compression %d lost original ReaderAt error: %v", compression, err)
		}
		reader.reads = nil
		_, err = document.DecodeChannelRegion(ctx, 0, 0, image.Rect(0, 0, 1, 1))
		if !errors.Is(err, context.Canceled) || len(reader.reads) != 0 {
			t.Fatalf("pre-canceled region performed I/O: %v", err)
		}
		if err := document.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed layer region was decoded: %v", err)
		}
		if _, err := document.DecodeCompositeChannelRegion(nil, 0, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed composite region was decoded: %v", err)
		}
	}
}

type regionFullErrorReader struct {
	reader io.ReaderAt
	err    error
}

func (reader regionFullErrorReader) ReadAt(data []byte, offset int64) (int, error) {
	count, _ := reader.reader.ReadAt(data, offset)
	return count, reader.err
}

func TestRegionPreservesErrorReturnedWithFullRead(t *testing.T) {
	underlying := errors.New("full-count source failure")
	document := decodeTestDocument([]byte{1, 2, 3, 4, 5, 6}, 3, 2, 8, 0, 2)
	document.reader = regionFullErrorReader{reader: document.reader, err: underlying}
	_, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 1, 1))
	if !errors.Is(err, underlying) || !errors.Is(err, ErrMalformed) {
		t.Fatalf("io.ReadFull silently discarded a full-count source failure: %v", err)
	}
	document.reader = regionFullErrorReader{reader: bytes.NewReader([]byte{1, 2, 3, 4, 5, 6}), err: io.EOF}
	if _, err := document.DecodeChannelRegion(nil, 0, 0, image.Rect(0, 0, 1, 1)); err != nil {
		t.Fatalf("full-count EOF incorrectly rejected valid samples: %v", err)
	}
}

func TestRegionZIPWorkLimitRequiresExplicitLargeInflation(t *testing.T) {
	data := decodeTestZIP(t, make([]byte, 1024*1024))
	document := decodeTestDocument(data, 1024, 1024, 8, 2, 2)
	document.limits.MaxDecodePixels = 1
	document.limits.MaxDecodeBytes = int64(len(data)) + 1025
	reader := &regionRecordingReader{reader: document.reader}
	document.reader = reader
	rectangle := image.Rect(512, 700, 513, 701)
	if _, err := document.DecodeChannelRegion(nil, 0, 0, rectangle); !errors.Is(err, ErrLimit) || len(reader.reads) != 0 {
		t.Fatalf("small ROI silently allowed unbounded whole-stream inflation: %v", err)
	}
	options := RegionOptions{MaxInflatedBytes: 1024 * 1024}
	plane, err := document.DecodeChannelRegion(nil, 0, 0, rectangle, options)
	if err != nil || !bytes.Equal(plane.Data, []byte{0}) {
		t.Fatalf("explicit full-stream work budget did not permit bounded storage: %v", err)
	}
	reader.reads = nil
	options.MaxWorkingBytes = 1024
	_, err = document.DecodeChannelRegion(nil, 0, 0, rectangle, options)
	if !errors.Is(err, ErrLimit) || len(reader.reads) != 0 {
		t.Fatalf("lower shared work-storage limit was ignored: %v", err)
	}
	for _, invalid := range []RegionOptions{
		{MaxWorkingBytes: -1}, {MaxInflatedBytes: -1}, {MaxRLEWorkBytes: -1},
		{MaxWorkingBytes: document.limits.MaxDecodeBytes + 1},
	} {
		_, err = document.DecodeChannelRegion(nil, 0, 0, rectangle, invalid)
		if !errors.Is(err, ErrLimit) || len(reader.reads) != 0 {
			t.Fatalf("invalid work budget performed I/O or raised source allocation limit: %v", err)
		}
	}
	_, err = document.DecodeChannelRegion(nil, 0, 0, rectangle, options, options)
	if !errors.Is(err, ErrMalformed) || len(reader.reads) != 0 {
		t.Fatalf("multiple region options accepted: %v", err)
	}
	document.Header.Channels = 2
	if _, err := document.DecodeCompositeChannelRegion(nil, 0, rectangle,
		RegionOptions{MaxInflatedBytes: 1024 * 1024}); !errors.Is(err, ErrLimit) || len(reader.reads) != 0 {
		t.Fatalf("unselected shared ZIP channel escaped the full-stream work budget: %v", err)
	}
}

func TestRegionRLEWorkIncludesTableAndSelectedRows(t *testing.T) {
	data := []byte{0, 4, 0, 4, 2, 1, 2, 3, 2, 4, 5, 6}
	document := decodeTestDocument(data, 3, 2, 8, 1, 1)
	reader := &regionRecordingReader{reader: document.reader}
	document.reader = reader
	rectangle := image.Rect(1, 1, 2, 2)
	_, err := document.DecodeChannelRegion(nil, 0, 0, rectangle, RegionOptions{MaxRLEWorkBytes: 3})
	if !errors.Is(err, ErrLimit) || len(reader.reads) != 0 {
		t.Fatalf("RLE table scan started before work budget validation: %v", err)
	}
	_, err = document.DecodeChannelRegion(nil, 0, 0, rectangle, RegionOptions{MaxRLEWorkBytes: 7})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("selected compressed RLE row escaped work budget: %v", err)
	}
	for _, read := range reader.reads {
		if read.offset >= 4 {
			t.Fatal("RLE pixel payload read after scan-work exhaustion")
		}
	}
	plane, err := document.DecodeChannelRegion(nil, 0, 0, rectangle, RegionOptions{MaxRLEWorkBytes: 8})
	if err != nil || !bytes.Equal(plane.Data, []byte{5}) {
		t.Fatalf("exact table/selected-row work allowance rejected: %v", err)
	}
}

type regionSparsePSBReader struct {
	prefix      []byte
	length      int64
	planeLength int64
	pixelReads  []regionRead
}

func (reader *regionSparsePSBReader) ReadAt(data []byte, offset int64) (int, error) {
	if offset < 0 || offset >= reader.length {
		return 0, io.EOF
	}
	count := min(len(data), int(reader.length-offset))
	first := int64(len(reader.prefix))
	second := first + reader.planeLength + 6
	for index := range count {
		position := offset + int64(index)
		data[index] = 0
		if position < first {
			data[index] = reader.prefix[position]
		} else if position < first+reader.planeLength {
			data[index] = []byte{0x7f, 0xc0, 0x12, 0x34}[(position-first)%4]
		} else if position >= second {
			data[index] = []byte{0x7f, 0xc0, 0x12, 0x34}[(position-second)%4]
		}
	}
	if offset >= first && offset < first+reader.planeLength || offset >= second {
		reader.pixelReads = append(reader.pixelReads, regionRead{offset, count})
	}
	if count < len(data) {
		return count, io.EOF
	}
	return count, nil
}

func TestRegionSparse720GBPSBThroughPublicSourceReader(t *testing.T) {
	const width, height = 300_000, 300_000
	const planeLength = int64(width) * height * 4
	var record bytes.Buffer
	indexTestNumber(&record, int16(1))
	for _, coordinate := range []int32{0, 0, height, width} {
		indexTestNumber(&record, coordinate)
	}
	indexTestNumber(&record, uint16(1))
	indexTestNumber(&record, int16(0))
	indexTestNumber(&record, uint64(planeLength+2))
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	indexTestNumber(&record, uint32(12))
	record.Write(make([]byte, 8))
	record.Write([]byte{1, 'L', 0, 0})
	layerInfoLength := int64(record.Len()) + 2 + planeLength
	var prefix bytes.Buffer
	prefix.WriteString("8BPS")
	indexTestNumber(&prefix, uint16(2))
	prefix.Write(make([]byte, 6))
	indexTestNumber(&prefix, uint16(1))
	indexTestNumber(&prefix, uint32(height))
	indexTestNumber(&prefix, uint32(width))
	indexTestNumber(&prefix, uint16(32))
	indexTestNumber(&prefix, uint16(1))
	indexTestNumber(&prefix, uint32(0))
	indexTestNumber(&prefix, uint32(0))
	indexTestNumber(&prefix, uint64(8+layerInfoLength+4))
	indexTestNumber(&prefix, uint64(layerInfoLength))
	prefix.Write(record.Bytes())
	indexTestNumber(&prefix, uint16(0))
	reader := &regionSparsePSBReader{prefix: prefix.Bytes(), planeLength: planeLength,
		length: int64(prefix.Len()) + planeLength*2 + 6}
	document, err := Read(reader, reader.length, LoadOptions{Limits: Limits{
		MaxFileBytes: reader.length, MaxDecodePixels: 4, MaxDecodeBytes: 16,
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	if len(reader.pixelReads) != 0 {
		t.Fatal("indexing a sparse PSB accessed pixel samples")
	}
	if _, err := document.DecodeChannel(nil, 0, 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("full 360GB plane should exceed pixel budget: %v", err)
	}
	rectangle := image.Rect(200_000, 299_999, 200_002, 300_000)
	plane, err := document.DecodeChannelRegion(nil, 0, 0, rectangle)
	if err != nil || !bytes.Equal(plane.Data, []byte{0x7f, 0xc0, 0x12, 0x34, 0x7f, 0xc0, 0x12, 0x34}) {
		t.Fatalf("small native-bit tile from sparse PSB differs: %+v, %v", plane, err)
	}
	expectedOffset := int64(prefix.Len()) + int64(rectangle.Min.Y)*width*4 + int64(rectangle.Min.X)*4
	if len(reader.pixelReads) != 1 || reader.pixelReads[0] != (regionRead{expectedOffset, 8}) {
		t.Fatalf("large PSB read outside its eight-byte region: %+v", reader.pixelReads)
	}
	if binary.BigEndian.Uint32(plane.Data[:4]) != 0x7fc01234 {
		t.Fatal("32-bit NaN payload changed during region decode")
	}
}
