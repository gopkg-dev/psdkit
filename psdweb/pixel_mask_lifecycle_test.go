package psdweb

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestPixelMaskLifecycleCatalog(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	catalog := bridgeCall(t, engine, "system.capabilities", "").Data.([]Capability)
	for method, names := range map[string][]string{
		"AddPixelMask":    {"id", "bounds", "plane", "settings"},
		"RemovePixelMask": {"id"},
	} {
		found := false
		for _, capability := range catalog {
			if capability.ID != "editor."+method {
				continue
			}
			found = true
			if capability.TargetKind != "editor" || capability.Description == "" ||
				len(capability.Parameters) != len(names) {
				t.Fatalf("missing pixel-mask browser contract: %+v", capability)
			}
			for index, parameter := range capability.Parameters {
				if parameter.Name != names[index] || parameter.Schema == nil ||
					parameter.Optional != (parameter.Name == "settings") {
					t.Fatalf("pixel-mask parameter contract = %+v", parameter)
				}
			}
		}
		if !found {
			t.Fatalf("editor.%s is missing from the browser catalog", method)
		}
	}
}

func TestPixelMaskLifecycleBridgeSavesAndReopensPSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original := bridgeBlendingFixture(version)
			asset := bridgeAsset(t, engine, "mask.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			gray := color.NRGBA{R: 64, G: 64, B: 64, A: 255}
			white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			var compression uint16
			saveReopen := func(settings *psdedit.PixelMaskSettings, expected color.NRGBA) {
				t.Helper()
				blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", editor), expected)
				saved := bridgeCall(t, engine, "editor.WritePSD", editor,
					map[string]any{"Compression": compression % 4})
				compression++
				if len(saved.Binaries) != 1 || saved.Binaries[0].MIME != "image/vnd.adobe.photoshop" {
					t.Fatal("pixel-mask edit did not export a PSD binary")
				}
				parsed, err := psdkit.ParseDocument(saved.Binaries[0].Data)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = parsed.Close() }()
				if parsed.Header.Version != version || len(parsed.Layers) != 2 ||
					(parsed.Layers[1].Mask != nil) != (settings != nil) {
					t.Fatal("format, layers, or mask presence changed during save")
				}
				maskChannels := 0
				for index, channel := range parsed.Layers[1].Channels {
					if channel.ID != -2 && channel.ID != -3 {
						continue
					}
					maskChannels++
					plane, err := parsed.DecodeChannel(context.Background(), 1, index)
					if err != nil {
						t.Fatal(err)
					}
					if channel.ID != -2 || plane.Width != 1 || plane.Height != 1 ||
						plane.Depth != 8 || !bytes.Equal(plane.Data, []byte{0}) {
						t.Fatalf("unexpected saved user-mask channel: %+v", plane)
					}
				}
				if settings == nil && maskChannels != 0 || settings != nil && maskChannels != 1 {
					t.Fatalf("saved user-mask channel count = %d", maskChannels)
				}
				outputAsset := bridgeAsset(t, engine, "mask-edited.psd", saved.Binaries[0].Data)
				reopened := bridgeCall(t, engine, "document.open", "", assetRef(outputAsset)).Data.(map[string]any)
				editor = reopened["editor"].(string)
				if settings != nil {
					actual := blendingBridgeResult[psdedit.PixelMaskSettings](t,
						bridgeCall(t, engine, "editor.PixelMask", editor, 2))
					if !reflect.DeepEqual(actual, *settings) {
						t.Fatalf("reopened pixel-mask settings = %+v, want %+v", actual, *settings)
					}
				}
				blendingBridgePixel(t, bridgeCall(t, engine, "preview.saved", "",
					ref(reopened["document"].(string))), expected)
				blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", editor), expected)
			}

			bridgeCall(t, engine, "editor.RemovePixelMask", editor, 2)
			bridgeCall(t, engine, "editor.RemovePixelMask", editor, 2)
			saveReopen(nil, gray)
			plane := psdfile.Plane{ID: 99, Width: 1, Height: 1, Depth: 8, RowBytes: 1, Data: []byte{0}}
			bridgeCall(t, engine, "editor.AddPixelMask", editor, 2, image.Rect(0, 0, 1, 1), plane)
			settings := psdedit.PixelMaskSettings{DefaultColor: 255}
			saveReopen(&settings, white)
			settings.Disabled = true
			bridgeCall(t, engine, "editor.SetPixelMask", editor, 2, settings)
			saveReopen(&settings, gray)
			settings.Disabled = false
			bridgeCall(t, engine, "editor.SetPixelMask", editor, 2, settings)
			saveReopen(&settings, white)
			bridgeCall(t, engine, "editor.RemovePixelMask", editor, 2)
			saveReopen(nil, gray)
			bridgeCall(t, engine, "editor.AddPixelMask", editor, 2, image.Rect(0, 0, 1, 1), plane, nil)
			settings.DefaultColor = 0
			saveReopen(&settings, white)
			if !bytes.Equal(original, engine.assets[asset].data) {
				t.Fatal("pixel-mask lifecycle changed immutable source bytes")
			}
		})
	}
}

func TestExistingBridgeOptionalArgumentsRemainCompatible(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
	original := bridgeBlendingFixture(1)
	asset := bridgeAsset(t, engine, "options.psd", original)
	opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
	editor := opened["editor"].(string)
	for _, arguments := range [][]any{nil, {nil}, {map[string]any{}}} {
		saved := bridgeCall(t, engine, "editor.WritePSD", editor, arguments...)
		if len(saved.Binaries) != 1 || !bytes.Equal(saved.Binaries[0].Data, original) {
			t.Fatal("omitted, null, or explicit writer options changed unedited source bytes")
		}
	}
}
