package psdfont

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"os"
	"testing"
)

func TestShapeHorizontalYAdvanceGeometryOracle(t *testing.T) {
	data, err := os.ReadFile("testdata/opentype/geometry_shape_oracle.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Cases []shapeOracleCase `json:"cases"`
		Fonts []struct {
			File string `json:"file"`
			SHA  string `json:"sha256"`
		} `json:"fonts"`
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	if len(oracle.Cases) != 56 {
		t.Fatal("expected independent 56-case geometry shape oracle")
	}
	fontData, err := os.ReadFile("testdata/opentype/fonts/geometry-y-position.ttf")
	if err != nil {
		t.Fatal(err)
	}
	if len(oracle.Fonts) != 1 {
		t.Fatal("expected one independently owned geometry font")
	}
	fontSHA := sha256.Sum256(fontData)
	if hex.EncodeToString(fontSHA[:]) != oracle.Fonts[0].SHA {
		t.Fatal("font bytes do not match frozen HB oracle")
	}
	face := shapeTestFace(t, "testdata/opentype/fonts/geometry-y-position.ttf")
	for _, row := range oracle.Cases {
		t.Run(row.ID, func(t *testing.T) {
			run, err := Shape(nil, face, row.Text, ShapeOptions{
				Script: row.Script, Language: row.Language, Features: row.Features})
			if err != nil {
				t.Fatal(err)
			}
			if run.UTF16Units != row.UTF16Units || len(run.Glyphs) != len(row.Glyphs) {
				t.Fatal("shape span/count differs")
			}
			for i, g := range run.Glyphs {
				actual := shapeOracleGlyph{GlyphID: g.GlyphID, ClusterStart: g.ClusterStart, ClusterLength: g.ClusterLength,
					XAdvance: g.XAdvance, YAdvance: g.YAdvance, XOffset: g.XOffset, YOffset: g.YOffset}
				if actual != row.Glyphs[i] {
					t.Fatalf("glyph %d actual %+v expected %+v", i, actual, row.Glyphs[i])
				}
				if g.YAdvance != 0 {
					t.Fatal("horizontal Shape applied vertical-only advance")
				}
			}
		})
	}
}
