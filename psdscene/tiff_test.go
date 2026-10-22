package psdscene

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func sceneTIFFSource(t *testing.T) (*psdfile.Document, *sceneTIFFReader) {
	t.Helper()
	// Eight sorted IFD fields end at 110; RGB8 component depths precede six owned source samples.
	payload := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 8, 0}
	for _, field := range [][4]uint32{
		{256, 3, 1, 2}, {257, 3, 1, 1}, {258, 3, 3, 110}, {259, 3, 1, 1},
		{262, 3, 1, 2}, {273, 4, 1, 116}, {277, 3, 1, 3}, {279, 4, 1, 6},
	} {
		payload = binary.LittleEndian.AppendUint16(payload, uint16(field[0]))
		payload = binary.LittleEndian.AppendUint16(payload, uint16(field[1]))
		payload = binary.LittleEndian.AppendUint32(payload, field[2])
		payload = binary.LittleEndian.AppendUint32(payload, field[3])
	}
	payload = append(payload, 0, 0, 0, 0, 8, 0, 8, 0, 8, 0, 23, 47, 89, 113, 151, 199)
	id := "tiff-source"
	values := make([]psdfile.DescriptorValue, 8)
	for index, value := range []float64{0, 0, 2, 0, 2, 1, 0, 1} {
		values[index] = psdfile.DescriptorValue{Type: "doub", Value: value}
	}
	descriptor := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: id}, {Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(1)},
		}}}, {Key: "Trnf", Type: "VlLs", Value: values},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	placed := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	extra := append(make([]byte, 8), 1, 'L', 0, 0)
	extra = append(extra, artboardTestBlock("SoLd", placed)...)
	var layers bytes.Buffer
	layers.Write(binary.BigEndian.AppendUint16(nil, 65535))
	for _, value := range []uint32{0, 0, 1, 2} {
		layers.Write(binary.BigEndian.AppendUint32(nil, value))
	}
	layers.Write(binary.BigEndian.AppendUint16(nil, 4))
	for _, channelID := range []int16{0, 1, 2, -1} {
		layers.Write(binary.BigEndian.AppendUint16(nil, uint16(channelID)))
		layers.Write(binary.BigEndian.AppendUint32(nil, 4))
	}
	layers.WriteString("8BIMnorm")
	layers.Write([]byte{255, 0, 0, 0})
	layers.Write(binary.BigEndian.AppendUint32(nil, uint32(len(extra))))
	layers.Write(extra)
	// The magenta saved placed pixels differ from the original TIFF samples.
	layers.Write([]byte{0, 0, 255, 255, 0, 0, 0, 0, 0, 0, 255, 255, 0, 0, 255, 255})
	if layers.Len()%2 != 0 {
		layers.WriteByte(0)
	}
	body := binary.BigEndian.AppendUint32([]byte("liFD"), 1)
	body = append(body, byte(len(id)))
	body = append(body, id...)
	body = append(body, cachedTestUnicode("original.tif")...)
	body = append(body, "TIFF8BIM"...)
	body = binary.BigEndian.AppendUint64(body, uint64(len(payload)))
	body = append(body, 0)
	body = append(body, payload...)
	linked := binary.BigEndian.AppendUint64(nil, uint64(len(body)))
	linked = append(linked, body...)
	for len(linked)%4 != 0 {
		linked = append(linked, 0)
	}
	section := binary.BigEndian.AppendUint32(nil, uint32(layers.Len()))
	section = append(section, layers.Bytes()...)
	section = binary.BigEndian.AppendUint32(section, 0)
	section = append(section, artboardTestBlock("lnk2", linked)...)
	prefix := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	prefix = append(prefix, make([]byte, 6)...)
	prefix = binary.BigEndian.AppendUint16(prefix, 4)
	prefix = binary.BigEndian.AppendUint32(prefix, 1)
	prefix = binary.BigEndian.AppendUint32(prefix, 2)
	prefix = binary.BigEndian.AppendUint16(prefix, 8)
	prefix = binary.BigEndian.AppendUint16(prefix, 3)
	prefix = append(prefix, make([]byte, 8)...)
	prefix = binary.BigEndian.AppendUint32(prefix, uint32(len(section)))
	prefix = append(prefix, section...)
	prefix = append(prefix, 0, 0, 255, 255, 0, 0, 255, 255, 255, 255)
	reader := &sceneTIFFReader{data: prefix}
	source, err := psdfile.Read(reader, int64(len(prefix)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for _, channel := range source.Layers[0].Channels {
		reader.forbidden = append(reader.forbidden, channel.Data)
	}
	reader.forbidden = append(reader.forbidden, source.Composite.Data)
	return source, reader
}

type sceneTIFFReader struct {
	data      []byte
	forbidden []psdfile.Span
	reads     int
	active    bool
}

func (reader *sceneTIFFReader) ReadAt(data []byte, offset int64) (int, error) {
	if reader.active {
		for _, span := range reader.forbidden {
			if len(data) != 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
				reader.reads++
				return 0, errors.New("saved placed RGB and merged preview reads are forbidden")
			}
		}
	}
	count, err := bytes.NewReader(reader.data).ReadAt(data, offset)
	if count != len(data) && err == nil {
		err = io.EOF
	}
	return count, err
}

func TestSceneFlatTIFFContentsIgnoreCachedRGBAndTilesDoNotReadParentPixels(t *testing.T) {
	source, reader := sceneTIFFSource(t)
	full, err := Render(nil, source)
	want := []byte{23, 47, 89, 255, 113, 151, 199, 255}
	if err != nil || !bytes.Equal(full.Pix, want) {
		t.Fatalf("live TIFF contents differ: %v", err)
	}
	reader.active = true
	if err := VisitTiles(nil, source, full.Rect, image.Pt(1, 1), func(tile *image.NRGBA) error {
		for x := tile.Rect.Min.X; x < tile.Rect.Max.X; x++ {
			if tile.NRGBAAt(x, 0) != full.NRGBAAt(x, 0) {
				t.Fatalf("TIFF contents tile differs at %d", x)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if reader.reads != 0 {
		t.Fatal("TIFF live contents reused the saved parent cache")
	}
}
