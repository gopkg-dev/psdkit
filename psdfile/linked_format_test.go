package psdfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

type linkedSignatureReader struct {
	data  []byte
	span  Span
	cause error
	reads int
}

func (reader *linkedSignatureReader) ReadAt(data []byte, offset int64) (int, error) {
	count, err := bytes.NewReader(reader.data).ReadAt(data, offset)
	if reader.span.Length != 0 && offset == reader.span.Offset && len(data) == 4 {
		reader.reads++
		if reader.cause != nil {
			return count, reader.cause
		}
	}
	return count, err
}

func TestLinkedDocumentFormatAndSignatureReadCauses(t *testing.T) {
	payload := make([]byte, 32)
	copy(payload, []byte{'I', 'I', 42, 0})
	data := linkedFixtureDocument(1, "lnkD", linkedFixtureEntry("liFD", 1, nil, payload, nil))
	reader := &linkedSignatureReader{data: data}
	parent, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	records, err := parent.LinkedFiles(nil)
	if err != nil || len(records) != 1 {
		t.Fatalf("linked records: %v", err)
	}
	reader.span = records[0].Data
	child, err := parent.OpenLinkedDocument(nil, records[0])
	var detail *Error
	if child != nil || !errors.Is(err, ErrUnsupported) || !errors.As(err, &detail) ||
		detail.Path != "linked.format" || detail.Offset != reader.span.Offset {
		t.Fatalf("non-PSD signature must report its actual format boundary: %v", err)
	}
	reader.cause = errors.New("complete signature accompanied by source failure")
	if child, err := parent.OpenLinkedDocument(nil, records[0]); child != nil || !errors.Is(err, reader.cause) {
		t.Fatalf("signature lost the source failure: %v", err)
	}
	reader.cause = io.EOF
	if child, err := parent.OpenLinkedDocument(nil, records[0]); child != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("complete signature with EOF did not retain format classification: %v", err)
	}
	reader.cause = nil
	reader.reads = 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if child, err := parent.OpenLinkedDocument(ctx, records[0]); child != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled signature read: %v", err)
	}
	if reader.reads != 0 {
		t.Fatal("canceled signature lookup performed a source read")
	}
}
