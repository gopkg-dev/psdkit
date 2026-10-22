package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func openTypeTestLiteral(value string) []byte {
	result := []byte{'(', 0xfe, 0xff}
	for _, unit := range utf16.Encode([]rune(value)) {
		for _, b := range []byte{byte(unit >> 8), byte(unit)} {
			if b == '(' || b == ')' || b == '\\' {
				result = append(result, '\\')
			}
			result = append(result, b)
		}
	}
	return append(result, ')')
}

func openTypeTestEngine(text, font string, size float64) []byte {
	units := len(utf16.Encode([]rune(text + "\r")))
	var b bytes.Buffer
	b.WriteString("<< /EngineDict << /Editor << /Text ")
	b.Write(openTypeTestLiteral(text + "\r"))
	b.WriteString(" >> ")
	fmt.Fprintf(&b, "/StyleRun << /RunArray [ << /StyleSheet << /StyleSheetData "+
		"<< /Font 0 /FontSize %g /Leading 30 /AutoKerning true ", size)
	b.WriteString("/FauxBold false /FauxItalic false /AutoLeading false /HorizontalScale 1 /VerticalScale 1 /Tracking 0 " +
		"/Kerning 0 /BaselineShift 0 /FontCaps 0 /FontBaseline 0 /Underline false /Strikethrough false " +
		"/Ligatures true /DLigatures false /NoBreak false /FillFlag true /StrokeFlag false " +
		"/FillColor << /Type 1 /Values [1 .4 .2 .8] >> >> >> >> ] ")
	fmt.Fprintf(&b, "/RunLengthArray [%d] >> /ParagraphRun << /RunArray [ << /ParagraphSheet << /Properties ", units)
	b.WriteString("<< /Justification 0 /FirstLineIndent 0 /StartIndent 0 /EndIndent 0 " +
		"/SpaceBefore 0 /SpaceAfter 0 >> >> >> ] ")
	fmt.Fprintf(&b, "/RunLengthArray [%d] >> /Rendered << /Shapes << /WritingDirection 0 /Children [ ", units)
	b.WriteString("<< /ShapeType 0 /Procession 0 /Cookie << /Photoshop << /ShapeType 0 /PointBase [0 0] " +
		"/Base << /ShapeType 0 /TransformPoint0 [1 0] /TransformPoint1 [0 1] /TransformPoint2 [0 0] >> >> >> >> " +
		"] >> >> >> /ResourceDict << /FontSet [ << /Name ")
	b.Write(openTypeTestLiteral(font))
	b.WriteString(" /Script 0 /FontType 1 /Synthetic 0 >> ] >> >>\x00")
	return b.Bytes()
}

func openTypeTestPSD(t *testing.T, text, font string, size float64, modern bool) []byte {
	return openTypeTestPSDExtra(t, text, font, size, modern, "")
}

func openTypeTestPSDExtra(t *testing.T, text, font string, size float64, modern bool, extraText string,
	engines ...[]byte,
) []byte {
	t.Helper()
	engine := openTypeTestEngine(text, font, size)
	if len(engines) == 1 {
		engine = engines[0]
	}
	desc := &psdfile.Descriptor{Version: 16, ClassID: "TxLr", Entries: []psdfile.DescriptorEntry{
		{Key: "Txt ", Type: "TEXT", Value: text}, {Key: "EngineData", Type: "tdta", Value: engine},
		{Key: "TextIndex", Type: "long", Value: int32(0)},
		{Key: "Ornt", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "textGridding", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "textGridding", ValueID: "None"}},
		{Key: "AntA", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Annt", ValueID: "antiAliasSharp"}},
	}}
	d, err := psdfile.EncodeDescriptor(nil, desc, true)
	if err != nil {
		t.Fatal(err)
	}
	warp := &psdfile.Descriptor{Version: 16, ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpNone"}},
	}}
	w, err := psdfile.EncodeDescriptor(nil, warp, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := binary.BigEndian.AppendUint16(nil, 1)
	for _, v := range []float64{1, 0, 0, 1, 20, 50} {
		payload = binary.BigEndian.AppendUint64(payload, math.Float64bits(v))
	}
	payload = binary.BigEndian.AppendUint16(payload, 50)
	payload = append(payload, d...)
	payload = binary.BigEndian.AppendUint16(payload, 1)
	payload = append(payload, w...)
	payload = append(payload, make([]byte, 16)...)
	tag := append([]byte("8BIMTySh"), binary.BigEndian.AppendUint32(nil, uint32(len(payload)))...)
	tag = append(tag, payload...)
	if len(payload)%2 != 0 {
		tag = append(tag, 0)
	}
	extra := append(make([]byte, 8), []byte{5, 'p', 'o', 'i', 'n', 't', 0, 0}...)
	extra = append(extra, tag...)
	layer := binary.BigEndian.AppendUint16(nil, 1)
	for _, n := range []uint32{0, 0, 96, 128} {
		layer = binary.BigEndian.AppendUint32(layer, n)
	}
	layer = binary.BigEndian.AppendUint16(layer, 4)
	for _, id := range []int16{0, 1, 2, -1} {
		layer = binary.BigEndian.AppendUint16(layer, uint16(id))
		layer = binary.BigEndian.AppendUint32(layer, 2+128*96)
	}
	layer = append(layer, []byte("8BIMnorm")...)
	layer = append(layer, 255, 0, 0, 0)
	layer = binary.BigEndian.AppendUint32(layer, uint32(len(extra)))
	layer = append(layer, extra...)
	for _, v := range []byte{200, 30, 220, 255} {
		layer = append(layer, 0, 0)
		layer = append(layer, bytes.Repeat([]byte{v}, 128*96)...)
	}
	layerMask := binary.BigEndian.AppendUint32(nil, uint32(len(layer)))
	layerMask = append(layerMask, layer...)
	layerMask = binary.BigEndian.AppendUint32(layerMask, 0)
	if modern {
		var m bytes.Buffer
		m.WriteString("/98 << /0 7 >> /0 << /opaqueFontData <9918> >> /1 << /1 [ << /0 << /0 ")
		m.Write(openTypeTestLiteral(text + "\r"))
		units := len(utf16.Encode([]rune(text + "\r")))
		fmt.Fprintf(&m, " /5 << /0 [ << /0 << /opaqueParagraph 0 >> /1 %d >> ] >> ", units)
		m.WriteString("/6 << /0 [ ")
		if len(engines) == 1 {
			decoded, err := psdtext.Decode(nil, engine)
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := decoded.Metadata(nil)
			if err != nil {
				t.Fatal(err)
			}
			for index, style := range metadata.Styles {
				fmt.Fprintf(&m, "<< /0 << /opaqueStyle %d >> /1 %d >> ", index, style.Length)
			}
		} else {
			fmt.Fprintf(&m, "<< /0 << /opaqueStyle 0 >> /1 %d >> ", units)
		}
		m.WriteString("] >> >> ")
		m.WriteString("/1 << /0 [ << /0 0 >> ] /2 [ << /savedGlyphData <aabbcc> >> ] >> >> ")
		if extraText != "" {
			extraUnits := len(utf16.Encode([]rune(extraText + "\r")))
			m.WriteString("<< /0 << /0 ")
			m.Write(openTypeTestLiteral(extraText + "\r"))
			fmt.Fprintf(&m, " /5 << /0 [ << /0 << >> /1 %d >> ] >> ", extraUnits)
			fmt.Fprintf(&m, "/6 << /0 [ << /0 << >> /1 %d >> ] >> >> ", extraUnits)
			m.WriteString("/1 << /0 [] /2 [] >> >> ")
		}
		m.WriteString("] >>\x00")
		p := m.Bytes()
		tag := append([]byte("8BIMTxt2"), binary.BigEndian.AppendUint32(nil, uint32(len(p)))...)
		tag = append(tag, p...)
		tag = append(tag, make([]byte, (4-len(p)%4)%4)...)
		layerMask = append(layerMask, tag...)
	}
	resolution := binary.BigEndian.AppendUint32(nil, 72<<16)
	resolution = binary.BigEndian.AppendUint16(resolution, 1)
	resolution = binary.BigEndian.AppendUint16(resolution, 1)
	resolution = binary.BigEndian.AppendUint32(resolution, 72<<16)
	resolution = binary.BigEndian.AppendUint16(resolution, 1)
	resolution = binary.BigEndian.AppendUint16(resolution, 1)
	resource := []byte{'8', 'B', 'I', 'M', 3, 237, 0, 0}
	resource = binary.BigEndian.AppendUint32(resource, 16)
	resource = append(resource, resolution...)
	data := []byte{'8', 'B', 'P', 'S', 0, 1, 0, 0, 0, 0, 0, 0, 0, 3}
	data = binary.BigEndian.AppendUint32(data, 96)
	data = binary.BigEndian.AppendUint32(data, 128)
	data = binary.BigEndian.AppendUint16(data, 8)
	data = binary.BigEndian.AppendUint16(data, 3)
	data = binary.BigEndian.AppendUint32(data, 0)
	data = binary.BigEndian.AppendUint32(data, uint32(len(resource)))
	data = append(data, resource...)
	data = binary.BigEndian.AppendUint32(data, uint32(len(layerMask)))
	data = append(data, layerMask...)
	data = append(data, 0, 0)
	data = append(data, bytes.Repeat([]byte{123}, 3*128*96)...)
	return data
}

type textSourceOracle struct {
	Fonts []struct {
		File string `json:"file"`
	} `json:"fonts"`
	Cases []struct {
		ID, Font, Text, Script, Language string
		Features                         []psdfont.ShapeFeature `json:"features"`
		ExpectedError                    string                 `json:"expected_error"`
		Config                           struct {
			ID   string
			Size float64
		} `json:"config"`
		Glyphs []struct {
			Path []struct {
				Op   string
				X, Y float64
				CX   float64 `json:"control_x"`
				CY   float64 `json:"control_y"`
			} `json:"canvas_path"`
		} `json:"glyphs"`
	} `json:"cases"`
}

func TestOpenTypeTextSourceIndependentLayoutNeverReadsSavedPixels(t *testing.T) {
	for _, name := range []string{"layout_oracle.json", "layout_geometry_oracle.json"} {
		path := testfixture.Path(t, filepath.Join("psdfont", "testdata", "opentype", name))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var oracle textSourceOracle
		if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
			t.Fatal(err)
		}
		faces := make(map[string]*psdfont.Face)
		for _, f := range oracle.Fonts {
			face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", f.File))
			if err != nil {
				t.Fatal(err)
			}
			faces[f.File] = face
		}
		for _, row := range oracle.Cases {
			if row.Config.ID != "13.75-zero" {
				continue
			}
			t.Run(name+"/"+row.ID, func(t *testing.T) {
				face := faces[row.Font]
				if face == nil {
					t.Fatal("unknown fixture font")
				}
				fontPath := filepath.Join("..", "psdfont", "testdata", "opentype", row.Font)
				fontBefore, err := face.ReadSpan(nil, psdfont.Span{Length: int64(lenFile(t, fontPath))})
				if err != nil {
					t.Fatal(err)
				}
				fixture := openTypeTestPSD(t, row.Text, face.PostScriptName, row.Config.Size, true)
				before := sha256.Sum256(fixture)
				reader := &textRasterForbiddenReader{reader: bytes.NewReader(fixture)}
				source, err := psdfile.Read(reader, int64(len(fixture)))
				if err != nil {
					t.Fatal(err)
				}
				defer source.Close()
				reader.forbidden = append(reader.forbidden, source.Composite.Data)
				for _, l := range source.Layers {
					for _, c := range l.Channels {
						reader.forbidden = append(reader.forbidden, c.Data)
					}
				}
				records, err := source.TextLayers(nil)
				if err != nil || len(records) != 1 {
					t.Fatalf("fixture metadata %v", err)
				}
				resolver := psdfont.ResolverFunc(func(_ context.Context, n string) (*psdfont.Face, error) {
					if n != face.PostScriptName {
						return nil, psdfont.ErrMissingFont
					}
					return face, nil
				})
				mode := OpenTypeTextNone
				for _, f := range row.Features {
					if f.Tag == "kern" && f.Enabled {
						mode = OpenTypeTextMetrics
					}
				}
				actual, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, OpenTypeTextRenderOptions{Kerning: mode,
					Shape: psdfont.ShapeOptions{Script: row.Script, Language: row.Language, Features: row.Features}})
				if row.ExpectedError != "" {
					kinds := map[string]error{"unsupported": psdfont.ErrUnsupported, "missing_glyph": psdfont.ErrMissingGlyph}
					kind := kinds[row.ExpectedError]
					if actual != nil || !errors.Is(err, kind) {
						t.Fatalf("actual=%v err=%v expected=%v", actual, err, kind)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				expected := &psdfont.Run{Scale: 1, Approximate: true}
				ops := map[string]psdfont.PathOp{"Move": psdfont.Move, "Line": psdfont.Line,
					"Quad": psdfont.Quad, "Close": psdfont.Close}
				for _, g := range row.Glyphs {
					glyph := &psdfont.Glyph{}
					for _, p := range g.Path {
						c := psdfont.Command{Op: ops[p.Op]}
						if c.Op != psdfont.Close {
							c.X, c.Y = p.X+20, -(p.Y + 50)
						}
						if c.Op == psdfont.Quad {
							c.ControlX, c.ControlY = p.CX+20, -(p.CY + 50)
						}
						glyph.Path = append(glyph.Path, c)
					}
					expected.Glyphs = append(expected.Glyphs, psdfont.PositionedGlyph{Glyph: glyph})
				}
				coverage, err := psdfont.RasterizeRun(nil, expected, image.Rect(0, 0, 128, 96))
				if err != nil {
					t.Fatal(err)
				}
				for i, a := range coverage.Pix {
					at := i * 4
					if actual.Pix[at] != 102 || actual.Pix[at+1] != 51 || actual.Pix[at+2] != 204 || actual.Pix[at+3] != a {
						t.Fatalf("independent FT/HB source pixel %d actual=%v expectedalpha=%d", i, actual.Pix[at:at+4], a)
					}
				}
				if sha256.Sum256(fixture) != before {
					t.Fatal("source bytes mutated")
				}
				after, err := face.ReadSpan(nil, psdfont.Span{Length: int64(len(fontBefore))})
				if err != nil || !bytes.Equal(after, fontBefore) {
					t.Fatal("owned font changed")
				}
			})
		}
	}
}

func lenFile(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return len(b)
}

func TestOpenTypeTextSourcePolicyBudgetOwnership(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	fixture := openTypeTestPSD(t, "ffi AV", face.PostScriptName, 13.75, true)
	source, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	record := records[0]
	calls := 0
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { calls++; return face, nil })
	settings := OpenTypeTextRenderOptions{Kerning: OpenTypeTextMetrics,
		Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}}}}
	for _, mode := range []OpenTypeTextKerning{0, 3, 255} {
		s := settings
		s.Kerning = mode
		p, err := RenderOpenTypeTextLayer(nil, source, record, resolver, s)
		if p != nil || !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatal("Optical/unset/unknown kerning accepted")
		}
	}
	for _, b := range []int64{1, 8192, 60000} {
		s := settings
		s.MaxWorkingBytes = b
		p, err := RenderOpenTypeTextLayer(nil, source, record, resolver, s)
		if p != nil || !errors.Is(err, psdfile.ErrLimit) {
			t.Fatal("frame allocation budget accepted")
		}
	}
	if calls != 0 {
		t.Fatal("font decoded before output budget preflight")
	}
	base, err := RenderOpenTypeTextLayer(nil, source, record, resolver, settings)
	if err != nil {
		t.Fatal(err)
	}
	bounds := image.Rect(0, 0, 128, 96)
	settings.Bounds = &bounds
	mutator := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		settings.Shape.Features[0].Enabled = false
		bounds = image.Rect(0, 0, 1, 1)
		return face, nil
	})
	owned, err := RenderOpenTypeTextLayer(nil, source, record, mutator, settings)
	if err != nil || !bytes.Equal(owned.Pix, base.Pix) || owned.Rect != base.Rect {
		t.Fatal("caller options alias changed in-flight source render", err)
	}
	p, err := RenderOpenTypeTextLayer(nil, source, record, nil, settings)
	if p != nil || !errors.Is(err, psdfont.ErrMissingFont) {
		t.Fatal("missing font fell back to saved pixels")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p, err = RenderOpenTypeTextLayer(ctx, source, record, resolver, settings)
	if p != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("canceled render accepted")
	}
	retained := sha256.Sum256(base.Pix)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(base.Pix) != retained {
		t.Fatal("pixels depended on open source")
	}
	p, err = RenderOpenTypeTextLayer(nil, source, record, resolver, settings)
	if p != nil || !errors.Is(err, psdfile.ErrClosed) {
		t.Fatal("closed source accepted")
	}
}

func TestOpenTypeTextSourceRejectsMultiline(t *testing.T) {
	fixture := openTypeTestPSD(t, "A\rB", "font", 13.75, true)
	s, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r, err := s.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := RenderOpenTypeTextLayer(nil, s, r[0], nil, OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone})
	if p != nil || !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatal("single-line API accepted multiline")
	}
	if !strings.Contains(r[0].Text, "\r") {
		t.Fatal("fixture lost carriage return")
	}
}

func TestOpenTypeTextSourceDoesNotModifyBorrowedMetadata(t *testing.T) {
	fixture := openTypeTestPSD(t, "ffi AV", "font", 13.75, true)
	source, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := source.DecodeTextEngine(nil, records[0])
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	style, _, _, err := openTypeTextStyle(metadata, records[0].Text, 1<<20)
	if err != nil || style.Kerning {
		t.Fatal("explicit OpenType style was not isolated", err)
	}
	after, err := psdtext.Encode(nil, engine)
	if err != nil || !bytes.Equal(before, after) || !metadata.Styles[0].Style.Data.Lookup("Ligatures").Bool {
		t.Fatal("source legacy feature projection was mutated")
	}
	view, err := source.ReadDocumentTextView(nil)
	if err != nil {
		t.Fatal(err)
	}
	docEngine, err := source.DecodeDocumentTextEngine(nil, view.Records[0])
	if err != nil {
		t.Fatal(err)
	}
	docMetadata, err := docEngine.DocumentMetadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err = psdtext.Encode(nil, docEngine)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.ReadDocumentTextAssociations(nil, docMetadata); err != nil {
		t.Fatal(err)
	}
	if err := openTypeTextAssociation(context.Background(), source, records[0], 4<<20); err != nil {
		t.Fatal(err)
	}
	after, err = psdtext.Encode(nil, docEngine)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("modern association data was changed")
	}
}

type textSourceCountContext struct {
	calls    atomic.Int64
	cancelAt int64
}

func (*textSourceCountContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*textSourceCountContext) Done() <-chan struct{}       { return nil }
func (*textSourceCountContext) Value(any) any               { return nil }
func (c *textSourceCountContext) Err() error {
	n := c.calls.Add(1)
	if c.cancelAt != 0 && n >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestOpenTypeTextSourceTargetGlyphLimitAndLateCancel(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	settings := OpenTypeTextRenderOptions{MaxGlyphs: 1, Kerning: OpenTypeTextNone}
	for _, extra := range []string{"", "Other longer text has many glyphs"} {
		fixture := openTypeTestPSDExtra(t, "A", face.PostScriptName, 13.75, true, extra)
		source, err := psdfile.Parse(fixture)
		if err != nil {
			t.Fatal(err)
		}
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		count := &textSourceCountContext{}
		result, err := RenderOpenTypeTextLayer(count, source, records[0], resolver, settings)
		if err != nil || result == nil {
			t.Fatalf("target one glyph rejected by global metadata: %v", err)
		}
		late := &textSourceCountContext{cancelAt: count.calls.Load()}
		partial, err := RenderOpenTypeTextLayer(late, source, records[0], resolver, settings)
		if partial != nil || !errors.Is(err, context.Canceled) {
			t.Fatal("late cancellation returned partial source")
		}
		if err := source.Close(); err != nil {
			t.Fatal(err)
		}
	}
	fixture := openTypeTestPSD(t, "AA", face.PostScriptName, 13.75, true)
	source, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, settings)
	if result != nil || !errors.Is(err, psdfont.ErrLimit) {
		t.Fatal("target input-glyph limit was not enforced")
	}
}

func TestOpenTypeTextSourceTerminalCRAndParagraphCoverage(t *testing.T) {
	fixture := openTypeTestPSD(t, "A", "font", 13.75, true)
	source, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := source.DecodeTextEngine(nil, records[0])
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.UTF16Units != 2 || metadata.Styles[0].Length != 2 || metadata.Paragraphs[0].Length != 2 {
		t.Fatal("fixture terminal CR is not covered independently of one input glyph")
	}
	metadata.Paragraphs[0].Length = 1
	if _, _, _, err := openTypeTextStyle(metadata, "A", 1<<20); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatal("truncated terminal paragraph coverage accepted")
	}
	metadata.Paragraphs[0].Length = 2
	metadata.Paragraphs[0].Start = 1
	if _, _, _, err := openTypeTextStyle(metadata, "A", 1<<20); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatal("shifted paragraph coverage accepted")
	}
	metadata.Paragraphs[0].Start = 0
	metadata.Styles[0].Length = 1
	if _, _, _, err := openTypeTextStyle(metadata, "A", 1<<20); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatal("style range omitting terminal CR accepted")
	}
}

func TestOpenTypeTextSourcePhysicalIdentityAndDescriptorOwnership(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{"transform", "transformBits", "layerIndex", "setIndex", "setKey", "blockOffset",
		"blockSpan", "blockPadding", "engineOffset", "hasEngine", "prefix", "textDescriptorData", "warpVersionData",
		"warpDescriptorData", "boundsData", "tail", "text", "textIndex", "hasTextIndex", "bounds", "boundsBits",
		"warpDescriptor", "descriptorClass", "descriptorSpan", "orientationType", "antialiasType", "griddingType",
		"callerCycle"}
	for _, modern := range []bool{false, true} {
		for _, field := range fields {
			t.Run(fmt.Sprintf("modern=%v/%s", modern, field), func(t *testing.T) {
				fixture := openTypeTestPSD(t, "ffi AV", face.PostScriptName, 13.75, modern)
				before := sha256.Sum256(fixture)
				source, err := psdfile.Parse(fixture)
				if err != nil {
					t.Fatal(err)
				}
				defer source.Close()
				records, err := source.TextLayers(nil)
				if err != nil {
					t.Fatal(err)
				}
				record := records[0]
				descriptor := *record.TextDescriptor
				descriptor.Entries = make([]psdfile.DescriptorEntry, len(record.TextDescriptor.Entries))
				copy(descriptor.Entries, record.TextDescriptor.Entries)
				record.TextDescriptor = &descriptor
				switch field {
				case "transform":
					record.Transform[4] += 25
				case "transformBits":
					record.TransformBits[4]++
				case "layerIndex":
					record.LayerIndex++
				case "setIndex":
					record.LayerSetIndex++
				case "setKey":
					record.LayerSetKey = "fake"
				case "blockOffset":
					record.Block.Data.Offset++
				case "blockSpan":
					record.Block.Span.Offset++
				case "blockPadding":
					record.Block.Padding.Length++
				case "engineOffset":
					record.EngineData.Offset++
				case "hasEngine":
					record.HasEngineData = false
				case "prefix":
					record.Prefix.Offset++
				case "textDescriptorData":
					record.TextDescriptorData.Offset++
				case "warpVersionData":
					record.WarpVersionData.Offset++
				case "warpDescriptorData":
					record.WarpDescriptorData.Offset++
				case "boundsData":
					record.BoundsData.Offset++
				case "tail":
					record.Tail.Length++
				case "text":
					record.Text = "A"
				case "textIndex":
					record.TextIndex++
				case "hasTextIndex":
					record.HasTextIndex = false
				case "bounds":
					record.Bounds[0] += 10
				case "boundsBits":
					record.BoundsBits[0]++
				case "warpDescriptor":
					record.Warp = &psdfile.Descriptor{ClassID: "warp"}
				case "descriptorClass":
					descriptor.ClassID = "fake"
				case "descriptorSpan":
					descriptor.Span.Offset++
				case "orientationType", "antialiasType", "griddingType":
					key := map[string]string{"orientationType": "Ornt", "antialiasType": "AntA",
						"griddingType": "textGridding"}[field]
					for index := range descriptor.Entries {
						if descriptor.Entries[index].Key == key {
							enum := descriptor.Entries[index].Value.(psdfile.DescriptorEnum)
							enum.TypeID = "futureEnum"
							descriptor.Entries[index].Value = enum
						}
					}
				case "callerCycle":
					descriptor.Entries[0].Value = &descriptor
				}
				calls := 0
				resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
					calls++
					return face, nil
				})
				pixels, err := RenderOpenTypeTextLayer(nil, source, record, resolver,
					OpenTypeTextRenderOptions{Kerning: OpenTypeTextMetrics})
				if pixels != nil || !errors.Is(err, psdfile.ErrMalformed) || calls != 0 {
					t.Fatalf("forged %s reached rendering: pixels=%v calls=%d err=%v", field, pixels != nil, calls, err)
				}
				if sha256.Sum256(fixture) != before {
					t.Fatal("source bytes were changed")
				}
			})
		}
	}
	fixture := openTypeTestPSD(t, "ffi AV", face.PostScriptName, 13.75, true)
	source, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	record := records[0]
	before := sha256.Sum256(fixture)
	baseline, err := RenderOpenTypeTextLayer(nil, source, record,
		psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil }),
		OpenTypeTextRenderOptions{Kerning: OpenTypeTextMetrics})
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		record.TextDescriptor.Entries[0].Value = "Forged after source validation"
		record.Warp.ClassID = "Forged after source validation"
		return face, nil
	})
	actual, err := RenderOpenTypeTextLayer(nil, source, record, resolver,
		OpenTypeTextRenderOptions{Kerning: OpenTypeTextMetrics})
	if actual == nil || err != nil || sha256.Sum256(fixture) != before || !bytes.Equal(actual.Pix, baseline.Pix) {
		t.Fatalf("caller descriptor mutation reached owned source: %v", err)
	}
}

func TestOpenTypeTextSourceUnknownNativeEnumTypes(t *testing.T) {
	for _, key := range []string{"Ornt", "AntA", "textGridding"} {
		t.Run(key, func(t *testing.T) {
			fixture := openTypeTestPSD(t, "A", "owned", 13.75, true)
			source, err := psdfile.Parse(fixture)
			if err != nil {
				t.Fatal(err)
			}
			records, err := source.TextLayers(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range records[0].TextDescriptor.Entries {
				if entry.Key != key {
					continue
				}
				enum := entry.Value.(psdfile.DescriptorEnum)
				at := enum.TypeData.Offset + 4
				copy(fixture[at:at+enum.TypeData.Length-4], bytes.Repeat([]byte{'x'}, int(enum.TypeData.Length-4)))
			}
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			source, err = psdfile.Parse(fixture)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			records, err = source.TextLayers(nil)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
				calls++
				return nil, psdfont.ErrMissingFont
			})
			pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver,
				OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone})
			if pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) || calls != 0 {
				t.Fatalf("unknown native %s enum type accepted: %v", key, err)
			}
		})
	}
}
