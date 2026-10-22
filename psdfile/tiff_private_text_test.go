package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdtext"
)

func tiffPrivateMixedTextFixture(engine []byte) ([]byte, map[string]int) {
	little := tiffPrivateDescriptorFixture{order: binary.LittleEndian, little: true}
	big := tiffPrivateDescriptorFixture{order: binary.BigEndian}
	data := binary.LittleEndian.AppendUint16(nil, 1)
	for _, bits := range []uint64{math.Float64bits(1), 1 << 63, 0, math.Float64bits(1),
		math.Float64bits(20.5), math.Float64bits(-5.25)} {
		data = append(data, little.u64(bits)...)
	}
	data = binary.BigEndian.AppendUint16(data, 50)
	text := append(big.u32(16), big.body("", "TxLr", true,
		big.entry("Txt ", "TEXT", big.unicode("前😀(后)\\"), true),
		big.entry("EngineData", "tdta", append(big.u32(uint32(len(engine))), engine...), false))...)
	data = append(data, text...)
	fields := map[string]int{"warpVersion": len(data)}
	data = binary.LittleEndian.AppendUint16(data, 1)
	warp := append(little.u32(16), little.body("", "warp", false,
		little.entry("warpStyle", "enum", append(little.identifier("warpStyle", false),
			little.identifier("warpNone", false)...), false),
		little.entry("warpValue", "doub", little.u64(math.Float64bits(15)), false))...)
	data = append(data, warp...)
	fields["bounds"] = len(data)
	data = append(data, make([]byte, 16)...)
	return append(data, 0, 0, 0), fields
}

func TestTIFFPrivateMixedTextViewOwnsMetadataAndKeepsEngineBytesLazy(t *testing.T) {
	engine := textFixtureEngine("前😀(后)\\")
	payload, _ := tiffPrivateMixedTextFixture(engine)
	fixture := tiffPrivateTestFixtureWithPayload(1, "TySh", tiffPrivateTestChild(), payload)
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	engineOffset := int64(bytes.Index(fixture.data, engine)) + record.Data.Offset
	guard := &tiffPrivateGuardReader{reader: parent.reader, active: true,
		forbidden: append(append(append([]Span{}, fixture.packets...), fixture.mediaSpan),
			Span{Offset: engineOffset, Length: int64(len(engine))})}
	parent.reader = guard
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	view, err := source.ReadTextLayerView(nil)
	if err != nil || len(view.Records) != 1 {
		t.Fatalf("mixed text view did not remain lazy: %v", err)
	}
	text := view.Records[0]
	if text.Text != "前😀(后)\\" || text.Transform != ([6]float64{1, 0, 0, 1, 20.5, -5.25}) ||
		text.TransformBits[1] != 1<<63 || text.TextVersion != 50 || text.WarpVersion != 1 ||
		textDescriptorValue(text.Warp, "warpValue") != float64(15) || text.EngineData.Offset != engineOffset ||
		text.Tail.Length != 3 || text.BoundsBits != ([4]uint32{}) {
		t.Fatalf("mixed-order text fields lost their exact spans or IEEE bits: %+v", text)
	}
	if _, err := source.ReadTextLayerView(nil, MetadataOptions{MaxBytes: view.MetadataBytes}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadTextLayerView(nil,
		MetadataOptions{MaxBytes: view.MetadataBytes - 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("mixed text view reservation deficit was ignored: %v", err)
	}
	guard.active = false
	decoded, err := source.DecodeTextEngine(nil, text)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := decoded.Metadata(nil)
	if err != nil || metadata.Text != text.Text+"\r" || metadata.Fonts[0].Name != "FixtureFont-Regular" {
		t.Fatalf("original mixed-text engine metadata differs: %v", err)
	}
	roundTrip, err := psdtext.Encode(nil, decoded)
	if err != nil || !bytes.Equal(roundTrip, engine) {
		t.Fatalf("EngineData raw bytes were changed by text normalization: %v", err)
	}
	text.TextDescriptor.Entries[0].Value = "edited owned metadata"
	again, err := source.ReadTextLayerView(nil)
	if err != nil || again.Records[0].Text != "前😀(后)\\" {
		t.Fatalf("mixed text view aliases original source: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.DecodeTextEngine(nil, text); !errors.Is(err, ErrClosed) {
		t.Fatalf("EngineData read outlived its private source: %v", err)
	}
	if _, err := decoded.Metadata(nil); err != nil {
		t.Fatalf("owned engine metadata was invalidated by Close: %v", err)
	}
}

func TestTIFFPrivateMixedTextRejectsUnknownBoundsAndPreservesCauses(t *testing.T) {
	engine := textFixtureEngine("前😀(后)\\")
	payload, fields := tiffPrivateMixedTextFixture(engine)
	for _, testCase := range []struct {
		change func([]byte)
		kind   error
	}{{func(data []byte) { binary.LittleEndian.PutUint16(data[50:], 50) }, ErrUnsupported},
		{func(data []byte) { binary.BigEndian.PutUint16(data[fields["warpVersion"]:], 1) }, ErrUnsupported},
		{func(data []byte) { binary.LittleEndian.PutUint32(data[fields["bounds"]:], math.Float32bits(1)) }, ErrUnsupported},
		{func(data []byte) { binary.LittleEndian.PutUint64(data[2:], math.Float64bits(math.Inf(1))) }, ErrMalformed}} {
		data := bytes.Clone(payload)
		testCase.change(data)
		fixture := tiffPrivateTestFixtureWithPayload(0, "TySh", tiffPrivateTestChild(), data)
		parent, record := rasterSource(fixture.data)
		defer parent.Close()
		if _, err := parent.OpenLinkedTIFFLayers(nil, record); !errors.Is(err, testCase.kind) {
			t.Fatalf("unknown mixed TySh encoding was accepted: %v", err)
		}
	}
	fixture := tiffPrivateTestFixtureWithPayload(0, "TySh", tiffPrivateTestChild(), payload)
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	view, err := source.ReadTextLayerView(nil)
	if err != nil {
		t.Fatal(err)
	}
	text := view.Records[0]
	failure := errors.New("independent original EngineData reader fault")
	parent.reader = tiffPrivateResourceFault{reader: parent.reader, span: text.EngineData,
		cause: errors.Join(io.EOF, failure)}
	if _, err := source.DecodeTextEngine(nil, text); !errors.Is(err, failure) {
		t.Fatalf("original EngineData source cause was swallowed: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := source.ReadTextLayerView(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("mixed text view ignored cancellation: %v", err)
	}
	if _, err := source.DecodeTextEngine(ctx, text); !errors.Is(err, context.Canceled) {
		t.Fatalf("mixed EngineData ignored cancellation: %v", err)
	}
	if _, err := source.DecodeTextEngine(nil, text,
		psdtext.DecodeOptions{MaxBytes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("original EngineData working budget was ignored: %v", err)
	}
}

func TestTIFFPrivateMixedTextLargeUnknownEngineRemainsLazy(t *testing.T) {
	engine := bytes.Repeat([]byte{0x7f}, 1<<20)
	payload, _ := tiffPrivateMixedTextFixture(engine)
	fixture := tiffPrivateTestFixtureWithPayload(0, "TySh", tiffPrivateTestChild(), payload)
	parent, record := rasterSource(fixture.data)
	defer parent.Close()
	offset := int64(bytes.Index(fixture.data, engine)) + record.Data.Offset
	guard := &tiffPrivateGuardReader{reader: parent.reader, active: true,
		forbidden: []Span{{Offset: offset, Length: int64(len(engine))}, fixture.mediaSpan}}
	parent.reader = guard
	source, err := parent.OpenLinkedTIFFLayers(nil, record)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	view, err := source.ReadTextLayerView(nil)
	if err != nil || len(view.Records) != 1 || view.Records[0].EngineData.Length != int64(len(engine)) {
		t.Fatalf("unparsed EngineData altered mixed descriptor structure: %v", err)
	}
	if guard.bytes > 4096 {
		t.Fatalf("mixed text operation read its large EngineData blob: %d", guard.bytes)
	}
	guard.active = false
	if _, err := source.DecodeTextEngine(nil, view.Records[0]); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown EngineData syntax did not remain an explicit decode boundary: %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadTextLayerView(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("mixed text view outlived its original TIFF: %v", err)
	}
}
