package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func fillEditFixture(t *testing.T) []byte {
	t.Helper()
	descriptor := &psdfile.Descriptor{Version: 16, ClassID: "solidColorLayer", Entries: []psdfile.DescriptorEntry{
		{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
			{Key: "Rd  ", Type: "doub", Value: float64(20)},
			{Key: "Grn ", Type: "doub", Value: float64(40)},
			{Key: "Bl  ", Type: "doub", Value: float64(60)},
		}}},
		{Key: "preserved", Type: "bool", Value: true},
	}}
	encoded, err := psdfile.EncodeDescriptor(context.Background(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, 0x99, 0x77)
	block := writerLinkedTag("8BIM", "SoCo", 4, encoded, 2)
	pixels := smartEditImage(smartEditColors())
	record, payloads := smartEditLayer(1, "Live fill", 20, image.Rect(0, 0, 2, 2), pixels, block, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	return smartEditDocument(1, 2, 2, 3, layers, nil, append([]byte{0, 0}, make([]byte, 12)...))
}

func TestSolidFillChangesLiveParametersAndPreservesSource(t *testing.T) {
	data := fillEditFixture(t)
	original := bytes.Clone(data)
	editor, source := smartEditOpen(t, data)
	layers, err := editor.Layers()
	if err != nil || len(layers) != 1 {
		t.Fatalf("layers=%d error=%v", len(layers), err)
	}
	id := layers[0].ID
	if err := editor.SetSolidFill(context.Background(), id, [3]float64{20.0 / 255, 40.0 / 255, 60.0 / 255}); err != nil {
		t.Fatal(err)
	}
	state, _ := editor.Changes()
	if state.Dirty {
		t.Fatal("unchanged color dirtied document")
	}
	rgb := [3]float64{.2, .4, .8}
	if err := editor.SetSolidFill(context.Background(), id, rgb); err != nil {
		t.Fatal(err)
	}
	preview, err := editor.Composite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(preview.Pix[:4], []byte{51, 102, 204, 255}) {
		t.Fatalf("live fill=%v", preview.Pix[:4])
	}
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	snapshot, err := saved.Decode(context.Background())
	if err != nil || snapshot.Layers[0].FillColor == nil || *snapshot.Layers[0].FillColor != rgb {
		t.Fatalf("saved descriptor color missing: %v", err)
	}
	var sourceBlock, savedBlock *psdfile.AdditionalBlock
	for index := range source.Layers[0].AdditionalInfo {
		if source.Layers[0].AdditionalInfo[index].Key == "SoCo" {
			sourceBlock = &source.Layers[0].AdditionalInfo[index]
		}
	}
	for index := range saved.Layers[0].AdditionalInfo {
		if saved.Layers[0].AdditionalInfo[index].Key == "SoCo" {
			savedBlock = &saved.Layers[0].AdditionalInfo[index]
		}
	}
	if sourceBlock == nil || savedBlock == nil {
		t.Fatal("solid descriptor block disappeared")
	}
	descriptor, err := saved.DecodeDescriptor(savedBlock.Data, true)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Entries[1].Key != "preserved" || descriptor.Entries[1].Value != true {
		t.Fatal("unrelated descriptor entry changed")
	}
	tail, err := saved.ReadSpan(psdfile.Span{Offset: descriptor.Span.Offset + descriptor.Span.Length, Length: savedBlock.Data.Length - descriptor.Span.Length})
	if err != nil || !bytes.Equal(tail, []byte{0x99, 0x77}) {
		t.Fatalf("original descriptor tail changed: %x %v", tail, err)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("source bytes changed")
	}
}

func TestSolidFillInvalidEditsAreTransactional(t *testing.T) {
	editor, _ := smartEditOpen(t, fillEditFixture(t))
	layers, _ := editor.Layers()
	before, _ := editor.Changes()
	if err := editor.SetSolidFill(context.Background(), layers[0].ID, [3]float64{2, 0, 0}); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid RGB accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetSolidFill(ctx, layers[0].ID, [3]float64{1, 0, 0}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled fill accepted: %v", err)
	}
	after, _ := editor.Changes()
	if after != before {
		t.Fatal("failed fill changed revision")
	}
}
