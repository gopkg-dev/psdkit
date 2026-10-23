package psdweb

import (
	"bytes"
	"fmt"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
)

func TestLZWRasterBridgeEditSaveReopenRealtime(t *testing.T) {
	contents, err := os.ReadFile("../psdfile/testdata/rgba-lzw-small.tif")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint16{1, 2} {
		for _, external := range []bool{false, true} {
			t.Run(fmt.Sprintf("v%d/external%t", version, external), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				input, _ := bridgeSmartFixture(t, version, external)
				asset := bridgeAsset(t, engine, "parent.psd", input)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
					bridgeCall(t, engine, "editor.SmartObjects", editor))
				raster := bridgeAsset(t, engine, "source.tif", contents)
				links := map[string]string{infos[0].SourceID: raster}
				if external {
					bridgeSmartCall(t, engine, "UpdateLinkedContents", editor, links, infos[0].ID)
				} else {
					bridgeCall(t, engine, "editor.ReplaceContents", editor, infos[0].ID, assetRef(raster))
				}
				for _, opacity := range []byte{255, 128} {
					bridgeCall(t, engine, "editor.SetOpacity", editor, infos[0].ID, opacity)
					expected := bridgeCall(t, engine, "editor.Composite", editor, map[string]any{})
					saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": 3})
					if len(saved.Binaries) != 1 || len(expected.Binaries) == 0 {
						t.Fatal("LZW edit did not return saved bytes and a preview")
					}
					written := bridgeAsset(t, engine, "saved.psd", saved.Binaries[0].Data)
					reopened := bridgeCall(t, engine, "document.open", "", assetRef(written)).Data.(map[string]any)
					editor = reopened["editor"].(string)
					live := linkedRasterBridgeCall(t, engine, "render.source", links,
						ref(reopened["document"].(string)), map[string]any{})
					if len(live.Binaries) == 0 || !bytes.Equal(live.Binaries[0].Data, expected.Binaries[0].Data) {
						t.Fatal("browser LZW realtime output differs after save and reopening")
					}
				}
			})
		}
	}
}
