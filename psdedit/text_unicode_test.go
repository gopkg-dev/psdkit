package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// unicodeTextFixture encodes Unicode source text independently of the content editor.
func unicodeTextFixture(t *testing.T, version uint16, text string) *Editor {
	t.Helper()
	payload := textEditPayloadFixture(t, text, textEditEngineFixture(text), "warpNone")
	tags := append(compositeFixtureTag("TySh", payload), compositeFixtureTag("pRsv", []byte{0xa1, 0x99, 0x73})...)
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index := 0; index < len(pixels.Pix); index += 4 {
		copy(pixels.Pix[index:], []byte{22, 33, 44, 255})
	}
	record, channels := smartEditLayer(version, "Unicode text", 30, pixels.Rect, pixels, tags, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	data := smartEditDocument(version, 4, 4, 3, layers, nil, append([]byte{0, 0}, make([]byte, 48)...))
	return compositeEditor(t, data)
}

func TestSetTextContentUnicodeSourcesSaveReopenAndEditAgain(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, compression := range []uint16{0, 1, 2, 3} {
			for _, test := range []struct {
				name, source, input, normalized string
				lengths                         []int64
			}{
				{"Chinese", "旧中文\r下一行", "中文标题", "中文标题", []int64{5}},
				{"supplementary and ZWJ", "😀旧", "👩‍💻🌍", "👩‍💻🌍", []int64{8}},
				{"combining marks", "e\u0301", "A\u030ae\u0301\rक्\u200dष", "A\u030ae\u0301\rक्\u200dष", []int64{5, 5}},
				{"multiline", "甲\r😀", "中文\r\n😀\n末尾\r", "中文\r😀\r末尾\r", []int64{3, 3, 3, 1}},
				{"empty", "中文😀", "", "", []int64{1}},
				{"RTL formatting", "مرحبا", "שלום \u200fمرحبا", "שלום \u200fمرحبا", []int64{12}},
			} {
				t.Run(fmt.Sprintf("version%d_compression%d/%s", version, compression, test.name), func(t *testing.T) {
					editor := unicodeTextFixture(t, version, test.source)
					original, originalEngine, originalMetadata := textEditRead(t, editor.source)
					originalBytes, err := editor.source.ReadSpan(editor.source.Source)
					if err != nil {
						t.Fatal(err)
					}
					pixels := image.NewNRGBA(image.Rect(1, 0, 3, 2))
					for index := 0; index < len(pixels.Pix); index += 4 {
						copy(pixels.Pix[index:], []byte{50, 100, 150, 128})
					}
					if test.input == "" {
						clear(pixels.Pix)
					}
					if err := editor.SetTextContent(t.Context(), 1, test.input, TextEditOptions{Pixels: pixels}); err != nil {
						t.Fatal(err)
					}
					saved := compositeSave(t, editor, SaveOptions{Compression: compression})
					updated, engine, metadata := textEditRead(t, saved)
					if saved.Header.Version != version || updated.Text != test.normalized ||
						metadata.Text != test.normalized+"\r" || len(metadata.Styles) != 1 ||
						len(metadata.Paragraphs) != len(test.lengths) {
						t.Fatalf("Unicode descriptor/engine content changed: %+v", metadata)
					}
					units := int64(0)
					for index, length := range test.lengths {
						paragraph := metadata.Paragraphs[index]
						if paragraph.Start != units || paragraph.Length != length ||
							!textEditNodeEqual(paragraph.Data, originalMetadata.Paragraphs[0].Data) {
							t.Fatalf("paragraph %d UTF-16 range/style changed: %+v", index, paragraph)
						}
						units += length
					}
					if metadata.UTF16Units != units || metadata.Styles[0].Start != 0 || metadata.Styles[0].Length != units ||
						!textEditNodeEqual(metadata.Styles[0].Data, originalMetadata.Styles[0].Data) ||
						!textEditNodeEqual(engine.Root.Lookup("ResourceDict"), originalEngine.Root.Lookup("ResourceDict")) ||
						!textEditNodeEqual(metadata.Engine.Lookup("UnrecognizedEngineProperty"),
							originalMetadata.Engine.Lookup("UnrecognizedEngineProperty")) {
						t.Fatal("Unicode editing changed style ranges or retained typography/engine metadata")
					}
					if updated.TransformBits != original.TransformBits || updated.BoundsBits != original.BoundsBits ||
						!reflect.DeepEqual(descriptorKeys(updated.TextDescriptor), descriptorKeys(original.TextDescriptor)) {
						t.Fatal("Unicode editing changed opaque descriptor bytes, ordering or geometry")
					}
					textUnicodeExpectOpaquePreserved(t, editor.source, saved, original, updated)
					planes, err := saved.DecodeLayer(t.Context(), 0)
					if err != nil || planes.Bounds != pixels.Rect || len(planes.Channels) != 4 {
						t.Fatalf("Unicode text raster geometry changed: %v", err)
					}
					for _, channel := range planes.Channels {
						component := int(channel.ID)
						if channel.ID == -1 {
							component = 3
						}
						for index, value := range channel.Data {
							if value != pixels.Pix[index*4+component] {
								t.Fatalf("Unicode text raster channel %d sample %d changed", channel.ID, index)
							}
						}
					}
					reopened := lifecycleReopen(t, saved)
					live, err := editor.Composite(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					persisted, err := reopened.Composite(t.Context())
					if err != nil || !bytes.Equal(persisted.Pix, live.Pix) {
						t.Fatalf("Unicode text preview changed after reopen: %v", err)
					}
					if err := reopened.SetTextContent(t.Context(), 1, "再编辑😀e\u0301",
						TextEditOptions{Pixels: pixels}); err != nil {
						t.Fatalf("saved Unicode source could not be edited again: %v", err)
					}
					again := compositeSave(t, reopened, SaveOptions{Compression: compression})
					last, _, lastMetadata := textEditRead(t, again)
					if last.Text != "再编辑😀e\u0301" || lastMetadata.UTF16Units != 8 ||
						len(lastMetadata.Paragraphs) != 1 || lastMetadata.Paragraphs[0].Length != 8 {
						t.Fatal("repeated Unicode edit did not preserve UTF-16 range semantics")
					}
					currentBytes, err := editor.source.ReadSpan(editor.source.Source)
					if err != nil || !bytes.Equal(currentBytes, originalBytes) {
						t.Fatalf("Unicode edit mutated source metadata or raster bytes: %v", err)
					}
				})
			}
		}
	}
}

func textUnicodeExpectOpaquePreserved(
	t *testing.T, source, saved *psdfile.Document, original, updated psdfile.TextLayerRecord,
) {
	t.Helper()
	oldBlob := gradientFind(original.TextDescriptor, "unknownTextBlob").Value.(psdfile.DescriptorBlob)
	newBlob := gradientFind(updated.TextDescriptor, "unknownTextBlob").Value.(psdfile.DescriptorBlob)
	oldBlobBytes, err := source.ReadSpan(oldBlob.Span)
	if err != nil {
		t.Fatal(err)
	}
	newBlobBytes, err := saved.ReadSpan(newBlob.Span)
	if err != nil || !bytes.Equal(oldBlobBytes, newBlobBytes) {
		t.Fatalf("Unicode editing changed opaque descriptor payload bytes: %v", err)
	}
	oldSuffix, err := source.ReadSpan(psdfile.Span{Offset: original.WarpVersionData.Offset,
		Length: original.Block.Data.Offset + original.Block.Data.Length - original.WarpVersionData.Offset})
	if err != nil {
		t.Fatal(err)
	}
	newSuffix, err := saved.ReadSpan(psdfile.Span{Offset: updated.WarpVersionData.Offset,
		Length: updated.Block.Data.Offset + updated.Block.Data.Length - updated.WarpVersionData.Offset})
	if err != nil || len(newSuffix) < len(oldSuffix) || len(newSuffix)-len(oldSuffix) > 3 ||
		!bytes.Equal(newSuffix[:len(oldSuffix)], oldSuffix) ||
		!bytes.Equal(newSuffix[len(oldSuffix):], make([]byte, len(newSuffix)-len(oldSuffix))) {
		t.Fatalf("Unicode editing changed warp/bounds/opaque tail bytes: %v", err)
	}
	for index, block := range source.Layers[0].AdditionalInfo {
		if block.Key == "TySh" {
			continue
		}
		oldBytes, err := source.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
		newBytes, err := saved.ReadSpan(saved.Layers[0].AdditionalInfo[index].Span)
		if err != nil || !bytes.Equal(newBytes, oldBytes) {
			t.Fatalf("Unicode editing changed unrelated tagged bytes: %v", err)
		}
	}
}

func TestSetTextContentUnicodeInvalidControlsAndFailuresAreAtomic(t *testing.T) {
	editor := unicodeTextFixture(t, 1, "中文😀")
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	if err := editor.SetTextContent(t.Context(), 1, "保留😀", TextEditOptions{Pixels: pixels}); err != nil {
		t.Fatal(err)
	}
	node := editor.roots[0]
	before, _ := editor.Changes()
	retained, oldPixels := editor.ownedBytesLocked(nil), node.pixels
	for _, test := range []struct {
		text string
		kind error
	}{
		{string([]byte{0xed, 0xa0, 0x80}), ErrInvalidEdit},
		{"中文\x00😀", ErrUnsupported}, {"中文\t", ErrUnsupported}, {"中文\x07", ErrUnsupported},
		{"中文\x1b", ErrUnsupported}, {"中文\x7f", ErrUnsupported}, {"中文\u0085", ErrUnsupported},
		{"中文\u2028😀", ErrUnsupported}, {"中文\u2029😀", ErrUnsupported},
	} {
		err := editor.SetTextContent(t.Context(), 1, test.text, TextEditOptions{Pixels: pixels})
		if !errors.Is(err, test.kind) {
			t.Fatalf("invalid Unicode/control edit category = %v, want %v", err, test.kind)
		}
	}
	if err := editor.SetTextContent(t.Context(), 1, "😀中文", TextEditOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Unicode editing no longer requires accurate replacement pixels: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := editor.SetTextContent(ctx, 1, "取消😀", TextEditOptions{Pixels: pixels}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Unicode edit = %v", err)
	}
	if after, _ := editor.Changes(); after != before || oldPixels != node.pixels ||
		editor.ownedBytesLocked(nil) != retained {
		t.Fatal("invalid or canceled Unicode edit changed an existing transaction")
	}
	node.setSemanticCost("other", editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
	retained = editor.ownedBytesLocked(nil)
	err := editor.SetTextContent(t.Context(), 1, "超预算😀", TextEditOptions{Pixels: pixels})
	if !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("Unicode peak budget = %v", err)
	}
	if after, _ := editor.Changes(); after != before || oldPixels != node.pixels ||
		editor.ownedBytesLocked(nil) != retained {
		t.Fatal("over-budget Unicode edit changed an existing transaction")
	}
	for _, sourceText := range []string{"原文\x00", "原文\t", "原文\u2028"} {
		invalidSource := unicodeTextFixture(t, 1, sourceText)
		err := invalidSource.SetTextContent(t.Context(), 1, "替换😀", TextEditOptions{Pixels: pixels})
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported source control content was silently accepted: %v", err)
		}
		if state, _ := invalidSource.Changes(); state.Dirty {
			t.Fatal("unsupported source Unicode edit dirtied the editor")
		}
	}
}
