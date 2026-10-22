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
)

func descriptorEncodeFixture() []byte {
	class := descriptorFixtureClass("层\x00", "Lyr ")
	reference := descriptorFixtureU32(7)
	for _, item := range []descriptorFixtureEntry{
		{kind: "prop", data: append(bytes.Clone(class), descriptorFixtureID("Nm  ", true)...)},
		{kind: "Clss", data: class},
		{kind: "Enmr", data: append(bytes.Clone(class), append(
			descriptorFixtureID("Ordn", true), descriptorFixtureID("Trgt", false)...,
		)...)},
		{kind: "rele", data: append(bytes.Clone(class), descriptorFixtureU32(0xfffffffe)...)},
		{kind: "Idnt", data: descriptorFixtureU32(0x80000001)},
		{kind: "indx", data: descriptorFixtureU32(0xffffffff)},
		{kind: "name", data: append(bytes.Clone(class), descriptorFixtureUnicode("选中😀\x00")...)},
	} {
		reference = append(reference, item.kind...)
		reference = append(reference, item.data...)
	}
	column := append([]byte("#Pxl"), descriptorFixtureU32(3)...)
	for _, bits := range []uint64{1 << 63, math.Float64bits(1.25), 0xfff8000000001234} {
		column = append(column, descriptorFixtureU64(bits)...)
	}
	array := append(descriptorFixtureU32(0x87654321), descriptorFixtureBody("mesh", "rationalPoint",
		descriptorFixtureEntry{key: "Hrzn", kind: "UnFl", data: column, compact: true},
		descriptorFixtureEntry{key: "Hrzn", kind: "UnFl", data: column},
	)...)
	nested := descriptorFixtureBody("nested", "root",
		descriptorFixtureEntry{key: "bool", kind: "bool", data: []byte{0x80}},
	)
	list := descriptorFixtureU32(2)
	list = append(list, "long"...)
	list = append(list, descriptorFixtureU32(0xfffffffd)...)
	list = append(list, "Objc"...)
	list = append(list, nested...)
	entries := []descriptorFixtureEntry{
		{key: "same", kind: "long", data: descriptorFixtureU32(0xfffffff9), compact: true},
		{key: "same", kind: "comp", data: descriptorFixtureU64(0xf123456789abcdef)},
		{key: "text", kind: "TEXT", data: descriptorFixtureUnicode("前\x00后😀\x00")},
		{key: "text", kind: "TEXT", data: descriptorFixtureUnicode("无终止符")},
		{key: "enum", kind: "enum", data: append(
			descriptorFixtureID("type.with.dot", false), descriptorFixtureID("Abcd", false)...,
		)},
		{key: "zero", kind: "doub", data: descriptorFixtureU64(1 << 63)},
		{key: "nan ", kind: "doub", data: descriptorFixtureU64(0x7ff0000000001234)},
		{key: "unit", kind: "UntF", data: append([]byte("#Pxl"), descriptorFixtureU64(0xfff8000000001234)...)},
		{key: "unit", kind: "UnFl", data: append([]byte("#Prc"), descriptorFixtureU32(0xffc12345)...)},
		{key: "bool", kind: "bool", data: []byte{2}},
		{key: "clas", kind: "type", data: class},
		{key: "clas", kind: "GlbC", data: class},
		{key: "nest", kind: "Objc", data: nested},
		{key: "glob", kind: "GlbO", data: nested},
		{key: "list", kind: "VlLs", data: list},
		{key: "ref ", kind: "obj ", data: reference},
		{key: "mesh", kind: "ObAr", data: array},
	}
	for _, kind := range []string{"alis", "tdta", "Pth "} {
		payload := []byte{0x78, 0x9c, 0, 0xff, 0x42, 0, 7, 11}
		entries = append(entries, descriptorFixtureEntry{
			key: "blob", kind: kind,
			data: append(descriptorFixtureU32(uint32(len(payload))), payload...),
		})
	}
	return append(descriptorFixtureU32(16), descriptorFixtureBody("中文🌟\x00", "null", entries...)...)
}

func TestEncodeDescriptorLosslessAllKnownTypes(t *testing.T) {
	data := descriptorEncodeFixture()
	document, descriptor := descriptorFixtureDecode(t, data, true)
	got, err := document.EncodeDescriptor(context.Background(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("ordered source descriptor did not round-trip byte-for-byte")
	}
	got, err = document.EncodeDescriptor(nil, descriptor, false)
	if err != nil || !bytes.Equal(got, data[4:]) {
		t.Fatalf("unversioned output differs: %v", err)
	}
	if len(descriptor.Entries) != 20 || descriptor.Entries[0].Key != descriptor.Entries[1].Key {
		t.Fatal("encoding mutated entries or duplicate keys")
	}
	invalidName := append(descriptorFixtureU32(1), 0xd8, 0)
	invalidName = append(invalidName, descriptorFixtureID("root", false)...)
	invalidName = append(invalidName, descriptorFixtureU32(0)...)
	source, value := descriptorFixtureDecode(t, invalidName, false)
	got, err = source.EncodeDescriptor(nil, value, false)
	if err != nil || !bytes.Equal(got, invalidName) {
		t.Fatalf("unchanged invalid-surrogate Unicode spelling was lost: %v", err)
	}
}

func TestEncodeDescriptorHonorsMutatedValuesAndNewOrder(t *testing.T) {
	data := descriptorEncodeFixture()
	document, descriptor := descriptorFixtureDecode(t, data, true)
	descriptor.Name = "已编辑🌈"
	descriptor.ClassID = "changedClass"
	descriptor.Entries[0].Key = "swap"
	descriptor.Entries[0].Value = int32(-123)
	descriptor.Entries[1].Value = int64(-1<<60) + 17
	descriptor.Entries[2].Value = "更新\x00内容😀"
	descriptor.Entries[3].Value = "替换无终止字段"
	enumeration := descriptor.Entries[4].Value.(DescriptorEnum)
	enumeration.ValueID = "newEnumID"
	descriptor.Entries[4].Value = enumeration
	descriptor.Entries[5].Value = math.Float64frombits(0x7ff8000000004321)
	unit := descriptor.Entries[7].Value.(DescriptorUnit)
	unit.Number = float64(42.5)
	descriptor.Entries[7].Value = unit
	unit32 := descriptor.Entries[8].Value.(DescriptorUnit)
	unit32.Number = nil
	unit32.Bits = 0x80000000
	descriptor.Entries[8].Value = unit32
	descriptor.Entries[9].Value = false
	class := descriptor.Entries[10].Value.(DescriptorClass)
	class.Name, class.ClassID = "新类", "newClass"
	descriptor.Entries[10].Value = class
	nested := descriptor.Entries[12].Value.(*Descriptor)
	nested.Entries[0].Value = false
	list := descriptor.Entries[14].Value.([]DescriptorValue)
	list[0].Value = int32(55)
	reference := descriptor.Entries[15].Value.(*DescriptorReference)
	named := reference.Items[6].Value.(DescriptorNamedReference)
	named.Name = "新引用😀"
	reference.Items[6].Value = named
	array := descriptor.Entries[16].Value.(*DescriptorObjectArray)
	array.Prefix = 19
	column := array.Entries[0].Value.(DescriptorUnitArray)
	column.Numbers[1] = -6.25
	array.Entries[0].Value = column
	descriptor.Entries[17].Value = []byte{9, 8, 7}
	descriptor.Entries = append(descriptor.Entries, DescriptorEntry{Key: "same", Type: "bool", Value: true})
	encoded, err := document.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	newSource, decoded := descriptorFixtureDecode(t, encoded, true)
	if decoded.Name != descriptor.Name || decoded.ClassID != descriptor.ClassID || len(decoded.Entries) != 21 {
		t.Fatal("changed descriptor header or ordered entry count was ignored")
	}
	entries := decoded.Entries
	if entries[0].Key != "swap" || entries[0].Value != int32(-123) || entries[1].Value != int64(-1<<60)+17 ||
		entries[2].Value != "更新\x00内容😀" || entries[3].Value != "替换无终止字段" {
		t.Fatal("changed keys, integers or Unicode strings were ignored")
	}
	if entries[4].Value.(DescriptorEnum).ValueID != "newEnumID" ||
		math.Float64bits(entries[5].Value.(float64)) != 0x7ff8000000004321 ||
		entries[7].Value.(DescriptorUnit).Number != float64(42.5) ||
		entries[8].Value.(DescriptorUnit).Bits != 0x80000000 || entries[9].Value != false {
		t.Fatal("changed enumeration, floating bits, unit Number or boolean was ignored")
	}
	if entries[10].Value.(DescriptorClass).Name != "新类" ||
		entries[12].Value.(*Descriptor).Entries[0].Value != false ||
		entries[14].Value.([]DescriptorValue)[0].Value != int32(55) ||
		entries[15].Value.(*DescriptorReference).Items[6].Value.(DescriptorNamedReference).Name != "新引用😀" {
		t.Fatal("changed class, nested descriptor, list or reference was ignored")
	}
	decodedArray := entries[16].Value.(*DescriptorObjectArray)
	if decodedArray.Prefix != 19 || decodedArray.Entries[0].Value.(DescriptorUnitArray).Numbers[1] != -6.25 {
		t.Fatal("changed object-array header or numeric sample was ignored")
	}
	blob, err := newSource.ReadSpan(entries[17].Value.(DescriptorBlob).Data)
	if err != nil || !bytes.Equal(blob, []byte{9, 8, 7}) {
		t.Fatalf("changed opaque blob was ignored: %v", err)
	}
	for _, index := range []int{18, 19} {
		original, originalErr := document.ReadSpan(descriptor.Entries[index].Span)
		actual, actualErr := newSource.ReadSpan(entries[index].Span)
		if originalErr != nil || actualErr != nil || !bytes.Equal(original, actual) {
			t.Fatal("an unrelated unchanged entry was reinterpreted")
		}
	}
	keyData, err := newSource.ReadSpan(entries[0].KeyData)
	if err != nil || binary.BigEndian.Uint32(keyData) != 0 {
		t.Fatal("changed four-byte key lost its original compact encoding convention")
	}
	for _, index := range []int{2, 3} {
		textData, readErr := newSource.ReadSpan(entries[index].Data)
		if readErr != nil {
			t.Fatal(readErr)
		}
		terminated := binary.BigEndian.Uint16(textData[len(textData)-2:]) == 0
		if terminated != (index == 2) {
			t.Fatal("changed text lost its original terminator convention")
		}
	}
	if !bytes.Equal(data, descriptorEncodeFixture()) {
		t.Fatal("encoding mutated source bytes")
	}
}

func TestEncodeDescriptorStandaloneAndTrailingNUL(t *testing.T) {
	value := &Descriptor{Name: "name\x00", ClassID: "root", Entries: []DescriptorEntry{
		{Key: "text", Type: "TEXT", Value: "内容😀\x00"},
		{Key: "blob", Type: "tdta", Value: []byte{1, 2, 3}},
		{Key: "unit", Type: "UnFl", Value: DescriptorUnit{Unit: "#Pxl", Bits: 0x7fc01234}},
	}}
	encoded, err := EncodeDescriptor(nil, value, true)
	if err != nil {
		t.Fatal(err)
	}
	_, decoded := descriptorFixtureDecode(t, encoded, true)
	if decoded.Version != 16 || decoded.Name != value.Name || decoded.Entries[0].Value != value.Entries[0].Value ||
		decoded.Entries[2].Value.(DescriptorUnit).Bits != 0x7fc01234 {
		t.Fatal("standalone construction lost a semantic trailing NUL or explicit float bits")
	}
	if !bytes.Equal(value.Entries[1].Value.([]byte), []byte{1, 2, 3}) {
		t.Fatal("standalone encoder mutated caller-owned blob bytes")
	}
	if value.Version != 0 {
		t.Fatal("default version changed the caller's descriptor")
	}
}

func TestEncodeDescriptorUnitNumberAndBitsEdits(t *testing.T) {
	for _, kind := range []string{"UnFl", "UntF"} {
		payload := descriptorFixtureU64(math.Float64bits(1.5))
		if kind == "UnFl" {
			payload = descriptorFixtureU32(math.Float32bits(1.5))
		}
		data := descriptorFixtureBody("", "root", descriptorFixtureEntry{
			key: "unit", kind: kind, data: append([]byte("#Pxl"), payload...),
		})
		for _, change := range []string{"number", "bits", "both", "conflict"} {
			t.Run(kind+"-"+change, func(t *testing.T) {
				source, value := descriptorFixtureDecode(t, data, false)
				unit := value.Entries[0].Value.(DescriptorUnit)
				wantedBits := math.Float64bits(2.25)
				var wantedNumber any = float64(2.25)
				if kind == "UnFl" {
					wantedBits = uint64(math.Float32bits(2.25))
					wantedNumber = float32(2.25)
				}
				if change != "bits" {
					unit.Number = wantedNumber
				}
				if change != "number" {
					unit.Bits = wantedBits
				}
				if change == "conflict" {
					unit.Bits = 1 << 31
				}
				value.Entries[0].Value = unit
				encoded, err := source.EncodeDescriptor(nil, value, false)
				if change == "conflict" {
					if !errors.Is(err, ErrMalformed) || encoded != nil {
						t.Fatalf("conflicting number representations accepted: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				_, decoded := descriptorFixtureDecode(t, encoded, false)
				if decoded.Entries[0].Value.(DescriptorUnit).Bits != wantedBits {
					t.Fatal("edited numeric representation was ignored")
				}
			})
		}
	}
}

type descriptorEncodeReader struct {
	reader      io.ReaderAt
	reads       []Span
	cancel      context.CancelFunc
	cancelAfter int
}

func (r *descriptorEncodeReader) ReadAt(data []byte, offset int64) (int, error) {
	r.reads = append(r.reads, Span{Offset: offset, Length: int64(len(data))})
	count, err := r.reader.ReadAt(data, offset)
	if r.cancel != nil && (r.cancelAfter == 0 || len(r.reads) >= r.cancelAfter) {
		r.cancel()
	}
	return count, err
}

func TestEncodeDescriptorReadsOnlyExplicitFieldsAndBlobs(t *testing.T) {
	largeBlob := bytes.Repeat([]byte{0x78, 0x9c, 0xff, 0}, 32<<10)
	data := descriptorFixtureBody("", "root", descriptorFixtureEntry{
		key: "blob", kind: "tdta", data: append(descriptorFixtureU32(uint32(len(largeBlob))), largeBlob...),
	})
	source := append(bytes.Repeat([]byte{0xa5}, 4096), data...)
	source = append(source, bytes.Repeat([]byte{0x5a}, 4096)...)
	document := descriptorFixtureDocument(source, DefaultLimits())
	descriptor, err := document.DecodeDescriptor(Span{Offset: 4096, Length: int64(len(data))}, false)
	if err != nil {
		t.Fatal(err)
	}
	reader := &descriptorEncodeReader{reader: document.reader}
	document.reader = reader
	encoded, err := document.EncodeDescriptor(nil, descriptor, false,
		DescriptorEncodeOptions{Limits: Limits{MaxReadBytes: 64}},
	)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatalf("bounded span-backed blob was not preserved: %v", err)
	}
	for _, read := range reader.reads {
		if read.Offset < 4096 || read.Offset+read.Length > 4096+int64(len(data)) || read.Length > 64 {
			t.Fatalf("encoder read unrelated data or exceeded the field read budget: %+v", read)
		}
	}
	_, err = EncodeDescriptor(nil, descriptor, false)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nonempty source blob encoded without a source: %v", err)
	}
	reader.reads = nil
	_, err = document.EncodeDescriptor(nil, descriptor, false, DescriptorEncodeOptions{MaxEncodedBytes: 1024})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized explicit blob was not bounded: %v", err)
	}
	for _, read := range reader.reads {
		if read.Length > 20 {
			t.Fatal("encoder read an oversized opaque blob before checking its output budget")
		}
	}
}

func TestEncodeDescriptorBudgetsCancellationAndClosure(t *testing.T) {
	data := descriptorEncodeFixture()
	document, descriptor := descriptorFixtureDecode(t, data, true)
	for _, options := range []DescriptorEncodeOptions{
		{MaxEncodedBytes: int64(len(data)) - 1},
		{Limits: Limits{MaxMetadataBytes: 1}},
		{Limits: Limits{MaxReadBytes: 1}},
		{Limits: Limits{MaxDescriptorItems: 1}},
		{Limits: Limits{MaxDescriptorDepth: 1}},
		{MaxEncodedBytes: -1},
		{Limits: Limits{MaxDescriptorDepth: 257}},
	} {
		encoded, err := document.EncodeDescriptor(nil, descriptor, true, options)
		if !errors.Is(err, ErrLimit) || encoded != nil {
			t.Fatalf("bad budget returned %d bytes and %v", len(encoded), err)
		}
	}
	encoded, err := document.EncodeDescriptor(nil, descriptor, true,
		DescriptorEncodeOptions{MaxEncodedBytes: int64(len(data))},
	)
	if err != nil || !bytes.Equal(encoded, data) {
		t.Fatalf("exact output byte budget rejected: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if encoded, err = document.EncodeDescriptor(ctx, descriptor, true); !errors.Is(err, context.Canceled) ||
		encoded != nil {
		t.Fatalf("pre-cancellation returned %d bytes and %v", len(encoded), err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader := &descriptorEncodeReader{reader: document.reader, cancel: cancel}
	document.reader = reader
	if encoded, err = document.EncodeDescriptor(ctx, descriptor, true); !errors.Is(err, context.Canceled) ||
		encoded != nil || len(reader.reads) != 1 {
		t.Fatalf("mid-read cancellation returned %d bytes, %d reads and %v", len(encoded), len(reader.reads), err)
	}
	lastData := descriptorFixtureBody("", "root", descriptorFixtureEntry{
		key: "blob", kind: "tdta", data: append(descriptorFixtureU32(8), []byte("payload!")...),
	})
	lastDocument, lastDescriptor := descriptorFixtureDecode(t, lastData, false)
	ctx, cancel = context.WithCancel(context.Background())
	lastReader := &descriptorEncodeReader{reader: lastDocument.reader, cancel: cancel, cancelAfter: 4}
	lastDocument.reader = lastReader
	if encoded, err = lastDocument.EncodeDescriptor(ctx, lastDescriptor, false); !errors.Is(err, context.Canceled) || encoded != nil || len(lastReader.reads) != 4 {
		t.Fatalf("last-field cancellation returned %d bytes, %d reads and %v", len(encoded), len(lastReader.reads), err)
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = document.EncodeDescriptor(nil, descriptor, true); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source accepted: %v", err)
	}
	cyclic := &Descriptor{ClassID: "root"}
	cyclic.Entries = []DescriptorEntry{{Key: "nest", Type: "Objc", Value: cyclic}}
	if _, err = EncodeDescriptor(nil, cyclic, false,
		DescriptorEncodeOptions{Limits: Limits{MaxDescriptorDepth: 8}},
	); !errors.Is(err, ErrLimit) {
		t.Fatalf("cyclic descriptor did not stop at the depth budget: %v", err)
	}
}

func TestEncodeDescriptorRejectsMalformedTypesWithoutPayloadDisclosure(t *testing.T) {
	for _, entry := range []DescriptorEntry{
		{Type: "long", Value: int64(1)},
		{Type: "comp", Value: int32(1)},
		{Type: "doub", Value: float32(1)},
		{Type: "bool", Value: byte(1)},
		{Type: "TEXT", Value: "\xff"},
		{Type: "Objc", Value: (*Descriptor)(nil)},
		{Type: "obj ", Value: (*DescriptorReference)(nil)},
		{Type: "UnFl", Value: DescriptorUnit{Unit: "#Pxl", Number: float64(1)}},
		{Type: "UntF", Value: DescriptorUnit{Unit: "#Pxl", Number: float32(1)}},
		{Type: "UnFl", Value: DescriptorUnit{Unit: "#Pxl", Width: 64}},
		{Type: "UnFl", Value: DescriptorUnit{Unit: "#Pxl", Bits: 1 << 32}},
		{Type: "UntF", Value: DescriptorUnit{Unit: "bad"}},
		{Type: "enum", Value: DescriptorEnum{TypeID: "", ValueID: "abcd"}},
	} {
		entry.Key = "private-opaque-identifier"
		value := &Descriptor{ClassID: "root", Entries: []DescriptorEntry{entry}}
		encoded, err := EncodeDescriptor(nil, value, false)
		if !errors.Is(err, ErrMalformed) || encoded != nil || strings.Contains(err.Error(), entry.Key) {
			t.Fatalf("malformed value did not fail privately: %v", err)
		}
	}
	for _, entry := range []DescriptorEntry{
		{Key: "private", Type: "????", Value: []byte("secret-payload")},
		{Key: "private", Type: "obj ", Value: &DescriptorReference{Items: []DescriptorValue{{Type: "????"}}}},
	} {
		value := &Descriptor{ClassID: "root", Entries: []DescriptorEntry{entry}}
		encoded, err := EncodeDescriptor(nil, value, false)
		if !errors.Is(err, ErrUnsupported) || encoded != nil || strings.Contains(err.Error(), "secret-payload") {
			t.Fatalf("unknown OSType did not fail privately: %v", err)
		}
	}
	if _, err := EncodeDescriptor(nil, &Descriptor{ClassID: "root", Version: 17}, true); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported descriptor version accepted: %v", err)
	}
	if _, err := EncodeDescriptor(nil, nil, false); !errors.Is(err, ErrMalformed) {
		t.Fatalf("nil descriptor accepted: %v", err)
	}
	if _, err := EncodeDescriptor(nil, &Descriptor{ClassID: "root"}, false,
		DescriptorEncodeOptions{}, DescriptorEncodeOptions{},
	); !errors.Is(err, ErrMalformed) {
		t.Fatalf("multiple options accepted: %v", err)
	}
}

func TestEncodeDescriptorSourceFailuresAndInheritedLimits(t *testing.T) {
	data := descriptorFixtureBody("name", "root")
	document, descriptor := descriptorFixtureDecode(t, data, false)
	document.reader = descriptorFailingReader{}
	if encoded, err := document.EncodeDescriptor(nil, descriptor, false); !errors.Is(err, ErrMalformed) ||
		encoded != nil {
		t.Fatalf("source failure returned %d bytes and %v", len(encoded), err)
	}
	document.reader = boundaryReaderAt{reader: bytes.NewReader(data)}
	if encoded, err := document.EncodeDescriptor(nil, descriptor, false); err != nil || !bytes.Equal(encoded, data) {
		t.Fatalf("complete read with EOF rejected: %v", err)
	}
	descriptor.ClassIDData = Span{Offset: int64(len(data)), Length: 4}
	if _, err := document.EncodeDescriptor(nil, descriptor, false); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid source-field span accepted: %v", err)
	}
	value := &Descriptor{ClassID: "root", Entries: []DescriptorEntry{
		{Key: "aaaa", Type: "long", Value: int32(1)}, {Key: "bbbb", Type: "long", Value: int32(2)},
	}}
	document.limits.MaxDescriptorItems = 1
	if _, err := document.EncodeDescriptor(nil, value, false); !errors.Is(err, ErrLimit) {
		t.Fatalf("source item limit was not inherited: %v", err)
	}
	if _, err := document.EncodeDescriptor(nil, value, false,
		DescriptorEncodeOptions{Limits: Limits{MaxDescriptorItems: 2}},
	); err != nil {
		t.Fatalf("explicit encoding limit did not override source item limit: %v", err)
	}
}

func FuzzEncodeDescriptorRoundTrip(f *testing.F) {
	f.Add(descriptorEncodeFixture(), true)
	f.Add(descriptorFixtureBody("", "root"), false)
	f.Fuzz(func(t *testing.T, data []byte, versioned bool) {
		if len(data) > 32<<10 {
			t.Skip()
		}
		limits := DefaultLimits()
		limits.MaxDescriptorDepth, limits.MaxDescriptorItems = 8, 1000
		limits.MaxMetadataBytes = 1 << 20
		document := descriptorFixtureDocument(data, limits)
		value, err := document.DecodeDescriptor(Span{Length: int64(len(data))}, versioned)
		if err != nil {
			return
		}
		encoded, err := document.EncodeDescriptor(nil, value, versioned)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, data[:value.Span.Length]) {
			t.Fatal("known source descriptor changed during round-trip")
		}
	})
}
