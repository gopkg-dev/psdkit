package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func textAutoFont(t *testing.T) (*psdfont.Face, psdfont.FontResolver) {
	t.Helper()
	face, err := psdfont.Open(t.Context(), filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		if name != face.PostScriptName {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
	return face, resolver
}

func textAutoOptions(resolver psdfont.FontResolver, openType bool) TextEditOptions {
	settings := &psdscene.Options{FontResolver: resolver}
	if openType {
		settings.ApproximateOpenTypeText = &psdscene.OpenTypeTextRenderOptions{
			Kerning: psdscene.OpenTypeTextMetrics,
			Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{
				{Tag: "liga", Enabled: true}, {Tag: "kern", Enabled: true},
			}},
		}
	} else {
		settings.ApproximateText = &psdscene.TextRenderOptions{Kerning: new(false)}
	}
	return TextEditOptions{Render: settings}
}

func textAutoEngine(text, font string) []byte {
	units := strconv.Itoa(len(utf16.Encode([]rune(text + "\r"))))
	engine := []byte("<< /EngineDict << /Editor << /Text ")
	engine = append(engine, textEditLiteral(text+"\r")...)
	engine = append(engine, []byte(" >> /StyleRun << /RunArray [ << /StyleSheet << /StyleSheetData "+
		"<< /Font 0 /FontSize 16 /Leading 24 /AutoKerning false /FauxBold false /FauxItalic false "+
		"/AutoLeading false /HorizontalScale 1 /VerticalScale 1 /Tracking 0 /Kerning 0 /BaselineShift 0 "+
		"/FontCaps 0 /FontBaseline 0 /Underline false /Strikethrough false /Ligatures false /DLigatures false "+
		"/NoBreak false /FillFlag true /StrokeFlag false /FillColor << /Type 1 /Values [1 .4 .2 .8] >> "+
		">> >> >> ] /RunLengthArray ["+units+"] >> /ParagraphRun << /RunArray [ "+
		"<< /ParagraphSheet << /Properties << /Justification 0 /FirstLineIndent 0 /StartIndent 0 /EndIndent 0 "+
		"/SpaceBefore 0 /SpaceAfter 0 >> >> >> ] /RunLengthArray ["+units+"] >> "+
		"/Rendered << /Shapes << /WritingDirection 0 /Children [ << /ShapeType 0 /Procession 0 /Cookie "+
		"<< /Photoshop << /ShapeType 0 /PointBase [0 0] /Base << /ShapeType 0 /TransformPoint0 [1 0] "+
		"/TransformPoint1 [0 1] /TransformPoint2 [0 0] >> >> >> >> ] >> >> "+
		"/UnrecognizedEngineProperty <a1b2c3> >> /ResourceDict << /FontSet [ << /Name ")...)
	engine = append(engine, textEditLiteral(font)...)
	return append(engine, []byte(" /Script 0 /FontType 1 /Synthetic 0 >> ] >> >>\x00")...)
}

func textAutoPayload(t *testing.T, text, font string) []byte {
	t.Helper()
	descriptor := &psdfile.Descriptor{Version: 16, ClassID: "TxLr", Entries: []psdfile.DescriptorEntry{
		{Key: "Txt ", Type: "TEXT", Value: text},
		{Key: "unknownTextBlob", Type: "tdta", Value: []byte{0x71, 0x98, 0x23}},
		{Key: "Ornt", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "AntA", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Annt", ValueID: "antiAliasSharp"}},
		{Key: "textGridding", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "textGridding", ValueID: "None"}},
		{Key: "EngineData", Type: "tdta", Value: textAutoEngine(text, font)},
		{Key: "unknownDuplicate", Type: "bool", Value: true},
		{Key: "unknownDuplicate", Type: "bool", Value: false},
	}}
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := binary.BigEndian.AppendUint16(nil, 1)
	for _, bits := range []uint64{math.Float64bits(1), 1 << 63, 0, math.Float64bits(1),
		math.Float64bits(20.25), math.Float64bits(40.5)} {
		payload = binary.BigEndian.AppendUint64(payload, bits)
	}
	payload = binary.BigEndian.AppendUint16(payload, 50)
	payload = append(payload, encoded...)
	warp := &psdfile.Descriptor{Version: 16, ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpNone"}},
	}}
	encoded, err = psdfile.EncodeDescriptor(t.Context(), warp, true)
	if err != nil {
		t.Fatal(err)
	}
	payload = binary.BigEndian.AppendUint16(payload, 1)
	payload = append(payload, encoded...)
	for _, bits := range []uint32{1 << 31, math.Float32bits(-20.5), math.Float32bits(144.25), math.Float32bits(40.75)} {
		payload = binary.BigEndian.AppendUint32(payload, bits)
	}
	return append(payload, 0xa1, 0x99, 0x66)
}

// textAutoFixture carries a tiny stale cache, a larger parent canvas and explicit original resolution.
func textAutoFixture(t *testing.T, version uint16, text, font string, resolution uint32, global, extra []byte) []byte {
	t.Helper()
	tags := append(compositeFixtureTag("TySh", textAutoPayload(t, text, font)), extra...)
	tags = append(tags, compositeFixtureTag("pRsv", []byte{0xa1, 0x99, 0x73})...)
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for index := 0; index < len(pixels.Pix); index += 4 {
		copy(pixels.Pix[index:], []byte{22, 33, 44, 255})
	}
	record, channels := smartEditLayer(version, "Automatic text", 30, pixels.Rect, pixels, tags, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	data := smartEditDocument(version, 96, 64, 3, layers, global,
		append([]byte{0, 0}, make([]byte, 96*64*3)...))
	if resolution == 0 {
		return data
	}
	info := binary.BigEndian.AppendUint32(nil, resolution<<16)
	info = binary.BigEndian.AppendUint16(info, 1)
	info = binary.BigEndian.AppendUint16(info, 1)
	info = binary.BigEndian.AppendUint32(info, resolution<<16)
	info = binary.BigEndian.AppendUint16(info, 1)
	info = binary.BigEndian.AppendUint16(info, 1)
	resource := []byte{'8', 'B', 'I', 'M', 3, 237, 0, 0}
	resource = binary.BigEndian.AppendUint32(resource, uint32(len(info)))
	resource = append(resource, info...)
	withResources := append([]byte{}, data[:30]...)
	withResources = binary.BigEndian.AppendUint32(withResources, uint32(len(resource)))
	withResources = append(withResources, resource...)
	return append(withResources, data[34:]...)
}

func textAutoExpected(t *testing.T, text, font string, options TextEditOptions) *image.NRGBA {
	t.Helper()
	source, err := psdfile.Parse(textAutoFixture(t, 1, text, font, 72, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	record, _, _ := textEditRead(t, source)
	var pixels *image.NRGBA
	if options.Render.ApproximateText != nil {
		pixels, err = psdscene.RenderTextLayer(t.Context(), source, record, options.Render.FontResolver,
			*options.Render.ApproximateText)
	} else {
		pixels, err = psdscene.RenderOpenTypeTextLayer(t.Context(), source, record, options.Render.FontResolver,
			*options.Render.ApproximateOpenTypeText)
	}
	if err != nil {
		t.Fatal(err)
	}
	return pixels
}

func textAutoExpectRaw(t *testing.T, editor *Editor, expected *image.NRGBA) {
	t.Helper()
	node := editor.roots[0]
	if node.pixels == nil || node.bounds != expected.Rect || !bytes.Equal(node.pixels.Pix, expected.Pix) {
		t.Fatal("automatic text artwork differs from independent target-metadata source or parent canvas")
	}
}

func textAutoVisibleEqual(actual, expected *image.NRGBA) bool {
	if actual == nil || expected == nil || actual.Rect != expected.Rect || len(actual.Pix) != len(expected.Pix) {
		return false
	}
	for index := 0; index < len(actual.Pix); index += 4 {
		if actual.Pix[index+3] != expected.Pix[index+3] ||
			expected.Pix[index+3] != 0 && !bytes.Equal(actual.Pix[index:index+3], expected.Pix[index:index+3]) {
			return false
		}
	}
	return true
}

func TestTextAutoEditSaveReopenContinuePSDPSBFourCompressions(t *testing.T) {
	face, resolver := textAutoFont(t)
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, openType := range []bool{false, true} {
				t.Run(fmt.Sprintf("version%d/compression%d/opentype%t", version, compression, openType), func(t *testing.T) {
					data := textAutoFixture(t, version, "AV", face.PostScriptName, 72, nil, nil)
					original := bytes.Clone(data)
					editor := compositeEditor(t, data)
					originalRecord, _, _ := textEditRead(t, editor.source)
					options := textAutoOptions(resolver, openType)
					for round, text := range []string{"ffiAV", "VéA"} {
						expected := textAutoExpected(t, text, face.PostScriptName, options)
						if err := editor.SetTextContent(t.Context(), 1, text, options); err != nil {
							t.Fatal(err)
						}
						textAutoExpectRaw(t, editor, expected)
						before, _ := editor.Changes()
						owned := editor.ownedBytesLocked(nil)
						if err := editor.SetTextContent(t.Context(), 1, text, options); err != nil {
							t.Fatal(err)
						}
						if after, _ := editor.Changes(); after != before || editor.ownedBytesLocked(nil) != owned {
							t.Fatal("repeated automatic edit did not compare the current edited metadata and artwork")
						}
						live, err := editor.Composite(t.Context())
						if err != nil || !textAutoVisibleEqual(live, expected) {
							t.Fatalf("fresh text artwork was not used by composite: %v", err)
						}
						extension := ".psd"
						if version == 2 {
							extension = ".psb"
						}
						path := filepath.Join(t.TempDir(), fmt.Sprintf("round%d%s", round, extension))
						if err := editor.Save(t.Context(), path, SaveOptions{Compression: compression}); err != nil {
							t.Fatal(err)
						}
						saved, err := psdfile.Open(path)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = saved.Close() })
						updated, _, metadata := textEditRead(t, saved)
						if saved.Header.Version != version || updated.Text != text || metadata.Text != text+"\r" ||
							metadata.UTF16Units != int64(len(utf16.Encode([]rune(text+"\r")))) ||
							updated.TransformBits != originalRecord.TransformBits ||
							updated.BoundsBits != originalRecord.BoundsBits ||
							!reflect.DeepEqual(descriptorKeys(updated.TextDescriptor), descriptorKeys(originalRecord.TextDescriptor)) {
							t.Fatal("automatic save lost original geometry, descriptor structure or updated UTF-16 metadata")
						}
						textUnicodeExpectOpaquePreserved(t, editor.source, saved, textAutoSourceRecord(t, editor), updated)
						merged, err := saved.DecodeComposite(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						for position := range expected.Rect.Dx() * expected.Rect.Dy() {
							alpha := uint32(expected.Pix[position*4+3])
							for component := range 3 {
								value := uint32(expected.Pix[position*4+component])
								want := byte((value*alpha + 255*(255-alpha) + 127) / 255)
								if merged.Channels[component].Data[position] != want {
									t.Fatal("saved composite retained stale text or differs from white-matte encoding")
								}
							}
							if len(merged.Channels) == 4 && merged.Channels[3].Data[position] != byte(alpha) {
								t.Fatal("saved composite alpha differs from newly edited glyphs")
							}
						}
						planes, err := saved.DecodeLayer(t.Context(), 0)
						if err != nil || planes.Bounds != expected.Rect {
							t.Fatalf("automatic saved raw artwork bounds differ: %v", err)
						}
						for _, channel := range planes.Channels {
							component := int(channel.ID)
							if channel.ID == -1 {
								component = 3
							}
							for index, value := range channel.Data {
								if value != expected.Pix[index*4+component] {
									t.Fatalf("saved text channel %d sample %d differs", channel.ID, index)
								}
							}
						}
						for _, channel := range saved.Layers[0].Channels {
							if channel.Compression != compression {
								t.Fatal("requested layer compression was not retained")
							}
						}
						editor = lifecycleReopen(t, saved)
						reopened, err := editor.Composite(t.Context())
						if err != nil || !textAutoVisibleEqual(reopened, expected) {
							t.Fatalf("automatic text preview changed on reopen: %v", err)
						}
					}
					if !bytes.Equal(data, original) {
						t.Fatal("automatic editing mutated original source bytes")
					}
				})
			}
		}
	}
}

func textAutoSourceRecord(t *testing.T, editor *Editor) psdfile.TextLayerRecord {
	t.Helper()
	record, _, _ := textEditRead(t, editor.source)
	return record
}
