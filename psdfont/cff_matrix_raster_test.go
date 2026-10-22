package psdfont

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/psdvector"
)

func TestCFFMatrixHarfBuzzKeepsHeadHmtxPlacements(t *testing.T) {
	count := 0
	features := ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: true}, {Tag: "kern", Enabled: true}}}
	for _, row := range cffMatrixOracle(t) {
		face := cffMatrixFace(t, row)
		for _, expected := range row.Shapes {
			shaped, err := Shape(nil, face, expected.Text, features)
			count++
			missing := false
			for _, placement := range expected.Placements {
				missing = missing || placement.ID == 0
			}
			if missing {
				// HarfBuzz emits .notdef; this existing strict API instead reports the unavailable character.
				if shaped != nil || !errors.Is(err, ErrMissingGlyph) {
					t.Fatal("strict missing-character policy changed while matrix metadata was extended")
				}
				continue
			}
			if err != nil || shaped == nil || len(shaped.Glyphs) != len(expected.Placements) {
				t.Fatalf("matrix raw metrics shaping: %v", err)
			}
			for index, actual := range shaped.Glyphs {
				want := expected.Placements[index]
				if actual.GlyphID != want.ID || actual.ClusterStart != want.Cluster ||
					actual.XAdvance != want.XAdvance || actual.YAdvance != want.YAdvance ||
					actual.XOffset != want.XOffset || actual.YOffset != want.YOffset {
					t.Fatal("FontMatrix renormalized head-domain hmtx/GPOS/UTF16 placement")
				}
			}
		}
	}
	if count != 35 {
		t.Fatal("matrix shaping controls were empty or truncated")
	}
}

func TestCFFMatrixBothLayoutsIndependentVectorPixels(t *testing.T) {
	data, err := os.ReadFile("testdata/cff-matrix/raster.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Cases []struct {
			File, SHA256, Text string
			OpenType           bool `json:"open_type"`
			Size, Advance      float64
			Mask               psdvector.Mask
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil || len(oracle.Cases) != 28 {
		t.Fatal("28 independent two-layout raster controls required")
	}
	fonts := make(map[string]cffMatrixOracleFont)
	for _, font := range cffMatrixOracle(t) {
		fonts[font.File] = font
	}
	for _, row := range oracle.Cases {
		font, ok := fonts[row.File]
		if !ok || font.SHA256 != row.SHA256 {
			t.Fatal("raster expected bytes differ from original font")
		}
		face := cffMatrixFace(t, font)
		resolver := ResolverFunc(func(context.Context, string) (*Face, error) { return face, nil })
		style := Style{FontName: face.PostScriptName, Size: row.Size}
		var run *Run
		if row.OpenType {
			run, err = LayoutOpenType(nil, row.Text, style, resolver,
				ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: true}, {Tag: "kern", Enabled: true}}})
		} else {
			run, err = Layout(nil, row.Text, style, resolver)
		}
		if err != nil || run == nil || !cffMatrixNear(run.AdvanceWidth, row.Advance) {
			t.Fatalf("matrix layout advance changed font=%s opentype=%t: %v", row.File, row.OpenType, err)
		}
		region := image.Rect(-80, -150, 420, 80)
		expected, err := psdvector.Rasterize(nil, &row.Mask, region, psdvector.Options{Samples: 4})
		if err != nil {
			t.Fatal(err)
		}
		actual, err := RasterizeRun(nil, run, region, RasterOptions{Samples: 4})
		if err != nil || actual == nil || !bytes.Equal(actual.Pix, expected.Pix) {
			t.Fatalf("matrix raster independent vector pixels differ font=%s opentype=%t: %v",
				row.File, row.OpenType, err)
		}
	}
}
