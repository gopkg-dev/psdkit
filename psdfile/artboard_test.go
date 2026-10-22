package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
)

func artboardFixturePayload() []byte {
	rectangle := descriptorFixtureBody("", "classFloatRect",
		descriptorFixtureEntry{key: "Left", kind: "doub", data: descriptorFixtureU64(math.Float64bits(-6.25))},
		descriptorFixtureEntry{key: "Top ", kind: "UntF", data: effectsFixtureUnit("#Pxl", 1.5)},
		descriptorFixtureEntry{key: "Rght", kind: "long", data: descriptorFixtureU32(32)},
		descriptorFixtureEntry{key: "Btom", kind: "doub", data: descriptorFixtureU64(math.Float64bits(16.75))})
	rgb := descriptorFixtureBody("", "RGBC",
		descriptorFixtureEntry{key: "Rd  ", kind: "doub", data: descriptorFixtureU64(math.Float64bits(18))},
		descriptorFixtureEntry{key: "Grn ", kind: "doub", data: descriptorFixtureU64(math.Float64bits(64))},
		descriptorFixtureEntry{key: "Bl  ", kind: "doub", data: descriptorFixtureU64(math.Float64bits(224))})
	ids := descriptorFixtureU32(2)
	ids = append(ids, "long"...)
	ids = append(ids, descriptorFixtureU32(17)...)
	ids = append(ids, "long"...)
	ids = append(ids, descriptorFixtureU32(0xfffffffe)...)
	body := descriptorFixtureBody("", "artboard",
		descriptorFixtureEntry{key: "artboardRect", kind: "Objc", data: rectangle},
		descriptorFixtureEntry{key: "artboardBackgroundType", kind: "long", data: descriptorFixtureU32(4)},
		descriptorFixtureEntry{key: "Clr ", kind: "Objc", data: rgb},
		descriptorFixtureEntry{key: "artboardPresetName", kind: "TEXT", data: descriptorFixtureUnicode("Board🌟")},
		descriptorFixtureEntry{key: "guideIDs", kind: "VlLs", data: ids},
		descriptorFixtureEntry{key: "futureRaw", kind: "tdta", data: append(descriptorFixtureU32(3), 7, 9, 11)},
		descriptorFixtureEntry{key: "futureRaw", kind: "long", data: descriptorFixtureU32(123)})
	payload := append(descriptorFixtureU32(16), body...)
	return append(payload, make([]byte, (4-len(payload)%4)%4)...)
}

func TestDecodeArtboardOwnsFractionalNegativeRectAndOrderedOriginalSpans(t *testing.T) {
	for _, key := range []string{"artb", "artd", "abdd"} {
		payload := artboardFixturePayload()
		data := append(make([]byte, 37), payload...)
		original := bytes.Clone(data)
		document := descriptorFixtureDocument(data, DefaultLimits())
		block := AdditionalBlock{Key: key, Data: Span{Offset: 37, Length: int64(len(payload))}}
		view, err := document.DecodeArtboard(nil, block)
		if err != nil {
			t.Fatal(err)
		}
		if view.Rect == nil || *view.Rect != (ArtboardRect{Left: -6.25, Top: 1.5, Right: 32, Bottom: 16.75}) ||
			view.BackgroundType == nil || *view.BackgroundType != 4 || view.PresetName == nil ||
			*view.PresetName != "Board🌟" || !reflect.DeepEqual(view.GuideIDs, []int32{17, -2}) ||
			view.BackgroundRGB == nil || *view.BackgroundRGB != [3]float64{18.0 / 255, 64.0 / 255, 224.0 / 255} {
			t.Fatal("fractional geometry, negative origin, raw background type, preset or guide identifiers were lost")
		}
		if len(view.OrderedEntries) != 7 || len(view.UnknownEntries) != 2 ||
			view.UnknownEntries[0].Key != "futureRaw" || view.UnknownEntries[1].Key != "futureRaw" {
			t.Fatal("unknown duplicate entries or original descriptor ordering were lost")
		}
		blob := view.UnknownEntries[0].Value.(DescriptorBlob)
		bytesBefore, err := document.ReadSpan(blob.Data)
		if err != nil || !bytes.Equal(bytesBefore, []byte{7, 9, 11}) || blob.Data.Offset < 37 {
			t.Fatalf("unknown blob span no longer addresses the original source: %v", err)
		}
		if !bytes.Equal(data, original) {
			t.Fatal("reading artboard metadata changed source bytes")
		}
		if err := document.Close(); err != nil {
			t.Fatal(err)
		}
		view.Rect.Left = -8
		if view.Descriptor.Entries[0].Value.(*Descriptor).Entries[0].Value != float64(-6.25) {
			t.Fatal("typed geometry fields alias the ordered descriptor's scalar storage")
		}
		if _, err := document.DecodeArtboard(nil, block); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed-source artboard decode: %v", err)
		}
	}
}

func TestArtboardsRetainsPhysicalAndBlockOrderWithAggregateBudget(t *testing.T) {
	payload := artboardFixturePayload()
	data := append(append(append([]byte(nil), payload...), payload...), payload...)
	document := descriptorFixtureDocument(data, DefaultLimits())
	length := int64(len(payload))
	document.Layers = []LayerRecord{{ID: 19, Name: "Board", AdditionalInfo: []AdditionalBlock{
		{Key: "abdd", Data: Span{Length: length}}, {Key: "artb", Data: Span{Offset: length, Length: length}},
	}}}
	document.AdditionalInfo = []AdditionalBlock{{Key: "artd", Data: Span{Offset: length * 2, Length: length}}}
	records, err := document.Artboards(nil)
	if err != nil || len(records) != 3 || records[0].Data.Block.Key != "abdd" || records[1].Data.Block.Key != "artb" ||
		records[0].LayerIndex != 0 || records[0].LayerID != 19 || records[2].LayerIndex != -1 {
		t.Fatalf("physical source order or document-level association was lost: %+v, %v", records, err)
	}
	budget := records[0].Data.MetadataBytes + 256
	if _, err := document.Artboards(nil, MetadataOptions{MaxBytes: budget + 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("multiple artboard views escaped their aggregate reservation: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := document.Artboards(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled artboard list decode: %v", err)
	}
}

func TestDecodeArtboardRejectsPartialNonfiniteAndDuplicateRectangleCoordinates(t *testing.T) {
	for _, entries := range [][]descriptorFixtureEntry{
		{{key: "Left", kind: "long", data: descriptorFixtureU32(0)}},
		{
			{key: "Left", kind: "doub", data: descriptorFixtureU64(math.Float64bits(math.NaN()))},
			{key: "Top ", kind: "long", data: descriptorFixtureU32(0)},
			{key: "Rght", kind: "long", data: descriptorFixtureU32(5)},
			{key: "Btom", kind: "long", data: descriptorFixtureU32(5)},
		},
		{
			{key: "Left", kind: "long", data: descriptorFixtureU32(0)},
			{key: "Left", kind: "long", data: descriptorFixtureU32(1)},
			{key: "Top ", kind: "long", data: descriptorFixtureU32(0)},
			{key: "Rght", kind: "long", data: descriptorFixtureU32(5)},
			{key: "Btom", kind: "long", data: descriptorFixtureU32(5)},
		},
	} {
		rectangle := descriptorFixtureBody("", "classFloatRect", entries...)
		body := descriptorFixtureBody("", "artboard",
			descriptorFixtureEntry{key: "artboardRect", kind: "Objc", data: rectangle})
		payload := append(descriptorFixtureU32(16), body...)
		document := descriptorFixtureDocument(payload, DefaultLimits())
		view, err := document.DecodeArtboard(nil, AdditionalBlock{Key: "artb", Data: Span{Length: int64(len(payload))}})
		if !errors.Is(err, ErrMalformed) || view != nil {
			t.Fatalf("malformed artboard rectangle was silently truncated or normalized: %v", err)
		}
	}
}

func TestDecodeArtboardLimitsVersionAndUnknownTailRemainExplicit(t *testing.T) {
	payload := artboardFixturePayload()
	block := AdditionalBlock{Key: "artb", Data: Span{Length: int64(len(payload))}}
	document := descriptorFixtureDocument(payload, DefaultLimits())
	if _, err := document.DecodeArtboard(nil, block, MetadataOptions{MaxBytes: 500}); !errors.Is(err, ErrLimit) {
		t.Fatalf("owned descriptor/view ignored reduced metadata budget: %v", err)
	}
	versioned := bytes.Clone(payload)
	binary.BigEndian.PutUint32(versioned, 17)
	_, err := descriptorFixtureDocument(versioned, DefaultLimits()).DecodeArtboard(nil, block)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown descriptor version was accepted: %v", err)
	}
	withTail := append(bytes.Clone(payload), 29)
	block.Data.Length = int64(len(withTail))
	view, err := descriptorFixtureDocument(withTail, DefaultLimits()).DecodeArtboard(nil, block)
	if err != nil || view.Tail.Length == 0 || len(view.Unsupported) == 0 {
		t.Fatalf("unknown trailing bytes were not retained as explicit metadata: %v", err)
	}
}
