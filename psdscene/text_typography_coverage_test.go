package psdscene

import (
	"bytes"
	"image"
	"testing"
)

func TestOpenTypeTypographyCoverageUsesSourceOutlinesAndAbsoluteROI(t *testing.T) {
	face, resolver := typographyTestFont(t)
	text := " AV \rVA"
	fixture := openTypeSceneWrongSavedBounds(t, openTypeTestPSDExtra(t, text, face.PostScriptName, 20,
		true, "", typographyTestEngine(t, text, face.PostScriptName,
			typographyTestSettings{tracking: -25, leading: 24, align: 2, bold: true})))
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
	baseline, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, control := range []struct {
		name   string
		area   bool
		binary bool
	}{
		{name: "legacyGrid"}, {name: "horizontalArea", area: true}, {name: "pixelCenters", binary: true},
	} {
		t.Run(control.name, func(t *testing.T) {
			options := legacy
			options.AreaCoverage, options.NoAntialias = control.area, control.binary
			full, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(full.Pix, baseline.Pix) == (control.area || control.binary) {
				t.Fatal("explicit coverage was ignored or default grid pixels changed")
			}
			painted := 0
			for index := 3; index < len(full.Pix); index += 4 {
				alpha := full.Pix[index]
				if alpha != 0 {
					painted++
				}
				if control.binary && alpha != 0 && alpha != 255 {
					t.Fatalf("unhinted pixel-center coverage contains intermediate alpha %d", alpha)
				}
			}
			if painted == 0 {
				t.Fatal("live source produced no glyph pixels")
			}
			for _, bounds := range []image.Rectangle{image.Rect(25, 15, 64, 47), image.Rect(52, 36, 91, 75)} {
				options.Bounds = &bounds
				crop, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
				if err != nil {
					t.Fatal(err)
				}
				openTypeSceneEqualWindow(t, crop, full, bounds)
			}
		})
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("coverage selection read saved glyph artwork")
	}
}
