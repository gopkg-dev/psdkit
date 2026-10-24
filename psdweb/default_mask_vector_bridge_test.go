package psdweb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

// bridgeDefaultMaskVectorFixture writes one user mask header without any encoded mask sample channel.
func bridgeDefaultMaskVectorFixture(version uint16) []byte {
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	var record, channels []byte
	for _, coordinate := range []uint32{0, 0, 1, 1} {
		record = binary.BigEndian.AppendUint32(record, coordinate)
	}
	record = binary.BigEndian.AppendUint16(record, 4)
	for index, id := range []int16{0, 1, 2, -1} {
		record = binary.BigEndian.AppendUint16(record, uint16(id))
		record = appendLength(record, 3)
		channels = append(channels, 0, 0, []byte{180, 90, 30, 255}[index])
	}
	record = append(record, []byte("8BIMnorm\xff\x00\x00\x00")...)
	extra := binary.BigEndian.AppendUint32(nil, 20)
	for _, coordinate := range []uint32{0, 0, 1, 1} {
		extra = binary.BigEndian.AppendUint32(extra, coordinate)
	}
	extra = append(extra, 64, 0, 0xa1, 0xb2)
	extra = binary.BigEndian.AppendUint32(extra, 0)
	extra = append(extra, 1, 'L', 0, 0)
	extra = append(extra, bridgeSmartTag("pRsv", []byte{0x91, 0x47}, false, 2)...)
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
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = append(header, make([]byte, 8)...)
	header = appendLength(header, len(section))
	header = append(header, section...)
	return append(header, 0, 0, 23, 23, 23, 23)
}

func TestDefaultCoverageMaskVectorBridgeSaveReopenAndContinue(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range 4 {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				original := bridgeDefaultMaskVectorFixture(version)
				asset := bridgeAsset(t, engine, "default-mask.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				bridgeCall(t, engine, "editor.AddVectorMask", editor, 1, bridgeCompletionVectorMask(t))
				for index, step := range []struct {
					settings psdedit.PixelMaskSettings
					alpha    byte
					vector   bool
				}{
					{psdedit.PixelMaskSettings{DefaultColor: 64}, 32, true},
					{psdedit.PixelMaskSettings{DefaultColor: 96}, 48, true},
					{psdedit.PixelMaskSettings{DefaultColor: 64, Invert: true, Density: new(uint8(128)),
						Feather: new(float64(3)), Relative: true}, 112, true},
					{psdedit.PixelMaskSettings{DefaultColor: 64, Disabled: true}, 128, true},
					{psdedit.PixelMaskSettings{DefaultColor: 96}, 96, false},
					{psdedit.PixelMaskSettings{DefaultColor: 64}, 32, true},
				} {
					switch index {
					case 4:
						bridgeCall(t, engine, "editor.RemoveVectorMask", editor, 1)
					case 5:
						bridgeCall(t, engine, "editor.AddVectorMask", editor, 1, bridgeCompletionVectorMask(t))
					}
					bridgeCall(t, engine, "editor.SetPixelMask", editor, 1, step.settings)
					settings := blendingBridgeResult[psdedit.PixelMaskSettings](t,
						bridgeCall(t, engine, "editor.PixelMask", editor, 1))
					if !reflect.DeepEqual(settings, step.settings) {
						t.Fatalf("current default-only mask settings changed: %+v", settings)
					}
					expected := color.NRGBA{R: 180, G: 90, B: 30, A: step.alpha}
					bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "editor.Composite", editor), expected)
					saved := bridgeCall(t, engine, "editor.WritePSD", editor,
						map[string]any{"Compression": compression})
					if len(saved.Binaries) != 1 {
						t.Fatal("default-only dual mask did not export a document")
					}
					verifyBridgeDefaultMaskVectorSaved(t, saved.Binaries[0].Data, expected,
						version, step.vector)
					output := bridgeAsset(t, engine, "default-mask-saved.psd", saved.Binaries[0].Data)
					opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
					editor = opened["editor"].(string)
					settings = blendingBridgeResult[psdedit.PixelMaskSettings](t,
						bridgeCall(t, engine, "editor.PixelMask", editor, 1))
					if !reflect.DeepEqual(settings, step.settings) {
						t.Fatal("reopened default-only user mask settings differ")
					}
					bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "editor.Composite", editor), expected)
					bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "preview.saved", "",
						ref(opened["document"].(string))), bridgeVectorOverlaySavedPixel(expected))
				}
				bridgeCall(t, engine, "editor.RemovePixelMask", editor, 1)
				bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "editor.Composite", editor),
					color.NRGBA{R: 180, G: 90, B: 30, A: 128})
				removed := bridgeCall(t, engine, "editor.WritePSD", editor,
					map[string]any{"Compression": compression})
				if len(removed.Binaries) != 1 {
					t.Fatal("removing a default-only user mask did not export the remaining vector")
				}
				output := bridgeAsset(t, engine, "default-mask-removed.psd", removed.Binaries[0].Data)
				opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
				editor = opened["editor"].(string)
				response := bridgeShadowVectorExecute(t, engine, "editor.PixelMask", editor, 1)
				if response.OK || response.Error == nil || response.Error.Code != "unsupported" {
					t.Fatal("generated vector carrier was misclassified as a default-only user mask")
				}
				bridgeVectorOverlayPreview(t, bridgeCall(t, engine, "editor.Composite", editor),
					color.NRGBA{R: 180, G: 90, B: 30, A: 128})
				if !bytes.Equal(original, engine.assets[asset].data) {
					t.Fatal("default-only dual-mask edit mutated original imported bytes")
				}
			})
		}
	}
}

func verifyBridgeDefaultMaskVectorSaved(
	t *testing.T, data []byte, expected color.NRGBA, version uint16, vector bool,
) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if source.Header.Version != version || len(source.Layers) != 1 {
		t.Fatal("default-only mask edit changed document format or layer count")
	}
	record := source.Layers[0]
	for _, channel := range record.Channels {
		if channel.ID == -2 || channel.ID == -3 {
			t.Fatal("default-only mask acquired fabricated sample channels")
		}
	}
	if record.Mask == nil || record.Mask.Bounds != image.Rect(0, 0, 1, 1) || record.Mask.Flags&8 != 0 {
		t.Fatal("ordinary default-only user mask metadata was replaced by generated coverage")
	}
	raw, err := source.ReadSpan(record.MaskData)
	if err != nil || !bytes.Equal(raw[len(raw)-2:], []byte{0xa1, 0xb2}) {
		t.Fatalf("default mask edit lost opaque metadata tail: %v", err)
	}
	vectors := 0
	for _, block := range record.AdditionalInfo {
		if block.Key == "vmsk" || block.Key == "vsms" {
			vectors++
		}
	}
	if vector && vectors != 1 || !vector && vectors != 0 {
		t.Fatal("saved vector presence differs from current lifecycle state")
	}
	snapshot, err := source.Decode(t.Context())
	if err != nil || snapshot.Layers[0].Mask == nil || snapshot.Layers[0].Mask.Pixels != nil {
		t.Fatalf("complete decoding lost default-only user coverage or invented pixels: %v", err)
	}
	full, err := psdscene.Render(t.Context(), source)
	if err != nil || full.NRGBAAt(0, 0) != expected {
		t.Fatalf("source full reconstruction changed default/vector coverage: %v", err)
	}
	tile, err := psdscene.RenderTile(t.Context(), source, image.Rect(0, 0, 1, 1))
	if err != nil || tile.NRGBAAt(0, 0) != expected {
		t.Fatalf("source region reconstruction changed default/vector coverage: %v", err)
	}
}
