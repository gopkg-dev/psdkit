package psdweb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func bridgeArtboardFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	encode := func(descriptor *psdfile.Descriptor) []byte {
		data, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	point := &psdfile.Descriptor{ClassID: "Pnt ", Entries: []psdfile.DescriptorEntry{
		{Key: "Hrzn", Type: "doub", Value: float64(0)}, {Key: "Vrtc", Type: "doub", Value: float64(0)},
	}}
	global := bridgeSmartTag("artd", encode(&psdfile.Descriptor{ClassID: "artboard",
		Entries: []psdfile.DescriptorEntry{
			{Key: "Cnt ", Type: "long", Value: int32(1)}, {Key: "origin", Type: "Objc", Value: point},
			{Key: "autoExpandOffset", Type: "Objc", Value: point},
		}}), false, 2)
	board := bridgeSmartTag("artb", encode(&psdfile.Descriptor{ClassID: "artboard",
		Entries: []psdfile.DescriptorEntry{
			{Key: "artboardBackgroundType", Type: "long", Value: int32(1)},
			{Key: "artboardRect", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "classFloatRect",
				Entries: []psdfile.DescriptorEntry{
					{Key: "Left", Type: "doub", Value: float64(0)}, {Key: "Top ", Type: "doub", Value: float64(0)},
					{Key: "Rght", Type: "doub", Value: float64(1)}, {Key: "Btom", Type: "doub", Value: float64(1)},
				}}},
		}}), false, 2)
	length := func(data []byte, value int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(value))
		}
		return binary.BigEndian.AppendUint32(data, uint32(value))
	}
	records := binary.BigEndian.AppendUint16(nil, 3)
	var channels []byte
	for _, divider := range []uint32{3, 0, 1} {
		for _, coordinate := range []uint32{0, 0, 1, 1} {
			records = binary.BigEndian.AppendUint32(records, coordinate)
		}
		count := uint16(0)
		if divider == 0 {
			count = 4
		}
		records = binary.BigEndian.AppendUint16(records, count)
		if count != 0 {
			for index, id := range []int16{0, 1, 2, -1} {
				records = binary.BigEndian.AppendUint16(records, uint16(id))
				records = length(records, 3)
				channels = append(channels, 0, 0, []byte{64, 128, 192, 255}[index])
			}
		}
		records = append(records, []byte("8BIMnorm\xff\x00\x00\x00")...)
		extra := append(make([]byte, 8), 1, 'L', 0, 0)
		if divider != 0 {
			extra = append(extra, bridgeSmartTag("lsct", binary.BigEndian.AppendUint32(nil, divider), false, 2)...)
		}
		if divider == 1 {
			extra = append(extra, board...)
		}
		records = binary.BigEndian.AppendUint32(records, uint32(len(extra)))
		records = append(records, extra...)
	}
	records = append(records, channels...)
	if len(records)%2 != 0 {
		records = append(records, 0)
	}
	section := append(length(nil, len(records)), records...)
	section = append(section, 0, 0, 0, 0)
	section = append(section, global...)
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 8)...)
	data = append(length(data, len(section)), section...)
	return append(data, 0, 0, 0, 0, 0)
}

func TestArtboardAutoSaveBridgeWithoutCompositeImage(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d/c%d", version, compression), func(t *testing.T) {
				engine, err := New(Config{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
				input := bridgeArtboardFixture(t, version)
				asset := bridgeAsset(t, engine, "artboard.psd", input)
				opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
				editor := opened["editor"].(string)
				for _, step := range []struct {
					opacity byte
					pixel   color.NRGBA
				}{{128, color.NRGBA{R: 159, G: 191, B: 223, A: 255}},
					{64, color.NRGBA{R: 207, G: 223, B: 239, A: 255}}} {
					layers := blendingBridgeResult[[]psdedit.LayerInfo](t, bridgeCall(t, engine, "editor.Tree", editor))
					if len(layers) != 1 || !layers[0].Group || len(layers[0].Children) != 1 {
						t.Fatal("browser artboard tree changed")
					}
					bridgeCall(t, engine, "editor.SetOpacity", editor, layers[0].Children[0].ID, step.opacity)
					blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", editor), step.pixel)
					blendingBridgePixel(t, bridgeCall(t, engine, "editor.CompositeGroup", editor, layers[0].ID), step.pixel)
					saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{"Compression": compression})
					if len(saved.Binaries) != 1 {
						t.Fatal("browser automatic artboard save did not return a file")
					}
					parsed, err := psdfile.Parse(saved.Binaries[0].Data)
					if err != nil {
						t.Fatal(err)
					}
					if parsed.Header.Version != version || len(parsed.Layers) != 3 {
						t.Fatal("artboard save wrote synthetic background layers or changed format")
					}
					_ = parsed.Close()
					written := bridgeAsset(t, engine, "saved.psd", saved.Binaries[0].Data)
					reopened := bridgeCall(t, engine, "document.open", "", assetRef(written)).Data.(map[string]any)
					editor = reopened["editor"].(string)
					blendingBridgePixel(t, bridgeCall(t, engine, "editor.Composite", editor), step.pixel)
					blendingBridgePixel(t, bridgeCall(t, engine, "preview.saved", "", ref(reopened["document"].(string))), step.pixel)
					blendingBridgePixel(t, bridgeCall(t, engine, "render.source", "", ref(reopened["document"].(string))), step.pixel)
				}
				if !bytes.Equal(engine.assets[asset].data, input) {
					t.Fatal("automatic artboard saving modified the original asset")
				}
			})
		}
	}
}
