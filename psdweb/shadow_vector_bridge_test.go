package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

// bridgeShadowVectorFixture combines independent wire fixtures without using the editor or writer.
// Its ordinary pixel mask and partial source alpha exercise saved white-matte quantization.
func bridgeShadowVectorFixture(t *testing.T, version uint16, mixed bool) []byte {
	t.Helper()
	effectsSource, err := psdfile.Parse(bridgeMixedMaskEffectsFixture(t, version))
	if err != nil {
		t.Fatal(err)
	}
	defer effectsSource.Close()
	var effects []byte
	for _, block := range effectsSource.Layers[1].AdditionalInfo {
		if block.Key != "lfx2" {
			continue
		}
		descriptor, err := effectsSource.DecodeDescriptorContext(t.Context(),
			psdfile.Span{Offset: block.Data.Offset + 4, Length: block.Data.Length - 4}, true)
		if err != nil {
			t.Fatal(err)
		}
		if !mixed {
			for index, entry := range descriptor.Entries {
				if entry.Key == "SoFi" {
					descriptor.Entries = append(descriptor.Entries[:index], descriptor.Entries[index+1:]...)
					break
				}
			}
		}
		encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
		if err != nil {
			t.Fatal(err)
		}
		effects = append(make([]byte, 4), encoded...)
	}
	if effects == nil {
		t.Fatal("shadow fixture has no independent effect descriptor")
	}
	data := bridgeVectorOverlayFixture(t, version)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[0]
	var additional []byte
	for _, block := range record.AdditionalInfo {
		if block.Key == "vmsk" {
			continue
		}
		payload, err := source.ReadSpan(block.Data)
		if err != nil {
			t.Fatal(err)
		}
		if block.Key == "lfx2" {
			payload = effects
		}
		additional = append(additional, bridgeSmartTag(block.Key, payload, false, 2)...)
	}
	start := record.AdditionalInfo[0].Span.Offset
	end := record.Extra.Offset + record.Extra.Length
	delta := int64(len(additional)) - (end - start)
	for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
		if version == 2 {
			length := int64(binary.BigEndian.Uint64(data[offset:])) + delta
			binary.BigEndian.PutUint64(data[offset:], uint64(length))
		} else {
			length := int64(binary.BigEndian.Uint32(data[offset:])) + delta
			binary.BigEndian.PutUint32(data[offset:], uint32(length))
		}
	}
	binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length+delta))
	result := append(bytes.Clone(data[:start]), additional...)
	return append(result, data[end:]...)
}

func bridgeShadowVectorExecute(
	t *testing.T, engine *Engine, operation, editor string, arguments ...any,
) Response {
	t.Helper()
	request := Request{Operation: operation, Target: editor}
	for _, argument := range arguments {
		encoded, err := json.Marshal(argument)
		if err != nil {
			t.Fatal(err)
		}
		request.Args = append(request.Args, encoded)
	}
	return engine.Execute(t.Context(), request, nil)
}

func bridgeShadowVectorRejectsPolicy(
	t *testing.T, engine *Engine, operation, editor string, arguments ...any,
) {
	t.Helper()
	before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
	response := bridgeShadowVectorExecute(t, engine, operation, editor, arguments...)
	if response.OK || response.Error == nil || response.Error.Code != "unsupported" || len(response.Binaries) != 0 {
		t.Fatalf("%s accepted a missing shadow policy or returned partial bytes: %+v", operation, response.Error)
	}
	after := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
	if before != after {
		t.Fatalf("%s changed editor state after rejecting its policy", operation)
	}
}

func TestShadowVectorBridgeTypedJSONEditSaveReopenAndCurrentMasks(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, mixed := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d/mixed%t", version, mixed), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				original := bridgeShadowVectorFixture(t, version, mixed)
				asset := bridgeAsset(t, engine, "shadow-vector.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				policy := map[string]any{"ApproximateDropShadow": true}
				current := original
				for compression, operation := range []string{
					"AddVectorMask", "SetVectorMask", "RemoveVectorMask", "AddVectorMask",
				} {
					mask := bridgeCompletionVectorMask(t)
					if operation == "SetVectorMask" {
						for index := range mask.Subpaths[0].Knots {
							knot := &mask.Subpaths[0].Knots[index]
							knot.Preceding.X *= .5
							knot.Anchor.X *= .5
							knot.Leaving.X *= .5
						}
					}
					if operation == "RemoveVectorMask" {
						bridgeCall(t, engine, "editor."+operation, editor, 1)
					} else {
						bridgeCall(t, engine, "editor."+operation, editor, 1, mask)
					}
					set := bridgeVectorOverlaySet(t, current)
					expectedCount := 1
					if mixed {
						expectedCount = 2
					}
					if len(set.Items) != expectedCount {
						t.Fatal("fixture changed its intended effect families")
					}
					set.Items[0].Distance = .25 + float64(compression)*.25
					set.Items[0].Size, set.Items[0].Opacity = .5, .5+float64(compression)*.125
					set.Items[0].Color = [3]float64{.2, .4, .8}
					if mixed {
						set.Items[1].Color, set.Items[1].Opacity = [3]float64{.8, .2, .6}, .5
					}
					before := bytes.Clone(bridgeMixedMaskRGBA(t, bridgeCall(t, engine, "editor.Composite", editor, policy)))
					bridgeShadowVectorRejectsPolicy(t, engine, "editor.SetEffectsWithOptions", editor, 1, set,
						map[string]any{})
					after := bridgeMixedMaskRGBA(t, bridgeCall(t, engine, "editor.Composite", editor, policy))
					if !bytes.Equal(before, after) {
						t.Fatal("rejected effect policy changed the live pixels")
					}
					bridgeCall(t, engine, "editor.SetEffectsWithOptions", editor, 1, set, policy)
					bridgeShadowVectorRejectsPolicy(t, engine, "editor.Composite", editor)
					bridgeShadowVectorRejectsPolicy(t, engine, "editor.WritePSD", editor,
						map[string]any{"Compression": compression})
					live := bytes.Clone(bridgeMixedMaskRGBA(t, bridgeCall(t, engine, "editor.Composite", editor, policy)))
					if live[3] == 0 || live[3] >= 128 {
						t.Fatalf("fixture lost its visible low-alpha mask coverage: %v", live)
					}
					saved := bridgeCall(t, engine, "editor.WritePSD", editor,
						map[string]any{"Compression": compression, "ApproximateDropShadow": true})
					if len(saved.Binaries) != 1 || saved.Binaries[0].MIME != "image/vnd.adobe.photoshop" {
						t.Fatal("shadow/vector bridge did not export a PSD/PSB binary")
					}
					current = saved.Binaries[0].Data
					persisted := bridgeVectorOverlaySet(t, current)
					for index, effect := range set.Items {
						actual := persisted.Items[index]
						if actual.Kind != effect.Kind || actual.Color != effect.Color || actual.Opacity != effect.Opacity ||
							actual.Distance != effect.Distance || actual.Size != effect.Size {
							t.Fatal("typed JSON shadow/vector edit lost effect controls on save")
						}
					}
					output := bridgeAsset(t, engine, "shadow-vector-saved.psd", current)
					opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
					editor = opened["editor"].(string)
					if opened["header"].(psdfile.Header).Version != version {
						t.Fatal("shadow/vector edit changed the PSD/PSB format")
					}
					reopened := bridgeMixedMaskRGBA(t, bridgeCall(t, engine, "editor.Composite", editor, policy))
					if !bytes.Equal(live, reopened) {
						t.Fatalf("live shadow/vector pixels changed after reopen: before=%v after=%v", live, reopened)
					}
					pixel := color.NRGBA{R: live[0], G: live[1], B: live[2], A: live[3]}
					bridgeVectorOverlayPreview(t,
						bridgeCall(t, engine, "preview.saved", "", ref(opened["document"].(string))),
						bridgeVectorOverlaySavedPixel(pixel))
				}
				if !bytes.Equal(original, engine.assets[asset].data) {
					t.Fatal("shadow/vector edits mutated the original imported asset")
				}
			})
		}
	}
}
