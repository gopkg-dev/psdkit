package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func TestCloneSmartPlacementOwnsAndChargesWarpMetadata(t *testing.T) {
	warp := &psdmodel.Warp{Style: "warpArcLower", StyleEnum: "warpStyle.warpArcLower",
		Rotate: "Diagonal", RotateEnum: "Ornt.Diagonal", Value: 68.8, HasValue: true,
		Bounds:    &psdmodel.WarpBounds{Right: 2, Bottom: 2, Pixels: false, Units: [4]string{"#Prc"}},
		MeshUnits: [2]string{"#Prc", "#Pxl"}, Fields: []string{"futureProperty", "warpStyle"},
		Kinds: []string{"tdta", "enum"}, Unsupported: []string{strings.Repeat("future warp field", 1024)}}
	original := &psdmodel.Placed{Width: 2, Height: 2, Warp: warp, Unsupported: []string{"independent reason"}}
	cloned, charge := cloneSmartPlacement(original, 4, 4)
	if charge < int64(len(warp.Unsupported[0])) ||
		smartProjectionMetadataBytes(original) < int64(len(warp.Unsupported[0])) {
		t.Fatal("retained warp audit fields were omitted from metadata charge")
	}
	cloned.Warp.Fields[0], cloned.Warp.Kinds[0], cloned.Warp.Unsupported[0] = "changed", "bool", "changed"
	cloned.Warp.Bounds.Units[0], cloned.Unsupported[0] = "#Pxl", "changed"
	if warp.Fields[0] != "futureProperty" || warp.Kinds[0] != "tdta" ||
		len(warp.Unsupported[0]) < 1024 || warp.Bounds.Units[0] != "#Prc" ||
		original.Unsupported[0] != "independent reason" {
		t.Fatal("owned smart placement aliases warp audit fields")
	}
}

func TestCloneSmartPlacementOwnsAndChargesOriginalQuiltSlices(t *testing.T) {
	source := &psdmodel.Placed{Width: 413, Height: 413, Warp: &psdmodel.Warp{
		ClassID: "quiltWarp", Style: "custom", Split: true,
		DeformRows: 10, DeformColumns: 10, HasDeformRows: true, HasDeformColumns: true,
		Bounds: &psdmodel.WarpBounds{Right: 413, Bottom: 413, Pixels: true},
		QuiltSlices: []psdmodel.WarpSlice{{
			Axis: "quiltSliceX", Location: "envelope", ClassID: "UntF", Prefix: 4,
			Field: "quiltSliceX", Kind: "UnFl", Unit: "#Pxl", Values: []float64{-0.6, 413.0 / 3, 826.0 / 3, 413.6},
		}},
	}}
	cloned, charged := cloneSmartPlacement(source, 826, 826)
	if !cloned.Warp.Split || cloned.Warp.DeformRows != 10 || cloned.Warp.DeformColumns != 10 ||
		!cloned.Warp.HasDeformRows || !cloned.Warp.HasDeformColumns ||
		!slices.Equal(cloned.Warp.QuiltSlices[0].Values, source.Warp.QuiltSlices[0].Values) {
		t.Fatal("placement clone discarded quilt presence or silently rescaled its original slice domain")
	}
	without := *source.Warp
	without.QuiltSlices = nil
	plain := *source
	plain.Warp = &without
	_, baseCharge := cloneSmartPlacement(&plain, 826, 826)
	slice := cloned.Warp.QuiltSlices[0]
	expected := int64(unsafe.Sizeof(psdmodel.WarpSlice{})) + int64(len(slice.Values))*8 +
		int64(len(slice.Axis)+len(slice.Location)+len(slice.ClassID)+len(slice.Field)+len(slice.Kind)+len(slice.Unit))
	if charged-baseCharge < expected || smartProjectionMetadataBytes(source)-smartProjectionMetadataBytes(&plain) < expected {
		t.Fatal("retained quilt columns or their independently owned coordinate arrays escaped the working budget")
	}
	cloned.Warp.QuiltSlices[0].Values[1] = 999
	cloned.Warp.QuiltSlices[0].Unit = "#Prc"
	cloned.Warp.QuiltSlices = append(cloned.Warp.QuiltSlices, psdmodel.WarpSlice{Axis: "quiltSliceY"})
	if source.Warp.QuiltSlices[0].Values[1] != 413.0/3 || source.Warp.QuiltSlices[0].Unit != "#Pxl" ||
		len(source.Warp.QuiltSlices) != 1 {
		t.Fatal("cloned quilt columns or coordinates alias the source placement")
	}
}

func TestUnknownWarpFieldsPreserveSourceOrderedDescriptorBytesOnSave(t *testing.T) {
	warp := &psdfile.Descriptor{ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "futureProperty", Type: "tdta", Value: []byte{0, 255, 7, 19}},
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpNone"}},
		{Key: "warpRotate", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Diagonal"}},
		{Key: "futureProperty", Type: "bool", Value: true},
		{Key: "warpValue", Type: "doub", Value: float64(0)},
	}}
	quad := []psdfile.DescriptorValue{
		{Type: "doub", Value: float64(0)}, {Type: "doub", Value: float64(0)},
		{Type: "doub", Value: float64(2)}, {Type: "doub", Value: float64(0)},
		{Type: "doub", Value: float64(2)}, {Type: "doub", Value: float64(2)},
		{Type: "doub", Value: float64(0)}, {Type: "doub", Value: float64(2)},
	}
	descriptor := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: "warp-test-source"},
		{Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(2)},
		}}},
		{Key: "Trnf", Type: "VlLs", Value: quad}, {Key: "warp", Type: "Objc", Value: warp},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	record, channels := smartEditLayer(1, "Unknown Warp", 10, pixels.Rect, pixels,
		writerLinkedTag("8BIM", "SoLd", 4, payload, 2), false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	data := smartEditDocument(1, 2, 2, 3, layers, nil, append([]byte{0, 0}, make([]byte, 12)...))
	editor, source := smartEditOpen(t, data)
	view, err := source.ReadLayerMetadata(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	metadata := view.Layer.Placed.Warp
	if !slices.Equal(metadata.Fields,
		[]string{"futureProperty", "warpStyle", "warpRotate", "futureProperty", "warpValue"}) ||
		metadata.Rotate != "Diagonal" || metadata.RotateEnum != "Ornt.Diagonal" || len(metadata.Unsupported) == 0 ||
		!psdrender.RequiresWarpRendering(view.Layer.Placed) {
		t.Fatal("unknown warp metadata became a valid flat placement")
	}
	if _, err := source.ReadLayerMetadata(nil, 0,
		psdfile.MetadataOptions{MaxBytes: view.WorkingBytes - 1}); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("owned metadata and parser-scratch budget was not charged: %v", err)
	}
	var saved bytes.Buffer
	if err := editor.Write(nil, &saved); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved.Bytes(), data) {
		t.Fatal("saving unsupported metadata rewrote unknown bytes, duplicates or source order")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if metadata.Fields[0] != "futureProperty" || metadata.RotateEnum != "Ornt.Diagonal" {
		t.Fatal("owned warp metadata depended on the closed original source")
	}
}
