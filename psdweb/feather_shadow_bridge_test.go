package psdweb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// bridgeFeatherShadowFixture combines independent wire fixtures without rendering or saving a document.
func bridgeFeatherShadowFixture(t *testing.T, version uint16, radius float64, mixed bool) []byte {
	t.Helper()
	shadow, err := psdfile.Parse(bridgeShadowVectorFixture(t, version, mixed))
	if err != nil {
		t.Fatal(err)
	}
	defer shadow.Close()
	var effects []byte
	for _, block := range shadow.Layers[0].AdditionalInfo {
		if block.Key == "lfx2" {
			effects, err = shadow.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(effects) == 0 {
		t.Fatal("independent shadow descriptor is absent")
	}
	data := bridgeFeatherOverlayFixture(t, version, radius)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	record := source.Layers[0]
	for _, channel := range record.Channels {
		value := map[int16]byte{0: 200, 1: 100, 2: 40, -1: 127, -2: 37}[channel.ID]
		for offset := channel.Data.Offset; offset < channel.Data.Offset+channel.Data.Length; offset++ {
			data[offset] = value
		}
	}
	for _, block := range record.AdditionalInfo {
		if block.Key != "lfx2" {
			continue
		}
		replacement := bridgeSmartTag("lfx2", effects, false, 2)
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
		return append(result, data[block.Span.Offset+block.Span.Length:]...)
	}
	t.Fatal("independent feather fixture has no effects")
	return nil
}

func verifyBridgeFeatherShadowSaved(
	t *testing.T, data, live []byte, version uint16, radius float64, dual bool,
) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	mask := source.Layers[0].Mask
	if source.Header.Version != version || mask == nil || mask.VectorFeather == nil ||
		*mask.VectorFeather != radius || (mask.Flags&2 != 0) != !dual {
		t.Fatal("feather-shadow JSON edit changed format or current mask settings")
	}
	for index, channel := range source.Layers[0].Channels {
		value, exists := map[int16]byte{0: 200, 1: 100, 2: 40, -1: 127, -2: 37}[channel.ID]
		plane, err := source.DecodeChannel(t.Context(), 0, index)
		if !exists || err != nil || !bytes.Equal(plane.Data, bytes.Repeat([]byte{value}, 32*16)) {
			t.Fatalf("feather-shadow baked effects or masks into original channel %d: %v", channel.ID, err)
		}
	}
	preserved := false
	for _, block := range source.Layers[0].AdditionalInfo {
		if block.Key != "pRsv" {
			continue
		}
		payload, err := source.ReadSpan(block.Data)
		if err != nil || !bytes.Equal(payload, []byte{0x91, 0x47, 0x2a}) {
			t.Fatal("feather-shadow rewrote an unrelated original block")
		}
		preserved = true
	}
	if !preserved {
		t.Fatal("feather-shadow lost the unrelated original block")
	}
	policy := psdscene.Options{Compose: psdcompose.Options{ApproximateDropShadow: true}}
	if pixels, err := psdscene.Render(t.Context(), source); pixels != nil ||
		!errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("saved feather-shadow implicitly enabled approximation: %v", err)
	}
	full, err := psdscene.Render(t.Context(), source, policy)
	if err != nil || !bytes.Equal(full.Pix, live) {
		t.Fatalf("feather-shadow source reconstruction changed pixels: %v", err)
	}
	region := image.Rect(2, 2, 12, 10)
	tile, err := psdscene.RenderTile(t.Context(), source, region, psdscene.TileOptions{Compose: policy.Compose})
	if err != nil {
		t.Fatal(err)
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if tile.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
				t.Fatal("feather-shadow ROI lost coverage outside the requested output")
			}
		}
	}
	merged, err := source.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("feather-shadow did not save its updated merged preview: %v", err)
	}
	for index := range 32 * 16 {
		alpha := live[index*4+3]
		if merged.Channels[3].Data[index] != alpha {
			t.Fatal("saved feather-shadow alpha differs from current live output")
		}
		for channel := range 3 {
			value := live[index*4+channel]
			expected := byte(math.RoundToEven(float64(value)*float64(alpha)/255 + 255 - float64(alpha)))
			if merged.Channels[channel].Data[index] != expected {
				t.Fatal("saved feather-shadow RGB differs from white-matted live output")
			}
		}
	}
}

func TestFeatherShadowBridgeExplicitPolicyEditSaveReopenContinue(t *testing.T) {
	radii := []float64{.5, 1.5, 3}
	for _, version := range []uint16{1, 2} {
		for compression := range 4 {
			for radiusIndex, initial := range radii {
				for _, dual := range []bool{false, true} {
					for _, mixed := range []bool{false, true} {
						name := fmt.Sprintf("v%d/c%d/r%g/dual%t/overlay%t", version, compression, initial, dual, mixed)
						t.Run(name, func(t *testing.T) {
							engine, err := New(Config{})
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
							original := bridgeFeatherShadowFixture(t, version, initial, mixed)
							asset := bridgeAsset(t, engine, "feather-shadow.psd", original)
							opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
							policy := map[string]any{"ApproximateDropShadow": true}
							current := original
							for round := range 2 {
								editor := opened["editor"].(string)
								radius := radii[(radiusIndex+round)%len(radii)]
								settings := blendingBridgeResult[psdedit.PixelMaskSettings](t,
									bridgeCall(t, engine, "editor.PixelMask", editor, 1))
								settings.Disabled, settings.Invert = !dual, round == 1
								settings.Density, settings.Feather = new(uint8(137)), new(radii[round])
								settings.VectorFeather, settings.VectorDensity = new(radius), new(uint8(149+round*53))
								bridgeCall(t, engine, "editor.SetPixelMask", editor, 1, settings)
								bridgeCall(t, engine, "editor.SetAdvancedBlending", editor, 1,
									psdedit.AdvancedBlendingOptions{MaskAsGlobal: new(round == 1),
										VectorMaskAsGlobal: new(round == 0), BlendInterior: new(round == 1)})
								bridgeCall(t, engine, "editor.SetOpacity", editor, 1, 173)
								bridgeCall(t, engine, "editor.SetFillOpacity", editor, 1, 199)
								if round == 1 {
									mask := &psdvector.Mask{Version: 3, Width: 32, Height: 16, HasInitialFill: true,
										Invert: true, NotLinked: true}
									path := psdvector.Subpath{Closed: true, Operation: 1, Unknown1: 1}
									for _, point := range []psdvector.Point{{X: -4, Y: -2}, {X: 24, Y: -2},
										{X: 24, Y: 12}, {X: -4, Y: 12}} {
										path.Knots = append(path.Knots,
											psdvector.Knot{Anchor: point, Preceding: point, Leaving: point})
									}
									mask.Subpaths = []psdvector.Subpath{path}
									bridgeCall(t, engine, "editor.SetVectorMask", editor, 1, mask)
								}
								set := bridgeVectorOverlaySet(t, current)
								set.Items[0].Distance, set.Items[0].Size = 1.25+float64(round), 1.5
								set.Items[0].Opacity, set.Items[0].Color = .5, [3]float64{.2, .4, .8}
								if mixed {
									set.Items[1].Opacity, set.Items[1].Color = .5, [3]float64{.8, .2, .6}
								}
								bridgeShadowVectorRejectsPolicy(t, engine, "editor.SetEffects", editor, 1, set)
								bridgeShadowVectorRejectsPolicy(t, engine, "editor.SetEffectsWithOptions", editor, 1, set,
									map[string]any{})
								bridgeCall(t, engine, "editor.SetEffectsWithOptions", editor, 1, set, policy)
								bridgeShadowVectorRejectsPolicy(t, engine, "editor.Composite", editor)
								bridgeShadowVectorRejectsPolicy(t, engine, "editor.WritePSD", editor,
									map[string]any{"Compression": compression})
								live := bytes.Clone(bridgeFeatherOverlayPixels(t,
									bridgeCall(t, engine, "editor.Composite", editor, policy)))
								saved := bridgeCall(t, engine, "editor.WritePSD", editor,
									map[string]any{"Compression": compression, "ApproximateDropShadow": true})
								if len(saved.Binaries) != 1 {
									t.Fatal("automatic feather-shadow save did not produce document bytes")
								}
								current = saved.Binaries[0].Data
								verifyBridgeFeatherShadowSaved(t, current, live, version, radius, dual)
								output := bridgeAsset(t, engine, "feather-shadow-edited.psd", current)
								opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
								reopened := bridgeFeatherOverlayPixels(t,
									bridgeCall(t, engine, "editor.Composite", opened["editor"].(string), policy))
								if !bytes.Equal(live, reopened) {
									t.Fatal("feather-shadow pixels changed after reopen")
								}
								persisted := bridgeVectorOverlaySet(t, current)
								if persisted.Items[0].Distance != set.Items[0].Distance ||
									persisted.Items[0].Size != set.Items[0].Size {
									t.Fatal("saved feather-shadow lost edited parameters")
								}
								bridgeCall(t, engine, "resource.release", editor)
							}
							if !bytes.Equal(engine.assets[asset].data, original) {
								t.Fatal("feather-shadow mutated the imported original asset")
							}
						})
					}
				}
			}
		}
	}
}
