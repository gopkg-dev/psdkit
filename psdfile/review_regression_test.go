package psdfile_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type reviewFullReadError struct {
	data []byte
	err  error
}

func (reader *reviewFullReadError) ReadAt(data []byte, offset int64) (int, error) {
	count, err := bytes.NewReader(reader.data).ReadAt(data, offset)
	if reader.err != nil {
		return count, reader.err
	}
	return count, err
}

func reviewFlatRGB8() []byte {
	var data bytes.Buffer
	data.WriteString("8BPS")
	_ = binary.Write(&data, binary.BigEndian, uint16(1))
	data.Write(make([]byte, 6))
	for _, value := range []any{uint16(3), uint32(1), uint32(1), uint16(8), uint16(3),
		uint32(0), uint32(0), uint32(0), uint16(0)} {
		_ = binary.Write(&data, binary.BigEndian, value)
	}
	data.Write([]byte{1, 2, 3})
	return data.Bytes()
}

func TestReviewRGB8SnapshotPreservesFullReadError(t *testing.T) {
	reader := &reviewFullReadError{data: reviewFlatRGB8()}
	source, err := psdfile.Read(reader, int64(len(reader.data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	underlying := errors.New("reader reported an I/O failure alongside complete bytes")
	reader.err = underlying
	result, err := source.Decode(nil)
	if result != nil || !errors.Is(err, underlying) {
		t.Fatalf("RGB8 snapshot lost a real underlying source error: result=%v, error=%v", result != nil, err)
	}
	reader.err = io.EOF
	if _, err := source.Decode(nil); err != nil {
		t.Fatalf("complete read with EOF should remain accepted: %v", err)
	}
}

func TestReviewReadSpanPreservesAccompanyingFullReadError(t *testing.T) {
	reader := &reviewFullReadError{data: reviewFlatRGB8()}
	source, err := psdfile.Read(reader, int64(len(reader.data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	span := psdfile.Span{Length: 8}
	cause := errors.New("complete span accompanied by a source I/O failure")
	reader.err = cause
	if data, err := source.ReadSpan(span); data != nil || !errors.Is(err, cause) {
		t.Fatalf("original span lost an accompanying error: %v/%v", data, err)
	}
	reader.err = io.EOF
	data, err := source.ReadSpan(span)
	if err != nil || !bytes.Equal(data, reader.data[:8]) {
		t.Fatalf("full span with EOF was rejected: %v", err)
	}
	if data, err := source.ReadSpan(psdfile.Span{}); err != nil || len(data) != 0 {
		t.Fatalf("empty span required an unnecessary source read: %v", err)
	}
}
