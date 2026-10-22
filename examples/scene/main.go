// Example scene rebuilds an RGB8 PSD/PSB including its embedded placed PSD/PSB documents.
package main

import (
	"context"
	"flag"
	"fmt"
	"image/png"
	"os"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdfont"
)

type textHintSettings struct {
	bluePolicy   string
	snapY        bool
	snapXQuarter bool
	areaSamples  int
}

func main() {
	input := flag.String("psd", "", "PSD/PSB source")
	output := flag.String("out", "", "owned encoded-RGB PNG preview")
	cached := flag.Bool("cached-special-layers", false, "allow saved text, gradient, pattern and stroke pixels")
	cachedText := flag.Bool("cached-text-layers", false, "explicitly use saved typography pixels only")
	vectorStroke := flag.Bool("approximate-vector-stroke", false, "explicitly draw supported live solid inside strokes")
	customWarp := flag.Bool("approximate-custom-warp", false,
		"explicitly project certified custom warp with the mathematical model")
	font := flag.String("font", "", "explicit TrueType/TTC font for approximate Latin text")
	var openTypeFonts []string
	flag.Func("opentype-font", "explicit OpenType font path; repeat for each exact PostScript face",
		func(path string) error {
			openTypeFonts = append(openTypeFonts, path)
			return nil
		})
	typography := flag.Bool("approximate-typography", false,
		"explicitly render supported point paragraphs, tracking, capitalization and faux bold from font outlines")
	expandedBold := flag.Bool("expanded-faux-bold", false,
		"explicitly add synthetic glyph advances and anchored outline expansion to source faux bold")
	textCoverage := flag.String("text-coverage", "grid", "unhinted text coverage: grid, area or pixel-center")
	textSamples := flag.Int("text-samples", 8, "grid samples per axis: 4 or 8")
	textHinting := flag.String("text-hinting", "none", "explicit CFF1 fitting: none, vertical, full, fractional or edge")
	quantizedBold := flag.Bool("quantized-faux-bold", false, "round synthetic extent to half output pixels")
	bluePolicy := flag.String("text-blue-policy", "grid", "CFF1 blue zones: grid or resolved-outward")
	snapY := flag.Bool("text-snap-baseline-y", false, "round image Y baseline before fractional fitting")
	snapXQuarter := flag.Bool("text-snap-baseline-x-quarter", false,
		"explicitly round device glyph X baselines to quarter pixels before fractional fitting")
	areaSamples := flag.Int("text-area-samples", 0, "area coverage strata: 4, 8, 16 or 64; requires text-coverage area")
	kerning := flag.String("text-kerning", "", "explicit approximate text policy: none or metrics")
	filters := flag.Bool("approximate-gaussian-smart-filters", false,
		"explicitly execute approximate live Gaussian smart filters")
	templateFilters := flag.Bool("approximate-template-smart-filters", false,
		"explicitly execute approximate surface blur, smart sharpen and color smart filters")
	budget := flag.Int64("max-working-bytes", 256<<20, "bound retained source and rendering working storage")
	shadows := flag.Bool("approximate-drop-shadow", false,
		"explicitly render live placed-layer drop shadows with the generic approximation")
	placedOverlay := flag.Bool("approximate-placed-color-overlay", false,
		"explicitly compose color overlay on freshly projected original placed contents")
	projection := flag.Bool("approximate-placed-projection", false,
		"explicitly sample transparent-border placed contents with full-canvas bilinear projection")
	assumeSRGB := flag.Bool("assume-untagged-srgb", false,
		"explicitly assign sRGB to untagged input during a required profile conversion")
	flag.Parse()
	if *input == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "-psd and -out are required")
		os.Exit(2)
	}
	if err := run(*input, *output, *cached, *cachedText, *vectorStroke, *customWarp,
		*font, openTypeFonts, *typography, *expandedBold, *textCoverage, *textSamples, *textHinting, *quantizedBold,
		textHintSettings{bluePolicy: *bluePolicy, snapY: *snapY,
			snapXQuarter: *snapXQuarter, areaSamples: *areaSamples},
		*kerning, *filters, *templateFilters,
		*shadows, *placedOverlay, *projection, *assumeSRGB, *budget); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(
	input, output string, cached, cachedText, vectorStroke, customWarp bool, fontPath string,
	openTypeFonts []string, typography, expandedBold bool, textCoverage string, textSamples int,
	textHinting string, quantizedBold bool, hintSettings textHintSettings, kerning string,
	filters, templateFilters, shadows, placedOverlay, projection, assumeSRGB bool, budget int64,
) error {
	configuration := psdkit.SceneOptions{CachedSpecialLayers: cached,
		CachedTextLayers: cachedText, ApproximateVectorStroke: vectorStroke,
		ApproximateCustomWarp: customWarp,
		AssumeUntaggedSRGB:    assumeSRGB, MaxWorkingBytes: budget}
	if textCoverage != "grid" && textCoverage != "area" && textCoverage != "pixel-center" ||
		textSamples != 4 && textSamples != 8 {
		return fmt.Errorf("choose -text-coverage grid|area|pixel-center and -text-samples 4|8")
	}
	if (expandedBold || textCoverage != "grid") && !typography {
		return fmt.Errorf("expanded faux bold and explicit coverage require -approximate-typography")
	}
	hinting := psdkit.OpenTypeTextHintNone
	switch textHinting {
	case "none":
	case "vertical":
		hinting = psdkit.OpenTypeTextHintVertical
	case "full":
		hinting = psdkit.OpenTypeTextHintFull
	case "fractional":
		hinting = psdkit.OpenTypeTextHintFractional
	case "edge":
		hinting = psdkit.OpenTypeTextHintEdgeFractional
	default:
		return fmt.Errorf("choose -text-hinting none|vertical|full|fractional|edge")
	}
	if hinting != psdkit.OpenTypeTextHintNone && !typography || quantizedBold && (!typography || !expandedBold) {
		return fmt.Errorf("hinting requires approximate typography; quantized faux bold also requires expanded faux bold")
	}
	blue := psdkit.OpenTypeTextBlueGrid
	switch hintSettings.bluePolicy {
	case "grid":
	case "resolved-outward":
		blue = psdkit.OpenTypeTextBlueResolvedOutward
	default:
		return fmt.Errorf("choose -text-blue-policy grid|resolved-outward")
	}
	if (blue != psdkit.OpenTypeTextBlueGrid || hintSettings.snapY || hintSettings.snapXQuarter) &&
		hinting < psdkit.OpenTypeTextHintFractional {
		return fmt.Errorf("blue-zone refinement and baseline snapping require fractional CFF fitting")
	}
	if hintSettings.areaSamples != 0 && (textCoverage != "area" || hintSettings.areaSamples != 4 &&
		hintSettings.areaSamples != 8 && hintSettings.areaSamples != 16 && hintSettings.areaSamples != 64) {
		return fmt.Errorf("explicit area strata require -text-coverage area and 4, 8, 16 or 64 samples")
	}
	configuration.Compose.ApproximateDropShadow = shadows
	configuration.Compose.ApproximatePlacedColorOverlay = placedOverlay
	configuration.ApproximatePlacedProjection = projection
	if filters || templateFilters {
		configuration.SmartFilters = &psdkit.SmartFilterOptions{
			ApproximateGaussian: filters, ApproximateSurfaceBlur: templateFilters,
			ApproximateSmartSharpen: templateFilters, ApproximateBrightnessContrast: templateFilters,
			ApproximateHueSaturation: templateFilters,
		}
	}
	if len(openTypeFonts) != 0 || typography {
		if fontPath != "" || len(openTypeFonts) == 0 || kerning != "none" && kerning != "metrics" {
			return fmt.Errorf("OpenType text requires -opentype-font and -text-kerning none|metrics")
		}
		faces := make(map[string]*psdkit.FontFace, len(openTypeFonts))
		for _, path := range openTypeFonts {
			face, err := psdkit.OpenFont(context.Background(), path)
			if err != nil {
				return err
			}
			if face.PostScriptName == "" || faces[face.PostScriptName] != nil {
				return fmt.Errorf("explicit fonts require unique nonempty PostScript names")
			}
			faces[face.PostScriptName] = face
		}
		configuration.FontResolver = psdkit.FontResolverFunc(
			func(ctx context.Context, name string) (*psdkit.FontFace, error) {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				face := faces[name]
				if face == nil {
					return nil, psdfont.ErrMissingFont
				}
				return face, nil
			})
		policy := psdkit.OpenTypeTextNone
		if kerning == "metrics" {
			policy = psdkit.OpenTypeTextMetrics
		}
		configuration.ApproximateOpenTypeText = &psdkit.OpenTypeTextRenderOptions{
			Kerning: policy, ApproximateTypography: typography, Samples: textSamples,
			AreaCoverage: textCoverage == "area", NoAntialias: textCoverage == "pixel-center",
			Hinting: hinting, FauxBoldQuantized: quantizedBold,
			HintBluePolicy: blue, HintSnapBaselineY: hintSettings.snapY, AreaSamples: hintSettings.areaSamples,
			HintSnapBaselineXQuarter: hintSettings.snapXQuarter,
		}
		if expandedBold {
			configuration.ApproximateOpenTypeText.FauxBold = psdkit.OpenTypeFauxBoldExpandedAdvance
		}
	} else if fontPath != "" || kerning != "" {
		if fontPath == "" || kerning != "none" && kerning != "metrics" {
			return fmt.Errorf("approximate text requires -font and -text-kerning none|metrics")
		}
		face, err := psdkit.OpenFont(context.Background(), fontPath)
		if err != nil {
			return err
		}
		configuration.FontResolver = psdkit.FontResolverFunc(
			func(_ context.Context, name string) (*psdkit.FontFace, error) {
				if name != face.PostScriptName {
					return nil, fmt.Errorf("explicit font does not match requested PostScript name")
				}
				return face, nil
			})
		metricPairs := kerning == "metrics"
		configuration.ApproximateText = &psdkit.TextRenderOptions{Kerning: &metricPairs}
	}
	source, err := psdkit.OpenDocument(input)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	pixels, err := psdkit.RenderSource(context.Background(), source,
		configuration)
	if err != nil {
		return err
	}
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	encodeErr := png.Encode(file, pixels)
	closeErr := file.Close()
	if encodeErr != nil {
		return encodeErr
	}
	return closeErr
}
