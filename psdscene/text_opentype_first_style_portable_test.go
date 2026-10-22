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

type firstPaintOracleCase struct {
	cffStyleBridgeCase
	Instance string
	Controls map[string]cffStyleBridgeCase
}

func firstPaintOracleOracle(t *testing.T) ([]firstPaintOracleCase, func(firstPaintOracleCase) psdfont.FontResolver) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "first-style-paint", "oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Instances []struct {
			ID, File, Name, SHA256 string
			Variations             []psdfont.VariationCoordinate
		}
		Cases []firstPaintOracleCase
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Cases) != 16 || len(oracle.Instances) != 4 {
		t.Fatalf("finite real GSUB4 FirstStyle corpus: %v", err)
	}
	faces, names := make(map[string]*psdfont.Face), make(map[string]string)
	for _, row := range oracle.Instances {
		binaryData, err := os.ReadFile(filepath.Join("..", row.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(binaryData)
		if hex.EncodeToString(digest[:]) != row.SHA256 {
			t.Fatal("FirstStyle font original bytes differ")
		}
		face, err := psdfont.Decode(nil, binaryData, psdfont.DecodeOptions{Variations: row.Variations})
		if err != nil || face == nil || face.PostScriptName != row.Name {
			t.Fatalf("exact selected font instance: %v", err)
		}
		faces[row.ID], names[row.ID] = face, row.Name
	}
	return oracle.Cases, func(row firstPaintOracleCase) psdfont.FontResolver {
		face, name := faces[row.Instance], names[row.Instance]
		if face == nil || name == "" || row.Fonts[0] != name {
			t.Fatal("FirstStyle case has no explicit font instance")
		}
		return psdfont.ResolverFunc(func(ctx context.Context, requested string) (*psdfont.Face, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if requested != name {
				return nil, psdfont.ErrMissingFont
			}
			return face, nil
		})
	}
}

func firstPaintOracleOptions(row firstPaintOracleCase) OpenTypeTextRenderOptions {
	options := multiStyleCaseOptions(row.multiStyleOracleCase)
	options.ClusterPaint = OpenTypeFirstStyleClusterPaint
	return options
}

func firstPaintOracleControl(row firstPaintOracleCase, kind string) firstPaintOracleCase {
	control := row.Controls[kind]
	row.Glyphs, row.Paints, row.Advance = control.Glyphs, control.Paints, control.Advance
	switch kind {
	case "equal-colors":
		row.Colors = append([][4]float64(nil), row.Colors...)
		for index := range row.Colors {
			row.Colors[index] = row.Colors[0]
		}
	case "unselected-feature":
		row.Liga = false
	}
	return row
}

func TestOpenTypeFirstStylePortableWholeRGBAAndSourceContracts(t *testing.T) {
	rows, resolve := firstPaintOracleOracle(t)
	for _, row := range rows {
		t.Run(row.Label, func(t *testing.T) {
			resolver := resolve(row)
			firstPaintOracleLayout(t, row, resolver)
			for _, modern := range []bool{false, true} {
				fixture := multiStyleCasePSD(t, row.multiStyleOracleCase, modern)
				before := sha256.Sum256(fixture)
				source, reader := openTypeSceneSource(t, fixture, nil)
				records, err := source.TextLayers(nil)
				if err != nil || len(records) != 1 {
					t.Fatalf("original source record: %v", err)
				}
				reject := multiStyleCaseOptions(row.multiStyleOracleCase)
				pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, reject)
				if pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) {
					t.Fatal("default mixed-RGBA rejection changed")
				}
				policy := firstPaintOracleOptions(row)
				expected := firstPaintOracleExpected(t, row)
				pixels, err = RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
				if err != nil || pixels == nil || !bytes.Equal(pixels.Pix, expected.Pix) {
					t.Fatalf("first logical UTF16 owner full RGBA: %v", err)
				}
				full, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy,
					FontResolver: resolver, CachedSpecialLayers: true})
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, full, expected, expected.Rect)
				settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver, CachedSpecialLayers: true}
				for _, bounds := range []image.Rectangle{image.Rect(7, 27, 79, 79), image.Rect(29, 34, 126, 90)} {
					tile, err := RenderTile(nil, source, bounds, settings)
					if err != nil {
						t.Fatal(err)
					}
					openTypeSceneEqualWindow(t, tile, expected, bounds)
				}
				visits := 0
				err = VisitTiles(nil, source, expected.Rect, image.Pt(40, 35), func(tile *image.NRGBA) error {
					visits++
					openTypeSceneEqualWindow(t, tile, expected, tile.Rect)
					return nil
				}, settings)
				if err != nil || visits != 12 || reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
					t.Fatalf("FirstStyle source visitor/cache/ownership: %v", err)
				}
				for _, kind := range []string{"equal-colors", "unselected-feature"} {
					control := firstPaintOracleControl(row, kind)
					controlData := multiStyleCasePSD(t, control.multiStyleOracleCase, modern)
					controlSource, controlReader := openTypeSceneSource(t, controlData, nil)
					controlRecords, err := controlSource.TextLayers(nil)
					if err != nil {
						t.Fatal(err)
					}
					firstPaintOracleLayout(t, control, resolver)
					controlExpected := firstPaintOracleExpected(t, control)
					paints := []OpenTypeClusterPaintPolicy{
						OpenTypeRejectMixedClusterPaint, OpenTypeFirstStyleClusterPaint,
					}
					for _, paint := range paints {
						options := firstPaintOracleOptions(control)
						options.ClusterPaint = paint
						actual, err := RenderOpenTypeTextLayer(nil, controlSource, controlRecords[0], resolver, options)
						if err != nil || actual == nil || !bytes.Equal(actual.Pix, controlExpected.Pix) {
							t.Fatalf("unchanged %s policy%d: %v", kind, paint, err)
						}
					}
					if controlReader.forbiddenHit != 0 {
						t.Fatal("control read saved payload")
					}
				}
			}
		})
	}
}

func TestOpenTypeFirstStylePortableMissingScriptNoOpAndExplicitDFLT(t *testing.T) {
	rows, _ := firstPaintOracleOracle(t)
	var row firstPaintOracleCase
	for _, candidate := range rows {
		if candidate.Instance == "cff1-default" {
			row = candidate
			break
		}
	}
	if row.Text != "ffi" {
		t.Fatal("original CFF1 qualification control absent")
	}
	data, err := os.ReadFile(filepath.Join("..", "psdfont", "testdata", "first-style-paint", "CFF1-Ligature3.otf"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != "ca26ca82596c721fb973e39ff28371746bc8a1eadf5dbc1b674c2f4fceeaec9b" {
		t.Fatal("original DFLT-only first-failure font was changed")
	}
	face, err := psdfont.Decode(nil, data)
	if err != nil || face.PostScriptName != row.Fonts[0] {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) { return face, nil })
	source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row.multiStyleOracleCase, true), nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	options := multiStyleCaseOptions(row.multiStyleOracleCase)
	unselected := firstPaintOracleExpected(t, firstPaintOracleControl(row, "unselected-feature"))
	result, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
	if err != nil || result == nil || !bytes.Equal(result.Pix, unselected.Pix) {
		t.Fatalf("default precise latn selection incorrectly fell back to DFLT: %v", err)
	}
	options.Shape.Script = "DFLT"
	if result, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options); result != nil ||
		!errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("explicit DFLT default paint did not reject mixed ligature: %v", err)
	}
	options.ClusterPaint = OpenTypeFirstStyleClusterPaint
	expected := firstPaintOracleExpected(t, row)
	result, err = RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
	if err != nil || result == nil || !bytes.Equal(result.Pix, expected.Pix) {
		t.Fatalf("explicit DFLT did not select the original three-character ligature: %v", err)
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("script qualification control read saved artwork")
	}
}

func TestOpenTypeFirstStylePortableRecursivePSDPSB(t *testing.T) {
	rows, resolve := firstPaintOracleOracle(t)
	for _, row := range rows {
		if row.Samples != 4 {
			continue
		}
		resolver := resolve(row)
		prototype := firstPaintOracleExpected(t, row)
		childData := multiStyleCasePSD(t, row.multiStyleOracleCase, true)
		canvas := image.Pt(300, 180)
		quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
		for _, large := range []bool{false, true} {
			contents := childData
			if large {
				contents = recursiveTextPSB(t, contents)
			}
			for _, external := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/psb=%t/external=%t", row.Label, large, external), func(t *testing.T) {
					data := recursiveTextParentFixture(t, contents, external, canvas, quad)
					before, childBefore := sha256.Sum256(data), sha256.Sum256(contents)
					parent, reader := recursiveTextOriginal(t, data)
					policy := firstPaintOracleOptions(row)
					settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
						CachedSpecialLayers: true, CachedPlacedLayers: true}
					mutate := func() {
						policy.ClusterPaint = OpenTypeRejectMixedClusterPaint
						policy.Shape.Features[0].Enabled = false
					}
					var childReaders []*openTypeSceneReader
					if external {
						settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
							mutate()
							child, childReader := openTypeSceneSource(t, contents, nil)
							childReaders = append(childReaders, childReader)
							return child, nil
						}
					}
					expected := firstPaintOracleProjected(t, parent, prototype, canvas)
					regions := []image.Rectangle{
						expected.Rect, image.Rect(25, 29, 101, 112), image.Rect(130, 20, 280, 149),
					}
					for _, bounds := range regions {
						policy = firstPaintOracleOptions(row)
						actual, err := RenderTile(nil, parent, bounds, settings)
						if err != nil {
							t.Fatal(err)
						}
						openTypeSceneEqualWindow(t, actual, expected, bounds)
					}
					policy = firstPaintOracleOptions(row)
					visits := 0
					err := VisitTiles(nil, parent, expected.Rect, image.Pt(100, 60), func(tile *image.NRGBA) error {
						visits++
						openTypeSceneEqualWindow(t, tile, expected, tile.Rect)
						mutate()
						return nil
					}, settings)
					if err != nil || visits != 9 || reader.forbiddenHit != 0 ||
						sha256.Sum256(data) != before || sha256.Sum256(contents) != childBefore {
						t.Fatalf("recursive cluster policy/cache/ownership: %v", err)
					}
					for _, childReader := range childReaders {
						if childReader.forbiddenHit != 0 {
							t.Fatal("child read saved text/composite")
						}
					}
				})
			}
		}
	}
}

func TestOpenTypeFirstStylePortablePolicySnapshotErrorsAndCancellation(t *testing.T) {
	rows, resolve := firstPaintOracleOracle(t)
	row := rows[len(rows)-2]
	resolver := resolve(row)
	fixture := multiStyleCasePSD(t, row.multiStyleOracleCase, true)
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := firstPaintOracleExpected(t, row)
	policy := firstPaintOracleOptions(row)
	bounds := expected.Rect
	policy.Bounds = &bounds
	settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver, CachedSpecialLayers: true}
	mutate := func() {
		policy.ClusterPaint = OpenTypeRejectMixedClusterPaint
		policy.Shape.Features[0].Enabled = false
		bounds = image.Rect(0, 0, 0, 0)
	}
	reader.hook = mutate
	actual, err := RenderTile(nil, source, expected.Rect, settings)
	if err != nil || actual == nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("ReaderAt changed owned ClusterPaint/features/bounds: %v", err)
	}
	reader.hook = nil
	policy = firstPaintOracleOptions(row)
	bounds = expected.Rect
	policy.Bounds = &bounds
	settings.FontResolver = psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		mutate()
		return resolver.ResolveFont(ctx, name)
	})
	actual, err = RenderTile(nil, source, expected.Rect, settings)
	if err != nil || actual == nil || !bytes.Equal(actual.Pix, expected.Pix) {
		t.Fatalf("font callback changed owned ClusterPaint/features/bounds: %v", err)
	}
	policy = firstPaintOracleOptions(row)
	bounds = expected.Rect
	policy.Bounds = &bounds
	settings.FontResolver = resolver
	visits := 0
	err = VisitTiles(nil, source, expected.Rect, image.Pt(40, 35), func(tile *image.NRGBA) error {
		visits++
		openTypeSceneEqualWindow(t, tile, expected, tile.Rect)
		mutate()
		return nil
	}, settings)
	if err != nil || visits != 12 {
		t.Fatalf("visitor callback changed policy snapshot: %v", err)
	}
	for _, boundary := range []OpenTypeStyleBoundaryPolicy{OpenTypeSingleStyle, OpenTypeIsolatedStyleRuns} {
		invalid := firstPaintOracleOptions(row)
		invalid.StyleBoundaries = boundary
		calls := 0
		counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
			calls++
			return resolver.ResolveFont(ctx, name)
		})
		reader.reads = 0
		pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, invalid)
		if pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) || calls != 0 || reader.reads != 0 {
			t.Fatal("contradictory FirstStyle reached metadata/font")
		}
	}
	policy = firstPaintOracleOptions(row)
	policy.ClusterPaint = OpenTypeClusterPaintPolicy(2)
	reader.reads = 0
	pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
	if pixels != nil || !errors.Is(err, psdfile.ErrUnsupported) || reader.reads != 0 {
		t.Fatal("unknown ClusterPaint reached metadata")
	}
	policy = firstPaintOracleOptions(row)
	settings.ApproximateOpenTypeText = &policy
	counter := &textSourceCountContext{}
	actual, err = RenderTile(counter, source, expected.Rect, settings)
	if err != nil || actual == nil {
		t.Fatal(err)
	}
	for _, poll := range []int64{1, counter.calls.Load() / 2, counter.calls.Load()} {
		partial, err := RenderTile(&textSourceCountContext{cancelAt: poll}, source, expected.Rect, settings)
		if partial != nil || !errors.Is(err, context.Canceled) {
			t.Fatal("late FirstStyle cancellation returned output")
		}
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("FirstStyle mutation/cancel read saved source pixels")
	}
}
func firstPaintOracleExpected(t *testing.T, row firstPaintOracleCase) *image.NRGBA {
	t.Helper()
	canvas := image.Rect(0, 0, 128, 96)
	result := image.NewNRGBA(canvas)
	if len(row.Paints) == 0 {
		t.Fatal("whole-glyph paint expected route is empty")
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
			t.Fatal("independent whole-glyph paint control has no visible pixels for a source style")
		}
	}
	return result
}

func firstPaintOracleProjected(
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

func firstPaintOracleLayout(t *testing.T, row firstPaintOracleCase, resolver psdfont.FontResolver) {
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
	shape := psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{
		{Tag: "liga", Enabled: row.Liga}, {Tag: "kern", Enabled: row.Kern},
	}}
	laidOut, _, err := layoutOpenTypeIndependentSpans(nil, groups, resolver, shape, 10000, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	for _, span := range laidOut {
		if span.run == nil {
			continue
		}
		for _, glyph := range span.run.Glyphs {
			if observed >= len(row.Glyphs) {
				t.Fatal("more positioned glyphs than independent HB result")
			}
			want := row.Glyphs[observed]
			if glyph.Glyph.ID != want.ID || glyph.ClusterStart != want.Start ||
				glyph.ClusterLength != want.Length || span.run.FontName != want.Font ||
				math.Abs(glyph.X-want.X) > 1e-12 || math.Abs(glyph.Y-want.Y) > 1e-12 ||
				math.Abs(glyph.Advance-want.Advance) > 1e-12 || math.Abs(span.run.Scale-want.Scale) > 1e-12 {
				t.Fatalf("HB glyph %d differs: %#v, want %#v", observed, glyph, want)
			}
			observed++
		}
	}
	if observed != len(row.Glyphs) {
		t.Fatal("fewer positioned glyphs than independent HB result")
	}
}
