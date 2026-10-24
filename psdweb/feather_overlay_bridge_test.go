package psdweb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// bridgeFeatherOverlayFixture encodes independent wire data with an inactive sampled pixel mask.
func bridgeFeatherOverlayFixture(t *testing.T, version uint16, feather float64) []byte {
	t.Helper()
	control, err := psdfile.Parse(bridgeVectorOverlayFixture(t, version))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	var effects []byte
	for _, block := range control.Layers[0].AdditionalInfo {
		if block.Key == "lfx2" {
			effects, err = control.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(effects) == 0 {
		t.Fatal("independent color-overlay descriptor is absent")
	}
	mask := &psdvector.Mask{Version: 3, Width: 32, Height: 16, HasInitialFill: true}
	path := psdvector.Subpath{Closed: true, Operation: 1, Unknown1: 1}
	for _, point := range []psdvector.Point{{X: 4, Y: 3}, {X: 26, Y: 3}, {X: 26, Y: 13}, {X: 4, Y: 13}} {
		path.Knots = append(path.Knots, psdvector.Knot{Anchor: point, Preceding: point, Leaving: point})
	}
	mask.Subpaths = []psdvector.Subpath{path}
	vector, err := psdvector.EncodeMask(t.Context(), mask)
	if err != nil {
		t.Fatal(err)
	}
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	var record, channels []byte
	for _, coordinate := range []uint32{0, 0, 16, 32} {
		record = binary.BigEndian.AppendUint32(record, coordinate)
	}
	record = binary.BigEndian.AppendUint16(record, 5)
	for _, id := range []int16{0, 1, 2, -1, -2} {
		record = binary.BigEndian.AppendUint16(record, uint16(id))
		record = appendLength(record, 2+32*16)
		value := byte(255)
		if id == -2 {
			value = 37
		}
		channels = append(channels, 0, 0)
		channels = append(channels, bytes.Repeat([]byte{value}, 32*16)...)
	}
	record = append(record, []byte("8BIMnorm\xff\x00\x00\x00")...)
	extra := binary.BigEndian.AppendUint32(nil, 28)
	for _, coordinate := range []uint32{0, 0, 16, 32} {
		extra = binary.BigEndian.AppendUint32(extra, coordinate)
	}
	extra = append(extra, 255, 18, 8)
	extra = binary.BigEndian.AppendUint64(extra, math.Float64bits(feather))
	extra = append(extra, 0)
	extra = binary.BigEndian.AppendUint32(extra, 0)
	extra = append(extra, 1, 'L', 0, 0)
	extra = append(extra, bridgeSmartTag("lfx2", effects, false, 2)...)
	extra = append(extra, bridgeSmartTag("vmsk", vector, false, 2)...)
	extra = append(extra, bridgeSmartTag("pRsv", []byte{0x91, 0x47, 0x2a}, false, 2)...)
	record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
	record = append(record, extra...)
	layers := append(binary.BigEndian.AppendUint16(nil, 0xffff), record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	section := append(appendLength(nil, len(layers)), layers...)
	section = binary.BigEndian.AppendUint32(section, 0)
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 4)
	header = binary.BigEndian.AppendUint32(header, 16)
	header = binary.BigEndian.AppendUint32(header, 32)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = append(header, make([]byte, 8)...)
	header = appendLength(header, len(section))
	header = append(header, section...)
	header = append(header, 0, 0)
	return append(header, bytes.Repeat([]byte{23}, 4*32*16)...)
}

func bridgeFeatherOverlayPixels(t *testing.T, response Response) []byte {
	t.Helper()
	for _, item := range response.Binaries {
		if item.MIME == "application/x-rgba8" && len(item.Data) == 32*16*4 {
			return item.Data
		}
	}
	t.Fatal("feather overlay omitted its full straight RGBA buffer")
	return nil
}

func TestFeatherColorOverlayBridgeEditSaveReopenAndContinue(t *testing.T) {
	// These alpha samples reuse the independently retained native rectangle controls, not the compositor's blur.
	points := []image.Point{image.Pt(3, 8), image.Pt(4, 8), image.Pt(15, 8), image.Pt(9, 7)}
	for _, control := range []struct {
		radius float64
		alpha  []byte
	}{
		{.5, []byte{56, 199, 255, 255}},
		{1.5, []byte{95, 160, 255, 255}},
		{3, []byte{101, 130, 231, 224}},
	} {
		for _, version := range []uint16{1, 2} {
			for compression := range 4 {
				t.Run(fmt.Sprintf("radius%g/v%d/c%d", control.radius, version, compression), func(t *testing.T) {
					engine, err := New(Config{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
					original := bridgeFeatherOverlayFixture(t, version, control.radius)
					asset := bridgeAsset(t, engine, "feather-overlay.psd", original)
					opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
					current := original
					for _, rgb := range [][3]byte{{60, 180, 220}, {20, 220, 100}} {
						editor := opened["editor"].(string)
						set := bridgeVectorOverlaySet(t, current)
						for channel, component := range rgb {
							set.Items[0].Color[channel] = float64(component) / 255
						}
						bridgeCall(t, engine, "editor.SetEffects", editor, 1, set)
						live := bridgeFeatherOverlayPixels(t, bridgeCall(t, engine, "editor.Composite", editor))
						for index, point := range points {
							offset := (point.Y*32 + point.X) * 4
							if math.Abs(float64(live[offset+3])-float64(control.alpha[index])) > 2 ||
								!bytes.Equal(live[offset:offset+3], rgb[:]) {
								t.Fatalf("native feather/overlay control at %v: actual=%v", point, live[offset:offset+4])
							}
						}
						saved := bridgeCall(t, engine, "editor.WritePSD", editor,
							map[string]any{"Compression": compression})
						if len(saved.Binaries) != 1 {
							t.Fatal("automatic feather-overlay save did not return document bytes")
						}
						current = saved.Binaries[0].Data
						verifyBridgeFeatherOverlaySaved(t, current, live, version, control.radius)
						output := bridgeAsset(t, engine, "feather-overlay-edited.psd", current)
						opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
						reopened := bridgeFeatherOverlayPixels(t,
							bridgeCall(t, engine, "editor.Composite", opened["editor"].(string)))
						if !bytes.Equal(live, reopened) {
							t.Fatal("JSON bridge changed live feather/overlay pixels after reopening")
						}
					}
					if !bytes.Equal(original, engine.assets[asset].data) {
						t.Fatal("feather overlay mutated its imported source asset")
					}
				})
			}
		}
	}
}

func verifyBridgeFeatherOverlaySaved(t *testing.T, data, live []byte, version uint16, radius float64) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	mask := source.Layers[0].Mask
	if source.Header.Version != version || mask == nil || mask.VectorFeather == nil ||
		*mask.VectorFeather != radius || mask.Flags&2 == 0 {
		t.Fatal("JSON effect edit changed format, vector feather or disabled pixel-mask metadata")
	}
	for index, channel := range source.Layers[0].Channels {
		if channel.ID != -2 {
			continue
		}
		plane, err := source.DecodeChannel(t.Context(), 0, index)
		if err != nil || !bytes.Equal(plane.Data, bytes.Repeat([]byte{37}, 32*16)) {
			t.Fatalf("automatic save changed inactive pixel-mask samples: %v", err)
		}
	}
	scene, err := psdscene.Render(t.Context(), source)
	if err != nil || !bytes.Equal(scene.Pix, live) {
		t.Fatalf("source reconstruction differs from current editor feather/overlay: %v", err)
	}
	region := image.Rect(2, 2, 12, 10)
	tile, err := psdscene.RenderTile(t.Context(), source, region)
	if err != nil {
		t.Fatal(err)
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if tile.NRGBAAt(x, y) != scene.NRGBAAt(x, y) {
				t.Fatal("source region lost feather samples outside its output bounds")
			}
		}
	}
	merged, err := source.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("saved automatic composite is missing: %v", err)
	}
	for index := range 32 * 16 {
		alpha := live[index*4+3]
		if merged.Channels[3].Data[index] != alpha {
			t.Fatal("automatic saved alpha omitted the current feather coverage")
		}
		for channel := range 3 {
			value := live[index*4+channel]
			expected := byte(math.RoundToEven(float64(value)*float64(alpha)/255 + 255 - float64(alpha)))
			if merged.Channels[channel].Data[index] != expected {
				t.Fatal("saved white-matted RGB does not match the edited live composite")
			}
		}
	}
}

func TestFeatherColorOverlayBridgeRejectsUnsupportedMasksWithoutMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		radius float64
		active bool
	}{{"uncalibrated-radius", 2, false}, {"active-pixel-mask", 1.5, true}} {
		t.Run(test.name, func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original := bridgeFeatherOverlayFixture(t, 1, test.radius)
			if test.active {
				source, err := psdfile.Parse(original)
				if err != nil {
					t.Fatal(err)
				}
				original[source.Layers[0].Mask.Span.Offset+17] &^= 2
				_ = source.Close()
			}
			asset := bridgeAsset(t, engine, "unsupported-feather-overlay.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
			set := bridgeVectorOverlaySet(t, original)
			set.Items[0].Color = [3]float64{0, 0, 1}
			response := bridgeShadowVectorExecute(t, engine, "editor.SetEffects", editor, 1, set)
			if response.OK || response.Error == nil || response.Error.Code != "unsupported" ||
				len(response.Binaries) != 0 {
				t.Fatalf("unsupported feather combination accepted by JSON bridge: %+v", response.Error)
			}
			after := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
			if before != after {
				t.Fatal("rejected feather overlay committed partial editor changes")
			}
			saved := bridgeCall(t, engine, "editor.WritePSD", editor)
			if len(saved.Binaries) != 1 || !bytes.Equal(saved.Binaries[0].Data, original) {
				t.Fatal("rejected feather edit changed the unchanged saved source")
			}
		})
	}
}
