package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestTextAffineCubicPreservesBothControls(t *testing.T) {
	glyph := &psdfont.Glyph{Path: []psdfont.Command{{Op: psdfont.Move},
		{Op: psdfont.Cubic, X: 10, Y: 20, ControlX: 2, ControlY: 7, Control2X: 8, Control2Y: 13},
		{Op: psdfont.Close}}}
	run := &psdfont.Run{Scale: 2, Glyphs: []psdfont.PositionedGlyph{{X: 5, Y: 11, Glyph: glyph}}}
	result, _, err := textAffineRun(context.Background(), run, [6]float64{1, 0.2, 0.3, 1, 4, 6}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	actual := result.Glyphs[0].Glyph.Path[1]
	values := [6]float64{actual.X, actual.Y, actual.ControlX, actual.ControlY, actual.Control2X, actual.Control2Y}
	want := [6]float64{20.3, 18, 12.1, -4.8, 20.5, 4.8}
	for index, value := range values {
		if math.Abs(value-want[index]) > 1e-12 {
			t.Fatalf("affine cubic component %d: %v differs from %v", index, value, want[index])
		}
	}
	if glyph.Path[1].Control2X != 8 || glyph.Path[1].Control2Y != 13 {
		t.Fatal("source cubic was mutated")
	}
}

func TestCFFSceneDirectFullTileVisitorIndependentOracle(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "psdfont", "testdata", "cff1", "oracle.json"))
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts []struct {
			File   string
			Glyphs []struct {
				ID   uint16
				Path []struct {
					Op     string
					Points [][]float64
				}
			}
			Shapes []struct {
				Text       string
				Placements []struct {
					ID       uint16
					XAdvance float64 `json:"x_advance"`
					XOffset  float64 `json:"x_offset"`
					YOffset  float64 `json:"y_offset"`
				}
			}
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	canvas := image.Rect(0, 0, 128, 96)
	var exports []map[string]any
	for _, font := range oracle.Fonts {
		t.Run(font.File, func(t *testing.T) {
			face, err := psdfont.Open(nil, filepath.Join("..", "psdfont", "testdata", "cff1", "fonts", font.File))
			if err != nil {
				t.Fatal(err)
			}
			shape := font.Shapes[1]
			expectedRun := &psdfont.Run{Scale: 64 / float64(face.UnitsPerEm)}
			penX := 20.0
			for _, placement := range shape.Placements {
				glyph := &psdfont.Glyph{}
				for _, point := range font.Glyphs[placement.ID].Path {
					command := psdfont.Command{}
					switch point.Op {
					case "moveTo":
						command.Op = psdfont.Move
					case "lineTo":
						command.Op = psdfont.Line
					case "curveTo":
						command.Op = psdfont.Cubic
						command.ControlX, command.ControlY = point.Points[0][0], point.Points[0][1]
						command.Control2X, command.Control2Y = point.Points[1][0], point.Points[1][1]
					case "closePath":
						command.Op = psdfont.Close
					default:
						t.Fatal("independent oracle has unsupported operation")
					}
					if command.Op != psdfont.Close {
						target := point.Points[len(point.Points)-1]
						command.X, command.Y = target[0], target[1]
					}
					glyph.Path = append(glyph.Path, command)
				}
				expectedRun.Glyphs = append(expectedRun.Glyphs, psdfont.PositionedGlyph{
					X: penX + placement.XOffset*expectedRun.Scale, Y: 50 - placement.YOffset*expectedRun.Scale, Glyph: glyph})
				penX += placement.XAdvance * expectedRun.Scale
			}
			coverage, err := psdfont.RasterizeRun(nil, expectedRun, canvas)
			if err != nil {
				t.Fatal(err)
			}
			artwork := image.NewNRGBA(canvas)
			for index, alpha := range coverage.Pix {
				artwork.Pix[index*4], artwork.Pix[index*4+1], artwork.Pix[index*4+2] = 102, 51, 204
				artwork.Pix[index*4+3] = alpha
			}
			expected, err := psdcompose.Render(nil, &psdmodel.Document{Width: 128, Height: 96,
				Layers: []*psdmodel.Layer{{Bounds: canvas, Pixels: artwork, BlendMode: "normal"}}})
			if err != nil {
				t.Fatal(err)
			}
			fixture := openTypeSceneWrongSavedBounds(t, openTypeTestPSD(t, shape.Text, face.PostScriptName, 64, true))
			fixtureDigest := sha256.Sum256(fixture)
			source, reader := openTypeSceneSource(t, fixture, nil)
			resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
				if name != face.PostScriptName {
					return nil, psdfont.ErrMissingFont
				}
				return face, nil
			})
			policy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextMetrics,
				Shape: psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "liga", Enabled: true}}}}
			records, err := source.TextLayers(nil)
			if err != nil || len(records) != 1 {
				t.Fatalf("source CFF text records: %v", err)
			}
			direct, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, policy)
			if err != nil || !bytes.Equal(direct.Pix, artwork.Pix) {
				t.Fatalf("direct CFF source outline differs from independent oracle: %v", err)
			}
			full, err := Render(nil, source, Options{ApproximateOpenTypeText: &policy, FontResolver: resolver,
				CachedSpecialLayers: true})
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, full, expected, canvas)
			tileOptions := TileOptions{ApproximateOpenTypeText: &policy, FontResolver: resolver, CachedSpecialLayers: true}
			for _, window := range []image.Rectangle{image.Rect(16, 32, 95, 65), image.Rect(64, 35, 128, 70)} {
				actual, err := RenderTile(nil, source, window, tileOptions)
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, actual, expected, window)
			}
			visited := 0
			err = VisitTiles(nil, source, canvas, image.Pt(37, 29), func(tile *image.NRGBA) error {
				openTypeSceneEqualWindow(t, tile, expected, tile.Rect)
				visited++
				return nil
			}, tileOptions)
			if err != nil || visited != 16 || reader.forbiddenHit != 0 || sha256.Sum256(fixture) != fixtureDigest {
				t.Fatalf("CFF source full/ROI/visitor read cache or changed source: %v", err)
			}
			if directory := os.Getenv("PSDKIT_CFF_EXPORT"); directory != "" {
				if err := os.MkdirAll(directory, 0755); err != nil {
					t.Fatal(err)
				}
				input := filepath.Join(directory, font.File+".psd")
				if err := os.WriteFile(input, fixture, 0600); err != nil {
					t.Fatal(err)
				}
				for suffix, pixels := range map[string]*image.NRGBA{"-expected.png": expected, "-direct.png": artwork} {
					file, err := os.Create(filepath.Join(directory, font.File+suffix))
					if err != nil {
						t.Fatal(err)
					}
					if err := png.Encode(file, pixels); err != nil {
						_ = file.Close()
						t.Fatal(err)
					}
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
				pixelDigest := sha256.Sum256(expected.Pix)
				directDigest := sha256.Sum256(artwork.Pix)
				exports = append(exports, map[string]any{"font_file": font.File, "source_file": input,
					"expected_file": font.File + "-expected.png", "direct_file": font.File + "-direct.png",
					"source_sha256": hex.EncodeToString(fixtureDigest[:]), "rgba_sha256": hex.EncodeToString(pixelDigest[:]),
					"direct_rgba_sha256": hex.EncodeToString(directDigest[:]), "saved_artwork_reads": reader.forbiddenHit})
			}
		})
	}
	if directory := os.Getenv("PSDKIT_CFF_EXPORT"); directory != "" {
		data, err := json.Marshal(exports, jsontext.WithIndent("  "), json.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "manifest.json"), append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
