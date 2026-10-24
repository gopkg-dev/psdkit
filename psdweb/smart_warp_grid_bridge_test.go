package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image"
	"reflect"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func bridgeGridControls(order, round int) []psdmodel.Point {
	points := make([]psdmodel.Point, order*order)
	for row := range order {
		for column := range order {
			points[row*order+column] = psdmodel.Point{
				X: float64(column) * 8 / float64(order-1), Y: float64(row) * 8 / float64(order-1)}
		}
	}
	for index := 2; index <= 4; index++ {
		if round == 0 {
			points[index].Y = .4
		} else {
			points[index*order].X = .4
		}
	}
	return points
}

func bridgeGridDescriptor(order int, guard float64) *psdfile.Descriptor {
	points := bridgeGridControls(order, 0)
	for index := range points {
		points[index].Y = float64(index/order) * 8 / float64(order-1)
	}
	warp := bridgeWarpDescriptor(points)
	warp.ClassID = "quiltWarp"
	warp.Entries = append(warp.Entries,
		psdfile.DescriptorEntry{Key: "deformNumRows", Type: "long", Value: int32(order)},
		psdfile.DescriptorEntry{Key: "deformNumCols", Type: "long", Value: int32(order)})
	for _, entry := range warp.Entries {
		if entry.Key != "customEnvelopeWarp" {
			continue
		}
		envelope := entry.Value.(*psdfile.Descriptor)
		envelope.Entries[0].Value.(*psdfile.DescriptorObjectArray).Prefix = uint32(order * order)
		count := (order - 1) / 3
		for _, axis := range []string{"quiltSliceX", "quiltSliceY"} {
			values := make([]float64, count+1)
			for index := range values {
				values[index] = 8 * float64(index) / float64(count)
			}
			values[0], values[count] = -guard, 8+guard
			envelope.Entries = append(envelope.Entries,
				psdfile.DescriptorEntry{Key: axis, Type: "ObAr", Value: &psdfile.DescriptorObjectArray{
					ClassID: "UntF", Prefix: uint32(count + 1), Entries: []psdfile.DescriptorEntry{
						{Key: axis, Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: values}},
					}}})
		}
	}
	return warp
}

// bridgeGridFixture upgrades only the independent fixture's modern placement descriptors.
func bridgeGridFixture(t *testing.T, version uint16, order int, guard float64, external bool) ([]byte, []byte) {
	t.Helper()
	data, contents := bridgeWarpFixture(t, version, external, false)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, record := range slices.Backward(source.Layers) {
		for _, block := range record.AdditionalInfo {
			if block.Key != "SoLd" {
				continue
			}
			descriptor, err := source.DecodeDescriptorContext(t.Context(),
				psdfile.Span{Offset: block.Data.Offset + 8, Length: block.Data.Length - 8}, true)
			if err != nil {
				t.Fatal(err)
			}
			for field := range descriptor.Entries {
				if descriptor.Entries[field].Key == "warp" {
					descriptor.Entries[field].Key = "quiltWarp"
					descriptor.Entries[field].Value = bridgeGridDescriptor(order, guard)
				}
			}
			encoded, err := source.EncodeDescriptor(t.Context(), descriptor, true)
			if err != nil {
				t.Fatal(err)
			}
			payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
			replacement := bridgeSmartTag("SoLd", payload, false, 2)
			delta := int64(len(replacement)) - block.Span.Length
			for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
				if version == 2 {
					binary.BigEndian.PutUint64(data[offset:], uint64(int64(binary.BigEndian.Uint64(data[offset:]))+delta))
				} else {
					binary.BigEndian.PutUint32(data[offset:], uint32(int64(binary.BigEndian.Uint32(data[offset:]))+delta))
				}
			}
			binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length+delta))
			result := append(bytes.Clone(data[:block.Span.Offset]), replacement...)
			data = append(result, data[block.Span.Offset+block.Span.Length:]...)
		}
	}
	return data, contents
}

func bridgeGridRequest(
	t *testing.T, engine *Engine, operation, target string, links map[string]string, arguments ...any,
) Response {
	t.Helper()
	request := Request{Operation: operation, Target: target, Links: links}
	for _, argument := range arguments {
		encoded, err := json.Marshal(argument)
		if err != nil {
			t.Fatal(err)
		}
		request.Args = append(request.Args, encoded)
	}
	return engine.Execute(t.Context(), request, nil)
}

func verifyBridgeWarpSource(
	t *testing.T, engine *Engine, opened map[string]any, links map[string]string, live []byte,
) {
	t.Helper()
	policy := map[string]any{"ApproximateCustomWarp": true}
	response := bridgeGridRequest(t, engine, "render.source", "", links,
		ref(opened["document"].(string)), policy)
	if !response.OK {
		t.Fatalf("saved Warp source redraw differs from edited artwork: %+v", response.Error)
	}
	fresh := bridgeWarpPixels(t, response)
	// Only the selected instance's saved artwork changes; the peer deliberately retains its poisoned old cache.
	// Source redraw ignores that peer cache, so compare the edited instance's visible premultiplied result.
	for y := range 8 {
		for x := range 8 {
			offset := (y*16 + x) * 4
			if fresh[offset+3] != live[offset+3] || fresh[offset+3] != 0 &&
				!bytes.Equal(fresh[offset:offset+3], live[offset:offset+3]) {
				t.Fatalf("fresh edited Warp pixel at %d,%d = %v, saved artwork = %v", x, y,
					fresh[offset:offset+4], live[offset:offset+4])
			}
		}
	}
	blocked := bridgeGridRequest(t, engine, "render.source", "", links,
		ref(opened["document"].(string)), map[string]any{})
	if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
		t.Fatal("fresh Warp source redraw implicitly enabled approximation")
	}
	region := image.Rect(1, 1, 6, 7)
	response = bridgeGridRequest(t, engine, "render.tile", "", links,
		ref(opened["document"].(string)), region, policy)
	if !response.OK {
		t.Fatalf("Warp tile failed: %+v", response.Error)
	}
	var packed []byte
	for _, item := range response.Binaries {
		if item.MIME == "application/x-rgba8" {
			packed = item.Data
		}
	}
	if len(packed) != region.Dx()*region.Dy()*4 {
		t.Fatal("Warp tile omitted its requested RGBA area")
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			at, full := ((y-region.Min.Y)*region.Dx()+x-region.Min.X)*4, (y*16+x)*4
			if !bytes.Equal(packed[at:at+4], fresh[full:full+4]) {
				t.Fatal("Warp tile changed sampling phase or lost source support")
			}
		}
	}
}

func TestSmartWarpGridBridgeEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, guard := range []float64{0, .6} {
			for _, version := range []uint16{1, 2} {
				for compression := range 4 {
					for _, external := range []bool{false, true} {
						name := fmt.Sprintf("order%d/guard%g/v%d/c%d/external%t", order, guard, version, compression, external)
						t.Run(name, func(t *testing.T) {
							engine, err := New(Config{})
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
							original, contents := bridgeGridFixture(t, version, order, guard, external)
							asset := bridgeAsset(t, engine, "uniform-grid.psd", original)
							opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
							var previous []byte
							for round := range 2 {
								editor := opened["editor"].(string)
								infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
									bridgeCall(t, engine, "editor.SmartObjects", editor))
								var links map[string]string
								if external {
									links = map[string]string{infos[0].SourceID: bridgeAsset(t, engine, "source.psd", contents)}
								}
								points := bridgeGridControls(order, round)
								before := blendingBridgeResult[psdedit.Changes](t,
									bridgeCall(t, engine, "editor.Changes", editor))
								blocked := bridgeGridRequest(t, engine, "editor.SetSmartObjectWarp", editor, links,
									infos[0].ID, points, map[string]any{"Render": map[string]any{}})
								if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
									t.Fatal("uniform grid edit inferred its mathematical sampling policy")
								}
								if external {
									blocked = bridgeGridRequest(t, engine, "editor.SetSmartObjectWarp", editor, nil,
										infos[0].ID, points, map[string]any{"Render": map[string]any{"ApproximateCustomWarp": true}})
									if blocked.OK || blocked.Error == nil || len(blocked.Binaries) != 0 {
										t.Fatal("uniform grid edit used an external source without explicit mapping")
									}
								}
								if after := blendingBridgeResult[psdedit.Changes](t,
									bridgeCall(t, engine, "editor.Changes", editor)); after != before {
									t.Fatal("rejected grid policy committed partial control points or artwork")
								}
								bridgeSmartCall(t, engine, "SetSmartObjectWarp", editor, links, infos[0].ID, points,
									map[string]any{"Render": map[string]any{"ApproximateCustomWarp": true}})
								saved := bridgeCall(t, engine, "editor.WritePSD", editor,
									map[string]any{"Compression": compression})
								if len(saved.Binaries) != 1 {
									t.Fatal("uniform grid did not save its generated current artwork")
								}
								live := bytes.Clone(bridgeWarpPixels(t, bridgeCall(t, engine, "editor.Composite", editor)))
								if previous != nil && bytes.Equal(previous, live) {
									t.Fatal("continued grid editing did not change visible curvature")
								}
								verifyBridgeWarpSaved(t, saved.Binaries[0].Data, original, live, version, points)
								parsed, err := psdfile.Parse(saved.Binaries[0].Data)
								if err != nil {
									t.Fatal(err)
								}
								metadata, err := parsed.ReadLayerMetadata(t.Context(), 0)
								if err != nil {
									t.Fatal(err)
								}
								warp := metadata.Layer.Placed.Warp
								if warp.DeformRows != order || warp.DeformColumns != order || len(warp.QuiltSlices) != 2 ||
									warp.QuiltSlices[0].Values[0] != -guard ||
									warp.QuiltSlices[0].Values[len(warp.QuiltSlices[0].Values)-1] != 8+guard {
									t.Fatal("grid edit changed order or physical source slice guards")
								}
								_ = parsed.Close()
								output := bridgeAsset(t, engine, "uniform-grid-edited.psd", saved.Binaries[0].Data)
								opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
								verifyBridgeWarpSource(t, engine, opened, links, live)
								reopened := bridgeWarpPixels(t,
									bridgeCall(t, engine, "editor.Composite", opened["editor"].(string)))
								if !reflect.DeepEqual(live, reopened) {
									t.Fatal("uniform grid artwork changed after reopening")
								}
								previous = live
								bridgeCall(t, engine, "resource.release", editor)
							}
							if !bytes.Equal(original, engine.assets[asset].data) {
								t.Fatal("uniform grid edit mutated the imported original document")
							}
						})
					}
				}
			}
		}
	}
}
