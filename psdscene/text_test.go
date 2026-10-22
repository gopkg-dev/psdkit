package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func TestTextAffineTransformsGlyphPointsAndQuadraticControls(t *testing.T) {
	path := []psdfont.Command{{Op: psdfont.Move}, {Op: psdfont.Line, X: 10},
		{Op: psdfont.Quad, X: 10, Y: 10, ControlX: 5, ControlY: 20}, {Op: psdfont.Close}}
	run := &psdfont.Run{Scale: .5, Glyphs: []psdfont.PositionedGlyph{{X: 10, Y: 20,
		Glyph: &psdfont.Glyph{Path: path}}}}
	result, _, err := textAffineRun(context.Background(), run, [6]float64{0, 1, -1, 0, 100, 200}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	actual := result.Glyphs[0].Glyph.Path
	if result.Scale != 1 || actual[0].X != 80 || actual[0].Y != -210 ||
		actual[1].X != 80 || actual[1].Y != -215 || actual[2].X != 85 || actual[2].Y != -215 ||
		actual[2].ControlX != 90 || actual[2].ControlY != -212.5 {
		t.Fatal("source xx/xy/yx/yy/tx/ty did not transform baseline and quadratic controls")
	}
	actual[1].X = 999
	if path[1].X != 10 || run.Glyphs[0].X != 10 || run.Glyphs[0].Y != 20 {
		t.Fatal("transformed source changed the caller's run or glyph")
	}
	if _, _, err := textAffineRun(context.Background(), run, [6]float64{}, 1); !errors.Is(err, psdfile.ErrLimit) {
		t.Fatal("affine copy ignored its working budget")
	}
}

func TestTextAffineCoverageUsesSourceDocumentCoordinates(t *testing.T) {
	path := []psdfont.Command{{Op: psdfont.Move}, {Op: psdfont.Line, X: 10},
		{Op: psdfont.Line, X: 10, Y: 10}, {Op: psdfont.Line, Y: 10}, {Op: psdfont.Close}}
	run := &psdfont.Run{Scale: 1, Glyphs: []psdfont.PositionedGlyph{{Glyph: &psdfont.Glyph{Path: path}}}}
	rotated, _, err := textAffineRun(context.Background(), run, [6]float64{0, 1, -1, 0, 20, 20}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := psdfont.RasterizeRun(nil, rotated, image.Rect(0, 0, 40, 40))
	if err != nil || coverage.AlphaAt(25, 25).A != 255 || coverage.AlphaAt(15, 25).A != 0 ||
		coverage.AlphaAt(25, 15).A != 0 {
		t.Fatalf("affine glyph used a projected cached source coordinate system: %v", err)
	}
}

func TestTextTypographyGuardsRejectWarpAndNondefaultProperties(t *testing.T) {
	warp := &psdfile.Descriptor{Entries: []psdfile.DescriptorEntry{{Key: "warpStyle",
		Value: psdfile.DescriptorEnum{ValueID: "warpArc"}}}}
	if err := textWarp(warp); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatal("saved text warp was rendered as unwarped glyphs")
	}
	for _, test := range []struct {
		key   string
		value *psdtext.Node
	}{
		{"Tracking", &psdtext.Node{Kind: psdtext.Number, Lexeme: "10"}},
		{"FauxBold", &psdtext.Node{Kind: psdtext.Boolean, Bool: true}},
		{"Ligatures", &psdtext.Node{Kind: psdtext.Boolean, Bool: true}},
		{"UnknownPrivateProperty", &psdtext.Node{Kind: psdtext.Number, Lexeme: "0"}},
	} {
		err := textDefault(test.key, test.value, textCharacterNumbers, textCharacterBooleans, nil)
		if !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("nondefault typography was silently ignored: %v", err)
		}
	}
	if err := textDefault("Justification", &psdtext.Node{Kind: psdtext.Number, Lexeme: "2"},
		textParagraphNumbers, textParagraphBooleans, textParagraphArrays); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatal("paragraph centering was replaced with left alignment")
	}
	data := []byte(`<< /Rendered << /Shapes << /WritingDirection 0 /Children [
 << /ShapeType 0 /Procession 0 /Cookie << /Photoshop << /ShapeType 0 /PointBase [0 0]
 /Base << /ShapeType 0 /TransformPoint0 [1 0] /TransformPoint1 [0 1] /TransformPoint2 [0 0] >> >> >> >>
 ] >> >> >>`)
	engine, err := psdtext.Decode(nil, data)
	if err != nil || textPointShape(engine.Root) != nil {
		t.Fatalf("explicit point-shape metadata rejected: %v", err)
	}
	engine.Root.Lookup("Rendered").Lookup("Shapes").Lookup("Children").Items[0].Lookup("ShapeType").Lexeme = "1"
	if err := textPointShape(engine.Root); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatal("paragraph text was rendered as point text")
	}
}

type textRasterForbiddenReader struct {
	reader    io.ReaderAt
	forbidden []psdfile.Span
}

func (r *textRasterForbiddenReader) ReadAt(data []byte, offset int64) (int, error) {
	for _, span := range r.forbidden {
		if int64(len(data)) != 0 && offset < span.Offset+span.Length && offset+int64(len(data)) > span.Offset {
			return 0, errors.New("fixture forbids reading saved raster data")
		}
	}
	return r.reader.ReadAt(data, offset)
}

func TestTextNativeStandaloneHelperNeverReadsSavedPixels(t *testing.T) {
	if os.Getenv("PSDKIT_TEXT_RENDER_NATIVE") != "1" || os.Getenv("PSDKIT_TEST_FONT") == "" {
		t.Skip("native controls and one explicitly selected font file are required")
	}
	face, err := psdfont.Open(nil, os.Getenv("PSDKIT_TEST_FONT"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		if name != face.PostScriptName {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
	for _, mode := range []string{"none", "metrics"} {
		filename := filepath.Join("..", "output", "text-plain-native", "text-plain-"+mode+".psd")
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		reader := &textRasterForbiddenReader{reader: bytes.NewReader(data)}
		document, err := psdfile.Read(reader, int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = document.Close() })
		reader.forbidden = append(reader.forbidden, document.Composite.Data)
		for _, layer := range document.Layers {
			for _, channel := range layer.Channels {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
		records, err := document.TextLayers(nil)
		if err != nil || len(records) != 1 || records[0].Text != "AVég (space)\rAVég" {
			t.Fatalf("native control metadata differs: %v", err)
		}
		if _, err := RenderTextLayer(nil, document, records[0], resolver); !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatal("modern kerning ambiguity was silently inferred from legacy TySh")
		}
		kerning := mode == "metrics"
		output, err := RenderTextLayer(nil, document, records[0], resolver, TextRenderOptions{Kerning: &kerning})
		if err != nil || output.Bounds() != image.Rect(0, 0, 256, 128) {
			t.Fatalf("standalone glyph render failed without cached raster reads: %v", err)
		}
		nonzero := false
		for index := 3; index < len(output.Pix); index += 4 {
			nonzero = nonzero || output.Pix[index] != 0
		}
		if !nonzero {
			t.Fatal("explicit-font source render produced no glyph coverage")
		}
		_, err = RenderTextLayer(nil, document, records[0], nil, TextRenderOptions{Kerning: &kerning})
		if !errors.Is(err, psdfont.ErrMissingFont) {
			t.Fatal("missing resolver was replaced with saved layer pixels")
		}
		if err := document.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := RenderTextLayer(nil, document, records[0], resolver); !errors.Is(err, psdfile.ErrClosed) {
			t.Fatal("closed source accepted fresh text rendering")
		}
	}
}
