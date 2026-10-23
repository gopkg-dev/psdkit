package psdedit

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func lzwSmartPixels(seed uint32) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index := range pixels.Pix {
		seed = 1664525*seed + 1013904223
		pixels.Pix[index] = byte(seed >> 24)
	}
	return pixels
}

func TestSmartContentsLZWEditSaveReopenAndContinue(t *testing.T) {
	first, err := os.ReadFile("../psdfile/testdata/rgba-lzw-small.tif")
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile("../psdfile/testdata/rgba-lzw-small-updated.tif")
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, operation := range []string{"replace", "embed", "external"} {
				t.Run(fmt.Sprintf("v%d/c%d/%s", version, compression, operation), func(t *testing.T) {
					data := smartEditFixture(t, version, "")
					if operation != "replace" {
						data = linkedEditFixture(t, version, 7, "lnkE")
					}
					input := bytes.Clone(data)
					editor, original := smartEditOpen(t, data)
					infos, err := editor.SmartObjects(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					for index, contents := range [][]byte{first, second} {
						immutable := bytes.Clone(contents)
						kind := "liFD"
						switch {
						case operation == "external":
							kind = "liFE"
							err = editor.UpdateLinkedContents(t.Context(), infos[index].ID,
								func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
									return LinkedContentsUpdate{Data: contents}, nil
								})
						case operation == "embed" && index == 0:
							err = editor.EmbedLinkedContents(t.Context(), infos[0].ID,
								func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return contents, nil })
						default:
							err = editor.ReplaceContents(t.Context(), infos[index].ID, contents)
						}
						if err != nil {
							t.Fatal(err)
						}
						seed := uint32(0x9e3779b9)
						if index != 0 {
							seed = 0x12345678
						}
						smartEditAssertCaches(t, editor, lzwSmartPixels(seed))
						expected, err := editor.Composite(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						reopened, saved := smartFormatsSaveOpen(t, editor, compression)
						smartFormatsAssertRecord(t, saved, kind, "TIFF", immutable)
						smartEditAssertPreservedMetadata(t, original, saved)
						sceneOptions := psdscene.Options{}
						tileOptions := psdscene.TileOptions{}
						if operation == "external" {
							resolve := func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return contents, nil }
							sceneOptions.ExternalRasterResolver, tileOptions.ExternalRasterResolver = resolve, resolve
						}
						live, err := psdscene.Render(t.Context(), saved, sceneOptions)
						if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
							t.Fatalf("LZW source did not rerender after reopening: %v", err)
						}
						region := image.Rect(1, 0, 5, 2)
						tile, err := psdscene.RenderTile(t.Context(), saved, region, tileOptions)
						if err != nil {
							t.Fatal(err)
						}
						for y := region.Min.Y; y < region.Max.Y; y++ {
							for x := region.Min.X; x < region.Max.X; x++ {
								if tile.NRGBAAt(x, y) != live.NRGBAAt(x, y) {
									t.Fatal("LZW tile differs from full live render")
								}
							}
						}
						if !bytes.Equal(contents, immutable) || !bytes.Equal(data, input) {
							t.Fatal("LZW edit or render mutated original contents")
						}
						editor = reopened
					}
				})
			}
		}
	}
}
