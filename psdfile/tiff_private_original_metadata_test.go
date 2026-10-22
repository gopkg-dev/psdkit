package psdfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestTIFFPrivateResourcesRejectOriginalLittleEndianFakeSignature(t *testing.T) {
	fixture := tiffPrivateTestFixture(0, "", tiffPrivateTestChild())
	data := bytes.Clone(fixture.data)
	field := tiffTestEntryOffset(data, 34377)
	binary.LittleEndian.PutUint32(field[4:8], 382)
	binary.LittleEndian.PutUint32(field[8:], 336)
	if string(data[336:340]) != "MIB8" {
		t.Fatal("fixture no longer points at a little-endian private signature")
	}
	before := bytes.Clone(data)
	parent, record := rasterSource(data)
	defer parent.Close()
	if source, err := parent.OpenLinkedTIFFLayers(nil, record); !errors.Is(err, ErrMalformed) {
		if source != nil {
			source.Close()
		}
		t.Fatalf("private unit reversals fabricated a valid big-endian resource: %v", err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("resource indexing modified original TIFF bytes")
	}
}

type tiffPrivateResourceFault struct {
	reader io.ReaderAt
	span   Span
	cause  error
}

func (reader tiffPrivateResourceFault) ReadAt(data []byte, offset int64) (int, error) {
	count, err := reader.reader.ReadAt(data, offset)
	if count > 0 && err == nil && offset >= reader.span.Offset && offset < reader.span.Offset+reader.span.Length {
		return count, reader.cause
	}
	return count, err
}

func TestTIFFPrivateOriginalResourceIndexRetainsJoinedEOFFault(t *testing.T) {
	fixture := tiffPrivateTestFixture(0, "", tiffPrivateTestChild())
	resource := tiffTestEntryOffset(fixture.data, 34377)
	offset := int64(binary.LittleEndian.Uint32(resource[8:])) + 13
	failure := errors.New("independent original image-resource source fault")
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	parent.reader = tiffPrivateResourceFault{reader: parent.reader, span: Span{Offset: offset, Length: 16},
		cause: errors.Join(io.EOF, failure)}
	if _, err := parent.OpenLinkedTIFFLayers(nil, record); !errors.Is(err, failure) {
		t.Fatalf("original resource indexing discarded its joined EOF source cause: %v", err)
	}
}

func TestTIFFPrivateEmbeddedChildSignatureAlwaysUsesOriginalBytes(t *testing.T) {
	fixture := tiffPrivateTestFixture(0, "lnsr", tiffPrivateTestChild())
	parent, record := rasterSource(fixture.data)
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	layer, err := source.LayerRecord(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var alias Span
	for _, block := range layer.AdditionalInfo {
		if block.Key == "lnsr" {
			alias = block.Data
		}
	}
	source.Close()
	parent.Close()
	data := bytes.Clone(fixture.data)
	copy(data[alias.Offset-13:], "SPB8")
	aliasParent, aliasRecord := rasterSource(data)
	defer aliasParent.Close()
	aliased, err := aliasParent.OpenLinkedTIFFLayers(nil, aliasRecord)
	if err != nil {
		t.Fatal(err)
	}
	defer aliased.Close()
	normalized, err := aliased.ReadNormalizedSpan(nil, alias)
	if err != nil || string(normalized) != "8BPS" {
		t.Fatalf("fixture does not exercise a normalized false PSD signature: %v", err)
	}
	_, err = aliased.OpenLinkedDocument(nil, LinkedFileRecord{Kind: "liFD", HasData: true,
		Data: Span{Offset: alias.Offset, Length: 26}})
	var detail *Error
	if !errors.Is(err, ErrUnsupported) || !errors.As(err, &detail) || detail.Path != "linked.format" {
		t.Fatalf("embedded child header was interpreted through private normalization: %v", err)
	}
}
