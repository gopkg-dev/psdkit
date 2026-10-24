package psdedit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func smartEditImage(colors [4]color.NRGBA) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index, value := range colors {
		pixels.SetNRGBA(index%2, index/2, value)
	}
	return pixels
}

func smartEditColors() [4]color.NRGBA {
	return [4]color.NRGBA{{10, 20, 30, 255}, {80, 90, 100, 255}, {120, 130, 140, 255}, {210, 220, 230, 255}}
}

func smartEditLayer(version uint16, name string, id uint32, bounds image.Rectangle, pixels *image.NRGBA,
	tags []byte, masked bool,
) ([]byte, []byte) {
	var record, payloads bytes.Buffer
	for _, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
		editorFixtureNumber(&record, int32(coordinate))
	}
	ids := []int16{0, 1, 2, -1}
	if masked {
		ids = append(ids, -2)
	}
	editorFixtureNumber(&record, uint16(len(ids)))
	for component, channelID := range ids {
		editorFixtureNumber(&record, channelID)
		record.Write(writerLinkedLength(nil, version, 2+pixels.Rect.Dx()*pixels.Rect.Dy()))
		editorFixtureNumber(&payloads, uint16(0))
		if channelID == -2 {
			payloads.Write([]byte{255, 128, 0, 255})
			continue
		}
		for index := range pixels.Rect.Dx() * pixels.Rect.Dy() {
			payloads.WriteByte(pixels.Pix[index*4+component])
		}
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	var extra bytes.Buffer
	if masked {
		editorFixtureNumber(&extra, uint32(20))
		for _, coordinate := range []int{bounds.Min.Y, bounds.Min.X, bounds.Max.Y, bounds.Max.X} {
			editorFixtureNumber(&extra, int32(coordinate))
		}
		extra.Write([]byte{255, 0, 0xa1, 0xb2})
	} else {
		editorFixtureNumber(&extra, uint32(0))
	}
	editorFixtureNumber(&extra, uint32(0))
	extra.WriteByte(byte(len(name)))
	extra.WriteString(name)
	for extra.Len()%4 != 0 {
		extra.WriteByte(0xd7)
	}
	editorFixtureTag(&extra, "lyid", binary.BigEndian.AppendUint32(nil, id))
	editorFixtureTag(&extra, "U000", []byte{1, 7, 13})
	extra.Write(tags)
	editorFixtureTag(&extra, "U000", []byte{2, 9})
	editorFixtureNumber(&record, uint32(extra.Len()))
	record.Write(extra.Bytes())
	return record.Bytes(), payloads.Bytes()
}

func smartEditDocument(version uint16, width, height int, channels uint16,
	layers, global, composite []byte,
) []byte {
	section := writerLinkedLength(nil, version, len(layers))
	section = append(section, layers...)
	section = binary.BigEndian.AppendUint32(section, 0)
	section = append(section, global...)
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, channels)
	data = binary.BigEndian.AppendUint32(data, uint32(height))
	data = binary.BigEndian.AppendUint32(data, uint32(width))
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint64(data, 0)
	data = writerLinkedLength(data, version, len(section))
	data = append(data, section...)
	return append(data, composite...)
}

func smartEditPrototype(version uint16, pixels *image.NRGBA) []byte {
	record, payloads := smartEditLayer(version, "Editable content", 70, image.Rect(0, 0, 2, 2), pixels, nil, false)
	layers := binary.BigEndian.AppendUint16(nil, 0xffff)
	layers = append(layers, record...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	composite := binary.BigEndian.AppendUint16(nil, 1)
	for range 8 {
		if version == 2 {
			composite = binary.BigEndian.AppendUint32(composite, 3)
		} else {
			composite = binary.BigEndian.AppendUint16(composite, 3)
		}
	}
	for component := range 4 {
		for row := range 2 {
			composite = append(composite, 1, pixels.Pix[row*8+component], pixels.Pix[row*8+4+component])
		}
	}
	return smartEditDocument(version, 2, 2, 4, layers, nil, composite)
}

func smartEditPrototypeCanvas(version uint16, pixels *image.NRGBA, dimension int) []byte {
	data := smartEditPrototype(version, pixels)
	footerSize := 42
	if version == 2 {
		footerSize = 58
	}
	data = data[:len(data)-footerSize]
	binary.BigEndian.PutUint32(data[14:18], uint32(dimension))
	binary.BigEndian.PutUint32(data[18:22], uint32(dimension))
	data = binary.BigEndian.AppendUint16(data, 1)
	for range 4 * dimension {
		if version == 2 {
			data = binary.BigEndian.AppendUint32(data, 2)
		} else {
			data = binary.BigEndian.AppendUint16(data, 2)
		}
	}
	for component := range 4 {
		value := byte(255)
		if component == 3 {
			value = 0
		}
		for range dimension {
			data = append(data, byte(257-dimension), value)
		}
	}
	return data
}

func smartEditLargePrototype(pixels *image.NRGBA) []byte {
	var record, payloads bytes.Buffer
	for _, coordinate := range []int32{0, 0, 64, 64} {
		editorFixtureNumber(&record, coordinate)
	}
	editorFixtureNumber(&record, uint16(4))
	for component, channelID := range []int16{0, 1, 2, -1} {
		editorFixtureNumber(&record, channelID)
		editorFixtureNumber(&record, uint32(258))
		editorFixtureNumber(&payloads, uint16(1))
		for range 64 {
			editorFixtureNumber(&payloads, uint16(2))
		}
		for range 64 {
			payloads.Write([]byte{0xc1, pixels.Pix[component]})
		}
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	var extra bytes.Buffer
	editorFixtureNumber(&extra, uint32(0))
	editorFixtureNumber(&extra, uint32(0))
	name := "Large editable"
	extra.WriteByte(byte(len(name)))
	extra.WriteString(name)
	for extra.Len()%4 != 0 {
		extra.WriteByte(0)
	}
	editorFixtureNumber(&record, uint32(extra.Len()))
	record.Write(extra.Bytes())
	layers := binary.BigEndian.AppendUint16(nil, 0xffff)
	layers = append(layers, record.Bytes()...)
	layers = append(layers, payloads.Bytes()...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	small := smartEditPrototypeCanvas(1, pixels, 32)
	footer := small[len(small)-514:]
	return smartEditDocument(1, 32, 32, 4, layers, nil, footer)
}

func smartEditFlat(width, height int, depth, mode uint16) []byte {
	channels := uint16(3)
	if mode == 1 {
		channels = 1
	}
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, channels)
	data = binary.BigEndian.AppendUint32(data, uint32(height))
	data = binary.BigEndian.AppendUint32(data, uint32(width))
	data = binary.BigEndian.AppendUint16(data, depth)
	data = binary.BigEndian.AppendUint16(data, mode)
	data = binary.BigEndian.AppendUint64(data, 0)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = binary.BigEndian.AppendUint16(data, 0)
	return append(data, bytes.Repeat([]byte{70}, width*height*int(channels)*int(depth/8))...)
}

func smartEditPlacement(t *testing.T, left int, peerProblem string) []byte {
	t.Helper()
	numbers := []float64{float64(left), 0, float64(left + 2), 0, float64(left + 2), 2, float64(left), 2}
	items := make([]psdfile.DescriptorValue, len(numbers))
	for index, number := range numbers {
		items[index] = psdfile.DescriptorValue{Type: "doub", Value: number}
	}
	value := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: "fixture-id"},
		{Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(2)},
		}}},
		{Key: "Trnf", Type: "VlLs", Value: items},
		{Key: "nonAffineTransform", Type: "VlLs", Value: items},
		{Key: "opaqueMetadata", Type: "tdta", Value: []byte{0, 9, 255, 31}},
		{Key: "unknownDouble", Type: "doub", Value: math.Float64frombits(0x7ff8000000000042)},
		{Key: "unknownDouble", Type: "doub", Value: math.Copysign(0, -1)},
		{Key: "unknownText", Type: "TEXT", Value: "原始\x00尾"},
	}}
	if peerProblem == "duplicate-size" || peerProblem == "ambiguous-size" {
		size := &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(2)},
		}}
		value.Entries = append(value.Entries, psdfile.DescriptorEntry{Key: "Sz  ", Type: "Objc", Value: size})
		if peerProblem == "ambiguous-size" {
			value.Entries[2].Value.(*psdfile.Descriptor).Entries[0].Value = float64(17)
		}
	}
	if peerProblem == "large-canvas" {
		size := value.Entries[2].Value.(*psdfile.Descriptor)
		size.Entries[0].Value, size.Entries[1].Value = float64(32), float64(32)
	}
	if peerProblem == "type" {
		value.Entries[1].Value = int32(1)
	}
	if peerProblem == "warp" {
		value.Entries = append(value.Entries, psdfile.DescriptorEntry{Key: "warp", Type: "Objc",
			Value: &psdfile.Descriptor{ClassID: "warp", Entries: []psdfile.DescriptorEntry{
				{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpArc"}},
				{Key: "warpValue", Type: "doub", Value: float64(20)},
			}},
		})
	}
	if peerProblem == "filter" {
		value.Entries = append(value.Entries, psdfile.DescriptorEntry{Key: "filterFX", Type: "Objc",
			Value: &psdfile.Descriptor{ClassID: "filterFX", Entries: []psdfile.DescriptorEntry{
				{Key: "enab", Type: "bool", Value: true},
				{Key: "filterFXList", Type: "VlLs", Value: []psdfile.DescriptorValue{{Type: "Objc",
					Value: &psdfile.Descriptor{ClassID: "filter", Entries: []psdfile.DescriptorEntry{
						{Key: "filterID", Type: "long", Value: int32(0x12345678)},
						{Key: "enab", Type: "bool", Value: true},
						{Key: "Fltr", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "unknownFilter"}},
					}},
				}}},
			}},
		})
	}
	encoded, err := psdfile.EncodeDescriptor(nil, value, true)
	if err != nil {
		t.Fatal(err)
	}
	version := uint32(4)
	if left != 0 {
		version = 5
	}
	payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), version), encoded...)
	return append(payload, 0xa1, 0xd3, 0x7f)
}

func smartEditFixture(t *testing.T, version uint16, problem string) []byte {
	t.Helper()
	oldPixels := smartEditImage([4]color.NRGBA{
		{220, 60, 40, 255}, {220, 60, 40, 255}, {220, 60, 40, 255}, {220, 60, 40, 255},
	})
	original := smartEditPrototype(version, oldPixels)
	if problem == "large-canvas" {
		original = smartEditPrototypeCanvas(version, oldPixels, 32)
	}
	kind := "liFD"
	if problem == "external" {
		kind = "liFE"
	}
	linked := writerLinkedEntry(kind, "fixture-id", 7, original)
	if version == 2 {
		copy(linked.body[linked.sizeOffset-8:linked.sizeOffset-4], "8BPB")
		copy(linked.data[linked.sizeOffset:linked.sizeOffset+4], "8BPB")
	}
	width := 4
	if version == 2 {
		width = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", width, linked.data, 4)
	var records, payloads []byte
	for index, left := range []int{0, 4} {
		peerProblem := ""
		if index == 1 || problem == "large-canvas" {
			peerProblem = problem
		}
		key := "SoLd"
		if index != 0 {
			key = "SoLE"
		}
		placement := writerLinkedTag("8BIM", key, 4, smartEditPlacement(t, left, peerProblem), 2)
		if problem == "legacy" {
			legacy := writerLinkedTag("8BIM", "PlLd", 4, smartDimensionLegacy(t, left), 2)
			placement = append(legacy, placement...)
		}
		record, pixels := smartEditLayer(version, []string{"First instance", "Second instance"}[index],
			uint32(10+index), image.Rect(left, 0, left+2, 2), oldPixels, placement, index == 1,
		)
		records, payloads = append(records, record...), append(payloads, pixels...)
	}
	layers := binary.BigEndian.AppendUint16(nil, 2)
	layers = append(layers, records...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	composite := append([]byte{0, 0}, bytes.Repeat([]byte{0}, 6*2*3)...)
	return smartEditDocument(version, 6, 2, 3, layers, global, composite)
}

func smartEditOpen(t *testing.T, data []byte, options ...psdfile.LoadOptions) (*Editor, *psdfile.Document) {
	t.Helper()
	source, err := psdfile.Parse(data, options...)
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

func smartEditAssertCaches(t *testing.T, editor *Editor, wanted *image.NRGBA) {
	t.Helper()
	for _, node := range editor.roots {
		if node.pixels == nil || node.pixels.Rect.Size() != wanted.Rect.Size() ||
			!bytes.Equal(node.pixels.Pix, wanted.Pix) {
			t.Fatal("a shared instance cache did not project the complete replacement at its own location")
		}
	}
	if editor.roots[0].bounds != image.Rect(0, 0, 2, 2) || editor.roots[1].bounds != image.Rect(4, 0, 6, 2) {
		t.Fatal("shared caches lost their distinct document positions")
	}
}

func smartEditAssertPreservedMetadata(t *testing.T, source, saved *psdfile.Document) {
	t.Helper()
	for index, layer := range source.Layers {
		actual := saved.Layers[index]
		if actual.ID != layer.ID || actual.Name != layer.Name || actual.Mask == nil != (layer.Mask == nil) {
			t.Fatal("content replacement changed layer identity, name or mask presence")
		}
		for _, pair := range [][2]psdfile.Span{{layer.MaskData, actual.MaskData}, {layer.BlendRanges, actual.BlendRanges}} {
			before, readErr := source.ReadSpan(pair[0])
			after, afterErr := saved.ReadSpan(pair[1])
			if readErr != nil || afterErr != nil || !bytes.Equal(before, after) {
				t.Fatal("content replacement rewrote mask or Blend If metadata")
			}
		}
		if len(layer.AdditionalInfo) != len(actual.AdditionalInfo) {
			t.Fatal("content replacement changed placement or unknown-block counts")
		}
		for blockIndex, block := range layer.AdditionalInfo {
			before, readErr := source.ReadSpan(block.Span)
			after, afterErr := saved.ReadSpan(actual.AdditionalInfo[blockIndex].Span)
			if readErr != nil || afterErr != nil || !bytes.Equal(before, after) {
				t.Fatal("content replacement changed SoLd/SoLE, UID, unknown data or alignment")
			}
		}
		for _, channel := range layer.Channels {
			if channel.ID != -2 {
				continue
			}
			for _, actualChannel := range actual.Channels {
				if actualChannel.ID == -2 {
					before, readErr := source.ReadSpan(channel.Span)
					after, afterErr := saved.ReadSpan(actualChannel.Span)
					if readErr != nil || afterErr != nil || !bytes.Equal(before, after) {
						t.Fatal("content replacement changed the original encoded pixel-mask channel")
					}
				}
			}
		}
	}
}

func TestReplaceContentsSharedPSDAndPSBPreserveMetadataAndEditableContents(t *testing.T) {
	for _, outerVersion := range []uint16{1, 2} {
		for _, innerVersion := range []uint16{1, 2} {
			t.Run(string(rune('0'+outerVersion))+"-"+string(rune('0'+innerVersion)), func(t *testing.T) {
				data := smartEditFixture(t, outerVersion, "")
				originalHash := sha256.Sum256(data)
				editor, source := smartEditOpen(t, data)
				infos, err := editor.SmartObjects(nil)
				if err != nil || len(infos) != 2 || len(infos[0].SharedInstances) != 2 ||
					infos[0].SourceID != infos[1].SourceID || !infos[0].Embedded || infos[0].SourceKind != "liFD" {
					t.Fatalf("shared source views differ: count=%d, %v", len(infos), err)
				}
				infos[0].SharedInstances[0] = 999
				if infos[1].SharedInstances[0] == 999 {
					t.Fatal("returned shared-instance lists alias each other")
				}
				wanted := smartEditImage(smartEditColors())
				replacement := smartEditPrototype(innerVersion, wanted)
				immutable := bytes.Clone(replacement)
				if err := editor.ReplaceContents(nil, infos[0].ID, replacement); err != nil {
					t.Fatal(err)
				}
				smartEditAssertCaches(t, editor, wanted)
				clear(replacement)
				if !bytes.Equal(editor.linkedOverlays[infos[0].SourceID].data, immutable) {
					t.Fatal("encoded replacement aliases caller-owned input")
				}
				changes, err := editor.Changes()
				if err != nil || changes.Revision != 1 || !changes.Dirty || !changes.CompositeChanged || changes.StructureChanged {
					t.Fatalf("shared replacement was not one visual transaction: %+v, %v", changes, err)
				}
				views, err := editor.SmartObjects(nil)
				if err != nil || !views[0].Replaced || !views[1].Replaced {
					t.Fatal("replacement state did not reach all shared views")
				}
				var buffer bytes.Buffer
				if err := editor.Write(nil, &buffer, SaveOptions{Compression: 1}); err != nil {
					t.Fatal(err)
				}
				saved, err := psdfile.Parse(buffer.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = saved.Close() })
				smartEditAssertPreservedMetadata(t, source, saved)
				linked, err := saved.LinkedFiles(nil)
				if err != nil || len(linked) != 1 {
					t.Fatalf("replacement linked record count=%d: %v", len(linked), err)
				}
				if linked[0].FileType != map[uint16]string{1: "8BPS", 2: "8BPB"}[innerVersion] {
					t.Fatal("linked file type no longer matches the replacement PSD/PSB format")
				}
				encoded, err := saved.ReadSpan(linked[0].Data)
				if err != nil || !bytes.Equal(encoded, immutable) || linked[0].DataSize != uint64(len(immutable)) {
					t.Fatalf("linked replacement bytes, lengths or RLE footer were truncated: %v", err)
				}
				child, err := saved.OpenLinkedDocument(nil, linked[0])
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = child.Close() })
				if child.Header.Version != innerVersion || len(child.Layers) != 1 || child.Layers[0].Name != "Editable content" {
					t.Fatal("replacement ceased to be an editable nested PSD/PSB document")
				}
				pixels, err := child.DecodeLayer(nil, 0)
				if err != nil || len(pixels.Channels) != 4 {
					t.Fatalf("editable nested replacement pixels are unreadable: %v", err)
				}
				composite, err := child.DecodeComposite(nil)
				if err != nil || len(composite.Channels) != 4 {
					t.Fatalf("replacement RLE saved-image frame is unreadable: %v", err)
				}
				if sha256.Sum256(data) != originalHash {
					t.Fatal("shared replacement mutated original source bytes")
				}
				changes, _ = editor.Changes()
				if changes.Dirty || changes.SavedRevision != 1 {
					t.Fatal("successful replacement write did not update the saved revision")
				}
			})
		}
	}
}

func TestReplaceContentsFailuresLeavePreviousTransactionIntact(t *testing.T) {
	data := smartEditFixture(t, 1, "")
	editor, _ := smartEditOpen(t, data)
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := smartEditPrototype(1, smartEditImage(smartEditColors()))
	if err := editor.ReplaceContents(nil, infos[0].ID, replacement); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	beforeContent := bytes.Clone(editor.linkedOverlays[infos[0].SourceID].data)
	beforePixels := bytes.Clone(editor.roots[0].pixels.Pix)
	for _, invalid := range [][]byte{nil, []byte("invalid"),
		smartEditFlat(2, 2, 16, 3), smartEditFlat(2, 2, 8, 1)} {
		if err := editor.ReplaceContents(nil, infos[0].ID, invalid); err == nil {
			t.Fatal("invalid replacement content was accepted")
		}
		after, _ := editor.Changes()
		if after != before || !bytes.Equal(editor.linkedOverlays[infos[0].SourceID].data, beforeContent) ||
			!bytes.Equal(editor.roots[0].pixels.Pix, beforePixels) {
			t.Fatal("rejected replacement changed an existing transaction")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := editor.ReplaceContents(ctx, infos[0].ID, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled replacement returned %v", err)
	}
	if after, _ := editor.Changes(); after != before {
		t.Fatal("canceled replacement changed revisions")
	}
	if err := editor.ReplaceContents(nil, 999, replacement); !errors.Is(err, ErrInvalidEdit) {
		t.Fatalf("unknown layer handle returned %v", err)
	}
}

type smartEditCancelReader struct {
	data    []byte
	trigger psdfile.Span
	cancel  context.CancelFunc
	matches int
}

func (r *smartEditCancelReader) ReadAt(data []byte, offset int64) (int, error) {
	count, err := bytes.NewReader(r.data).ReadAt(data, offset)
	if r.cancel != nil && offset < r.trigger.Offset+r.trigger.Length &&
		r.trigger.Offset < offset+int64(len(data)) {
		r.matches++
		r.cancel()
	}
	return count, err
}

func TestReplaceContentsCancellationDuringMetadataReadKeepsExistingOverlay(t *testing.T) {
	data := smartEditFixture(t, 1, "")
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := smartEditPrototype(1, smartEditImage(smartEditColors()))
	if err := editor.ReplaceContents(nil, infos[0].ID, replacement); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	oldContent := bytes.Clone(editor.linkedOverlays[infos[0].SourceID].data)
	oldCache := bytes.Clone(editor.roots[0].pixels.Pix)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader.trigger, reader.cancel = source.Layers[0].Span, cancel
	if err := editor.ReplaceContents(ctx, infos[1].ID, replacement); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-decode cancellation returned %v", err)
	}
	reader.cancel = nil
	after, _ := editor.Changes()
	if reader.matches != 1 || after != before ||
		!bytes.Equal(editor.linkedOverlays[infos[0].SourceID].data, oldContent) ||
		!bytes.Equal(editor.roots[0].pixels.Pix, oldCache) {
		t.Fatal("source decode continued after cancellation or partially committed a replacement")
	}
}

func TestReplaceContentsDoesNotReadOriginalRasterCaches(t *testing.T) {
	data := smartEditFixture(t, 1, "")
	reader := &smartEditCancelReader{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader.trigger, reader.cancel = source.Layers[0].Channels[0].Data, cancel
	replacement := smartEditPrototype(1, smartEditImage(smartEditColors()))
	if err := editor.ReplaceContents(ctx, infos[0].ID, replacement); err != nil || reader.matches != 0 {
		t.Fatalf("replacement read unrelated original raster bytes: reads=%d error=%v", reader.matches, err)
	}
}

func TestReplaceContentsTransparentPrototypeDoesNotBakeInstanceMasks(t *testing.T) {
	editor, source := smartEditOpen(t, smartEditFixture(t, 1, ""))
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	pixels := smartEditImage([4]color.NRGBA{
		{255, 0, 0, 255}, {0, 255, 0, 128}, {0, 0, 255, 0}, {255, 255, 255, 64},
	})
	if err := editor.ReplaceContents(nil, infos[0].ID, smartEditPrototype(1, pixels)); err != nil {
		t.Fatal(err)
	}
	for _, node := range editor.roots {
		for index, want := range []byte{255, 128, 0, 64} {
			if node.pixels.Pix[index*4+3] != want {
				t.Fatal("an instance mask was baked into its shared source cache")
			}
		}
	}
	composite, err := editor.Composite(nil)
	if err != nil {
		t.Fatal(err)
	}
	if composite.NRGBAAt(1, 0).A != 128 || composite.NRGBAAt(5, 0).A != 64 ||
		composite.NRGBAAt(4, 1).A != 0 || composite.NRGBAAt(5, 1).A != 64 {
		t.Fatal("source alpha and per-instance mask did not apply exactly once")
	}
	var buffer bytes.Buffer
	if err := editor.Write(nil, &buffer, SaveOptions{Compression: 1}); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Parse(buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	smartEditAssertPreservedMetadata(t, source, saved)
	merged, err := saved.DecodeComposite(nil)
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("transparent replacement lost the saved merged alpha: %v", err)
	}
	wantAlpha := []byte{255, 128, 0, 0, 255, 64, 0, 64, 0, 0, 0, 64}
	if !bytes.Equal(merged.Channels[3].Data, wantAlpha) {
		t.Fatal("saved merged transparency does not match the edited source and instance masks")
	}
}

func TestReplaceContentsRejectsUnsupportedSharedPeersAndExternalSourcesAtomically(t *testing.T) {
	for _, problem := range []string{"warp", "filter", "type", "external"} {
		t.Run(problem, func(t *testing.T) {
			editor, _ := smartEditOpen(t, smartEditFixture(t, 1, problem))
			infos, err := editor.SmartObjects(nil)
			if err != nil || len(infos) != 2 {
				t.Fatalf("unsupported semantic fixture metadata differs: %v", err)
			}
			before, _ := editor.Changes()
			err = editor.ReplaceContents(nil, infos[0].ID, smartEditPrototype(1, smartEditImage(smartEditColors())))
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("unsupported shared semantic %s returned %v", problem, err)
			}
			after, _ := editor.Changes()
			if before != after || len(editor.linkedOverlays) != 0 ||
				editor.roots[0].pixels != nil || editor.roots[1].pixels != nil {
				t.Fatal("rejecting a shared peer partially committed earlier cache updates")
			}
		})
	}
}

func TestReplaceContentsOwnedBudgetIsTransactional(t *testing.T) {
	data := smartEditFixture(t, 1, "")
	replacement := smartEditPrototype(1, smartEditImage(smartEditColors()))
	editor, _ := smartEditOpen(t, data, psdfile.LoadOptions{
		Limits: psdfile.Limits{MaxDecodeBytes: int64(len(replacement)) - 1},
	})
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	if err := editor.ReplaceContents(nil, infos[0].ID, replacement); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("owned encoded-content budget returned %v", err)
	}
	if after, _ := editor.Changes(); after != before || len(editor.linkedOverlays) != 0 {
		t.Fatal("owned-content limit changed the transaction")
	}
}

func TestReplaceContentsSmallPrototypeFitsConfiguredWorkingBudget(t *testing.T) {
	data := smartEditFixture(t, 1, "large-canvas")
	editor, _ := smartEditOpen(t, data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 16 << 10}})
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := smartEditPrototypeCanvas(1, smartEditImage(smartEditColors()), 32)
	if err := editor.ReplaceContents(nil, infos[0].ID, replacement); err != nil {
		t.Fatalf("small prototype was incorrectly rejected under a valid 16 KiB working budget: %v", err)
	}
	after, _ := editor.Changes()
	if after.Revision != 1 || len(editor.linkedOverlays) != 1 ||
		editor.roots[0].pixels == nil || editor.roots[1].pixels == nil {
		t.Fatal("accepted prototype did not commit one complete shared-content update")
	}
}

func TestReplaceContentsSimultaneousPrototypeAndSourceBuffersAreBounded(t *testing.T) {
	limits := psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 24 << 10}}
	editor, _ := smartEditOpen(t, smartEditFixture(t, 1, "large-canvas"), limits)
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := smartEditLargePrototype(smartEditImage(smartEditColors()))
	content, err := psdfile.Parse(replacement, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = content.Close() })
	// One 32x32 saved preview and one 64x64 raster total 20 KiB: an individual decode fits.
	if _, err := content.Decode(nil); err != nil {
		t.Fatalf("independent prototype decode did not fit the 24 KiB budget: %v", err)
	}
	before, _ := editor.Changes()
	if err := editor.ReplaceContents(nil, infos[0].ID, replacement); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("simultaneous prototype/result/source buffers escaped the working budget: %v", err)
	}
	after, _ := editor.Changes()
	if after != before || len(editor.linkedOverlays) != 0 ||
		editor.roots[0].pixels != nil || editor.roots[1].pixels != nil {
		t.Fatal("working-buffer rejection partially committed a shared replacement")
	}
}

func TestReplaceContentsRepeatWriteSameTargetAndCloseOwnership(t *testing.T) {
	directory := t.TempDir()
	filename := filepath.Join(directory, "shared.psd")
	data := smartEditFixture(t, 1, "")
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	editor, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	infos, err := editor.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	first := smartEditPrototype(1, smartEditImage(smartEditColors()))
	if err := editor.ReplaceSmartObjectContents(nil, infos[0].ID, first); err != nil {
		t.Fatal(err)
	}
	if err := editor.Save(nil, filename, SaveOptions{Compression: 1}); err != nil {
		t.Fatal(err)
	}
	colors := smartEditColors()
	colors[0] = color.NRGBA{240, 30, 80, 255}
	second := smartEditPrototype(2, smartEditImage(colors))
	if err := editor.ReplaceContents(nil, infos[1].ID, second); err != nil {
		t.Fatal(err)
	}
	if err := editor.Save(nil, filename, SaveOptions{Compression: 1}); err != nil {
		t.Fatal(err)
	}
	var repeated bytes.Buffer
	if err := editor.Write(nil, &repeated, SaveOptions{Compression: 1}); err != nil {
		t.Fatal(err)
	}
	savedBytes, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(savedBytes, repeated.Bytes()) {
		t.Fatalf("same-target save lost owned source state or repeat write differs: %v", err)
	}
	saved, err := psdfile.Parse(savedBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	linked, err := saved.LinkedFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	content, err := saved.ReadSpan(linked[0].Data)
	if err != nil || !bytes.Equal(content, second) {
		t.Fatalf("second replacement did not supersede the first: %v", err)
	}
	if err := editor.Close(); err != nil {
		t.Fatal(err)
	}
	if len(editor.linkedOverlays) != 0 || len(editor.linkedRecords) != 0 || editor.roots != nil {
		t.Fatal("closed editor retains owned smart-object replacement buffers")
	}
	for _, operation := range []func() error{
		func() error { _, err := editor.SmartObjects(nil); return err },
		func() error { return editor.ReplaceContents(nil, infos[0].ID, first) },
		func() error { return editor.Write(nil, io.Discard) },
		func() error { _, err := editor.Composite(nil); return err },
	} {
		if err := operation(); !errors.Is(err, psdfile.ErrClosed) {
			t.Fatalf("closed content editor accepted an operation: %v", err)
		}
	}
}

type smartEditSamplingContext struct {
	context.Context
	calls, cancelAt int
	cancel          context.CancelFunc
}

func (c *smartEditSamplingContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		c.cancel()
	}
	return c.Context.Err()
}

func TestReplaceContentsProjectionSamplingCanBeCanceled(t *testing.T) {
	pixels := smartEditImage(smartEditColors())
	layer := &psdmodel.Layer{Placed: &psdmodel.Placed{Width: 2, Height: 2,
		Transform: []float64{0, 0, 512, 0, 512, 512, 0, 512}}}
	plan, err := psdrender.CompileProjection(layer, 512, 512, false)
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &smartEditSamplingContext{Context: base, cancel: cancel, cancelAt: 4}
	projected, _, err := psdrender.ProjectImageContext(ctx, layer, pixels, plan)
	if !errors.Is(err, context.Canceled) || projected != nil || ctx.calls != ctx.cancelAt {
		t.Fatalf("sampling did not stop on cancellation: image=%t, calls=%d, %v", projected != nil, ctx.calls, err)
	}
}

func TestReplaceContentsOptionalNativeSharedSource(t *testing.T) {
	directory := os.Getenv("PSDKIT_SMART_FIXTURE_DIR")
	if directory == "" {
		t.Skip("set PSDKIT_SMART_FIXTURE_DIR to verify generated Photoshop shared-content fixtures")
	}
	filename := filepath.Join(directory, "smart-shared-original.psd")
	original, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	editor, source := smartEditOpen(t, original)
	infos, err := editor.SmartObjects(nil)
	if err != nil || len(infos) != 2 {
		t.Fatalf("native shared-source metadata: count=%d, %v", len(infos), err)
	}
	replacement, err := os.ReadFile(filepath.Join(directory, "smart-replacement.psd"))
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.ReplaceContents(nil, infos[0].ID, replacement); err != nil {
		t.Fatal(err)
	}
	composite, err := editor.Composite(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		point image.Point
		want  color.NRGBA
	}{
		{image.Pt(6, 6), color.NRGBA{20, 220, 80, 255}},
		{image.Pt(22, 6), color.NRGBA{20, 220, 80, 255}},
		{image.Pt(10, 10), color.NRGBA{20, 130, 150, 255}},
		{image.Pt(26, 10), color.NRGBA{20, 130, 150, 255}},
		{image.Pt(16, 8), color.NRGBA{30, 40, 50, 255}},
	} {
		got := composite.NRGBAAt(test.point.X, test.point.Y)
		if math.Abs(float64(got.R)-float64(test.want.R)) > 1 || math.Abs(float64(got.G)-float64(test.want.G)) > 1 ||
			math.Abs(float64(got.B)-float64(test.want.B)) > 1 || got.A != test.want.A {
			t.Fatalf("native shared projection at %v: got %v, want %v", test.point, got, test.want)
		}
	}
	var buffer bytes.Buffer
	if err := editor.Write(nil, &buffer, SaveOptions{Compression: 1}); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Parse(buffer.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = saved.Close() })
	smartEditAssertPreservedMetadata(t, source, saved)
	linked, err := saved.LinkedFiles(nil)
	if err != nil || len(linked) != 1 {
		t.Fatalf("native replacement linked source: count=%d, %v", len(linked), err)
	}
	if linked[0].FileType != "8BPS" {
		t.Fatal("native PSB-to-PSD replacement did not update its linked-file type")
	}
	content, err := saved.ReadSpan(linked[0].Data)
	if err != nil || sha256.Sum256(content) != sha256.Sum256(replacement) {
		t.Fatalf("native replacement encoded content was altered: %v", err)
	}
	after, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("native original file was altered: %v", err)
	}
}
