package psdscene

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type tilePlacedSparseReader struct {
	prefix    []byte
	length    int64
	forbidden []psdfile.Span
	reads     int
}

func (reader *tilePlacedSparseReader) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			reader.reads++
			return 0, errors.New("saved placed-layer or merged footer samples are forbidden")
		}
	}
	if offset < 0 || offset >= reader.length {
		return 0, io.EOF
	}
	count := min(len(data), int(reader.length-offset))
	clear(data[:count])
	if offset < int64(len(reader.prefix)) {
		copy(data[:count], reader.prefix[offset:])
	}
	if count < len(data) {
		return count, io.EOF
	}
	return count, nil
}

func tilePlacedLargeSource(t *testing.T, external bool) (*psdfile.Document, *tilePlacedSparseReader, []byte) {
	t.Helper()
	const width, height = 300_000, 300_000
	id := "tile-source"
	prototype := cachedTestSource(nil)
	transform := []float64{234567, 299999, 234569, 299999, 234569, 300000, 234567, 300000}
	items := make([]psdfile.DescriptorValue, len(transform))
	for index, value := range transform {
		items[index] = psdfile.DescriptorValue{Type: "doub", Value: value}
	}
	descriptor := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: id}, {Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(1)},
		}}}, {Key: "Trnf", Type: "VlLs", Value: items},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	placed := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	extra := append(make([]byte, 8), 1, 'L', 0, 0)
	extra = append(extra, artboardTestBlock("SoLd", placed)...)
	var layers bytes.Buffer
	_ = binary.Write(&layers, binary.BigEndian, int16(-1))
	for _, value := range []int32{0, 0, 1, 2} {
		_ = binary.Write(&layers, binary.BigEndian, value)
	}
	_ = binary.Write(&layers, binary.BigEndian, uint16(4))
	for _, id := range []int16{0, 1, 2, -1} {
		_ = binary.Write(&layers, binary.BigEndian, id)
		_ = binary.Write(&layers, binary.BigEndian, uint64(4))
	}
	layers.WriteString("8BIMnorm")
	layers.Write([]byte{255, 0, 0, 0})
	_ = binary.Write(&layers, binary.BigEndian, uint32(len(extra)))
	layers.Write(extra)
	layers.Write([]byte{0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 255, 255, 0, 0, 255, 255})
	if layers.Len()%2 != 0 {
		layers.WriteByte(0)
	}
	var linked bytes.Buffer
	kind := "liFD"
	if external {
		kind = "liFE"
	}
	linked.WriteString(kind)
	_ = binary.Write(&linked, binary.BigEndian, uint32(1))
	linked.WriteByte(byte(len(id)))
	linked.WriteString(id)
	linked.Write(cachedTestUnicode("prototype.psd"))
	linked.WriteString("8BPS8BIM")
	size := uint64(len(prototype))
	if external {
		size = 0
	}
	_ = binary.Write(&linked, binary.BigEndian, size)
	linked.WriteByte(0)
	if external {
		descriptor, err := psdfile.EncodeDescriptor(nil, &psdfile.Descriptor{ClassID: "null"}, true)
		if err != nil {
			t.Fatal(err)
		}
		linked.Write(descriptor)
		_ = binary.Write(&linked, binary.BigEndian, uint64(len(prototype)))
	} else {
		linked.Write(prototype)
	}
	entry := binary.BigEndian.AppendUint64(nil, uint64(linked.Len()))
	entry = append(entry, linked.Bytes()...)
	for len(entry)%4 != 0 {
		entry = append(entry, 0)
	}
	global := binary.BigEndian.AppendUint64([]byte("8BIMlnk2"), uint64(len(entry)))
	global = append(global, entry...)
	section := binary.BigEndian.AppendUint64(nil, uint64(layers.Len()))
	section = append(section, layers.Bytes()...)
	section = binary.BigEndian.AppendUint32(section, 0)
	section = append(section, global...)
	prefix := binary.BigEndian.AppendUint16([]byte("8BPS"), 2)
	prefix = append(prefix, make([]byte, 6)...)
	prefix = binary.BigEndian.AppendUint16(prefix, 4)
	prefix = binary.BigEndian.AppendUint32(prefix, height)
	prefix = binary.BigEndian.AppendUint32(prefix, width)
	prefix = binary.BigEndian.AppendUint16(prefix, 8)
	prefix = binary.BigEndian.AppendUint16(prefix, 3)
	prefix = binary.BigEndian.AppendUint64(prefix, 0)
	prefix = binary.BigEndian.AppendUint64(prefix, uint64(len(section)))
	prefix = append(prefix, section...)
	prefix = binary.BigEndian.AppendUint16(prefix, 0)
	length := int64(len(prefix)) + int64(width)*height*4
	reader := &tilePlacedSparseReader{prefix: prefix, length: length}
	source, err := psdfile.Read(reader, length, psdfile.LoadOptions{Limits: psdfile.Limits{
		MaxFileBytes: length, MaxDecodeBytes: 4 << 20, MaxDecodePixels: 10_000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for _, channel := range source.Layers[0].Channels {
		reader.forbidden = append(reader.forbidden, channel.Data)
	}
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	return source, reader, prototype
}

func TestLivePlacedTileRebuildsHugeParentWithoutSavedLayerOrFooter(t *testing.T) {
	for _, external := range []bool{false, true} {
		source, reader, prototype := tilePlacedLargeSource(t, external)
		options := TileOptions{MaxWorkingBytes: 4 << 20, MaxPixels: 4}
		if external {
			options.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				return psdfile.Parse(prototype)
			}
		}
		rectangle := image.Rect(234_567, 299_999, 234_569, 300_000)
		result, err := RenderTile(nil, source, rectangle, options)
		if err != nil || result.Rect != rectangle ||
			!bytes.Equal(result.Pix, []byte{201, 21, 39, 255, 17, 31, 77, 128}) {
			t.Fatalf("huge parent live placed source reconstruction failed: external=%t error=%v", external, err)
		}
		if reader.reads != 0 {
			t.Fatal("live placed tile used saved parent channels or merged footer")
		}
		if _, err := Render(nil, source, Options{MaxWorkingBytes: 4 << 20,
			ExternalResolver: options.ExternalResolver}); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("full giant parent unexpectedly allocated inside tile budget: %v", err)
		}
	}
}

func TestCachedPlacedTileRequiresExplicitSelectionAndNeverMasksLiveFailure(t *testing.T) {
	source, reader, _ := tilePlacedLargeSource(t, true)
	if _, err := RenderTile(nil, source, image.Rect(234_567, 299_999, 234_569, 300_000)); !errors.Is(err, psdfile.ErrUnsupported) || reader.reads != 0 {
		t.Fatalf("missing external live source silently fell back to saved channels: %v", err)
	}
	reader.forbidden = []psdfile.Span{source.Composite.Data}
	result, err := RenderTile(nil, source, image.Rect(0, 0, 2, 1), TileOptions{CachedPlacedLayers: true})
	if err != nil || !bytes.Equal(result.Pix, []byte{255, 0, 255, 255, 255, 0, 255, 255}) {
		t.Fatalf("explicit saved placed selection did not use its own poisoned layer cache: %v", err)
	}
}
