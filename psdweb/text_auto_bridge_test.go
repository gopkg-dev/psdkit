package psdweb

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/internal/webfixtures"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func bridgeTextAutoLiteral(value string) []byte {
	data := []byte{'(', 0xfe, 0xff}
	for _, unit := range utf16.Encode([]rune(value)) {
		for _, component := range []byte{byte(unit >> 8), byte(unit)} {
			if component == '(' || component == ')' || component == '\\' {
				data = append(data, '\\')
			}
			data = append(data, component)
		}
	}
	return append(data, ')')
}

// bridgeTextAutoFixture encodes one ordinary point-text layer independently of the editing writer.
func bridgeTextAutoFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	engine := append([]byte("<< /EngineDict << /Editor << /Text "), bridgeTextAutoLiteral("AV\r")...)
	engine = append(engine, []byte(" >> /StyleRun << /RunArray [ << /StyleSheet << /StyleSheetData << "+
		"/Font 0 /FontSize 24 /Leading 30 /AutoKerning false /AutoLeading false "+
		"/FauxBold false /FauxItalic false /HorizontalScale 1 /VerticalScale 1 /Tracking 0 "+
		"/Kerning 0 /BaselineShift 0 /FontCaps 0 /FontBaseline 0 /Underline false /Strikethrough false "+
		"/Ligatures false /DLigatures false /NoBreak false /FillFlag true /StrokeFlag false "+
		"/FillColor << /Type 1 /Values [1 .4 .2 .8] >> >> >> >> ] /RunLengthArray [3] >> "+
		"/ParagraphRun << /RunArray [ << /ParagraphSheet << /Properties << "+
		"/Justification 0 /FirstLineIndent 0 /StartIndent 0 /EndIndent 0 /SpaceBefore 0 /SpaceAfter 0 "+
		">> >> >> ] /RunLengthArray [3] >> /Rendered << /Shapes << /WritingDirection 0 /Children [ "+
		"<< /ShapeType 0 /Procession 0 /Cookie << /Photoshop << /ShapeType 0 /PointBase [0 0] "+
		"/Base << /ShapeType 0 /TransformPoint0 [1 0] /TransformPoint1 [0 1] /TransformPoint2 [0 0] >> "+
		">> >> >> ] >> >> /opaqueEngine <a1b2> >> /ResourceDict << /FontSet [ << /Name ")...)
	engine = append(engine, bridgeTextAutoLiteral("BridgeFixture-Regular")...)
	engine = append(engine, []byte(" /Script 0 /FontType 1 /Synthetic 0 >> ] >> >>\x00")...)
	descriptor := &psdfile.Descriptor{ClassID: "TxLr", Entries: []psdfile.DescriptorEntry{
		{Key: "Txt ", Type: "TEXT", Value: "AV"}, {Key: "EngineData", Type: "tdta", Value: engine},
		{Key: "Ornt", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "textGridding", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "textGridding", ValueID: "None"}},
		{Key: "AntA", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Annt", ValueID: "antiAliasSharp"}},
		{Key: "opaqueText", Type: "tdta", Value: []byte{0x91, 0x47}},
	}}
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := binary.BigEndian.AppendUint16(nil, 1)
	for _, value := range []float64{1, 0, 0, 1, 12, 44} {
		payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(value))
	}
	payload = binary.BigEndian.AppendUint16(payload, 50)
	payload = append(payload, encoded...)
	warp, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "warp",
		Entries: []psdfile.DescriptorEntry{{Key: "warpStyle", Type: "enum",
			Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpNone"}}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	payload = binary.BigEndian.AppendUint16(payload, 1)
	payload = append(payload, warp...)
	payload = append(payload, make([]byte, 16)...)
	payload = append(payload, 0xa1, 0x99, 0x66)
	appendLength := func(data []byte, length int) []byte {
		if version == 2 {
			return binary.BigEndian.AppendUint64(data, uint64(length))
		}
		return binary.BigEndian.AppendUint32(data, uint32(length))
	}
	var record, channels []byte
	for _, coordinate := range []uint32{0, 0, 96, 128} {
		record = binary.BigEndian.AppendUint32(record, coordinate)
	}
	record = binary.BigEndian.AppendUint16(record, 4)
	for index, id := range []int16{0, 1, 2, -1} {
		record = binary.BigEndian.AppendUint16(record, uint16(id))
		record = appendLength(record, 2+128*96)
		channels = append(channels, 0, 0)
		channels = append(channels, bytes.Repeat([]byte{[]byte{200, 30, 220, 255}[index]}, 128*96)...)
	}
	record = append(record, []byte("8BIMnorm\x80\x00\x00\x00")...)
	extra := binary.BigEndian.AppendUint32(nil, 20)
	for _, coordinate := range []uint32{0, 0, 96, 128} {
		extra = binary.BigEndian.AppendUint32(extra, coordinate)
	}
	extra = append(extra, 128, 0, 0x71, 0x98)
	extra = binary.BigEndian.AppendUint32(extra, 0)
	extra = append(extra, 1, 'T', 0, 0)
	extra = append(extra, bridgeSmartTag("TySh", payload, false, 2)...)
	extra = append(extra, bridgeSmartTag("pRsv", []byte{0x97, 0x41, 0x23}, false, 2)...)
	record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
	record = append(record, extra...)
	layers := append(binary.BigEndian.AppendUint16(nil, 0xffff), record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	section := append(appendLength(nil, len(layers)), layers...)
	section = binary.BigEndian.AppendUint32(section, 0)
	resolution := binary.BigEndian.AppendUint32(nil, 72<<16)
	resolution = append(resolution, 0, 1, 0, 1)
	resolution = binary.BigEndian.AppendUint32(resolution, 72<<16)
	resolution = append(resolution, 0, 1, 0, 1)
	resource := binary.BigEndian.AppendUint32([]byte{'8', 'B', 'I', 'M', 3, 237, 0, 0}, 16)
	resource = append(resource, resolution...)
	header := binary.BigEndian.AppendUint16([]byte("8BPS"), version)
	header = append(header, make([]byte, 6)...)
	header = binary.BigEndian.AppendUint16(header, 4)
	header = binary.BigEndian.AppendUint32(header, 96)
	header = binary.BigEndian.AppendUint32(header, 128)
	header = binary.BigEndian.AppendUint16(header, 8)
	header = binary.BigEndian.AppendUint16(header, 3)
	header = binary.BigEndian.AppendUint32(header, 0)
	header = binary.BigEndian.AppendUint32(header, uint32(len(resource)))
	header = append(header, resource...)
	header = appendLength(header, len(section))
	header = append(header, section...)
	header = append(header, 0, 0)
	return append(header, bytes.Repeat([]byte{23}, 4*128*96)...)
}

func bridgeTextAutoPolicy(openType bool) map[string]any {
	if openType {
		return map[string]any{"Render": map[string]any{"ApproximateOpenTypeText": map[string]any{
			"Kerning": 1, "Samples": 4, "ApproximateTypography": true}}}
	}
	return map[string]any{"Render": map[string]any{"ApproximateText": map[string]any{"Samples": 4, "Kerning": false}}}
}

func verifyBridgeTextAutoSaved(t *testing.T, data []byte, desired string, version uint16, openType bool) {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(t.Context())
	if err != nil || len(records) != 1 || records[0].Text != desired || source.Header.Version != version {
		t.Fatalf("automatic browser text changed format or did not persist new content: %v", err)
	}
	face, err := psdfont.Decode(t.Context(), webfixtures.Font())
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		if name != "BridgeFixture-Regular" {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
	var expected *image.NRGBA
	if openType {
		expected, err = psdscene.RenderOpenTypeTextLayer(t.Context(), source, records[0], resolver,
			psdscene.OpenTypeTextRenderOptions{Kerning: psdscene.OpenTypeTextNone, Samples: 4, ApproximateTypography: true})
	} else {
		expected, err = psdscene.RenderTextLayer(t.Context(), source, records[0], resolver,
			psdscene.TextRenderOptions{Samples: 4, Kerning: new(false)})
	}
	if err != nil {
		t.Fatalf("saved text no longer supports its selected explicit renderer: %v", err)
	}
	if expected.Rect != image.Rect(0, 0, 128, 96) || source.Layers[0].Bounds != expected.Rect {
		t.Fatal("automatic text used a cropped or one-pixel candidate canvas")
	}
	artwork, err := source.DecodeLayer(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, plane := range artwork.Channels {
		component := int(plane.ID)
		if component == -1 {
			component = 3
		}
		if component < 0 || component > 3 {
			continue
		}
		for index, value := range plane.Data {
			if value != expected.Pix[index*4+component] {
				t.Fatalf("text cache channel %d is stale or baked parent styling", plane.ID)
			}
		}
	}
	merged, err := source.DecodeComposite(t.Context())
	if err != nil || len(merged.Channels) != 4 {
		t.Fatalf("saved automatic text composite is missing: %v", err)
	}
	nonzero := false
	for index, actual := range merged.Channels[3].Data {
		rawAlpha := expected.Pix[index*4+3]
		nonzero = nonzero || rawAlpha != 0
		wanted := math.Round(float64(rawAlpha) * 128 * 128 / (255 * 255))
		if math.Abs(float64(actual)-wanted) > 1 {
			t.Fatal("text save applied parent opacity/default mask incorrectly or twice")
		}
	}
	if nonzero != (desired != "") {
		t.Fatal("automatic text did not rebuild visible glyphs or clear empty content")
	}
	rawMask, err := source.ReadSpan(source.Layers[0].Mask.Span)
	if err != nil || rawMask[16] != 128 || !bytes.Equal(rawMask[len(rawMask)-2:], []byte{0x71, 0x98}) {
		t.Fatal("automatic text changed retained parent mask metadata")
	}
}

func TestTextAutoBridgeExplicitFontEditSaveReopenAndContinue(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, openType := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/openType%t", version, compression, openType), func(t *testing.T) {
					engine, err := New(Config{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
					original := bridgeTextAutoFixture(t, version)
					asset := bridgeAsset(t, engine, "point-text.psd", original)
					opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
					editor := opened["editor"].(string)
					policy := bridgeTextAutoPolicy(openType)
					before := blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
					response := bridgeContentsFilterRequest(t, engine, "SetTextContent", editor, 1, "VA", policy)
					if response.OK || response.Error == nil || len(response.Binaries) != 0 {
						t.Fatal("automatic text accepted no explicitly imported font")
					}
					if after := blendingBridgeResult[psdedit.Changes](t,
						bridgeCall(t, engine, "editor.Changes", editor)); after != before {
						t.Fatal("missing font committed partial text parameters or pixels")
					}
					fontAsset := bridgeAsset(t, engine, "fixture.ttf", webfixtures.Font())
					decoded := bridgeCall(t, engine, "font.decode", "", assetRef(fontAsset), map[string]any{})
					fontHandle := decoded.Data.(map[string]any)["results"].([]any)[0].(map[string]any)["handle"].(string)
					for _, noPolicy := range []map[string]any{{}, {"Render": map[string]any{}}} {
						response := bridgeContentsFilterRequest(t, engine, "SetTextContent", editor, 1, "VA", noPolicy)
						if response.OK || response.Error == nil || response.Error.Code != "unsupported" {
							t.Fatalf("text edit implicitly selected approximate glyph rendering: %+v", response.Error)
						}
					}
					for _, step := range []struct{ input, normalized string }{
						{"VAAV", "VAAV"}, {"AV\nVA", "AV\rVA"}, {"", ""},
					} {
						bridgeCall(t, engine, "editor.SetTextContent", editor, 1, step.input, policy)
						// The writer must receive owned generated pixels without any caller-provided raster or composite.
						saved := bridgeCall(t, engine, "editor.WritePSD", editor,
							map[string]any{"Compression": compression})
						if len(saved.Binaries) != 1 {
							t.Fatal("automatic text edit did not produce PSD/PSB bytes")
						}
						verifyBridgeTextAutoSaved(t, saved.Binaries[0].Data, step.normalized, version, openType)
						output := bridgeAsset(t, engine, "point-text-edited.psd", saved.Binaries[0].Data)
						opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
						editor = opened["editor"].(string)
					}
					bridgeCall(t, engine, "resource.release", fontHandle)
					before = blendingBridgeResult[psdedit.Changes](t, bridgeCall(t, engine, "editor.Changes", editor))
					response = bridgeContentsFilterRequest(t, engine, "SetTextContent", editor, 1, "AV", policy)
					if response.OK || response.Error == nil || len(response.Binaries) != 0 {
						t.Fatal("text editing retained a released font or used a system substitute")
					}
					if after := blendingBridgeResult[psdedit.Changes](t,
						bridgeCall(t, engine, "editor.Changes", editor)); after != before {
						t.Fatal("released font committed a partial text edit")
					}
					if !bytes.Equal(original, engine.assets[asset].data) {
						t.Fatal("automatic text editing mutated imported document bytes")
					}
				})
			}
		}
	}
}
