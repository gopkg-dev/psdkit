package psdweb

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
)

func TestExternalRasterBridgeLiveEditSaveReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original, _ := bridgeSmartFixture(t, version, true)
			inputAsset := bridgeAsset(t, engine, "external.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(inputAsset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
				bridgeCall(t, engine, "editor.SmartObjects", editor))
			colors := []color.NRGBA{{R: 29, G: 83, B: 217, A: 255}, {R: 231, G: 71, B: 43, A: 255}}
			var assets [2]string
			for index, pixel := range colors {
				pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				pixels.SetNRGBA(0, 0, pixel)
				var encoded bytes.Buffer
				if err := png.Encode(&encoded, pixels); err != nil {
					t.Fatal(err)
				}
				assets[index] = bridgeAsset(t, engine, "external.png", encoded.Bytes())
			}
			links := map[string]string{infos[0].SourceID: assets[0]}
			bridgeSmartCall(t, engine, "UpdateLinkedContents", editor, links, infos[0].ID)
			for index, pixel := range colors {
				saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": 3})
				if len(saved.Binaries) != 1 {
					t.Fatal("browser save did not produce one PSD/PSB")
				}
				savedAsset := bridgeAsset(t, engine, "saved.psd", saved.Binaries[0].Data)
				reopened := bridgeCall(t, engine, "document.open", "", assetRef(savedAsset)).Data.(map[string]any)
				document := reopened["document"].(string)
				editor = reopened["editor"].(string)
				bridgeSmartPreview(t, linkedRasterBridgeCall(t, engine, "render.source", links,
					ref(document), map[string]any{}), pixel)
				bridgeSmartPreview(t, linkedRasterBridgeCall(t, engine, "render.tile", links,
					ref(document), image.Rect(0, 0, 2, 1), map[string]any{}), pixel)
				bridgeSmartPreview(t, bridgeCall(t, engine, "preview.saved", "", ref(document)), pixel)
				request := Request{Operation: "render.source"}
				for _, argument := range []any{ref(document), map[string]any{}} {
					encoded, err := json.Marshal(argument)
					if err != nil {
						t.Fatal(err)
					}
					request.Args = append(request.Args, encoded)
				}
				missing := engine.Execute(t.Context(), request, nil)
				if missing.OK || len(missing.Binaries) != 0 || missing.Error.Code != "invalid_request" {
					t.Fatalf("live render reused a previous request mapping or cached external source: %+v", missing)
				}
				if index == 0 {
					links[infos[0].SourceID] = assets[1]
					// The same saved document must use the newly mapped source instead of its saved raster.
					bridgeSmartPreview(t, linkedRasterBridgeCall(t, engine, "render.source", links,
						ref(document), map[string]any{}), colors[1])
					bridgeSmartCall(t, engine, "UpdateLinkedContents", editor, links, infos[1].ID)
				}
			}
			if !bytes.Equal(engine.assets[inputAsset].data, original) {
				t.Fatal("browser live rendering changed the imported source asset")
			}
		})
	}
}

func linkedRasterBridgeCall(
	t *testing.T, engine *Engine, operation string, links map[string]string, arguments ...any,
) Response {
	t.Helper()
	request := Request{Operation: operation, Links: links}
	for _, argument := range arguments {
		encoded, err := json.Marshal(argument)
		if err != nil {
			t.Fatal(err)
		}
		request.Args = append(request.Args, encoded)
	}
	response := engine.Execute(context.Background(), request, nil)
	if !response.OK {
		t.Fatalf("%s: %+v", operation, response.Error)
	}
	return response
}
