package psdscene

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdfile"
)

type externalRasterFixtureLink struct {
	id, name, fileType string
	contents           []byte
	cached             []byte
}

func externalRasterFixture(t *testing.T, links []externalRasterFixtureLink, instances ...int) []byte {
	t.Helper()
	var records, channels bytes.Buffer
	_ = binary.Write(&records, binary.BigEndian, -int16(len(instances)))
	for index, instance := range instances {
		transform := make([]psdfile.DescriptorValue, 8)
		for coordinate, value := range []float64{float64(index * 2), 0, float64(index*2 + 2), 0,
			float64(index*2 + 2), 1, float64(index * 2), 1} {
			transform[coordinate] = psdfile.DescriptorValue{Type: "doub", Value: value}
		}
		descriptor := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
			{Key: "Idnt", Type: "TEXT", Value: links[instance].id},
			{Key: "Type", Type: "long", Value: int32(2)},
			{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
				{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(1)},
			}}}, {Key: "Trnf", Type: "VlLs", Value: transform},
		}}
		encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
		if err != nil {
			t.Fatal(err)
		}
		extra := append(make([]byte, 8), 1, 'L', 0, 0)
		extra = append(extra, artboardTestBlock("SoLd", append([]byte("soLD\x00\x00\x00\x04"), encoded...))...)
		for _, value := range []int32{0, int32(index * 2), 1, int32(index*2 + 2)} {
			_ = binary.Write(&records, binary.BigEndian, value)
		}
		_ = binary.Write(&records, binary.BigEndian, uint16(4))
		for _, id := range []int16{0, 1, 2, -1} {
			_ = binary.Write(&records, binary.BigEndian, id)
			_ = binary.Write(&records, binary.BigEndian, uint32(4))
			channels.Write([]byte{0, 0, 255, 255})
		}
		records.WriteString("8BIMnorm")
		records.Write([]byte{255, 0, 0, 0})
		_ = binary.Write(&records, binary.BigEndian, uint32(len(extra)))
		records.Write(extra)
	}
	records.Write(channels.Bytes())
	if records.Len()%2 != 0 {
		records.WriteByte(0)
	}
	var linked []byte
	for _, link := range links {
		kind := "liFE"
		if link.contents != nil {
			kind = "liFD"
		}
		version := uint32(1)
		payload := link.contents
		if link.cached != nil && link.contents == nil {
			version, payload = 2, link.cached
		}
		body := binary.BigEndian.AppendUint32([]byte(kind), version)
		body = append(body, byte(len(link.id)))
		body = append(body, link.id...)
		body = append(body, cachedTestUnicode(link.name)...)
		body = append(body, link.fileType...)
		body = append(body, "8BIM"...)
		body = binary.BigEndian.AppendUint64(body, uint64(len(payload)))
		body = append(body, 0)
		if link.contents == nil {
			descriptor, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "null"}, true)
			if err != nil {
				t.Fatal(err)
			}
			body = append(body, descriptor...)
			body = binary.BigEndian.AppendUint64(body, 0)
		}
		body = append(body, payload...)
		linked = binary.BigEndian.AppendUint64(linked, uint64(len(body)))
		linked = append(linked, body...)
		for len(linked)%4 != 0 {
			linked = append(linked, 0)
		}
	}
	section := append(artboardTestU32(uint32(records.Len())), records.Bytes()...)
	section = append(section, 0, 0, 0, 0)
	section = append(section, artboardTestBlock("lnkE", linked)...)
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 4)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint32(data, uint32(len(instances)*2))
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 8)...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(section)))
	data = append(data, section...)
	data = append(data, 0, 0)
	return append(data, bytes.Repeat([]byte{255}, len(instances)*8)...)
}

func externalRasterPNG(t *testing.T, pixels *image.NRGBA, profile []byte) []byte {
	t.Helper()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	if profile == nil {
		return encoded.Bytes()
	}
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(profile); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	body := append([]byte("scene\x00\x00"), compressed.Bytes()...)
	chunk := binary.BigEndian.AppendUint32(nil, uint32(len(body)))
	chunk = append(chunk, "iCCP"...)
	chunk = append(chunk, body...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(chunk[4:]))
	data := append(bytes.Clone(encoded.Bytes()[:33]), chunk...)
	return append(data, encoded.Bytes()[33:]...)
}

func externalRasterTIFF(pixels *image.NRGBA) []byte {
	const fields = 10
	const directoryEnd = 8 + 2 + fields*12 + 4
	data := make([]byte, directoryEnd)
	copy(data, "II*\x00\x08\x00\x00\x00")
	binary.LittleEndian.PutUint16(data[8:], fields)
	for index, field := range [][4]uint32{
		{256, 3, 1, 2}, {257, 3, 1, 1}, {258, 3, 4, directoryEnd}, {259, 3, 1, 1}, {262, 3, 1, 2},
		{273, 4, 1, directoryEnd + 8}, {277, 3, 1, 4}, {278, 4, 1, 1}, {279, 4, 1, 8}, {338, 3, 1, 2},
	} {
		entry := data[10+index*12:]
		binary.LittleEndian.PutUint16(entry, uint16(field[0]))
		binary.LittleEndian.PutUint16(entry[2:], uint16(field[1]))
		binary.LittleEndian.PutUint32(entry[4:], field[2])
		binary.LittleEndian.PutUint32(entry[8:], field[3])
	}
	data = append(data, 8, 0, 8, 0, 8, 0, 8, 0)
	return append(data, pixels.Pix...)
}

func externalRasterInput() *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	pixels.SetNRGBA(0, 0, color.NRGBA{R: 201, G: 21, B: 39, A: 255})
	pixels.SetNRGBA(1, 0, color.NRGBA{R: 17, G: 31, B: 77, A: 128})
	return pixels
}

func TestExternalRasterFormatsFullTileVisitorAndPlacedArtwork(t *testing.T) {
	input := externalRasterInput()
	var jpegBytes bytes.Buffer
	if err := jpeg.Encode(&jpegBytes, input, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		fileType string
		data     []byte
	}{{"PNGf", externalRasterPNG(t, input, nil)}, {"JPEG", jpegBytes.Bytes()}, {"TIFF", externalRasterTIFF(input)}} {
		t.Run(fixture.fileType, func(t *testing.T) {
			data := externalRasterFixture(t, []externalRasterFixtureLink{{id: "shared", name: "chosen",
				fileType: fixture.fileType}}, 0, 0)
			before, encodedBefore := sha256.Sum256(data), sha256.Sum256(fixture.data)
			source := cachedOpen(t, data)
			decoded, err := psdfile.DecodeRasterContents(t.Context(), fixture.data)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			resolver := func(_ context.Context, record psdfile.LinkedFileRecord) ([]byte, error) {
				calls++
				if record.ID != "shared" || record.Name != "chosen" || record.FileType != fixture.fileType {
					t.Fatal("raster resolver did not receive retained source metadata")
				}
				return fixture.data, nil
			}
			options := Options{ExternalRasterResolver: resolver}
			full, err := Render(t.Context(), source, options)
			if err != nil || calls != 1 {
				t.Fatalf("full render did not reuse a shared external prototype: calls=%d error=%v", calls, err)
			}
			for x := range 4 {
				if full.NRGBAAt(x, 0) != decoded.Pixels.NRGBAAt(x%2, 0) {
					t.Fatalf("external source pixels differ at %d", x)
				}
			}
			tileOptions := TileOptions{ExternalRasterResolver: resolver}
			calls = 0
			tile, err := RenderTile(t.Context(), source, full.Rect, tileOptions)
			if err != nil || calls != 1 || !bytes.Equal(tile.Pix, full.Pix) {
				t.Fatalf("tile render lost shared external pixels: calls=%d error=%v", calls, err)
			}
			if err := VisitTiles(t.Context(), source, full.Rect, image.Pt(1, 1), func(tile *image.NRGBA) error {
				if tile.NRGBAAt(tile.Rect.Min.X, 0) != full.NRGBAAt(tile.Rect.Min.X, 0) {
					t.Fatal("external source visitor changed absolute pixels")
				}
				return nil
			}, tileOptions); err != nil {
				t.Fatal(err)
			}
			layer, err := RenderPlacedLayer(t.Context(), source, 0, options)
			if err != nil || !bytes.Equal(layer.Pixels.Pix, decoded.Pixels.Pix) ||
				!layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
				t.Fatalf("single external placed layer lost source provenance: %v", err)
			}
			if sha256.Sum256(data) != before || sha256.Sum256(fixture.data) != encodedBefore {
				t.Fatal("rendering changed parent or resolved encoded bytes")
			}
			retained := bytes.Clone(full.Pix)
			clear(fixture.data)
			if !bytes.Equal(retained, full.Pix) {
				t.Fatal("external source output borrowed callback storage")
			}
		})
	}
}

func TestExternalRasterHugePSBTileIgnoresSavedCaches(t *testing.T) {
	source, reader, _ := tilePlacedLargeSource(t, true)
	position := bytes.Index(reader.prefix, []byte("8BPS8BIM"))
	if position < 0 {
		t.Fatal("external file type fixture is absent")
	}
	copy(reader.prefix[position:], "PNGf")
	encoded := externalRasterPNG(t, externalRasterInput(), nil)
	calls := 0
	options := TileOptions{MaxWorkingBytes: 4 << 20, MaxPixels: 4,
		ExternalRasterResolver: func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
			calls++
			return encoded, nil
		}}
	rectangle := image.Rect(234_567, 299_999, 234_569, 300_000)
	result, err := RenderTile(t.Context(), source, rectangle, options)
	if err != nil || calls != 1 || reader.reads != 0 || !bytes.Equal(result.Pix, externalRasterInput().Pix) {
		t.Fatalf("external raster used a huge parent cache or incorrect coordinates: %v", err)
	}
}

func TestExternalRasterICCUsesExistingExplicitColorPolicy(t *testing.T) {
	profile := sceneCostReviewProfile(1)
	encoded := externalRasterPNG(t, externalRasterInput(), profile)
	source := cachedOpen(t, externalRasterFixture(t,
		[]externalRasterFixtureLink{{id: "profile", name: "tagged.png", fileType: "PNGf"}}, 0))
	resolver := func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return encoded, nil }
	if result, err := Render(t.Context(), source, Options{ExternalRasterResolver: resolver}); result != nil || !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("tagged external raster silently assumed an untagged parent profile: %v", err)
	}
	options := Options{ExternalRasterResolver: resolver, AssumeUntaggedSRGB: true}
	full, err := Render(t.Context(), source, options)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := psdcolor.TransformProfile(t.Context(), externalRasterInput(), profile, nil,
		psdcolor.ProfileTransformOptions{AssumeSRGB: true})
	if err != nil || !bytes.Equal(full.Pix, expected.Pix) {
		t.Fatalf("external raster did not use the existing source color conversion: %v", err)
	}
	tile, err := RenderTile(t.Context(), source, full.Rect,
		TileOptions{ExternalRasterResolver: resolver, AssumeUntaggedSRGB: true})
	if err != nil || !bytes.Equal(tile.Pix, full.Pix) {
		t.Fatalf("regional external raster changed ICC conversion: %v", err)
	}
}
