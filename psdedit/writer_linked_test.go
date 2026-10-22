package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type writerLinkedFixture struct {
	kind, id      string
	version       uint32
	data, payload []byte
	body          []byte
	sizeOffset    int
	dataOffset    int
	padding       []byte
}

func writerLinkedUnicode(value string) []byte {
	units := utf16.Encode([]rune(value))
	data := binary.BigEndian.AppendUint32(nil, uint32(len(units)))
	for _, unit := range units {
		data = binary.BigEndian.AppendUint16(data, unit)
	}
	return data
}

func writerLinkedDescriptor() []byte {
	data := binary.BigEndian.AppendUint32(nil, 16)
	data = append(data, writerLinkedUnicode("")...)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = append(data, "null"...)
	data = binary.BigEndian.AppendUint32(data, 0)
	return data
}

// writerLinkedEntry encodes wire fields independently of the production linked-file reader and writer.
func writerLinkedEntry(kind, id string, version uint32, payload []byte) writerLinkedFixture {
	body := binary.BigEndian.AppendUint32([]byte(kind), version)
	body = append(body, byte(len(id)))
	body = append(body, id...)
	body = append(body, writerLinkedUnicode("原内容😀.psb\x00")...)
	body = append(body, []byte("8BPS\x00\x87\x41\xff")...)
	sizeOffset := len(body)
	dataSize := len(payload)
	if kind == "liFA" || kind == "liFE" && version == 1 {
		dataSize = 0
	}
	body = binary.BigEndian.AppendUint64(body, uint64(dataSize))
	body = append(body, 1)
	body = append(body, writerLinkedDescriptor()...)
	switch kind {
	case "liFE":
		body = append(body, writerLinkedDescriptor()...)
		if version > 3 {
			body = binary.BigEndian.AppendUint32(body, 2026)
			body = append(body, 10, 12, 4, 23)
			body = binary.BigEndian.AppendUint64(body, math.Float64bits(19.5))
		}
		body = binary.BigEndian.AppendUint64(body, 123456789)
	case "liFA":
		body = append(body, 0, 0, 0, 0, 0, 0, 0, 0xba)
	}
	dataOffset := len(body)
	if kind == "liFD" || kind == "liFE" && version > 2 {
		body = append(body, payload...)
	}
	if version >= 5 {
		body = append(body, writerLinkedUnicode("child:raw\x00😀\x00")...)
	}
	if version >= 6 {
		body = binary.BigEndian.AppendUint64(body, 1<<63)
	}
	if version >= 7 {
		body = append(body, 2)
	}
	if kind == "liFE" && version == 2 {
		dataOffset = len(body)
		body = append(body, payload...)
	}
	body = append(body, 0xd3, 0x54, 0x29)
	padding := bytes.Repeat([]byte{0xae}, (4-len(body)%4)%4)
	data := append(binary.BigEndian.AppendUint64(nil, uint64(len(body))), body...)
	data = append(data, padding...)
	return writerLinkedFixture{
		kind: kind, id: id, version: version, data: data, payload: payload, body: body,
		sizeOffset: sizeOffset, dataOffset: dataOffset, padding: padding,
	}
}

func writerLinkedExpected(entry writerLinkedFixture, replacement []byte) []byte {
	body := bytes.Clone(entry.body[:entry.sizeOffset])
	body = binary.BigEndian.AppendUint64(body, uint64(len(replacement)))
	body = append(body, entry.body[entry.sizeOffset+8:entry.dataOffset]...)
	body = append(body, replacement...)
	body = append(body, entry.body[entry.dataOffset+len(entry.payload):]...)
	data := append(binary.BigEndian.AppendUint64(nil, uint64(len(body))), body...)
	padding := (4 - len(body)%4) % 4
	retained := min(padding, len(entry.padding))
	data = append(data, entry.padding[:retained]...)
	data = append(data, make([]byte, padding-retained)...)
	return data
}

func writerLinkedTag(signature, key string, width int, payload []byte, alignment int) []byte {
	data := []byte(signature + key)
	if width == 8 {
		data = binary.BigEndian.AppendUint64(data, uint64(len(payload)))
	} else {
		data = binary.BigEndian.AppendUint32(data, uint32(len(payload)))
	}
	data = append(data, payload...)
	return append(data, bytes.Repeat([]byte{0xf2}, (alignment-len(payload)%alignment)%alignment)...)
}

func writerLinkedLength(data []byte, version uint16, length int) []byte {
	if version == 2 {
		return binary.BigEndian.AppendUint64(data, uint64(length))
	}
	return binary.BigEndian.AppendUint32(data, uint32(length))
}

func writerLinkedDocument(version uint16, global bool, payload []byte) []byte {
	width, alignment, key := 4, 2, "lnkD"
	if global {
		alignment, key = 4, "lnk2"
		if version == 2 {
			width = 8
		}
	}
	tag := writerLinkedTag("8BIM", key, width, payload, alignment)
	section := writerLinkedLength(nil, version, 0)
	if !global {
		var info []byte
		info = binary.BigEndian.AppendUint16(info, 1)
		for range 4 {
			info = binary.BigEndian.AppendUint32(info, 0)
		}
		info = binary.BigEndian.AppendUint16(info, 0)
		info = append(info, []byte("8BIMnorm\xff\x00\x00\x00")...)
		extra := binary.BigEndian.AppendUint64(nil, 0)
		extra = append(extra, 1, 'L', 0xe3, 0xe4)
		extra = append(extra, writerLinkedTag("8BIM", "U000", 4, []byte{5, 9, 1}, 2)...)
		extra = append(extra, tag...)
		extra = append(extra, writerLinkedTag("8BIM", "U000", 4, []byte{7, 2}, 2)...)
		info = binary.BigEndian.AppendUint32(info, uint32(len(extra)))
		info = append(info, extra...)
		if len(info)%2 != 0 {
			info = append(info, 0xaa)
		}
		section = writerLinkedLength(nil, version, len(info))
		section = append(section, info...)
	}
	section = binary.BigEndian.AppendUint32(section, 0)
	if global {
		section = append(section, writerLinkedTag("8BIM", "G000", 4, []byte{9, 1, 7}, 4)...)
		section = append(section, tag...)
		section = append(section, writerLinkedTag("8B64", "G000", 8, []byte{2, 6}, 4)...)
	}
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint64(data, 0)
	data = writerLinkedLength(data, version, len(section))
	data = append(data, section...)
	data = append(data, 0, 0, 50, 70, 90)
	return data
}

func writerLinkedEditor(t *testing.T, data []byte) (*Editor, psdfile.AdditionalBlock) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	editor.linkedRecords, err = source.LinkedFiles(nil)
	if err != nil || len(editor.linkedRecords) == 0 {
		t.Fatalf("independent fixture linked metadata: %v", err)
	}
	return editor, editor.linkedRecords[0].Block
}

func writerLinkedEmit(t *testing.T, source *psdfile.Document, plan writePlan) []byte {
	t.Helper()
	if plan.err != nil {
		t.Fatal(plan.err)
	}
	var data bytes.Buffer
	for _, part := range plan.parts {
		if part.data != nil {
			data.Write(part.data)
		} else if _, err := source.CopySpan(context.Background(), &data, part.span); err != nil {
			t.Fatal(err)
		}
	}
	if int64(data.Len()) != plan.size {
		t.Fatal("write-plan declared size differs from emitted bytes")
	}
	return data.Bytes()
}

func TestWriterLinkedReplacesDuplicateEmbeddedRecordsAndRetainsEveryOtherByte(t *testing.T) {
	entries := []writerLinkedFixture{
		writerLinkedEntry("liFD", "repeat", 7, []byte{1, 2, 3, 4}),
		writerLinkedEntry("liFE", "external", 7, []byte{17, 18, 19}),
		writerLinkedEntry("liFD", "repeat", 1, []byte{5, 6}),
		writerLinkedEntry("liFA", "alias", 7, nil),
		writerLinkedEntry("liFD", "untouched", 4, []byte{9, 8, 7, 6, 5}),
	}
	var originalPayload []byte
	for _, entry := range entries {
		originalPayload = append(originalPayload, entry.data...)
	}
	for _, version := range []uint16{1, 2} {
		for _, global := range []bool{false, true} {
			for _, replacement := range [][]byte{nil, {0x33}, {0x41, 0x42, 0x43}, bytes.Repeat([]byte{0xab}, 17)} {
				t.Run(fmt.Sprintf("v%d/global_%t/bytes_%d", version, global, len(replacement)), func(t *testing.T) {
					editor, block := writerLinkedEditor(t, writerLinkedDocument(version, global, originalPayload))
					editor.linkedOverlays = map[string]*linkedReplacement{"repeat": {data: bytes.Clone(replacement)}}
					// Returned linked metadata is source-ordered; sorting also tolerates an internal cache reordering.
					slices.Reverse(editor.linkedRecords)
					if !editor.linkedBlockChangedLocked(block) {
						t.Fatal("matching duplicate IDs did not mark the source block changed")
					}
					plan, err := editor.writeLinkedBlockLocked(block, global, 1<<20)
					if err != nil {
						t.Fatal(err)
					}
					var expectedPayload []byte
					for _, entry := range entries {
						if entry.id == "repeat" {
							expectedPayload = append(expectedPayload, writerLinkedExpected(entry, replacement)...)
						} else {
							expectedPayload = append(expectedPayload, entry.data...)
						}
					}
					alignment := 2
					if global {
						alignment = 4
					}
					expected := writerLinkedTag(block.Signature, block.Key, int(block.LengthWidth), expectedPayload, alignment)
					actual := writerLinkedEmit(t, editor.source, plan)
					if !bytes.Equal(actual, expected) {
						t.Fatalf("linked record headers, content, unknown tails or alignment changed unexpectedly: got=%d want=%d",
							len(actual), len(expected))
					}
					fresh, _ := writerLinkedEditor(t, writerLinkedDocument(version, global, expectedPayload))
					for _, record := range fresh.linkedRecords {
						if record.ID == "repeat" && record.DataSize != uint64(len(replacement)) {
							t.Fatal("reopened replacement data size differs")
						}
					}
					for _, part := range plan.parts {
						if part.data != nil {
							continue
						}
						for _, record := range editor.linkedRecords {
							if record.ID == "repeat" && part.span.Offset < record.Data.Offset+record.Data.Length &&
								record.Data.Offset < part.span.Offset+part.span.Length {
								t.Fatal("plan still copies the replaced original content")
							}
						}
					}
				})
			}
		}
	}
}

func TestWriterLinkedUnchangedAndUnsupportedKinds(t *testing.T) {
	for _, kind := range []string{"liFD", "liFE", "liFA"} {
		entry := writerLinkedEntry(kind, "fixture-id", 7, []byte{3, 2, 1})
		editor, block := writerLinkedEditor(t, writerLinkedDocument(2, true, entry.data))
		original, err := editor.source.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
		if editor.linkedBlockChangedLocked(block) {
			t.Fatal("unchanged block was marked dirty")
		}
		plan, err := editor.writeLinkedBlockLocked(block, true, 1<<20)
		if err != nil || !bytes.Equal(writerLinkedEmit(t, editor.source, plan), original) {
			t.Fatalf("unchanged block lost byte identity: %v", err)
		}
		editor.linkedOverlays = map[string]*linkedReplacement{"fixture-id": {data: []byte{9}}}
		_, err = editor.writeLinkedBlockLocked(block, true, 1<<20)
		if kind != "liFD" && !errors.Is(err, ErrUnsupported) || kind == "liFD" && err != nil {
			t.Fatalf("replacement kind %s: %v", kind, err)
		}
	}
}

func TestWriterLinkedUnknownTrailingRecordsAndOuterPaddingArePreserved(t *testing.T) {
	entry := writerLinkedEntry("liFD", "fixture-id", 7, []byte{8, 9, 2})
	editor, _ := writerLinkedEditor(t, writerLinkedDocument(1, true, entry.data))
	// A future record remains opaque to the helper and is copied as the block's unindexed remainder.
	future := append(binary.BigEndian.AppendUint64(nil, 5), []byte("X999\x07")...)
	for _, global := range []bool{false, true} {
		payload := append(bytes.Clone(entry.data), future...)
		data := writerLinkedDocument(1, global, payload)
		source, err := psdfile.Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = source.Close() })
		var block psdfile.AdditionalBlock
		if global {
			block = source.AdditionalInfo[1]
		} else {
			block = source.Layers[0].AdditionalInfo[1]
		}
		record := editor.linkedRecords[0]
		shift := block.Data.Offset - record.Block.Data.Offset
		shiftSpan := func(span psdfile.Span) psdfile.Span {
			if span == (psdfile.Span{}) {
				return span
			}
			span.Offset += shift
			return span
		}
		record.Block, record.Span, record.Body, record.Header = block,
			shiftSpan(record.Span), shiftSpan(record.Body), shiftSpan(record.Header)
		record.LengthData, record.DataSizeData, record.Data, record.Padding =
			shiftSpan(record.LengthData), shiftSpan(record.DataSizeData), shiftSpan(record.Data), shiftSpan(record.Padding)
		current := &Editor{source: source, linkedRecords: []psdfile.LinkedFileRecord{record},
			linkedOverlays: map[string]*linkedReplacement{"fixture-id": {data: []byte{0xb7}}}}
		plan, err := current.writeLinkedBlockLocked(block, global, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		expectedPayload := append(writerLinkedExpected(entry, []byte{0xb7}), future...)
		alignment := 2
		if global {
			alignment = 4
		}
		expected := writerLinkedTag(block.Signature, block.Key, int(block.LengthWidth), expectedPayload, alignment)
		if actual := writerLinkedEmit(t, source, plan); !bytes.Equal(actual, expected) {
			t.Fatal("unrecognized record bytes or original outer padding were discarded")
		}
	}
}

type writerLinkedCountingReader struct {
	reader io.ReaderAt
	reads  int
}

func (reader *writerLinkedCountingReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.reads++
	return reader.reader.ReadAt(data, offset)
}

func TestWriterLinkedPreparationDoesNotReadOriginalContentAndBoundsOutput(t *testing.T) {
	entry := writerLinkedEntry("liFD", "fixture-id", 7, bytes.Repeat([]byte{0xd7}, 2<<20))
	data := writerLinkedDocument(1, true, entry.data)
	reader := &writerLinkedCountingReader{reader: bytes.NewReader(data)}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	records, err := source.LinkedFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	current := &Editor{source: source, linkedRecords: records,
		linkedOverlays: map[string]*linkedReplacement{"fixture-id": {data: []byte{2, 9}}}}
	reader.reads = 0
	plan, err := current.writeLinkedBlockLocked(records[0].Block, true, 1<<20)
	if err != nil || reader.reads != 0 || plan.size >= 1024 {
		t.Fatalf("plan preparation read original header/payload or retained replaced payload: reads=%d err=%v",
			reader.reads, err)
	}
	if _, err := current.writeLinkedBlockLocked(records[0].Block, true, plan.size-1); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("output budget was ignored: %v", err)
	}
	current.linkedRecords[0].DataSizeData.Offset = current.linkedRecords[0].Data.Offset + 1
	if _, err := current.writeLinkedBlockLocked(records[0].Block, true, 1<<20); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid cached content spans were accepted: %v", err)
	}
}

type writerLinkedSparsePatch struct {
	offset int64
	data   []byte
}

type writerLinkedSparseReader struct {
	size    int64
	patches []writerLinkedSparsePatch
	reads   int
}

func (reader *writerLinkedSparseReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.reads++
	if offset < 0 || offset >= reader.size {
		return 0, io.EOF
	}
	count := min(int64(len(data)), reader.size-offset)
	clear(data[:count])
	for _, patch := range reader.patches {
		start, end := max(offset, patch.offset), min(offset+count, patch.offset+int64(len(patch.data)))
		if end > start {
			copy(data[start-offset:end-offset], patch.data[start-patch.offset:end-patch.offset])
		}
	}
	if count < int64(len(data)) {
		return int(count), io.EOF
	}
	return int(count), nil
}

func TestWriterLinkedRejectsFourByteBlockLengthOverflowWithoutLargeAllocation(t *testing.T) {
	const oldDataLength = int64(math.MaxUint32) - 3
	entry := writerLinkedEntry("liFD", "fixture-id", 1, nil)
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), 2)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = binary.BigEndian.AppendUint64(header, 0)
	header = binary.BigEndian.AppendUint64(header, uint64(oldDataLength+24))
	header = binary.BigEndian.AppendUint64(header, 0)
	header = binary.BigEndian.AppendUint32(header, 0)
	header = append(header, []byte("8BIMlnkD")...)
	header = binary.BigEndian.AppendUint32(header, uint32(oldDataLength))
	dataStart := int64(len(header))
	header = binary.BigEndian.AppendUint64(header, uint64(oldDataLength-8))
	header = append(header, entry.body[:entry.dataOffset]...)
	footer := dataStart + oldDataLength
	reader := &writerLinkedSparseReader{size: footer + 5, patches: []writerLinkedSparsePatch{
		{offset: 0, data: header}, {offset: footer, data: []byte{0, 0, 5, 7, 9}},
	}}
	source, err := psdfile.Read(reader, reader.size, psdfile.LoadOptions{
		Limits: psdfile.Limits{MaxFileBytes: reader.size + 1024},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	records, err := source.LinkedFiles(nil)
	if err != nil || len(records) != 1 {
		t.Fatalf("sparse independently encoded large source: %v", err)
	}
	current := &Editor{source: source, linkedRecords: records,
		linkedOverlays: map[string]*linkedReplacement{"fixture-id": {data: make([]byte, 8)}}}
	reader.reads = 0
	_, err = current.writeLinkedBlockLocked(records[0].Block, true, reader.size+1024)
	if !errors.Is(err, psdfile.ErrLimit) || reader.reads != 0 {
		t.Fatalf("four-byte linked block overflow was accepted or read a sparse tail: reads=%d err=%v", reader.reads, err)
	}
}

func TestWriterLinkedLargeBorrowedReplacementHasSmallPlanMetadata(t *testing.T) {
	entry := writerLinkedEntry("liFD", "fixture-id", 7, []byte{1})
	editor, block := writerLinkedEditor(t, writerLinkedDocument(2, false, entry.data))
	content := bytes.Repeat([]byte{0xd9}, 33<<20)
	editor.linkedOverlays = map[string]*linkedReplacement{"fixture-id": {data: content}}
	plan, err := editor.writeLinkedBlockLocked(block, false, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.parts) > 16 || plan.size < int64(len(content)) {
		t.Fatal("large content was expanded into oversized write-plan metadata")
	}
	borrowed := false
	for _, part := range plan.parts {
		if len(part.data) == len(content) {
			borrowed = &part.data[0] == &content[0] && part.content
		}
	}
	if !borrowed {
		t.Fatal("write-plan preparation copied the editor-owned linked replacement")
	}
	// This exercises the layer-extra integration without treating the borrowed content as metadata.
	editor.revision = 1
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); err != nil {
		t.Fatalf("layer-local content was incorrectly charged to the 32 MiB metadata budget: %v", err)
	}
	source, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	records, err := source.LinkedFiles(nil)
	if err != nil || len(records) != 1 || records[0].Data.Length != int64(len(content)) {
		t.Fatalf("large layer-local content did not survive complete saving: %v", err)
	}
}
