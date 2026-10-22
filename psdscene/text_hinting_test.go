package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfont"
)

func TestOpenTypeHintingSourceCoverageAndAbsoluteROIs(t *testing.T) {
	face, err := psdfont.Open(nil,
		filepath.Join("..", "psdfont", "testdata", "cff1", "fonts", "psdkit-CFF1-Name.otf"))
	if err != nil {
		t.Fatal(err)
	}
	resolver := psdfont.ResolverFunc(func(_ context.Context, name string) (*psdfont.Face, error) {
		if name != face.PostScriptName {
			return nil, psdfont.ErrMissingFont
		}
		return face, nil
	})
	text := "A V\rVA"
	fixture := openTypeSceneWrongSavedBounds(t, openTypeTestPSDExtra(t, text, face.PostScriptName, 20,
		true, "", typographyTestEngine(t, text, face.PostScriptName,
			typographyTestSettings{tracking: -25, leading: 24, align: 2, bold: true})))
	before := sha256.Sum256(fixture)
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	baseline := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true}
	unhinted, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, baseline)
	if err != nil {
		t.Fatal(err)
	}
	explicit := baseline
	explicit.Hinting = OpenTypeTextHintNone
	legacy, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, explicit)
	if err != nil || !bytes.Equal(unhinted.Pix, legacy.Pix) {
		t.Fatalf("explicit None must retain default source pixels: %v", err)
	}
	for _, control := range []struct {
		mode     OpenTypeTextHinting
		resolved bool
		quarterX bool
	}{
		{mode: OpenTypeTextHintVertical}, {mode: OpenTypeTextHintFull},
		{mode: OpenTypeTextHintFractional}, {mode: OpenTypeTextHintEdgeFractional},
		{mode: OpenTypeTextHintEdgeFractional, resolved: true},
		{mode: OpenTypeTextHintEdgeFractional, resolved: true, quarterX: true},
	} {
		options := baseline
		options.Hinting = control.mode
		options.HintSnapBaselineXQuarter = control.quarterX
		options.FauxBold, options.FauxBoldQuantized = OpenTypeFauxBoldExpandedAdvance, true
		options.AreaCoverage, options.AreaSamples, options.CoverageLevels16 = true, 4, true
		if control.resolved {
			options.HintBluePolicy, options.HintSnapBaselineY = OpenTypeTextBlueResolvedOutward, true
			options.AreaSamples = 64
		}
		full, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
		if err != nil {
			t.Fatal(err)
		}
		painted := 0
		for index := 3; index < len(full.Pix); index += 4 {
			if full.Pix[index]%17 != 0 {
				t.Fatalf("source opacity one must preserve sixteen coverage levels: %d", full.Pix[index])
			}
			if full.Pix[index] != 0 {
				painted++
			}
		}
		if painted == 0 {
			t.Fatal("CFF1 source produced no live glyphs")
		}
		for _, bounds := range []image.Rectangle{
			image.Rect(25, 15, 64, 47), image.Rect(52, 36, 91, 75), image.Rect(49, 17, 50, 75),
		} {
			options.Bounds = &bounds
			crop, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options)
			if err != nil {
				t.Fatal(err)
			}
			openTypeSceneEqualWindow(t, crop, full, bounds)
		}
	}
	if reader.forbiddenHit != 0 || before != sha256.Sum256(fixture) {
		t.Fatal("hinting consumed saved glyph artwork or modified source metadata")
	}
}

func TestOpenTypeHintingRequiresExplicitSupportedSourceAndLattice(t *testing.T) {
	face, resolver := typographyTestFont(t)
	text := "AV"
	fixture := openTypeTestPSDExtra(t, text, face.PostScriptName, 20, true, "",
		typographyTestEngine(t, text, face.PostScriptName, typographyTestSettings{leading: 24}))
	source, reader := openTypeSceneSource(t, fixture, nil)
	records, err := source.TextLayers(nil)
	if err != nil {
		t.Fatal(err)
	}
	options := OpenTypeTextRenderOptions{Kerning: OpenTypeTextNone, ApproximateTypography: true,
		Hinting: OpenTypeTextHintVertical}
	if pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], resolver, options); pixels != nil ||
		!errors.Is(err, psdfont.ErrUnsupported) {
		t.Fatalf("TrueType instructions must not silently select a CFF hinter: %v", err)
	}
	for _, invalid := range []OpenTypeTextRenderOptions{
		{Kerning: OpenTypeTextNone, Hinting: OpenTypeTextHintVertical},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, Hinting: 255},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, AreaSamples: 4},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, HintGrid: psdfont.CFFHintCenters},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, FauxBoldQuantized: true},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, HintBluePolicy: OpenTypeTextBlueResolvedOutward},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, Hinting: OpenTypeTextHintVertical,
			HintBluePolicy: OpenTypeTextBlueResolvedOutward},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, Hinting: OpenTypeTextHintEdgeFractional,
			HintSnapBaselineY: true, HintAnchor: psdfont.CFFHintBaseline},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, HintSnapBaselineY: true},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, HintSnapBaselineXQuarter: true},
		{Kerning: OpenTypeTextNone, ApproximateTypography: true, Hinting: OpenTypeTextHintFull,
			HintSnapBaselineXQuarter: true},
	} {
		calls := 0
		counted := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
			calls++
			return resolver.ResolveFont(ctx, name)
		})
		pixels, err := RenderOpenTypeTextLayer(nil, source, records[0], counted, invalid)
		if pixels != nil || err == nil || calls != 0 {
			t.Fatalf("invalid hint policy reached font resolution: %v", err)
		}
	}
	if reader.forbiddenHit != 0 {
		t.Fatal("rejected hinting request consumed saved glyphs")
	}
}
