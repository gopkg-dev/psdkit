package psdcompose

import "github.com/gopkg-dev/psdkit/psdmodel"

// wholeColorAlphaDocument limits native byte alpha stages to flat ordinary raster documents.
// Warnings conservatively exclude saved text and shape rasters whose source kind is not in Layer.
func wholeColorAlphaDocument(document *psdmodel.Document) bool {
	if document == nil || len(document.Warnings) != 0 {
		return false
	}
	for _, layer := range document.Layers {
		if !wholeColorAlphaRaster(layer) {
			return false
		}
	}
	return true
}

func wholeColorAlphaRaster(layer *psdmodel.Layer) bool {
	return layer != nil && layer.Pixels != nil && !layer.Group && layer.Children == nil &&
		!layer.Clipping && layer.Mask == nil && layer.VectorMask == nil && layer.VectorDensity == nil &&
		layer.VectorFeather == nil && !composeCustomRanges(layer.BlendRanges) &&
		len(layer.ChannelRestrictions) == 0 && layer.Knockout == psdmodel.KnockoutNone &&
		layer.Placed == nil && layer.FillColor == nil && layer.Adjustment == nil && layer.Gradient == nil &&
		layer.Pattern == nil && layer.PatternTile == nil && layer.Effects == nil && len(layer.Unsupported) == 0 &&
		(layer.TransparencyShapes == nil || *layer.TransparencyShapes)
}
