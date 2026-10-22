package psdscene

import (
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

func validateOpenTypeTypographyPolicy(options OpenTypeTextRenderOptions) error {
	if options.FauxBold > OpenTypeFauxBoldExpandedAdvance {
		return sceneError(psdfile.ErrUnsupported, "text.policy.fauxBold", 0, "unknown synthetic-weight model")
	}
	if options.FauxBoldQuantized && options.FauxBold != OpenTypeFauxBoldExpandedAdvance {
		return sceneError(psdfile.ErrMalformed, "text.policy.fauxBold", 0,
			"quantized synthetic extent requires expanded-advance typography")
	}
	if options.Hinting > OpenTypeTextHintEdgeFractional || options.HintAnchor > psdfont.CFFHintBaseline ||
		options.HintGrid > psdfont.CFFHintCenters || options.HintBluePolicy > OpenTypeTextBlueResolvedOutward {
		return sceneError(psdfile.ErrUnsupported, "text.policy.hinting", 0, "unknown CFF1 fitting policy")
	}
	if options.HintBluePolicy != OpenTypeTextBlueGrid || options.HintSnapBaselineY || options.HintSnapBaselineXQuarter {
		if options.Hinting < OpenTypeTextHintFractional ||
			options.HintAnchor != psdfont.CFFHintWorld || options.HintGrid != psdfont.CFFHintEdges {
			return sceneError(psdfile.ErrUnsupported, "text.policy.hinting", 0,
				"resolved blue zones and baseline snapping require fractional fitting on world pixel edges")
		}
	}
	if options.Hinting == OpenTypeTextHintNone && (options.HintAnchor != psdfont.CFFHintWorld ||
		options.HintGrid != psdfont.CFFHintEdges) {
		return sceneError(psdfile.ErrMalformed, "text.policy.hinting", 0, "hint lattice requires a fitting mode")
	}
	if options.NoAntialias && (options.AreaCoverage || options.CoverageLevels16) {
		return sceneError(psdfile.ErrMalformed, "text.policy.coverage", 0,
			"area coverage and pixel-center membership cannot be selected together")
	}
	if options.AreaSamples != 0 && (!options.AreaCoverage || options.AreaSamples != 4 && options.AreaSamples != 8 &&
		options.AreaSamples != 16 && options.AreaSamples != 64) {
		return sceneError(psdfile.ErrMalformed, "text.policy.coverage", 0,
			"explicit area strata require area coverage and 4, 8, 16 or 64 samples")
	}
	if options.FauxBold != OpenTypeFauxBoldOutline || options.AreaCoverage || options.NoAntialias ||
		options.CoverageLevels16 || options.Hinting != OpenTypeTextHintNone {
		if !options.ApproximateTypography || options.StyleBoundaries != OpenTypeSingleStyle {
			return sceneError(psdfile.ErrUnsupported, "text.policy.typography", 0,
				"synthetic layout and explicit coverage require approximate single-style typography")
		}
	}
	return nil
}
