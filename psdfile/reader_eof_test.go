package psdfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type boundaryReaderAt struct {
	reader *bytes.Reader
}

func (r boundaryReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	count, err := r.reader.ReadAt(data, offset)
	if count == len(data) && offset+int64(count) == r.reader.Size() {
		err = io.EOF
	}
	return count, err
}

func TestIndexAndDescriptorAcceptCompleteBoundaryEOF(t *testing.T) {
	// These 29 saved grayscale samples also encode an independently built one-entry descriptor.
	descriptor := []byte{
		0, 0, 0, 0, 0, 0, 0, 0, 'T', 'e', 's', 't', 0, 0, 0, 1,
		0, 0, 0, 0, 'f', 'l', 'a', 'g', 'b', 'o', 'o', 'l', 1,
	}
	var encoded bytes.Buffer
	encoded.WriteString("8BPS")
	for _, value := range []any{
		uint16(1), [6]byte{}, uint16(1), uint32(1), uint32(len(descriptor)), uint16(8), uint16(1),
		uint32(0), uint32(0), uint32(0), uint16(0),
	} {
		if err := binary.Write(&encoded, binary.BigEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	encoded.Write(descriptor)
	reader := boundaryReaderAt{reader: bytes.NewReader(encoded.Bytes())}
	document, err := Read(reader, int64(encoded.Len()))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	value, err := document.DecodeDescriptor(document.Composite.Data, false)
	if err != nil || len(value.Entries) != 1 || value.Entries[0].Value != true {
		t.Fatalf("a complete ReaderAt read with EOF was rejected: %v", err)
	}
	// The index cursor must follow the same ReaderAt contract for structural fields.
	cursor := indexCursor{document: document, position: int64(encoded.Len() - 1), end: int64(encoded.Len()),
		path: "boundary"}
	if value := cursor.u8("flag"); value != 1 || cursor.err != nil {
		t.Fatalf("complete boundary structural read failed: %v", cursor.err)
	}
	short := indexCursor{document: document, position: int64(encoded.Len() - 1), end: int64(encoded.Len() + 1),
		path: "short"}
	short.u16("value")
	if !errors.Is(short.err, ErrMalformed) || !errors.Is(short.err, io.EOF) {
		t.Fatal("genuinely short ReaderAt read was accepted")
	}
}
