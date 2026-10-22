package psdfont

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"os"
	"testing"
)

func TestCFF2FullActualStaticAndInstanceGlyph(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/full-glyph-inputs-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		Fonts []struct {
			File string
			Axes []struct {
				Tag                       string
				Minimum, Default, Maximum float64
			}
			Observations []struct {
				Normalized      string
				ID              uint16
				Name            string
				Path            []jsontext.Value
				StoredHmtx      [2]int `json:"stored_hmtx"`
				Coordinates     []VariationCoordinate
				FinalNormalized int16 `json:"final_normalized"`
			}
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range oracle.Fonts {
		for _, observation := range row.Observations {
			options := DecodeOptions{Variations: observation.Coordinates}
			face, err := Open(nil, row.File, options)
			if face == nil || err != nil {
				t.Fatalf("font%s coord%s: %v", row.File, observation.Normalized, err)
			}
			coords, err := face.VariationCoordinates(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, coordinate := range coords {
				if coordinate.Normalized != observation.FinalNormalized {
					t.Fatalf("final coordinate %+v expected%d", coordinate, observation.FinalNormalized)
				}
			}
			glyph, err := face.Glyph(nil, observation.ID)
			if glyph == nil || err != nil {
				t.Fatalf("glyph%s coord%s: %v", observation.Name, observation.Normalized, err)
			}
			if glyph.AdvanceWidth != uint16(observation.StoredHmtx[0]) ||
				glyph.LeftSideBearing != int16(observation.StoredHmtx[1]) || glyph.CharstringWidth != 0 {
				t.Fatal("CFF2 altered raw hmtx or synthesized embedded width")
			}
			if len(glyph.Path) != len(observation.Path) {
				t.Fatalf("pathcount%s=%d/%d", observation.Name, len(glyph.Path), len(observation.Path))
			}
			for index, raw := range observation.Path {
				var command []jsontext.Value
				if err := json.Unmarshal(raw, &command, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				var kind string
				var points [][2]float64
				if err := json.Unmarshal(command[0], &kind, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(command[1], &points, json.MatchCaseInsensitiveNames(true)); err != nil {
					t.Fatal(err)
				}
				expected := Command{}
				switch kind {
				case "moveTo":
					expected = Command{Op: Move, X: points[0][0], Y: points[0][1]}
				case "lineTo":
					expected = Command{Op: Line, X: points[0][0], Y: points[0][1]}
				case "curveTo":
					expected = Command{Op: Cubic, X: points[2][0], Y: points[2][1],
						ControlX: points[0][0], ControlY: points[0][1], Control2X: points[1][0], Control2Y: points[1][1]}
				case "closePath":
					expected.Op = Close
				default:
					t.Fatalf("unexpected primary path op %s", kind)
				}
				actual := glyph.Path[index]
				want := []float64{expected.X, expected.Y,
					expected.ControlX, expected.ControlY, expected.Control2X, expected.Control2Y}
				got := []float64{actual.X, actual.Y, actual.ControlX, actual.ControlY, actual.Control2X, actual.Control2Y}
				if actual.Op != expected.Op {
					t.Fatal("path op differs")
				}
				for field := range want {
					if math.Float64bits(want[field]) != math.Float64bits(got[field]) {
						t.Fatalf("glyph%s coord%s point%d field%d %.17g/%.17g",
							observation.Name, observation.Normalized, index, field, got[field], want[field])
					}
				}
			}
			count++
		}
	}
	if count != 121 {
		t.Fatalf("actual cases%d want121", count)
	}
	t.Logf("112 sharedprimary CFF2 paths plus9 inheritedPrivate Fraction paths")
}
