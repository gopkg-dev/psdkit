package psdcompose

import (
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

// ordinaryGrayRangeDocument limits the measured range stage to flat saved RGB8 raster documents.
// Cached sources, groups, clipping and advanced source semantics retain their previous range path.
// The registered effect cache excludes active effects while permitting saved disabled descriptors.
func (plan *composePlan) ordinaryGrayRangeDocument() bool {
	document := plan.document
	if document == nil || len(document.Warnings) != 0 {
		return false
	}
	for _, layer := range document.Layers {
		if layer == nil {
			return false
		}
		ordinary := *layer
		ordinary.Effects = nil
		if layer.Pixels == nil || layer.Group || layer.Children != nil ||
			!SupportedBlend(layer.BlendMode) || !passRangeOrdinaryFlags(&ordinary) || plan.effects[layer] != nil ||
			layer.PatternTile != nil ||
			layer.VectorMask != nil || layer.VectorDensity != nil || layer.VectorFeather != nil ||
			!passRangeSavedMask(layer.Mask) ||
			len(layer.BlendRanges) > 1 && composeCustomRanges(layer.BlendRanges[1:]) {
			return false
		}
	}
	return true
}

// ordinaryGrayRangeGates separates native source-Fill modulation from destination coverage.
// The source uses nearest byte 30R/59G/11B gray; destination gray retains the floating backdrop.
// Keeping the gates separate preserves ordinary kernels and changes the eight special-Fill kernels.
func ordinaryGrayRangeGates(
	pair psdmodel.BlendRange, source, backdrop [3]float64, backdropAlpha float64,
) (float64, float64) {
	sourceGray, backdropGray := float64(0), float64(0)
	for component, weight := range [3]float64{.3, .59, .11} {
		sourceGray += math.Round(composeClamp(source[component])*255) / 255 * weight
		backdropGray += backdrop[component] * weight
	}
	sourceGray = math.Round(sourceGray*255) / 255
	return composeSlider(pair.Source, sourceGray),
		1 - backdropAlpha + backdropAlpha*composeSlider(pair.Destination, backdropGray)
}
