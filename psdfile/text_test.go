package psdfile

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"image"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdtext"
)

func textFixtureLiteral(value string) []byte {
	result := []byte{'(', 0xfe, 0xff}
	for _, unit := range utf16.Encode([]rune(value)) {
		for _, valueByte := range []byte{byte(unit >> 8), byte(unit)} {
			if valueByte == '(' || valueByte == ')' || valueByte == '\\' {
				result = append(result, '\\')
			}
			result = append(result, valueByte)
		}
	}
	return append(result, ')')
}

func textFixtureEngine(text string) []byte {
	data := []byte("\n<< /EngineDict << /Editor << /Text ")
	data = append(data, textFixtureLiteral(text+"\r")...)
	data = append(data, []byte(" >> /StyleRun << /RunArray [ << /StyleSheet << /StyleSheetData ")...)
	data = append(data, []byte("<< /Font 0 /FontSize 24 /Leading 30 >> >> >> ] /RunLengthArray [ ")...)
	units := len(utf16.Encode([]rune(text + "\r")))
	data = append(data, strconv.Itoa(units)...)
	data = append(data, []byte(" ] >> >> /ResourceDict << /FontSet [ << /Name ")...)
	data = append(data, textFixtureLiteral("FixtureFont-Regular")...)
	return append(data, []byte(" /Script 0 /FontType 1 /Synthetic 0 >> ] >> >>\x00")...)
}

func textFixturePayload(engine []byte) ([]byte, map[string]int) {
	text := "前😀(后)\\"
	var data bytes.Buffer
	fields := make(map[string]int)
	indexTestNumber(&data, uint16(1))
	for _, bits := range []uint64{math.Float64bits(1), 1 << 63, 0, math.Float64bits(1),
		math.Float64bits(20.5), math.Float64bits(-5.25)} {
		indexTestNumber(&data, bits)
	}
	indexTestNumber(&data, uint16(50))
	data.Write(linkedFixtureDescriptor(
		descriptorFixtureEntry{key: "Txt ", kind: "TEXT", data: descriptorFixtureUnicode(text)},
		descriptorFixtureEntry{key: "EngineData", kind: "tdta", data: append(
			descriptorFixtureU32(uint32(len(engine))), engine...)},
	))
	fields["warpVersion"] = data.Len()
	indexTestNumber(&data, uint16(1))
	data.Write(linkedFixtureDescriptor(
		descriptorFixtureEntry{key: "warpStyle", kind: "enum", data: append(
			descriptorFixtureID("warpStyle", false), descriptorFixtureID("warpArc", false)...)},
		descriptorFixtureEntry{key: "warpValue", kind: "doub", data: descriptorFixtureU64(math.Float64bits(15))},
	))
	fields["bounds"] = data.Len()
	for _, bits := range []uint32{1 << 31, math.Float32bits(-20.5), math.Float32bits(185.375), math.Float32bits(37.875)} {
		indexTestNumber(&data, bits)
	}
	data.Write([]byte{0xa1, 0xa2})
	return data.Bytes(), fields
}

func textFixtureDocument(version uint16, key string, payload []byte, duplicate bool) []byte {
	tags := indexTestTag(version, "8BIM", key, payload, 2)
	if duplicate {
		tags = append(tags, indexTestTag(version, "8BIM", key, payload, 2)...)
	}
	layers := indexTestLayerSet(version, image.Rect(0, 0, 1, 1), nil, nil, nil, tags)
	return indexTestDocument(version, 8, 3, nil, indexTestGlobal(version, layers, nil), []byte{0, 0, 0})
}

func TestTextLayersAreLazyOrderedAndBytePreserving(t *testing.T) {
	engineBytes := textFixtureEngine("前😀(后)\\")
	payload, _ := textFixturePayload(engineBytes)
	for _, version := range []uint16{1, 2} {
		data := textFixtureDocument(version, "TySh", payload, true)
		engineOffset := bytes.Index(data, engineBytes)
		reader := &indexForbiddenReader{data: data,
			forbidden: []Span{{Offset: int64(engineOffset), Length: int64(len(engineBytes))}}}
		document, err := Read(reader, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = document.Close() })
		reader.forbidden = append(reader.forbidden, document.Composite.Data)
		records, err := document.TextLayers(nil)
		if err != nil || len(records) != 2 {
			t.Fatalf("PSD version %d lazy text indexing: count=%d err=%v", version, len(records), err)
		}
		first := records[0]
		if first.Block.Span.Offset >= records[1].Block.Span.Offset || first.LayerIndex != 0 ||
			first.Text != "前😀(后)\\" || first.Transform != ([6]float64{1, 0, 0, 1, 20.5, -5.25}) ||
			first.TransformBits[1] != 1<<63 || first.Bounds != ([4]float64{0, -20.5, 185.375, 37.875}) ||
			first.BoundsBits[0] != 1<<31 || first.BoundsData.Length != 16 || first.Tail.Length != 2 ||
			!first.HasEngineData || first.EngineData.Offset != int64(engineOffset) {
			t.Fatal("text layer fields, IEEE bits or physical order were lost")
		}
		if textDescriptorValue(first.Warp, "warpValue") != float64(15) {
			t.Fatal("warp descriptor was lost or interpreted as pixel rendering")
		}
		reader.forbidden = nil
		engine, err := document.DecodeTextEngine(nil, first)
		if err != nil || engine.Span.Offset != first.EngineData.Offset {
			t.Fatalf("selected lazy EngineData decode: %v", err)
		}
		metadata, err := engine.Metadata(nil)
		if err != nil || metadata.Text != first.Text+"\r" || metadata.UTF16Units != 8 ||
			metadata.Fonts[0].Name != "FixtureFont-Regular" || *metadata.Styles[0].Style.Leading != 30 {
			t.Fatalf("owned text engine metadata differs: %v", err)
		}
		encoded, err := psdtext.Encode(nil, engine)
		if err != nil || !bytes.Equal(encoded, engineBytes) {
			t.Fatalf("native EngineData bytes were normalized: %v", err)
		}
		first.TextDescriptor.Entries[0].Value = "edited metadata"
		again, err := document.TextLayers(nil)
		if err != nil || again[0].Text != "前😀(后)\\" {
			t.Fatal("owned descriptor edits changed borrowed source bytes")
		}
		if err := document.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := document.TextLayers(nil); !errors.Is(err, ErrClosed) {
			t.Fatal("closed source accepted text indexing")
		}
		if _, err := document.DecodeTextEngine(nil, first); !errors.Is(err, ErrClosed) {
			t.Fatal("closed source accepted lazy EngineData decoding")
		}
		if _, err := engine.Metadata(nil); err != nil {
			t.Fatalf("owned EngineData depended on a closed source: %v", err)
		}
	}
}

func TestTextSourceLimitsMalformedVersionsAndCancellation(t *testing.T) {
	payload, fields := textFixturePayload(textFixtureEngine("前😀(后)\\"))
	for _, test := range []struct {
		key    string
		change func([]byte)
		kind   error
	}{
		{"tySh", func([]byte) {}, ErrUnsupported},
		{"TySh", func(data []byte) { binary.BigEndian.PutUint16(data, 2) }, ErrUnsupported},
		{"TySh", func(data []byte) { binary.BigEndian.PutUint16(data[50:], 51) }, ErrUnsupported},
		{"TySh", func(data []byte) { binary.BigEndian.PutUint16(data[fields["warpVersion"]:], 2) }, ErrUnsupported},
		{"TySh", func(data []byte) { binary.BigEndian.PutUint64(data[2:], math.Float64bits(math.NaN())) }, ErrMalformed},
		{"TySh", func(data []byte) {
			binary.BigEndian.PutUint32(data[fields["bounds"]:], math.Float32bits(float32(math.Inf(1))))
		}, ErrMalformed},
	} {
		changed := bytes.Clone(payload)
		test.change(changed)
		document := linkedFixtureParse(t, textFixtureDocument(1, test.key, changed, false))
		if _, err := document.TextLayers(nil); !errors.Is(err, test.kind) {
			t.Fatalf("text boundary key=%s returned %v", test.key, err)
		}
	}
	for _, length := range []int{0, 51, fields["warpVersion"], fields["bounds"] + 15} {
		document := linkedFixtureParse(t, textFixtureDocument(1, "TySh", payload[:length], false))
		if _, err := document.TextLayers(nil); !errors.Is(err, ErrMalformed) {
			t.Fatalf("truncated TySh length %d was accepted: %v", length, err)
		}
	}
	data := textFixtureDocument(1, "TySh", payload, false)
	reader := &linkedTestReader{reader: bytes.NewReader(data)}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = document.Close() })
	records, err := document.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := document.TextLayers(nil, MetadataOptions{MaxBytes: 1}); !errors.Is(err, ErrLimit) {
		t.Fatal("owned text index ignored metadata budget")
	}
	_, err = document.DecodeTextEngine(nil, records[0], psdtext.DecodeOptions{MaxBytes: 1})
	if !errors.Is(err, ErrLimit) {
		t.Fatal("lazy engine ignored per-call byte budget")
	}
	_, err = document.DecodeTextEngine(nil, records[0], psdtext.DecodeOptions{MaxNodes: 1})
	if !errors.Is(err, ErrLimit) || !errors.Is(err, psdtext.ErrLimit) {
		t.Fatalf("EngineData node limit lost its source or syntax category: %v", err)
	}
	if _, err := document.DecodeEngineData(nil, Span{Offset: -1}); !errors.Is(err, ErrMalformed) {
		t.Fatal("negative lazy source span was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader.reads = 0
	if _, err := document.TextLayers(ctx); !errors.Is(err, context.Canceled) || reader.reads != 0 {
		t.Fatalf("pre-canceled text indexing performed I/O: %v", err)
	}
	if _, err := document.DecodeTextEngine(ctx, records[0]); !errors.Is(err, context.Canceled) || reader.reads != 0 {
		t.Fatalf("pre-canceled engine decoding performed I/O: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	reader.cancel = cancel
	if _, err := document.TextLayers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("read-time cancellation was lost: %v", err)
	}
	reader.cancel, reader.fullEOF = nil, true
	if _, err := document.TextLayers(nil); err != nil {
		t.Fatalf("complete EOF reads rejected in TySh: %v", err)
	}
	if _, err := document.DecodeTextEngine(nil, records[0]); err != nil {
		t.Fatalf("complete EOF reads rejected in EngineData: %v", err)
	}
	reader.fullEOF, reader.zero = false, true
	if _, err := document.DecodeTextEngine(nil, records[0]); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("non-progressing EngineData read accepted: %v", err)
	}
	reader.zero, reader.failure = false, errors.New("fixture text read failed")
	if _, err := document.DecodeTextEngine(nil, records[0]); !errors.Is(err, ErrMalformed) ||
		!errors.Is(err, reader.failure) {
		t.Fatalf("EngineData source failure lost its I/O cause: %v", err)
	}
}

func TestTextLazyUnknownEngineDataRetainsItsOriginalSpan(t *testing.T) {
	unknown := []byte("<< /UnknownOperator exec >>")
	payload, _ := textFixturePayload(unknown)
	document := linkedFixtureParse(t, textFixtureDocument(1, "TySh", payload, false))
	records, err := document.TextLayers(nil)
	if err != nil || len(records) != 1 {
		t.Fatalf("unknown lazy EngineData prevented descriptor indexing: %v", err)
	}
	if _, err := document.DecodeTextEngine(nil, records[0]); !errors.Is(err, ErrUnsupported) ||
		!errors.Is(err, psdtext.ErrUnsupported) {
		t.Fatalf("unknown executable token was silently accepted: %v", err)
	}
	original, err := document.ReadSpan(records[0].EngineData)
	if err != nil || !bytes.Equal(original, unknown) {
		t.Fatalf("unsupported EngineData lost its original byte boundary: %v", err)
	}
}

func TestTextEngineCloseWaitsForSourceRead(t *testing.T) {
	payload, _ := textFixturePayload(textFixtureEngine("前😀(后)\\"))
	data := textFixtureDocument(1, "TySh", payload, false)
	reader := &linkedTestReader{reader: bytes.NewReader(data)}
	document, err := Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	records, err := document.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	reader.started, reader.release = make(chan struct{}), make(chan struct{})
	readDone, closeDone := make(chan error, 1), make(chan error, 1)
	go func() { _, err := document.DecodeTextEngine(nil, records[0]); readDone <- err }()
	<-reader.started
	go func() { closeDone <- document.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned while EngineData was borrowed: %v", err)
	default:
	}
	close(reader.release)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
}

func TestTextNativeFixtures(t *testing.T) {
	if os.Getenv("PSDKIT_TEXT_NATIVE") != "1" {
		t.Skip("set PSDKIT_TEXT_NATIVE=1 after explicitly authorized native fixture generation")
	}
	var manifest struct {
		Cases []struct {
			ID, PSD   string
			Requested struct{ Text, Font string }
		}
		Errors []jsontext.Value
	}
	data, err := os.ReadFile(filepath.Join("..", "verification", "photoshop-text-fixtures.json"))
	if err != nil || json.Unmarshal(data, &manifest, json.MatchCaseInsensitiveNames(true)) != nil || len(manifest.Errors) != 0 || len(manifest.Cases) != 2 {
		t.Fatalf("native text fixture report is missing or incomplete: %v", err)
	}
	for _, fixture := range manifest.Cases {
		t.Run(fixture.ID, func(t *testing.T) {
			document, err := Open(filepath.Join("..", fixture.PSD))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = document.Close() })
			records, err := document.TextLayers(nil)
			if err != nil || len(records) != 1 {
				t.Fatalf("native TySh decode: %d records, %v", len(records), err)
			}
			record := records[0]
			if record.Text != fixture.Requested.Text || record.Transform != ([6]float64{1, 0, 0, 1, 20, 35}) ||
				record.BoundsData.Length != 16 || record.Tail.Length > 3 {
				t.Fatalf("native text/bounds differ: textEqual=%t transform=%v boundsBytes=%d tailBytes=%d",
					record.Text == fixture.Requested.Text, record.Transform, record.BoundsData.Length, record.Tail.Length)
			}
			tail, err := document.ReadSpan(record.Tail)
			if err != nil || !bytes.Equal(tail, make([]byte, len(tail))) {
				t.Fatalf("native internal TySh padding was not zero-filled: %v", err)
			}
			if fixture.ID == "text-english" &&
				record.Bounds != ([4]float64{0, -20.595703125, 185.3946533203125, 37.79296875}) {
				t.Fatalf("native fractional float32 bounds were not preserved: %v", record.Bounds)
			}
			engine, err := document.DecodeTextEngine(nil, record)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := engine.Metadata(nil)
			if err != nil || metadata.Text != record.Text+"\r" || len(metadata.Styles) != 1 ||
				len(metadata.Paragraphs) != 2 {
				t.Fatalf("native requested font/style/paragraph metadata differs: %v", err)
			}
			style := metadata.Styles[0].Style
			if style.Font == nil || *style.Font < 0 || *style.Font >= int64(len(metadata.Fonts)) ||
				metadata.Fonts[*style.Font].Name != fixture.Requested.Font || style.FontSize == nil ||
				*style.FontSize != 24 || style.Leading == nil || *style.Leading != 30 {
				t.Fatal("native style has a missing or different font, size or leading")
			}
			original, err := document.ReadSpan(record.EngineData)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := psdtext.Encode(nil, engine)
			if err != nil || !bytes.Equal(encoded, original) {
				t.Fatalf("native EngineData was not retained byte for byte: %v", err)
			}
		})
	}
}
