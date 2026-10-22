package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfilter"
)

func filterCacheFixture() []byte {
	var channels bytes.Buffer
	for _, value := range []int32{0, 0, 1, 2} {
		indexTestNumber(&channels, value)
	}
	indexTestNumber(&channels, uint32(8))
	indexTestNumber(&channels, uint32(0))
	indexTestNumber(&channels, uint32(1))
	indexTestNumber(&channels, uint64(4))
	channels.Write([]byte{0, 0, 33, 44})
	indexTestNumber(&channels, uint32(0))
	var record bytes.Buffer
	record.Write([]byte{2, 'i', 'd'})
	indexTestNumber(&record, uint32(1))
	indexTestNumber(&record, uint64(channels.Len()))
	record.Write(channels.Bytes())
	record.WriteByte(1)
	for _, value := range []int32{0, 0, 1, 2} {
		indexTestNumber(&record, value)
	}
	indexTestNumber(&record, uint64(8))
	record.Write([]byte{0, 1, 0, 0, 0, 2, 0xff, 255})
	var payload bytes.Buffer
	indexTestNumber(&payload, uint32(3))
	indexTestNumber(&payload, uint64(record.Len()))
	payload.Write(record.Bytes())
	payload.Write(bytes.Repeat([]byte{0}, (4-record.Len()%4)%4))
	return payload.Bytes()
}

type filterPayloadGuard struct {
	data      []byte
	forbidden []Span
}

func (reader filterPayloadGuard) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if int64(len(data)) != 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			return 0, errors.New("filter sample payload unexpectedly read")
		}
	}
	return bytes.NewReader(reader.data).ReadAt(data, offset)
}

func TestFilterCachesDeferredPSDPSBAndFourByteRLE(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, key := range []string{"FEid", "FXid"} {
			payload := filterCacheFixture()
			tag := indexTestTag(version, "8BIM", key, payload, 4)
			data := indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, nil, tag), []byte{0, 0, 0})
			source := linkedFixtureParse(t, data)
			records, err := source.FilterCaches(nil)
			if err != nil || len(records) != 1 || len(records[0].Arrays) != 2 || records[0].Mask == nil {
				t.Fatalf("version %d %s: %+v, %v", version, key, records, err)
			}
			record := records[0]
			guard := filterPayloadGuard{data: data,
				forbidden: []Span{record.Arrays[0].Data, record.Mask.Data}}
			guarded, err := Read(guard, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := guarded.FilterCaches(nil); err != nil {
				t.Fatalf("deferred indexing read cache pixels: %v", err)
			}
			_ = guarded.Close()
			plane, err := source.DecodeFilterCacheRegion(nil, record.Arrays[0], image.Rect(1, 0, 2, 1))
			if err != nil || !bytes.Equal(plane.Data, []byte{44}) {
				t.Fatalf("raw cached region: %+v, %v", plane, err)
			}
			mask, err := source.DecodeFilterCacheRegion(nil, *record.Mask, image.Rect(0, 0, 2, 1))
			if err != nil || !bytes.Equal(mask.Data, []byte{255, 255}) {
				t.Fatalf("PSD filter-mask four-byte RLE table: %+v, %v", mask, err)
			}
			copy(payload[12+3:12+7], []byte{0, 0, 0, 9})
			badTag := indexTestTag(version, "8BIM", key, payload, 4)
			bad := linkedFixtureParse(t, indexTestDocument(version, 8, 3, nil,
				indexTestGlobal(version, nil, badTag), []byte{0, 0, 0}))
			if _, err := bad.FilterCaches(nil); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unknown cache version: %v", err)
			}
		}
	}
}

func TestSmartFiltersTypedOrderUnknownFieldsAndBudget(t *testing.T) {
	unit := func(key string, value float64) DescriptorEntry {
		return DescriptorEntry{Key: key, Type: "UntF", Value: DescriptorUnit{Unit: "#Pxl", Width: 64, Number: value}}
	}
	first := &Descriptor{Entries: []DescriptorEntry{
		{Key: "filterID", Type: "long", Value: int32(0x47736e42)},
		{Key: "enab", Type: "bool", Value: true},
		{Key: "Fltr", Type: "Objc", Value: &Descriptor{Entries: []DescriptorEntry{unit("Rds ", 2)}}},
		{Key: "unknown", Type: "long", Value: int32(77)},
	}}
	second := &Descriptor{Entries: []DescriptorEntry{
		{Key: "filterID", Type: "long", Value: int32(-1)}, {Key: "enab", Type: "bool", Value: false},
	}}
	stackDescriptor := &Descriptor{Entries: []DescriptorEntry{
		{Key: "enab", Type: "bool", Value: true},
		{Key: "filterFXList", Type: "VlLs", Value: []DescriptorValue{
			{Type: "Objc", Value: first}, {Type: "Objc", Value: second},
		}},
	}}
	placement := SmartObjectRecord{Descriptor: &Descriptor{Entries: []DescriptorEntry{
		{Key: "placed", Type: "TEXT", Value: "placed-id\x00"},
		{Key: "filterFX", Type: "Objc", Value: stackDescriptor},
	}}}
	stack, err := DecodeSmartFilters(nil, placement)
	if err != nil {
		t.Fatal(err)
	}
	if len(stack.Filters) != 2 || stack.PlacementID != "placed-id" ||
		stack.Filters[0].Operation.Kind != psdfilter.GaussianBlur || stack.Filters[0].Operation.Radius != 2 ||
		stack.Filters[1].FilterID != ^uint32(0) || stack.Filters[1].Operation.Kind != "" ||
		stack.Filters[0].Descriptor != first || smartDescriptorValue(first, "unknown") != int32(77) {
		t.Fatalf("typed metadata changed source order or unknown fields: %+v", stack)
	}
	if _, err := DecodeSmartFilters(nil, placement, MetadataOptions{MaxBytes: stack.MetadataBytes}); err != nil {
		t.Fatal(err)
	}
	_, decodeErr := DecodeSmartFilters(nil, placement, MetadataOptions{MaxBytes: stack.MetadataBytes - 1})
	if !errors.Is(decodeErr, ErrLimit) {
		t.Fatalf("typed minus-one budget: %v", decodeErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DecodeSmartFilters(ctx, placement); !errors.Is(err, context.Canceled) {
		t.Fatalf("context: %v", err)
	}
	first.Entries[2].Value.(*Descriptor).Entries[0].Value = DescriptorUnit{Unit: "#Prc", Width: 64, Number: 2.0}
	if _, err := DecodeSmartFilters(nil, placement); !errors.Is(err, ErrMalformed) {
		t.Fatalf("invalid radius unit: %v", err)
	}
}

func TestSmartFilterNativeMetadataAndDeferredMask(t *testing.T) {
	path := filepath.Join("..", "output", "smart-filter-native", "alpha-chain.psd")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("optional native smart-filter fixture is absent")
	}
	source, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	placements, err := source.SmartObjects(nil)
	if err != nil || len(placements) != 1 {
		t.Fatalf("placement: %d, %v", len(placements), err)
	}
	stack, err := DecodeSmartFilters(nil, placements[0])
	if err != nil || stack == nil || len(stack.Filters) != 2 ||
		stack.Filters[0].Operation.Radius != 1 || stack.Filters[1].Operation.Radius != 2 {
		t.Fatalf("native stack: %+v, %v", stack, err)
	}
	view, err := source.ReadFilterCacheView(nil)
	if err != nil || len(view.Records) != 1 {
		t.Fatalf("cache: %+v, %v", view, err)
	}
	record := view.Records[0]
	if record.ID != stack.PlacementID || record.Bounds != image.Rect(0, 0, 32, 24) ||
		len(record.Arrays) != 26 || record.Mask == nil || record.Mask.Depth != 8 {
		t.Fatalf("native cache metadata: %+v", record)
	}
	if _, err := source.ReadFilterCacheView(nil, MetadataOptions{MaxBytes: view.MetadataBytes}); err != nil {
		t.Fatal(err)
	}
	_, decodeErr := source.ReadFilterCacheView(nil, MetadataOptions{MaxBytes: view.MetadataBytes - 1})
	if !errors.Is(decodeErr, ErrLimit) {
		t.Fatalf("cache minus-one budget: %v", decodeErr)
	}
	mask, err := source.DecodeFilterCacheRegion(nil, *record.Mask, image.Rect(0, 0, 32, 24))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range mask.Data {
		if value != 255 {
			t.Fatalf("native default mask contains %d", value)
		}
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if mask.Data[0] != 255 || stack.Filters[1].Operation.Radius != 2 {
		t.Fatal("owned decoded mask or typed controls changed after close")
	}
	if _, err := source.FilterCaches(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed source: %v", err)
	}
	if _, err := source.DecodeFilterCacheRegion(nil, *record.Mask, image.Rect(0, 0, 1, 1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed decode: %v", err)
	}
}

func TestSmartFilterNativeSourceAwareParameterEncoding(t *testing.T) {
	path := filepath.Join("..", "output", "smart-filter-native", "alpha-chain.psd")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("optional native smart-filter fixture is absent")
	}
	source, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	placements, err := source.SmartObjects(nil)
	if err != nil || len(placements) != 1 {
		t.Fatalf("placement: %v", err)
	}
	placement := placements[0]
	view, err := DecodeSmartFilters(nil, placement)
	if err != nil {
		t.Fatal(err)
	}
	desired := psdfilter.Stack{Enabled: view.Enabled}
	for _, item := range view.Filters {
		desired.Filters = append(desired.Filters, item.Operation)
	}
	original, err := source.ReadSpan(placement.Block.Data)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := source.EncodeSmartFilterParameters(nil, placement.Block, desired)
	if err != nil || !bytes.Equal(original, unchanged) {
		t.Fatalf("unchanged smart-filter payload differs: %v", err)
	}
	desired.Filters[0].Radius, desired.Filters[1].Opacity = 3, .6
	changed, err := source.EncodeSmartFilterParameters(nil, placement.Block, desired)
	if err != nil {
		t.Fatal(err)
	}
	bridge := &Document{reader: bytes.NewReader(changed), size: int64(len(changed)), limits: source.Limits()}
	descriptor, err := bridge.DecodeDescriptor(Span{Offset: 8, Length: int64(len(changed)) - 8}, true)
	if err != nil {
		t.Fatal(err)
	}
	patched, err := DecodeSmartFilters(nil, SmartObjectRecord{Descriptor: descriptor})
	if err != nil || patched.Filters[0].Operation.Radius != 3 || patched.Filters[1].Operation.Opacity != .6 {
		t.Fatalf("patched controls: %+v, %v", patched, err)
	}
	if view.Filters[0].Operation.Radius != 1 || view.Filters[1].Operation.Opacity != 1 ||
		!bytes.Equal(original[:8], changed[:8]) || binary.BigEndian.Uint32(changed[4:8]) != 4 {
		t.Fatal("caller metadata or original placement prefix changed")
	}
	for index, entry := range placement.Descriptor.Entries {
		if entry.Key == "filterFX" {
			continue
		}
		other := descriptor.Entries[index]
		before, err := source.ReadSpan(entry.Span)
		if err != nil {
			t.Fatal(err)
		}
		after, err := bridge.ReadSpan(other.Span)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("unrelated descriptor field %s changed: %v", entry.Key, err)
		}
	}
}
