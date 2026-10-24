package psdweb

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func bridgeContentsFilterRequest(
	t *testing.T, engine *Engine, operation, editor string, arguments ...any,
) Response {
	t.Helper()
	request := Request{Operation: "editor." + operation, Target: editor}
	for _, argument := range arguments {
		encoded, err := json.Marshal(argument)
		if err != nil {
			t.Fatal(err)
		}
		request.Args = append(request.Args, encoded)
	}
	return engine.Execute(t.Context(), request, nil)
}

func verifyBridgeFilteredSourceAndArtwork(t *testing.T, data, contents []byte, expected *image.NRGBA) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	linked, err := source.LinkedFiles(t.Context())
	if err != nil || len(linked) != 1 {
		t.Fatalf("saved filtered source identity is missing: %v", err)
	}
	encoded, err := source.ReadSpan(linked[0].Data)
	if err != nil || !bytes.Equal(encoded, contents) {
		t.Fatalf("saved filtered replacement did not retain exact new source bytes: %v", err)
	}
	artwork, err := source.DecodeLayer(t.Context(), 0)
	if err != nil || artwork.Bounds != expected.Rect {
		t.Fatalf("saved filtered artwork dimensions changed: %v", err)
	}
	for _, plane := range artwork.Channels {
		component := int(plane.ID)
		if plane.ID == -1 {
			component = 3
		} else if component < 0 || component > 2 {
			continue
		}
		for index, value := range plane.Data {
			if value != expected.Pix[index*4+component] {
				t.Fatalf("saved artwork channel %d used old contents or repeated its filter", plane.ID)
			}
		}
	}
}

func TestSmartContentsFilterBridgeExactReplaceSaveReopenAndContinue(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, raster := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/raster%t", version, compression, raster), func(t *testing.T) {
					engine, err := New(Config{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
					original, _ := bridgeFilterFixture(t, version, false, false)
					asset := bridgeAsset(t, engine, "filtered-parent.psd", original)
					opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
					editor := opened["editor"].(string)
					stack := bridgeFilterStack(t, original)
					stack.Filters[1].Adjustment.HueSaturation.Master.Lightness = 51
					if response := bridgeFilterRequest(t, engine, editor, nil, stack, map[string]any{}); !response.OK {
						t.Fatalf("preparing current filter controls: %+v", response.Error)
					}
					var current []byte
					for index, step := range []struct {
						source, filtered [4]byte
					}{
						{[4]byte{255, 128, 64, 0}, [4]byte{255, 193, 161, 130}},
						{[4]byte{0, 64, 128, 255}, [4]byte{130, 161, 193, 255}},
					} {
						current = bridgeFilterContents(t, bridgeFilterGray(step.source), raster)
						replacement := bridgeAsset(t, engine, "new-filter-source", current)
						if index == 0 {
							bridgeCall(t, engine, "editor.ReplaceContents", editor, 1, assetRef(replacement))
						} else {
							bridgeCall(t, engine, "editor.ReplaceSmartObjectContentsWithOptions", editor, 1,
								assetRef(replacement), map[string]any{"SmartFilters": map[string]any{}})
						}
						expected := bridgeFilterGray(step.filtered)
						var saved []byte
						// Saving precedes Composite; the caller supplies neither rendered artwork nor a composite image.
						opened, saved = bridgeFilterSaveReopen(t, engine, editor, version, compression, stack, expected)
						verifyBridgeFilteredSourceAndArtwork(t, saved, current, expected)
						editor = opened["editor"].(string)
						bridgeFilterPixels(t, linkedRasterBridgeCall(t, engine, "render.source", nil,
							ref(opened["document"].(string)), map[string]any{"SmartFilters": map[string]any{}}), expected)
						bridgeSmartBytes(t, bridgeSmartCall(t, engine, "ReadSmartObjectContents", editor, nil, 1), current)
						if !bytes.Equal(engine.assets[replacement].data, current) {
							t.Fatal("filter replacement mutated imported source bytes")
						}
					}
					// Reopened filter editing must use the final replacement source, not an earlier projected cache.
					stack.Filters[1].Adjustment.HueSaturation.Master.Lightness = 0
					if response := bridgeFilterRequest(t, engine, editor, nil, stack, map[string]any{}); !response.OK {
						t.Fatalf("continuing filter editing after source replacements: %+v", response.Error)
					}
					_, saved := bridgeFilterSaveReopen(t, engine, editor, version, compression, stack,
						bridgeFilterGray([4]byte{0, 64, 128, 255}))
					verifyBridgeFilteredSourceAndArtwork(t, saved, current, bridgeFilterGray([4]byte{0, 64, 128, 255}))
					if !bytes.Equal(engine.assets[asset].data, original) {
						t.Fatal("filter source replacement mutated the imported parent document")
					}
				})
			}
		}
	}
}

func TestSmartContentsFilterBridgeApproximationMustBeExplicitOnEveryReplacement(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			engine, err := New(Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
			original, _ := bridgeFilterFixture(t, version, false, false)
			asset := bridgeAsset(t, engine, "approximate-filter-parent.psd", original)
			opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
			editor := opened["editor"].(string)
			stack := bridgeFilterStack(t, original)
			stack.Filters[0].Radius = .7
			filterPolicy := map[string]any{"ApproximateGaussian": true}
			if response := bridgeFilterRequest(t, engine, editor, nil, stack,
				map[string]any{"Render": map[string]any{"SmartFilters": filterPolicy}}); !response.OK {
				t.Fatalf("preparing explicit current Gaussian: %+v", response.Error)
			}
			for index, values := range [][4]byte{{255, 128, 64, 0}, {0, 64, 128, 255}} {
				pixels := bridgeFilterGray(values)
				contents := bridgeFilterContents(t, pixels, index != 0)
				replacement := bridgeAsset(t, engine, "approximate-new-source", contents)
				beforeOutput := bridgeCall(t, engine, "editor.WritePSD", editor)
				before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
				for _, options := range []map[string]any{{}, {"SmartFilters": map[string]any{}},
					{"SmartFilters": map[string]any{"ApproximateGaussian": false}}} {
					response := bridgeContentsFilterRequest(t, engine, "ReplaceContentsWithOptions", editor,
						1, assetRef(replacement), options)
					if response.OK || response.Error == nil || response.Error.Code != "unsupported" ||
						len(response.Binaries) != 0 {
						t.Fatalf("replacement implicitly enabled filter approximation: %+v", response.Error)
					}
					if after := blendingBridgeResult[psdedit.Changes](t,
						bridgeCall(t, engine, "editor.Changes", editor)); after != before {
						t.Fatal("rejected approximation changed the shared source transaction")
					}
					afterOutput := bridgeCall(t, engine, "editor.WritePSD", editor)
					if len(afterOutput.Binaries) != 1 ||
						!bytes.Equal(beforeOutput.Binaries[0].Data, afterOutput.Binaries[0].Data) {
						t.Fatal("rejected approximation changed persisted filter/source/artwork bytes")
					}
				}
				bridgeCall(t, engine, "editor.ReplaceContentsWithOptions", editor, 1, assetRef(replacement),
					map[string]any{"SmartFilters": filterPolicy})
				expected, err := psdfilter.Apply(t.Context(), pixels, stack,
					psdfilter.Options{ApproximateGaussian: true})
				if err != nil {
					t.Fatal(err)
				}
				twice, err := psdfilter.Apply(t.Context(), expected, stack,
					psdfilter.Options{ApproximateGaussian: true})
				if err != nil || bytes.Equal(expected.Pix, twice.Pix) {
					t.Fatalf("Gaussian fixture cannot detect duplicate filtering: %v", err)
				}
				var saved []byte
				opened, saved = bridgeFilterSaveReopen(t, engine, editor, version, uint16(index+2), stack, expected)
				verifyBridgeFilteredSourceAndArtwork(t, saved, contents, expected)
				editor = opened["editor"].(string)
				bridgeFilterPixels(t, linkedRasterBridgeCall(t, engine, "render.source", nil,
					ref(opened["document"].(string)), map[string]any{"SmartFilters": filterPolicy}), expected)
			}
		})
	}
}

func TestSmartContentsFilterBridgeExternalEmbedAndRetainedUpdate(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, operation := range []string{"EmbedLinkedContentsWithOptions", "UpdateLinkedContentsWithOptions"} {
			t.Run(fmt.Sprintf("v%d/%s", version, operation), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				original, sourceBytes := bridgeFilterFixture(t, version, true, false)
				asset := bridgeAsset(t, engine, "external-filter-parent.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				links := map[string]string{"filter-fixture": bridgeAsset(t, engine, "initial-filter-source", sourceBytes)}
				stack := bridgeFilterStack(t, original)
				stack.Filters[1].Adjustment.HueSaturation.Master.Lightness = 51
				if response := bridgeFilterRequest(t, engine, editor, links, stack,
					map[string]any{"Render": map[string]any{}}); !response.OK {
					t.Fatalf("preparing current external filter controls: %+v", response.Error)
				}
				retained := operation == "UpdateLinkedContentsWithOptions"
				for index, step := range []struct{ source, filtered [4]byte }{
					{[4]byte{255, 128, 64, 0}, [4]byte{255, 193, 161, 130}},
					{[4]byte{0, 64, 128, 255}, [4]byte{130, 161, 193, 255}},
				} {
					contents := bridgeFilterContents(t, bridgeFilterGray(step.source), true)
					replacement := bridgeAsset(t, engine, "external-filter-replacement.png", contents)
					links["filter-fixture"] = replacement
					if index == 0 || retained {
						bridgeSmartCall(t, engine, operation, editor, links, 1,
							map[string]any{"SmartFilters": map[string]any{}})
					} else {
						bridgeCall(t, engine, "editor.ReplaceContents", editor, 1, assetRef(replacement))
					}
					var saved []byte
					expected := bridgeFilterGray(step.filtered)
					opened, saved = bridgeFilterSaveReopen(t, engine, editor, version, uint16(3-index), stack, expected)
					verifyBridgeFilteredSourceAndArtwork(t, saved, contents, expected)
					parsed, err := psdfile.Parse(saved)
					if err != nil {
						t.Fatal(err)
					}
					records, err := parsed.LinkedFiles(t.Context())
					_ = parsed.Close()
					kind := "liFD"
					if retained {
						kind = "liFE"
					}
					if err != nil || len(records) != 1 || records[0].Kind != kind || records[0].ID != "filter-fixture" {
						t.Fatalf("filtered external update changed link ownership or identity: %v", err)
					}
					editor = opened["editor"].(string)
					bridgeSmartBytes(t, bridgeSmartCall(t, engine, "ReadSmartObjectContents", editor, links, 1), contents)
					bridgeFilterPixels(t, linkedRasterBridgeCall(t, engine, "render.source", links,
						ref(opened["document"].(string)), map[string]any{"SmartFilters": map[string]any{}}), expected)
				}
			})
		}
	}
}
