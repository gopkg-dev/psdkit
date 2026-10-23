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
	"path/filepath"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func smartFormatsPixels(width, height int, alpha bool) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value := color.NRGBA{R: 120, G: 84, B: 48, A: 255}
			if alpha {
				value.A = 128
			}
			pixels.SetNRGBA(x, y, value)
		}
	}
	return pixels
}

// smartFormatsTIFF writes a classic, single-strip TIFF independently of the production codec.
func smartFormatsTIFF(pixels *image.NRGBA, alpha bool) []byte {
	samples, fields := uint16(3), uint16(9)
	if alpha {
		samples, fields = 4, 10
	}
	bitsOffset := uint32(8 + 2 + int(fields)*12 + 4)
	pixelOffset := bitsOffset + uint32(samples)*2
	data := make([]byte, pixelOffset)
	copy(data, "II")
	binary.LittleEndian.PutUint16(data[2:], 42)
	binary.LittleEndian.PutUint32(data[4:], 8)
	binary.LittleEndian.PutUint16(data[8:], fields)
	entries := [][4]uint32{
		{256, 4, 1, uint32(pixels.Rect.Dx())}, {257, 4, 1, uint32(pixels.Rect.Dy())},
		{258, 3, uint32(samples), bitsOffset}, {259, 3, 1, 1}, {262, 3, 1, 2},
		{273, 4, 1, pixelOffset}, {277, 3, 1, uint32(samples)},
		{278, 4, 1, uint32(pixels.Rect.Dy())},
		{279, 4, 1, uint32(pixels.Rect.Dx() * pixels.Rect.Dy() * int(samples))},
	}
	if alpha {
		entries = append(entries, [4]uint32{338, 3, 1, 2})
	}
	for index, entry := range entries {
		position := 10 + index*12
		binary.LittleEndian.PutUint16(data[position:], uint16(entry[0]))
		binary.LittleEndian.PutUint16(data[position+2:], uint16(entry[1]))
		binary.LittleEndian.PutUint32(data[position+4:], entry[2])
		binary.LittleEndian.PutUint32(data[position+8:], entry[3])
	}
	for component := range samples {
		binary.LittleEndian.PutUint16(data[int(bitsOffset)+int(component)*2:], 8)
	}
	for y := range pixels.Rect.Dy() {
		for x := range pixels.Rect.Dx() {
			value := pixels.NRGBAAt(x, y)
			data = append(data, value.R, value.G, value.B)
			if alpha {
				data = append(data, value.A)
			}
		}
	}
	return data
}

func smartFormatsEncoded(t *testing.T, format string, width, height int) ([]byte, *image.NRGBA, string) {
	t.Helper()
	pixels := smartFormatsPixels(width, height, format == "png" || format == "tiff-alpha")
	var encoded bytes.Buffer
	switch format {
	case "png":
		if err := png.Encode(&encoded, pixels); err != nil {
			t.Fatal(err)
		}
		return encoded.Bytes(), pixels, "PNGf"
	case "jpeg":
		if err := jpeg.Encode(&encoded, pixels, &jpeg.Options{Quality: 95}); err != nil {
			t.Fatal(err)
		}
		decoded, err := jpeg.Decode(bytes.NewReader(encoded.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		for y := range height {
			for x := range width {
				pixels.SetNRGBA(x, y, color.NRGBAModel.Convert(decoded.At(x, y)).(color.NRGBA))
			}
		}
		return encoded.Bytes(), pixels, "JPEG"
	case "tiff", "tiff-alpha":
		return smartFormatsTIFF(pixels, format == "tiff-alpha"), pixels, "TIFF"
	default:
		t.Fatalf("unknown fixture format %q", format)
		return nil, nil, ""
	}
}

func smartFormatsSaveOpen(t *testing.T, editor *Editor, compression uint16) (*Editor, *psdfile.Document) {
	t.Helper()
	extension := ".psd"
	if editor.source.Header.Version == 2 {
		extension = ".psb"
	}
	filename := filepath.Join(t.TempDir(), "edited"+extension)
	if err := editor.Save(t.Context(), filename, SaveOptions{Compression: compression}); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	reopened, err := New(saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if saved.Header.Version != editor.source.Header.Version {
		t.Fatal("saving changed the parent PSD/PSB format")
	}
	if _, err := saved.DecodeComposite(t.Context()); err != nil {
		t.Fatalf("saved merged preview cannot be decoded: %v", err)
	}
	return reopened, saved
}

func smartFormatsBaseline(t *testing.T, parent uint16, external bool) (*Editor, *psdfile.Document) {
	t.Helper()
	fixture := smartEditFixture(t, parent, "")
	if external {
		fixture = linkedEditFixture(t, parent, 7, "lnkE")
	}
	editor, _ := smartEditOpen(t, fixture)
	objects, err := editor.SmartObjects(t.Context())
	if err != nil || len(objects) != 2 {
		t.Fatalf("baseline smart objects: %v", err)
	}
	ranges := []psdmodel.BlendRange{{
		Source:      psdmodel.BlendSlider{BlackLow: 4, BlackHigh: 32, WhiteLow: 224, WhiteHigh: 250},
		Destination: psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255},
	}}
	if err := editor.SetBlendIf(objects[1].ID, ranges); err != nil {
		t.Fatal(err)
	}
	return smartFormatsSaveOpen(t, editor, 0)
}

func smartFormatsAssertSavedPixels(t *testing.T, saved *psdfile.Document, wanted *image.NRGBA) {
	t.Helper()
	for layerIndex := range saved.Layers {
		decoded, err := saved.DecodeLayer(t.Context(), layerIndex)
		if err != nil {
			t.Fatal(err)
		}
		for _, plane := range decoded.Channels {
			component := int(plane.ID)
			if component == -2 {
				continue
			}
			if component == -1 {
				component = 3
			}
			for index, sample := range plane.Data {
				if sample != wanted.Pix[index*4+component] {
					t.Fatalf("saved shared layer %d component %d sample %d = %d, want %d",
						layerIndex, component, index, sample, wanted.Pix[index*4+component])
				}
			}
		}
	}
}

func smartFormatsAssertRecord(
	t *testing.T, saved *psdfile.Document, kind, fileType string, encoded []byte,
) psdfile.LinkedFileRecord {
	t.Helper()
	records, err := saved.LinkedFiles(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("saved linked record count = %d: %v", len(records), err)
	}
	record := records[0]
	if record.Kind != kind || record.FileType != fileType {
		t.Fatalf("saved source type = %q/%q, want %q/%q", record.Kind, record.FileType, kind, fileType)
	}
	data, err := saved.ReadSpan(record.Data)
	if err != nil || !bytes.Equal(data, encoded) {
		t.Fatalf("encoded source bytes were changed: %v", err)
	}
	return record
}

func TestSmartContentsRasterSaveReopenSharedSources(t *testing.T) {
	for _, parent := range []uint16{1, 2} {
		for formatIndex, format := range []string{"png", "jpeg", "tiff", "tiff-alpha"} {
			for _, operation := range []string{"replace", "embed", "update"} {
				t.Run(fmt.Sprintf("parent%d/%s/%s", parent, format, operation), func(t *testing.T) {
					editor, original := smartFormatsBaseline(t, parent, operation != "replace")
					objects, err := editor.SmartObjects(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					encoded, wanted, fileType := smartFormatsEncoded(t, format, 2, 2)
					immutable := bytes.Clone(encoded)
					kind := "liFD"
					switch operation {
					case "replace":
						err = editor.ReplaceContents(t.Context(), objects[1].ID, encoded)
					case "embed":
						err = editor.EmbedLinkedContentsWithOptions(t.Context(), objects[1].ID,
							func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return encoded, nil },
							SmartContentsOptions{})
					case "update":
						kind = "liFE"
						err = editor.UpdateLinkedContentsWithOptions(t.Context(), objects[1].ID,
							func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
								return LinkedContentsUpdate{Data: encoded}, nil
							}, SmartContentsOptions{})
					}
					if err != nil {
						t.Fatal(err)
					}
					clear(encoded)
					smartEditAssertCaches(t, editor, wanted)
					reopened, saved := smartFormatsSaveOpen(t, editor, uint16(formatIndex))
					record := smartFormatsAssertRecord(t, saved, kind, fileType, immutable)
					smartFormatsAssertSavedPixels(t, saved, wanted)
					smartEditAssertPreservedMetadata(t, original, saved)
					if operation == "update" {
						before, _ := original.LinkedFiles(t.Context())
						if record.ExternalFileSize != uint64(len(immutable)) {
							t.Fatal("external file size was not refreshed")
						}
						for _, pair := range [][2]psdfile.Span{
							{before[0].ExternalDescriptor, record.ExternalDescriptor},
							{before[0].FileOpenDescriptor, record.FileOpenDescriptor},
							{before[0].Timestamp.Span, record.Timestamp.Span},
						} {
							previous, _ := original.ReadSpan(pair[0])
							current, _ := saved.ReadSpan(pair[1])
							if !bytes.Equal(previous, current) {
								t.Fatal("external path, open settings or unspecified timestamp changed")
							}
						}
					}
					views, err := reopened.SmartObjects(t.Context())
					if err != nil || len(views) != 2 || len(views[0].SharedInstances) != 2 ||
						views[0].SourceKind != kind || views[1].SourceKind != kind {
						t.Fatalf("shared-source relationships did not survive reopen: %v", err)
					}
					back := smartEditPrototype(2, smartEditImage(smartEditColors()))
					if operation == "update" {
						err = reopened.UpdateLinkedContents(t.Context(), views[0].ID,
							func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
								return LinkedContentsUpdate{Data: back}, nil
							})
					} else {
						err = reopened.ReplaceContents(t.Context(), views[0].ID, back)
					}
					if err != nil {
						t.Fatalf("reopened raster source cannot be replaced by PSD/PSB: %v", err)
					}
					_, second := smartFormatsSaveOpen(t, reopened, 1)
					smartFormatsAssertRecord(t, second, kind, "8BPB", back)
					smartFormatsAssertSavedPixels(t, second, smartEditImage(smartEditColors()))
				})
			}
		}
	}
}

func TestSmartContentsRasterDimensionsAndMaskRoundTrip(t *testing.T) {
	for _, parent := range []uint16{1, 2} {
		for _, format := range []string{"png", "jpeg", "tiff-alpha"} {
			t.Run(fmt.Sprintf("parent%d/%s", parent, format), func(t *testing.T) {
				editor, original := smartFormatsBaseline(t, parent, false)
				objects, _ := editor.SmartObjects(t.Context())
				encoded, pixels, fileType := smartFormatsEncoded(t, format, 3, 2)
				if err := editor.ReplaceContents(t.Context(), objects[0].ID, encoded); err != nil {
					t.Fatal(err)
				}
				wanted := image.NewNRGBA(image.Rect(0, 0, 2, 2))
				for y := range 2 {
					for x := range 2 {
						wanted.SetNRGBA(x, y, pixels.NRGBAAt(0, 0))
					}
				}
				smartEditAssertCaches(t, editor, wanted)
				_, saved := smartFormatsSaveOpen(t, editor, 2)
				smartFormatsAssertRecord(t, saved, "liFD", fileType, encoded)
				smartFormatsAssertSavedPixels(t, saved, wanted)
				before, _ := original.SmartObjects(t.Context())
				after, err := saved.SmartObjects(t.Context())
				if err != nil || len(after) != len(before) {
					t.Fatalf("resized source placements cannot be read: %v", err)
				}
				for index, placement := range after {
					if placement.Width != 3 || placement.Height != 2 ||
						!slices.Equal(placement.Transform, before[index].Transform) ||
						!slices.Equal(placement.NonAffine, before[index].NonAffine) {
						t.Fatal("resizing changed destination corners or missed a shared source size")
					}
					for _, pair := range [][2]psdfile.Span{
						{original.Layers[index].MaskData, saved.Layers[index].MaskData},
						{original.Layers[index].BlendRanges, saved.Layers[index].BlendRanges},
					} {
						previous, _ := original.ReadSpan(pair[0])
						current, _ := saved.ReadSpan(pair[1])
						if !bytes.Equal(previous, current) {
							t.Fatal("resizing changed the existing pixel mask or Blend If")
						}
					}
				}
			})
		}
	}
}

func TestSmartContentsRasterUnsupportedAndBudgetFailuresAreAtomic(t *testing.T) {
	for _, operation := range []string{"replace", "embed", "update"} {
		t.Run(operation, func(t *testing.T) {
			fixture := smartEditFixture(t, 1, "")
			if operation != "replace" {
				fixture = linkedEditFixture(t, 1, 7, "lnkE")
			}
			editor, _ := smartEditOpen(t, fixture,
				psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 2 << 20}})
			objects, _ := editor.SmartObjects(t.Context())
			apply := func(data []byte) error {
				switch operation {
				case "embed":
					return editor.EmbedLinkedContents(t.Context(), objects[0].ID,
						func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return data, nil })
				case "update":
					return editor.UpdateLinkedContents(t.Context(), objects[0].ID,
						func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
							return LinkedContentsUpdate{Data: data}, nil
						})
				default:
					return editor.ReplaceContents(t.Context(), objects[0].ID, data)
				}
			}
			unsupported := smartFormatsTIFF(smartFormatsPixels(2, 2, false), false)
			binary.LittleEndian.PutUint16(unsupported[2:], 43)
			large, _, _ := smartFormatsEncoded(t, "png", 512, 512)
			for _, rejected := range []struct {
				data []byte
				kind error
			}{{unsupported, ErrUnsupported}, {large, psdfile.ErrLimit}} {
				before, _ := editor.Changes()
				err := apply(rejected.data)
				after, _ := editor.Changes()
				if !errors.Is(err, rejected.kind) || before != after || len(editor.linkedOverlays) != 0 ||
					editor.linkedRecordsBytes != 0 || editor.roots[0].pixels != nil || editor.roots[1].pixels != nil {
					t.Fatalf("failed content edit was not atomic: %v, before=%+v after=%+v", err, before, after)
				}
			}
			good, _, _ := smartFormatsEncoded(t, "png", 2, 2)
			if err := apply(good); err != nil {
				t.Fatalf("rejected raster poisoned the next valid edit: %v", err)
			}
			smartFormatsSaveOpen(t, editor, 1)
		})
	}
}

func smartFormatsExternalFixture(t *testing.T, parent uint16, version uint32, custom, empty bool) []byte {
	t.Helper()
	fixture := linkedEditFixture(t, parent, version, "lnkE")
	if custom {
		fixture = gateSmartFixture(t, parent, true, true)
	}
	_, source := smartEditOpen(t, fixture)
	records, err := source.LinkedFiles(t.Context())
	if err != nil || len(records) != 1 {
		t.Fatalf("external fixture records: %v", err)
	}
	var payload []byte
	if !empty {
		payload = smartEditPrototype(1, smartEditImage(smartEditColors()))
	}
	entry := writerLinkedEntry("liFE", "fixture-id", version, payload)
	block := writerLinkedTag("8BIM", "lnkE", 4, entry.data, 4)
	span := records[0].Block.Span
	result := append(bytes.Clone(fixture[:span.Offset]), block...)
	result = append(result, fixture[span.Offset+span.Length:]...)
	delta := int64(len(block)) - span.Length
	if parent == 1 {
		length := int64(binary.BigEndian.Uint32(fixture[34:])) + delta
		binary.BigEndian.PutUint32(result[34:], uint32(length))
	} else {
		length := int64(binary.BigEndian.Uint64(fixture[34:])) + delta
		binary.BigEndian.PutUint64(result[34:], uint64(length))
	}
	return result
}

func TestSmartContentsRasterRetainsEmptyExternalCaches(t *testing.T) {
	for _, parent := range []uint16{1, 2} {
		for _, version := range []uint32{1, 2, 3, 7} {
			t.Run(fmt.Sprintf("parent%d/record%d", parent, version), func(t *testing.T) {
				editor, source := smartEditOpen(t, smartFormatsExternalFixture(t, parent, version, false, true))
				objects, _ := editor.SmartObjects(t.Context())
				before, _ := source.LinkedFiles(t.Context())
				for _, format := range []string{"png", "tiff-alpha"} {
					encoded, wanted, fileType := smartFormatsEncoded(t, format, 2, 2)
					if err := editor.UpdateLinkedContents(t.Context(), objects[0].ID,
						func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
							return LinkedContentsUpdate{Data: encoded}, nil
						}); err != nil {
						t.Fatal(err)
					}
					smartEditAssertCaches(t, editor, wanted)
					reopened, saved := smartFormatsSaveOpen(t, editor, 1)
					record := smartFormatsAssertRecord(t, saved, "liFE", fileType, nil)
					if record.HasData != before[0].HasData || record.DataSize != 0 || record.Data.Length != 0 ||
						record.ExternalFileSize != uint64(len(encoded)) {
						t.Fatal("external update populated an intentionally empty source cache or lost file size")
					}
					for _, pair := range [][2]psdfile.Span{
						{before[0].ExternalDescriptor, record.ExternalDescriptor},
						{before[0].FileOpenDescriptor, record.FileOpenDescriptor},
						{before[0].Tail, record.Tail},
					} {
						old, _ := source.ReadSpan(pair[0])
						current, _ := saved.ReadSpan(pair[1])
						if !bytes.Equal(old, current) {
							t.Fatal("empty-cache update changed external path, open settings or unknown tail")
						}
					}
					smartFormatsAssertSavedPixels(t, saved, wanted)
					editor = reopened
					objects, _ = editor.SmartObjects(t.Context())
				}
			})
		}
	}
}

func TestSmartContentsExternalOptionsReachCustomWarpPolicy(t *testing.T) {
	for _, operation := range []string{"embed", "update"} {
		t.Run(operation, func(t *testing.T) {
			editor, _ := smartEditOpen(t, smartFormatsExternalFixture(t, 1, 7, true, false))
			objects, _ := editor.SmartObjects(t.Context())
			encoded, _, fileType := smartFormatsEncoded(t, "png", 2, 2)
			apply := func(options SmartContentsOptions) error {
				if operation == "embed" {
					return editor.EmbedLinkedContentsWithOptions(t.Context(), objects[0].ID,
						func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return encoded, nil }, options)
				}
				return editor.UpdateLinkedContentsWithOptions(t.Context(), objects[0].ID,
					func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
						return LinkedContentsUpdate{Data: encoded}, nil
					}, options)
			}
			if err := apply(SmartContentsOptions{}); !errors.Is(err, ErrUnsupported) || editor.revision != 0 {
				t.Fatalf("default external update weakened the custom Warp gate: %v", err)
			}
			if err := apply(SmartContentsOptions{ApproximateCustomWarp: true}); err != nil {
				t.Fatalf("explicit external source policy was not forwarded: %v", err)
			}
			_, saved := smartFormatsSaveOpen(t, editor, 1)
			kind := "liFE"
			if operation == "embed" {
				kind = "liFD"
			}
			smartFormatsAssertRecord(t, saved, kind, fileType, encoded)
		})
	}
}
