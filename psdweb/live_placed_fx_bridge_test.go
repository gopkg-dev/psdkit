package psdweb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

// bridgeLivePlacedFXFixture combines independent placement/effect wire records and poisons parent artwork.
func bridgeLivePlacedFXFixture(t *testing.T, version uint16, external bool) ([]byte, []byte) {
	t.Helper()
	data, contents := bridgeSmartFixture(t, version, external)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	control, err := psdfile.Parse(bridgeShadowVectorFixture(t, version, true))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	var tags []byte
	for _, block := range control.Layers[0].AdditionalInfo {
		if block.Key == "lfx2" {
			payload, err := control.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			tags = append(tags, bridgeSmartTag("lfx2", payload, false, 2)...)
		}
	}
	if len(tags) == 0 {
		t.Fatal("placed FX fixture has no independent effects")
	}
	tags = append(tags, bridgeSmartTag("pEvd", []byte{0x47, 0x31, 0x86}, false, 2)...)
	for _, record := range source.Layers {
		for _, channel := range record.Channels {
			value := byte(255)
			if channel.ID == 1 {
				value = 0
			}
			for offset := channel.Data.Offset; offset < channel.Data.Offset+channel.Data.Length; offset++ {
				data[offset] = value
			}
		}
	}
	binary.BigEndian.PutUint16(data[12:], 4)
	binary.BigEndian.PutUint16(data[source.Layers[0].Span.Offset-2:], 0xfffe)
	data = append(data, 255, 255)
	for _, record := range slices.Backward(source.Layers) {
		for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
			if version == 2 {
				binary.BigEndian.PutUint64(data[offset:], binary.BigEndian.Uint64(data[offset:])+uint64(len(tags)))
			} else {
				binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(len(tags)))
			}
		}
		binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length)+uint32(len(tags)))
		position := record.Extra.Offset + record.Extra.Length
		result := append(bytes.Clone(data[:position]), tags...)
		data = append(result, data[position:]...)
	}
	return data, contents
}

func bridgeLivePlacedFXSet(t *testing.T, data []byte, index int) *psdeffects.Set {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, block := range source.Layers[index].AdditionalInfo {
		if block.Key == "lfx2" {
			set, err := source.DecodeEffects(t.Context(), block)
			if err != nil {
				t.Fatal(err)
			}
			return set
		}
	}
	t.Fatal("placed FX descriptor is absent")
	return nil
}

func bridgeLivePlacedFXPixels(t *testing.T, response Response) []byte {
	t.Helper()
	for _, item := range response.Binaries {
		if item.MIME == "application/x-rgba8" && len(item.Data) == 8 {
			return item.Data
		}
	}
	t.Fatal("placed FX did not return the full parent RGBA output")
	return nil
}

func verifyBridgeLivePlacedFXSaved(
	t *testing.T, data, contents, live []byte, version uint16, expected color.NRGBA,
) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Header.Version != version {
		t.Fatal("placed FX changed PSD/PSB format")
	}
	linked, err := source.LinkedFiles(t.Context())
	if err != nil || len(linked) != 1 || linked[0].Kind != "liFD" {
		t.Fatalf("placed FX source identity was not retained: %v", err)
	}
	encoded, err := source.ReadSpan(linked[0].Data)
	if err != nil || !bytes.Equal(encoded, contents) {
		t.Fatal("placed FX changed or lost current shared source bytes")
	}
	for index, record := range source.Layers {
		pixels, err := source.DecodeLayer(t.Context(), index)
		if err != nil {
			t.Fatal(err)
		}
		point := image.Pt(index, 0)
		if !point.In(pixels.Bounds) {
			t.Fatal("current projected source was cropped out of its saved layer")
		}
		position := point.X - pixels.Bounds.Min.X + (point.Y-pixels.Bounds.Min.Y)*pixels.Bounds.Dx()
		for _, channel := range pixels.Channels {
			component := int(channel.ID)
			if channel.ID == -1 {
				component = 3
			} else if component < 0 || component > 2 {
				continue
			}
			if channel.Data[position] != [4]byte{expected.R, expected.G, expected.B, expected.A}[component] {
				t.Fatalf("placed FX baked styling or used stale source in layer %d channel %d", index, channel.ID)
			}
		}
		preserved := false
		for _, block := range record.AdditionalInfo {
			if block.Key == "pEvd" {
				payload, err := source.ReadSpan(block.Data)
				if err != nil || !bytes.Equal(payload, []byte{0x47, 0x31, 0x86}) {
					t.Fatal("placed FX rewrote an unknown source block")
				}
				preserved = true
			}
		}
		if !preserved {
			t.Fatal("placed FX discarded an unknown source block")
		}
	}
	merged, err := source.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("placed FX did not save its new merged preview: %v", err)
	}
	for index := range 2 {
		alpha := live[index*4+3]
		if merged.Channels[3].Data[index] != alpha {
			t.Fatal("saved placed FX alpha does not match current live output")
		}
		for channel := range 3 {
			value := live[index*4+channel]
			expected := byte(math.RoundToEven(float64(value)*float64(alpha)/255 + 255 - float64(alpha)))
			if merged.Channels[channel].Data[index] != expected {
				t.Fatal("saved placed FX RGB does not match the current white-matted preview")
			}
		}
	}
	scene, err := psdscene.Render(t.Context(), source,
		psdscene.Options{Compose: psdcompose.Options{ApproximateDropShadow: true}})
	if err != nil || !bytes.Equal(scene.Pix, live) {
		t.Fatalf("saved placed FX source reconstruction changed live pixels: %v", err)
	}
}

func TestLivePlacedFXBridgeEditReplaceSaveReopenContinue(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range 4 {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				original, _ := bridgeLivePlacedFXFixture(t, version, false)
				asset := bridgeAsset(t, engine, "placed-FX.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				current := original
				policy := map[string]any{"ApproximateDropShadow": true}
				for round, ink := range []color.NRGBA{{R: 45, G: 130, B: 210, A: 153}, {R: 230, G: 60, B: 30, A: 204}} {
					editor := opened["editor"].(string)
					infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
						bridgeCall(t, engine, "editor.SmartObjects", editor))
					if len(infos) != 2 || len(infos[0].SharedInstances) != 2 {
						t.Fatal("placed FX lost its two shared instances")
					}
					id := infos[0].ID
					pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
					pixels.SetNRGBA(0, 0, ink)
					contents := bridgeFilterContents(t, pixels, round == 0)
					replacement := bridgeAsset(t, engine, "replacement", contents)
					bridgeCall(t, engine, "editor.ReplaceContents", editor, id, assetRef(replacement))
					if round == 0 {
						bridgeCall(t, engine, "editor.AddPixelMask", editor, id, infos[0].Bounds,
							psdfile.Plane{Depth: 8, Width: 1, Height: 1, RowBytes: 1, Data: []byte{137}},
							psdedit.PixelMaskSettings{})
					}
					bridgeCall(t, engine, "editor.SetAdvancedBlending", editor, id,
						psdedit.AdvancedBlendingOptions{MaskAsGlobal: new(round == 1), BlendInterior: new(round == 1)})
					bridgeCall(t, engine, "editor.SetOpacity", editor, id, 173)
					bridgeCall(t, engine, "editor.SetFillOpacity", editor, id, 199)
					set := bridgeLivePlacedFXSet(t, current, 0)
					set.Items[0].Distance, set.Items[0].Size = .25+float64(round)*.5, .5
					set.Items[0].Opacity, set.Items[0].Color = .5, [3]float64{.2, .4, .8}
					set.Items[1].Opacity, set.Items[1].Color = .5, [3]float64{.8, .2, .6}
					bridgeShadowVectorRejectsPolicy(t, engine, "editor.SetEffectsWithOptions", editor, id, set,
						map[string]any{})
					bridgeCall(t, engine, "editor.SetEffectsWithOptions", editor, id, set, policy)
					bridgeShadowVectorRejectsPolicy(t, engine, "editor.Composite", editor)
					bridgeShadowVectorRejectsPolicy(t, engine, "editor.WritePSD", editor,
						map[string]any{"Compression": compression})
					// Save directly after editing, before a caller requests or supplies a composite.
					saved := bridgeCall(t, engine, "editor.WritePSD", editor,
						map[string]any{"Compression": compression, "ApproximateDropShadow": true})
					if len(saved.Binaries) != 1 {
						t.Fatal("placed FX edit did not produce PSD/PSB bytes")
					}
					live := bytes.Clone(bridgeLivePlacedFXPixels(t, bridgeCall(t, engine, "editor.Composite", editor, policy)))
					current = saved.Binaries[0].Data
					verifyBridgeLivePlacedFXSaved(t, current, contents, live, version, ink)
					persisted, peer := bridgeLivePlacedFXSet(t, current, 0), bridgeLivePlacedFXSet(t, current, 1)
					if persisted.Items[0].Distance != set.Items[0].Distance ||
						persisted.Items[1].Color != set.Items[1].Color || peer.Items[0].Distance == set.Items[0].Distance {
						t.Fatal("placed FX lost current parameters or changed the shared instance's independent effects")
					}
					output := bridgeAsset(t, engine, "placed-FX-edited.psd", current)
					opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
					reopened := bridgeLivePlacedFXPixels(t,
						bridgeCall(t, engine, "editor.Composite", opened["editor"].(string), policy))
					if !bytes.Equal(live, reopened) {
						t.Fatal("placed FX changed pixels after reopening")
					}
					bridgeCall(t, engine, "resource.release", editor)
				}
				if !bytes.Equal(original, engine.assets[asset].data) {
					t.Fatal("placed FX mutated its imported source asset")
				}
			})
		}
	}
}

func TestLivePlacedFXBridgeExternalMappingAndEmbedBeforeAutomaticSave(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range 4 {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				original, contents := bridgeLivePlacedFXFixture(t, version, true)
				asset := bridgeAsset(t, engine, "external-placed-FX.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
					bridgeCall(t, engine, "editor.SmartObjects", editor))
				id := infos[0].ID
				links := map[string]string{infos[0].SourceID: bridgeAsset(t, engine, "external-source.psd", contents)}
				set := bridgeLivePlacedFXSet(t, original, 0)
				set.Items[0].Distance, set.Items[0].Size, set.Items[0].Opacity = .25, .5, .5
				set.Items[1].Color = [3]float64{.8, .2, .6}
				options := map[string]any{"ApproximateDropShadow": true, "Render": map[string]any{}}
				before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
				blocked := bridgeContentsFilterRequest(t, engine, "SetEffectsWithOptions", editor, id, set, options)
				if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
					t.Fatal("external placed FX accepted no source mapping or returned partial artwork")
				}
				if after := blendingBridgeResult[psdedit.Changes](t,
					bridgeCall(t, engine, "editor.Changes", editor)); after != before {
					t.Fatal("missing external source mapping partially committed effects")
				}
				bridgeSmartCall(t, engine, "SetEffectsWithOptions", editor, links, id, set, options)
				bridgeShadowVectorRejectsPolicy(t, engine, "editor.WritePSD", editor,
					map[string]any{"Compression": compression, "ApproximateDropShadow": true})
				// Automatic saves never retain an earlier resolver; explicitly embed before entering the strict path.
				bridgeSmartCall(t, engine, "EmbedLinkedContentsWithOptions", editor, links, id,
					psdedit.SmartContentsOptions{})
				policy := map[string]any{"ApproximateDropShadow": true}
				for round := range 2 {
					set.Items[0].Distance = .25 + float64(round)*.5
					bridgeCall(t, engine, "editor.SetEffectsWithOptions", editor, id, set, policy)
					saved := bridgeCall(t, engine, "editor.WritePSD", editor,
						map[string]any{"Compression": compression, "ApproximateDropShadow": true})
					if len(saved.Binaries) != 1 {
						t.Fatal("embedded placed FX did not save automatically")
					}
					live := bridgeLivePlacedFXPixels(t, bridgeCall(t, engine, "editor.Composite", editor, policy))
					verifyBridgeLivePlacedFXSaved(t, saved.Binaries[0].Data, contents, live, version,
						color.NRGBA{R: 120, G: 120, B: 120, A: 255})
					output := bridgeAsset(t, engine, "embedded-placed-FX.psd", saved.Binaries[0].Data)
					opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
					bridgeCall(t, engine, "resource.release", editor)
					editor = opened["editor"].(string)
					infos = blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
						bridgeCall(t, engine, "editor.SmartObjects", editor))
					id = infos[0].ID
				}
			})
		}
	}
}
