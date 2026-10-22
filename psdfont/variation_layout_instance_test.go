package psdfont

import (
	"context"
	"encoding/json/v2"
	"math"
	"os"
	"testing"
)

func TestCFF2GPOSFormat2VariationUsesSubtableOrigin(t *testing.T) {
	cff2VerifyPortableFonts(t)
	face, err := Open(nil, "testdata/cff2/fonts/InstancePairFormat2.otf",
		DecodeOptions{Variations: []VariationCoordinate{{Tag: "wght", Value: .5}}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := Shape(nil, face, "AB", ShapeOptions{Features: []ShapeFeature{{Tag: "kern", Enabled: true}}})
	if err != nil || run == nil || len(run.Glyphs) != 2 || run.Glyphs[0].XAdvance != 501 ||
		run.Glyphs[1].XAdvance != 499 || run.Glyphs[0].InstancePosition == nil ||
		run.Glyphs[1].InstancePosition == nil || run.Glyphs[0].InstancePosition.XAdvance != 501 ||
		run.Glyphs[1].InstancePosition.XAdvance != 499 {
		t.Fatalf("PairPosFormat2 offsets must remain relative to PairPos: %+v %v", run, err)
	}
}

func TestCFF2GSUBAlternateAdvancesAndBothLayouts(t *testing.T) {
	cff2VerifyPortableFonts(t)
	data, err := os.ReadFile("testdata/cff2/gsub-inputs-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var oracle struct {
		File  string
		Cases []struct {
			Wght    float64
			Glyph   uint16
			Advance int64
			Precise float64
		}
	}
	if err := json.Unmarshal(data, &oracle, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	for _, row := range oracle.Cases {
		face, err := Open(nil, oracle.File,
			DecodeOptions{Variations: []VariationCoordinate{{Tag: "wght", Value: row.Wght}}})
		if err != nil {
			t.Fatal(err)
		}
		selected := ShapeOptions{Features: []ShapeFeature{{Tag: "liga", Enabled: true}}}
		run, err := Shape(nil, face, "AB", selected)
		if err != nil || run == nil || len(run.Glyphs) != 1 || run.Glyphs[0].GlyphID != row.Glyph ||
			run.Glyphs[0].XAdvance != row.Advance || run.Glyphs[0].InstancePosition == nil ||
			run.Glyphs[0].InstancePosition.XAdvance != row.Precise {
			t.Fatalf("GSUB instance%g run%+v err%v", row.Wght, run, err)
		}
		resolver := ResolverFunc(func(ctx context.Context, name string) (*Face, error) {
			if name != face.PostScriptName {
				return nil, ErrMissingFont
			}
			return face, ctx.Err()
		})
		style := Style{FontName: face.PostScriptName, Size: float64(face.UnitsPerEm)}
		layout, err := LayoutOpenType(nil, "AB", style, resolver, selected)
		if err != nil || layout == nil || len(layout.Glyphs) != 1 ||
			layout.Glyphs[0].Advance != float64(row.Advance) || layout.AdvanceWidth != float64(row.Advance) {
			t.Fatalf("actual instance layout: %v", err)
		}
		legacy, err := Layout(nil, "CD", style, resolver)
		if err != nil || legacy == nil || len(legacy.Glyphs) != 2 {
			t.Fatalf("actual legacy instance layout: %v", err)
		}
		for _, placement := range legacy.Glyphs {
			if placement.Glyph.InstanceMetrics == nil ||
				placement.Advance != math.Floor(placement.Glyph.InstanceMetrics.AdvanceWidth+.5) {
				t.Fatal("legacy layout ignored effective instance advance or rounded twice")
			}
		}
	}
	if len(oracle.Cases) != 4 {
		t.Fatal("four actual GSUB/legacy/OpenType instance controls required")
	}
}
