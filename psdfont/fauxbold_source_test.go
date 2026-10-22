package psdfont

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFauxBoldSourceHanOptional(t *testing.T) {
	directory := os.Getenv("PSDKIT_FAUXBOLD_FONT_DIR")
	if directory == "" {
		t.Skip("set PSDKIT_FAUXBOLD_FONT_DIR to explicitly supplied Source Han Sans CN fonts")
	}
	faces := make(map[string]*Face)
	resolver := ResolverFunc(func(ctx context.Context, name string) (*Face, error) {
		if face := faces[name]; face != nil {
			return face, nil
		}
		face, err := Open(ctx, filepath.Join(directory, name+".otf"))
		if err == nil {
			faces[name] = face
		}
		return face, err
	})
	// These exact names and words are the supplied template's explicitly selected source artwork.
	for _, test := range []struct {
		name, text string
		size       float64
	}{
		{"SourceHanSansCN-Medium", "INDEPENDENT KEYS, SENSITIVE ACTION", 15},
		{"SourceHanSansCN-Medium", "Original Mold Opening, PRECISE ", 15},
		{"SourceHanSansCN-Medium", "HOLE POSITION", 15},
		{"SourceHanSansCN-Bold", " SOFT SILICONE , EASY TO INSTALL & REMOVE", 17.40793 * 1.1489017680135072},
		{"SourceHanSansCN-Heavy", "DETAIL ", 38},
		{"SourceHanSansCN-Heavy", "DISPLAY", 38},
	} {
		t.Run(test.name+"/"+test.text, func(t *testing.T) {
			run, err := LayoutOpenType(nil, test.text, Style{FontName: test.name, Size: test.size}, resolver,
				ShapeOptions{Features: []ShapeFeature{{Tag: "kern", Enabled: true}, {Tag: "liga", Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			advance := run.AdvanceWidth
			bounds := run.Bounds.Inset(-2)
			plain, err := RasterizeRun(nil, run, bounds)
			if err != nil {
				t.Fatal(err)
			}
			bold, err := RasterizeRun(nil, run, bounds, RasterOptions{FauxBoldRadius: test.size / 48})
			if err != nil {
				t.Fatal(err)
			}
			if run.AdvanceWidth != advance || bytes.Equal(plain.Pix, bold.Pix) {
				t.Fatal("source faux bold must add geometry without changing source advance")
			}
			for _, options := range []RasterOptions{
				{AreaCoverage: true},
				{AreaCoverage: true, FauxBoldRadius: test.size / 48},
				{NoAntialias: true},
				{NoAntialias: true, FauxBoldRadius: test.size / 48},
				{FauxBoldRadius: test.size / 48, FauxBoldAnchored: true},
			} {
				coverage, err := RasterizeRun(nil, run, bounds, options)
				if err != nil || coverage == nil || run.AdvanceWidth != advance {
					t.Fatalf("source coverage mode=%+v changed layout or exceeded default raster limits: %v", options, err)
				}
			}
			t.Logf("glyphs=%d ink=%v radius=%.9f advance=%.9f", len(run.Glyphs), bounds,
				test.size/48, advance)
		})
	}
}
