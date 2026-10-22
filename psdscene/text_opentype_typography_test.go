package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"math"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"unicode/utf16"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

type typographyTestSettings struct {
	tracking, leading float64
	auto, bold        bool
	caps, align       int64
}

func typographyTestEngine(t *testing.T, text, font string, settings typographyTestSettings) []byte {
	t.Helper()
	engine, err := psdtext.Decode(nil, openTypeTestEngine(text, font, 20))
	if err != nil {
		t.Fatal(err)
	}
	data := engine.Root.Lookup("EngineDict")
	style := data.Lookup("StyleRun").Lookup("RunArray").Items[0].Lookup("StyleSheet").Lookup("StyleSheetData")
	style.Lookup("Tracking").Lexeme = strconv.FormatFloat(settings.tracking, 'g', -1, 64)
	style.Lookup("Leading").Lexeme = strconv.FormatFloat(settings.leading, 'g', -1, 64)
	style.Lookup("AutoLeading").Bool = settings.auto
	style.Lookup("FauxBold").Bool = settings.bold
	style.Lookup("FontCaps").Lexeme = strconv.FormatInt(settings.caps, 10)
	paragraphs := data.Lookup("ParagraphRun")
	prototype := paragraphs.Lookup("RunArray").Items[0]
	prototype.Lookup("ParagraphSheet").Lookup("Properties").Lookup("Justification").Lexeme =
		strconv.FormatInt(settings.align, 10)
	prototype.Lookup("ParagraphSheet").Lookup("Properties").Entries = append(
		prototype.Lookup("ParagraphSheet").Lookup("Properties").Entries,
		psdtext.Entry{Key: "AutoLeading", Value: &psdtext.Node{Kind: psdtext.Number, Lexeme: "1.2"}})
	paragraphs.Lookup("RunArray").Items = nil
	paragraphs.Lookup("RunLengthArray").Items = nil
	begin := 0
	for _, span := range regexp.MustCompile("\r\n|\r|\n").FindAllStringIndex(text+"\r", -1) {
		units := len(utf16.Encode([]rune((text + "\r")[begin:span[1]])))
		paragraphs.Lookup("RunArray").Items = append(paragraphs.Lookup("RunArray").Items,
			multiStyleCloneNode(prototype))
		paragraphs.Lookup("RunLengthArray").Items = append(paragraphs.Lookup("RunLengthArray").Items,
			&psdtext.Node{Kind: psdtext.Number, Lexeme: strconv.Itoa(units)})
		begin = span[1]
	}
	encoded, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func typographyTestFont(t *testing.T) (*psdfont.Face, psdfont.FontResolver) {
	t.Helper()
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
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

func TestOpenTypeTypographyLayoutUsesSourceTrackingCenterAndLeading(t *testing.T) {
	face, resolver := typographyTestFont(t)
	for _, automatic := range []bool{false, true} {
		settings := typographyTestSettings{tracking: -25, leading: 56, auto: automatic, caps: 2, align: 2}
		engine, err := psdtext.Decode(nil, typographyTestEngine(t, "aV\rAV", face.PostScriptName, settings))
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
		typography, lines, _, err := openTypeTypographyLines(context.Background(), metadata, "aV\rAV", 1<<20)
		if err != nil || len(lines) != 2 || lines[0].start != 0 || lines[1].start != 3 ||
			lines[0].length != 3 || lines[1].length != 3 {
			t.Fatalf("original CR or UTF16 paragraph range changed: %v %v", lines, err)
		}
		shape := psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "kern", Enabled: false}}}
		runs, _, err := layoutOpenTypePointLines(context.Background(), lines, typography, resolver, shape, 10, 8<<20)
		if err != nil {
			t.Fatal(err)
		}
		plain, err := psdfont.LayoutOpenType(nil, "AV", psdfont.Style{FontName: face.PostScriptName,
			Size: 20, Leading: 56}, resolver, shape)
		if err != nil || len(plain.Glyphs) != 2 {
			t.Fatalf("independent untracked control: %v", err)
		}
		width := plain.AdvanceWidth - .5
		leading := float64(56)
		if automatic {
			leading = 24
		}
		for lineIndex, run := range runs {
			if run.AdvanceWidth != width {
				t.Fatalf("tracking width=%g expected %g", run.AdvanceWidth, width)
			}
			for glyphIndex, glyph := range run.Glyphs {
				expected := plain.Glyphs[glyphIndex]
				if glyph.Rune != expected.Rune || glyph.Glyph.ID != expected.Glyph.ID ||
					glyph.X != expected.X-width/2-float64(glyphIndex)*.5 ||
					glyph.Y != expected.Y+float64(lineIndex)*leading ||
					glyph.ClusterStart != expected.ClusterStart+int64(lineIndex*3) {
					t.Fatalf("line%d glyph%d source typography ignored: %+v", lineIndex, glyphIndex, glyph)
				}
			}
		}
		after, err := psdtext.Encode(nil, engine)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("borrowed source engine was modified")
		}
	}
}

func TestOpenTypeTypographyKeepsSourceEmptyLinesAndCRLFCoverage(t *testing.T) {
	face, _ := typographyTestFont(t)
	for _, text := range []string{"\rAV\r", "A\r\nV\n", "\r\r\r"} {
		engine, err := psdtext.Decode(nil, typographyTestEngine(t, text, face.PostScriptName,
			typographyTestSettings{leading: 56}))
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := engine.Metadata(nil)
		if err != nil {
			t.Fatal(err)
		}
		_, lines, _, err := openTypeTypographyLines(context.Background(), metadata, text, 1<<20)
		if err != nil || len(lines) != len(metadata.Paragraphs) || lines[len(lines)-1].text != "" {
			t.Fatalf("source paragraph was trimmed: %q %v %v", text, lines, err)
		}
		metadata.Paragraphs[0].Length++
		_, rejected, _, err := openTypeTypographyLines(context.Background(), metadata, text, 1<<20)
		if rejected != nil || !errors.Is(err, psdfile.ErrMalformed) {
			t.Fatalf("invalid paragraph coverage accepted: %v", err)
		}
	}
}

func TestOpenTypeTypographySceneFullROIAndVisitorNeverUsesSavedGlyphs(t *testing.T) {
	face, resolver := typographyTestFont(t)
	for _, bold := range []bool{false, true} {
		text := "aV\rAV"
		settings := typographyTestSettings{tracking: -50, leading: 30, auto: true, caps: 2, align: 2, bold: bold}
		fixture := openTypeSceneWrongSavedBounds(t, openTypeTestPSDExtra(t, text, face.PostScriptName, 20,
			true, "", typographyTestEngine(t, text, face.PostScriptName, settings)))
		before := sha256.Sum256(fixture)
		source, reader := openTypeSceneSource(t, fixture, nil)
		records, err := source.TextLayers(nil)
		if err != nil || len(records) != 1 {
			t.Fatal(err)
		}
		policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
		strict := policy
		strict.ApproximateTypography = false
		rejected, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, strict)
		if rejected != nil || !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("strict multiline default changed: %v", err)
		}
		full, err := Render(nil, source, Options{FontResolver: resolver, ApproximateOpenTypeText: &policy})
		if err != nil {
			t.Fatal(err)
		}
		painted := 0
		for index := 3; index < len(full.Pix); index += 4 {
			if full.Pix[index] != 0 {
				painted++
			}
		}
		if painted == 0 {
			t.Fatal("live font outlines produced no visible text")
		}
		for _, window := range []image.Rectangle{image.Rect(0, 40, 21, 53), image.Rect(18, 60, 33, 76),
			image.Rect(0, 0, 4, 4), image.Rect(2, 13, 80, 82)} {
			tile, err := RenderTile(nil, source, window,
				TileOptions{FontResolver: resolver, ApproximateOpenTypeText: &policy})
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, tile, full, window)
		}
		visited := 0
		err = VisitTiles(nil, source, full.Bounds(), image.Pt(17, 13), func(tile *image.NRGBA) error {
			visited++
			openTypeSceneEqualWindow(t, tile, full, tile.Bounds())
			return nil
		}, TileOptions{FontResolver: resolver, ApproximateOpenTypeText: &policy})
		if err != nil || visited == 0 || reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
			t.Fatalf("live source/visitor invariant: visited%d forbidden%d err=%v", visited, reader.forbiddenHit, err)
		}
	}
}

func TestOpenTypeTypographyLimitsCancelAndUnknownProperties(t *testing.T) {
	face, resolver := typographyTestFont(t)
	text := "AV\r\rAV"
	settings := typographyTestSettings{tracking: -25, leading: 56, align: 2}
	fixture := openTypeTestPSDExtra(t, text, face.PostScriptName, 20, true, "",
		typographyTestEngine(t, text, face.PostScriptName, settings))
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		calls++
		return resolver.ResolveFont(ctx, name)
	})
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true, MaxGlyphs: 5}
	if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy); pixels != nil ||
		!errors.Is(err, psdfile.ErrLimit) || calls != 0 {
		t.Fatalf("whole-layer paragraph/glyph preflight: calls%d %v", calls, err)
	}
	policy.MaxGlyphs, policy.MaxWorkingBytes = 0, 16384
	if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, policy); pixels != nil ||
		!errors.Is(err, psdfile.ErrLimit) || calls != 0 {
		t.Fatalf("metadata/output quota preflight: calls%d %v", calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	policy.MaxWorkingBytes = 0
	if pixels, err := RenderOpenTypeTextLayer(ctx, source, records[0], counted, policy); pixels != nil ||
		!errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("pre-cancel: calls%d %v", calls, err)
	}
	engine, err := psdtext.Decode(nil, typographyTestEngine(t, text, face.PostScriptName, settings))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	metadata.Styles[0].Style.Data.Entries = append(metadata.Styles[0].Style.Data.Entries,
		psdtext.Entry{Key: "UnknownTypography", Value: &psdtext.Node{Kind: psdtext.Number, Lexeme: "0"}})
	_, lines, _, err := openTypeTypographyLines(context.Background(), metadata, text, 1<<20)
	if lines != nil || !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("unknown typography ignored: %v", err)
	}
	entries := metadata.Styles[0].Style.Data.Entries
	metadata.Styles[0].Style.Data.Entries = entries[:len(entries)-1]
	metadata.Styles[0].Style.Data.Entries = append(metadata.Styles[0].Style.Data.Entries,
		psdtext.Entry{Key: "Tracking", Value: &psdtext.Node{Kind: psdtext.Number, Lexeme: "0"}})
	_, lines, _, err = openTypeTypographyLines(context.Background(), metadata, text, 1<<20)
	if lines != nil || !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("duplicate tracking accepted: %v", err)
	}
	entries = metadata.Styles[0].Style.Data.Entries
	metadata.Styles[0].Style.Data.Entries = entries[:len(entries)-1]
	value := math.Inf(1)
	metadata.Styles[0].Style.Tracking = &value
	_, lines, _, err = openTypeTypographyLines(context.Background(), metadata, text, 1<<20)
	if lines != nil || !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("nonfinite tracking accepted: %v", err)
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("typography validation read saved glyph channels")
	}
}
