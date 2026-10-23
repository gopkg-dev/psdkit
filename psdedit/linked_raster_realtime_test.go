package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestLinkedRasterRealtimeEditSaveReopenContinue(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, format := range []string{"png", "jpeg", "tiff", "tiff-alpha", "tiff-deflate"} {
				t.Run(fmt.Sprintf("version%d/compression%d/%s", version, compression, format), func(t *testing.T) {
					original := linkedEditFixture(t, version, 7, "lnkE")
					unchanged := bytes.Clone(original)
					editor, source := smartEditOpen(t, original)
					infos, err := editor.SmartObjects(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					var encoded []byte
					var expected *image.NRGBA
					fileType := "TIFF"
					if format == "tiff-deflate" {
						expected = smartFormatsPixels(2, 2, true)
						encoded = smartDeflateTIFF(t, expected, binary.BigEndian, 8, true)
					} else {
						encoded, expected, fileType = smartFormatsEncoded(t, format, 2, 2)
					}
					encodedCopy := bytes.Clone(encoded)
					if err := editor.UpdateLinkedContents(t.Context(), infos[0].ID,
						func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
							return LinkedContentsUpdate{Data: encoded}, nil
						}); err != nil {
						t.Fatal(err)
					}
					reopened, saved := smartFormatsSaveOpen(t, editor, compression)
					smartFormatsAssertRecord(t, saved, "liFE", fileType, encoded)
					smartEditAssertPreservedMetadata(t, source, saved)
					resolverCalls := 0
					resolver := func(ctx context.Context, record psdfile.LinkedFileRecord) ([]byte, error) {
						resolverCalls++
						if record.Kind != "liFE" || record.ID != infos[0].SourceID || record.FileType != fileType {
							t.Fatal("resolver received the wrong retained external source")
						}
						return encoded, ctx.Err()
					}
					linkedRasterLiveMatchesEditor(t, editor, saved, resolver)
					if resolverCalls != 2 {
						t.Fatalf("shared contents resolved %d times across full/region renders, want 2", resolverCalls)
					}
					placed, err := psdscene.RenderPlacedLayer(t.Context(), saved, 0,
						psdscene.Options{ExternalRasterResolver: resolver})
					if err != nil || !bytes.Equal(placed.Pixels.Pix, expected.Pix) {
						t.Fatalf("placed artwork did not decode the external raster: %v", err)
					}
					if output, err := psdscene.Render(t.Context(), saved); output != nil || !errors.Is(err, psdfile.ErrUnsupported) {
						t.Fatalf("external live render used retained cached bytes without a resolver: %v", err)
					}
					// A new explicit source must win over the file's old linked cache and saved parent raster.
					newPixels := smartEditImage(smartEditColors())
					var replacement bytes.Buffer
					if err := png.Encode(&replacement, newPixels); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(encoded, encodedCopy) || !bytes.Equal(original, unchanged) {
						t.Fatal("rendering changed caller-owned source bytes")
					}
					encoded = linkedRasterReplacement(t, format, newPixels)
					newLive, err := psdscene.Render(t.Context(), saved,
						psdscene.Options{ExternalRasterResolver: resolver})
					if err != nil {
						t.Fatal(err)
					}
					cached, err := reopened.Composite(t.Context())
					if err != nil || bytes.Equal(cached.Pix, newLive.Pix) {
						t.Fatalf("live render reused stale saved source pixels: %v", err)
					}
					encoded = replacement.Bytes()
					if err := reopened.UpdateLinkedContents(t.Context(), infos[1].ID,
						func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
							return LinkedContentsUpdate{Data: encoded}, nil
						}); err != nil {
						t.Fatal(err)
					}
					fileType = "PNGf"
					if err := reopened.SetSmartObjectTransformWithOptions(t.Context(), infos[0].ID,
						smartTransformCorners(1, 0, 2, 2), nil, SmartTransformOptions{}); err != nil {
						t.Fatal(err)
					}
					continued, savedAgain := smartFormatsSaveOpen(t, reopened, compression)
					smartFormatsAssertRecord(t, savedAgain, "liFE", "PNGf", encoded)
					linkedRasterLiveMatchesEditor(t, continued, savedAgain, resolver)
					if err := continued.SetOpacity(infos[0].ID, 128); err != nil {
						t.Fatal(err)
					}
					_, savedThird := smartFormatsSaveOpen(t, continued, compression)
					linkedRasterLiveMatchesEditor(t, continued, savedThird, resolver)
				})
			}
		}
	}
}

func linkedRasterReplacement(t *testing.T, format string, pixels *image.NRGBA) []byte {
	t.Helper()
	var output bytes.Buffer
	switch format {
	case "png":
		if err := png.Encode(&output, pixels); err != nil {
			t.Fatal(err)
		}
	case "jpeg":
		if err := jpeg.Encode(&output, pixels, &jpeg.Options{Quality: 95}); err != nil {
			t.Fatal(err)
		}
	case "tiff", "tiff-alpha":
		return smartFormatsTIFF(pixels, format == "tiff-alpha")
	case "tiff-deflate":
		return smartDeflateTIFF(t, pixels, binary.BigEndian, 8, true)
	default:
		t.Fatalf("unexpected raster fixture format %q", format)
	}
	return output.Bytes()
}

func linkedRasterLiveMatchesEditor(
	t *testing.T, editor *Editor, saved *psdfile.Document, resolver psdscene.LinkedRasterResolver,
) {
	t.Helper()
	expected, err := editor.Composite(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	actual, err := psdscene.Render(t.Context(), saved, psdscene.Options{ExternalRasterResolver: resolver})
	if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("external source render differs from edited and saved pixels: %v", err)
	}
	region := image.Rect(1, 0, 5, 2)
	partial, err := psdscene.RenderTile(t.Context(), saved, region,
		psdscene.TileOptions{ExternalRasterResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if partial.NRGBAAt(x, y) != actual.NRGBAAt(x, y) {
				t.Fatalf("external raster tile differs at %d,%d", x, y)
			}
		}
	}
	if actual.NRGBAAt(actual.Rect.Min.X, actual.Rect.Min.Y) == (color.NRGBA{R: 220, G: 60, B: 40, A: 255}) {
		t.Fatal("reopened source used the original stale layer raster")
	}
}
