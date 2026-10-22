package psdcompose

import "github.com/gopkg-dev/psdkit/psdmodel"

// maskedGroupEmpty avoids evaluating a subtree whose own mask removes its entire contribution.
// Effects and bounded clipping work retain their existing evaluation path.
// Partially transparent pass-through backdrops retain the original interpolation arithmetic.
func (plan *composePlan) maskedGroupEmpty(layer *psdmodel.Layer, backdrop composePixel, x, y int) bool {
	if plan.options.ApproximateClippedGroupBackdrop ||
		layer.BlendMode == "pass" && backdrop.alpha != 1 {
		return false
	}
	masked := layer.Mask != nil && !layer.Mask.Disabled && plan.mask(layer.Mask, x, y) == 0
	if !masked && layer.VectorMask != nil && !layer.VectorMask.Disabled {
		masked = plan.vectorAlpha(layer, x, y) == 0
	}
	return masked && plan.effects[layer] == nil
}
