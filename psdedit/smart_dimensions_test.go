package psdedit

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func smartSizedPrototype(version uint16, canvas image.Point, bounds image.Rectangle, pixels *image.NRGBA) []byte {
	record, payloads := smartEditLayer(version, "Editable dimensions", 70, bounds, pixels, nil, false)
	layers := binary.BigEndian.AppendUint16(nil, 0xffff)
	layers = append(layers, record...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	composite := []byte{0, 0}
	for component := range 4 {
		for row := range canvas.Y {
			for column := range canvas.X {
				pixel := pixels.NRGBAAt(column-bounds.Min.X, row-bounds.Min.Y)
				value := []byte{pixel.R, pixel.G, pixel.B, pixel.A}[component]
				if component != 3 {
					value = byte(math.RoundToEven(float64(value)*float64(pixel.A)/255 + 255 - float64(pixel.A)))
				}
				composite = append(composite, value)
			}
		}
	}
	return smartEditDocument(version, canvas.X, canvas.Y, 4, layers, nil, composite)
}

func smartDimensionsPixels(width, height int) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for row := range height {
		for column := range width {
			pixels.SetNRGBA(column, row, color.NRGBA{R: byte(20 + row*40 + column*60), G: byte(20 + row*140),
				B: 120, A: 255})
		}
	}
	return pixels
}

func smartDimensionLegacy(t *testing.T, left int) []byte {
	t.Helper()
	value := &psdfile.Descriptor{ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpNone"}},
		{Key: "bounds", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "Rctn", Entries: []psdfile.DescriptorEntry{
			{Key: "Left", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(left)}},
			{Key: "Top ", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(0)}},
			{Key: "Rght", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(left + 2)}},
			{Key: "Btom", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(2)}},
		}}},
		{Key: "unknownLegacy", Type: "tdta", Value: []byte{7, 0, 3, 11}},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, value, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := binary.BigEndian.AppendUint32([]byte("plcL"), 3)
	payload = append(payload, 6)
	payload = append(payload, "legacy"...)
	for _, value := range []uint32{1, 1, 16, 2} {
		payload = binary.BigEndian.AppendUint32(payload, value)
	}
	for _, value := range []float64{float64(left), 0, float64(left + 2), 0,
		float64(left + 2), 2, float64(left), 2} {
		payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(value))
	}
	payload = binary.BigEndian.AppendUint32(payload, 0)
	payload = append(payload, encoded...)
	return append(payload, 0x91, 0x07, 0xa8)
}

func TestReplaceContentsDimensionsKeepCornersAndPatchOnlySizeNumbers(t *testing.T) {
	for _, outer := range []uint16{1, 2} {
		for _, inner := range []uint16{1, 2} {
			t.Run(string(rune('0'+outer))+"-"+string(rune('0'+inner)), func(t *testing.T) {
				original := smartEditFixture(t, outer, "")
				originalDigest := sha256.Sum256(original)
				editor, source := smartEditOpen(t, original)
				infos, err := editor.SmartObjects(nil)
				if err != nil {
					t.Fatal(err)
				}
				pixels := smartDimensionsPixels(3, 2)
				replacement := smartSizedPrototype(inner, image.Pt(3, 2), pixels.Rect, pixels)
				if err := editor.ReplaceContents(nil, infos[0].ID, replacement); err != nil {
					t.Fatal(err)
				}
				actual, _ := editor.SmartObjects(nil)
				for _, instance := range actual {
					if instance.Width != 3 || instance.Height != 2 || len(instance.SharedInstances) != 2 {
						t.Fatal("shared dimension views did not update together")
					}
				}
				for _, node := range editor.roots {
					for index, red := range []byte{35, 125, 75, 165} {
						if node.pixels.Pix[index*4] != red || node.pixels.Pix[index*4+3] != 255 {
							t.Fatal("new prototype was not normalized into the retained destination corners")
						}
					}
				}
				var output bytes.Buffer
				if err := editor.Write(nil, &output, SaveOptions{Compression: 1}); err != nil {
					t.Fatal(err)
				}
				saved, err := psdfile.Parse(output.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = saved.Close() })
				before, _ := source.SmartObjects(nil)
				after, err := saved.SmartObjects(nil)
				if err != nil || len(before) != len(after) {
					t.Fatalf("dimension metadata is unreadable: %v", err)
				}
				for index, record := range before {
					changed := after[index]
					if changed.Width != 3 || changed.Height != 2 || record.SourceID != changed.SourceID ||
						!slices.Equal(record.Transform, changed.Transform) || !slices.Equal(record.NonAffine, changed.NonAffine) {
						t.Fatal("dimension write changed source identity or destination geometry")
					}
					oldPayload, _ := source.ReadSpan(record.Block.Data)
					newPayload, _ := saved.ReadSpan(changed.Block.Data)
					allowed := make([]bool, len(oldPayload))
					for _, entry := range record.Descriptor.Entries {
						if entry.Key != "Sz  " {
							continue
						}
						for _, field := range entry.Value.(*psdfile.Descriptor).Entries {
							if field.Key != "Wdth" && field.Key != "Hght" {
								continue
							}
							for offset := field.Data.Offset; offset < field.Data.Offset+field.Data.Length; offset++ {
								allowed[offset-record.Block.Data.Offset] = true
							}
						}
					}
					if len(oldPayload) != len(newPayload) {
						t.Fatal("fixed-width dimension patch changed descriptor length")
					}
					for offset := range oldPayload {
						if !allowed[offset] && oldPayload[offset] != newPayload[offset] {
							t.Fatal("dimension patch rewrote unrelated descriptor or trailing bytes")
						}
					}
				}
				linked, _ := saved.LinkedFiles(nil)
				child, err := saved.OpenLinkedDocument(nil, linked[0])
				if err != nil || child.Header.Width != 3 || child.Header.Height != 2 || len(child.Layers) != 1 ||
					child.Layers[0].Name != "Editable dimensions" {
					t.Fatalf("dimension replacement lost editable content: %v", err)
				}
				_ = child.Close()
				if sha256.Sum256(original) != originalDigest {
					t.Fatal("dimension transaction changed immutable source bytes")
				}
			})
		}
	}
}

func TestReplaceContentsPrototypeCanvasIsIndependentOfLayerInkBounds(t *testing.T) {
	editor, _ := smartEditOpen(t, smartEditFixture(t, 1, ""))
	infos, _ := editor.SmartObjects(nil)
	pixels := smartDimensionsPixels(8, 4)
	data := smartSizedPrototype(1, image.Pt(12, 6), image.Rect(2, 1, 10, 5), pixels)
	if err := editor.ReplaceContents(nil, infos[0].ID, data); err != nil {
		t.Fatal(err)
	}
	actual, _ := editor.SmartObjects(nil)
	if actual[0].Width != 12 || actual[0].Height != 6 || actual[0].Bounds != image.Rect(0, 0, 2, 2) {
		t.Fatal("visible source ink bounds replaced the prototype canvas or destination corners")
	}
}

func TestReplaceContentsDimensionsRepeatAndRestoreOriginalMetadata(t *testing.T) {
	editor, source := smartEditOpen(t, smartEditFixture(t, 1, ""))
	infos, _ := editor.SmartObjects(nil)
	for _, size := range []image.Point{{3, 2}, {5, 3}, {2, 2}} {
		pixels := smartDimensionsPixels(size.X, size.Y)
		if err := editor.ReplaceContents(nil, infos[0].ID, smartSizedPrototype(1, size, pixels.Rect, pixels)); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		if err := editor.Write(nil, &output, SaveOptions{Compression: 1}); err != nil {
			t.Fatal(err)
		}
		saved, err := psdfile.Parse(output.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		placements, err := saved.SmartObjects(nil)
		if err != nil || placements[0].Width != float64(size.X) || placements[0].Height != float64(size.Y) {
			t.Fatalf("repeated replacement accumulated dimension scaling: %v", err)
		}
		if size == image.Pt(2, 2) {
			smartEditAssertPreservedMetadata(t, source, saved)
			for _, node := range editor.roots {
				if node.placed != nil || node.semanticCosts["placed"] != 0 || len(node.additional) != 0 {
					t.Fatal("restoring original dimensions retained stale placement overlays")
				}
			}
		}
		_ = saved.Close()
	}
}

func TestReplaceContentsRebuildsNestedPrototypeInsteadOfStaleSmartCaches(t *testing.T) {
	nested := smartEditFixture(t, 1, "")
	indexed, err := psdfile.Parse(nested)
	if err != nil {
		t.Fatal(err)
	}
	linked, _ := indexed.LinkedFiles(nil)
	span := linked[0].Data
	_ = indexed.Close()
	blue := smartEditImage([4]color.NRGBA{{20, 180, 240, 255}, {20, 180, 240, 255},
		{20, 180, 240, 255}, {20, 180, 240, 255}})
	content := smartEditPrototype(1, blue)
	if int64(len(content)) != span.Length {
		t.Fatal("independent stale-cache fixture content length differs")
	}
	copy(nested[span.Offset:span.Offset+span.Length], content)
	editor, _ := smartEditOpen(t, smartEditFixture(t, 1, ""))
	infos, _ := editor.SmartObjects(nil)
	if err := editor.ReplaceContents(nil, infos[0].ID, nested); err != nil {
		t.Fatal(err)
	}
	for _, node := range editor.roots {
		pixel := node.pixels.NRGBAAt(0, 0)
		if pixel.R != 20 || pixel.G != 180 || pixel.B != 240 || pixel.A != 255 {
			t.Fatal("replacement rendered nested saved red caches instead of embedded blue source")
		}
	}
}

func TestReplaceContentsDimensionsPatchKnownLegacyWarpAndPreservePrefixAndTail(t *testing.T) {
	editor, source := smartEditOpen(t, smartEditFixture(t, 1, "legacy"))
	infos, _ := editor.SmartObjects(nil)
	pixels := smartDimensionsPixels(3, 2)
	if err := editor.ReplaceContents(nil, infos[0].ID,
		smartSizedPrototype(1, image.Pt(3, 2), pixels.Rect, pixels)); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(nil, &output, SaveOptions{Compression: 1}); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	for index, layer := range source.Layers {
		for blockIndex, block := range layer.AdditionalInfo {
			if block.Key != "PlLd" {
				continue
			}
			actual := saved.Layers[index].AdditionalInfo[blockIndex]
			before, _ := source.ReadSpan(block.Data)
			after, _ := saved.ReadSpan(actual.Data)
			header := 9 + int(before[8]) + 16 + 64 + 4
			if len(before) != len(after) || !bytes.Equal(before[:header], after[:header]) ||
				!bytes.Equal(before[len(before)-3:], after[len(after)-3:]) {
				t.Fatal("legacy numeric patch changed instance ID, transform, header or opaque tail")
			}
			descriptor, err := saved.DecodeDescriptorContext(nil,
				psdfile.Span{Offset: actual.Data.Offset + int64(header), Length: actual.Data.Length - int64(header)}, true)
			if err != nil {
				t.Fatal(err)
			}
			bounds := descriptor.Entries[1].Value.(*psdfile.Descriptor)
			for fieldIndex, expected := range []float64{0, 0, 3, 2} {
				value, valid := smartScalar(bounds.Entries[fieldIndex])
				if !valid || value != expected {
					t.Fatal("legacy source bounds disagree with the modern prototype canvas")
				}
			}
		}
	}
}

func TestReplaceContentsDimensionsHandleDuplicateSizesOrRejectAmbiguityAtomically(t *testing.T) {
	for _, problem := range []string{"duplicate-size", "ambiguous-size"} {
		t.Run(problem, func(t *testing.T) {
			editor, _ := smartEditOpen(t, smartEditFixture(t, 1, problem))
			infos, _ := editor.SmartObjects(nil)
			pixels := smartDimensionsPixels(3, 2)
			err := editor.ReplaceContents(nil, infos[0].ID, smartSizedPrototype(1, image.Pt(3, 2), pixels.Rect, pixels))
			if problem == "ambiguous-size" {
				if !errors.Is(err, ErrUnsupported) || len(editor.linkedOverlays) != 0 ||
					editor.roots[0].pixels != nil || editor.roots[1].pixels != nil {
					t.Fatal("ambiguous duplicate fields partially committed a replacement")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := editor.Write(nil, &output, SaveOptions{Compression: 1}); err != nil {
				t.Fatal(err)
			}
			saved, err := psdfile.Parse(output.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer saved.Close()
			placements, err := saved.SmartObjects(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, placement := range placements {
				for _, entry := range placement.Descriptor.Entries {
					if entry.Key == "Sz  " {
						value, _ := smartScalar(entry.Value.(*psdfile.Descriptor).Entries[0])
						if value != 3 {
							t.Fatal("a consistent duplicate size retained stale source dimensions")
						}
					}
				}
			}
		})
	}
}

func TestReplaceContentsCombinedMetadataViewsRespectWorkingBudget(t *testing.T) {
	data := smartEditFixture(t, 1, "")
	original, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	placements, err := original.ReadSmartObjectView(nil)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := original.ReadLinkedFileView(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = original.Close()
	budget := placements.MetadataBytes + linked.MetadataBytes - 1
	editor, _ := smartEditOpen(t, data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: budget}})
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	err = editor.ReplaceContents(nil, infos[0].ID, smartEditPrototype(1, smartEditImage(smartEditColors())))
	if !errors.Is(err, psdfile.ErrLimit) || editor.linkedRecordsBytes != 0 || len(editor.linkedOverlays) != 0 ||
		editor.roots[0].pixels != nil || editor.roots[1].pixels != nil {
		t.Fatal("separately valid metadata views exceeded the combined budget or partially committed")
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("combined metadata limit changed revisions")
	}
}

func TestReplaceContentsDimensionsRejectUnknownLegacyVersionBeforeMutating(t *testing.T) {
	data := smartEditFixture(t, 1, "legacy")
	indexed, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, layer := range indexed.Layers {
		for _, block := range layer.AdditionalInfo {
			if block.Key == "PlLd" {
				binary.BigEndian.PutUint32(data[block.Data.Offset+4:], 4)
			}
		}
	}
	_ = indexed.Close()
	editor, _ := smartEditOpen(t, data)
	infos, _ := editor.SmartObjects(nil)
	pixels := smartDimensionsPixels(3, 2)
	err = editor.ReplaceContents(nil, infos[0].ID, smartSizedPrototype(1, image.Pt(3, 2), pixels.Rect, pixels))
	if !errors.Is(err, ErrUnsupported) || len(editor.linkedOverlays) != 0 || editor.linkedRecordsBytes != 0 ||
		editor.roots[0].pixels != nil || editor.roots[1].pixels != nil {
		t.Fatal("unknown legacy placed-layer version was copied with stale dimensions or partly committed")
	}
}
