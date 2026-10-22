package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func TestOpenTypeClusterPaintCompleteCoverageAndLogicalOwner(t *testing.T) {
	red, blue, green := [4]float64{.25, 1, 0, 0}, [4]float64{.75, 0, 0, 1}, [4]float64{1, 0, 1, 0}
	spans := []openTypeSourceSpan{{start: 0, length: 2, color: green},
		{start: 2, length: 1, color: red}, {start: 3, length: 1, color: blue},
		{start: 4, length: 2, color: green}}
	visits := int64(3)
	color, err := openTypeClusterColor(context.Background(), spans, 2, 3, OpenTypeFirstStyleClusterPaint, &visits)
	if err != nil || color != red || visits != 0 {
		t.Fatalf("logical owner did not retain complete first RGBA: %v %v visits%d", color, err, visits)
	}
	visits = 3
	if color, err := openTypeClusterColor(context.Background(), spans, 2, 3,
		OpenTypeRejectMixedClusterPaint, &visits); color != [4]float64{} ||
		!errors.Is(err, psdfile.ErrUnsupported) || visits != 0 {
		t.Fatalf("default failed before full coverage or accepted mixed RGBA: %v %v", color, err)
	}
	for _, amount := range []int64{2, 3, 4} {
		quota := amount
		color, err := openTypeClusterColor(context.Background(), spans, 2, 3,
			OpenTypeFirstStyleClusterPaint, &quota)
		if amount < 3 && (color != [4]float64{} || !errors.Is(err, psdfile.ErrLimit)) ||
			amount >= 3 && (color != red || err != nil) {
			t.Fatalf("cluster traversal quota%d: %v %v", amount, color, err)
		}
	}
	for _, bad := range []struct{ start, length int64 }{{5, 1}, {3, 2}, {4, 0}, {4, math.MaxInt64}} {
		broken := append([]openTypeSourceSpan(nil), spans...)
		broken[3].start, broken[3].length = bad.start, bad.length
		quota := int64(4)
		if _, err := openTypeClusterColor(context.Background(), broken, 2, 3,
			OpenTypeFirstStyleClusterPaint, &quota); !errors.Is(err, psdfile.ErrMalformed) {
			t.Fatalf("FirstStyle returned owner before validating later coverage: %v", err)
		}
	}
	quota := int64(3)
	if _, err := openTypeClusterColor(context.Background(), spans, 2, math.MaxInt64,
		OpenTypeFirstStyleClusterPaint, &quota); !errors.Is(err, psdfile.ErrMalformed) {
		t.Fatalf("cluster overflow accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := openTypeClusterColor(
		ctx, spans, 2, 3, OpenTypeFirstStyleClusterPaint, &quota,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cluster cancellation lost: %v", err)
	}
}

func TestOpenTypeClusterPaintPolicyFailsBeforeResolverAndSceneMetadata(t *testing.T) {
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	source, reader := openTypeSceneSource(t, openTypeTestPSDExtra(t, "ffi", face.PostScriptName,
		20, true, "", multiStyleEngine(t, "ffi", face.PostScriptName, 1, 20)), nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	resolverCalls := 0
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		resolverCalls++
		return face, nil
	})
	for _, options := range []OpenTypeTextRenderOptions{
		{Kerning: OpenTypeTextNone, ClusterPaint: OpenTypeClusterPaintPolicy(2)},
		{Kerning: OpenTypeTextNone, ClusterPaint: OpenTypeFirstStyleClusterPaint},
		{Kerning: OpenTypeTextNone, ClusterPaint: OpenTypeFirstStyleClusterPaint,
			StyleBoundaries: OpenTypeIsolatedStyleRuns},
	} {
		if result, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options); result != nil ||
			!errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("direct accepted contradictory/unknown cluster policy: %v", err)
		}
		if owned, _, err := ownOpenTypeTextOptions(&options, 64<<20); owned != nil ||
			!errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("owned scene options accepted contradictory/unknown policy: %v", err)
		}
		if result, err := RenderTile(nil, source, image.Rect(0, 0, 1, 1),
			TileOptions{ApproximateOpenTypeText: &options, FontResolver: resolver}); result != nil ||
			!errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("scene tile accepted contradictory/unknown cluster policy: %v", err)
		}
	}
	if resolverCalls != 0 || reader.forbiddenHit != 0 {
		t.Fatalf("rejected policy called resolver or read pixels: %d/%d", resolverCalls, reader.forbiddenHit)
	}
}

func firstStylePaintCase(t *testing.T) (multiStyleOracleCase, psdfont.FontResolver) {
	t.Helper()
	face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "opentype", "fonts", "base.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	row := multiStyleOracleCase{Text: "ffi", Ends: []int64{1, 2, 4},
		Fonts: []string{face.PostScriptName, face.PostScriptName, face.PostScriptName},
		Sizes: []float64{32, 32, 32}, Colors: [][4]float64{{.25, 1, 0, 0}, {.75, 0, 0, 1}, {1, 0, 1, 0}},
		Policy: OpenTypeCompatibleStyleRuns, Liga: true, Transform: [6]float64{1, .125, .25, 1, 18, 52}, Samples: 4}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	return row, resolver
}

func TestOpenTypeFirstStylePaintRetainedBytesGlyphQuotaAndCancel(t *testing.T) {
	row, resolver := firstStylePaintCase(t)
	source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row, true), nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	options := multiStyleCaseOptions(row)
	options.ClusterPaint = OpenTypeFirstStyleClusterPaint
	counter := &textSourceCountContext{}
	expected, err := RenderOpenTypeTextLayer(counter, source, records[0], resolver, options)
	if err != nil || expected == nil {
		t.Fatal(err)
	}
	for _, at := range []int64{1, counter.calls.Load() / 2, counter.calls.Load()} {
		result, err := RenderOpenTypeTextLayer(&textSourceCountContext{cancelAt: at}, source, records[0], resolver, options)
		if result != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("FirstStyle late cancellation exposed output: %v", err)
		}
	}
	lower, upper := int64(1), int64(8<<20)
	for lower < upper {
		middle := lower + (upper-lower)/2
		options.MaxWorkingBytes = middle
		result, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
		if err != nil {
			if result != nil || !errors.Is(err, psdfile.ErrLimit) && !errors.Is(err, psdfont.ErrLimit) &&
				!errors.Is(err, psdtext.ErrLimit) {
				t.Fatalf("unexpected byte-boundary error: %v", err)
			}
			lower = middle + 1
		} else {
			if result == nil || !bytes.Equal(result.Pix, expected.Pix) {
				t.Fatal("byte limit changed valid paint")
			}
			upper = middle
		}
	}
	for _, delta := range []int64{-1, 0, 1} {
		options.MaxWorkingBytes = lower + delta
		result, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
		if delta < 0 {
			if result != nil || !errors.Is(err, psdfile.ErrLimit) && !errors.Is(err, psdfont.ErrLimit) &&
				!errors.Is(err, psdtext.ErrLimit) {
				t.Fatalf("one-byte-short paint succeeded: %v", err)
			}
		} else if err != nil || result == nil || !bytes.Equal(result.Pix, expected.Pix) {
			t.Fatalf("tight byte budget failed: %v", err)
		}
	}
	t.Logf("FirstStyle exact retained byte threshold=%d; cancellation checkpoints=%d", lower, counter.calls.Load())
	options.MaxWorkingBytes = 64 << 20
	for _, amount := range []int{2, 3, 4} {
		options.MaxGlyphs = amount
		result, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
		if amount < 3 && (result != nil || !errors.Is(err, psdfile.ErrLimit)) ||
			amount >= 3 && (err != nil || result == nil || !bytes.Equal(result.Pix, expected.Pix)) {
			t.Fatalf("whole-layer input glyph quota%d reset at ligature: %v", amount, err)
		}
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("resource/cancel paint used saved glyph payload")
	}
}

func TestOpenTypeFirstStylePaintWholeStylesAndOptionOwnership(t *testing.T) {
	row, resolver := firstStylePaintCase(t)
	options := multiStyleCaseOptions(row)
	options.ClusterPaint = OpenTypeFirstStyleClusterPaint
	source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row, true), nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	bounds := image.Rect(17, 23, 85, 70)
	options.Bounds = &bounds
	expected, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
	if err != nil {
		t.Fatal(err)
	}
	reader.hook = func() {
		options.ClusterPaint = OpenTypeRejectMixedClusterPaint
		options.StyleBoundaries = OpenTypeSingleStyle
		options.Shape.Features[0].Enabled = false
		bounds = image.Rect(0, 0, 1, 1)
	}
	owned, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
	if err != nil || owned == nil || owned.Rect != expected.Rect || !bytes.Equal(owned.Pix, expected.Pix) {
		t.Fatalf("metadata callback changed owned policy/features/bounds: %v", err)
	}
	reader.hook = nil
	engine, err := psdtext.Decode(nil, multiStyleCaseEngine(t, row))
	if err != nil {
		t.Fatal(err)
	}
	styles := engine.Root.Lookup("EngineDict").Lookup("StyleRun").Lookup("RunArray")
	last := styles.Items[len(styles.Items)-1].Lookup("StyleSheet").Lookup("StyleSheetData")
	last.Entries = append(last.Entries, psdtext.Entry{Key: "UnqualifiedLastColorStyle", Value: &psdtext.Node{
		Kind: psdtext.Number, Lexeme: "0"}})
	data, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	source, reader = openTypeSceneSource(t, openTypeTestPSDExtra(t, row.Text, row.Fonts[0], row.Sizes[0],
		true, "", data), nil)
	records, err = source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		calls++
		return resolver.ResolveFont(ctx, name)
	})
	options = multiStyleCaseOptions(row)
	options.ClusterPaint = OpenTypeFirstStyleClusterPaint
	tiny := image.Rect(0, 0, 1, 1)
	options.Bounds = &tiny
	if result, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, options); result != nil ||
		!errors.Is(err, psdfile.ErrUnsupported) || calls != 0 {
		t.Fatalf("first-owner skipped an off-ROI later invalid source style: calls%d %v", calls, err)
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("style validation used saved glyph payload")
	}
}

func TestOpenTypeFirstStylePaintDoesNotCrossTypographyGroups(t *testing.T) {
	row, baseResolver := firstStylePaintCase(t)
	base, err := baseResolver.ResolveFont(context.Background(), row.Fonts[0])
	if err != nil {
		t.Fatal(err)
	}
	other, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "first-style-paint",
		"CFF1-Ligature3-Latn-v3.otf"))
	if err != nil {
		t.Fatal(err)
	}
	row.Ends = []int64{1, 4}
	row.Fonts, row.Sizes, row.Colors = row.Fonts[:2], row.Sizes[:2], row.Colors[:2]
	faces := map[string]*psdfont.Face{base.PostScriptName: base, other.PostScriptName: other}
	resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		face := faces[name]
		if face == nil {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
	for _, boundary := range []string{"size", "leading", "font"} {
		candidate := row
		candidate.Sizes = append([]float64(nil), row.Sizes...)
		candidate.Fonts = append([]string(nil), row.Fonts...)
		if boundary == "size" {
			candidate.Sizes[1] = 48
		}
		if boundary == "font" {
			candidate.Fonts[1] = other.PostScriptName
		}
		encoded := multiStyleCaseEngine(t, candidate)
		engine, err := psdtext.Decode(nil, encoded)
		if err != nil {
			t.Fatal(err)
		}
		if boundary == "leading" {
			data := engine.Root.Lookup("EngineDict").Lookup("StyleRun").Lookup("RunArray").Items[1]
			data.Lookup("StyleSheet").Lookup("StyleSheetData").Lookup("Leading").Lexeme = "64"
			encoded, err = psdtext.Encode(nil, engine)
			if err != nil {
				t.Fatal(err)
			}
		}
		metadata, err := engine.Metadata(nil)
		if err != nil {
			t.Fatal(err)
		}
		spans, _, err := openTypeTextSpans(context.Background(), metadata, candidate.Text, 16<<20)
		if err != nil {
			t.Fatal(err)
		}
		groups, _, err := openTypeStyleGroups(context.Background(), spans, candidate.Text,
			OpenTypeCompatibleStyleRuns, 16<<20)
		if err != nil || len(groups) != 2 {
			t.Fatalf("FirstStyle merged different %s groups: %v", boundary, err)
		}
		laidOut, _, err := layoutOpenTypeIndependentSpans(context.Background(), groups, resolver,
			multiStyleCaseOptions(candidate).Shape, 10000, 16<<20)
		if err != nil || len(laidOut) != 2 || laidOut[0].run == nil || laidOut[1].run == nil ||
			len(laidOut[0].run.Glyphs) != 1 || len(laidOut[1].run.Glyphs) != 1 ||
			laidOut[0].run.Glyphs[0].ClusterStart != 0 || laidOut[0].run.Glyphs[0].ClusterLength != 1 ||
			laidOut[1].run.Glyphs[0].ClusterStart != 1 || laidOut[1].run.Glyphs[0].ClusterLength != 2 {
			t.Fatalf("ligature crossed %s boundary instead of preserving f / fi groups: %v", boundary, err)
		}
		source, reader := openTypeSceneSource(t, openTypeTestPSDExtra(t, candidate.Text, candidate.Fonts[0],
			candidate.Sizes[0], true, "", encoded), nil)
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		options := multiStyleCaseOptions(candidate)
		options.StyleBoundaries = OpenTypeIsolatedStyleRuns
		isolated, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
		if err != nil {
			t.Fatal(err)
		}
		options.StyleBoundaries, options.ClusterPaint = OpenTypeCompatibleStyleRuns, OpenTypeFirstStyleClusterPaint
		first, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
		if err != nil || first == nil || !bytes.Equal(first.Pix, isolated.Pix) || reader.forbiddenHit != 0 {
			t.Fatalf("FirstStyle changed independent %s group paint: %v", boundary, err)
		}
	}
}
