package psdfile

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdpattern"
)

func patternFixtureRectangle() []byte {
	var data []byte
	for _, value := range []uint32{0, 0, 2, 2} {
		data = binary.BigEndian.AppendUint32(data, value)
	}
	return data
}

func patternFixtureArray(depth uint16, compression byte, pixels []byte) []byte {
	body := binary.BigEndian.AppendUint32(nil, uint32(depth))
	body = append(body, patternFixtureRectangle()...)
	body = binary.BigEndian.AppendUint16(body, depth)
	body = append(body, compression)
	body = append(body, pixels...)
	data := binary.BigEndian.AppendUint32(nil, 1)
	data = binary.BigEndian.AppendUint32(data, uint32(len(body)))
	return append(data, body...)
}

func patternFixture(depth uint16, compression byte, samples []byte) []byte {
	vm := append(patternFixtureRectangle(), descriptorFixtureU32(3)...)
	for range 3 {
		vm = append(vm, patternFixtureArray(depth, compression, samples)...)
	}
	vm = append(vm, descriptorFixtureU32(0)...)
	vm = append(vm, descriptorFixtureU32(0)...)
	body := descriptorFixtureU32(1)
	body = append(body, descriptorFixtureU32(3)...)
	body = append(body, 0, 2, 0, 2)
	body = append(body, descriptorFixtureUnicode("Fixture pattern")...)
	body = append(body, 7)
	body = append(body, "fixture"...)
	body = append(body, descriptorFixtureU32(3)...)
	body = append(body, descriptorFixtureU32(uint32(len(vm)))...)
	body = append(body, vm...)
	data := descriptorFixtureU32(uint32(len(body)))
	data = append(data, body...)
	return append(data, make([]byte, (4-len(body)%4)%4)...)
}

func patternFixtureSource(t *testing.T, pattern []byte, keys ...string) *Document {
	t.Helper()
	var tags []byte
	for _, key := range keys {
		tags = append(tags, indexTestTag(1, "8BIM", key, pattern, 4)...)
	}
	data := indexTestDocument(1, 8, 3, nil, indexTestGlobal(1, nil, tags), []byte{0, 0, 0})
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	return document
}

type patternFixtureCountingReader struct {
	data    []byte
	payload []Span
	reads   int
	touched bool
}

func (r *patternFixtureCountingReader) ReadAt(data []byte, offset int64) (int, error) {
	r.reads++
	for _, span := range r.payload {
		if offset < span.Offset+span.Length && span.Offset < offset+int64(len(data)) {
			r.touched = true
		}
	}
	return bytes.NewReader(r.data).ReadAt(data, offset)
}

func TestPatternsOwnMetadataAndSkipPayloadsAcrossDuplicateBlocks(t *testing.T) {
	document := patternFixtureSource(t, patternFixture(8, 0, []byte{10, 20, 30, 40}), "Patt", "Pat2", "Pat3")
	records, err := document.Patterns(nil)
	if err != nil || len(records) != 3 {
		t.Fatalf("duplicate ordered blocks differ: %v", err)
	}
	data, err := document.ReadSpan(document.Source)
	if err != nil {
		t.Fatal(err)
	}
	reader := &patternFixtureCountingReader{data: data}
	for _, record := range records {
		for _, array := range record.Arrays {
			if array.Data.Length != 0 {
				reader.payload = append(reader.payload, array.Data)
			}
		}
	}
	source, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader.touched = false
	view, err := source.ReadPatternView(nil)
	if err != nil || reader.touched || len(view.Records) != 3 || view.MetadataBytes <= 0 {
		t.Fatalf("index read payloads or failed to report a reservation: %v", err)
	}
	record := view.Records[0]
	if record.Version != 1 || record.ColorMode != 3 || record.VMVersion != 3 || record.VMChannels != 3 ||
		record.VMBounds != image.Rect(0, 0, 2, 2) || len(record.Arrays) != 5 || record.Arrays[0].Data.Length != 4 ||
		record.ID != "fixture" || record.Name != "Fixture pattern" {
		t.Fatal("typed pattern layout differs from independent binary fixture")
	}
	view.Records[0].Arrays[0].Depth = 32
	again, err := source.Patterns(nil)
	if err != nil || again[0].Arrays[0].Depth != 8 || reader.touched {
		t.Fatalf("record metadata borrowed mutable source storage: %v", err)
	}
}

func TestPatternsIndexLayerAndAlternateLayerSetsInPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		pattern := patternFixture(8, 0, []byte{10, 20, 30, 40})
		primary := indexTestLayerSet(version, image.Rect(0, 0, 1, 1), nil, nil, nil,
			indexTestTag(version, "8BIM", "Pat2", pattern, 2))
		alternate := indexTestLayerSet(version, image.Rect(0, 0, 1, 1), nil, nil, nil,
			indexTestTag(version, "8BIM", "Pat3", pattern, 2))
		tags := indexTestTag(version, "8BIM", "Patt", pattern, 4)
		tags = append(tags, indexTestTag(version, "8BIM", "Lr16", alternate, 4)...)
		data := indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, primary, tags), []byte{0, 0, 0})
		document, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		records, err := document.Patterns(nil)
		if err != nil || len(records) != 3 {
			t.Fatalf("version %d did not index every physical pattern block: %v", version, err)
		}
		for index, record := range records {
			if index > 0 && record.Span.Offset <= records[index-1].Span.Offset {
				t.Fatal("pattern blocks lost source order across layer sets")
			}
			pixels, err := document.DecodePattern(nil, record)
			if err != nil || !bytes.Equal(pixels.Channels[0].Plane.Data, []byte{10, 20, 30, 40}) {
				t.Fatalf("version %d changed pattern samples: %v", version, err)
			}
		}
		_ = document.Close()
	}
}

func TestDecodePatternPreservesDepthAndCompressionSamples(t *testing.T) {
	for _, depth := range []uint16{1, 8, 16, 32} {
		samples := []byte{0x80, 0x40}
		if depth != 1 {
			samples = make([]byte, 4*int(depth)/8)
			for index := range samples {
				samples[index] = byte(index + 1)
			}
		}
		for _, compression := range []byte{0, 2} {
			encoded := samples
			if compression == 2 {
				var buffer bytes.Buffer
				writer := zlib.NewWriter(&buffer)
				if _, err := writer.Write(samples); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				encoded = buffer.Bytes()
			}
			document := patternFixtureSource(t, patternFixture(depth, compression, encoded), "Patt")
			records, err := document.Patterns(nil)
			if err != nil {
				t.Fatal(err)
			}
			pixels, err := document.DecodePattern(nil, records[0])
			if err != nil || len(pixels.Channels) != 3 {
				t.Fatalf("original-depth decode differs: %v", err)
			}
			for _, channel := range pixels.Channels {
				if channel.Plane.Depth != depth || !bytes.Equal(channel.Plane.Data, samples) {
					t.Fatal("original sample bits changed during pattern decode")
				}
			}
		}
	}
	rle := []byte{0, 3, 0, 3, 1, 10, 20, 1, 30, 40}
	document := patternFixtureSource(t, patternFixture(8, 1, rle), "Patt")
	records, err := document.Patterns(nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := document.DecodePattern(nil, records[0])
	if err != nil || !bytes.Equal(decoded.Channels[0].Plane.Data, []byte{10, 20, 30, 40}) {
		t.Fatalf("pattern row tables must use uint16 even independently of PSD/PSB source widths: %v", err)
	}
}

func TestPatternsUnknownVersionsLimitsContextAndStrictIO(t *testing.T) {
	unknown := descriptorFixtureU32(4)
	unknown = append(unknown, descriptorFixtureU32(7)...)
	document := patternFixtureSource(t, unknown, "Patt")
	records, err := document.Patterns(nil)
	if err != nil || len(records) != 1 || len(records[0].Unsupported) == 0 || records[0].Version != 7 {
		t.Fatalf("unknown record version was discarded instead of retained: %v", err)
	}
	if _, err := document.DecodePattern(nil, records[0]); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown retained version was decoded: %v", err)
	}
	valid := patternFixtureSource(t, patternFixture(8, 0, []byte{10, 20, 30, 40}), "Patt")
	if _, err := valid.Patterns(nil, MetadataOptions{MaxBytes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("metadata budget ignored: %v", err)
	}
	records, err = valid.Patterns(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := valid.DecodePattern(nil, records[0], PatternDecodeOptions{MaxWorkingBytes: 1000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("decode aggregate scratch escaped the budget: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := valid.Patterns(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled pattern indexing accepted: %v", err)
	}
	data, _ := valid.ReadSpan(valid.Source)
	reader := &linkedTestReader{reader: bytes.NewReader(data)}
	source, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reader.failure = errors.New("fixture pattern payload failure")
	if _, err := source.DecodePattern(nil, records[0]); !errors.Is(err, reader.failure) || !errors.Is(err, ErrMalformed) {
		t.Fatalf("full ReaderAt failure was erased by streaming decode: %v", err)
	}
	reader.failure, reader.zero = nil, true
	if _, err := source.DecodePattern(nil, records[0]); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("non-progressing pattern source accepted: %v", err)
	}
	if err := valid.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := valid.Patterns(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed pattern source accepted: %v", err)
	}
}

func patternFillFixture(extra ...descriptorFixtureEntry) []byte {
	pattern := descriptorFixtureBody("", "Ptrn",
		descriptorFixtureEntry{key: "Nm  ", kind: "TEXT", data: descriptorFixtureUnicode("Fixture pattern")},
		descriptorFixtureEntry{key: "Idnt", kind: "TEXT", data: descriptorFixtureUnicode("fixture")},
	)
	phase := descriptorFixtureBody("", "Pnt ",
		descriptorFixtureEntry{key: "Hrzn", kind: "doub", data: descriptorFixtureU64(0)},
		descriptorFixtureEntry{key: "Vrtc", kind: "doub", data: descriptorFixtureU64(0)},
	)
	entries := []descriptorFixtureEntry{
		{key: "Ptrn", kind: "Objc", data: pattern},
		{key: "Scl ", kind: "UntF", data: gradientFixtureUnit("#Prc", 100)},
		{key: "Angl", kind: "UntF", data: gradientFixtureUnit("#Ang", 0)},
		{key: "Lnkd", kind: "bool", data: []byte{1}},
		{key: "phase", kind: "Objc", data: phase},
	}
	entries = append(entries, extra...)
	return append(descriptorFixtureU32(16), descriptorFixtureBody("", "null", entries...)...)
}

func TestPatternFillOwnedDescriptorResolutionAndUnknownFields(t *testing.T) {
	pattern := patternFixture(8, 0, []byte{10, 20, 30, 40})
	fill := patternFillFixture()
	global := indexTestGlobal(1, nil,
		append(indexTestTag(1, "8BIM", "Patt", pattern, 4), indexTestTag(1, "8BIM", "PtFl", fill, 4)...))
	data := indexTestDocument(1, 8, 3, nil, global, []byte{0, 0, 0})
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	view, err := document.DecodePatternFill(nil, document.AdditionalInfo[1])
	if err != nil || view.Fill.ID != "fixture" || view.Fill.Name != "Fixture pattern" ||
		view.Fill.Scale != 100 || view.Fill.Angle != 0 || !view.Fill.Link || view.Fill.Phase != (psdpattern.Phase{}) {
		t.Fatalf("typed pattern fill differs: %v", err)
	}
	record, err := document.ResolvePattern(nil, *view.Fill)
	if err != nil || record.ID != "fixture" {
		t.Fatalf("local retained pattern did not resolve: %v", err)
	}
	estimate, err := document.EstimatePatternTileRGB8(*record)
	if err != nil || estimate.RetainedBytes != 16 || estimate.WorkingBytes <= estimate.RetainedBytes {
		t.Fatalf("tile decode estimate differs from owned output: %v", err)
	}
	if _, err := document.DecodePatternTileRGB8(nil, *record,
		PatternDecodeOptions{MaxWorkingBytes: estimate.WorkingBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("combined output/scratch preflight exceeded the exact estimate: %v", err)
	}
	if _, err := document.DecodePatternTileRGB8(nil, *record,
		PatternDecodeOptions{MaxWorkingBytes: estimate.WorkingBytes}); err != nil {
		t.Fatalf("documented tile working estimate did not permit its decode: %v", err)
	}
	tile, err := document.DecodePatternTileRGB8(nil, *record)
	if err != nil || !bytes.Equal(tile.Pix, []byte{10, 10, 10, 255, 20, 20, 20, 255,
		30, 30, 30, 255, 40, 40, 40, 255}) {
		t.Fatalf("RGB tile or default transparency differs: %v", err)
	}
	missing := *view.Fill
	missing.ID = "missing-fixture"
	if _, err := document.ResolvePattern(nil, missing); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("missing external pattern was implicitly resolved: %v", err)
	}
	duplicate := patternFixtureSource(t, pattern, "Patt", "Pat3")
	if _, err := duplicate.ResolvePattern(nil, *view.Fill); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicate local references were silently collapsed: %v", err)
	}
	unknown := patternFillFixture(descriptorFixtureEntry{key: "unknown", kind: "bool", data: []byte{1}})
	raw := descriptorFixtureDocument(unknown, DefaultLimits())
	block := AdditionalBlock{Key: "PtFl", Data: Span{Length: int64(len(unknown))}}
	retained, err := raw.DecodePatternFill(nil, block)
	if !errors.Is(err, ErrUnsupported) || retained == nil || len(retained.Descriptor.Entries) != 6 ||
		len(retained.Fill.Unsupported) == 0 {
		t.Fatalf("unknown pattern descriptor metadata discarded or ignored: %v", err)
	}
}
