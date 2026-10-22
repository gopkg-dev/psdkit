package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func nativeTextDocumentFixture(text string) []byte {
	data := []byte(" /98 << /0 7 >> /0 << /fontContext <aa01> /UnknownResource (opaqueSeed) >> /1 << /1 [ << /0 << /0 ")
	data = append(data, textEditLiteral(text+"\r")...)
	data = append(data, []byte(" /5 << /0 [ ")...)
	for part := range strings.SplitSeq(text, "\r") {
		units := len(utf16.Encode([]rune(part + "\r")))
		data = append(data, []byte("<< /0 << /privateParagraph <7721> >> /1 "+strconv.Itoa(units)+" >> ")...)
	}
	units := len(utf16.Encode([]rune(text + "\r")))
	data = append(data, []byte("] >> /6 << /0 [ << /0 << /privateStyle <7318> >> /1 "+strconv.Itoa(units)+" >> ] >> >> ")...)
	data = append(data, []byte("/1 << /0 [ << /0 0 >> ] /2 [ << /6 [ << /10 0 /callerGlyphs <5548> >> ] >> ] >> >> ] ")...)
	return append(data, []byte("/unknownDefault <e391> >> /unknownOuter (opaqueTail) ")...)
}

func nativeTextGlobalTag(payload []byte) []byte {
	data := append([]byte("8BIMTxt2"), binary.BigEndian.AppendUint32(nil, uint32(len(payload)))...)
	data = append(data, payload...)
	return append(data, make([]byte, (4-len(payload)%4)%4)...)
}

func nativeTextPayloadWithIndex(t *testing.T, text string) []byte {
	t.Helper()
	payload := textEditPayloadFixture(t, text, textEditEngineFixture(text), "warpNone")
	bridge, record, err := textEditSource(context.Background(), payload, psdfile.DefaultLimits(), 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	record.TextDescriptor.Entries = append(record.TextDescriptor.Entries, psdfile.DescriptorEntry{
		Key: "TextIndex", Type: "long", Value: int32(0),
	})
	encoded, err := bridge.EncodeDescriptor(nil, record.TextDescriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	result := append([]byte(nil), payload[:52]...)
	result = append(result, encoded...)
	return append(result, payload[record.WarpVersionData.Offset-record.Block.Data.Offset:]...)
}

func nativeTextFixture(t *testing.T, version uint16, options ...psdfile.LoadOptions) (*Editor, NativeTextStateOptions) {
	t.Helper()
	oldText := "AVég\rline"
	payload := nativeTextPayloadWithIndex(t, oldText)
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index := 3; index < len(pixels.Pix); index += 4 {
		pixels.Pix[index] = 255
	}
	record, channels := smartEditLayer(version, "Text", 30, pixels.Rect, pixels, compositeFixtureTag("TySh", payload), false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	global := nativeTextGlobalTag(nativeTextDocumentFixture(oldText))
	data := smartEditDocument(version, 4, 4, 3, layers, global, append([]byte{0, 0}, make([]byte, 48)...))
	editor := compositeEditor(t, data, options...)
	artwork := image.NewNRGBA(image.Rect(1, 0, 4, 2))
	for index := 0; index < len(artwork.Pix); index += 4 {
		copy(artwork.Pix[index:], []byte{20, 40, 80, 255})
	}
	preview := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for index := 0; index < len(preview.Pix); index += 4 {
		copy(preview.Pix[index:], []byte{90, 120, 150, 255})
	}
	newText := "new😀 Latin"
	state := NativeTextStateOptions{ExpectedRevision: 0, LayerSetIndex: editor.source.ActiveLayerSet,
		TySh: nativeTextPayloadWithIndex(t, newText), Txt2: nativeTextDocumentFixture(newText), Pixels: artwork, CompositeImage: preview}
	return editor, state
}

func TestNativeTextImportOwnsStateAndPreservesPreviewAfterRenamePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor, state := nativeTextFixture(t, version)
		nativeTxt2 := append([]byte(nil), state.Txt2...)
		if err := editor.SetNativeTextState(nil, 1, state); err != nil {
			t.Fatal(err)
		}
		if editor.revision != 1 || editor.nativeTextPreviewRevision != 1 || editor.nativeTextPreview == nil {
			t.Fatal("native transaction did not commit one revision with its preview")
		}
		state.TySh[0], state.Txt2[0], state.Pixels.Pix[0], state.CompositeImage.Pix[0] = 255, 255, 255, 255
		if err := editor.Rename(1, "Renamed native text"); err != nil {
			t.Fatal(err)
		}
		if editor.nativeTextPreview == nil || editor.nativeTextPreviewRevision != editor.revision {
			t.Fatal("nonvisual rename invalidated accurate preview")
		}
		var buffer bytes.Buffer
		if err := editor.Write(nil, &buffer); err != nil {
			t.Fatal(err)
		}
		saved, err := psdfile.Read(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
		if err != nil {
			t.Fatal(err)
		}
		defer saved.Close()
		records, err := saved.TextLayers(nil)
		if err != nil || len(records) != 1 || records[0].Text != "new😀 Latin" {
			t.Fatalf("native text: %v", err)
		}
		blocks, err := saved.ReadDocumentTextView(nil)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := saved.ReadSpan(blocks.Records[0].EngineData)
		if err != nil || !bytes.Equal(actual, nativeTxt2) {
			t.Fatal("complete caller native Txt2 was not retained")
		}
		layer, err := saved.DecodeLayer(nil, 0)
		if err != nil || layer.Bounds != image.Rect(1, 0, 4, 2) || layer.Channels[0].Data[0] != 20 {
			t.Fatalf("caller artwork ownership or bounds: %v", err)
		}
		composite, err := saved.DecodeComposite(nil)
		if err != nil || composite.Channels[0].Data[0] != 90 {
			t.Fatalf("accurate preview ownership: %v", err)
		}
		if err := editor.SetOpacity(1, 77); err != nil {
			t.Fatal(err)
		}
		if editor.nativeTextPreview != nil {
			t.Fatal("visual edit retained stale accurate preview")
		}
	}
}

func TestNativeTextImportRejectsUnsafeStateAtomically(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Editor, *NativeTextStateOptions)
		kind   error
	}{
		{"stale-zero", func(e *Editor, s *NativeTextStateOptions) { e.revision = 1 }, ErrStaleNativeTextState},
		{"wrong-set", func(_ *Editor, s *NativeTextStateOptions) { s.LayerSetIndex = 999 }, ErrUnsupported},
		{"missing-artwork", func(_ *Editor, s *NativeTextStateOptions) { s.Pixels = nil }, ErrInvalidEdit},
		{"missing-preview", func(_ *Editor, s *NativeTextStateOptions) { s.CompositeImage = nil }, ErrInvalidEdit},
		{"resource-drift", func(_ *Editor, s *NativeTextStateOptions) {
			s.Txt2 = bytes.Replace(s.Txt2, []byte("opaqueSeed"), []byte("opaqueHack"), 1)
		}, ErrUnsupported},
		{"shape-reference", func(_ *Editor, s *NativeTextStateOptions) {
			s.Txt2 = bytes.Replace(s.Txt2, []byte("/0 [ << /0 0 >> ]"), []byte("/0 [ << /0 3 >> ]"), 1)
		}, ErrInvalidEdit},
		{"content-mismatch", func(_ *Editor, s *NativeTextStateOptions) { s.Txt2 = nativeTextDocumentFixture("other") }, ErrInvalidEdit},
		{"relative-mask", func(e *Editor, _ *NativeTextStateOptions) { e.source.Layers[0].Mask = &psdfile.MaskRecord{Flags: 1} }, ErrUnsupported},
		{"extra-channel", func(e *Editor, _ *NativeTextStateOptions) {
			e.source.Layers[0].Channels = append(e.source.Layers[0].Channels, psdfile.ChannelRecord{ID: 7})
		}, ErrUnsupported},
		{"other-physical-set", func(e *Editor, _ *NativeTextStateOptions) {
			e.source.LayerSets = append(e.source.LayerSets, e.source.LayerSets[0])
		}, ErrUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor, state := nativeTextFixture(t, 1)
			test.mutate(editor, &state)
			before, _ := editor.Changes()
			owned := editor.ownedBytesLocked(nil)
			err := editor.SetNativeTextState(nil, 1, state)
			node, _, _, _ := editor.findNodeLocked(1)
			after, _ := editor.Changes()
			if !errors.Is(err, test.kind) || before != after || owned != editor.ownedBytesLocked(nil) ||
				node.pixels != nil || len(node.additional) != 0 || len(editor.globalAdditional) != 0 || editor.nativeTextPreview != nil {
				t.Fatalf("native failure was not atomic: %v", err)
			}
		})
	}
}

func TestNativeTextImportBudgetCancellationAndClosedSource(t *testing.T) {
	editor, state := nativeTextFixture(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.SetNativeTextState(ctx, 1, state); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
	before, _ := editor.Changes()
	owned := editor.ownedBytesLocked(nil)
	if err := editor.SetNativeTextState(nil, 1, state); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("peak budget: %v", err)
	}
	if after, _ := editor.Changes(); after != before || owned != editor.ownedBytesLocked(nil) {
		t.Fatal("budget failure committed")
	}
	if err := editor.source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetNativeTextState(nil, 1, state); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source: %v", err)
	}
}

func TestNativeTextImportCancellationDuringPayloadRead(t *testing.T) {
	fixture, state := nativeTextFixture(t, 1)
	data, err := fixture.source.ReadSpan(fixture.source.Source)
	if err != nil {
		t.Fatal(err)
	}
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	defer editor.Close()
	blocks, err := source.ReadDocumentTextView(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.trigger, reader.cancel = blocks.Records[0].EngineData, cancel
	err = editor.SetNativeTextState(ctx, 1, state)
	node, _, _, _ := editor.findNodeLocked(1)
	if !errors.Is(err, context.Canceled) || reader.matches == 0 || editor.revision != 0 || node.pixels != nil ||
		len(editor.globalAdditional) != 0 || editor.nativeTextPreview != nil {
		t.Fatalf("mid-read transaction: %v", err)
	}
}

func TestNativeTextBufferedWriteRejectsRawPeakButKeepsCompressedFit(t *testing.T) {
	const side = 1024
	const workingBytes = int64(8 << 20)
	for _, compression := range []uint16{0, 1, 2, 3} {
		t.Run(strconv.Itoa(int(compression)), func(t *testing.T) {
			fixture, state := nativeTextFixture(t, 1)
			original, err := fixture.source.ReadSpan(fixture.source.Source)
			if err != nil {
				t.Fatal(err)
			}
			data := append(bytes.Clone(original[:fixture.source.Composite.Span.Offset]), make([]byte, 2+side*side*3)...)
			binary.BigEndian.PutUint32(data[14:], side)
			binary.BigEndian.PutUint32(data[18:], side)
			source, err := psdfile.Read(bytes.NewReader(data), int64(len(data)), psdfile.LoadOptions{
				Limits: psdfile.Limits{MaxDecodeBytes: workingBytes},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			editor, err := New(source)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			state.LayerSetIndex = source.ActiveLayerSet
			state.CompositeImage = image.NewNRGBA(image.Rect(0, 0, side, side))
			for index := 3; index < len(state.CompositeImage.Pix); index += 4 {
				state.CompositeImage.Pix[index] = 255
			}
			if err := editor.SetNativeTextState(nil, 1, state); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			err = editor.Write(nil, &output, SaveOptions{Compression: compression})
			if compression == 0 {
				if !errors.Is(err, psdfile.ErrLimit) || output.Len() != 0 || editor.savedRevision != 0 {
					t.Fatalf("Raw peak bypass: %v, bytes=%d revision=%d", err, output.Len(), editor.savedRevision)
				}
				return
			}
			if err != nil || output.Len() == 0 || editor.savedRevision != 1 {
				t.Fatalf("compressed output that fits was rejected: %v, bytes=%d revision=%d", err, output.Len(), editor.savedRevision)
			}
		})
	}
	editor, state := nativeTextFixture(t, 1, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: workingBytes}})
	state.Pixels = image.NewNRGBA(image.Rect(0, 0, side, side))
	if err := editor.SetNativeTextState(nil, 1, state); err != nil {
		t.Fatal(err)
	}
	for _, invalidate := range []bool{false, true} {
		if invalidate {
			if err := editor.SetOpacity(1, 128); err != nil {
				t.Fatal(err)
			}
		}
		var output bytes.Buffer
		err := editor.Write(nil, &output)
		if !errors.Is(err, psdfile.ErrLimit) || output.Len() != 0 || editor.savedRevision != 0 {
			t.Fatalf("retained native artwork peak bypass after invalidate=%v: %v", invalidate, err)
		}
	}
}
