package psdweb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func bridgeWarpControls(round int) []psdmodel.Point {
	points := make([]psdmodel.Point, 0, 16)
	for row := range 4 {
		for column := range 4 {
			points = append(points, psdmodel.Point{X: float64(column) * 8 / 3, Y: float64(row) * 8 / 3})
		}
	}
	if round == 0 {
		points[1].Y, points[2].Y = .5, .5
		points[5].X += .25
	} else {
		points[4].X, points[8].X = .5, .5
		points[10].Y -= .25
	}
	return points
}

func bridgeWarpDescriptor(points []psdmodel.Point) *psdfile.Descriptor {
	horizontal, vertical := make([]float64, len(points)), make([]float64, len(points))
	for index, point := range points {
		horizontal[index], vertical[index] = point.X, point.Y
	}
	mesh := &psdfile.DescriptorObjectArray{ClassID: "rationalPoint", Prefix: 16,
		Entries: []psdfile.DescriptorEntry{
			{Key: "Hrzn", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: horizontal}},
			{Key: "Vrtc", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: vertical}},
		}}
	return &psdfile.Descriptor{ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpCustom"}},
		{Key: "warpValue", Type: "doub", Value: float64(0)},
		{Key: "warpPerspective", Type: "doub", Value: float64(0)},
		{Key: "warpPerspectiveOther", Type: "doub", Value: float64(0)},
		{Key: "warpRotate", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "uOrder", Type: "long", Value: int32(4)}, {Key: "vOrder", Type: "long", Value: int32(4)},
		{Key: "bounds", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "classFloatRect",
			Entries: []psdfile.DescriptorEntry{
				{Key: "Left", Type: "doub", Value: float64(0)}, {Key: "Top ", Type: "doub", Value: float64(0)},
				{Key: "Rght", Type: "doub", Value: float64(8)}, {Key: "Btom", Type: "doub", Value: float64(8)},
			}}},
		{Key: "customEnvelopeWarp", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "customEnvelopeWarp",
			Entries: []psdfile.DescriptorEntry{{Key: "meshPoints", Type: "ObAr", Value: mesh}}}},
	}}
}

// bridgeWarpFixture encodes two shared single-patch instances without the production parent writer.
func bridgeWarpFixture(t *testing.T, version uint16, external, projective bool) ([]byte, []byte) {
	t.Helper()
	pixels := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			pixels.SetNRGBA(x, y, color.NRGBA{R: byte(10 + x*30), G: byte(20 + y*30), B: 173, A: 191})
		}
	}
	child, err := psdedit.BuildRasterContents(t.Context(), pixels)
	if err != nil {
		t.Fatal(err)
	}
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	var records, channels []byte
	for instance := range 2 {
		left := instance * 8
		quad := []float64{float64(left), 0, float64(left + 8), 0, float64(left + 8), 8, float64(left), 8}
		if projective && instance == 0 {
			quad[6] += .25
		}
		transform := make([]psdfile.DescriptorValue, len(quad))
		for index, value := range quad {
			transform[index] = psdfile.DescriptorValue{Type: "doub", Value: value}
		}
		points := make([]psdmodel.Point, 0, 16)
		for row := range 4 {
			for column := range 4 {
				points = append(points, psdmodel.Point{X: float64(column) * 8 / 3, Y: float64(row) * 8 / 3})
			}
		}
		descriptor := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
			{Key: "Idnt", Type: "TEXT", Value: "warp-source"}, {Key: "Type", Type: "long", Value: int32(2)},
			{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size",
				Entries: []psdfile.DescriptorEntry{
					{Key: "Wdth", Type: "doub", Value: float64(8)}, {Key: "Hght", Type: "doub", Value: float64(8)},
				}}},
			{Key: "Trnf", Type: "VlLs", Value: transform},
			{Key: "warp", Type: "Objc", Value: bridgeWarpDescriptor(points)},
			{Key: "opaquePlacement", Type: "tdta", Value: []byte{0x76, 0x19, 0x32}},
		}}
		encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
		if err != nil {
			t.Fatal(err)
		}
		placement := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
		for _, value := range []uint32{0, uint32(left), 8, uint32(left + 8)} {
			records = binary.BigEndian.AppendUint32(records, value)
		}
		records = binary.BigEndian.AppendUint16(records, 4)
		for _, channel := range []int16{0, 1, 2, -1} {
			records = binary.BigEndian.AppendUint16(records, uint16(channel))
			records = appendLength(records, 66)
			value := byte(255)
			if channel == 1 {
				value = 0
			}
			channels = append(channels, 0, 0)
			channels = append(channels, bytes.Repeat([]byte{value}, 64)...)
		}
		records = append(records, []byte("8BIMnorm\xff\x00\x00\x00")...)
		extra := append(make([]byte, 8), 1, 'W', 0, 0)
		extra = append(extra, bridgeSmartTag("SoLd", placement, false, 2)...)
		extra = append(extra, bridgeSmartTag("pRsv", []byte{0x38, 0x74, 0x29}, false, 2)...)
		records = binary.BigEndian.AppendUint32(records, uint32(len(extra)))
		records = append(records, extra...)
	}
	layers := append(binary.BigEndian.AppendUint16(nil, 0xfffe), records...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	kind, key := "liFD", "lnk2"
	if external {
		kind, key = "liFE", "lnkE"
	}
	body := binary.BigEndian.AppendUint32([]byte(kind), 3)
	body = append(body, byte(len("warp-source")))
	body = append(body, "warp-source"...)
	body = binary.BigEndian.AppendUint32(body, 6)
	for _, unit := range "source" {
		body = binary.BigEndian.AppendUint16(body, uint16(unit))
	}
	body = append(body, "8BPS\x00\x00\x00\x00"...)
	body = binary.BigEndian.AppendUint64(body, uint64(len(child)))
	body = append(body, 0)
	if external {
		descriptor, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "null"}, true)
		if err != nil {
			t.Fatal(err)
		}
		body = append(body, descriptor...)
		body = binary.BigEndian.AppendUint64(body, uint64(len(child)))
	}
	body = append(body, child...)
	entry := append(binary.BigEndian.AppendUint64(nil, uint64(len(body))), body...)
	entry = append(entry, make([]byte, (4-len(body)%4)%4)...)
	section := append(appendLength(nil, len(layers)), layers...)
	section = binary.BigEndian.AppendUint32(section, 0)
	section = append(section, bridgeSmartTag(key, entry, version == 2 && key == "lnk2", 4)...)
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 4)
	header = binary.BigEndian.AppendUint32(header, 8)
	header = binary.BigEndian.AppendUint32(header, 16)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = append(header, make([]byte, 8)...)
	header = appendLength(header, len(section))
	header = append(header, section...)
	header = append(header, 0, 0)
	return append(header, bytes.Repeat([]byte{23}, 16*8*4)...), child
}

func bridgeWarpPixels(t *testing.T, response Response) []byte {
	t.Helper()
	for _, item := range response.Binaries {
		if item.MIME == "application/x-rgba8" && len(item.Data) == 16*8*4 {
			return item.Data
		}
	}
	t.Fatal("Warp edit did not produce parent canvas pixels")
	return nil
}

func verifyBridgeWarpSaved(t *testing.T, data, original, live []byte, version uint16, points []psdmodel.Point) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	before, err := psdfile.Parse(original)
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	metadata, err := source.ReadLayerMetadata(t.Context(), 0)
	if err != nil || source.Header.Version != version || metadata.Layer.Placed == nil ||
		!reflect.DeepEqual(metadata.Layer.Placed.Warp.MeshPoints, points) {
		t.Fatalf("saved Warp points or format changed: %v", err)
	}
	for _, block := range before.Layers[1].AdditionalInfo {
		var current psdfile.AdditionalBlock
		for _, candidate := range source.Layers[1].AdditionalInfo {
			if candidate.Key == block.Key {
				current = candidate
			}
		}
		oldBytes, oldErr := before.ReadSpan(block.Span)
		newBytes, newErr := source.ReadSpan(current.Span)
		if oldErr != nil || newErr != nil || !bytes.Equal(oldBytes, newBytes) {
			t.Fatal("single-instance Warp edit changed a shared peer's descriptors")
		}
	}
	oldLinks, err := before.LinkedFiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	newLinks, err := source.LinkedFiles(t.Context())
	if err != nil || len(newLinks) != 1 || len(oldLinks) != 1 || newLinks[0].Kind != oldLinks[0].Kind {
		t.Fatalf("single-instance Warp edit changed shared source identity: %v", err)
	}
	oldBytes, oldErr := before.ReadSpan(oldLinks[0].Span)
	newBytes, newErr := source.ReadSpan(newLinks[0].Span)
	if oldErr != nil || newErr != nil || !bytes.Equal(oldBytes, newBytes) {
		t.Fatal("single-instance Warp edit changed shared original source bytes")
	}
	merged, err := source.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("Warp save omitted its current merged preview: %v", err)
	}
	for index := range 16 * 8 {
		alpha := live[index*4+3]
		if merged.Channels[3].Data[index] != alpha {
			t.Fatal("Warp saved preview alpha differs from live output")
		}
		for component := range 3 {
			value := live[index*4+component]
			want := byte(math.RoundToEven(float64(value)*float64(alpha)/255 + 255 - float64(alpha)))
			if merged.Channels[component].Data[index] != want {
				t.Fatal("Warp saved preview RGB differs from live output")
			}
		}
	}
}

func TestSmartWarpBridgeEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range 4 {
			for _, external := range []bool{false, true} {
				for _, projective := range []bool{false, true} {
					t.Run(fmt.Sprintf("v%d/c%d/external%t/projective%t", version, compression, external, projective),
						func(t *testing.T) {
							engine, err := New(Config{})
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
							original, contents := bridgeWarpFixture(t, version, external, projective)
							asset := bridgeAsset(t, engine, "custom-Warp.psd", original)
							opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
							var previous []byte
							for round := range 2 {
								editor := opened["editor"].(string)
								infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
									bridgeCall(t, engine, "editor.SmartObjects", editor))
								points := bridgeWarpControls(round)
								options := map[string]any{}
								var links map[string]string
								if external {
									links = map[string]string{infos[0].SourceID: bridgeAsset(t, engine, "source.psd", contents)}
								}
								if external || projective {
									before := blendingBridgeResult[psdedit.Changes](t,
										bridgeCall(t, engine, "editor.Changes", editor))
									blocked := bridgeContentsFilterRequest(t, engine, "SetSmartObjectWarp", editor,
										infos[0].ID, points, options)
									if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
										t.Fatal("Warp edit implicitly resolved external contents or enabled approximation")
									}
									if after := blendingBridgeResult[psdedit.Changes](t,
										bridgeCall(t, engine, "editor.Changes", editor)); before != after {
										t.Fatal("failed Warp policy check committed partial state")
									}
									options["Render"] = map[string]any{"ApproximateCustomWarp": projective}
								}
								bridgeSmartCall(t, engine, "SetSmartObjectWarp", editor, links, infos[0].ID, points, options)
								saved := bridgeCall(t, engine, "editor.WritePSD", editor,
									map[string]any{"Compression": compression})
								if len(saved.Binaries) != 1 {
									t.Fatal("Warp edit did not save directly from current generated source pixels")
								}
								live := bytes.Clone(bridgeWarpPixels(t, bridgeCall(t, engine, "editor.Composite", editor)))
								if previous != nil && bytes.Equal(previous, live) {
									t.Fatal("continued Warp edit did not update its pixels")
								}
								verifyBridgeWarpSaved(t, saved.Binaries[0].Data, original, live, version, points)
								output := bridgeAsset(t, engine, "custom-Warp-edited.psd", saved.Binaries[0].Data)
								opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
								reopened := bridgeWarpPixels(t,
									bridgeCall(t, engine, "editor.Composite", opened["editor"].(string)))
								if !bytes.Equal(live, reopened) {
									t.Fatal("Warp saved instance pixels changed after reopening")
								}
								previous = live
								bridgeCall(t, engine, "resource.release", editor)
							}
							if !bytes.Equal(original, engine.assets[asset].data) {
								t.Fatal("Warp editing changed its imported source asset")
							}
						})
				}
			}
		}
	}
}
