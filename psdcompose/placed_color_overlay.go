package psdcompose

import "github.com/gopkg-dev/psdkit/psdmodel"

// validatePlacedColorOverlay confines the bridge to unchanged, freshly projected Normal raster sources.
func validatePlacedColorOverlay(layer *psdmodel.Layer, path string) error {
	if !layer.RenderedEffects.Valid(layer.Pixels, layer.Bounds, layer.Placed) {
		return composeError(ErrUnsupported, path, "placed Color Overlay requires freshly rendered source pixels")
	}
	if layer.Gradient != nil || layer.VectorMask != nil && !layer.VectorMask.Disabled ||
		layer.Clipping || composeCustomRanges(layer.BlendRanges) ||
		len(layer.ChannelRestrictions) != 0 || layer.Knockout != psdmodel.KnockoutNone ||
		layer.TransparencyShapes != nil && !*layer.TransparencyShapes {
		return composeError(ErrUnsupported, path, "advanced placed Color Overlay interactions are unsupported")
	}
	return nil
}
