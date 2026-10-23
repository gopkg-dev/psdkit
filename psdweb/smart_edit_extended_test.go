package psdweb

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestSmartContentsBridgeOptionsSourceBytesSaveAndReopen(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, operation := range []string{
			"ReplaceContentsWithOptions", "ReplaceSmartObjectContentsWithOptions",
			"EmbedLinkedContentsWithOptions", "UpdateLinkedContentsWithOptions",
		} {
			t.Run(fmt.Sprintf("version%d/%s", version, operation), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				external := operation == "EmbedLinkedContentsWithOptions" ||
					operation == "UpdateLinkedContentsWithOptions"
				retained := operation == "UpdateLinkedContentsWithOptions"
				original, source := bridgeSmartFixture(t, version, external)
				asset := bridgeAsset(t, engine, "shared.psd", original)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
					bridgeCall(t, engine, "editor.SmartObjects", editor))
				if len(infos) != 2 || len(infos[0].SharedInstances) != 2 {
					t.Fatal("browser did not expose both shared instances")
				}
				links := map[string]string{infos[0].SourceID: bridgeAsset(t, engine, "source.psd", source)}
				before := bridgeSmartCall(t, engine, "ReadSmartObjectContents", editor, links, infos[0].ID)
				bridgeSmartBytes(t, before, source)
				wanted := color.NRGBA{R: 27, G: 81, B: 219, A: 255}
				pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				pixels.SetNRGBA(0, 0, wanted)
				var encoded bytes.Buffer
				if err := png.Encode(&encoded, pixels); err != nil {
					t.Fatal(err)
				}
				replacement := bytes.Clone(encoded.Bytes())
				replacementAsset := bridgeAsset(t, engine, "replacement.png", replacement)
				links[infos[0].SourceID] = replacementAsset
				options := psdedit.SmartContentsOptions{AssumeUntaggedSRGB: true}
				if external {
					bridgeSmartCall(t, engine, operation, editor, links, infos[0].ID, options)
				} else {
					bridgeCall(t, engine, "editor."+operation, editor, infos[0].ID, assetRef(replacementAsset), options)
				}
				for _, info := range infos {
					bridgeSmartBytes(t, bridgeSmartCall(t, engine, "ReadSmartObjectContents", editor, nil, info.ID),
						replacement)
				}
				updated := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
					bridgeCall(t, engine, "editor.SmartObjects", editor))
				for _, info := range updated {
					if info.FileType != "PNGf" || info.Embedded == retained || !info.Replaced {
						t.Fatalf("browser retained an obsolete source view: %+v", info)
					}
				}
				saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": 3})
				if len(saved.Binaries) != 1 {
					t.Fatal("edited PSD/PSB was not exported")
				}
				if !bytes.Equal(original, engine.assets[asset].data) ||
					!bytes.Equal(replacement, engine.assets[replacementAsset].data) {
					t.Fatal("smart-object editing mutated an imported asset")
				}
				outputAsset := bridgeAsset(t, engine, "updated.psd", saved.Binaries[0].Data)
				reopened := bridgeCall(t, engine, "document.open", "", assetRef(outputAsset)).Data.(map[string]any)
				if reopened["header"].(psdfile.Header).Version != version {
					t.Fatal("save changed the PSD/PSB format")
				}
				reopenedEditor := reopened["editor"].(string)
				reopenedInfos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
					bridgeCall(t, engine, "editor.SmartObjects", reopenedEditor))
				for _, info := range reopenedInfos {
					if info.FileType != "PNGf" || info.Embedded == retained || info.SourceID != infos[0].SourceID {
						t.Fatalf("saved source identity, type or embedding state changed: %+v", info)
					}
					bridgeSmartBytes(t,
						bridgeSmartCall(t, engine, "ReadSmartObjectContents", reopenedEditor, links, info.ID), replacement)
				}
				if retained {
					id, err := json.Marshal(reopenedInfos[0].ID)
					if err != nil {
						t.Fatal(err)
					}
					request := Request{Operation: "editor.ReadSmartObjectContents", Target: reopenedEditor}
					request.Args = append(request.Args, id)
					blocked := engine.Execute(context.Background(), request, nil)
					if blocked.OK || blocked.Error.Code != "invalid_request" || len(blocked.Binaries) != 0 {
						t.Fatalf("external source was read without an explicit asset mapping: %+v", blocked)
					}
				}
				bridgeSmartPreview(t, bridgeCall(t, engine, "preview.saved", "",
					ref(reopened["document"].(string))), wanted)
				bridgeSmartPreview(t, bridgeCall(t, engine, "editor.Composite", reopenedEditor, map[string]any{}), wanted)
			})
		}
	}
}

func bridgeSmartCall(
	t *testing.T, engine *Engine, operation, target string, links map[string]string, arguments ...any,
) Response {
	t.Helper()
	request := Request{Operation: "editor." + operation, Target: target, Links: links}
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

func bridgeSmartBytes(t *testing.T, response Response, expected []byte) {
	t.Helper()
	if len(response.Binaries) != 1 || !bytes.Equal(response.Binaries[0].Data, expected) {
		t.Fatal("browser source export changed the original encoded contents")
	}
}

func bridgeSmartPreview(t *testing.T, response Response, expected color.NRGBA) {
	t.Helper()
	if len(response.Binaries) == 0 {
		t.Fatal("browser preview is missing")
	}
	pixels, err := png.Decode(bytes.NewReader(response.Binaries[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if pixels.Bounds().Size() != image.Pt(2, 1) {
		t.Fatal("shared instances no longer occupy the full parent canvas")
	}
	for column := range 2 {
		if actual := color.NRGBAModel.Convert(pixels.At(column, 0)); actual != expected {
			t.Fatalf("shared instance %d pixel = %v, want %v", column, actual, expected)
		}
	}
}

// bridgeSmartFixture creates two shared placed instances without the production parent-document writer.
func bridgeSmartFixture(t *testing.T, version uint16, external bool) ([]byte, []byte) {
	t.Helper()
	oldPixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	oldPixels.SetNRGBA(0, 0, color.NRGBA{R: 120, G: 120, B: 120, A: 255})
	child, err := psdedit.BuildRasterContents(context.Background(), oldPixels)
	if err != nil {
		t.Fatal(err)
	}
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	var records, channelData []byte
	for column := range 2 {
		transform := make([]psdfile.DescriptorValue, 8)
		for index, coordinate := range []int{column, 0, column + 1, 0, column + 1, 1, column, 1} {
			transform[index] = psdfile.DescriptorValue{Type: "doub", Value: float64(coordinate)}
		}
		placement, encodeErr := psdfile.EncodeDescriptor(nil, &psdfile.Descriptor{
			ClassID: "null", Entries: []psdfile.DescriptorEntry{
				{Key: "Idnt", Type: "TEXT", Value: "shared-fixture"},
				{Key: "Type", Type: "long", Value: int32(2)},
				{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{
					ClassID: "size", Entries: []psdfile.DescriptorEntry{
						{Key: "Wdth", Type: "doub", Value: float64(1)},
						{Key: "Hght", Type: "doub", Value: float64(1)},
					},
				}},
				{Key: "Trnf", Type: "VlLs", Value: transform},
				{Key: "nonAffineTransform", Type: "VlLs", Value: transform},
			},
		}, true)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		for _, coordinate := range []int{0, column, 1, column + 1} {
			records = binary.BigEndian.AppendUint32(records, uint32(coordinate))
		}
		records = binary.BigEndian.AppendUint16(records, 4)
		for _, channel := range []int16{0, 1, 2, -1} {
			records = binary.BigEndian.AppendUint16(records, uint16(channel))
			records = appendLength(records, 3)
			sample := byte(120)
			if channel == -1 {
				sample = 255
			}
			channelData = append(channelData, 0, 0, sample)
		}
		records = append(records, []byte("8BIMnorm\xff\x00\x00\x00")...)
		extra := append(make([]byte, 8), 1, 'S', 0, 0)
		extra = append(extra, bridgeSmartTag("lyid", binary.BigEndian.AppendUint32(nil, uint32(10+column)), false, 2)...)
		placement = append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), placement...)
		extra = append(extra, bridgeSmartTag("SoLd", placement, false, 2)...)
		records = binary.BigEndian.AppendUint32(records, uint32(len(extra)))
		records = append(records, extra...)
	}
	layers := binary.BigEndian.AppendUint16(nil, 2)
	layers = append(layers, records...)
	layers = append(layers, channelData...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	kind, key := "liFD", "lnk2"
	if external {
		kind, key = "liFE", "lnkE"
	}
	body := binary.BigEndian.AppendUint32([]byte(kind), 3)
	body = append(body, byte(len("shared-fixture")))
	body = append(body, "shared-fixture"...)
	body = binary.BigEndian.AppendUint32(body, 6)
	for _, unit := range "source" {
		body = binary.BigEndian.AppendUint16(body, uint16(unit))
	}
	body = append(body, "8BPS\x00\x00\x00\x00"...)
	body = binary.BigEndian.AppendUint64(body, uint64(len(child)))
	body = append(body, 0)
	if external {
		descriptor, encodeErr := psdfile.EncodeDescriptor(nil, &psdfile.Descriptor{ClassID: "null"}, true)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		body = append(body, descriptor...)
		body = binary.BigEndian.AppendUint64(body, uint64(len(child)))
	}
	body = append(body, child...)
	entry := binary.BigEndian.AppendUint64(nil, uint64(len(body)))
	entry = append(entry, body...)
	entry = append(entry, make([]byte, (4-len(body)%4)%4)...)
	section := appendLength(nil, len(layers))
	section = append(section, layers...)
	section = binary.BigEndian.AppendUint32(section, 0)
	section = append(section, bridgeSmartTag(key, entry, version == 2 && key == "lnk2", 4)...)
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = binary.BigEndian.AppendUint32(header, 1)
	header = binary.BigEndian.AppendUint32(header, 2)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = append(header, make([]byte, 8)...)
	header = appendLength(header, len(section))
	header = append(header, section...)
	return append(header, 0, 0, 120, 120, 120, 120, 120, 120), child
}

func bridgeSmartTag(key string, payload []byte, wide bool, alignment int) []byte {
	data := []byte("8BIM" + key)
	if wide {
		data = binary.BigEndian.AppendUint64(data, uint64(len(payload)))
	} else {
		data = binary.BigEndian.AppendUint32(data, uint32(len(payload)))
	}
	data = append(data, payload...)
	return append(data, make([]byte, (alignment-len(payload)%alignment)%alignment)...)
}
