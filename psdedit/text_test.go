package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func textEditLiteral(value string) []byte {
	result := []byte{'(', 0xfe, 0xff}
	for _, unit := range utf16.Encode([]rune(value)) {
		for _, valueByte := range []byte{byte(unit >> 8), byte(unit)} {
			if valueByte == '(' || valueByte == ')' || valueByte == '\\' {
				result = append(result, '\\')
			}
			result = append(result, valueByte)
		}
	}
	return append(result, ')')
}

func textEditEngineFixture(text string) []byte {
	data := []byte("\n% retained engine comment.\n<< /EngineDict << /Editor << /Text ")
	data = append(data, textEditLiteral(text+"\r")...)
	data = append(data, []byte(" >> /StyleRun << /RunArray [ << /StyleSheet << /StyleSheetData ")...)
	data = append(data, []byte("<< /Font 0 /FontSize 24 /Leading 30 /UnknownTypography <99a1> >> >> >> ] ")...)
	data = append(data, []byte("/RunLengthArray [ "+strconv.Itoa(len(utf16.Encode([]rune(text+"\r"))))+"] >> ")...)
	data = append(data, []byte("/ParagraphRun << /RunArray [ ")...)
	paragraph := "<< /ParagraphSheet << /Properties << /Justification 0 /UnknownParagraph (raw) >> >> >> "
	for range strings.Count(text, "\r") + 1 {
		data = append(data, paragraph...)
	}
	data = append(data, []byte("] /RunLengthArray [ ")...)
	for part := range strings.SplitSeq(text, "\r") {
		data = append(data, strconv.Itoa(len(utf16.Encode([]rune(part+"\r"))))...)
		data = append(data, ' ')
	}
	data = append(data, []byte("] >> /Rendered << /Shapes << /Children [ ")...)
	data = append(data, []byte("<< /ShapeType 0 /Cookie << /Photoshop << /ShapeType 0 >> >> >> ] >> >> ")...)
	data = append(data, []byte("/UnrecognizedEngineProperty <a1b2c3> >> /ResourceDict << /FontSet [ << /Name ")...)
	data = append(data, textEditLiteral("FixtureFont-Regular")...)
	return append(data, []byte(" /Script 0 /FontType 1 /Synthetic 0 >> ] >> >>\x00")...)
}

func textEditPayloadFixture(t *testing.T, text string, engine []byte, warp string) []byte {
	t.Helper()
	descriptor := &psdfile.Descriptor{Version: 16, ClassID: "TxLr", Entries: []psdfile.DescriptorEntry{
		{Key: "Txt ", Type: "TEXT", Value: text},
		{Key: "unknownTextBlob", Type: "tdta", Value: []byte{0x71, 0x98, 0x23}},
		{Key: "Ornt", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "textGridding", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "textGridding", ValueID: "None"}},
		{Key: "EngineData", Type: "tdta", Value: engine},
		{Key: "unknownDuplicate", Type: "bool", Value: true},
		{Key: "unknownDuplicate", Type: "bool", Value: false},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := binary.BigEndian.AppendUint16(nil, 1)
	for _, bits := range []uint64{math.Float64bits(1), 1 << 63, 0, math.Float64bits(1),
		math.Float64bits(20.25), math.Float64bits(40.5)} {
		payload = binary.BigEndian.AppendUint64(payload, bits)
	}
	payload = binary.BigEndian.AppendUint16(payload, 50)
	payload = append(payload, encoded...)
	warpDescriptor := &psdfile.Descriptor{Version: 16, ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: warp}},
		{Key: "unknownWarp", Type: "long", Value: int32(99)},
	}}
	encoded, err = psdfile.EncodeDescriptor(nil, warpDescriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload = binary.BigEndian.AppendUint16(payload, 1)
	payload = append(payload, encoded...)
	for _, bits := range []uint32{1 << 31, math.Float32bits(-20.5), math.Float32bits(144.25), math.Float32bits(40.75)} {
		payload = binary.BigEndian.AppendUint32(payload, bits)
	}
	return append(payload, 0xa1, 0x99, 0x66)
}

func textEditFixture(
	t *testing.T, version uint16, engine []byte, global, extra []byte, options ...psdfile.LoadOptions,
) *Editor {
	t.Helper()
	payload := textEditPayloadFixture(t, "AVég\rline", engine, "warpNone")
	tags := append(compositeFixtureTag("TySh", payload), extra...)
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index := 3; index < len(pixels.Pix); index += 4 {
		pixels.Pix[index] = 255
	}
	record, channels := smartEditLayer(version, "Text", 30, pixels.Rect, pixels, tags, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	data := smartEditDocument(version, 4, 4, 3, layers, global, append([]byte{0, 0}, make([]byte, 48)...))
	return compositeEditor(t, data, options...)
}

func textEditRead(
	t *testing.T, source *psdfile.Document,
) (psdfile.TextLayerRecord, *psdtext.EngineData, *psdtext.Metadata) {
	t.Helper()
	records, err := source.TextLayers(nil)
	if err != nil || len(records) != 1 {
		t.Fatalf("text metadata: %v", err)
	}
	engine, err := source.DecodeTextEngine(nil, records[0])
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	return records[0], engine, metadata
}

func TestSetTextContentUpdatesUTF16RangesAndOwnedPixelsPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor := textEditFixture(t, version, textEditEngineFixture("AVég\rline"), nil,
			compositeFixtureTag("pRsv", []byte{0x99, 0x77}))
		original, originalEngine, _ := textEditRead(t, editor.source)
		originalPayload, _ := editor.source.ReadSpan(original.Block.Data)
		pixels := image.NewNRGBA(image.Rect(1, 0, 3, 2))
		for index := 0; index < len(pixels.Pix); index += 4 {
			copy(pixels.Pix[index:], []byte{50, 100, 150, 128})
		}
		// Supplementary Latin U+10780 consumes two UTF-16 units while remaining one Go rune.
		text := "A𐞀é\r\nnext\nthird"
		if err := editor.SetTextContent(nil, 1, text, TextEditOptions{Pixels: pixels}); err != nil {
			t.Fatal(err)
		}
		node, _, _, _ := editor.findNodeLocked(1)
		ownedBytes := editor.ownedBytesLocked(nil)
		before, _ := editor.Changes()
		if err := editor.SetTextContent(nil, 1, text, TextEditOptions{Pixels: pixels}); err != nil {
			t.Fatal(err)
		}
		if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != ownedBytes {
			t.Fatal("identical repeated edit changed revision or retained bytes")
		}
		pixels.Pix[0], pixels.Pix[3] = 255, 255
		if node.pixels.Pix[0] != 50 || node.pixels.Pix[3] != 128 || node.bounds != pixels.Rect {
			t.Fatal("replacement retained caller pixels or lost absolute geometry")
		}
		live, err := editor.Composite(context.Background())
		if err != nil || live.NRGBAAt(1, 0).A != 128 || live.NRGBAAt(0, 0).A != 0 {
			t.Fatalf("composite still uses original text pixels: %v", err)
		}
		saved := compositeSave(t, editor)
		updated, engine, metadata := textEditRead(t, saved)
		if updated.Text != "A𐞀é\rnext\rthird" || metadata.Text != updated.Text+"\r" ||
			metadata.UTF16Units != 16 || len(metadata.Styles) != 1 || len(metadata.Paragraphs) != 3 ||
			metadata.Paragraphs[0].Length != 5 || metadata.Paragraphs[1].Length != 5 ||
			metadata.Paragraphs[2].Length != 6 || saved.Layers[0].Bounds != pixels.Rect {
			t.Fatalf("text ranges or saved raster rectangle changed: %+v", metadata)
		}
		if updated.TransformBits != original.TransformBits || updated.BoundsBits != original.BoundsBits ||
			!reflect.DeepEqual(descriptorKeys(updated.TextDescriptor), descriptorKeys(original.TextDescriptor)) ||
			!textEditNodeEqual(engine.Root.Lookup("ResourceDict"), originalEngine.Root.Lookup("ResourceDict")) ||
			!textEditNodeEqual(metadata.Engine.Lookup("UnrecognizedEngineProperty"),
				originalEngine.Root.Lookup("EngineDict").Lookup("UnrecognizedEngineProperty")) {
			t.Fatal("known edit discarded unknown metadata, source order or IEEE bits")
		}
		oldSuffix, _ := editor.source.ReadSpan(psdfile.Span{Offset: original.WarpVersionData.Offset,
			Length: original.Block.Data.Offset + original.Block.Data.Length - original.WarpVersionData.Offset})
		newSuffix, _ := saved.ReadSpan(psdfile.Span{Offset: updated.WarpVersionData.Offset,
			Length: updated.Block.Data.Offset + updated.Block.Data.Length - updated.WarpVersionData.Offset})
		currentSourcePayload, _ := editor.source.ReadSpan(original.Block.Data)
		if len(newSuffix) < len(oldSuffix) || len(newSuffix)-len(oldSuffix) > 3 ||
			!bytes.Equal(oldSuffix, newSuffix[:len(oldSuffix)]) ||
			!bytes.Equal(newSuffix[len(oldSuffix):], make([]byte, len(newSuffix)-len(oldSuffix))) ||
			!bytes.Equal(originalPayload, currentSourcePayload) {
			t.Fatal("TySh warp/bounds/tail or immutable original source changed")
		}
		planes, err := saved.DecodeLayer(nil, 0)
		if err != nil || planes.Bounds != pixels.Rect || planes.Channels[0].Data[0] != 50 ||
			planes.Channels[3].Data[0] != 128 {
			t.Fatalf("saved text channel samples differ: %v", err)
		}
	}
}

func TestSetTextContentRejectsUnsafeFamiliesAndFailuresAtomically(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	for _, test := range []struct {
		name          string
		engine, extra []byte
		global        []byte
		text          string
		pixels        *image.NRGBA
		kind          error
	}{
		{name: "missing cache", text: "new", kind: ErrUnsupported},
		{name: "modern private state", global: compositeFixtureTag("Txt2", []byte("unknown")),
			text: "new", pixels: pixels, kind: ErrUnsupported},
		{name: "invalid UTF8", text: string([]byte{0xff}), pixels: pixels, kind: ErrInvalidEdit},
		{name: "NUL", text: "bad\x00", pixels: pixels, kind: ErrUnsupported},
		{name: "bad cache", text: "new", pixels: &image.NRGBA{Rect: image.Rect(0, 0, 1, 1)}, kind: ErrInvalidEdit},
		{name: "paragraph text", engine: bytes.Replace(textEditEngineFixture("AVég\rline"),
			[]byte("/ShapeType 0"), []byte("/ShapeType 1"), 1), text: "new", pixels: pixels, kind: ErrUnsupported},
		{name: "duplicate edited key", engine: bytes.Replace(textEditEngineFixture("AVég\rline"),
			[]byte("/Editor <<"), []byte("/Editor << /Text (raw)"), 1), text: "new", pixels: pixels, kind: ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := test.engine
			if engine == nil {
				engine = textEditEngineFixture("AVég\rline")
			}
			editor := textEditFixture(t, 1, engine, test.global, test.extra)
			before, _ := editor.Changes()
			err := editor.SetTextContent(nil, 1, test.text, TextEditOptions{Pixels: test.pixels})
			node, _, _, _ := editor.findNodeLocked(1)
			if after, _ := editor.Changes(); !errors.Is(err, test.kind) || after != before ||
				node.pixels != nil || len(node.additional) != 0 || node.semanticBytes != 0 {
				t.Fatalf("failure partially committed or returned wrong category: %v", err)
			}
		})
	}
	editor := textEditFixture(t, 1, textEditEngineFixture("AVég\rline"), nil, nil)
	if err := editor.SetTextContent(nil, 1, "first", TextEditOptions{Pixels: pixels}); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	before, _ := editor.Changes()
	retained, oldPixels := editor.ownedBytesLocked(nil), node.pixels
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := editor.SetTextContent(ctx, 1, "canceled", TextEditOptions{Pixels: pixels})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if after, _ := editor.Changes(); after != before || node.pixels != oldPixels ||
		editor.ownedBytesLocked(nil) != retained {
		t.Fatal("canceled replacement changed an existing transaction")
	}
	if err := editor.source.Close(); err != nil {
		t.Fatal(err)
	}
	err = editor.SetTextContent(nil, 1, "closed", TextEditOptions{Pixels: pixels})
	if !errors.Is(err, psdfile.ErrClosed) {
		t.Fatal(err)
	}
}

func TestSetTextContentReplacementChargesPreviousOverlaysToPeakBudget(t *testing.T) {
	limits := psdfile.DefaultLimits()
	limits.MaxDecodeBytes = 2 << 20
	editor := textEditFixture(t, 1, textEditEngineFixture("AVég\rline"), nil, nil,
		psdfile.LoadOptions{Limits: limits})
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	if err := editor.SetTextContent(nil, 1, "first", TextEditOptions{Pixels: pixels}); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	node.setSemanticCost("other", limits.MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
	before, _ := editor.Changes()
	retained, oldPixels := editor.ownedBytesLocked(nil), node.pixels
	if err := editor.SetTextContent(nil, 1, "second", TextEditOptions{Pixels: pixels}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatal(err)
	}
	if after, _ := editor.Changes(); after != before || oldPixels != node.pixels ||
		editor.ownedBytesLocked(nil) != retained {
		t.Fatal("over-budget replacement changed an existing transaction")
	}
}

func TestSetTextContentCancellationDuringMetadataReadIsTransactional(t *testing.T) {
	fixture := textEditFixture(t, 1, textEditEngineFixture("AVég\rline"), nil, nil)
	data, err := fixture.source.ReadSpan(fixture.source.Source)
	if err != nil {
		t.Fatal(err)
	}
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	record, _, _ := textEditRead(t, source)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.trigger, reader.cancel = record.Block.Data, cancel
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	err = editor.SetTextContent(ctx, 1, "changed", TextEditOptions{Pixels: pixels})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during original payload read was lost: %v", err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	if state, _ := editor.Changes(); state.Dirty || node.pixels != nil || len(node.additional) != 0 ||
		node.semanticBytes != 0 || reader.matches == 0 {
		t.Fatal("canceled payload read committed a partial transaction")
	}
}

func TestSetTextContentRejectsInvalidatedExtraChannelsAndRelativeMasks(t *testing.T) {
	editor := textEditFixture(t, 1, textEditEngineFixture("AVég\rline"), nil, nil)
	node, _, _, _ := editor.findNodeLocked(1)
	layer := &editor.source.Layers[0]
	pixels := image.NewNRGBA(image.Rect(1, 0, 2, 1))
	layer.Mask = &psdfile.MaskRecord{Flags: 1}
	if err := editor.SetTextContent(nil, 1, "new", TextEditOptions{Pixels: pixels}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("relative mask would move with changed ink rectangle: %v", err)
	}
	layer.Mask = nil
	layer.Channels = append(layer.Channels, psdfile.ChannelRecord{ID: 7})
	if err := editor.SetTextContent(nil, 1, "new", TextEditOptions{Pixels: pixels}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown source channel geometry would become invalid: %v", err)
	}
	if state, _ := editor.Changes(); state.Dirty || node.pixels != nil || len(node.additional) != 0 {
		t.Fatal("unsafe geometry changes committed an overlay")
	}
}
