package psdfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
)

type linkedFixture struct {
	data, payload []byte
	fields        map[string]Span
}

func linkedFixtureDescriptor(entries ...descriptorFixtureEntry) []byte {
	return append(descriptorFixtureU32(16), descriptorFixtureBody("", "null", entries...)...)
}

func linkedFixtureEntry(kind string, version uint32, openDescriptor, payload, tail []byte) linkedFixture {
	var body bytes.Buffer
	fields := make(map[string]Span)
	field := func(key string, data []byte) {
		fields[key] = Span{Offset: int64(body.Len()) + 8, Length: int64(len(data))}
		body.Write(data)
	}
	field("kind", []byte(kind))
	field("version", descriptorFixtureU32(version))
	field("id", append([]byte{10}, []byte("fixture-id")...))
	field("name", descriptorFixtureUnicode("素材😀.psb\x00"))
	field("fileType", []byte("8BPS"))
	field("creator", []byte("8BIM"))
	size := uint64(len(payload))
	if kind == "liFA" || kind == "liFE" && version == 1 {
		size = 0
	}
	field("dataSize", descriptorFixtureU64(size))
	flag := byte(0)
	if len(openDescriptor) != 0 {
		flag = 1
	}
	field("fileOpen", []byte{flag})
	if flag != 0 {
		field("fileOpenDescriptor", openDescriptor)
	}
	switch kind {
	case "liFE":
		field("externalDescriptor", linkedFixtureDescriptor(descriptorFixtureEntry{
			key: "fullPath", kind: "TEXT", data: descriptorFixtureUnicode("/fixture/unopened/素材.psb"),
		}))
		if version > 3 {
			timestamp := append(descriptorFixtureU32(2025), 12, 31, 23, 59)
			timestamp = append(timestamp, descriptorFixtureU64(math.Float64bits(59.875))...)
			field("timestamp", timestamp)
		}
		field("externalFileSize", descriptorFixtureU64(9876543210))
	case "liFA":
		field("aliasReserved", []byte{0, 0, 0, 0, 0, 0, 0, 0x73})
	}
	if kind == "liFD" || kind == "liFE" && version > 2 {
		field("data", payload)
	}
	if version >= 5 {
		field("childDocumentID", descriptorFixtureUnicode("child:middle\x00終😀\x00"))
	}
	if version >= 6 {
		field("assetModTime", descriptorFixtureU64(1<<63))
	}
	if version >= 7 {
		field("assetLocked", []byte{2})
	}
	if kind == "liFE" && version == 2 {
		field("data", payload)
	}
	field("tail", tail)
	data := append(descriptorFixtureU64(uint64(body.Len())), body.Bytes()...)
	padding := (4 - body.Len()%4) % 4
	fields["padding"] = Span{Offset: int64(len(data)), Length: int64(padding)}
	data = append(data, bytes.Repeat([]byte{0xa9}, padding)...)
	return linkedFixture{data: data, payload: payload, fields: fields}
}

func linkedFixtureDocument(version uint16, key string, entries ...linkedFixture) []byte {
	var payload []byte
	for _, entry := range entries {
		payload = append(payload, entry.data...)
	}
	tag := indexTestTag(version, "8BIM", key, payload, 4)
	return indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, nil, tag), []byte{0, 0, 0})
}

func linkedFixtureParse(t *testing.T, data []byte) *Document {
	t.Helper()
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	return document
}

func linkedAssertField(t *testing.T, record LinkedFileRecord, fixture linkedFixture, key string, got Span) {
	t.Helper()
	expected, exists := fixture.fields[key]
	if !exists {
		if got != (Span{}) {
			t.Fatalf("unexpected %s span: %+v", key, got)
		}
		return
	}
	expected.Offset += record.Span.Offset
	if got != expected {
		t.Fatalf("%s span = %+v, want %+v", key, got, expected)
	}
}

func TestLinkedFilesAllKindsVersionsPSDAndPSB(t *testing.T) {
	openDescriptor := linkedFixtureDescriptor(descriptorFixtureEntry{
		key: "name", kind: "TEXT", data: descriptorFixtureUnicode("original.psb"),
	})
	for _, documentVersion := range []uint16{1, 2} {
		for _, key := range []string{"lnkD", "lnk2", "lnk3", "lnkE"} {
			for _, kind := range []string{"liFD", "liFE", "liFA"} {
				for version := uint32(1); version <= 7; version++ {
					t.Run(fmt.Sprintf("v%d/%s/%s/%d", documentVersion, key, kind, version), func(t *testing.T) {
						fixture := linkedFixtureEntry(kind, version, openDescriptor, []byte{8, 6, 7, 5, 3}, []byte{0xca, 0xfe})
						document := linkedFixtureParse(t, linkedFixtureDocument(documentVersion, key, fixture))
						records, err := document.LinkedFiles(nil)
						if err != nil || len(records) != 1 {
							t.Fatalf("linked records=%d: %v", len(records), err)
						}
						record := records[0]
						if record.Kind != kind || record.Version != version || record.ID != "fixture-id" ||
							record.Name != "素材😀.psb" || record.FileType != "8BPS" || record.Creator != "8BIM" ||
							record.Block.Key != key || record.Index != 0 {
							t.Fatal("linked metadata was changed or lost")
						}
						for name, span := range map[string]Span{
							"id": record.IDData, "name": record.NameData, "dataSize": record.DataSizeData,
							"fileOpen": record.FileOpenFlagData, "fileOpenDescriptor": record.FileOpenDescriptor,
							"externalDescriptor": record.ExternalDescriptor, "externalFileSize": record.ExternalFileSizeData,
							"aliasReserved": record.AliasReserved, "data": record.Data, "tail": record.Tail,
							"padding": record.Padding, "childDocumentID": record.ChildDocumentIDData,
							"assetModTime": record.AssetModTimeData, "assetLocked": record.AssetLockedData,
						} {
							linkedAssertField(t, record, fixture, name, span)
						}
						if record.Span.Length != int64(len(fixture.data))-record.Padding.Length ||
							record.LengthData != (Span{Offset: record.Span.Offset, Length: 8}) ||
							record.Body != (Span{Offset: record.Span.Offset + 8, Length: record.Span.Length - 8}) {
							t.Fatal("linked record header/body length markers differ")
						}
						if record.HasData != (kind == "liFD" || kind == "liFE" && version >= 2) {
							t.Fatal("content presence does not match record kind/version")
						}
						if record.HasData && record.Header.Offset+record.Header.Length != record.Data.Offset {
							t.Fatal("header crosses the content boundary")
						}
						if kind == "liFE" {
							if record.ExternalFileSize != 9876543210 || (record.Timestamp != nil) != (version > 3) {
								t.Fatal("external metadata differs")
							}
							if record.Timestamp != nil && (record.Timestamp.Year != 2025 ||
								record.Timestamp.Month != 12 || record.Timestamp.Day != 31 || record.Timestamp.Hour != 23 ||
								record.Timestamp.Minute != 59 || record.Timestamp.Seconds != 59.875) {
								t.Fatal("timestamp fields changed")
							}
						}
						if version >= 5 && record.ChildDocumentID != "child:middle\x00終😀" ||
							(version >= 6 && (record.AssetModTime == nil || math.Float64bits(*record.AssetModTime) != 1<<63)) ||
							(version >= 7 && (record.AssetLocked == nil || *record.AssetLocked != 2)) {
							t.Fatal("versioned metadata or IEEE/wire precision changed")
						}
						var copied bytes.Buffer
						span := Span{Offset: record.Span.Offset, Length: record.Span.Length + record.Padding.Length}
						if _, err := document.CopySpan(context.Background(), &copied, span); err != nil ||
							!bytes.Equal(copied.Bytes(), fixture.data) {
							t.Fatalf("raw entry or unknown tail/padding changed: %v", err)
						}
					})
				}
			}
		}
	}
}

func TestLinkedFilesDuplicateBlocksRecordsAndLayerSetOrder(t *testing.T) {
	fixture := linkedFixtureEntry("liFD", 1, nil, []byte{9}, nil)
	for _, version := range []uint16{1, 2} {
		layerTag := indexTestTag(version, "8BIM", "lnkD", fixture.data, 2)
		main := indexTestLayerSet(version, image.Rectangle{}, nil, nil, nil, layerTag)
		alternate := indexTestLayerSet(version, image.Rectangle{}, nil, nil, nil, layerTag)
		tags := append(indexTestTag(version, "8BIM", "lnk2", append(bytes.Clone(fixture.data), fixture.data...), 4),
			indexTestTag(version, "8BIM", "Layr", alternate, 4)...)
		tags = append(tags, indexTestTag(version, "8BIM", "lnk2", fixture.data, 4)...)
		document := linkedFixtureParse(t, indexTestDocument(version, 8, 3, nil,
			indexTestGlobal(version, main, tags), []byte{0, 0, 0}))
		records, err := document.LinkedFiles(context.Background())
		if err != nil || len(records) != 5 {
			t.Fatalf("version=%d duplicated records=%d: %v", version, len(records), err)
		}
		for index, record := range records {
			if record.ID != "fixture-id" || index > 0 && record.Span.Offset <= records[index-1].Span.Offset {
				t.Fatal("duplicate IDs were deduplicated or source ordering changed")
			}
		}
		if records[1].Index != 0 || records[2].Index != 1 || records[1].Block != records[2].Block {
			t.Fatal("record occurrence identity within the duplicate block changed")
		}
	}
}

func TestLinkedFilesEmptyEmbeddedContentAndEmptyBlocks(t *testing.T) {
	fixture := linkedFixtureEntry("liFD", 1, nil, nil, nil)
	document := linkedFixtureParse(t, linkedFixtureDocument(1, "lnk2", fixture))
	records, err := document.LinkedFiles(nil)
	if err != nil || len(records) != 1 || !records[0].HasData || records[0].Data.Length != 0 ||
		records[0].Data.Offset != records[0].Header.Offset+records[0].Header.Length {
		t.Fatalf("empty embedded content was confused with an external/alias entry: %v", err)
	}
	for _, data := range [][]byte{
		indexTestDocument(1, 8, 3, nil, nil, []byte{0, 0, 0}),
		linkedFixtureDocument(1, "lnk2"),
	} {
		empty := linkedFixtureParse(t, data)
		if records, err := empty.LinkedFiles(nil); err != nil || len(records) != 0 {
			t.Fatalf("empty linked index rejected: %v", err)
		}
	}
}

func TestLinkedFilesNeverReadsContentOrDescriptorBlobs(t *testing.T) {
	content := bytes.Repeat([]byte{0x59}, 2<<20)
	blob := bytes.Repeat([]byte{0x63}, 1<<20)
	openDescriptor := linkedFixtureDescriptor(descriptorFixtureEntry{
		key: "blob", kind: "tdta", data: append(descriptorFixtureU32(uint32(len(blob))), blob...),
	})
	fixture := linkedFixtureEntry("liFD", 7, openDescriptor, content, []byte{0x33})
	data := linkedFixtureDocument(2, "lnk2", fixture)
	reader := &indexForbiddenReader{data: data}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	contentStart, blobStart := bytes.Index(data, content), bytes.Index(data, blob)
	if contentStart < 0 || blobStart < 0 {
		t.Fatal("independent fixture payload positions missing")
	}
	reader.forbidden = []Span{
		{Offset: int64(contentStart), Length: int64(len(content))},
		{Offset: int64(blobStart), Length: int64(len(blob))},
	}
	reader.reads = nil
	records, err := document.LinkedFiles(context.Background())
	if err != nil || len(records) != 1 || records[0].Data.Offset != int64(contentStart) {
		t.Fatalf("content was read or its span was misplaced: %v", err)
	}
	readBytes := int64(0)
	for _, span := range reader.reads {
		readBytes += span.Length
	}
	if readBytes > 2048 {
		t.Fatalf("linked indexing read %d structural bytes", readBytes)
	}
	descriptor, err := document.DecodeDescriptor(records[0].FileOpenDescriptor, true)
	if err != nil || len(descriptor.Entries) != 1 {
		t.Fatalf("retained descriptor span is not explicitly decodable: %v", err)
	}
	reader.forbidden = nil
	var output bytes.Buffer
	if count, err := document.CopySpan(context.Background(), &output, records[0].Data); err != nil ||
		count != int64(len(content)) || !bytes.Equal(output.Bytes(), content) {
		t.Fatalf("explicit content copy differs: %v", err)
	}
}

func TestLinkedFilesBudgetsMalformedAndUnsupported(t *testing.T) {
	openDescriptor := linkedFixtureDescriptor(descriptorFixtureEntry{
		key: "nest", kind: "Objc", data: descriptorFixtureBody("", "null", descriptorFixtureEntry{
			key: "text", kind: "TEXT", data: descriptorFixtureUnicode("value"),
		}),
	})
	fixture := linkedFixtureEntry("liFE", 7, openDescriptor, []byte{2, 7, 1, 8}, nil)
	for _, test := range []struct {
		name   string
		limits Limits
	}{
		{"metadata", Limits{MaxMetadataBytes: 32}}, {"blocks", Limits{MaxBlocks: 1}},
		{"read", Limits{MaxReadBytes: 3}}, {"descriptor_items", Limits{MaxDescriptorItems: 1}},
		{"descriptor_depth", Limits{MaxDescriptorDepth: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := linkedFixtureParse(t, linkedFixtureDocument(1, "lnkE", fixture))
			limits, err := loadLimits([]LoadOptions{{Limits: test.limits}})
			if err != nil {
				t.Fatal(err)
			}
			document.limits = limits
			if _, err := document.LinkedFiles(nil); !errors.Is(err, ErrLimit) {
				t.Fatalf("linked index ignored %s budget: %v", test.name, err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		change func([]byte)
		kind   error
	}{
		{"unsigned_length", func(data []byte) { binary.BigEndian.PutUint64(data, math.MaxUint64) }, ErrMalformed},
		{"long_record", func(data []byte) { binary.BigEndian.PutUint64(data, uint64(len(data)+1)) }, ErrMalformed},
		{"kind", func(data []byte) { copy(data[8:], "zzzz") }, ErrUnsupported},
		{"version", func(data []byte) { binary.BigEndian.PutUint32(data[12:], 8) }, ErrUnsupported},
		{"id_length", func(data []byte) { data[fixture.fields["id"].Offset] = 255 }, ErrMalformed},
		{"name_length", func(data []byte) {
			binary.BigEndian.PutUint32(data[fixture.fields["name"].Offset:], math.MaxUint32)
		}, ErrMalformed},
		{"flag", func(data []byte) { data[fixture.fields["fileOpen"].Offset] = 2 }, ErrMalformed},
		{"size", func(data []byte) {
			binary.BigEndian.PutUint64(data[fixture.fields["dataSize"].Offset:], math.MaxUint64)
		}, ErrMalformed},
		{"open_version", func(data []byte) {
			binary.BigEndian.PutUint32(data[fixture.fields["fileOpenDescriptor"].Offset:], 15)
		}, ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := bytes.Clone(fixture.data)
			test.change(changed)
			document := linkedFixtureParse(t, linkedFixtureDocument(1, "lnkE", linkedFixture{data: changed}))
			_, err := document.LinkedFiles(nil)
			if !errors.Is(err, test.kind) || err == nil || strings.Contains(err.Error(), "fixture-id") {
				t.Fatalf("malformed/unsupported index error category or redaction differs: %v", err)
			}
		})
	}
	for length := 0; length < len(fixture.data); length++ {
		data := linkedFixtureDocument(1, "lnkE", linkedFixture{data: bytes.Clone(fixture.data[:length])})
		document := linkedFixtureParse(t, data)
		_, err := document.LinkedFiles(nil)
		if length != 0 && err == nil {
			t.Fatalf("truncated linked entry accepted at length %d", length)
		}
	}
}

type linkedTestReader struct {
	reader    io.ReaderAt
	reads     int
	failure   error
	failAt    int64
	zero      bool
	fullEOF   bool
	cancel    context.CancelFunc
	started   chan struct{}
	release   chan struct{}
	blockOnce sync.Once
}

func (reader *linkedTestReader) ReadAt(data []byte, offset int64) (int, error) {
	reader.reads++
	if reader.started != nil {
		reader.blockOnce.Do(func() { close(reader.started); <-reader.release })
	}
	if reader.cancel != nil {
		reader.cancel()
	}
	if reader.failure != nil && (reader.failAt == 0 || offset >= reader.failAt) {
		return 0, reader.failure
	}
	if reader.zero {
		return 0, nil
	}
	count, err := reader.reader.ReadAt(data, offset)
	if reader.fullEOF && count == len(data) {
		err = io.EOF
	}
	return count, err
}

func TestLinkedFilesOwnedMetadataCancellationCloseAndReaderErrors(t *testing.T) {
	fixture := linkedFixtureEntry("liFE", 7, nil, []byte{9, 2, 6}, nil)
	data := linkedFixtureDocument(1, "lnkE", fixture)
	reader := &linkedTestReader{reader: bytes.NewReader(data)}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	records, err := document.LinkedFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	records[0].Name, records[0].Timestamp.Year, *records[0].AssetLocked = "modified", 1234, 44
	fresh, err := document.LinkedFiles(nil)
	if err != nil || fresh[0].Name != "素材😀.psb" || fresh[0].Timestamp.Year != 2025 || *fresh[0].AssetLocked != 2 {
		t.Fatalf("returned metadata aliases subsequent calls: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader.reads = 0
	if _, err := document.LinkedFiles(ctx); !errors.Is(err, context.Canceled) || reader.reads != 0 {
		t.Fatalf("pre-canceled index performed reads: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader.cancel = cancel
	if _, err := document.LinkedFiles(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-index cancellation was lost: %v", err)
	}
	reader.cancel = nil
	wantIO := errors.New("fixture source failure")
	reader.failure = wantIO
	if _, err := document.LinkedFiles(nil); !errors.Is(err, ErrMalformed) || !errors.Is(err, wantIO) {
		t.Fatalf("underlying structural I/O failure was lost: %v", err)
	}
	for _, name := range []string{"id", "name", "fileType", "dataSize", "fileOpen", "externalDescriptor"} {
		reader.failAt = document.AdditionalInfo[0].Data.Offset + fixture.fields[name].Offset
		if _, err := document.LinkedFiles(nil); !errors.Is(err, ErrMalformed) || !errors.Is(err, wantIO) {
			t.Fatalf("underlying %s field I/O failure was overwritten: %v", name, err)
		}
	}
	reader.failAt = 0
	reader.failure, reader.zero = nil, true
	if _, err := document.LinkedFiles(nil); !errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("non-progressing ReaderAt was accepted: %v", err)
	}
	reader.zero, reader.fullEOF = false, true
	if _, err := document.LinkedFiles(nil); err != nil {
		t.Fatalf("complete ReaderAt reads with EOF were rejected: %v", err)
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := document.LinkedFiles(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed linked index: %v", err)
	}
}

func TestLinkedFilesCloseWaitsForActiveRead(t *testing.T) {
	fixture := linkedFixtureEntry("liFD", 1, nil, []byte{0}, nil)
	data := linkedFixtureDocument(1, "lnk2", fixture)
	reader := &linkedTestReader{reader: bytes.NewReader(data)}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	reader.started, reader.release = make(chan struct{}), make(chan struct{})
	readDone, closeDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := document.LinkedFiles(nil); readDone <- err }()
	<-reader.started
	go func() { closeDone <- document.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before linked indexing completed: %v", err)
	default:
	}
	close(reader.release)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestLinkedFilesRealSampleSpansAndContentPreservation(t *testing.T) {
	data, err := os.ReadFile(testfixture.Sample(t))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("optional real PSD fixture is absent")
	}
	if err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(data)
	reader := &indexForbiddenReader{data: data}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	reader.reads = nil
	records, err := document.LinkedFiles(context.Background())
	if err != nil || len(records) != 1 {
		t.Fatalf("real linked record count=%d: %v", len(records), err)
	}
	instances := 0
	for _, layer := range document.Layers {
		placed := false
		for _, block := range layer.AdditionalInfo {
			placed = placed || block.Key == "SoLd" || block.Key == "SoLE" || block.Key == "PlLd"
		}
		if placed {
			instances++
		}
	}
	if instances != 14 || records[0].Version != 7 || records[0].Data.Length != 12123114 {
		t.Fatal("real sample does not contain the independently verified shared embedded object and its 14 instances")
	}
	indexedBytes := int64(0)
	for _, read := range reader.reads {
		indexedBytes += read.Length
	}
	t.Logf("linkedRecords=%d instances=%d structuralBytes=%d contentBytes=%d",
		len(records), instances, indexedBytes, records[0].Data.Length)
	for _, record := range records {
		if record.Kind != "liFD" || !record.HasData || record.Data.Length <= 0 ||
			record.DataSize != uint64(record.Data.Length) {
			t.Fatal("real linked content metadata differs")
		}
		reader.forbidden = append(reader.forbidden, record.Data)
		for _, read := range reader.reads {
			if read.Offset < record.Data.Offset+record.Data.Length && record.Data.Offset < read.Offset+read.Length {
				t.Fatal("real linked indexing read embedded object content")
			}
		}
	}
	if _, err := document.LinkedFiles(nil); err != nil {
		t.Fatalf("real metadata needs embedded bytes: %v", err)
	}
	reader.forbidden = nil
	for _, record := range records {
		want := sha256.Sum256(data[record.Data.Offset : record.Data.Offset+record.Data.Length])
		got := sha256.New()
		count, err := document.CopySpan(context.Background(), got, record.Data)
		if err != nil || count != record.Data.Length || !bytes.Equal(got.Sum(nil), want[:]) {
			t.Fatalf("real original content was not preserved: count=%d err=%v", count, err)
		}
		// This hash was independently obtained from psd-tools' native liFD payload decoding.
		const expected = "a2d635c76ff98d9d6b5f4a20843419131da4f4037241e11f7379adfaba2ee0c5"
		if fmt.Sprintf("%x", got.Sum(nil)) != expected {
			t.Fatal("real embedded content span differs from the independent decoder")
		}
	}
	if after := sha256.Sum256(data); after != before {
		t.Fatal("linked indexing or content copy mutated the original source")
	}
}
