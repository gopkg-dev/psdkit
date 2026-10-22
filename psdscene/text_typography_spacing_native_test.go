package psdscene

import (
	"context"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

func TestOpenTypeTypographyExplicitKerningDoesNotFollowLegacyAutoKerning(t *testing.T) {
	face, resolver := typographyTestFont(t)
	engine, err := psdtext.Decode(nil, typographyTestEngine(t, "AVA", face.PostScriptName,
		typographyTestSettings{tracking: -25, leading: 56, align: 2}))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Styles[0].Style.AutoKerning == nil || !*metadata.Styles[0].Style.AutoKerning {
		t.Fatal("fixture must retain legacy AutoKerning=true")
	}
	typography, lines, _, err := openTypeTypographyLines(nil, metadata, "AVA", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	// The independent HarfBuzz base-font oracle gives AVA advances 600/600/600 or 520/560/600.
	for _, control := range []struct {
		name    string
		kern    bool
		width   float64
		origins [3]float64
	}{
		{name: "manual", width: 35, origins: [3]float64{-17.5, -6, 5.5}},
		{name: "metrics", kern: true, width: 32.6, origins: [3]float64{-16.3, -6.4, 4.3}},
	} {
		t.Run(control.name, func(t *testing.T) {
			shape := psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "kern", Enabled: control.kern}}}
			runs, _, err := layoutOpenTypePointLines(context.Background(), lines, typography,
				resolver, shape, 10, 8<<20)
			if err != nil || len(runs) != 1 || runs[0] == nil || len(runs[0].Glyphs) != 3 {
				t.Fatalf("source layout: %v", err)
			}
			if math.Abs(runs[0].AdvanceWidth-control.width) > 1e-12 {
				t.Fatalf("width %.17g; want %.17g", runs[0].AdvanceWidth, control.width)
			}
			for index, glyph := range runs[0].Glyphs {
				if math.Abs(glyph.X-control.origins[index]) > 1e-12 || glyph.ClusterStart != int64(index) {
					t.Fatalf("glyph %d: %+v; want origin %.17g", index, glyph, control.origins[index])
				}
			}
		})
	}
}

func TestOpenTypeTypographyCenterIncludesSpacesButNotTerminalEngineMarker(t *testing.T) {
	face, resolver := typographyTestFont(t)
	engine, err := psdtext.Decode(nil, typographyTestEngine(t, " AV ", face.PostScriptName,
		typographyTestSettings{tracking: -25, leading: 56, align: 2}))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := engine.Metadata(nil)
	if err != nil {
		t.Fatal(err)
	}
	typography, lines, _, err := openTypeTypographyLines(nil, metadata, " AV ", 1<<20)
	if err != nil || len(lines) != 1 || lines[0].text != " AV " || lines[0].length != 5 {
		t.Fatalf("source spaces or terminal CR were changed: %v; %v", lines, err)
	}
	shape := psdfont.ShapeOptions{Features: []psdfont.ShapeFeature{{Tag: "kern", Enabled: false}}}
	runs, _, err := layoutOpenTypePointLines(nil, lines, typography, resolver, shape, 10, 8<<20)
	if err != nil || len(runs) != 1 || runs[0] == nil || len(runs[0].Glyphs) != 4 {
		t.Fatalf("source layout: %v", err)
	}
	// Source hmtx is space=250 and A/V=600 at UPM 1000; 20px em and three -0.5px gaps give 32.5px.
	if math.Abs(runs[0].AdvanceWidth-32.5) > 1e-12 || runs[0].UTF16Units != 4 {
		t.Fatalf("spaces or terminal tracking changed advance: %+v", runs[0])
	}
	origins := [4]float64{-16.25, -11.75, -.25, 11.25}
	advances := [4]float64{4.5, 11.5, 11.5, 5}
	characters := [4]rune{' ', 'A', 'V', ' '}
	for index, glyph := range runs[0].Glyphs {
		if glyph.Rune != characters[index] || glyph.ClusterStart != int64(index) || glyph.ClusterLength != 1 ||
			math.Abs(glyph.X-origins[index]) > 1e-12 || math.Abs(glyph.Advance-advances[index]) > 1e-12 {
			t.Fatalf("source whitespace glyph %d: %+v", index, glyph)
		}
	}
}
