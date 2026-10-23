package psdscene

import (
	"math"

	"github.com/gopkg-dev/psdkit/psdprecision"
)

// knownInactiveRGB16Mask only promotes a source's explicit disabled flag with known metadata.
// The source's ordinary fallback policy for unknown or density-only masks is unchanged.
func (engine *precisionSourceRenderer) knownInactiveRGB16Mask(layer *psdprecision.Layer) bool {
	if layer.Mask == nil {
		return true
	}
	if !layer.Mask.Disabled {
		return false
	}
	metadata := engine.source.Layers[engine.nodes[layer]].Mask
	if metadata == nil || metadata.Flags&0xe0 != 0 || metadata.ParameterFlags&0xf0 != 0 ||
		metadata.HasReal && metadata.RealFlags&0xe0 != 0 {
		return false
	}
	flags := metadata.Flags
	if flags&8 != 0 && metadata.HasReal {
		flags = metadata.RealFlags
	}
	if flags&2 == 0 {
		return false
	}
	for _, feather := range []*float64{metadata.UserFeather, metadata.VectorFeather} {
		if feather != nil && (math.IsNaN(*feather) || math.IsInf(*feather, 0) || *feather < 0) {
			return false
		}
	}
	return true
}
