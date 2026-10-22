package psdfont

import (
	"encoding/json/v2"
	"math"
	"os"
	"testing"
)

func TestCFF2Avar2ActualPrimaryCoordinatesAndGlyph(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/avar2-inputs-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Cases []struct {
			File, Label  string
			Coordinates  []VariationCoordinate
			Normalized   []int16
			FirstX       float64 `json:"first_x"`
			ShapeAdvance int64   `json:"shape_advance"`
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, row := range oracle.Cases {
		face, err := Open(nil, row.File, DecodeOptions{Variations: row.Coordinates})
		if err != nil {
			t.Fatalf("%s %s: %v", row.File, row.Label, err)
		}
		coordinates, err := face.VariationCoordinates(nil)
		if err != nil || len(coordinates) != len(row.Normalized) {
			t.Fatalf("coordinates: %v", err)
		}
		for axis, coordinate := range coordinates {
			if coordinate.Normalized != row.Normalized[axis] {
				t.Fatalf("%s/%s axis%d %+v expected%d", row.File, row.Label,
					axis, coordinate, row.Normalized[axis])
			}
		}
		id, err := face.LookupRune(nil, 'A')
		if err != nil {
			t.Fatal(err)
		}
		glyph, err := face.Glyph(nil, id)
		if err != nil || glyph == nil || len(glyph.Path) == 0 {
			t.Fatalf("nonempty actual glyph: %v", err)
		}
		if math.Float64bits(glyph.Path[0].X) != math.Float64bits(row.FirstX) {
			t.Fatalf("%s/%s firstx%g expected%g", row.File, row.Label, glyph.Path[0].X, row.FirstX)
		}
		run, err := Shape(nil, face, "A")
		if err != nil || run == nil || len(run.Glyphs) != 1 || run.Glyphs[0].XAdvance != row.ShapeAdvance {
			t.Fatalf("%s/%s shape=%+v err%v expected%d", row.File, row.Label, run, err, row.ShapeAdvance)
		}
	}
	if len(oracle.Cases) != 176 {
		t.Fatal("176 shared primary instances required")
	}
	face, err := Open(nil, "testdata/cff2/fonts/Avar2ConstantDefaultShift.otf")
	if err != nil {
		t.Fatal(err)
	}
	coords, err := face.VariationCoordinates(nil)
	if err != nil || len(coords) != 2 || coords[0].Normalized != 2048 || coords[1].Normalized != 1024 {
		t.Fatalf("omitted request must apply constant default shift: %+v %v", coords, err)
	}
}
