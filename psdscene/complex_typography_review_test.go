package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func complexTypographyReviewFace(t *testing.T) (*psdfont.Face, psdfont.FontResolver) {
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

func complexTypographyReviewEngine(t *testing.T, text, font string, size float64, properties map[string]string) []byte {
	t.Helper()
	engine, err := psdtext.Decode(nil, openTypeTestEngine(text, font, size))
	if err != nil {
		t.Fatal(err)
	}
	style := engine.Root.Lookup("EngineDict").Lookup("StyleRun").Lookup("RunArray").Items[0]
	data := style.Lookup("StyleSheet").Lookup("StyleSheetData")
	for key, spelling := range properties {
		node := data.Lookup(key)
		if node == nil {
			t.Fatalf("review fixture has no style property %s", key)
		}
		if node.Kind == psdtext.Boolean {
			node.Bool = spelling == "true"
		} else {
			node.Lexeme = spelling
		}
	}
	encoded, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestComplexTypographyReviewPrivateArtworkIndependence(t *testing.T) {
	face, resolver := complexTypographyReviewFace(t)
	text := "AV AV"
	engine := complexTypographyReviewEngine(t, text, face.PostScriptName, 36,
		map[string]string{"Tracking": "-25", "FauxBold": "true"})
	first := openTypeTestPSDExtra(t, text, face.PostScriptName, 36, true, "", engine)
	second := bytes.ReplaceAll(first, []byte("/savedGlyphData <aabbcc>"), []byte("/savedGlyphData <ffee11>"))
	second = bytes.ReplaceAll(second, []byte("/0 [ << /0 0 >> ] /2"), []byte("/0 [ << /0 9 >> ] /2"))
	if bytes.Equal(first, second) {
		t.Fatal("private saved artwork control did not change source bytes")
	}
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
	var expected *image.NRGBA
	for _, fixture := range [][]byte{first, second} {
		before := sha256.Sum256(fixture)
		source, reader := openTypeSceneSource(t, openTypeSceneWrongSavedBounds(t, fixture), nil)
		actual, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
			CachedTextLayers: true, CachedSpecialLayers: true})
		if err != nil {
			t.Fatal(err)
		}
		if expected == nil {
			expected = actual
		} else {
			openTypeSceneEqualWindow(t, actual, expected, actual.Rect)
		}
		for _, window := range []image.Rectangle{image.Rect(0, 25, 40, 60), image.Rect(40, 25, 97, 62)} {
			roi, err := RenderTile(nil, source, window, TileOptions{ApproximateOpenTypeText: &policy,
				FontResolver: resolver, CachedTextLayers: true, CachedSpecialLayers: true})
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, roi, actual, window)
		}
		if reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
			t.Fatal("live typography depended on saved channels or mutated source bytes")
		}
	}
	nonzero := false
	for offset := 3; offset < len(expected.Pix); offset += 4 {
		if expected.Pix[offset] != 0 {
			nonzero = true
			break
		}
	}
	if !nonzero {
		t.Fatal("private artwork control compared only transparent output")
	}
}

func TestComplexTypographyReviewNoFontSubstitutionOrCacheFallback(t *testing.T) {
	face, _ := complexTypographyReviewFace(t)
	fixture := openTypeTestPSDExtra(t, "AV", "RequestedDifferentFont", 36, true, "",
		complexTypographyReviewEngine(t, "AV", "RequestedDifferentFont", 36,
			map[string]string{"Tracking": "-25", "FauxBold": "true"}))
	source, reader := openTypeSceneSource(t, fixture, nil)
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	pixels, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
		CachedTextLayers: true, CachedSpecialLayers: true})
	if pixels != nil || !errors.Is(err, psdfont.ErrMissingFont) || reader.forbiddenHit != 0 {
		t.Fatalf("different font or a saved raster was selected: %v", err)
	}
	policy.MaxWorkingBytes = 1
	pixels, err = Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver})
	if pixels != nil || !errors.Is(err, psdfile.ErrLimit) || reader.forbiddenHit != 0 {
		t.Fatalf("explicit typography byte limit was ignored: %v", err)
	}
}

func TestComplexTypographyReviewSourcePropertiesAndOptionOwnership(t *testing.T) {
	face, resolver := complexTypographyReviewFace(t)
	text := "AV AV"
	render := func(engine []byte, options OpenTypeTextRenderOptions) *image.NRGBA {
		t.Helper()
		fixture := openTypeTestPSDExtra(t, text, face.PostScriptName, 36, true, "", engine)
		source, reader := openTypeSceneSource(t, fixture, nil)
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		before := sha256.Sum256(fixture)
		actual, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
		if err != nil {
			t.Fatal(err)
		}
		if reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
			t.Fatal("source properties used saved artwork or changed source bytes")
		}
		return actual
	}
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
	baseEngine := complexTypographyReviewEngine(t, text, face.PostScriptName, 36, nil)
	base := render(baseEngine, policy)
	for _, name := range []string{"Tracking", "FauxBold", "Justification"} {
		t.Run(name, func(t *testing.T) {
			engine := bytes.Clone(baseEngine)
			switch name {
			case "Tracking":
				engine = bytes.ReplaceAll(engine, []byte("/Tracking 0"), []byte("/Tracking -100"))
			case "FauxBold":
				engine = bytes.ReplaceAll(engine, []byte("/FauxBold false"), []byte("/FauxBold true"))
			case "Justification":
				engine = bytes.ReplaceAll(engine, []byte("/Justification 0"), []byte("/Justification 2"))
			}
			actual := render(engine, policy)
			if bytes.Equal(actual.Pix, base.Pix) {
				t.Fatal("nondefault source property did not affect live glyph geometry")
			}
		})
	}
	fixture := openTypeTestPSDExtra(t, text, face.PostScriptName, 36, true, "", baseEngine)
	source, _ := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	bounds := image.Rect(0, 0, 128, 96)
	policy.Bounds = &bounds
	policy.Shape.Features = []psdfont.ShapeFeature{{Tag: "liga", Enabled: false}}
	mutator := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		bounds = image.Rect(0, 0, 1, 1)
		policy.Shape.Features[0].Enabled = true
		return face, nil
	})
	actual, err := RenderOpenTypeTextLayer(nil, source, records[0], mutator, policy)
	if err != nil || actual.Rect != base.Rect || !bytes.Equal(actual.Pix, base.Pix) {
		t.Fatalf("resolver changed borrowed caller bounds/features: %v", err)
	}
}

func TestComplexTypographyReviewCancellationLimitsAndMalformedProperties(t *testing.T) {
	face, resolver := complexTypographyReviewFace(t)
	text := "AV AV"
	engine := complexTypographyReviewEngine(t, text, face.PostScriptName, 36,
		map[string]string{"Tracking": "-25", "FauxBold": "true"})
	fixture := openTypeTestPSDExtra(t, text, face.PostScriptName, 36, true, "", engine)
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
	counter := &textSourceCountContext{}
	base, err := RenderOpenTypeTextLayer(counter, source, records[0], resolver, policy)
	if err != nil {
		t.Fatal(err)
	}
	late := &textSourceCountContext{cancelAt: counter.calls.Load()}
	partial, err := RenderOpenTypeTextLayer(late, source, records[0], resolver, policy)
	if partial != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("last-stage cancellation returned partial pixels: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	canceling := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		cancel()
		return face, nil
	})
	partial, err = RenderOpenTypeTextLayer(ctx, source, records[0], canceling, policy)
	if partial != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("resolver cancellation returned partial pixels: %v", err)
	}
	policy.MaxGlyphs = 2
	calls := 0
	counted := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		calls++
		return face, nil
	})
	partial, err = RenderOpenTypeTextLayer(nil, source, records[0], counted, policy)
	if partial != nil || !errors.Is(err, psdfile.ErrLimit) || calls != 0 {
		t.Fatalf("input glyph limit reached font resolver: calls=%d error=%v", calls, err)
	}
	policy.MaxGlyphs = 0
	for _, spelling := range []string{"1e309", "-1e309", "1e300"} {
		t.Run("tracking="+spelling, func(t *testing.T) {
			data := bytes.ReplaceAll(engine, []byte("/Tracking -25"), []byte("/Tracking "+spelling))
			candidate := openTypeTestPSDExtra(t, text, face.PostScriptName, 36, false, "", data)
			other, _ := openTypeSceneSource(t, candidate, nil)
			textRecords, recordErr := other.TextLayers(nil)
			if recordErr != nil {
				t.Fatal(recordErr)
			}
			pixels, renderErr := RenderOpenTypeTextLayer(nil, other, textRecords[0], resolver, policy)
			if pixels != nil || renderErr == nil {
				t.Fatal("nonfinite or overflowing source tracking produced pixels")
			}
		})
	}
	before := sha256.Sum256(base.Pix)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(base.Pix) != before || reader.forbiddenHit != 0 {
		t.Fatal("owned output changed after source close or used saved artwork")
	}
	partial, err = RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
	if partial != nil || !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source rendered cached typography: %v", err)
	}
}

func TestComplexTypographyReviewTrackingUsesPixelEm(t *testing.T) {
	face, resolver := complexTypographyReviewFace(t)
	size, tracking := float64(36), float64(-25)
	style := openTypeTypography{style: psdfont.Style{FontName: face.PostScriptName, Size: size, Leading: 30,
		Tracking: tracking}}
	lines := []openTypePointLine{{text: "AV AV", length: 6, leading: 30, justification: 2}}
	shape := psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "kern", Enabled: false}}}
	plain, err := psdfont.LayoutOpenType(nil, lines[0].text, psdfont.Style{
		FontName: face.PostScriptName, Size: size, Leading: 30}, resolver, shape)
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := layoutOpenTypePointLines(context.Background(), lines, style, resolver, shape, 100, 1<<20)
	if err != nil || len(actual) != 1 || actual[0] == nil {
		t.Fatal(err)
	}
	step := tracking * size / 1000
	width := plain.AdvanceWidth + float64(len(plain.Glyphs)-1)*step
	for index, glyph := range actual[0].Glyphs {
		expected := plain.Glyphs[index].X + float64(index)*step - width/2
		if math.Abs(glyph.X-expected) > 1e-12 {
			t.Fatalf("glyph %d source tracking is not measured in pixel em: actual=%g expected=%g", index, glyph.X, expected)
		}
	}
}

func complexTypographyReviewParagraphEngine(t *testing.T, text, font string, autoLeading bool) []byte {
	t.Helper()
	data := complexTypographyReviewEngine(t, text, font, 36,
		map[string]string{"Tracking": "-25", "FauxBold": "true", "AutoLeading": strconv.FormatBool(autoLeading)})
	engine, err := psdtext.Decode(nil, data)
	if err != nil {
		t.Fatal(err)
	}
	paragraph := engine.Root.Lookup("EngineDict").Lookup("ParagraphRun")
	prototype := paragraph.Lookup("RunArray").Items[0]
	properties := prototype.Lookup("ParagraphSheet").Lookup("Properties")
	properties.Entries = append(properties.Entries, psdtext.Entry{Key: "AutoLeading",
		Value: &psdtext.Node{Kind: psdtext.Number, Lexeme: "1.2"}})
	lengths := []int64{}
	count := int64(0)
	for position := 0; position < len(text); position++ {
		count++
		if text[position] != '\r' && text[position] != '\n' {
			continue
		}
		if text[position] == '\r' && position+1 < len(text) && text[position+1] == '\n' {
			position++
			count++
		}
		lengths = append(lengths, count)
		count = 0
	}
	lengths = append(lengths, count+1)
	array, lengthArray := paragraph.Lookup("RunArray"), paragraph.Lookup("RunLengthArray")
	array.Items, lengthArray.Items = nil, nil
	for _, length := range lengths {
		array.Items = append(array.Items, multiStyleCloneNode(prototype))
		lengthArray.Items = append(lengthArray.Items,
			&psdtext.Node{Kind: psdtext.Number, Lexeme: strconv.FormatInt(length, 10)})
	}
	encoded, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestComplexTypographyReviewCRLFEmptyLinesAndLeading(t *testing.T) {
	face, resolver := complexTypographyReviewFace(t)
	for _, text := range []string{"AV\r\n\r\nAV", "AV\n\nAV", "AV\r\rAV", "AV\r"} {
		t.Run(strconv.Quote(text), func(t *testing.T) {
			engine, err := psdtext.Decode(nil, complexTypographyReviewParagraphEngine(t, text, face.PostScriptName, true))
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := engine.Metadata(nil)
			if err != nil {
				t.Fatal(err)
			}
			typography, lines, _, err := openTypeTypographyLines(context.Background(), metadata, text, 1<<20)
			if err != nil || len(lines) != len(metadata.Paragraphs) {
				t.Fatal(err)
			}
			for _, line := range lines {
				if math.Abs(line.leading-43.2) > 1e-12 {
					t.Fatal("source automatic leading ratio was not applied")
				}
			}
			runs, _, err := layoutOpenTypePointLines(context.Background(), lines, typography, resolver,
				psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "kern", Enabled: false}}}, 100, 2<<20)
			if err != nil {
				t.Fatal(err)
			}
			for index, run := range runs {
				if lines[index].text == "" {
					if run != nil {
						t.Fatal("empty source line invented a glyph")
					}
					continue
				}
				if run == nil || len(run.Glyphs) == 0 ||
					math.Abs(run.Glyphs[0].Y-float64(index)*43.2) > 1e-12 ||
					run.Glyphs[0].ClusterStart != lines[index].start {
					t.Fatal("line break position, empty-line leading or original UTF16 cluster changed")
				}
			}
		})
	}
	engine, err := psdtext.Decode(nil, complexTypographyReviewParagraphEngine(t, "AV\rAV", face.PostScriptName, false))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, lines, _, err := openTypeTypographyLines(context.Background(), metadata, "AV\rAV", 1<<20)
	if err != nil || len(lines) != 2 || lines[0].leading != 30 || lines[1].leading != 30 {
		t.Fatal("explicit leading was replaced by the automatic ratio", err)
	}
	metadata.Paragraphs[0].Length--
	_, lines, _, err = openTypeTypographyLines(context.Background(), metadata, "AV\rAV", 1<<20)
	if lines != nil || !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("paragraph boundary disconnected from source line breaks was accepted: %v", err)
	}
}

func TestComplexTypographyReviewFillAlphaAndAffineFauxBold(t *testing.T) {
	face, resolver := complexTypographyReviewFace(t)
	text := "AV"
	engine := complexTypographyReviewEngine(t, text, face.PostScriptName, 36,
		map[string]string{"FauxBold": "true", "Tracking": "-25"})
	engine = bytes.ReplaceAll(engine, []byte("/Values [1 .4 .2 .8]"), []byte("/Values [.4 .4 .2 .8]"))
	fixture := openTypeTestPSDExtra(t, text, face.PostScriptName, 36, true, "", engine)
	indexed, err := psdfile.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	records, err := indexed.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	transformAt := records[0].Block.Data.Offset + 2
	for index, value := range []float64{1.4, .2, .3, .8, 20, 50} {
		binary.BigEndian.PutUint64(fixture[transformAt+int64(index)*8:], math.Float64bits(value))
	}
	_ = indexed.Close()
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err = source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
	full, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
	if err != nil {
		t.Fatal(err)
	}
	maximumAlpha := byte(0)
	for offset := 3; offset < len(full.Pix); offset += 4 {
		maximumAlpha = max(maximumAlpha, full.Pix[offset])
	}
	if maximumAlpha != 102 {
		t.Fatalf("fill alpha was compounded by faux-bold components: actual maximum=%d expected=102", maximumAlpha)
	}
	for _, rectangle := range []image.Rectangle{image.Rect(13, 23, 40, 53), image.Rect(30, 37, 63, 68)} {
		policy.Bounds = &rectangle
		roi, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
		if err != nil {
			t.Fatal(err)
		}
		openTypeSceneEqualWindow(t, roi, full, rectangle)
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("affine faux-bold source sampled saved glyph channels")
	}
}

func TestComplexTypographyReviewEngineGeometryFlagsAreNotIgnored(t *testing.T) {
	face, _ := complexTypographyReviewFace(t)
	for _, candidate := range []struct {
		name string
		key  string
		node *psdtext.Node
		kind error
	}{
		{"wholePixelWidths", "UseFractionalGlyphWidths", &psdtext.Node{Kind: psdtext.Boolean}, psdfile.ErrUnsupported},
		{"malformedGrid", "GridInfo", &psdtext.Node{Kind: psdtext.Number, Lexeme: "0"}, psdfile.ErrMalformed},
		{"activeGrid", "GridInfo", &psdtext.Node{Kind: psdtext.Dictionary, Entries: []psdtext.Entry{
			{Key: "GridIsOn", Value: &psdtext.Node{Kind: psdtext.Boolean, Bool: true}},
		}}, psdfile.ErrUnsupported},
		{"gridBaseline", "GridInfo", &psdtext.Node{Kind: psdtext.Dictionary, Entries: []psdtext.Entry{
			{Key: "AlignLineHeightToGridFlags", Value: &psdtext.Node{Kind: psdtext.Boolean, Bool: true}},
		}}, psdfile.ErrUnsupported},
	} {
		t.Run(candidate.name, func(t *testing.T) {
			engine, err := psdtext.Decode(nil, complexTypographyReviewEngine(t, "AV", face.PostScriptName, 36, nil))
			if err != nil {
				t.Fatal(err)
			}
			metadata, err := engine.Metadata(nil)
			if err != nil {
				t.Fatal(err)
			}
			metadata.Engine.Entries = append(metadata.Engine.Entries, psdtext.Entry{Key: candidate.key, Value: candidate.node})
			_, lines, _, err := openTypeTypographyLines(context.Background(), metadata, "AV", 1<<20)
			if lines != nil || !errors.Is(err, candidate.kind) {
				t.Fatalf("engine geometry flag was silently ignored: %v", err)
			}
		})
	}
}
