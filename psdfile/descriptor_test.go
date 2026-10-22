package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"unicode/utf16"
)

type descriptorFixtureEntry struct {
	key, kind string
	data      []byte
	compact   bool
}

func descriptorFixtureU32(value uint32) []byte {
	return binary.BigEndian.AppendUint32(nil, value)
}

func descriptorFixtureU64(value uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, value)
}

func descriptorFixtureUnicode(value string) []byte {
	codes := utf16.Encode([]rune(value))
	data := descriptorFixtureU32(uint32(len(codes)))
	for _, code := range codes {
		data = binary.BigEndian.AppendUint16(data, code)
	}
	return data
}

func descriptorFixtureID(value string, compact bool) []byte {
	if compact {
		return append(descriptorFixtureU32(0), value...)
	}
	return append(descriptorFixtureU32(uint32(len(value))), value...)
}

func descriptorFixtureClass(name, classID string) []byte {
	return append(descriptorFixtureUnicode(name), descriptorFixtureID(classID, false)...)
}

func descriptorFixtureBody(name, classID string, entries ...descriptorFixtureEntry) []byte {
	data := descriptorFixtureClass(name, classID)
	data = append(data, descriptorFixtureU32(uint32(len(entries)))...)
	for _, entry := range entries {
		data = append(data, descriptorFixtureID(entry.key, entry.compact)...)
		data = append(data, entry.kind...)
		data = append(data, entry.data...)
	}
	return data
}

func descriptorFixtureDocument(data []byte, limits Limits) *Document {
	return &Document{reader: bytes.NewReader(data), size: int64(len(data)), limits: limits}
}

func descriptorFixtureDecode(t *testing.T, data []byte, versioned bool) (*Document, *Descriptor) {
	t.Helper()
	document := descriptorFixtureDocument(data, DefaultLimits())
	descriptor, err := document.DecodeDescriptor(Span{Length: int64(len(data))}, versioned)
	if err != nil {
		t.Fatal(err)
	}
	return document, descriptor
}

func TestDecodeDescriptorPreservesTypesOrderAndEncoding(t *testing.T) {
	largeNegative := int64(-1<<61) + 9
	float64Bits := uint64(0xfff8000000001234)
	float32Bits := uint32(0xffc12345)
	data := descriptorFixtureBody("中文🌟\x00", "null",
		descriptorFixtureEntry{key: "same", kind: "long", data: descriptorFixtureU32(0xfffffff9), compact: true},
		descriptorFixtureEntry{key: "same", kind: "comp", data: descriptorFixtureU64(uint64(largeNegative))},
		descriptorFixtureEntry{key: "text", kind: "TEXT", data: descriptorFixtureUnicode("前\x00后😀\x00")},
		descriptorFixtureEntry{key: "enum", kind: "enum", data: append(
			descriptorFixtureID("type.with.dot", false), descriptorFixtureID("value.with.dot", false)...,
		)},
		descriptorFixtureEntry{key: "zero", kind: "doub", data: descriptorFixtureU64(1 << 63)},
		descriptorFixtureEntry{key: "unit", kind: "UntF", data: append([]byte("#Pxl"), descriptorFixtureU64(float64Bits)...)},
		descriptorFixtureEntry{key: "unit", kind: "UnFl", data: append([]byte("#Prc"), descriptorFixtureU32(float32Bits)...)},
		descriptorFixtureEntry{key: "bool", kind: "bool", data: []byte{2}},
		descriptorFixtureEntry{key: "clas", kind: "GlbC", data: descriptorFixtureClass("图层\x00", "Lyr ")},
	)
	data = append(descriptorFixtureU32(16), data...)
	document, descriptor := descriptorFixtureDecode(t, data, true)
	if descriptor.Version != 16 || descriptor.Name != "中文🌟" || descriptor.ClassID != "null" {
		t.Fatalf("incorrect descriptor header: version=%d, name length=%d", descriptor.Version, len(descriptor.Name))
	}
	if descriptor.Span.Length != int64(len(data)) || descriptor.VersionData != (Span{Length: 4}) {
		t.Fatalf("incorrect source span: %+v", descriptor.Span)
	}
	entries := descriptor.Entries
	if len(entries) != 9 || entries[0].Key != "same" || entries[1].Key != "same" {
		t.Fatal("duplicate keys or physical entry order were lost")
	}
	if entries[0].Value != int32(-7) || entries[1].Value != largeNegative {
		t.Fatal("integer type or signed precision was lost")
	}
	if entries[2].Value != "前\x00后😀" {
		t.Fatal("UTF-16 surrogate pairs or embedded null were lost")
	}
	enumeration := entries[3].Value.(DescriptorEnum)
	if enumeration.TypeID != "type.with.dot" || enumeration.ValueID != "value.with.dot" {
		t.Fatal("enumeration identifiers were merged")
	}
	if math.Float64bits(entries[4].Value.(float64)) != 1<<63 {
		t.Fatal("negative zero was normalized")
	}
	unit64, unit32 := entries[5].Value.(DescriptorUnit), entries[6].Value.(DescriptorUnit)
	if unit64.Width != 64 || unit64.Bits != float64Bits ||
		math.Float64bits(unit64.Number.(float64)) != float64Bits {
		t.Fatal("64-bit unit number did not preserve the original IEEE payload")
	}
	if unit32.Width != 32 || unit32.Bits != uint64(float32Bits) ||
		math.Float32bits(unit32.Number.(float32)) != float32Bits {
		t.Fatal("32-bit unit number did not preserve the original IEEE payload")
	}
	if entries[7].Value != true || entries[8].Value.(DescriptorClass).Name != "图层" {
		t.Fatal("boolean or class structure was decoded incorrectly")
	}
	firstKey, err := document.ReadSpan(entries[0].KeyData)
	if err != nil || binary.BigEndian.Uint32(firstKey) != 0 {
		t.Fatalf("compact key encoding is unavailable: %v", err)
	}
	secondKey, err := document.ReadSpan(entries[1].KeyData)
	if err != nil || binary.BigEndian.Uint32(secondKey) != 4 {
		t.Fatalf("explicit-length key encoding is unavailable: %v", err)
	}
	booleanData, err := document.ReadSpan(entries[7].Data)
	if err != nil || !bytes.Equal(booleanData, []byte{2}) {
		t.Fatalf("noncanonical true byte was normalized: %v", err)
	}
	for index, entry := range entries {
		if index > 0 && entry.Span.Offset != entries[index-1].Span.Offset+entries[index-1].Span.Length {
			t.Fatal("entry spans are not contiguous")
		}
		encoded, err := document.ReadSpan(entry.Span)
		if err != nil || !bytes.Equal(encoded, data[entry.Span.Offset:entry.Span.Offset+entry.Span.Length]) {
			t.Fatalf("original entry bytes not preserved at entry %d: %v", index, err)
		}
	}
}

func TestDecodeDescriptorNestedTypedLists(t *testing.T) {
	nested := descriptorFixtureBody("nested", "Glbl",
		descriptorFixtureEntry{key: "flag", kind: "bool", data: []byte{1}},
	)
	list := descriptorFixtureU32(3)
	list = append(list, "long"...)
	list = append(list, descriptorFixtureU32(0xfffffffe)...)
	list = append(list, "GlbO"...)
	list = append(list, nested...)
	list = append(list, "VlLs"...)
	list = append(list, descriptorFixtureU32(0)...)
	data := descriptorFixtureBody("", "root",
		descriptorFixtureEntry{key: "list", kind: "VlLs", data: list},
	)
	_, descriptor := descriptorFixtureDecode(t, data, false)
	items := descriptor.Entries[0].Value.([]DescriptorValue)
	if len(items) != 3 || items[0].Type != "long" || items[0].Value != int32(-2) {
		t.Fatal("list item types or order were lost")
	}
	object := items[1].Value.(*Descriptor)
	if items[1].Type != "GlbO" || object.Name != "nested" || object.Entries[0].Value != true {
		t.Fatal("nested global descriptor was lost")
	}
	if len(items[2].Value.([]DescriptorValue)) != 0 {
		t.Fatal("empty nested list was lost")
	}
	if object.Span != items[1].Data || items[0].Span.Length != 8 || items[0].Data.Length != 4 {
		t.Fatal("nested value span did not identify the original encoding")
	}
}

type descriptorRecordingReader struct {
	reader *bytes.Reader
	reads  []Span
}

func (r *descriptorRecordingReader) ReadAt(data []byte, offset int64) (int, error) {
	r.reads = append(r.reads, Span{Offset: offset, Length: int64(len(data))})
	return r.reader.ReadAt(data, offset)
}

func TestDecodeDescriptorBlobPayloadsStayLazy(t *testing.T) {
	largeBlob := bytes.Repeat([]byte{0xa5}, 1<<20)
	entries := make([]descriptorFixtureEntry, 3)
	for index, kind := range []string{"alis", "tdta", "Pth "} {
		entries[index] = descriptorFixtureEntry{
			key: "blob", kind: kind, data: append(descriptorFixtureU32(uint32(len(largeBlob))), largeBlob...),
		}
	}
	data := descriptorFixtureBody("", "root", entries...)
	limits := DefaultLimits()
	limits.MaxMetadataBytes, limits.MaxReadBytes = 2048, 64
	document := descriptorFixtureDocument(data, limits)
	recording := &descriptorRecordingReader{reader: bytes.NewReader(data)}
	document.reader = recording
	descriptor, err := document.DecodeDescriptor(Span{Length: int64(len(data))}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range descriptor.Entries {
		blob := entry.Value.(DescriptorBlob)
		if blob.Data.Length != int64(len(largeBlob)) || blob.Span != entry.Data {
			t.Fatal("blob did not preserve its length word and payload spans")
		}
		for _, read := range recording.reads {
			if read.Offset < blob.Data.Offset+blob.Data.Length && read.Offset+read.Length > blob.Data.Offset {
				t.Fatal("descriptor decoding eagerly read blob payload")
			}
		}
		if _, err := document.ReadSpan(blob.Data); !errors.Is(err, ErrLimit) {
			t.Fatalf("large blob read did not respect MaxReadBytes: %v", err)
		}
		var output bytes.Buffer
		if _, err := document.CopySpan(context.Background(), &output, blob.Data); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(output.Bytes(), largeBlob) {
			t.Fatal("original blob could not be streamed unchanged")
		}
		recording.reads = nil
	}
}

func TestDecodeDescriptorReferences(t *testing.T) {
	class := descriptorFixtureClass("层\x00", "Lyr ")
	references := []descriptorFixtureEntry{
		{kind: "prop", data: append(bytes.Clone(class), descriptorFixtureID("Nm  ", true)...)},
		{kind: "Clss", data: class},
		{kind: "Enmr", data: append(bytes.Clone(class), append(
			descriptorFixtureID("Ordn", true), descriptorFixtureID("Trgt", true)...,
		)...)},
		{kind: "rele", data: append(bytes.Clone(class), descriptorFixtureU32(0xfffffffe)...)},
		{kind: "Idnt", data: descriptorFixtureU32(123)},
		{kind: "indx", data: descriptorFixtureU32(0xffffffff)},
		{kind: "name", data: append(bytes.Clone(class), descriptorFixtureUnicode("选中😀\x00")...)},
	}
	payload := descriptorFixtureU32(uint32(len(references)))
	for _, item := range references {
		payload = append(payload, item.kind...)
		payload = append(payload, item.data...)
	}
	data := descriptorFixtureBody("", "root", descriptorFixtureEntry{key: "ref ", kind: "obj ", data: payload})
	_, descriptor := descriptorFixtureDecode(t, data, false)
	reference := descriptor.Entries[0].Value.(*DescriptorReference)
	if len(reference.Items) != 7 || reference.Span != descriptor.Entries[0].Data {
		t.Fatal("reference items or source spans were lost")
	}
	if reference.Items[0].Value.(DescriptorProperty).KeyID != "Nm  " ||
		reference.Items[1].Value.(DescriptorClass).ClassID != "Lyr " {
		t.Fatal("property or class reference was lost")
	}
	enumerated := reference.Items[2].Value.(DescriptorEnumeratedReference)
	if enumerated.Class.Name != "层" || enumerated.Enum.TypeID != "Ordn" || enumerated.Enum.ValueID != "Trgt" {
		t.Fatal("enumerated reference class or identifiers were lost")
	}
	if reference.Items[3].Value.(DescriptorOffset).Offset != 0xfffffffe ||
		reference.Items[4].Value != int32(123) || reference.Items[5].Value != int32(-1) ||
		reference.Items[6].Value.(DescriptorNamedReference).Name != "选中😀" {
		t.Fatal("offset, identifier, index or named reference was decoded incorrectly")
	}
}

func TestDecodeDescriptorObjectArray(t *testing.T) {
	bits := []uint64{math.Float64bits(1.25), 1 << 63, 0x7ff8000000001234}
	column := append([]byte("#Pxl"), descriptorFixtureU32(uint32(len(bits)))...)
	for _, value := range bits {
		column = append(column, descriptorFixtureU64(value)...)
	}
	array := descriptorFixtureBody("mesh", "rationalPoint",
		descriptorFixtureEntry{key: "Hrzn", kind: "UnFl", data: column, compact: true},
		descriptorFixtureEntry{key: "Hrzn", kind: "UnFl", data: column},
	)
	array = append(descriptorFixtureU32(16), array...)
	data := descriptorFixtureBody("", "root", descriptorFixtureEntry{key: "mesh", kind: "ObAr", data: array})
	document, descriptor := descriptorFixtureDecode(t, data, false)
	object := descriptor.Entries[0].Value.(*DescriptorObjectArray)
	if object.Prefix != 16 || object.Name != "mesh" || object.ClassID != "rationalPoint" || len(object.Entries) != 2 {
		t.Fatal("object array header or duplicate columns were lost")
	}
	for _, entry := range object.Entries {
		values := entry.Value.(DescriptorUnitArray)
		if values.Unit != "#Pxl" || len(values.Numbers) != len(bits) || values.Span != entry.Data {
			t.Fatal("object array unit or encoded value span was lost")
		}
		for index, number := range values.Numbers {
			if math.Float64bits(number) != bits[index] {
				t.Fatal("object array numbers were normalized")
			}
		}
		original, err := document.ReadSpan(values.NumbersData)
		if err != nil || !bytes.Equal(original, column[8:]) {
			t.Fatalf("object array original sample bytes are unavailable: %v", err)
		}
	}
}

func TestDecodeDescriptorLimitsAndMalformedInput(t *testing.T) {
	valid := descriptorFixtureBody("", "root",
		descriptorFixtureEntry{key: "text", kind: "TEXT", data: descriptorFixtureUnicode("a reasonably long string")},
	)
	for _, test := range []struct {
		name      string
		configure func(*Limits)
	}{
		{name: "metadata", configure: func(limits *Limits) { limits.MaxMetadataBytes = 200 }},
		{name: "read", configure: func(limits *Limits) { limits.MaxReadBytes = 16 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.configure(&limits)
			document := descriptorFixtureDocument(valid, limits)
			_, err := document.DecodeDescriptor(Span{Length: int64(len(valid))}, false)
			if !errors.Is(err, ErrLimit) {
				t.Fatalf("expected resource limit error, got %v", err)
			}
		})
	}
	nested := descriptorFixtureBody("", "root",
		descriptorFixtureEntry{key: "nest", kind: "Objc", data: descriptorFixtureBody("", "nest")},
	)
	limits := DefaultLimits()
	limits.MaxDescriptorDepth = 1
	if _, err := descriptorFixtureDocument(nested, limits).DecodeDescriptor(
		Span{Length: int64(len(nested))}, false,
	); !errors.Is(err, ErrLimit) {
		t.Fatalf("expected depth limit error, got %v", err)
	}
	list := descriptorFixtureU32(2)
	list = append(list, []byte("bool\x01bool\x00")...)
	countData := descriptorFixtureBody("", "root", descriptorFixtureEntry{key: "list", kind: "VlLs", data: list})
	limits = DefaultLimits()
	limits.MaxDescriptorItems = 2
	if _, err := descriptorFixtureDocument(countData, limits).DecodeDescriptor(
		Span{Length: int64(len(countData))}, false,
	); !errors.Is(err, ErrLimit) {
		t.Fatalf("nested counts should share a per-call item budget, got %v", err)
	}
	for length := range len(valid) {
		document := descriptorFixtureDocument(valid[:length], DefaultLimits())
		if _, err := document.DecodeDescriptor(Span{Length: int64(length)}, false); !errors.Is(err, ErrMalformed) {
			t.Fatalf("truncation at byte %d: expected ErrMalformed, got %v", length, err)
		}
	}
	for _, span := range []Span{{Offset: -1, Length: 4}, {Length: -1}, {Offset: 1, Length: math.MaxInt64}} {
		_, err := descriptorFixtureDocument(valid, DefaultLimits()).DecodeDescriptor(span, false)
		if !errors.Is(err, ErrMalformed) {
			t.Fatalf("invalid span did not return ErrMalformed: %v", err)
		}
	}
	oversizeCount := append(descriptorFixtureClass("", "root"), descriptorFixtureU32(math.MaxUint32)...)
	if _, err := descriptorFixtureDocument(oversizeCount, DefaultLimits()).DecodeDescriptor(
		Span{Length: int64(len(oversizeCount))}, false,
	); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized count did not return ErrLimit: %v", err)
	}
}

func TestDecodeDescriptorUnsupportedDoesNotLoseRawSource(t *testing.T) {
	secretID := "a-private-opaque-object-identifier"
	unknown := descriptorFixtureBody("", "root", descriptorFixtureEntry{key: secretID, kind: "????"})
	document := descriptorFixtureDocument(unknown, DefaultLimits())
	span := Span{Length: int64(len(unknown))}
	if _, err := document.DecodeDescriptor(span, false); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown OSType did not return ErrUnsupported: %v", err)
	} else {
		var detail *Error
		if !errors.As(err, &detail) || detail.Path != "descriptor.entries[0].type" || detail.Offset <= 0 {
			t.Fatal("unknown OSType error does not provide a field path and source offset")
		}
		if strings.Contains(err.Error(), secretID) {
			t.Fatal("error exposed an opaque object identifier")
		}
	}
	referenceData := append(descriptorFixtureU32(1), []byte("????")...)
	reference := descriptorFixtureBody("", "root",
		descriptorFixtureEntry{key: "ref ", kind: "obj ", data: referenceData},
	)
	if _, err := descriptorFixtureDocument(reference, DefaultLimits()).DecodeDescriptor(
		Span{Length: int64(len(reference))}, false,
	); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown reference type did not return ErrUnsupported: %v", err)
	}
	raw, err := document.ReadSpan(span)
	if err != nil || !bytes.Equal(raw, unknown) {
		t.Fatalf("unsupported descriptor discarded the original bytes: %v", err)
	}
	versioned := append(descriptorFixtureU32(17), descriptorFixtureBody("", "root")...)
	if _, err := descriptorFixtureDocument(versioned, DefaultLimits()).DecodeDescriptor(
		Span{Length: int64(len(versioned))}, true,
	); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported descriptor version did not return ErrUnsupported: %v", err)
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := document.DecodeDescriptor(span, false); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed document did not return ErrClosed: %v", err)
	}
}

type descriptorFailingReader struct{}

func (descriptorFailingReader) ReadAt(_ []byte, _ int64) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestDecodeDescriptorReaderErrorIncludesCause(t *testing.T) {
	document := &Document{reader: descriptorFailingReader{}, size: 100, limits: DefaultLimits()}
	_, err := document.DecodeDescriptor(Span{Offset: 10, Length: 50}, false)
	if !errors.Is(err, ErrMalformed) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("underlying ReaderAt error was lost: %v", err)
	}
	var detail *Error
	if !errors.As(err, &detail) || detail.Offset != 10 || detail.Path != "descriptor.name" {
		t.Fatal("I/O error omitted its source position and field path")
	}
}

func TestDecodeDescriptorConsumesOneObjectWithinContainingSpan(t *testing.T) {
	body := descriptorFixtureBody("", "root")
	data := append([]byte{8, 9}, body...)
	data = append(data, 10, 11, 12)
	document := descriptorFixtureDocument(data, DefaultLimits())
	result, err := document.DecodeDescriptor(Span{Offset: 2, Length: int64(len(data) - 2)}, false)
	if err != nil || result.Span != (Span{Offset: 2, Length: int64(len(body))}) {
		t.Fatalf("descriptor consumed following unrelated fields: %v", err)
	}
}

func FuzzDecodeDescriptor(f *testing.F) {
	f.Add(descriptorFixtureBody("", "root"))
	f.Add(descriptorFixtureBody("测试😀\x00", "root",
		descriptorFixtureEntry{key: "blob", kind: "tdta", data: []byte{0, 0, 0, 1, 0xff}},
	))
	arrayColumn := append([]byte("#Pxl"), descriptorFixtureU32(2)...)
	arrayColumn = append(arrayColumn, descriptorFixtureU64(math.Float64bits(1.5))...)
	arrayColumn = append(arrayColumn, descriptorFixtureU64(math.Float64bits(-2.5))...)
	array := append(descriptorFixtureU32(16), descriptorFixtureBody("", "rationalPoint",
		descriptorFixtureEntry{key: "Hrzn", kind: "UnFl", data: arrayColumn},
	)...)
	f.Add(descriptorFixtureBody("", "root", descriptorFixtureEntry{key: "mesh", kind: "ObAr", data: array}))
	ref := append(descriptorFixtureU32(1), []byte("name")...)
	ref = append(ref, descriptorFixtureClass("", "Lyr ")...)
	ref = append(ref, descriptorFixtureUnicode("图层\x00")...)
	f.Add(descriptorFixtureBody("", "root", descriptorFixtureEntry{key: "ref ", kind: "obj ", data: ref}))
	f.Fuzz(func(t *testing.T, data []byte) {
		limits := DefaultLimits()
		limits.MaxMetadataBytes, limits.MaxReadBytes = 4096, 4096
		limits.MaxDescriptorItems, limits.MaxDescriptorDepth = 50, 8
		document := descriptorFixtureDocument(data, limits)
		result, err := document.DecodeDescriptor(Span{Length: int64(len(data))}, false)
		if err == nil && (result == nil || !document.validSpan(result.Span)) {
			t.Fatal("successful descriptor decode returned an invalid source span")
		}
	})
}
