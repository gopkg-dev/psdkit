package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdvector"
)

func vectorPayloadFixture() []byte {
	data := binary.BigEndian.AppendUint32(nil, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	record := make([]byte, 26)
	binary.BigEndian.PutUint16(record, 6)
	data = append(data, record...)
	clear(record)
	binary.BigEndian.PutUint16(record[2:], 4)
	binary.BigEndian.PutUint16(record[4:], 1)
	binary.BigEndian.PutUint16(record[6:], 1)
	data = append(data, record...)
	for _, point := range [][2]uint32{{1 << 22, 1 << 22}, {3 << 22, 1 << 22}, {3 << 22, 3 << 22}, {1 << 22, 3 << 22}} {
		clear(record)
		binary.BigEndian.PutUint16(record, 2)
		for offset := 2; offset < 26; offset += 8 {
			binary.BigEndian.PutUint32(record[offset:], point[1])
			binary.BigEndian.PutUint32(record[offset+4:], point[0])
		}
		data = append(data, record...)
	}
	return data
}

func vectorSourceFixture(version uint16, payload []byte) []byte {
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 4)
	data = binary.BigEndian.AppendUint32(data, 4)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	resource := binary.BigEndian.AppendUint16([]byte("8BIM"), 4000)
	resource = append(resource, 0, 0)
	resource = binary.BigEndian.AppendUint32(resource, uint32(len(payload)))
	resource = append(resource, payload...)
	if len(payload)%2 != 0 {
		resource = append(resource, 0)
	}
	data = binary.BigEndian.AppendUint32(data, uint32(len(resource)))
	data = append(data, resource...)
	if version == 2 {
		data = binary.BigEndian.AppendUint64(data, 0)
	} else {
		data = binary.BigEndian.AppendUint32(data, 0)
	}
	data = binary.BigEndian.AppendUint16(data, 0)
	return append(data, make([]byte, 4*4*3)...)
}

type vectorCountReader struct {
	reader    *bytes.Reader
	reads     int
	fail      bool
	fullError error
}

func (reader *vectorCountReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) != 0 {
		reader.reads++
		if reader.fail {
			return 0, io.ErrUnexpectedEOF
		}
	}
	count, err := reader.reader.ReadAt(data, offset)
	if len(data) != 0 && count == len(data) && reader.fullError != nil {
		return count, reader.fullError
	}
	return count, err
}

func TestDecodeVectorMaskSourceOwnershipAndOriginalSpans(t *testing.T) {
	payload := vectorPayloadFixture()
	for _, version := range []uint16{1, 2} {
		source, err := Parse(vectorSourceFixture(version, payload))
		if err != nil {
			t.Fatal(err)
		}
		span := source.Resources[0].Data
		mask, err := source.DecodeVectorMaskBlock(nil, AdditionalBlock{Key: "vmsk", Data: span})
		if err != nil || mask.Span.Offset != span.Offset || mask.Span.Length != span.Length ||
			mask.Subpaths[0].Knots[0].Anchor.X != 1 {
			t.Fatalf("PSD/PSB vector data or original spans lost: %v", err)
		}
		original, err := source.ReadSpan(span)
		if err != nil || !bytes.Equal(original, payload) {
			t.Fatalf("typed decode changed original payload: %v", err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		pixels, err := psdvector.Rasterize(nil, mask, image.Rect(0, 0, 4, 4))
		if err != nil || pixels.Pix[5] != 255 || mask.Records[2].Raw[1] != 2 {
			t.Fatalf("typed vector data borrowed source lifetime: %v", err)
		}
		if _, err := source.DecodeVectorMask(nil, span); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed source accepted vector decode: %v", err)
		}
	}
}

func TestDecodeVectorMaskBudgetPrecedesPayloadReads(t *testing.T) {
	data := vectorSourceFixture(1, vectorPayloadFixture())
	for _, limits := range []Limits{{MaxDecodeBytes: 64}, {MaxDescriptorItems: 1}, {MaxReadBytes: 64}} {
		reader := &vectorCountReader{reader: bytes.NewReader(data)}
		source, err := Read(reader, int64(len(data)), LoadOptions{Limits: limits})
		if err != nil {
			t.Fatal(err)
		}
		reader.reads = 0
		_, err = source.DecodeVectorMask(nil, source.Resources[0].Data)
		if !errors.Is(err, ErrLimit) || reader.reads != 0 {
			t.Fatalf("insufficient budget read retained payload: %v, reads=%d", err, reader.reads)
		}
		_ = source.Close()
	}
}

func TestDecodeHeaderlessSourcePathRetainsOwnedRecords(t *testing.T) {
	payload := vectorPayloadFixture()[8:]
	source, err := Parse(vectorSourceFixture(1, payload))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	span := source.Resources[0].Data
	path, err := source.DecodePath(nil, span)
	if err != nil || path.Version != 0 || path.Records[0].Span.Offset != span.Offset || path.HeaderSpan.Length != 0 {
		t.Fatalf("saved path resource was treated as a mask header: %v", err)
	}
	pixels, err := psdvector.Rasterize(nil, path, image.Rect(0, 0, 4, 4))
	if err != nil || pixels.Pix[5] != 255 {
		t.Fatalf("saved path geometry did not render: %v", err)
	}
	for _, id := range []uint16{1025, 2000, 2997} {
		resourcePath, err := source.DecodePathResource(nil, ResourceBlock{ID: id, Data: span})
		if err != nil || resourcePath.Records[0].Span.Offset != span.Offset {
			t.Fatalf("saved/working path resource did not preserve offsets: %v", err)
		}
	}
	for _, id := range []uint16{1000, 2998, 2999, 4000} {
		_, err := source.DecodePathResource(nil, ResourceBlock{ID: id, Data: span})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("nongeometry or clipping-name resource was treated as path records: %v", err)
		}
	}
}

func TestDecodeVectorMaskIOErrorsCancellationAndUnsupportedVersion(t *testing.T) {
	data := vectorSourceFixture(1, vectorPayloadFixture())
	reader := &vectorCountReader{reader: bytes.NewReader(data)}
	source, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	span := source.Resources[0].Data
	reader.fail = true
	_, err = source.DecodeVectorMask(nil, span)
	if !errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("vector source failure lost category or cause: %v", err)
	}
	reader.fail = false
	reader.fullError = errors.New("vector source returned bytes with a failure")
	_, err = source.DecodeVectorMask(nil, span)
	if !errors.Is(err, ErrMalformed) || !errors.Is(err, reader.fullError) {
		t.Fatalf("full payload incorrectly suppressed ReaderAt failure: %v", err)
	}
	reader.fullError = io.EOF
	if _, err := source.DecodeVectorMask(nil, span); err != nil {
		t.Fatalf("complete payload with EOF was rejected: %v", err)
	}
	reader.fullError = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.DecodeVectorMask(ctx, span); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled source vector decode: %v", err)
	}
	_, err = source.DecodeVectorMaskBlock(nil, AdditionalBlock{Key: "abcd", Data: span})
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nonvector additional block accepted: %v", err)
	}
	versionData := vectorPayloadFixture()
	binary.BigEndian.PutUint32(versionData, 4)
	unsupported, err := Parse(vectorSourceFixture(1, versionData))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unsupported.Close() }()
	_, err = unsupported.DecodeVectorMask(nil, unsupported.Resources[0].Data)
	if !errors.Is(err, ErrUnsupported) || !errors.Is(err, psdvector.ErrUnsupported) {
		t.Fatalf("unsupported vector version lost categories: %v", err)
	}
}
