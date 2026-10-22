package psdfont

import (
	"encoding/json/v2"
	"errors"
	"math"
	"os"
	"testing"
)

func TestCFF2FixedActualPreciseHVARAndGlyphMetrics(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/fixed-metrics-inputs-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		File  string
		Cases []struct {
			User              map[string]float64
			ID                uint16
			Advance, LSB, RSB float64
			Raw               [2]int
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, row := range oracle.Cases {
		var options DecodeOptions
		for tag, value := range row.User {
			options.Variations = append(options.Variations, VariationCoordinate{Tag: tag, Value: value})
		}
		face, err := Open(nil, oracle.File, options)
		if err != nil || face == nil {
			t.Fatal(err)
		}
		glyph, err := face.Glyph(nil, row.ID)
		if err != nil || glyph == nil || glyph.InstanceMetrics == nil {
			t.Fatalf("glyph%d: %v", row.ID, err)
		}
		actual := glyph.InstanceMetrics
		if math.Float64bits(actual.AdvanceWidth) != math.Float64bits(row.Advance) ||
			math.Float64bits(actual.LeftSideBearing) != math.Float64bits(row.LSB) ||
			math.Float64bits(actual.RightSideBearing) != math.Float64bits(row.RSB) ||
			glyph.AdvanceWidth != uint16(row.Raw[0]) || glyph.LeftSideBearing != int16(row.Raw[1]) {
			t.Fatalf("glyph%d user%v preciseAW/LSB %g/%g expected%g/%g", row.ID, row.User,
				actual.AdvanceWidth, actual.LeftSideBearing, row.Advance, row.LSB)
		}
		if actual.HasInk && actual.OutlineBounds[0] != row.LSB {
			t.Fatal("precise outline and explicit LSBdelta mismatch")
		}
		shortContours, err := face.Glyph(nil, row.ID, GlyphOptions{MaxContours: 1})
		if shortContours != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("primary plus raw-baseline contour total must be shared: %v", err)
		}
		boundedContours, err := face.Glyph(nil, row.ID, GlyphOptions{MaxContours: 2})
		if boundedContours == nil || err != nil {
			t.Fatalf("exact shared contour count: %v", err)
		}
		for _, delta := range []int64{-1, 0, 1} {
			bounded, err := face.Glyph(nil, row.ID, GlyphOptions{MaxWorkingBytes: glyph.WorkingBytes() + delta})
			if delta < 0 {
				if bounded != nil || !errors.Is(err, ErrLimit) {
					t.Fatal("one-short glyph metrics workspace accepted")
				}
			} else if bounded == nil || err != nil {
				t.Fatalf("exact metricsworkspace:%v", err)
			}
		}
	}
	if len(oracle.Cases) != 110 {
		t.Fatal("110realFractioncontrolsrequired")
	}
	t.Log("110 fixed-profile Fraction AW/LSB/RSB controls; rawhmtx preserved; current+default sharedbudget ±1")
}
