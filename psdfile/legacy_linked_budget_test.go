package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

type legacyLinkedBudgetReader struct {
	data      []byte
	forbidden []Span
	reads     int
	failure   error
	cancel    context.CancelFunc
}

func (reader *legacyLinkedBudgetReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.reads++
	for _, span := range reader.forbidden {
		if offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			return 0, errors.New("linked-name scan accessed a lazy name or content payload")
		}
	}
	count, err := bytes.NewReader(reader.data).ReadAt(data, offset)
	if reader.cancel != nil {
		reader.cancel()
	}
	if reader.failure != nil {
		return count, reader.failure
	}
	return count, err
}

func TestLegacyLinkedBudgetReadsOnlyStructureLengths(t *testing.T) {
	fixture := linkedFixtureEntry("liFD", 7, linkedFixtureDescriptor(descriptorFixtureEntry{
		key: "large", kind: "TEXT", data: descriptorFixtureUnicode("unread descriptor"),
	}), bytes.Repeat([]byte{0xa5}, 64<<10), []byte{1, 2, 3})
	reader := &legacyLinkedBudgetReader{data: linkedFixtureDocument(1, "lnk2", fixture)}
	source, err := Read(reader, int64(len(reader.data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	start := source.AdditionalInfo[0].Data.Offset
	for _, field := range []string{"id", "name", "data", "fileOpenDescriptor", "childDocumentID", "tail"} {
		span := fixture.fields[field]
		switch field {
		case "id":
			span.Offset++
			span.Length--
		case "name":
			span.Offset += 4
			span.Length -= 4
		}
		span.Offset += start
		reader.forbidden = append(reader.forbidden, span)
	}
	reader.reads = 0
	reservation, err := source.RGB8SnapshotMetadataBytesContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	name := fixture.fields["name"]
	units := int64(binary.BigEndian.Uint32(fixture.data[name.Offset : name.Offset+4]))
	expected := units*12 + 10*2 + 256
	if reservation != expected || reader.reads != 5 {
		t.Fatalf("scanner charged media or failed to bound name expansion: bytes=%d reads=%d", reservation, reader.reads)
	}
	source.mu.RLock()
	_, err = source.rgb8LinkedMetadataBytesLocked(context.Background(), reservation-1)
	source.mu.RUnlock()
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("exact linked-name snapshot budget not enforced: %v", err)
	}
}

func TestLegacyLinkedBudgetCancellationLifecycleAndFullReadError(t *testing.T) {
	fixture := linkedFixtureEntry("liFD", 1, nil, []byte{1, 2, 3}, nil)
	reader := &legacyLinkedBudgetReader{data: linkedFixtureDocument(1, "lnk2", fixture)}
	source, err := Read(reader, int64(len(reader.data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader.reads = 0
	if _, err := source.RGB8SnapshotMetadataBytesContext(ctx); !errors.Is(err, context.Canceled) || reader.reads != 0 {
		t.Fatalf("pre-canceled reservation accessed linked fields: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader.cancel = cancel
	if _, err := source.RGB8SnapshotMetadataBytesContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-field cancellation was replaced by a malformed-structure error: %v", err)
	}
	cancel()
	reader.cancel = nil
	underlying := errors.New("complete linked header with an I/O failure")
	reader.failure = underlying
	if _, err := source.RGB8SnapshotMetadataBytesContext(nil); !errors.Is(err, underlying) {
		t.Fatalf("full-count source failure was lost by structure scan: %v", err)
	}
	reader.failure = io.EOF
	if _, err := source.RGB8SnapshotMetadataBytesContext(nil); err != nil {
		t.Fatalf("complete structural read with EOF was rejected: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.RGB8SnapshotMetadataBytesContext(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source metadata was scanned: %v", err)
	}
}

func TestLegacyLinkedBudgetRejectsUnknownOrTruncatedStructure(t *testing.T) {
	for _, testCase := range []struct {
		name string
		edit func([]byte)
		kind error
	}{
		{"kind", func(data []byte) { copy(data[8:12], "liZZ") }, ErrUnsupported},
		{"version", func(data []byte) { binary.BigEndian.PutUint32(data[12:16], 8) }, ErrUnsupported},
		{"name-count", func(data []byte) { binary.BigEndian.PutUint32(data[27:31], 1<<21) }, ErrMalformed},
		{"record-length", func(data []byte) { binary.BigEndian.PutUint64(data[:8], uint64(len(data)+1)) }, ErrMalformed},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := linkedFixtureEntry("liFD", 1, nil, nil, nil)
			testCase.edit(fixture.data)
			source := linkedFixtureParse(t, linkedFixtureDocument(1, "lnk2", fixture))
			if _, err := source.RGB8SnapshotMetadataBytesContext(nil); !errors.Is(err, testCase.kind) {
				t.Fatalf("malformed or unknown structure was accepted: %v", err)
			}
		})
	}
}
