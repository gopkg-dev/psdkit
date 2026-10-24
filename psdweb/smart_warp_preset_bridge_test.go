package psdweb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func bridgePresetDescriptor(style string, value float64) *psdfile.Descriptor {
	warp := bridgeWarpDescriptor(nil)
	warp.Entries = warp.Entries[:len(warp.Entries)-1]
	for index := range warp.Entries {
		switch warp.Entries[index].Key {
		case "warpStyle":
			warp.Entries[index].Value = psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: style}
		case "warpValue":
			warp.Entries[index].Value = value
		case "vOrder":
			warp.Entries[index].Value = int32(2)
		}
	}
	return warp
}

// bridgePresetFixture retains independently encoded shared sources and replaces only the native Warp descriptor.
func bridgePresetFixture(t *testing.T, version uint16, external bool, style string) ([]byte, []byte) {
	t.Helper()
	data, contents := bridgeWarpFixture(t, version, external, false)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, record := range slices.Backward(source.Layers) {
		for _, block := range record.AdditionalInfo {
			if block.Key != "SoLd" {
				continue
			}
			descriptor, err := source.DecodeDescriptorContext(t.Context(),
				psdfile.Span{Offset: block.Data.Offset + 8, Length: block.Data.Length - 8}, true)
			if err != nil {
				t.Fatal(err)
			}
			for field := range descriptor.Entries {
				if descriptor.Entries[field].Key == "warp" {
					descriptor.Entries[field].Value = bridgePresetDescriptor(style, 0)
				}
			}
			encoded, err := source.EncodeDescriptor(t.Context(), descriptor, true)
			if err != nil {
				t.Fatal(err)
			}
			payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
			replacement := bridgeSmartTag("SoLd", payload, false, 2)
			delta := int64(len(replacement)) - block.Span.Length
			for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
				if version == 2 {
					binary.BigEndian.PutUint64(data[offset:], uint64(int64(binary.BigEndian.Uint64(data[offset:]))+delta))
				} else {
					binary.BigEndian.PutUint32(data[offset:], uint32(int64(binary.BigEndian.Uint32(data[offset:]))+delta))
				}
			}
			binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length+delta))
			result := append(bytes.Clone(data[:block.Span.Offset]), replacement...)
			data = append(result, data[block.Span.Offset+block.Span.Length:]...)
		}
	}
	return data, contents
}

func verifyBridgePresetSaved(t *testing.T, data, original, live []byte, version uint16, value float64) {
	t.Helper()
	verifyBridgeWarpSaved(t, data, original, live, version, nil)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	metadata, err := source.ReadLayerMetadata(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	warp := metadata.Layer.Placed.Warp
	if warp.Style != "warpArcLower" || warp.Rotate != "horizontal" || warp.Value != value ||
		!warp.HasValue || warp.UOrder != 4 || warp.VOrder != 2 || warp.HasEnvelope || warp.Split {
		t.Fatalf("preset edit changed the native schema or lost the current bend: %+v", warp)
	}
}

func TestSmartWarpPresetBridgeEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range 4 {
			for _, external := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/external%t", version, compression, external), func(t *testing.T) {
					engine, err := New(Config{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
					original, contents := bridgePresetFixture(t, version, external, "warpArcLower")
					asset := bridgeAsset(t, engine, "preset.psd", original)
					opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
					var previous []byte
					for _, value := range []float64{25, -25} {
						editor := opened["editor"].(string)
						infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
							bridgeCall(t, engine, "editor.SmartObjects", editor))
						var links map[string]string
						if external {
							links = map[string]string{infos[0].SourceID: bridgeAsset(t, engine, "source.psd", contents)}
						}
						parameters := map[string]any{"Value": value}
						policy := map[string]any{"Render": map[string]any{"ApproximateCustomWarp": true}}
						before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
						blocked := bridgeGridRequest(t, engine, "editor.SetSmartObjectWarpPreset", editor, links,
							infos[0].ID, parameters, map[string]any{})
						if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
							t.Fatal("preset editing implicitly enabled mathematical sampling")
						}
						if external {
							blocked = bridgeGridRequest(t, engine, "editor.SetSmartObjectWarpPreset", editor, nil,
								infos[0].ID, parameters, policy)
							if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
								t.Fatal("preset editing inferred an external mapping")
							}
						}
						if after := blendingBridgeResult[psdedit.Changes](t,
							bridgeCall(t, engine, "editor.Changes", editor)); before != after {
							t.Fatal("preset policy failure partially committed state")
						}
						bridgeSmartCall(t, engine, "SetSmartObjectWarpPreset", editor, links,
							infos[0].ID, parameters, policy)
						live := bytes.Clone(bridgeWarpPixels(t, bridgeCall(t, engine, "editor.Composite", editor)))
						if previous != nil && bytes.Equal(previous, live) {
							t.Fatal("continued native bend editing did not update visible pixels")
						}
						saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": compression})
						if len(saved.Binaries) != 1 {
							t.Fatal("preset edit required caller-supplied pixels to save")
						}
						verifyBridgePresetSaved(t, saved.Binaries[0].Data, original, live, version, value)
						output := bridgeAsset(t, engine, "preset-edited.psd", saved.Binaries[0].Data)
						opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
						reopened := bridgeWarpPixels(t,
							bridgeCall(t, engine, "editor.Composite", opened["editor"].(string)))
						if !bytes.Equal(live, reopened) {
							t.Fatal("saved preset artwork changed after reopening")
						}
						verifyBridgeWarpSource(t, engine, opened, links, live)
						if external {
							blocked = bridgeGridRequest(t, engine, "render.source", "", nil,
								ref(opened["document"].(string)), map[string]any{"ApproximateCustomWarp": true})
							if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
								t.Fatal("preset source redraw inherited an earlier external mapping")
							}
						}
						previous = live
						bridgeCall(t, engine, "resource.release", editor)
					}
					if !bytes.Equal(original, engine.assets[asset].data) {
						t.Fatal("preset editing mutated its imported source asset")
					}
				})
			}
		}
	}
}

func TestSmartWarpPresetBridgeSourceRequiresExplicitPolicyEvenForZeroBend(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, external := range []bool{false, true} {
			for _, style := range []string{"warpArcLower", "warpCylinder"} {
				t.Run(fmt.Sprintf("v%d/external%t/%s", version, external, style), func(t *testing.T) {
					engine, err := New(Config{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
					data, contents := bridgePresetFixture(t, version, external, style)
					asset := bridgeAsset(t, engine, "zero-preset.psd", data)
					opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
					var links map[string]string
					if external {
						links = map[string]string{"warp-source": bridgeAsset(t, engine, "source.psd", contents)}
					}
					for _, operation := range []string{"render.source", "render.tile"} {
						arguments := []any{ref(opened["document"].(string))}
						if operation == "render.tile" {
							arguments = append(arguments, image.Rect(1, 1, 7, 7))
						}
						for _, policy := range []bool{false, true} {
							options := map[string]any{"ApproximateCustomWarp": policy}
							response := bridgeGridRequest(t, engine, operation, "", links, append(arguments, options)...)
							if !policy || style != "warpArcLower" {
								if response.OK || response.Error == nil || len(response.Binaries) != 0 {
									t.Fatalf("%s silently enabled or broadened preset sampling", operation)
								}
								continue
							}
							if !response.OK {
								t.Fatalf("%s rejected explicitly selected zero-bend preset: %+v", operation, response.Error)
							}
							var pixels []byte
							for _, item := range response.Binaries {
								if item.MIME == "application/x-rgba8" {
									pixels = item.Data
								}
							}
							width, x, y := 16, 3, 4
							if operation == "render.tile" {
								width, x, y = 6, 2, 3
							}
							offset := (y*width + x) * 4
							if len(pixels) < offset+4 || !bytes.Equal(pixels[offset:offset+4], []byte{100, 140, 173, 191}) {
								t.Fatal("zero-bend preset did not use independent source UV colors")
							}
						}
					}
				})
			}
		}
	}
}

func TestSmartWarpPresetBridgeUnsupportedIsAtomic(t *testing.T) {
	for _, style := range []string{"warpCylinder", "warpArcLower"} {
		for _, value := range []float64{-100, 100, 0} {
			if style == "warpArcLower" && value == 0 {
				continue
			}
			t.Run(fmt.Sprintf("%s/value%g", style, value), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				data, _ := bridgePresetFixture(t, 1, false, style)
				asset := bridgeAsset(t, engine, "unsupported.psd", data)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
					bridgeCall(t, engine, "editor.SmartObjects", editor))
				before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
				blocked := bridgeGridRequest(t, engine, "editor.SetSmartObjectWarpPreset", editor, nil,
					infos[0].ID, map[string]any{"Value": value},
					map[string]any{"Render": map[string]any{"ApproximateCustomWarp": true}})
				if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
					t.Fatal("unsupported native preset or bend was accepted")
				}
				if after := blendingBridgeResult[psdedit.Changes](t,
					bridgeCall(t, engine, "editor.Changes", editor)); before != after {
					t.Fatal("unsupported preset left partial state")
				}
			})
		}
	}
}
