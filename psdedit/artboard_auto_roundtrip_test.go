package psdedit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func artboardAutoDescriptor(t *testing.T, bounds image.Rectangle, background int32) []byte {
	t.Helper()
	rectangle := &psdfile.Descriptor{ClassID: "classFloatRect"}
	for index, key := range []string{"Left", "Top ", "Rght", "Btom"} {
		rectangle.Entries = append(rectangle.Entries, psdfile.DescriptorEntry{Key: key, Type: "doub",
			Value: float64([]int{bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y}[index])})
	}
	descriptor := &psdfile.Descriptor{ClassID: "artboard", Entries: []psdfile.DescriptorEntry{
		{Key: "artboardRect", Type: "Objc", Value: rectangle},
		{Key: "artboardBackgroundType", Type: "long", Value: background},
		{Key: "Clr ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "RGBC", Entries: []psdfile.DescriptorEntry{
			{Key: "Rd  ", Type: "doub", Value: float64(10)},
			{Key: "Grn ", Type: "doub", Value: float64(80)},
			{Key: "Bl  ", Type: "doub", Value: float64(160)},
		}}},
	}}
	data, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func artboardAutoDocumentDescriptor(t *testing.T, count int32, origin float64) []byte {
	t.Helper()
	point := &psdfile.Descriptor{ClassID: "Pnt ", Entries: []psdfile.DescriptorEntry{
		{Key: "Hrzn", Type: "doub", Value: origin}, {Key: "Vrtc", Type: "doub", Value: -origin},
	}}
	descriptor := &psdfile.Descriptor{ClassID: "artboard", Entries: []psdfile.DescriptorEntry{
		{Key: "Cnt ", Type: "long", Value: count},
		{Key: "origin", Type: "Objc", Value: point},
		{Key: "autoExpandOffset", Type: "Objc", Value: point},
	}}
	data, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func artboardAutoFixture(t *testing.T, version uint16, background int32, origin float64) []byte {
	t.Helper()
	var records, samples []byte
	for index, name := range []string{"end-left", "left-child", "left-board", "end-right", "right-child", "right-board", "gap"} {
		bounds := image.Rectangle{}
		var pixels *image.NRGBA
		if index == 1 || index == 4 || index == 6 {
			bounds = image.Rect(0, 0, 5, 1)
			pixels = image.NewNRGBA(bounds)
			for x := range 5 {
				pixel := color.NRGBA{R: 255, A: 128}
				if index == 4 {
					pixel = color.NRGBA{G: 200, B: 100, A: 255}
				}
				if index == 6 {
					pixel = color.NRGBA{}
					if x == 2 {
						pixel = color.NRGBA{R: 255, B: 255, A: 255}
					}
				}
				pixels.SetNRGBA(x, 0, pixel)
			}
		}
		for _, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
			records = binary.BigEndian.AppendUint32(records, uint32(int32(coordinate)))
		}
		channels := uint16(0)
		if pixels != nil {
			channels = 4
		}
		records = binary.BigEndian.AppendUint16(records, channels)
		for component := range int(channels) {
			id := int16(component)
			if component == 3 {
				id = -1
			}
			records = binary.BigEndian.AppendUint16(records, uint16(id))
			records = compositeFixtureLength(records, version, 7)
			samples = binary.BigEndian.AppendUint16(samples, 0)
			for x := range 5 {
				samples = append(samples, pixels.Pix[x*4+component])
			}
		}
		records = append(records, []byte("8BIMnorm")...)
		records = append(records, 255, 0, 0, 0)
		extra := make([]byte, 8)
		extra = append(extra, byte(len(name)))
		extra = append(extra, name...)
		extra = append(extra, make([]byte, (4-(len(name)+1)%4)%4)...)
		if index == 0 || index == 2 || index == 3 || index == 5 {
			divider := uint32(3)
			if index == 2 || index == 5 {
				divider = 1
			}
			payload := binary.BigEndian.AppendUint32(nil, divider)
			payload = append(payload, []byte("8BIMnorm")...)
			extra = append(extra, compositeFixtureTag("lsct", payload)...)
		}
		if index == 2 || index == 5 {
			box, kind := image.Rect(0, 0, 2, 1), background
			if index == 5 {
				box, kind = image.Rect(3, 0, 5, 1), 2
			}
			extra = append(extra, compositeFixtureTag("artb", artboardAutoDescriptor(t, box, kind))...)
			extra = append(extra, compositeFixtureTag("pRsv", []byte{byte(index), 99, 7, 0})...)
		}
		records = binary.BigEndian.AppendUint32(records, uint32(len(extra)))
		records = append(records, extra...)
	}
	layers := binary.BigEndian.AppendUint16(nil, uint16(0xfff9))
	layers = append(layers, records...)
	layers = append(layers, samples...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	global := compositeFixtureTag("artd", artboardAutoDocumentDescriptor(t, 2, origin))
	return smartEditDocument(version, 5, 1, 4, layers, global,
		append([]byte{0, 0}, bytes.Repeat([]byte{19}, 20)...))
}

func artboardAutoLayer(t *testing.T, editor *Editor, name string) LayerID {
	t.Helper()
	layers, err := editor.Layers()
	if err != nil {
		t.Fatal(err)
	}
	var find func([]LayerInfo) LayerID
	find = func(items []LayerInfo) LayerID {
		for _, item := range items {
			if item.Name == name {
				return item.ID
			}
			if child := find(item.Children); child != 0 {
				return child
			}
		}
		return 0
	}
	if id := find(layers); id != 0 {
		return id
	}
	t.Fatalf("layer %q is missing", name)
	return 0
}

func artboardAutoMetadata(t *testing.T, source *psdfile.Document) map[string][]byte {
	t.Helper()
	metadata := make(map[string][]byte)
	for _, record := range source.Layers {
		for _, block := range record.AdditionalInfo {
			if block.Key != "artb" && block.Key != "pRsv" {
				continue
			}
			data, err := source.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			metadata[record.Name+"/"+block.Key] = data
		}
	}
	for _, block := range source.AdditionalInfo {
		if block.Key == "artd" {
			data, err := source.ReadSpan(block.Data)
			if err != nil {
				t.Fatal(err)
			}
			metadata["artd"] = data
		}
	}
	return metadata
}

func artboardAutoExpected(left, right color.NRGBA) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 5, 1))
	for x, pixel := range []color.NRGBA{left, left, {R: 255, B: 255, A: 255}, right, right} {
		pixels.SetNRGBA(x, 0, pixel)
	}
	return pixels
}

func artboardAutoRoundtrip(
	t *testing.T, editor *Editor, compression uint16, expected *image.NRGBA, disk bool,
) (*Editor, *psdfile.Document) {
	t.Helper()
	before, _ := editor.Changes()
	pixels, err := editor.Composite(t.Context())
	if err != nil || pixels.Rect != expected.Rect || !bytes.Equal(pixels.Pix, expected.Pix) {
		t.Fatalf("edited automatic artboard composite differs: %v, got %v want %v", err, pixels, expected.Pix)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("artboard Composite changed edit or saved revision")
	}
	var saved *psdfile.Document
	if disk {
		path := filepath.Join(t.TempDir(), "automatic-artboard.psd")
		if err := editor.Save(t.Context(), path, SaveOptions{Compression: compression}); err != nil {
			t.Fatal(err)
		}
		saved, err = psdfile.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = saved.Close() })
	} else {
		saved = compositeSave(t, editor, SaveOptions{Compression: compression})
	}
	if saved.Header.Version != editor.source.Header.Version {
		t.Fatal("automatic artboard save changed PSD/PSB format")
	}
	live, err := psdscene.Render(t.Context(), saved)
	if err != nil || !bytes.Equal(live.Pix, expected.Pix) || live.Rect != expected.Rect {
		t.Fatalf("reopened Scene differs from edited artboard composite: %v", err)
	}
	region := expected.Rect
	if region.Dx() > 2 {
		region.Min.X++
		region.Max.X--
	}
	tile, err := psdscene.RenderTile(t.Context(), saved, region)
	if err != nil || tile.Rect != region {
		t.Fatalf("reopened artboard tile failed: %v", err)
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			if tile.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
				t.Fatal("reopened artboard ROI changed source coordinates or clipping")
			}
		}
	}
	var planes []byte
	for component := range int(saved.Header.Channels) {
		for offset := 0; offset < len(expected.Pix); offset += 4 {
			alpha := int(expected.Pix[offset+3])
			value := alpha
			if component < 3 {
				value = (int(expected.Pix[offset+component])*alpha + 255*(255-alpha) + 127) / 255
			}
			planes = append(planes, byte(value))
		}
	}
	compositeExpect(t, saved, planes)
	reopened, err := New(saved)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedPixels, err := reopened.Composite(t.Context())
	if err != nil || !bytes.Equal(reopenedPixels.Pix, expected.Pix) {
		t.Fatalf("reopened editor lost automatic artboard rendering: %v", err)
	}
	return reopened, saved
}

func TestArtboardAutoBackgroundsOriginsAndChildEditsPSDPSB(t *testing.T) {
	backgrounds := map[int32]color.NRGBA{
		1: {R: 255, G: 127, B: 127, A: 255}, 2: {R: 128, A: 255},
		3: {R: 255, A: 128}, 4: {R: 133, G: 40, B: 80, A: 255},
	}
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for background, expectedLeft := range backgrounds {
				for _, origin := range []float64{0, 9} {
					t.Run(fmt.Sprintf("v%d/c%d/bg%d/origin%g", version, compression, background, origin), func(t *testing.T) {
						editor, _, reader := compositeLayerOnlyEditor(t, artboardAutoFixture(t, version, background, origin))
						reader.blockFooter = true
						metadata := artboardAutoMetadata(t, editor.source)
						child := artboardAutoLayer(t, editor, "left-child")
						if err := editor.Rename(child, "edited-child"); err != nil {
							t.Fatal(err)
						}
						pixels := image.NewNRGBA(image.Rect(0, 0, 5, 1))
						for x := range 5 {
							pixels.SetNRGBA(x, 0, color.NRGBA{R: 255, A: 128})
						}
						if err := editor.ReplacePixels(child, pixels); err != nil {
							t.Fatal(err)
						}
						expected := artboardAutoExpected(expectedLeft, color.NRGBA{G: 200, B: 100, A: 255})
						reopened, saved := artboardAutoRoundtrip(t, editor, compression, expected, false)
						if reader.footerReads != 0 || !reflect.DeepEqual(metadata, artboardAutoMetadata(t, saved)) {
							t.Fatal("automatic artboard rendering read old footer or changed board metadata")
						}
						child = artboardAutoLayer(t, reopened, "edited-child")
						if err := reopened.SetOpacity(child, 0); err != nil {
							t.Fatal(err)
						}
						backgroundPixel := map[int32]color.NRGBA{1: {R: 255, G: 255, B: 255, A: 255},
							2: {A: 255}, 3: {}, 4: {R: 10, G: 80, B: 160, A: 255}}[background]
						expected = artboardAutoExpected(backgroundPixel, color.NRGBA{G: 200, B: 100, A: 255})
						reopened, saved = artboardAutoRoundtrip(t, reopened, compression, expected, true)
						if !reflect.DeepEqual(metadata, artboardAutoMetadata(t, saved)) {
							t.Fatal("continued opacity edit changed board metadata")
						}
						child = artboardAutoLayer(t, reopened, "edited-child")
						if err := reopened.SetVisible(child, false); err != nil {
							t.Fatal(err)
						}
						if err := reopened.SetOpacity(child, 255); err != nil {
							t.Fatal(err)
						}
						artboardAutoRoundtrip(t, reopened, compression, expected, false)
					})
				}
			}
		}
	}
}

func TestArtboardAutoDisabledOwnStatePreservesMetadataAcrossSaves(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			editor := compositeEditor(t, editableDisabledArtboardFixture(t, version))
			metadata := disabledArtboardSavedMetadata(t, editor.source)
			pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
			pixels.SetNRGBA(0, 0, color.NRGBA{B: 255, A: 128})
			if err := editor.ReplacePixels(2, pixels); err != nil {
				t.Fatal(err)
			}
			for step, value := range []byte{127, 191} {
				expected := image.NewNRGBA(pixels.Rect)
				expected.SetNRGBA(0, 0, color.NRGBA{R: value, G: value, B: 255, A: 255})
				reopened, saved := artboardAutoRoundtrip(t, editor, compression, expected, step != 0)
				if !reflect.DeepEqual(metadata, disabledArtboardSavedMetadata(t, saved)) {
					t.Fatal("automatic save discarded artboard or disabled mask/vector/effect metadata")
				}
				editor = reopened
				if step == 0 {
					if err := editor.SetOpacity(2, 128); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}
