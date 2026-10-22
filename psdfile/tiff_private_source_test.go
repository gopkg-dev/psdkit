package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"io"
	"math"
	"testing"
)

type tiffPrivateFixture struct {
	data, media []byte
	packets     []Span
	mediaSpan   Span
	planes      [][]byte
}

func tiffPrivateTestTag(key string, data []byte, alignment int) []byte {
	f := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	result := append(f.signature("8BIM"), f.signature(key)...)
	result = append(result, f.u32(uint32(len(data)))...)
	result = append(result, data...)
	for (len(result)-12)%alignment != 0 {
		result = append(result, 0)
	}
	return result
}

func tiffPrivateTestPlacement(id string) []byte {
	f := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	size := f.body("", "size", true,
		f.entry("Wdth", "doub", f.u64(math.Float64bits(2)), true),
		f.entry("Hght", "doub", f.u64(math.Float64bits(2)), true))
	coordinates := f.u32(8)
	for _, value := range []float64{0, 0, 2, 0, 2, 2, 0, 2} {
		coordinates = append(coordinates, f.signature("doub")...)
		coordinates = append(coordinates, f.u64(math.Float64bits(value))...)
	}
	descriptor := append(f.u32(16), f.body("", "null", true,
		f.entry("Idnt", "TEXT", f.unicode(id), true), f.entry("Type", "long", f.u32(2), true),
		f.entry("Sz  ", "Objc", size, true), f.entry("Trnf", "VlLs", coordinates, true))...)
	result := append(f.signature("soLD"), f.u32(4)...)
	return append(result, descriptor...)
}

func tiffPrivateTestChild() []byte {
	data := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	data = append(data, make([]byte, 6)...)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint32(data, 1)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = append(data, make([]byte, 12)...)
	return append(data, 0, 0, 22, 44, 88)
}

func tiffPrivateTestFixture(compression uint16, extraKey string, media []byte) tiffPrivateFixture {
	return tiffPrivateTestFixtureWithPayload(compression, extraKey, media, []byte{1, 2, 3, 4})
}

func tiffPrivateTestFixtureWithPayload(
	compression uint16, extraKey string, media, extraPayload []byte,
) tiffPrivateFixture {
	f := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	planes := [][]byte{{0, 64, 128, 255}, {31, 41, 51, 61}, {71, 81, 91, 101}, {111, 121, 131, 141}}
	var channels [][]byte
	for _, plane := range planes {
		encoded := binary.LittleEndian.AppendUint16(nil, compression)
		if compression == 0 {
			encoded = append(encoded, plane...)
		} else {
			encoded = append(encoded, 3, 0, 3, 0, 1, plane[0], plane[1], 1, plane[2], plane[3])
		}
		channels = append(channels, encoded)
	}
	layer := make([]byte, 16)
	binary.LittleEndian.PutUint32(layer[8:12], 2)
	binary.LittleEndian.PutUint32(layer[12:16], 2)
	layer = binary.LittleEndian.AppendUint16(layer, 4)
	for index, id := range []int16{-1, 0, 1, 2} {
		layer = binary.LittleEndian.AppendUint16(layer, uint16(id))
		layer = append(layer, f.u32(uint32(len(channels[index])))...)
	}
	layer = append(layer, f.signature("8BIM")...)
	layer = append(layer, f.signature("norm")...)
	layer = append(layer, 255, 0, 0, 0)
	extra := append(make([]byte, 8), 1, 'A', 0, 0)
	extra = append(extra, tiffPrivateTestTag("luni", f.unicode("A\x00"), 2)...)
	extra = append(extra, tiffPrivateTestTag("lyid", f.u32(42), 2)...)
	extra = append(extra, tiffPrivateTestTag("SoLd", tiffPrivateTestPlacement("first"), 2)...)
	if extraKey != "" {
		extra = append(extra, tiffPrivateTestTag(extraKey, extraPayload, 2)...)
	}
	layer = append(layer, f.u32(uint32(len(extra)))...)
	layer = append(layer, extra...)
	layr := append([]byte{1, 0}, layer...)
	var packets []Span
	for _, encoded := range channels {
		prefix := 2
		if compression == 1 {
			prefix += 4
		}
		packets = append(packets, Span{Offset: int64(48 + len(layr) + prefix), Length: int64(len(encoded) - prefix)})
		layr = append(layr, encoded...)
	}
	private := []byte("Adobe Photoshop Document Data Block\x00")
	private = append(private, tiffPrivateTestTag("Layr", layr, 4)...)
	body := append(f.signature("liFD"), f.u32(1)...)
	body = append(body, 5)
	body = append(body, "first"...)
	body = append(body, f.unicode("child.psd\x00")...)
	body = append(body, f.signature("8BPS")...)
	body = append(body, f.signature("8BIM")...)
	body = append(body, f.u64(uint64(len(media)))...)
	body = append(body, 0)
	mediaOffset := int64(len(private) + 12 + 8 + len(body))
	body = append(body, media...)
	linked := append(f.u64(uint64(len(body))), body...)
	for (len(linked)-8)%4 != 0 {
		linked = append(linked, 0)
	}
	private = append(private, tiffPrivateTestTag("lnk2", linked, 4)...)
	resource := binary.BigEndian.AppendUint16([]byte("8BIM"), 1037)
	resource = append(resource, 0, 0)
	resource = binary.BigEndian.AppendUint32(resource, 4)
	resource = binary.BigEndian.AppendUint32(resource, 37)
	profile := rasterICC()
	fields := []tiffTestField{{256, 3, 1, []byte{2, 0}}, {257, 3, 1, []byte{2, 0}},
		{258, 3, 3, []byte{8, 0, 8, 0, 8, 0}}, {259, 3, 1, []byte{1, 0}}, {262, 3, 1, []byte{2, 0}},
		{273, 4, 1, make([]byte, 4)}, {277, 3, 1, []byte{3, 0}}, {279, 4, 1, f.u32(12)},
		{34377, 1, uint32(len(resource)), resource}, {34675, 7, uint32(len(profile)), profile},
		{37724, 7, uint32(len(private)), private}}
	data := make([]byte, 8+2+len(fields)*12+4)
	copy(data, "II*\x00")
	binary.LittleEndian.PutUint32(data[4:8], 8)
	binary.LittleEndian.PutUint16(data[8:10], uint16(len(fields)))
	privateOffset := int64(0)
	for index, field := range fields {
		entry := data[10+index*12 : 22+index*12]
		binary.LittleEndian.PutUint16(entry[:2], field.tag)
		binary.LittleEndian.PutUint16(entry[2:4], field.kind)
		binary.LittleEndian.PutUint32(entry[4:8], field.count)
		if len(field.data) <= 4 {
			copy(entry[8:], field.data)
			continue
		}
		if len(data)%2 != 0 {
			data = append(data, 0)
		}
		if field.tag == 37724 {
			privateOffset = int64(len(data))
		}
		binary.LittleEndian.PutUint32(entry[8:], uint32(len(data)))
		data = append(data, field.data...)
	}
	if len(data)%2 != 0 {
		data = append(data, 0)
	}
	binary.LittleEndian.PutUint32(tiffTestEntryOffset(data, 273)[8:], uint32(len(data)))
	data = append(data, bytes.Repeat([]byte{255}, 12)...)
	for index := range packets {
		packets[index].Offset += privateOffset + 13
	}
	return tiffPrivateFixture{data: data, media: media, packets: packets,
		mediaSpan: Span{Offset: privateOffset + mediaOffset + 13, Length: int64(len(media))}, planes: planes}
}

func tiffTestEntryOffset(data []byte, tag uint16) []byte {
	count := int(binary.LittleEndian.Uint16(data[8:10]))
	for index := range count {
		entry := data[10+index*12 : 22+index*12]
		if binary.LittleEndian.Uint16(entry[:2]) == tag {
			return entry
		}
	}
	return nil
}

type tiffPrivateGuardReader struct {
	reader    io.ReaderAt
	forbidden []Span
	active    bool
	bytes     int
	reads     int
}

func (reader *tiffPrivateGuardReader) ReadAt(data []byte, offset int64) (int, error) {
	if reader.active {
		for _, span := range reader.forbidden {
			if len(data) != 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
				return 0, errors.New("pixel or media payload was read while indexing")
			}
		}
	}
	count, err := reader.reader.ReadAt(data, offset)
	reader.bytes += count
	reader.reads++
	return count, err
}

func TestTIFFPrivateLayerSourceOwnsPlanesAndMapsNormalizedFields(t *testing.T) {
	for _, compression := range []uint16{0, 1} {
		fixture := tiffPrivateTestFixture(compression, "", tiffPrivateTestChild())
		parent, record := rasterSource(fixture.data)
		defer parent.Close()
		guard := &tiffPrivateGuardReader{reader: parent.reader, active: true,
			forbidden: append(append([]Span{}, fixture.packets...), fixture.mediaSpan)}
		parent.reader = guard
		source, err := parent.OpenLinkedTIFFLayers(nil, record)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		if source.Info().LayerCount != 1 || source.Info().SavedPreviewKind != "flattened TIFF main IFD saved preview" {
			t.Fatalf("private layer source provenance was lost: %+v", source.Info())
		}
		layer, err := source.LayerRecord(nil, 0)
		if err != nil || layer.Name != "A" || layer.ID != 42 || layer.Bounds != image.Rect(0, 0, 2, 2) {
			t.Fatalf("private layer fields differ: %+v/%v", layer, err)
		}
		placements, err := source.ReadSmartObjectView(nil)
		if err != nil || len(placements.Records) != 1 || placements.Records[0].SourceID != "first" ||
			placements.Records[0].Width != 2 || placements.Records[0].Height != 2 {
			t.Fatalf("private modern placement differs: %+v/%v", placements, err)
		}
		linked, err := source.ReadLinkedFileView(nil)
		if err != nil || len(linked.Records) != 1 || linked.Records[0].Data != fixture.mediaSpan {
			t.Fatalf("borrowed child media mapping differs: %+v/%v", linked, err)
		}
		resources, err := source.Resources(nil)
		if err != nil || len(resources) != 1 || resources[0].ID != 1037 {
			t.Fatalf("big-endian resource fields changed: %+v/%v", resources, err)
		}
		resourceData, err := source.ReadOriginalSpan(nil, resources[0].Data)
		if err != nil || binary.BigEndian.Uint32(resourceData) != 37 {
			t.Fatalf("TIFF image resource byte order was changed: %v", err)
		}
		profile, err := source.ICCProfile(nil)
		if err != nil || !bytes.Equal(profile, rasterICC()) {
			t.Fatalf("TIFF original ICC bytes changed: %v", err)
		}
		countSpan := Span{Offset: source.Info().Layers.Offset, Length: 2}
		original, err := source.ReadOriginalSpan(nil, countSpan)
		if err != nil || !bytes.Equal(original, []byte{1, 0}) {
			t.Fatalf("original private layer count was normalized: %v", err)
		}
		normalized, err := source.ReadNormalizedSpan(nil, countSpan)
		if err != nil || !bytes.Equal(normalized, []byte{0, 1}) {
			t.Fatalf("normalized private count differs: %v", err)
		}
		mapping, err := source.OriginalSpan(nil, countSpan)
		ranges, rangeErr := source.NormalizationRanges(nil, countSpan)
		if err != nil || rangeErr != nil || mapping != countSpan || len(ranges) != 1 || ranges[0].UnitBytes != 2 {
			t.Fatalf("normalized-to-original mapping lost its unit semantics: %v/%v", err, rangeErr)
		}
		guard.active = false
		pixels, err := source.DecodeLayer(nil, 0)
		if err != nil || len(pixels.Channels) != 4 {
			t.Fatalf("private saved planes differ: %v", err)
		}
		for index, plane := range pixels.Channels {
			if !bytes.Equal(plane.Data, fixture.planes[index]) {
				t.Fatalf("private channel %d values differ", index)
			}
			region, err := source.DecodeChannelRegion(nil, 0, index, image.Rect(1, 1, 2, 2))
			if err != nil || !bytes.Equal(region.Data, fixture.planes[index][3:]) {
				t.Fatalf("private channel %d region differs: %v", index, err)
			}
		}
		child, err := source.OpenLinkedDocument(nil, linked.Records[0])
		if err != nil || child.Header.Signature != "8BPS" {
			t.Fatalf("actual embedded PSD child could not open: %v", err)
		}
		defer child.Close()
		preview, err := source.SavedPreview(nil)
		if err != nil || preview.Kind != source.Info().SavedPreviewKind || preview.Raster.Pixels.Pix[3] != 255 {
			t.Fatalf("saved TIFF main image was replaced with private alpha: %v", err)
		}
		layer.Channels[0].Compression = 999
		again, err := source.LayerRecord(nil, 0)
		if err != nil || again.Channels[0].Compression != compression {
			t.Fatalf("returned private record aliases internal metadata: %v", err)
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := child.DecodeComposite(nil); !errors.Is(err, ErrClosed) {
			t.Fatalf("child borrowed from closed private view remained valid: %v", err)
		}
		if _, err := source.DecodeLayer(nil, 0); !errors.Is(err, ErrClosed) {
			t.Fatalf("closed private source decoded pixels: %v", err)
		}
		if !bytes.Equal(pixels.Channels[0].Data, fixture.planes[0]) {
			t.Fatal("owned private samples were invalidated by Close")
		}
	}
}

func TestTIFFPrivateIndexNeverReadsLargeMediaAndRejectsUnknownSemantics(t *testing.T) {
	fixture := tiffPrivateTestFixture(1, "", bytes.Repeat([]byte{0xab}, 6<<20))
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	guard := &tiffPrivateGuardReader{reader: parent.reader, active: true,
		forbidden: append(append([]Span{}, fixture.packets...), fixture.mediaSpan)}
	parent.reader = guard
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.ReadLinkedFileView(nil); err != nil {
		t.Fatal(err)
	}
	if guard.bytes > 4096 {
		t.Fatalf("private TIFF indexing read deferred payload bytes: %d", guard.bytes)
	}
	broken := tiffPrivateTestFixture(0, "zzzz", tiffPrivateTestChild())
	unknown, entry := rasterSource(broken.data)
	defer unknown.Close()
	if _, err := unknown.OpenLinkedTIFFLayers(nil, entry); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown private layer block was silently accepted: %v", err)
	}
	opaqueFixture := tiffPrivateTestFixture(0, "shmd", tiffPrivateTestChild())
	opaqueParent, opaqueEntry := rasterSource(opaqueFixture.data)
	defer opaqueParent.Close()
	opaque, err := opaqueParent.OpenLinkedTIFFLayers(nil, opaqueEntry)
	if err != nil {
		t.Fatal(err)
	}
	defer opaque.Close()
	blocks, err := opaque.OpaqueBlocks(nil)
	if err != nil || len(blocks) != 1 || blocks[0].Key != "shmd" {
		t.Fatalf("unparsed private schema was not explicitly marked opaque: %v", err)
	}
	if _, err := opaque.ReadNormalizedSpan(nil, blocks[0].Data); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("opaque private bytes were mislabeled as normalized: %v", err)
	}
}

func TestTIFFPrivateSourceClosedCancellationLimitsAndReaderCauses(t *testing.T) {
	fixture := tiffPrivateTestFixture(0, "", tiffPrivateTestChild())
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := parent.OpenLinkedTIFFLayers(ctx, record); !errors.Is(err, context.Canceled) {
		t.Fatalf("private indexing ignored cancellation: %v", err)
	}
	for _, options := range []LoadOptions{{Limits: Limits{MaxMetadataBytes: 4096}},
		{Limits: Limits{MaxReadBytes: 4}}, {Limits: Limits{MaxDescriptorItems: 2}}} {
		if _, err := parent.OpenLinkedTIFFLayers(nil, record, options); !errors.Is(err, ErrLimit) {
			t.Fatalf("private indexing budget was ignored: %v", err)
		}
	}
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.DecodeLayer(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("private decode cancellation was lost: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadSmartObjectView(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("private placement access outlived the original source: %v", err)
	}
	failure := errors.New("independent private source failure")
	for _, cause := range []error{failure, errors.Join(io.EOF, failure)} {
		broken, entry := rasterSource(fixture.data)
		broken.reader = failedRasterRead{reader: broken.reader.(*bytes.Reader), cause: cause}
		defer broken.Close()
		if _, err := broken.OpenLinkedTIFFLayers(nil, entry); !errors.Is(err, failure) {
			t.Fatalf("private original ReaderAt cause was discarded: %v", err)
		}
	}
}

func TestTIFFPrivateSourceWorkingDeficitAndMalformedPreviewStayStrict(t *testing.T) {
	fixture := tiffPrivateTestFixture(1, "", tiffPrivateTestChild())
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	reservation := source.Info().MetadataBytes
	for _, limits := range []Limits{{MaxMetadataBytes: reservation - 1}, {MaxDecodeBytes: reservation - 1},
		{MaxBlocks: 2}} {
		if _, err := parent.OpenLinkedTIFFLayers(nil, record, LoadOptions{Limits: limits}); !errors.Is(err, ErrLimit) {
			t.Fatalf("private source reservation deficit was accepted: %v", err)
		}
	}
	brokenData := bytes.Clone(fixture.data)
	binary.LittleEndian.PutUint32(tiffTestEntryOffset(brokenData, 279)[8:], 11)
	broken, entry := rasterSource(brokenData)
	defer broken.Close()
	if _, err := broken.OpenLinkedTIFFLayers(nil, entry); !errors.Is(err, ErrMalformed) {
		t.Fatalf("broken main IFD was hidden by valid private channels: %v", err)
	}
}

func TestTIFFPrivateCorruptRLETableAndPacketsFailAtTheirReadStage(t *testing.T) {
	fixture := tiffPrivateTestFixture(1, "", tiffPrivateTestChild())
	badTable := bytes.Clone(fixture.data)
	packetOffset := fixture.packets[0].Offset - 13
	binary.LittleEndian.PutUint16(badTable[packetOffset-4:], 4)
	parent, record := rasterSource(badTable)
	defer parent.Close()
	if _, err := parent.OpenLinkedTIFFLayers(nil, record); !errors.Is(err, ErrMalformed) {
		t.Fatalf("private RLE row-count total was not validated during indexing: %v", err)
	}
	badPacket := bytes.Clone(fixture.data)
	badPacket[packetOffset] = 2
	packetParent, packetRecord := rasterSource(badPacket)
	defer packetParent.Close()
	source, err := packetParent.OpenLinkedTIFFLayers(nil, packetRecord)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := source.DecodeChannel(nil, 0, 0); !errors.Is(err, ErrMalformed) {
		t.Fatalf("private RLE packet crossing the declared row was accepted: %v", err)
	}
}
