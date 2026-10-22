package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdtext"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type cff2AuthorSceneCase struct {
	cffStyleBridgeCase
	Instance             string
	DeclaredShapedGlyphs []struct {
		ID               uint16
		Start, Length    int64
		EffectiveAdvance float64
		RoundedAdvance   int64
	}
}

type cff2AuthorSceneInstance struct {
	ID, File, Name, SHA256 string
	UPM                    uint16
	Variations             []psdfont.VariationCoordinate
	Coordinates            []psdfont.VariationInstanceCoordinate
	Variable               bool
	Glyphs                 []struct {
		ID                                           uint16
		Name                                         string
		Commands                                     [][]float64
		RawAdvance                                   uint16
		RawLSB                                       int16
		EffectiveAdvance, EffectiveLSB, EffectiveRSB float64
		RoundedAdvance                               int64
		Bounds                                       [4]float64
		HasInk                                       bool
	}
}

func cff2AuthorSceneOracle(t *testing.T) ([]cff2AuthorSceneCase, func(cff2AuthorSceneCase) psdfont.FontResolver) {
	t.Helper()
	data, err := os.ReadFile(testfixture.Path(t, "psdscene/testdata/cff2-source/oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Instances []cff2AuthorSceneInstance
		Cases     []cff2AuthorSceneCase
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Instances) < 16 || len(oracle.Cases) < 48 {
		t.Fatalf("finite primary CFF2 instance/source controls required: %v", err)
	}
	faces := make(map[string]*psdfont.Face)
	names := make(map[string]string)
	for _, instance := range oracle.Instances {
		fontData, err := os.ReadFile(filepath.Join("..", instance.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(fontData)
		if hex.EncodeToString(digest[:]) != instance.SHA256 {
			t.Fatal("frozen CFF2 input font SHA differs")
		}
		request := append([]psdfont.VariationCoordinate(nil), instance.Variations...)
		face, err := psdfont.Decode(nil, fontData, psdfont.DecodeOptions{Variations: request})
		if err != nil || face == nil || face.PostScriptName != instance.Name || face.UnitsPerEm != instance.UPM {
			t.Fatalf("CFF2 explicit immutable instance identity %s: %v", instance.ID, err)
		}
		for index := range request {
			request[index].Tag, request[index].Value = "xxxx", -123
		}
		coordinates, err := face.VariationCoordinates(nil)
		if err != nil || len(coordinates) != len(instance.Coordinates) {
			t.Fatalf("selected CFF2 coordinate count %s: %v", instance.ID, err)
		}
		for index, coordinate := range coordinates {
			if coordinate != instance.Coordinates[index] {
				t.Fatalf("declared fixed coordinate differs %s/%d", instance.ID, index)
			}
		}
		for _, expected := range instance.Glyphs {
			glyph, err := face.Glyph(nil, expected.ID)
			if err != nil || glyph == nil || glyph.AdvanceWidth != expected.RawAdvance ||
				glyph.LeftSideBearing != expected.RawLSB || glyph.CharstringWidth != 0 ||
				len(glyph.Instructions) != 0 || len(glyph.ComponentSources) != 0 || glyph.HasComponentMetrics {
				t.Fatalf("CFF2 raw metrics/source domains %s/%d: %v", instance.ID, expected.ID, err)
			}
			metrics := glyph.InstanceMetrics
			if instance.Variable {
				if metrics == nil || metrics.RawAdvanceWidth != expected.RawAdvance ||
					metrics.RawLeftSideBearing != expected.RawLSB ||
					math.Float64bits(metrics.AdvanceWidth) != math.Float64bits(expected.EffectiveAdvance) ||
					math.Float64bits(metrics.LeftSideBearing) != math.Float64bits(expected.EffectiveLSB) ||
					math.Float64bits(metrics.RightSideBearing) != math.Float64bits(expected.EffectiveRSB) ||
					metrics.OutlineBounds != expected.Bounds || metrics.HasInk != expected.HasInk {
					t.Fatalf("CFF2 precise metric profile differs %s/%d: %+v", instance.ID, expected.ID, metrics)
				}
			} else if metrics != nil {
				t.Fatal("static CFF2 gained a variable metric record")
			}
			source, err := face.ReadSpan(nil, glyph.Source)
			if err != nil || !bytes.Equal(source, fontData[glyph.Source.Offset:glyph.Source.Offset+glyph.Source.Length]) {
				t.Fatalf("CFF2 original glyph source differs: %v", err)
			}
		}
		for index := range fontData {
			fontData[index] = 0
		}
		owned, err := face.ReadSpan(nil, psdfont.Span{Length: int64(len(fontData))})
		if err != nil || sha256.Sum256(owned) != digest {
			t.Fatalf("CFF2 caller font mutation changed immutable instance %s: %v", instance.ID, err)
		}
		faces[instance.ID], names[instance.ID] = face, instance.Name
	}
	return oracle.Cases, func(row cff2AuthorSceneCase) psdfont.FontResolver {
		face := faces[row.Instance]
		name := names[row.Instance]
		if face == nil || name == "" || len(row.Fonts) == 0 || row.Fonts[0] != name {
			t.Fatal("CFF2 source case has no explicitly selected immutable face")
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

func cff2AuthorSceneExpected(t *testing.T, row cff2AuthorSceneCase) *image.NRGBA {
	t.Helper()
	canvas := image.Rect(0, 0, 128, 96)
	result := image.NewNRGBA(canvas)
	wantPaints := 2
	if row.Policy == OpenTypeSingleStyle {
		wantPaints = 1
	}
	if len(row.Paints) != wantPaints {
		t.Fatal("CFF2 source color paint count differs")
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
			t.Fatal("independent CFF2 instance control has no visible pixels for a source style")
		}
	}
	return result
}

func cff2AuthorSceneDirectExpected(t *testing.T, row cff2AuthorSceneCase) *image.NRGBA {
	t.Helper()
	expected := cff2AuthorSceneExpected(t, row)
	if row.Policy == OpenTypeSingleStyle {
		for at := 0; at < len(expected.Pix); at += 4 {
			for channel := range 3 {
				expected.Pix[at+channel] = byte(math.Round(row.Colors[0][channel+1] * 255))
			}
		}
	}
	return expected
}

func cff2AuthorSceneLegacyPSD(t *testing.T, row cff2AuthorSceneCase, modern bool) []byte {
	t.Helper()
	if row.Policy != OpenTypeSingleStyle || row.Kern || row.Liga {
		t.Fatal("qualified legacy generation requires single-style unfeatured typography")
	}
	engine, err := psdtext.Decode(nil, multiStyleCaseEngine(t, row.multiStyleOracleCase))
	if err != nil {
		t.Fatal(err)
	}
	styles := engine.Root.Lookup("EngineDict").Lookup("StyleRun").Lookup("RunArray")
	if len(styles.Items) != 1 {
		t.Fatal("qualified legacy generation has multiple styles")
	}
	style := styles.Items[0].Lookup("StyleSheet").Lookup("StyleSheetData")
	style.Lookup("Ligatures").Bool = false
	style.Lookup("AutoKerning").Bool = false
	encoded, err := psdtext.Encode(nil, engine)
	if err != nil {
		t.Fatal(err)
	}
	result := openTypeTestPSDExtra(t, row.Text, row.Fonts[0], row.Sizes[0], modern, "", encoded)
	at := bytes.Index(result, []byte("8BIMTySh")) + 14
	if at < 14 {
		t.Fatal("qualified legacy source has no TySh")
	}
	for index, value := range row.Transform {
		binary.BigEndian.PutUint64(result[at+index*8:], math.Float64bits(value))
	}
	return result
}

func cff2AuthorSceneLayout(t *testing.T, row cff2AuthorSceneCase, resolver psdfont.FontResolver) {
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
	shapedCount := 0
	for _, group := range groups {
		face, err := resolver.ResolveFont(context.Background(), group.style.FontName)
		if err != nil {
			t.Fatal(err)
		}
		shaped, err := psdfont.Shape(nil, face, group.text, psdfont.ShapeOptions{
			Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: row.Liga}, {Tag: "kern", Enabled: row.Kern}},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, glyph := range shaped.Glyphs {
			if shapedCount >= len(row.DeclaredShapedGlyphs) {
				t.Fatal("unrecorded CFF2 precise shaped glyph")
			}
			want := row.DeclaredShapedGlyphs[shapedCount]
			if glyph.GlyphID != want.ID || glyph.ClusterStart+group.start != want.Start ||
				glyph.ClusterLength != want.Length || glyph.XAdvance != want.RoundedAdvance {
				t.Fatal("CFF2 final floor(total+.5) shaped profile differs")
			}
			if glyph.InstancePosition != nil {
				if math.Float64bits(glyph.InstancePosition.XAdvance) != math.Float64bits(want.EffectiveAdvance) {
					t.Fatal("CFF2 precise pre-round shaped advance differs")
				}
			} else if float64(glyph.XAdvance) != want.EffectiveAdvance {
				t.Fatal("static CFF2 precise advance differs")
			}
			shapedCount++
		}
	}
	if shapedCount != len(row.DeclaredShapedGlyphs) {
		t.Fatal("CFF2 precise shape count differs")
	}

	laidOut, _, err := layoutOpenTypeIndependentSpans(nil, groups, resolver, psdfont.ShapeOptions{
		Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: row.Liga}, {Tag: "kern", Enabled: row.Kern}},
	}, 10000, 32<<20)
	if err != nil {
		t.Fatal(err)
	}
	observed := 0
	for _, span := range laidOut {
		for _, glyph := range span.run.Glyphs {
			if observed >= len(row.Glyphs) {
				t.Fatal("candidate CFF2 instance layout produced an unrecorded HB glyph")
			}
			want := row.Glyphs[observed]
			if glyph.Glyph.ID != want.ID || span.run.FontName != want.Font ||
				glyph.ClusterStart != want.Start || glyph.ClusterLength != want.Length ||
				math.Abs(glyph.X-want.X) > 1e-12 || math.Abs(glyph.Y-want.Y) > 1e-12 ||
				math.Abs(glyph.Advance-want.Advance) > 1e-12 || math.Abs(span.run.Scale-want.Scale) > 1e-12 {
				t.Fatalf("independent CFF2 instance HB layout differs at glyph %d", observed)
			}
			observed++
		}
	}
	if observed == 0 || observed != len(row.Glyphs) {
		t.Fatalf("candidate CFF2 instance layout glyph count differs: %d", observed)
	}
}

func TestCFF2AuthorSceneSourceROIStylesAndPixels(t *testing.T) {
	rows, resolve := cff2AuthorSceneOracle(t)
	for _, row := range rows {
		t.Run(row.Label, func(t *testing.T) {
			resolver := resolve(row)
			cff2AuthorSceneLayout(t, row, resolver)
			expected := cff2AuthorSceneExpected(t, row)
			expectedDirect := cff2AuthorSceneDirectExpected(t, row)
			for _, modern := range []bool{false, true} {
				fixture := multiStyleCasePSD(t, row.multiStyleOracleCase, modern)
				before := sha256.Sum256(fixture)
				source, reader := openTypeSceneSource(t, fixture, nil)
				records, err := source.TextLayers(nil)
				if err != nil || len(records) != 1 {
					t.Fatalf("one live CFF2 instance text layer required: modern=%t: %v", modern, err)
				}
				policy := multiStyleCaseOptions(row.multiStyleOracleCase)
				direct, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
				if err != nil || direct == nil || direct.Rect != expectedDirect.Rect ||
					!bytes.Equal(direct.Pix, expectedDirect.Pix) {
					t.Fatalf("FontTools/vector CFF2 instance direct pixels differ: modern=%t: %v", modern, err)
				}
				if row.Policy == OpenTypeSingleStyle && !row.Kern {
					kerning := false
					legacy, err := RenderTextLayer(nil, source, records[0], resolver,
						TextRenderOptions{Samples: row.Samples, Kerning: &kerning})
					if legacy != nil || !errors.Is(err, psdfile.ErrUnsupported) || reader.forbiddenHit != 0 {
						t.Fatal("original OpenType Ligatures=true input gained legacy eligibility or cache fallback")
					}
					legacyData := cff2AuthorSceneLegacyPSD(t, row, modern)
					legacyBefore := sha256.Sum256(legacyData)
					legacySource, legacyReader := openTypeSceneSource(t, legacyData, nil)
					legacyRecords, err := legacySource.TextLayers(nil)
					if err != nil || len(legacyRecords) != 1 {
						t.Fatal("qualified legacy record is absent")
					}
					legacy, err = RenderTextLayer(nil, legacySource, legacyRecords[0], resolver,
						TextRenderOptions{Samples: row.Samples, Kerning: &kerning})
					if err != nil || legacy == nil || legacy.Rect != expectedDirect.Rect ||
						!bytes.Equal(legacy.Pix, expectedDirect.Pix) || legacyReader.forbiddenHit != 0 ||
						sha256.Sum256(legacyData) != legacyBefore {
						t.Fatalf("qualified legacy CFF2 profile pixels/cache/ownership: modern=%t: %v", modern, err)
					}
					t.Logf("qualified legacy source %s modern=%t bytes=%d sha256=%x",
						row.Label, modern, len(legacyData), legacyBefore)
				}
				full, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
					CachedSpecialLayers: true})
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, full, expected, expected.Rect)
				settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
					CachedSpecialLayers: true}
				for _, region := range []image.Rectangle{image.Rect(3, 15, 73, 66), image.Rect(45, 28, 126, 85)} {
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
					t.Fatalf("CFF2 instance Source visitor/cache/ownership: modern=%t count=%d reads=%d err=%v",
						modern, visited, reader.forbiddenHit, err)
				}
			}
		})
	}
}

func cff2AuthorSceneProjected(
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

func TestCFF2AuthorSceneRecursivePSDPSB(t *testing.T) {
	rows, resolve := cff2AuthorSceneOracle(t)
	for _, row := range rows {
		if row.Samples != 4 || row.Policy != OpenTypeCompatibleStyleRuns {
			continue
		}
		resolver := resolve(row)
		prototype := cff2AuthorSceneExpected(t, row)
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
					expected := cff2AuthorSceneProjected(t, parent, prototype, canvas)
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
						t.Fatalf("CFF2 instance recursive cache/ownership: visits=%d saved reads=%d err=%v",
							visited, parentReader.forbiddenHit, err)
					}
					if external && len(childReaders) == 0 {
						t.Fatal("CFF2 instance recursive external path never opened its original child")
					}
					for _, reader := range childReaders {
						if reader.forbiddenHit != 0 {
							t.Fatal("CFF2 instance recursive external path read saved child pixels")
						}
					}
				})
			}
		}
	}
}

func TestCFF2AuthorSceneCancellationAndLiveCauses(t *testing.T) {
	rows, resolve := cff2AuthorSceneOracle(t)
	row := rows[0]
	found := false
	for _, candidate := range rows {
		if candidate.Kern && candidate.Policy == OpenTypeCompatibleStyleRuns && candidate.Samples == 4 {
			row, found = candidate, true
			break
		}
	}
	if !found {
		t.Fatal("CFF2 cancellation control requires the selected variable pair instance")
	}
	resolver := resolve(row)
	expected := cff2AuthorSceneExpected(t, row)
	fixture := multiStyleCasePSD(t, row.multiStyleOracleCase, true)
	source, reader := openTypeSceneSource(t, fixture, nil)
	policy := multiStyleCaseOptions(row.multiStyleOracleCase)
	settings := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver, CachedSpecialLayers: true}
	emptyRegion := image.Rect(50, 50, 50, 50)
	empty, err := RenderTile(nil, source, emptyRegion, settings)
	if err != nil || empty == nil || empty.Rect != emptyRegion || len(empty.Pix) != 0 || reader.forbiddenHit != 0 {
		t.Fatalf("CFF2 instance empty ROI source qualification/cache: %v", err)
	}
	policy.MaxGlyphs = 2
	fontCalls := 0
	settings.FontResolver = psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
		fontCalls++
		return resolver.ResolveFont(ctx, name)
	})
	limited, err := RenderTile(nil, source, emptyRegion, settings)
	if limited != nil || !errors.Is(err, psdfile.ErrLimit) || fontCalls != 0 || reader.forbiddenHit != 0 {
		t.Fatalf("CFF2 instance empty ROI skipped whole source glyph preflight: calls=%d err=%v", fontCalls, err)
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
		t.Fatal("CFF2 instance Scene control lacks measurable context checks")
	}
	for _, cancelAt := range []int64{1, polls / 2, polls - 1, polls} {
		pixels, err := RenderTile(&textSourceCountContext{cancelAt: cancelAt}, source, expected.Rect, settings)
		if pixels != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("CFF2 instance Scene cancellation returned output at poll %d/%d: %v", cancelAt, polls, err)
		}
	}
	fontCause := errors.New("author QA CFF2 instance resolver live cause")
	settings.FontResolver = psdfont.ResolverFunc(func(context.Context, string) (*psdfont.Face, error) {
		return nil, fontCause
	})
	for _, region := range []image.Rectangle{expected.Rect, emptyRegion} {
		pixels, err = RenderTile(nil, source, region, settings)
		if pixels != nil || !errors.Is(err, fontCause) || reader.forbiddenHit != 0 {
			t.Fatal("CFF2 instance Scene replaced the resolver cause or fell back to saved artwork")
		}
	}
	settings.FontResolver = resolver
	readCause := errors.New("author QA CFF2 instance source ReaderAt live cause")
	reader.reader = multiStyleCausalReader{ReaderAt: reader.reader, cause: readCause}
	pixels, err = RenderTile(nil, source, expected.Rect, settings)
	if pixels != nil || !errors.Is(err, readCause) {
		t.Fatalf("CFF2 instance Scene replaced live ReaderAt cause: %v", err)
	}
	_ = source.Close()
	pixels, err = RenderTile(nil, source, expected.Rect, settings)
	if pixels != nil || !errors.Is(err, psdfile.ErrClosed) {
		t.Fatal("closed original CFF2 instance Source returned pixels")
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
		t.Fatalf("recursive CFF2 instance empty ROI source preflight/cache: external=%d err=%v", len(childReaders), err)
	}
	recursiveExpected := cff2AuthorSceneProjected(t, parent, expected, canvas)
	recursiveBaseline := &textSourceCountContext{}
	pixels, err = RenderTile(recursiveBaseline, parent, recursiveExpected.Rect, settings)
	if err != nil {
		t.Fatal(err)
	}
	openTypeSceneEqualWindow(t, pixels, recursiveExpected, recursiveExpected.Rect)
	recursivePolls := recursiveBaseline.calls.Load()
	if recursivePolls < 4 {
		t.Fatal("recursive CFF2 instance control lacks measurable context checks")
	}
	for _, cancelAt := range []int64{1, recursivePolls / 2, recursivePolls - 1, recursivePolls} {
		pixels, err = RenderTile(&textSourceCountContext{cancelAt: cancelAt}, parent, recursiveExpected.Rect, settings)
		if pixels != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("recursive CFF2 instance cancellation returned output at poll %d/%d: %v",
				cancelAt, recursivePolls, err)
		}
	}
	externalCause := errors.New("author QA CFF2 instance external original cause")
	settings.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
		return nil, externalCause
	}
	pixels, err = RenderTile(nil, parent, recursiveExpected.Rect, settings)
	if pixels != nil || !errors.Is(err, externalCause) || parentReader.forbiddenHit != 0 {
		t.Fatal("recursive CFF2 instance replaced live external cause or read saved parent pixels")
	}
	for _, childReader := range childReaders {
		if childReader.forbiddenHit != 0 {
			t.Fatal("recursive CFF2 instance cancellation read saved child pixels")
		}
	}
}
