package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
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

func TestCFFStyleBridgeKnownCombination(t *testing.T) {
	faces := make(map[string]*psdfont.Face)
	for _, name := range []string{"psdkit-CFF1-Name", "psdkit-CFF1-CID3"} {
		face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "cff1", "fonts", name+".otf"))
		if err != nil {
			t.Fatal(err)
		}
		faces[name] = face
	}
	resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		return faces[name], nil
	})
	for _, policy := range []OpenTypeStyleBoundaryPolicy{OpenTypeIsolatedStyleRuns, OpenTypeCompatibleStyleRuns} {
		row := multiStyleOracleCase{Text: "A\U0001df00Vfi", Ends: []int64{3, 7},
			Fonts: []string{"psdkit-CFF1-Name", "psdkit-CFF1-CID3"}, Sizes: []float64{64, 48},
			Colors: [][4]float64{{0.65, 0.15, 0.4, 0.9}, {0.35, 0.9, 0.2, 0.1}},
			Policy: policy, Liga: true, Kern: true, Transform: [6]float64{1, 0.125, 0.25, 1, 18, 52}, Samples: 4}
		source, reader := openTypeSceneSource(t, multiStyleCasePSD(t, row, true), nil)
		records, err := source.TextLayers(nil)
		if err != nil {
			t.Fatal(err)
		}
		pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, multiStyleCaseOptions(row))
		if err != nil || pixels == nil || reader.forbiddenHit != 0 {
			t.Fatalf("multi-style CFF combination policy=%d: %v; saved reads=%d", policy, err, reader.forbiddenHit)
		}
	}
}

type cffStyleBridgeCase struct {
	multiStyleOracleCase
	Paints []struct {
		Color [4]float64
		Mask  psdvector.Mask
	}
}

func cffStyleBridgeOracle(t *testing.T) ([]cffStyleBridgeCase, psdfont.FontResolver) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "cff-style-bridge", "oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts []struct{ File, Name, SHA256 string }
		Cases []cffStyleBridgeCase
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Cases) != 4 {
		t.Fatal("four finite CFF/style intersection cases required")
	}
	faces := make(map[string]*psdfont.Face)
	for _, item := range oracle.Fonts {
		path := filepath.Join("..", item.File)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != item.SHA256 {
			t.Fatal("independent oracle font bytes differ")
		}
		face, err := psdfont.Decode(nil, data)
		if err != nil || face.PostScriptName != item.Name {
			t.Fatalf("independent oracle font identity: %v", err)
		}
		faces[item.Name] = face
	}
	return oracle.Cases, psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		face := faces[name]
		if face == nil {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
}

func cffStyleBridgeExpected(t *testing.T, row cffStyleBridgeCase) *image.NRGBA {
	t.Helper()
	canvas := image.Rect(0, 0, 128, 96)
	var layers []*psdmodel.Layer
	for _, paint := range row.Paints {
		coverage, err := psdvector.Rasterize(nil, &paint.Mask, canvas, psdvector.Options{Samples: row.Samples})
		if err != nil {
			t.Fatal(err)
		}
		pixels := image.NewNRGBA(canvas)
		for index, alpha := range coverage.Pix {
			for channel := range 3 {
				pixels.Pix[index*4+channel] = byte(math.Round(paint.Color[channel+1] * 255))
			}
			pixels.Pix[index*4+3] = byte(math.Round(float64(alpha) * paint.Color[0]))
		}
		layers = append(layers, &psdmodel.Layer{Bounds: canvas, Pixels: pixels, BlendMode: "normal"})
	}
	expected, err := psdcompose.Render(nil, &psdmodel.Document{Width: 128, Height: 96, Layers: layers})
	if err != nil {
		t.Fatal(err)
	}
	return expected
}

func TestCFFStyleBridgeIndependentGeometryPixelsAndSources(t *testing.T) {
	rows, resolver := cffStyleBridgeOracle(t)
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

func TestCFFStyleBridgeRecursivePSDPSBIndependentContents(t *testing.T) {
	rows, resolver := cffStyleBridgeOracle(t)
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

func TestCFFStyleBridgeCubicBoundsBeforeROIEarlyOut(t *testing.T) {
	paint := &openTypePaintRange{run: &psdfont.Run{Scale: 1, Glyphs: []psdfont.PositionedGlyph{{
		Glyph: &psdfont.Glyph{Path: []psdfont.Command{{Op: psdfont.Move, X: 100, Y: 100},
			{Op: psdfont.Cubic, X: 100, Y: 100, ControlX: 110, ControlY: 110, Control2X: -50, Control2Y: 20},
			{Op: psdfont.Close}}}}}}, end: 1}
	bounds, err := openTypePaintBounds(context.Background(), paint, [6]float64{1, 0, 0, 1, 0, 0})
	if err != nil || bounds != image.Rect(-50, -110, 110, -20) {
		t.Fatalf("cubic control hull omitted a control or endpoint: %v, %v", bounds, err)
	}
	for _, control := range []struct {
		x, y float64
		kind error
	}{
		{math.NaN(), 0, psdfile.ErrMalformed}, {0, math.Inf(1), psdfile.ErrMalformed},
		{1 << 41, 0, psdfile.ErrLimit}, {0, -(1 << 41), psdfile.ErrLimit},
	} {
		paint.run.Glyphs[0].X, paint.run.Glyphs[0].Y = 1e6, 1e6
		paint.run.Glyphs[0].Glyph.Path[1].Control2X, paint.run.Glyphs[0].Glyph.Path[1].Control2Y = control.x, control.y
		scales := []float64{1}
		if errors.Is(control.kind, psdfile.ErrMalformed) {
			scales = append(scales, 1e-20)
		}
		for _, scale := range scales {
			paint.run.Scale = scale
			bounds, err := openTypePaintBounds(context.Background(), paint, [6]float64{1, 0, 0, 1, 0, 0})
			if bounds != (image.Rectangle{}) || !errors.Is(err, control.kind) {
				t.Fatalf("invalid off-ROI Control2 hidden by hull/scale: scale=%g err=%v", scale, err)
			}
		}
	}
	// Second and first controls share the same transformed-coordinate domain, including tiny valid scales.
	paint.run.Scale = 1e-20
	paint.run.Glyphs[0].Glyph.Path[1].ControlX = 1 << 41
	paint.run.Glyphs[0].Glyph.Path[1].ControlY = 20
	paint.run.Glyphs[0].Glyph.Path[1].Control2X = 110
	paint.run.Glyphs[0].Glyph.Path[1].Control2Y = 110
	firstHull, err := openTypePaintBounds(context.Background(), paint, [6]float64{1, 0, 0, 1, 0, 0})
	if err != nil {
		t.Fatal("validly rescaled first control was rejected")
	}
	paint.run.Glyphs[0].Glyph.Path[1].ControlX, paint.run.Glyphs[0].Glyph.Path[1].Control2X =
		paint.run.Glyphs[0].Glyph.Path[1].Control2X, paint.run.Glyphs[0].Glyph.Path[1].ControlX
	paint.run.Glyphs[0].Glyph.Path[1].ControlY, paint.run.Glyphs[0].Glyph.Path[1].Control2Y =
		paint.run.Glyphs[0].Glyph.Path[1].Control2Y, paint.run.Glyphs[0].Glyph.Path[1].ControlY
	secondHull, err := openTypePaintBounds(context.Background(), paint, [6]float64{1, 0, 0, 1, 0, 0})
	if err != nil || firstHull != secondHull {
		t.Fatal("second control used a narrower coordinate domain than the first")
	}
	// Old TrueType commands retain zero second controls and preserve the original affine hull.
	paint.run.Scale = 1
	paint.run.Glyphs[0].X, paint.run.Glyphs[0].Y = 0, 0
	paint.run.Glyphs[0].Glyph.Path = []psdfont.Command{{Op: psdfont.Move},
		{Op: psdfont.Quad, X: 10, Y: 20, ControlX: 3, ControlY: 15}, {Op: psdfont.Close}}
	bounds, err = openTypePaintBounds(context.Background(), paint, [6]float64{1, 0, 0, 1, 0, 0})
	if err != nil || bounds != image.Rect(0, -20, 10, 0) {
		t.Fatal("old quadratic hull changed")
	}
}
