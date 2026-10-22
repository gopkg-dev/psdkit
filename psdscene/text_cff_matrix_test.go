package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
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
)

func cffMatrixStyleOracle(t *testing.T) ([]cffStyleBridgeCase, psdfont.FontResolver) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cff-matrix", "oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts []struct{ File, Name, SHA256 string }
		Cases []cffStyleBridgeCase
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Cases) != 4 {
		t.Fatalf("four independent affine matrix/style controls required: %v", err)
	}
	faces := make(map[string]*psdfont.Face)
	for _, row := range oracle.Fonts {
		data, err := os.ReadFile(filepath.Join("..", row.File))
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != row.SHA256 {
			t.Fatal("matrix font original SHA differs")
		}
		face, err := psdfont.Decode(nil, data)
		if err != nil || face == nil || face.PostScriptName != row.Name {
			t.Fatalf("matrix font: %v", err)
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

func TestCFFMatrixStyleIndependentGeometryPixelsAndSources(t *testing.T) {
	rows, resolver := cffMatrixStyleOracle(t)
	for _, row := range rows {
		t.Run(row.Label, func(t *testing.T) {
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
				Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}, {Tag: "kern", Enabled: true}}},
				10000, 32<<20)
			if err != nil {
				t.Fatal(err)
			}
			observed := 0
			for _, span := range laidOut {
				for _, glyph := range span.run.Glyphs {
					want := row.Glyphs[observed]
					if glyph.Glyph.ID != want.ID || span.run.FontName != want.Font ||
						glyph.ClusterStart != want.Start || glyph.ClusterLength != want.Length ||
						math.Abs(glyph.X-want.X) > 1e-12 || math.Abs(glyph.Y-want.Y) > 1e-12 ||
						math.Abs(glyph.Advance-want.Advance) > 1e-12 || math.Abs(span.run.Scale-want.Scale) > 1e-12 {
						t.Fatalf("independent HB glyph %d differs", observed)
					}
					observed++
				}
			}
			if observed != len(row.Glyphs) {
				t.Fatal("independent HB glyph count differs")
			}
			expected := cffStyleBridgeExpected(t, row)
			for _, modern := range []bool{false, true} {
				fixture := multiStyleCasePSD(t, row.multiStyleOracleCase, modern)
				before := sha256.Sum256(fixture)
				source, reader := openTypeSceneSource(t, fixture, nil)
				records, err := source.TextLayers(nil)
				if err != nil {
					t.Fatal(err)
				}
				policy := multiStyleCaseOptions(row.multiStyleOracleCase)
				direct, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
				if err != nil || !bytes.Equal(direct.Pix, expected.Pix) {
					t.Fatalf("independent fontTools-vector direct pixels differ: modern=%t: %v", modern, err)
				}
				full, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
					CachedSpecialLayers: true})
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, full, expected, expected.Rect)
				options := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver, CachedSpecialLayers: true}
				for _, region := range []image.Rectangle{image.Rect(8, 28, 80, 70), image.Rect(66, 30, 128, 80)} {
					actual, err := RenderTile(nil, source, region, options)
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
				}, options)
				if err != nil || visited != 12 || reader.forbiddenHit != 0 || sha256.Sum256(fixture) != before {
					t.Fatalf("CFF/style source visitor/cache/ownership: modern=%t: %v", modern, err)
				}
			}
		})
	}
}

func TestCFFMatrixStyleRecursivePSDPSBIndependentContents(t *testing.T) {
	rows, resolver := cffMatrixStyleOracle(t)
	for _, row := range rows[:1] {
		prototype := cffStyleBridgeExpected(t, row)
		childData := multiStyleCasePSD(t, row.multiStyleOracleCase, true)
		canvas := image.Pt(300, 180)
		quad := []float64{20, 10, 276, 10, 276, 138, 20, 138}
		for _, psb := range []bool{false, true} {
			contents := childData
			if psb {
				contents = recursiveTextPSB(t, contents)
			}
			for _, external := range []bool{false, true} {
				parent, parentReader := recursiveTextOriginal(t,
					recursiveTextParentFixture(t, contents, external, canvas, quad))
				policy := multiStyleCaseOptions(row.multiStyleOracleCase)
				options := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver,
					CachedSpecialLayers: true, CachedPlacedLayers: true}
				var childReader *openTypeSceneReader
				if external {
					options.ExternalResolver = func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
						child, reader := openTypeSceneSource(t, contents, nil)
						childReader = reader
						return child, nil
					}
				}
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
				for _, region := range []image.Rectangle{expected.Rect, image.Rect(23, 19, 87, 95)} {
					actual, err := RenderTile(nil, parent, region, options)
					if err != nil {
						t.Fatal(err)
					}
					openTypeSceneEqualWindow(t, actual, expected, region)
				}
				visited := 0
				err = VisitTiles(nil, parent, expected.Rect, image.Pt(100, 60), func(tile *image.NRGBA) error {
					visited++
					openTypeSceneEqualWindow(t, tile, expected, tile.Rect)
					return nil
				}, options)
				if err != nil || visited != 9 || parentReader.forbiddenHit != 0 ||
					childReader != nil && childReader.forbiddenHit != 0 {
					t.Fatalf("CFF multi-style recursive source psb=%t external=%t: %v", psb, external, err)
				}
			}
		}
	}
}
