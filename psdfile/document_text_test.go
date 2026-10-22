package psdfile

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdtext"
)

func documentTextFixtureBytes() []byte {
	return []byte(" /98 << /0 7 >> /0 << /unknown true >> /1 << /1 [] >> \x00")
}

func TestDocumentTextViewIsLazyOrderedAndBounded(t *testing.T) {
	payload := documentTextFixtureBytes()
	for _, version := range []uint16{1, 2} {
		tags := indexTestTag(version, "8BIM", "Txt2", payload, 4)
		tags = append(tags, indexTestTag(version, "8BIM", "Txt2", payload, 4)...)
		data := indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, nil, tags), []byte{0, 0, 0})
		reader := &indexForbiddenReader{data: data}
		document, err := Read(reader, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		defer document.Close()
		for _, block := range document.AdditionalInfo {
			reader.forbidden = append(reader.forbidden, block.Data)
		}
		reader.forbidden = append(reader.forbidden, document.Composite.Data)
		view, err := document.ReadDocumentTextView(nil)
		if err != nil || len(view.Records) != 2 {
			t.Fatalf("lazy view: %+v %v", view, err)
		}
		if view.Records[0].Block.Span.Offset >= view.Records[1].Block.Span.Offset {
			t.Fatal("source order lost")
		}
		if view.Records[0].EngineData != view.Records[0].Block.Data {
			t.Fatal("invented inner length prefix")
		}
		if _, err := document.ReadDocumentTextView(nil, MetadataOptions{MaxBytes: view.MetadataBytes}); err != nil {
			t.Fatalf("exact view budget: %v", err)
		}
		if _, err := document.ReadDocumentTextView(nil, MetadataOptions{MaxBytes: view.MetadataBytes - 1}); !errors.Is(err, ErrLimit) {
			t.Fatalf("one below budget: %v", err)
		}
		reader.forbidden = []Span{document.Composite.Data}
		engine, err := document.DecodeDocumentTextEngine(nil, view.Records[0])
		if err != nil {
			t.Fatal(err)
		}
		if engine.Root.Lookup("98").Span.Offset <= view.Records[0].EngineData.Offset {
			t.Fatal("original child span was not retained")
		}
		encoded, err := psdtext.Encode(nil, engine)
		if err != nil || !bytes.Equal(encoded, payload) {
			t.Fatalf("unchanged encode: %v", err)
		}
		if _, err := document.DecodeDocumentTextEngine(nil, view.Records[0], psdtext.DecodeOptions{MaxBytes: 1}); !errors.Is(err, ErrLimit) {
			t.Fatalf("decode budget: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := document.ReadDocumentTextView(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel view: %v", err)
		}
		if _, err := document.DecodeDocumentTextEngine(ctx, view.Records[0]); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel decode: %v", err)
		}
		if err := document.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := document.ReadDocumentTextView(nil); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed view: %v", err)
		}
		if _, err := document.DecodeDocumentTextEngine(nil, view.Records[0]); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed decode: %v", err)
		}
	}
}

func documentTextLayerPayload(t *testing.T, index int32) []byte {
	t.Helper()
	payload, _ := textFixturePayload(textFixtureEngine("前😀(后)\\"))
	data := textFixtureDocument(1, "TySh", payload, false)
	document, err := Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	layers, err := document.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	layer := layers[0]
	layer.TextDescriptor.Entries = append(layer.TextDescriptor.Entries,
		DescriptorEntry{Key: "TextIndex", Type: "long", Value: index})
	encoded, err := document.EncodeDescriptor(nil, layer.TextDescriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	result := append([]byte(nil), payload[:52]...)
	result = append(result, encoded...)
	result = append(result, payload[layer.WarpVersionData.Offset-layer.Block.Data.Offset:]...)
	return result
}

func TestDocumentTextAssociationChecksUniqueIndexAndText(t *testing.T) {
	payload := documentTextLayerPayload(t, 0)
	for _, duplicate := range []bool{false, true} {
		data := textFixtureDocument(1, "TySh", payload, duplicate)
		document, err := Read(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		defer document.Close()
		metadata := &psdtext.DocumentMetadata{Schema: 7, Texts: []psdtext.DocumentTextRecord{
			{Index: 0, Text: "前😀(后)\\\r", UTF16Units: 9},
		}}
		view, err := document.ReadDocumentTextAssociations(nil, metadata)
		if duplicate {
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("duplicate association: %v", err)
			}
			continue
		}
		if err != nil || len(view.Records) != 1 || view.Records[0].Text != &metadata.Texts[0] {
			t.Fatalf("association: %+v %v", view, err)
		}
		if _, err := document.ReadDocumentTextAssociations(nil, metadata, MetadataOptions{MaxBytes: view.MetadataBytes}); err != nil {
			t.Fatalf("exact association budget: %v", err)
		}
		if _, err := document.ReadDocumentTextAssociations(nil, metadata, MetadataOptions{MaxBytes: view.MetadataBytes - 1}); !errors.Is(err, ErrLimit) {
			t.Fatalf("one below association budget: %v", err)
		}
		metadata.Texts[0].Text = "different\r"
		if _, err := document.ReadDocumentTextAssociations(nil, metadata); !errors.Is(err, ErrMalformed) {
			t.Fatalf("different text: %v", err)
		}
		metadata.Texts = nil
		if _, err := document.ReadDocumentTextAssociations(nil, metadata); !errors.Is(err, ErrMalformed) {
			t.Fatalf("index range: %v", err)
		}
	}
}

func TestDocumentTextAssociationRetainsMatchingPhysicalSets(t *testing.T) {
	payload := documentTextLayerPayload(t, 0)
	data := textFixtureDocument(1, "TySh", payload, false)
	document, err := Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	metadata := &psdtext.DocumentMetadata{Schema: 7, Texts: []psdtext.DocumentTextRecord{
		{Index: 0, Text: "前😀(后)\\\r", UTF16Units: 9},
	}}
	physical := document.LayerSets[0]
	physical.Key = "Lr16"
	document.LayerSets = append(document.LayerSets, physical)
	view, err := document.ReadDocumentTextAssociations(nil, metadata)
	if err != nil || len(view.Records) != 2 ||
		view.Records[0].Layer.LayerSetIndex == view.Records[1].Layer.LayerSetIndex {
		t.Fatalf("matching physical sets: %+v %v", view, err)
	}
	document.LayerSets[1].Layers = append([]LayerRecord(nil), document.LayerSets[1].Layers...)
	document.LayerSets[1].Layers = append(document.LayerSets[1].Layers, document.LayerSets[1].Layers[0])
	if _, err := document.ReadDocumentTextAssociations(nil, metadata); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("same-set index collision: %v", err)
	}
}

func TestDocumentTextAssociationRejectsMutatedIndexDescriptor(t *testing.T) {
	payload := documentTextLayerPayload(t, 0)
	data := textFixtureDocument(1, "TySh", payload, false)
	document, err := Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	records, err := document.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	record := records[0]
	metadata := &psdtext.DocumentMetadata{Schema: 7, Texts: []psdtext.DocumentTextRecord{
		{Index: 0, Text: record.Text + "\r"},
	}}
	for _, test := range []struct {
		value any
		kind  string
	}{{nil, "long"}, {int32(1), "long"}, {int64(0), "comp"}, {int32(-1), "long"}} {
		for index := range record.TextDescriptor.Entries {
			entry := &record.TextDescriptor.Entries[index]
			if entry.Key == "TextIndex" {
				entry.Type, entry.Value = test.kind, test.value
			}
		}
		if _, err := record.ResolveDocumentText(nil, metadata); !errors.Is(err, ErrMalformed) {
			t.Fatalf("mutated current index was accepted: %v", err)
		}
	}
}

func TestDocumentTextAssociationRejectsMutatedTextDescriptor(t *testing.T) {
	payload := documentTextLayerPayload(t, 0)
	data := textFixtureDocument(1, "TySh", payload, false)
	document, err := Read(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer document.Close()
	for _, test := range []struct {
		value any
		kind  string
	}{{nil, "TEXT"}, {"different", "TEXT"}, {int32(0), "long"}} {
		records, err := document.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		record := records[0]
		metadata := &psdtext.DocumentMetadata{Schema: 7, Texts: []psdtext.DocumentTextRecord{
			{Index: 0, Text: record.Text + "\r"},
		}}
		for index := range record.TextDescriptor.Entries {
			entry := &record.TextDescriptor.Entries[index]
			if entry.Key == "Txt " {
				entry.Type, entry.Value = test.kind, test.value
			}
		}
		if _, err := record.ResolveDocumentText(nil, metadata); !errors.Is(err, ErrMalformed) {
			t.Fatalf("mutated current text was accepted: %v", err)
		}
	}
	records, err := document.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	record := records[0]
	record.TextDescriptor.Entries = append(record.TextDescriptor.Entries,
		DescriptorEntry{Key: "Txt ", Type: "TEXT", Value: record.Text})
	metadata := &psdtext.DocumentMetadata{Schema: 7, Texts: []psdtext.DocumentTextRecord{
		{Index: 0, Text: record.Text + "\r"},
	}}
	if _, err := record.ResolveDocumentText(nil, metadata); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("duplicate current text was accepted: %v", err)
	}
}
