package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdtext"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func cffSeacAuthorSceneOracle(t *testing.T) ([]cffStyleBridgeCase, psdfont.FontResolver) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cff-seac", "oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts []struct{ File, Name, SHA256 string }
		Cases []cffStyleBridgeCase
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Fonts) != 4 || len(oracle.Cases) != 16 {
		t.Fatalf("four seac fonts and sixteen finite style/sample cases required: %v", err)
	}
	faces := make(map[string]*psdfont.Face)
	for _, row := range oracle.Fonts {
		fontData, err := os.ReadFile(filepath.Join("..", row.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(fontData)
		if hex.EncodeToString(digest[:]) != row.SHA256 {
			t.Fatal("portable seac font SHA differs from independently recorded input")
		}
		face, err := psdfont.Decode(nil, fontData)
		if err != nil || face == nil || face.PostScriptName != row.Name {
			t.Fatalf("portable seac font identity: %v", err)
		}
		faces[row.Name] = face
	}
	return oracle.Cases, psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		if faces[name] == nil {
			return nil, psdfont.ErrMissingFont
		}
		return faces[name], nil
	})
}

func cffSeacAuthorSceneExpected(t *testing.T, row cffStyleBridgeCase) *image.NRGBA {
	t.Helper()
	canvas := image.Rect(0, 0, 128, 96)
	result := image.NewNRGBA(canvas)
	if len(row.Paints) != 2 {
		t.Fatal("independent seac control requires two source color paints")
	}
	for _, paint := range row.Paints {
		coverage, err := psdvector.Rasterize(nil, &paint.Mask, canvas, psdvector.Options{Samples: row.Samples})
		if err != nil {
			t.Fatal(err)
		}
		visible := 0
		for index, sample := range coverage.Pix {
			alpha := uint32(math.Round(float64(sample) * paint.Color[0]))
			if alpha == 0 {
				continue
			}
			visible++
			at := index * 4
			backAlpha := uint32(result.Pix[at+3])
			combined := alpha*255 + backAlpha*(255-alpha)
			for channel := range 3 {
				front := uint32(math.Round(paint.Color[channel+1] * 255))
				numerator := front*alpha*255 + uint32(result.Pix[at+channel])*backAlpha*(255-alpha)
				result.Pix[at+channel] = byte((numerator + combined/2) / combined)
			}
			result.Pix[at+3] = byte((combined + 127) / 255)
		}
		if visible == 0 {
			t.Fatal("independent seac control has no visible pixels for a source style")
		}
	}
	return result
}

func cffSeacAuthorSceneLayout(t *testing.T, row cffStyleBridgeCase, resolver psdfont.FontResolver) {
	t.Helper()
	engine, err := psdtext.Decode(nil, multiStyleCaseEngine(t, row.multiStyleOracleCase))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	spans, _, err := openTypeTextSpans(context.Background(), metadata, row.Text, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	groups, _, err := openTypeStyleGroups(context.Background(), spans, row.Text, row.Policy, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	laidOut, _, err := layoutOpenTypeIndependentSpans(nil, groups, resolver, psdfont.ShapeOptions{
		Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: false}, {Tag: "kern", Enabled: false}},
	}, 10000, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	for _, span := range laidOut {
		for _, glyph := range span.run.Glyphs {
			if observed >= len(row.Glyphs) {
				t.Fatal("candidate seac layout produced an unrecorded HB glyph")
			}
			want := row.Glyphs[observed]
			if glyph.Glyph.ID != want.ID || span.run.FontName != want.Font ||
				glyph.ClusterStart != want.Start || glyph.ClusterLength != want.Length ||
				math.Abs(glyph.X-want.X) > 1e-12 || math.Abs(glyph.Y-want.Y) > 1e-12 ||
				math.Abs(glyph.Advance-want.Advance) > 1e-12 || math.Abs(span.run.Scale-want.Scale) > 1e-12 {
				t.Fatalf("independent seac HB layout differs at glyph %d", observed)
			}
			observed++
		}
	}
	if observed != 3 || observed != len(row.Glyphs) {
		t.Fatalf("candidate seac layout glyph count differs: %d", observed)
	}
}

func TestCFFSeacAuthorSceneSourceROIStylesAndPixels(t *testing.T) {
	rows, resolver := cffSeacAuthorSceneOracle(t)
	for _, row := range rows {
		t.Run(row.Label, func(t *testing.T) {
			cffSeacAuthorSceneLayout(t, row, resolver)
			expected := cffSeacAuthorSceneExpected(t, row)
			for _, modern := range []bool{false, true} {
				fixture := multiStyleCasePSD(t, row.multiStyleOracleCase, modern)
				before := sha256.Sum256(fixture)
				source, reader := openTypeSceneSource(t, fixture, nil)
				records, err := source.TextLayers(nil)
				if err != nil || len(records) != 1 {
					t.Fatalf("one live seac text layer required: modern=%t: %v", modern, err)
				}
				policy := multiStyleCaseOptions(row.multiStyleOracleCase)
				direct, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
				if err != nil || direct == nil || direct.Rect != expected.Rect || !bytes.Equal(direct.Pix, expected.Pix) {
					t.Fatalf("FontTools/vector seac direct pixels differ: modern=%t: %v", modern, err)
				}
				full, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
					CachedSpecialLayers: true})
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, full, expected, expected.Rect)
				settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
					CachedSpecialLayers: true}
				for _, region := range []image.Rectangle{image.Rect(10, 40, 76, 75), image.Rect(60, 38, 126, 80)} {
					actual, err := RenderTile(nil, source, region, settings)
					if err != nil {
						t.Fatal(err)
					}
					openTypeSceneEqualWindow(t, actual, expected, region)
				}
				visited := 0
				err = VisitTiles(nil, source, expected.Rect, image.Pt(40, 35), func(tile *image.NRGBA) error {
					openTypeSceneEqualWindow(t, tile, expected, tile.Rect)
					visited++
					return nil
				}, settings)
				if err != nil || visited != 12 || reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
					t.Fatalf("seac Source visitor/cache/ownership: modern=%t count=%d reads=%d err=%v",
						modern, visited, reader.forbiddenHit, err)
				}
			}
		})
	}
}

func cffSeacAuthorSceneProjected(
	t *testing.T, parent *psdfile.Document, prototype *image.NRGBA, canvas image.Point,
) *image.NRGBA {
	t.Helper()
	metadata, err := parent.ReadLayerMetadata(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := psdrender.CompileProjectionRegion(metadata.Layer,
		image.Rectangle{Max: canvas}, image.Rectangle{Max: canvas})
	if err != nil {
		t.Fatal(err)
	}
	projected, origin, err := psdrender.ProjectPlacedContentsContext(nil, metadata.Layer, prototype, plan)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := psdcompose.Render(nil, &psdmodel.Document{Width: canvas.X, Height: canvas.Y,
		Layers: []*psdmodel.Layer{{Bounds: image.Rectangle{Min: origin, Max: origin.Add(projected.Rect.Size())},
			Pixels: projected, BlendMode: "normal"}}})
	if err != nil {
		t.Fatal(err)
	}
	return expected
}

func TestCFFSeacAuthorSceneRecursivePSDPSB(t *testing.T) {
	rows, resolver := cffSeacAuthorSceneOracle(t)
	for _, row := range rows {
		if row.Samples != 4 {
			continue
		}
		prototype := cffSeacAuthorSceneExpected(t, row)
		childData := multiStyleCasePSD(t, row.multiStyleOracleCase, true)
		canvas := image.Pt(300, 180)
		quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
		for _, psb := range []bool{false, true} {
			contents := childData
			if psb {
				contents = recursiveTextPSB(t, contents)
			}
			for _, external := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/psb=%t/external=%t", row.Label, psb, external), func(t *testing.T) {
					childBefore := sha256.Sum256(contents)
					parentData := recursiveTextParentFixture(t, contents, external, canvas, quad)
					parentBefore := sha256.Sum256(parentData)
					parent, parentReader := recursiveTextOriginal(t, parentData)
					policy := multiStyleCaseOptions(row.multiStyleOracleCase)
					settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
						CachedSpecialLayers: true, CachedPlacedLayers: true}
					var childReaders []*openTypeSceneReader
					if external {
						settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
							child, reader := openTypeSceneSource(t, contents, nil)
							childReaders = append(childReaders, reader)
							return child, nil
						}
					}
					expected := cffSeacAuthorSceneProjected(t, parent, prototype, canvas)
					for _, region := range []image.Rectangle{
						expected.Rect, image.Rect(23, 19, 87, 95), image.Rect(130, 35, 280, 145),
					} {
						actual, err := RenderTile(nil, parent, region, settings)
						if err != nil {
							t.Fatal(err)
						}
						openTypeSceneEqualWindow(t, actual, expected, region)
					}
					visited := 0
					err := VisitTiles(nil, parent, expected.Rect, image.Pt(100, 60), func(tile *image.NRGBA) error {
						visited++
						openTypeSceneEqualWindow(t, tile, expected, tile.Rect)
						return nil
					}, settings)
					if err != nil || visited != 9 || parentReader.forbiddenHit != 0 ||
						sha256.Sum256(contents) != childBefore || sha256.Sum256(parentData) != parentBefore {
						t.Fatalf("seac recursive cache/ownership: visits=%d saved reads=%d err=%v",
							visited, parentReader.forbiddenHit, err)
					}
					if external && len(childReaders) == 0 {
						t.Fatal("seac recursive external path never opened its original child")
					}
					for _, reader := range childReaders {
						if reader.forbiddenHit != 0 {
							t.Fatal("seac recursive external path read saved child pixels")
						}
					}
				})
			}
		}
	}
}

func TestCFFSeacAuthorSceneCancellationAndLiveCauses(t *testing.T) {
	rows, resolver := cffSeacAuthorSceneOracle(t)
	row := rows[4]
	expected := cffSeacAuthorSceneExpected(t, row)
	fixture := multiStyleCasePSD(t, row.multiStyleOracleCase, true)
	source, reader := openTypeSceneSource(t, fixture, nil)
	policy := multiStyleCaseOptions(row.multiStyleOracleCase)
	settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver, CachedSpecialLayers: true}
	emptyRegion := image.Rect(50, 50, 50, 50)
	empty, err := RenderTile(nil, source, emptyRegion, settings)
	if err != nil || empty == nil || empty.Rect != emptyRegion || len(empty.Pix) != 0 || reader.forbiddenHit != 0 {
		t.Fatalf("seac empty ROI source qualification/cache: %v", err)
	}
	policy.MaxGlyphs = 2
	fontCalls := 0
	settings.FontResolver = psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		fontCalls++
		return resolver.ResolveFont(ctx, name)
	})
	limited, err := RenderTile(nil, source, emptyRegion, settings)
	if limited != nil || !errors.Is(err, psdfile.ErrLimit) || fontCalls != 0 || reader.forbiddenHit != 0 {
		t.Fatalf("seac empty ROI skipped whole source glyph preflight: calls=%d err=%v", fontCalls, err)
	}
	policy.MaxGlyphs = 0
	settings.FontResolver = resolver
	baseline := &textSourceCountContext{}
	pixels, err := RenderTile(baseline, source, expected.Rect, settings)
	if err != nil {
		t.Fatal(err)
	}
	openTypeSceneEqualWindow(t, pixels, expected, expected.Rect)
	polls := baseline.calls.Load()
	if polls < 4 {
		t.Fatal("seac Scene control lacks measurable context checks")
	}
	for _, cancelAt := range []int64{1, polls / 2, polls - 1, polls} {
		pixels, err := RenderTile(&textSourceCountContext{cancelAt: cancelAt}, source, expected.Rect, settings)
		if pixels != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("seac Scene cancellation returned output at poll %d/%d: %v", cancelAt, polls, err)
		}
	}
	fontCause := errors.New("author QA seac resolver live cause")
	settings.FontResolver = psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		return nil, fontCause
	})
	for _, region := range []image.Rectangle{expected.Rect, emptyRegion} {
		pixels, err = RenderTile(nil, source, region, settings)
		if pixels != nil || !errors.Is(err, fontCause) || reader.forbiddenHit != 0 {
			t.Fatal("seac Scene replaced the resolver cause or fell back to saved artwork")
		}
	}
	settings.FontResolver = resolver
	readCause := errors.New("author QA seac source ReaderAt live cause")
	reader.reader = multiStyleCausalReader{ReaderAt: reader.reader, cause: readCause}
	pixels, err = RenderTile(nil, source, expected.Rect, settings)
	if pixels != nil || !errors.Is(err, readCause) {
		t.Fatalf("seac Scene replaced live ReaderAt cause: %v", err)
	}
	_ = source.Close()
	pixels, err = RenderTile(nil, source, expected.Rect, settings)
	if pixels != nil || !errors.Is(err, psdfile.ErrClosed) {
		t.Fatal("closed original seac Source returned pixels")
	}
	canvas := image.Pt(300, 180)
	quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
	parent, parentReader := recursiveTextOriginal(t, recursiveTextParentFixture(t, fixture, true, canvas, quad))
	settings.CachedPlacedLayers = true
	var childReaders []*openTypeSceneReader
	settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		child, childReader := openTypeSceneSource(t, fixture, nil)
		childReaders = append(childReaders, childReader)
		return child, nil
	}
	empty, err = RenderTile(nil, parent, emptyRegion, settings)
	if err != nil || empty == nil || empty.Rect != emptyRegion || len(empty.Pix) != 0 ||
		parentReader.forbiddenHit != 0 || len(childReaders) != 0 {
		t.Fatalf("recursive seac empty ROI source preflight/cache: external=%d err=%v", len(childReaders), err)
	}
	recursiveExpected := cffSeacAuthorSceneProjected(t, parent, expected, canvas)
	recursiveBaseline := &textSourceCountContext{}
	pixels, err = RenderTile(recursiveBaseline, parent, recursiveExpected.Rect, settings)
	if err != nil {
		t.Fatal(err)
	}
	openTypeSceneEqualWindow(t, pixels, recursiveExpected, recursiveExpected.Rect)
	recursivePolls := recursiveBaseline.calls.Load()
	if recursivePolls < 4 {
		t.Fatal("recursive seac control lacks measurable context checks")
	}
	for _, cancelAt := range []int64{1, recursivePolls / 2, recursivePolls - 1, recursivePolls} {
		pixels, err = RenderTile(&textSourceCountContext{cancelAt: cancelAt}, parent, recursiveExpected.Rect, settings)
		if pixels != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("recursive seac cancellation returned output at poll %d/%d: %v",
				cancelAt, recursivePolls, err)
		}
	}
	externalCause := errors.New("author QA seac external original cause")
	settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		return nil, externalCause
	}
	pixels, err = RenderTile(nil, parent, recursiveExpected.Rect, settings)
	if pixels != nil || !errors.Is(err, externalCause) || parentReader.forbiddenHit != 0 {
		t.Fatal("recursive seac replaced live external cause or read saved parent pixels")
	}
	for _, childReader := range childReaders {
		if childReader.forbiddenHit != 0 {
			t.Fatal("recursive seac cancellation read saved child pixels")
		}
	}
}
