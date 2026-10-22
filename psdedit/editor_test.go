package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type editorFixtureLayer struct {
	name    string
	id      uint32
	divider uint32
	tag     string
}

func editorFixtureNumber(target *bytes.Buffer, value any) {
	_ = binary.Write(target, binary.BigEndian, value)
}

func editorFixtureTag(target *bytes.Buffer, key string, data []byte) {
	target.WriteString("8BIM")
	target.WriteString(key)
	editorFixtureNumber(target, uint32(len(data)))
	target.Write(data)
	if len(data)%2 != 0 {
		target.WriteByte(0)
	}
}

func editorFixtureBytes(layers []editorFixtureLayer, depth, mode uint16, seed uint32) []byte {
	var records, payloads bytes.Buffer
	if len(layers) != 0 {
		editorFixtureNumber(&records, int16(-len(layers)))
	}
	for _, layer := range layers {
		width, height := int32(2), int32(2)
		channelCount := uint16(3)
		if layer.divider != 0 {
			width, height, channelCount = 0, 0, 0
		}
		for _, coordinate := range []int32{0, 0, height, width} {
			editorFixtureNumber(&records, coordinate)
		}
		editorFixtureNumber(&records, channelCount)
		channelBytes := int(width*height) * int(depth/8)
		for index := uint16(0); index < channelCount; index++ {
			editorFixtureNumber(&records, int16(index))
			editorFixtureNumber(&records, uint32(channelBytes+2))
			editorFixtureNumber(&payloads, uint16(0))
			payloads.Write(bytes.Repeat([]byte{byte(index + 1)}, channelBytes))
		}
		records.WriteString("8BIMnorm")
		records.Write([]byte{255, 0, 0, 0})
		var extra bytes.Buffer
		editorFixtureNumber(&extra, uint32(0))
		editorFixtureNumber(&extra, uint32(0))
		extra.WriteByte(byte(len(layer.name)))
		extra.WriteString(layer.name)
		for padding := (4 - (1+len(layer.name))%4) % 4; padding > 0; padding-- {
			extra.WriteByte(0)
		}
		if layer.id != 0 {
			var data [4]byte
			binary.BigEndian.PutUint32(data[:], layer.id)
			editorFixtureTag(&extra, "lyid", data[:])
		}
		if layer.divider != 0 {
			var data [4]byte
			binary.BigEndian.PutUint32(data[:], layer.divider)
			editorFixtureTag(&extra, "lsct", data[:])
		}
		if layer.tag != "" {
			editorFixtureTag(&extra, layer.tag, []byte{0, 0, 0, 0})
		}
		editorFixtureNumber(&records, uint32(extra.Len()))
		records.Write(extra.Bytes())
	}
	records.Write(payloads.Bytes())
	if records.Len()%2 != 0 {
		records.WriteByte(0)
	}
	var layerMask bytes.Buffer
	if records.Len() != 0 {
		editorFixtureNumber(&layerMask, uint32(records.Len()))
		layerMask.Write(records.Bytes())
		editorFixtureNumber(&layerMask, uint32(0))
	}
	var resources bytes.Buffer
	if seed != 0 {
		resources.WriteString("8BIM")
		editorFixtureNumber(&resources, uint16(1044))
		resources.Write([]byte{0, 0})
		editorFixtureNumber(&resources, uint32(4))
		editorFixtureNumber(&resources, seed)
	}
	var document bytes.Buffer
	document.WriteString("8BPS")
	editorFixtureNumber(&document, uint16(1))
	document.Write(make([]byte, 6))
	editorFixtureNumber(&document, uint16(3))
	editorFixtureNumber(&document, uint32(2))
	editorFixtureNumber(&document, uint32(2))
	editorFixtureNumber(&document, depth)
	editorFixtureNumber(&document, mode)
	editorFixtureNumber(&document, uint32(0))
	editorFixtureNumber(&document, uint32(resources.Len()))
	document.Write(resources.Bytes())
	editorFixtureNumber(&document, uint32(layerMask.Len()))
	document.Write(layerMask.Bytes())
	editorFixtureNumber(&document, uint16(0))
	document.Write(bytes.Repeat([]byte{17}, 12*int(depth/8)))
	return document.Bytes()
}

func editorFixtureOpen(
	t *testing.T, layers []editorFixtureLayer, depth, mode uint16, seed uint32, options ...psdfile.LoadOptions,
) (*Editor, *psdfile.Document) {
	t.Helper()
	source, err := psdfile.Parse(editorFixtureBytes(layers, depth, mode, seed), options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	return editor, source
}

func editorFixtureImage() *image.NRGBA {
	return &image.NRGBA{
		Rect: image.Rect(10, 20, 12, 22), Stride: 12,
		Pix: []byte{1, 2, 3, 4, 5, 6, 7, 8, 99, 99, 99, 99, 9, 10, 11, 12, 13, 14, 15, 16},
	}
}

func TestEditorIndexesNestedPhysicalGroupsAndOwnedViews(t *testing.T) {
	editor, source := editorFixtureOpen(t, []editorFixtureLayer{
		{name: "bottom", id: 10},
		{name: "outer-end", id: 20, divider: 3},
		{name: "child", id: 30},
		{name: "inner-end", id: 40, divider: 3},
		{name: "leaf", id: 50},
		{name: "inner", id: 60, divider: 2},
		{name: "outer", id: 70, divider: 1},
		{name: "top", id: 80},
	}, 8, 3, 100)
	tree, err := editor.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if len(tree) != 3 || tree[1].ID != 7 || !tree[1].Expanded || len(tree[1].Children) != 2 ||
		tree[1].Children[1].ID != 6 || tree[1].Children[1].Expanded ||
		tree[1].Children[1].Children[0].ID != 5 || tree[1].Children[0].Parent != 7 {
		t.Fatalf("unexpected hierarchy: %+v", tree)
	}
	tree[1].Children[0].Name = "caller mutation"
	child, err := editor.Layer(3)
	if err != nil || child.Name != "child" || child.Parent != 7 || source.Layers[2].Name != "child" {
		t.Fatalf("view aliases source or editor: %+v %v", child, err)
	}
	if _, err := editor.Layer(2); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("end marker is public: %v", err)
	}
	flat, err := editor.Layers()
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []LayerID{1, 3, 5, 6, 7, 8} {
		if flat[index].ID != id || len(flat[index].Children) != 0 {
			t.Fatalf("flat metadata order changed: %+v", flat)
		}
	}
	physical := editor.writeLayersLocked()
	for index, layer := range physical {
		if layer.sourceIndex != index {
			t.Fatalf("source record order changed at %d: %+v", index, layer)
		}
	}
	if !physical[1].end || !physical[3].end || physical[5].divider != 2 || physical[6].divider != 1 {
		t.Fatalf("writer hierarchy markers changed: %+v", physical)
	}
}

func TestEditorRevisionNoOpsAndSourceImmutability(t *testing.T) {
	editor, source := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 8, 3, 0)
	for _, operation := range []func() error{
		func() error { return editor.Rename(1, "Layer") },
		func() error { return editor.SetVisible(1, true) },
		func() error { return editor.SetOpacity(1, 255) },
		func() error { return editor.Move(1, 0, 0) },
	} {
		if err := operation(); err != nil {
			t.Fatal(err)
		}
	}
	changes, _ := editor.Changes()
	if changes.Revision != 0 || changes.Dirty || changes.CompositeChanged {
		t.Fatalf("no-op advanced revision: %+v", changes)
	}
	if err := editor.Rename(1, "中文名字 😀"); err != nil {
		t.Fatal(err)
	}
	changes, _ = editor.Changes()
	if changes.Revision != 1 || !changes.Dirty || changes.CompositeChanged {
		t.Fatalf("rename changed composite: %+v", changes)
	}
	if err := editor.SetVisible(1, false); err != nil {
		t.Fatal(err)
	}
	if err := editor.SetOpacity(1, 127); err != nil {
		t.Fatal(err)
	}
	changes, _ = editor.Changes()
	if changes.Revision != 3 || !changes.CompositeChanged || changes.StructureChanged {
		t.Fatalf("metadata change flags are wrong: %+v", changes)
	}
	if source.Layers[0].Name != "Layer" || source.Layers[0].Opacity != 255 || source.Layers[0].Flags != 0 {
		t.Fatal("source layer metadata changed")
	}
}

func TestEditorOwnsStraightAlphaImageAndSourcePlanes(t *testing.T) {
	editor, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 8, 3, 0)
	pixels := editorFixtureImage()
	if err := editor.ReplacePixels(1, pixels); err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	node, _, _, _ := editor.findNodeLocked(1)
	if !bytes.Equal(node.pixels.Pix, want) || node.pixels.Rect != image.Rect(0, 0, 2, 2) {
		t.Fatalf("stride, origin or alpha changed: %+v", node.pixels)
	}
	if err := editor.ReplacePixels(1, pixels); err != nil {
		t.Fatal(err)
	}
	changes, _ := editor.Changes()
	if changes.Revision != 1 {
		t.Fatalf("equal replacement advanced revision: %+v", changes)
	}
	pixels.Pix[0] = 255
	if node.pixels.Pix[0] != 1 {
		t.Fatal("replacement retains caller image")
	}
	plane := psdfile.Plane{ID: 0, Width: 2, Height: 2, Depth: 8, RowBytes: 2, Data: []byte{1, 2, 3, 4}}
	if err := editor.ReplaceChannel(1, 0, plane); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("channel silently replaced RGBA overlay: %v", err)
	}
	planeEditor, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 16, 3, 0)
	plane.Depth, plane.RowBytes, plane.Data = 16, 4, []byte{0, 1, 0, 2, 0, 3, 0, 4}
	if err := planeEditor.ReplaceChannel(1, 0, plane); err != nil {
		t.Fatal(err)
	}
	if err := planeEditor.ReplaceChannel(1, 0, plane); err != nil {
		t.Fatal(err)
	}
	plane.Data[0] = 255
	node, _, _, _ = planeEditor.findNodeLocked(1)
	if node.planes[0].Data[0] != 0 {
		t.Fatal("replacement retains caller plane")
	}
	changes, _ = planeEditor.Changes()
	if changes.Revision != 1 {
		t.Fatalf("equal plane replacement advanced revision: %+v", changes)
	}
}

func TestEditorTreeEditsPreserveHandlesAndPreventCycles(t *testing.T) {
	editor, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 8, 3, 100)
	group, err := editor.AddGroup(0, 1, "Group", true)
	if err != nil {
		t.Fatal(err)
	}
	inner, err := editor.AddGroup(group, 0, "Inner", false)
	if err != nil {
		t.Fatal(err)
	}
	raster, err := editor.AddRaster(group, 1, "Raster", editorFixtureImage(), image.Pt(-3, 5))
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.Move(group, inner, 0); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("ancestor moved into descendant: %v", err)
	}
	if err := editor.Move(group, group, 0); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("self-parent accepted: %v", err)
	}
	if err := editor.Move(raster, 1, 0); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("raster accepted children: %v", err)
	}
	if err := editor.Move(1, group, 2); err != nil {
		t.Fatal(err)
	}
	if err := editor.Move(1, group, 0); err != nil {
		t.Fatal(err)
	}
	info, err := editor.Layer(group)
	if err != nil || len(info.Children) != 3 || info.Children[0].ID != 1 || info.PSDID != 100 ||
		info.Children[1].PSDID != 102 || info.Children[2].PSDID != 104 ||
		info.Children[2].Bounds != image.Rect(-3, 5, -1, 7) {
		t.Fatalf("move changed IDs or bottom-to-top ordering: %+v %v", info, err)
	}
	if err := editor.Remove(group); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.Layer(1); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("removed descendant still exists: %v", err)
	}
	next, err := editor.AddGroup(0, 0, "Next", false)
	if err != nil || next <= raster {
		t.Fatalf("handles were reused: %d %v", next, err)
	}
	changes, _ := editor.Changes()
	if !changes.StructureChanged || !changes.CompositeChanged {
		t.Fatalf("structural edits did not change composite: %+v", changes)
	}
}

func TestEditorRejectsMalformedInputsAndBudgetsAtomically(t *testing.T) {
	editor, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 8, 3, 0,
		psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 16, MaxDecodePixels: 4}})
	for name, pixels := range map[string]*image.NRGBA{
		"nil":             nil,
		"stride":          {Rect: image.Rect(0, 0, 2, 2), Stride: 4, Pix: make([]byte, 16)},
		"truncated":       {Rect: image.Rect(0, 0, 2, 2), Stride: 8, Pix: make([]byte, 15)},
		"negative stride": {Rect: image.Rect(0, 0, 2, 2), Stride: -8, Pix: make([]byte, 16)},
		"empty":           {Rect: image.Rectangle{}, Stride: 0},
	} {
		t.Run(name, func(t *testing.T) {
			if err := editor.ReplacePixels(1, pixels); !errors.Is(err, ErrInvalidEdit) {
				t.Fatalf("invalid input accepted: %v", err)
			}
		})
	}
	if err := editor.Rename(1, string([]byte{0xFF})); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("invalid UTF-8 accepted: %v", err)
	}
	if _, err := editor.AddRaster(
		0, 1, "Overflow", editorFixtureImage(), image.Pt(math.MaxInt32, 0),
	); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("coordinate overflow accepted: %v", err)
	}
	if err := editor.ReplacePixels(1, image.NewNRGBA(image.Rect(0, 0, 3, 3))); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("pixel budget ignored: %v", err)
	}
	if err := editor.ReplacePixels(1, editorFixtureImage()); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.AddRaster(
		0, 1, "Over budget", editorFixtureImage(), image.Point{},
	); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("aggregate owned budget ignored: %v", err)
	}
	changes, _ := editor.Changes()
	if changes.Revision != 1 || changes.StructureChanged {
		t.Fatalf("failed operations mutated state: %+v", changes)
	}
}

func TestEditorSemanticCachesAndMultipleSetsAreNotEditable(t *testing.T) {
	for _, tag := range []string{
		"SoLd", "SoLE", "PlLd", "TySh", "tySh", "SoCo", "GdFl", "PtFl", "vscg", "vstk", "PxSc", "PxSD", "levl",
	} {
		t.Run(tag, func(t *testing.T) {
			editor, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Cache", id: 7, tag: tag}}, 8, 3, 0)
			if err := editor.ReplacePixels(1, editorFixtureImage()); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("semantic cache accepted replacement: %v", err)
			}
			if err := editor.ReplaceChannel(1, 0, psdfile.Plane{
				ID: 0, Width: 2, Height: 2, Depth: 8, RowBytes: 2, Data: []byte{1, 2, 3, 4},
			}); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("semantic cache accepted channel replacement: %v", err)
			}
			changes, err := editor.Changes()
			if err != nil || changes.Revision != 0 || changes.CompositeChanged {
				t.Fatalf("rejected cache replacement mutated state: %+v %v", changes, err)
			}
		})
	}
	editor, source := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 8, 3, 0)
	source.LayerSets = append(source.LayerSets, source.LayerSets[0])
	if err := editor.Rename(1, "Changed"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("multiple sets accepted mutation: %v", err)
	}
	if _, err := editor.Tree(); err != nil {
		t.Fatalf("multiple sets prevent metadata read: %v", err)
	}
}

func TestEditorUnknownHierarchyRejectsStructuralEditsWithoutMutation(t *testing.T) {
	editor, source := editorFixtureOpen(t, []editorFixtureLayer{
		{name: "Unknown hierarchy", id: 7, divider: 99},
	}, 8, 3, 100)
	originalID, originalPSDID := editor.nextID, editor.nextPSDID
	operations := map[string]func() error{
		"add raster": func() error {
			_, err := editor.AddRaster(0, 1, "Raster", editorFixtureImage(), image.Point{})
			return err
		},
		"add group": func() error {
			_, err := editor.AddGroup(0, 1, "Group", true)
			return err
		},
		"move":           func() error { return editor.Move(1, 0, 0) },
		"remove":         func() error { return editor.Remove(1) },
		"replace pixels": func() error { return editor.ReplacePixels(1, editorFixtureImage()) },
		"replace channel": func() error {
			return editor.ReplaceChannel(1, 0, psdfile.Plane{
				ID: 0, Width: 2, Height: 2, Depth: 8, RowBytes: 2, Data: []byte{1, 2, 3, 4},
			})
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unknown hierarchy edit accepted: %v", err)
			}
			changes, err := editor.Changes()
			if err != nil || changes.Revision != 0 || changes.CompositeChanged || changes.StructureChanged ||
				editor.nextID != originalID || editor.nextPSDID != originalPSDID {
				t.Fatalf("rejected edit changed revision or reserved IDs: %+v %v", changes, err)
			}
		})
	}
	if err := editor.Rename(1, "Renamed unknown hierarchy"); err != nil {
		t.Fatalf("unknown hierarchy prevents a metadata rename: %v", err)
	}
	changes, err := editor.Changes()
	if err != nil || changes.Revision != 1 || changes.CompositeChanged || changes.StructureChanged ||
		source.Layers[0].Divider != 99 || source.Layers[0].Name != "Unknown hierarchy" {
		t.Fatalf("rename changed source hierarchy or visual state: %+v %v", changes, err)
	}
}

func TestEditorNULNamesAreRejectedWithoutMutation(t *testing.T) {
	editor, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 8, 3, 100)
	originalID, originalPSDID := editor.nextID, editor.nextPSDID
	for _, name := range []string{"Layer\x00", "Embedded\x00NUL", "\x00"} {
		operations := []func() error{
			func() error { return editor.Rename(1, name) },
			func() error {
				_, err := editor.AddRaster(0, 1, name, editorFixtureImage(), image.Point{})
				return err
			},
			func() error {
				_, err := editor.AddGroup(0, 1, name, true)
				return err
			},
		}
		for _, operation := range operations {
			if err := operation(); !errors.Is(err, ErrInvalidEdit) {
				t.Fatalf("NUL name accepted: %v", err)
			}
		}
	}
	changes, err := editor.Changes()
	if err != nil || changes.Revision != 0 || changes.Dirty || changes.CompositeChanged ||
		editor.nextID != originalID || editor.nextPSDID != originalPSDID {
		t.Fatalf("rejected NUL name changed editor state: %+v %v", changes, err)
	}
	layer, err := editor.Layer(1)
	if err != nil || layer.Name != "Layer" {
		t.Fatalf("rejected NUL name changed layer: %+v %v", layer, err)
	}
}

func TestEditorPSDIDExhaustionAndGroupMarkers(t *testing.T) {
	editor, _ := editorFixtureOpen(t, []editorFixtureLayer{
		{name: "End", id: 500, divider: 3}, {name: "Group", id: 7, divider: 1},
	}, 8, 3, 100)
	group, err := editor.AddGroup(0, 1, "Added", true)
	if err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(group)
	if node.psdID != 501 || node.endPSDID != 502 {
		t.Fatalf("end marker PSD ID was not reserved: %+v", node)
	}
	exhausted, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: math.MaxUint32}}, 8, 3, 0)
	if _, err := exhausted.AddGroup(0, 1, "Exhausted", true); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("PSD ID wrapped: %v", err)
	}
	nearEnd, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: math.MaxUint32 - 1}}, 8, 3, 0)
	originalID, originalPSDID := nearEnd.nextID, nearEnd.nextPSDID
	if _, err := nearEnd.AddGroup(0, 1, "Too many IDs", true); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("group IDs wrapped: %v", err)
	}
	if _, err := nearEnd.AddRaster(
		0, 1, "Exhausted", editorFixtureImage(), image.Point{},
	); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("accepted a layer ID without a representable next seed: %v", err)
	}
	changes, err := nearEnd.Changes()
	if err != nil || changes.Revision != 0 || changes.StructureChanged ||
		nearEnd.nextID != originalID || nearEnd.nextPSDID != originalPSDID {
		t.Fatalf("exhausted ID allocation mutated state: %+v %v", changes, err)
	}
	lastWritable, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: math.MaxUint32 - 2}}, 8, 3, 0)
	if _, err := lastWritable.AddGroup(0, 1, "Needs two IDs", true); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("group consumed the last seed value: %v", err)
	}
	lastRaster, err := lastWritable.AddRaster(0, 1, "Last writable ID", editorFixtureImage(), image.Point{})
	if err != nil {
		t.Fatalf("rejected last writable PSD ID: %v", err)
	}
	info, err := lastWritable.Layer(lastRaster)
	if err != nil || info.PSDID != math.MaxUint32-1 || lastWritable.nextPSDID != math.MaxUint32 {
		t.Fatalf("last writable ID did not leave a representable seed: %+v %v", info, err)
	}
	var output bytes.Buffer
	if err := lastWritable.Write(context.Background(), &output, SaveOptions{
		CompositeImage: image.NewNRGBA(image.Rect(0, 0, 2, 2)),
	}); err != nil {
		t.Fatalf("valid boundary edit cannot be saved: %v", err)
	}
	saved, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatalf("saved boundary document cannot be indexed: %v", err)
	}
	defer func() { _ = saved.Close() }()
	var seed uint32
	for _, resource := range saved.Resources {
		if resource.ID != 1044 {
			continue
		}
		data, err := saved.ReadSpan(resource.Data)
		if err != nil {
			t.Fatal(err)
		}
		seed = binary.BigEndian.Uint32(data)
	}
	if seed != math.MaxUint32 {
		t.Fatalf("saved boundary seed=%d, want the last representable value", seed)
	}
	if _, err := lastWritable.AddRaster(
		0, 2, "No remaining IDs", editorFixtureImage(), image.Point{},
	); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("seed value was reused as a layer identifier: %v", err)
	}
}

func TestEditorExhaustedVirtualBackgroundRemainsCopyable(t *testing.T) {
	data := editorFixtureBytes(nil, 8, 3, math.MaxUint32)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	editor, err := New(source)
	if err != nil {
		t.Fatalf("an exhausted seed prevents read-only source use: %v", err)
	}
	defer func() { _ = editor.Close() }()
	layers, err := editor.Layers()
	if err != nil || len(layers) != 1 || layers[0].PSDID != 0 {
		t.Fatalf("unallocated virtual background metadata is wrong: %+v %v", layers, err)
	}
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); err != nil || !bytes.Equal(output.Bytes(), data) {
		t.Fatalf("read-only copy of exhausted source changed: %v", err)
	}
	originalID, originalPSDID := editor.nextID, editor.nextPSDID
	if err := editor.Rename(layers[0].ID, "Requires materialization"); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("virtual background accepted an unsavable metadata edit: %v", err)
	}
	if _, err := editor.AddRaster(
		0, 1, "Exhausted", editorFixtureImage(), image.Point{},
	); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("virtual background accepted an unsavable structure edit: %v", err)
	}
	changes, err := editor.Changes()
	if err != nil || changes.Revision != 0 || editor.nextID != originalID || editor.nextPSDID != originalPSDID {
		t.Fatalf("exhausted virtual background mutation changed state: %+v %v", changes, err)
	}
}

func TestEditorVirtualBackgroundAndSourceOwnership(t *testing.T) {
	editor, source := editorFixtureOpen(t, nil, 8, 3, 0)
	layers, err := editor.Layers()
	if err != nil || len(layers) != 1 || layers[0].Name != "Background" || layers[0].SourceIndex != -2 {
		t.Fatalf("flattened RGB background missing: %+v %v", layers, err)
	}
	changes, _ := editor.Changes()
	if changes.Revision != 0 || changes.Dirty {
		t.Fatalf("virtual background changed source revision: %+v", changes)
	}
	if err := editor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		t.Fatalf("borrowed source closed: %v", err)
	}
	if _, err := editor.Tree(); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed editor remains usable: %v", err)
	}
	if err := editor.Rename(1, "Closed"); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed editor mutation returns wrong error: %v", err)
	}
	filename := filepath.Join(t.TempDir(), "owned.psd")
	if err := os.WriteFile(filename, editorFixtureBytes(nil, 8, 3, 0), 0o600); err != nil {
		t.Fatal(err)
	}
	owned, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := owned.source.ReadAt(nil, 0); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("owned source remains open: %v", err)
	}
	highDepth, _ := editorFixtureOpen(t, nil, 16, 3, 0)
	if layers, err := highDepth.Layers(); err != nil || len(layers) != 0 {
		t.Fatalf("high-depth virtual background invented: %+v %v", layers, err)
	}
	if _, err := highDepth.AddRaster(0, 0, "RGB8", editorFixtureImage(), image.Point{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("high-depth source accepted RGBA raster: %v", err)
	}
}

func TestEditorSerializesConcurrentMetadataEdits(t *testing.T) {
	editor, _ := editorFixtureOpen(t, []editorFixtureLayer{{name: "Layer", id: 7}}, 8, 3, 0)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 10 {
				if _, err := editor.Tree(); err != nil {
					t.Error(err)
				}
				if err := editor.SetVisible(1, false); err != nil {
					t.Error(err)
				}
			}
		})
	}
	workers.Wait()
	changes, _ := editor.Changes()
	if changes.Revision != 1 {
		t.Fatalf("concurrent no-ops advanced revision: %+v", changes)
	}
}
