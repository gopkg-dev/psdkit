package psdfile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
)

func smartFixtureList(numbers ...float64) []byte {
	data := descriptorFixtureU32(uint32(len(numbers)))
	for _, number := range numbers {
		data = append(data, "doub"...)
		data = append(data, descriptorFixtureU64(math.Float64bits(number))...)
	}
	return data
}

func smartFixtureEntries() []descriptorFixtureEntry {
	size := descriptorFixtureBody("", "size",
		descriptorFixtureEntry{key: "Wdth", kind: "doub", data: descriptorFixtureU64(math.Float64bits(100))},
		descriptorFixtureEntry{key: "Hght", kind: "UntF", data: append(
			[]byte("#Pxl"), descriptorFixtureU64(math.Float64bits(50))...,
		)},
	)
	transform := smartFixtureList(11, 13, 111, 13, 111, 63, 11, 63)
	return []descriptorFixtureEntry{
		{key: "Idnt", kind: "TEXT", data: descriptorFixtureUnicode("fixture-id\x00")},
		{key: "Type", kind: "long", data: descriptorFixtureU32(2)},
		{key: "Sz  ", kind: "Objc", data: size},
		{key: "Trnf", kind: "VlLs", data: transform},
		{key: "nonAffineTransform", kind: "VlLs", data: transform},
		{key: "warp", kind: "Objc", data: descriptorFixtureBody("old", "warp")},
		{key: "quiltWarp", kind: "Objc", data: descriptorFixtureBody("effective", "quiltWarp")},
	}
}

func smartFixturePlacement(version uint32, entries []descriptorFixtureEntry, tail []byte) []byte {
	data := append([]byte("soLD"), descriptorFixtureU32(version)...)
	data = append(data, descriptorFixtureU32(16)...)
	data = append(data, descriptorFixtureBody("", "null", entries...)...)
	return append(data, tail...)
}

func smartFixtureLayerSet(version uint16, layerTags ...[]byte) []byte {
	var layers bytes.Buffer
	indexTestNumber(&layers, -int16(len(layerTags)))
	for _, tags := range layerTags {
		for _, coordinate := range []int32{0, 0, 1, 1} {
			indexTestNumber(&layers, coordinate)
		}
		indexTestNumber(&layers, uint16(1))
		indexTestNumber(&layers, int16(0))
		indexTestLength(&layers, version, 3)
		layers.WriteString("8BIMnorm")
		layers.Write([]byte{255, 0, 0, 0})
		var extra bytes.Buffer
		indexTestNumber(&extra, uint32(0))
		indexTestNumber(&extra, uint32(0))
		extra.Write([]byte{1, 'L', 0, 0})
		extra.Write(tags)
		indexTestNumber(&layers, uint32(extra.Len()))
		layers.Write(extra.Bytes())
	}
	for range layerTags {
		layers.Write([]byte{0, 0, 42})
	}
	if layers.Len()%2 != 0 {
		layers.WriteByte(0)
	}
	return layers.Bytes()
}

func smartFixturePSD(version uint16, layerTags ...[]byte) []byte {
	layers := smartFixtureLayerSet(version, layerTags...)
	return indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, layers, nil), []byte{0, 0, 51})
}

func smartFixtureParse(t *testing.T, version uint16, key string, payload []byte) *Document {
	t.Helper()
	data := smartFixturePSD(version, indexTestTag(version, "8BIM", key, payload, 2))
	document, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	return document
}

func TestSmartObjectsModernBlocksOrderOwnedMetadataAndRawSpans(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(map[uint16]string{1: "PSD", 2: "PSB"}[version], func(t *testing.T) {
			first := smartFixturePlacement(4, smartFixtureEntries(), []byte{0xde, 0xad, 0xbe, 0xef})
			second := smartFixturePlacement(5, smartFixtureEntries(), nil)
			tags := indexTestTag(version, "8BIM", "SoLd", first, 2)
			tags = append(tags, indexTestTag(version, "8BIM", "PlLd", []byte("legacy-kept-raw"), 2)...)
			tags = append(tags, indexTestTag(version, "8BIM", "SoLE", second, 2)...)
			third := indexTestTag(version, "8BIM", "SoLd", second, 2)
			data := smartFixturePSD(version, tags, third)
			before := sha256.Sum256(data)
			reader := &indexForbiddenReader{data: data}
			document, err := Read(reader, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = document.Close() })
			reader.forbidden = append(reader.forbidden, document.Composite.Data)
			for _, layer := range document.Layers {
				for _, channel := range layer.Channels {
					reader.forbidden = append(reader.forbidden, channel.Data)
				}
			}
			records, err := document.SmartObjects(nil)
			if err != nil || len(records) != 3 {
				t.Fatalf("record count=%d: %v", len(records), err)
			}
			for index, record := range records {
				if record.LayerIndex != []int{0, 0, 1}[index] || record.Block.Key != []string{"SoLd", "SoLE", "SoLd"}[index] ||
					record.LayerSetIndex != 0 || record.LayerSetKey != "main" {
					t.Fatal("placement block duplicates or physical layer order were lost")
				}
				if record.SourceID != "fixture-id" || record.SourceType != 2 || record.Width != 100 || record.Height != 50 {
					t.Fatal("source association, type or prototype dimensions differ")
				}
				wantTransform := []float64{11, 13, 111, 13, 111, 63, 11, 63}
				if !reflect.DeepEqual(record.Transform, wantTransform) || !reflect.DeepEqual(record.NonAffine, wantTransform) {
					t.Fatal("corner ordering or coordinates differ")
				}
				if record.Warp == nil || record.Warp.Name != "effective" {
					t.Fatal("quilt warp did not take precedence over legacy warp")
				}
				if record.Prefix.Offset != record.Block.Data.Offset || record.Prefix.Length != 8 ||
					record.DescriptorData.Offset != record.Prefix.Offset+8 ||
					record.Tail.Offset != record.DescriptorData.Offset+record.DescriptorData.Length ||
					record.Tail.Offset+record.Tail.Length != record.Block.Data.Offset+record.Block.Data.Length {
					t.Fatal("placement prefix, descriptor or trailing source spans differ")
				}
			}
			tail, err := document.ReadSpan(records[0].Tail)
			if err != nil || !bytes.Equal(tail, []byte{0xde, 0xad, 0xbe, 0xef}) {
				t.Fatalf("opaque placement tail unavailable: %v", err)
			}
			records[0].Transform[0] = -999
			records[0].Descriptor.Entries[0].Value = "caller-changed"
			records[0].Warp.Name = "caller-changed"
			again, err := document.SmartObjects(nil)
			if err != nil || again[0].Transform[0] != 11 || again[0].SourceID != "fixture-id" ||
				again[0].Warp.Name != "effective" || records[1].Warp.Name != "effective" {
				t.Fatalf("returned metadata aliases the source or another placement: %v", err)
			}
			if sha256.Sum256(data) != before {
				t.Fatal("metadata inspection mutated original source bytes")
			}
		})
	}
}

func TestSmartObjectsRejectMalformedPlacements(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]descriptorFixtureEntry) []descriptorFixtureEntry
		kind error
	}{
		{name: "missing-reference", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			return entries[1:]
		}, kind: ErrMalformed},
		{name: "typed-reference", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[0] = descriptorFixtureEntry{key: "Idnt", kind: "long", data: descriptorFixtureU32(1)}
			return entries
		}, kind: ErrMalformed},
		{name: "missing-type", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			return append(entries[:1], entries[2:]...)
		}, kind: ErrMalformed},
		{name: "typed-type", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[1] = descriptorFixtureEntry{key: "Type", kind: "TEXT", data: descriptorFixtureUnicode("invalid")}
			return entries
		}, kind: ErrMalformed},
		{name: "unknown-type", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[1].data = descriptorFixtureU32(4)
			return entries
		}, kind: ErrUnsupported},
		{name: "fractional-type", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[1] = descriptorFixtureEntry{key: "Type", kind: "doub", data: descriptorFixtureU64(math.Float64bits(2.5))}
			return entries
		}, kind: ErrUnsupported},
		{name: "invalid-size", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[2].data = descriptorFixtureBody("", "size",
				descriptorFixtureEntry{key: "Wdth", kind: "doub", data: descriptorFixtureU64(math.Float64bits(math.NaN()))},
				descriptorFixtureEntry{key: "Hght", kind: "long", data: descriptorFixtureU32(50)},
			)
			return entries
		}, kind: ErrMalformed},
		{name: "zero-size", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[2].data = descriptorFixtureBody("", "size",
				descriptorFixtureEntry{key: "Wdth", kind: "long", data: descriptorFixtureU32(0)},
				descriptorFixtureEntry{key: "Hght", kind: "long", data: descriptorFixtureU32(50)},
			)
			return entries
		}, kind: ErrMalformed},
		{name: "missing-size", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			return append(entries[:2], entries[3:]...)
		}, kind: ErrMalformed},
		{name: "short-transform", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[3].data = smartFixtureList(1, 2, 3, 4, 5, 6, 7)
			return entries
		}, kind: ErrMalformed},
		{name: "missing-transform", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			return append(entries[:3], entries[4:]...)
		}, kind: ErrMalformed},
		{name: "typed-transform", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[3] = descriptorFixtureEntry{key: "Trnf", kind: "TEXT", data: descriptorFixtureUnicode("invalid")}
			return entries
		}, kind: ErrMalformed},
		{name: "infinite-transform", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[3].data = smartFixtureList(1, 2, 3, 4, 5, 6, 7, math.Inf(1))
			return entries
		}, kind: ErrMalformed},
		{name: "typed-warp", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[5] = descriptorFixtureEntry{key: "warp", kind: "TEXT", data: descriptorFixtureUnicode("invalid")}
			return entries
		}, kind: ErrMalformed},
		{name: "typed-quilt-warp", edit: func(entries []descriptorFixtureEntry) []descriptorFixtureEntry {
			entries[6] = descriptorFixtureEntry{key: "quiltWarp", kind: "TEXT", data: descriptorFixtureUnicode("invalid")}
			return entries
		}, kind: ErrMalformed},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := smartFixturePlacement(4, test.edit(smartFixtureEntries()), nil)
			document := smartFixtureParse(t, 1, "SoLd", data)
			records, err := document.SmartObjects(nil)
			if !errors.Is(err, test.kind) || records != nil {
				t.Fatalf("malformed placement returned %d records and %v", len(records), err)
			}
		})
	}
	for _, payload := range [][]byte{
		[]byte("incomplete"),
		append([]byte("bad!"), descriptorFixtureU32(4)...),
		append([]byte("soLD"), descriptorFixtureU32(99)...),
	} {
		document := smartFixtureParse(t, 1, "SoLE", payload)
		if records, err := document.SmartObjects(nil); err == nil || records != nil {
			t.Fatal("incomplete or unsupported schema returned placements")
		}
	}
}

func TestSmartObjectsContextClosureAndWholeOperationBudgets(t *testing.T) {
	data := smartFixturePlacement(4, smartFixtureEntries(), nil)
	document := smartFixtureParse(t, 1, "SoLd", data)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if records, err := document.SmartObjects(ctx); !errors.Is(err, context.Canceled) || records != nil {
		t.Fatalf("pre-cancellation returned %d records and %v", len(records), err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader := &descriptorEncodeReader{reader: document.reader, cancel: cancel}
	document.reader = reader
	if records, err := document.SmartObjects(ctx); !errors.Is(err, context.Canceled) || records != nil ||
		len(reader.reads) != 1 {
		t.Fatalf("prefix cancellation returned %d records, %d reads and %v", len(records), len(reader.reads), err)
	}
	if err := document.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := document.SmartObjects(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed placement source accepted: %v", err)
	}
	for _, limit := range []string{"metadata", "blocks"} {
		fresh := smartFixtureParse(t, 1, "SoLd", data)
		if limit == "metadata" {
			fresh.limits.MaxMetadataBytes = int64(len(data)) - 1
		} else {
			fresh.Layers[0].AdditionalInfo = append(fresh.Layers[0].AdditionalInfo, fresh.Layers[0].AdditionalInfo[0])
			fresh.limits.MaxBlocks = 1
		}
		if records, err := fresh.SmartObjects(nil); !errors.Is(err, ErrLimit) || records != nil {
			t.Fatalf("whole-operation %s budget returned %d records and %v", limit, len(records), err)
		}
	}
}

func TestSmartObjectsIndexesAlternateLayerSetsInPhysicalOrder(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		placement := smartFixturePlacement(4, smartFixtureEntries(), nil)
		main := smartFixtureLayerSet(version, indexTestTag(version, "8BIM", "SoLd", placement, 2))
		alternate := smartFixtureLayerSet(version, indexTestTag(version, "8BIM", "SoLE", placement, 2))
		// Reuse the independent record geometry with a valid 16-bit RAW channel.
		if version == 1 {
			binary.BigEndian.PutUint32(alternate[22:26], 4)
		} else {
			binary.BigEndian.PutUint64(alternate[22:30], 4)
		}
		copy(alternate[len(alternate)-4:], []byte{0, 0, 0, 42})
		tag := indexTestTag(version, "8BIM", "Lr16", alternate, 4)
		data := indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, main, tag), []byte{0, 0, 51})
		document, err := Parse(data)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = document.Close() })
		if len(document.LayerSets) != 2 || document.ActiveLayerSet != 0 {
			t.Fatal("independent main/alternate layer-set fixture differs")
		}
		records, err := document.SmartObjects(nil)
		if err != nil || len(records) != 2 {
			t.Fatalf("alternate-placement count=%d: %v", len(records), err)
		}
		for index, record := range records {
			if record.LayerSetIndex != index || record.LayerIndex != 0 ||
				record.LayerSetKey != []string{"main", "Lr16"}[index] {
				t.Fatal("alternate placements lost their source layer-set identity")
			}
		}
		if records[0].Block.Span.Offset >= records[1].Block.Span.Offset {
			t.Fatal("main/alternate placements do not follow physical source order")
		}
		// Layer-set traversal order cannot substitute for the advertised block order.
		document.LayerSets[0], document.LayerSets[1] = document.LayerSets[1], document.LayerSets[0]
		records, err = document.SmartObjects(nil)
		if err != nil || records[0].LayerSetKey != "main" || records[0].LayerSetIndex != 1 ||
			records[1].LayerSetKey != "Lr16" || records[1].LayerSetIndex != 0 {
			t.Fatalf("placement source order changed with layer-set traversal: %v", err)
		}
	}
}

func TestSmartObjectsDescriptorBudgetsAccumulateAcrossRecords(t *testing.T) {
	placement := smartFixturePlacement(4, smartFixtureEntries(), nil)
	one := smartFixtureParse(t, 1, "SoLd", placement)
	// Wire counts are 7 root entries, 2 size entries and two eight-number lists.
	one.limits.MaxDescriptorItems = 25
	if _, err := one.SmartObjects(nil); err != nil {
		t.Fatalf("one placement exceeds its independent exact item budget: %v", err)
	}
	data := smartFixturePSD(1,
		append(indexTestTag(1, "8BIM", "SoLd", placement, 2), indexTestTag(1, "8BIM", "SoLE", placement, 2)...),
	)
	two, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = two.Close() })
	two.limits.MaxDescriptorItems = 25
	if records, err := two.SmartObjects(nil); !errors.Is(err, ErrLimit) || records != nil {
		t.Fatalf("item budget reset between placements: count=%d, %v", len(records), err)
	}
	two.limits.MaxDescriptorItems = 50
	if records, err := two.SmartObjects(nil); err != nil || len(records) != 2 {
		t.Fatalf("two placements exceed their exact aggregate item budget: count=%d, %v", len(records), err)
	}
	// Locate one placement's minimum valid budget through the public operation.
	// Two retained descriptors must exceed one and a half times that allocation.
	low, high := int64(1), DefaultLimits().MaxMetadataBytes
	for low < high {
		midpoint := low + (high-low)/2
		one.limits.MaxMetadataBytes = midpoint
		_, err := one.SmartObjects(nil)
		if errors.Is(err, ErrLimit) {
			low = midpoint + 1
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		high = midpoint
	}
	one.limits.MaxMetadataBytes = low
	if _, err := one.SmartObjects(nil); err != nil {
		t.Fatal(err)
	}
	two.limits.MaxMetadataBytes = low + low/2
	if records, err := two.SmartObjects(nil); !errors.Is(err, ErrLimit) || records != nil {
		t.Fatalf("metadata budget reset between retained descriptors: count=%d, %v", len(records), err)
	}
}

func TestSmartObjectsLeavesOpaquePlacementTailDeferred(t *testing.T) {
	entries := smartFixtureEntries()
	tail := bytes.Repeat([]byte{0xa5}, 1<<20)
	payload := smartFixturePlacement(4, entries, tail)
	data := smartFixturePSD(1, indexTestTag(1, "8BIM", "SoLd", payload, 2))
	reader := &indexForbiddenReader{data: data}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	block := document.Layers[0].AdditionalInfo[0]
	reader.forbidden = []Span{{Offset: block.Data.Offset + int64(len(payload)-len(tail)), Length: int64(len(tail))}}
	document.limits.MaxMetadataBytes = 16 << 10
	records, err := document.SmartObjects(nil)
	if err != nil || len(records) != 1 || records[0].Tail.Length != int64(len(tail)) {
		t.Fatalf("opaque tail consumed metadata budget or source bytes: count=%d, %v", len(records), err)
	}
}

func smartFixtureNestedPSD(version uint16) []byte {
	data := indexTestDocument(version, 8, 3, nil, nil, append([]byte{0, 0}, bytes.Repeat([]byte{77}, 3*256*256)...))
	binary.BigEndian.PutUint16(data[12:14], 3)
	binary.BigEndian.PutUint32(data[14:18], 256)
	binary.BigEndian.PutUint32(data[18:22], 256)
	return data
}

func TestOpenLinkedDocumentBorrowsPSDAndPSBWithoutCopyingPixels(t *testing.T) {
	for _, childVersion := range []uint16{1, 2} {
		for _, parentVersion := range []uint16{1, 2} {
			t.Run(string(rune('0'+parentVersion))+"-"+string(rune('0'+childVersion)), func(t *testing.T) {
				nested := smartFixtureNestedPSD(childVersion)
				fixture := linkedFixtureEntry("liFD", 7, nil, nested, nil)
				data := linkedFixtureDocument(parentVersion, "lnk2", fixture)
				reader := &indexForbiddenReader{data: data}
				parent, err := Read(reader, int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = parent.Close() })
				records, err := parent.LinkedFiles(nil)
				if err != nil || len(records) != 1 {
					t.Fatalf("nested source indexing: %v", err)
				}
				expected, err := Parse(nested)
				if err != nil {
					t.Fatal(err)
				}
				deferred := expected.Composite.Data
				deferred.Offset += records[0].Data.Offset
				reader.forbidden, reader.reads = []Span{deferred}, nil
				ctx, cancel := context.WithCancel(context.Background())
				child, err := parent.OpenLinkedDocument(ctx, records[0])
				if err != nil {
					t.Fatalf("nested structural index read deferred pixels: %v", err)
				}
				cancel()
				t.Cleanup(func() { _ = child.Close() })
				if child.Header.Version != childVersion || child.Header.Width != 256 || child.Header.Height != 256 ||
					child.Source.Length != records[0].Data.Length || child.closer != nil {
					t.Fatal("nested header, content span or borrowed ownership differs")
				}
				var readBytes int64
				for _, read := range reader.reads {
					if read.Offset < records[0].Data.Offset || read.Offset+read.Length >
						records[0].Data.Offset+records[0].Data.Length {
						t.Fatal("nested indexing read outside its linked-content slice")
					}
					readBytes += read.Length
				}
				if readBytes > 128 {
					t.Fatal("nested indexing copied encoded pixel contents")
				}
				reader.forbidden, reader.reads = nil, nil
				plane, err := child.DecodeCompositeChannel(nil, 0)
				if err != nil || len(plane.Data) != 256*256 || !bytes.Equal(plane.Data, bytes.Repeat([]byte{77}, 256*256)) {
					t.Fatalf("lazy nested decode failed after index context expired: %v", err)
				}
				if len(reader.reads) == 0 {
					t.Fatal("lazy child decode did not read through its parent source")
				}
				if err := child.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := parent.ReadSpan(Span{Length: 4}); err != nil {
					t.Fatalf("child Close closed its parent source: %v", err)
				}
				child, err = parent.OpenLinkedDocument(nil, records[0])
				if err != nil {
					t.Fatal(err)
				}
				if err := parent.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := child.DecodeCompositeChannel(nil, 0); !errors.Is(err, ErrClosed) {
					t.Fatalf("child read survived parent closure: %v", err)
				}
				if err := child.Close(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestOpenLinkedDocumentRejectsIncompleteContentAndHonorsContext(t *testing.T) {
	data := linkedFixtureDocument(1, "lnkD", linkedFixtureEntry("liFD", 1, nil, smartFixtureNestedPSD(1), nil))
	parent := linkedFixtureParse(t, data)
	records, err := parent.LinkedFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []LinkedFileRecord{
		{HasData: false, Data: records[0].Data},
		{HasData: true, Data: Span{Length: 25}},
		{HasData: true, Data: Span{Offset: int64(len(data)), Length: 26}},
	} {
		if child, err := parent.OpenLinkedDocument(nil, record); !errors.Is(err, ErrUnsupported) || child != nil {
			t.Fatalf("incomplete linked content returned a child and %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if child, err := parent.OpenLinkedDocument(ctx, records[0]); !errors.Is(err, context.Canceled) || child != nil {
		t.Fatalf("canceled linked indexing returned a child and %v", err)
	}
	limitedChild, limitedErr := parent.OpenLinkedDocument(
		nil, records[0], LoadOptions{Limits: Limits{MaxFileBytes: 26}},
	)
	if !errors.Is(limitedErr, ErrLimit) || limitedChild != nil {
		t.Fatalf("nested file-byte budget returned a child and %v", limitedErr)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if child, err := parent.OpenLinkedDocument(nil, records[0]); !errors.Is(err, ErrClosed) || child != nil {
		t.Fatalf("closed parent returned a child and %v", err)
	}
}

func TestOpenLinkedDocumentIndexesCachedExternalContentWithoutOpeningAPath(t *testing.T) {
	fixture := linkedFixtureEntry("liFE", 7, nil, smartFixtureNestedPSD(2), nil)
	data := linkedFixtureDocument(1, "lnkE", fixture)
	parent := linkedFixtureParse(t, data)
	records, err := parent.LinkedFiles(nil)
	if err != nil || len(records) != 1 || records[0].ExternalDescriptor.Length == 0 || !records[0].HasData {
		t.Fatalf("cached external record differs: count=%d, %v", len(records), err)
	}
	child, err := parent.OpenLinkedDocument(nil, records[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Close() })
	if child.Header.Version != 2 || child.Header.Width != 256 {
		t.Fatal("cached external PSD/PSB was not indexed through its retained data")
	}
	if _, err := child.DecodeCompositeChannel(nil, 2); err != nil {
		t.Fatalf("cached external content attempted a path lookup or lost its bytes: %v", err)
	}
}

func smartFixtureVersionInfo(hasMerged bool) []byte {
	payload := descriptorFixtureU32(1)
	flag := byte(0)
	if hasMerged {
		flag = 1
	}
	payload = append(payload, flag)
	payload = append(payload, descriptorFixtureUnicode("fixture writer\x00")...)
	payload = append(payload, descriptorFixtureUnicode("fixture reader\x00")...)
	payload = append(payload, descriptorFixtureU32(1)...)
	var resource bytes.Buffer
	resource.WriteString("8BIM")
	indexTestNumber(&resource, uint16(1057))
	resource.Write([]byte{0, 0})
	indexTestNumber(&resource, uint32(len(payload)))
	resource.Write(payload)
	if len(payload)%2 != 0 {
		resource.WriteByte(0)
	}
	return resource.Bytes()
}

func TestOpenLinkedDocumentWithAbsentRealMergedPreview(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		var saved bytes.Buffer
		indexTestNumber(&saved, uint16(1))
		for range 24 {
			if version == 2 {
				indexTestNumber(&saved, uint32(2))
			} else {
				indexTestNumber(&saved, uint16(2))
			}
		}
		for range 24 {
			saved.Write([]byte{0xf9, 0})
		}
		// Native newPlacedLayer PSB retains reserved bytes after its placeholder RLE.
		saved.Write(bytes.Repeat([]byte{0}, 64<<10))
		nested := indexTestDocument(version, 8, 3, smartFixtureVersionInfo(false),
			indexTestGlobal(version, smartFixtureLayerSet(version, nil), nil), saved.Bytes(),
		)
		binary.BigEndian.PutUint16(nested[12:14], 3)
		binary.BigEndian.PutUint32(nested[14:18], 8)
		binary.BigEndian.PutUint32(nested[18:22], 8)
		outer := linkedFixtureDocument(1, "lnk2", linkedFixtureEntry("liFD", 1, nil, nested, nil))
		parent := linkedFixtureParse(t, outer)
		records, err := parent.LinkedFiles(nil)
		if err != nil {
			t.Fatal(err)
		}
		child, err := parent.OpenLinkedDocument(nil, records[0])
		if err != nil {
			t.Fatalf("version=%d: editable nested source with no real merged preview was rejected: %v", version, err)
		}
		t.Cleanup(func() { _ = child.Close() })
		if child.Source.Length != int64(len(nested)) || len(child.Layers) != 1 {
			t.Fatal("placeholder-preview indexing discarded source bytes or editable layers")
		}
		if child.Composite.ReservedTail.Length != 64<<10 ||
			child.Composite.ReservedTail.Offset+child.Composite.ReservedTail.Length != child.Source.Length ||
			child.Composite.Data.Offset+child.Composite.Data.Length != child.Composite.ReservedTail.Offset {
			t.Fatal("placeholder merged-preview tail is not independently indexed from its RLE data")
		}
		pixels, err := child.DecodeLayer(nil, 0)
		if err != nil || len(pixels.Channels) != 1 || !bytes.Equal(pixels.Channels[0].Data, []byte{42}) {
			t.Fatalf("editable nested layer with no real merged preview cannot be decoded: %v", err)
		}
		got := sha256.New()
		if _, err := child.CopySpan(nil, got, child.Source); err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256(nested)
		if !bytes.Equal(got.Sum(nil), want[:]) {
			t.Fatal("placeholder merged-preview bytes were normalized or discarded")
		}
		truePreview := bytes.Clone(nested)
		for _, resource := range child.Resources {
			if resource.ID == 1057 {
				truePreview[resource.Data.Offset+4] = 1
			}
		}
		indexed, indexErr := Parse(truePreview)
		if !errors.Is(indexErr, ErrMalformed) || indexed != nil {
			t.Fatalf("standalone real merged preview lost strict RLE tail validation: %v", indexErr)
		}
	}
}

func TestSmartObjectsRealSampleSharedPrototypeAndDeferredContents(t *testing.T) {
	data, err := os.ReadFile(testfixture.Sample(t))
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("optional stable real PSD fixture is absent")
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
	linked, err := document.LinkedFiles(nil)
	if err != nil || len(linked) != 1 {
		t.Fatalf("sample linked content count=%d: %v", len(linked), err)
	}
	reader.forbidden = []Span{linked[0].Data, document.Composite.Data}
	for _, layer := range document.Layers {
		for _, channel := range layer.Channels {
			reader.forbidden = append(reader.forbidden, channel.Data)
		}
	}
	records, err := document.SmartObjects(nil)
	if err != nil || len(records) != 14 {
		t.Fatalf("sample smart-instance count=%d: %v", len(records), err)
	}
	for _, record := range records {
		if record.SourceID != linked[0].ID || record.Width != 1000 || record.Height != 2000 ||
			record.SourceType != 2 || len(record.Transform) != 8 || len(record.NonAffine) != 8 || record.Warp == nil {
			t.Fatal("shared source identity, independently verified dimensions, type or transform differs")
		}
	}
	// This placement transform is independently decoded by psd-tools from the stable sample.
	want := []float64{
		235.9050503641161, 139.27255985077696, 608, 139.27255985077696,
		608, 669.3521864453808, 235.9050503641161, 669.3521864453808,
	}
	found := false
	for recordIndex, record := range records {
		matches := true
		for index, number := range record.Transform {
			matches = matches && math.Abs(number-want[index]) <= 1e-9
		}
		found = found || matches
		if recordIndex != 0 && record.Block.Span.Offset < records[recordIndex-1].Block.Span.Offset {
			t.Fatal("sample placement records are not in physical block order")
		}
	}
	if !found {
		t.Fatal("sample transform differs from the independent decoder")
	}
	if sha256.Sum256(data) != before {
		t.Fatal("smart-object indexing mutated the real sample")
	}
}

func TestSmartObjectsOptionalNativeFixtures(t *testing.T) {
	directory := os.Getenv("PSDKIT_SMART_FIXTURE_DIR")
	if directory == "" {
		t.Skip("set PSDKIT_SMART_FIXTURE_DIR to verify locally generated Photoshop fixtures")
	}
	for _, filename := range []string{"smart-shared-original.psd", "smart-shared-native-replaced.psd"} {
		t.Run(filename, func(t *testing.T) {
			document, err := Open(filepath.Join(directory, filename))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = document.Close() })
			records, err := document.SmartObjects(nil)
			if err != nil || len(records) != 2 {
				t.Fatalf("native shared-instance count=%d: %v", len(records), err)
			}
			linked, err := document.LinkedFiles(nil)
			if err != nil || len(linked) != 1 {
				t.Fatalf("native linked-source count=%d: %v", len(linked), err)
			}
			corners := make(map[float64]bool)
			for _, record := range records {
				if record.SourceID != linked[0].ID || record.Width != 8 || record.Height != 8 || record.SourceType != 2 ||
					len(record.Transform) != 8 {
					t.Fatal("native source identity, independently verified prototype or type differs")
				}
				corners[record.Transform[0]] = true
				left := record.Transform[0]
				if !reflect.DeepEqual(record.Transform, []float64{left, 4, left + 8, 4, left + 8, 12, left, 12}) {
					t.Fatal("native transformed corners differ from the independent decoder")
				}
			}
			if !corners[4] || !corners[20] {
				t.Fatal("native shared placements lost their distinct offsets")
			}
			child, err := document.OpenLinkedDocument(nil, linked[0])
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = child.Close() })
			if child.Header.Width != 8 || child.Header.Height != 8 {
				t.Fatal("native embedded document dimensions differ from the placement prototype")
			}
			if filename == "smart-shared-original.psd" {
				// Photoshop's newPlacedLayer PSB has no real saved composite and retains a reserved tail.
				if len(child.Layers) == 0 {
					t.Fatal("native embedded source has no editable layer records")
				}
				if _, err := child.DecodeLayer(nil, 0); err != nil {
					t.Fatalf("native embedded layer cannot be decoded: %v", err)
				}
			} else if _, err := child.DecodeComposite(nil); err != nil {
				t.Fatalf("native replacement's saved composite cannot be decoded: %v", err)
			}
		})
	}
	document, err := Open(filepath.Join(directory, "smart-replacement.psd"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	if document.Header.Width != 8 || document.Header.Height != 8 {
		t.Fatal("native replacement dimensions differ")
	}
	if records, err := document.SmartObjects(nil); err != nil || len(records) != 0 {
		t.Fatalf("flat replacement has unexpected nested placements: count=%d, %v", len(records), err)
	}
}
