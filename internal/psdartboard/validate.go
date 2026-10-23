// Package psdartboard shares bounded retained-artboard validation and owned composition snapshots.
package psdartboard

import (
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// Issue is the compatibility parser's known artboard marker, removed only after shared qualification.
const Issue = "首版暂不支持画板文档"

// ValidateMetadata validates retained board rectangles and document options without changing either source view.
// Callers reserve each view's MetadataBytes plus 256 bytes for this selection and its temporary identity map.
func ValidateMetadata(views []psdfile.ArtboardRecord, source *psdfile.Document) (
	[]psdfile.ArtboardRecord, error,
) {
	if source == nil {
		return nil, renderError(psdfile.ErrMalformed, "artboard.source", 0, "source document is required")
	}
	var document *psdfile.ArtboardData
	var boards []psdfile.ArtboardRecord
	seen := make(map[int]bool)
	for _, view := range views {
		if view.Data == nil || len(view.Data.Unsupported) != 0 {
			return nil, renderError(psdfile.ErrUnsupported, "artboard.metadata", 0,
				"unrecognized or ambiguous artboard metadata")
		}
		if view.LayerIndex >= 0 {
			if seen[view.LayerIndex] || view.LayerIndex >= len(source.Layers) || view.Data.Block.Key == "artd" {
				return nil, renderError(psdfile.ErrUnsupported, "artboard.count", view.Data.Block.Data.Offset,
					"duplicate or ambiguous per-layer artboard descriptors")
			}
			seen[view.LayerIndex] = true
			if err := validateRectangle(view.Data, float64(source.Header.Width), float64(source.Header.Height)); err != nil {
				return nil, err
			}
			boards = append(boards, view)
		} else {
			if document != nil || view.Data.Block.Key != "artd" {
				return nil, renderError(psdfile.ErrUnsupported, "artboard.document", view.Data.Block.Data.Offset,
					"one document-level artd descriptor is required")
			}
			document = view.Data
		}
	}
	if len(boards) == 0 || document == nil {
		return nil, renderError(psdfile.ErrUnsupported, "artboard.rectangle", 0,
			"explicit board and document-level metadata are required")
	}
	if err := validateDocument(document, len(boards)); err != nil {
		return nil, err
	}
	return boards, nil
}

func validateRectangle(board *psdfile.ArtboardData, width, height float64) error {
	if board == nil {
		return renderError(psdfile.ErrMalformed, "artboard.metadata", 0, "artboard metadata is required")
	}
	if board.Rect == nil || board.BackgroundType == nil {
		return renderError(psdfile.ErrUnsupported, "artboard.rectangle", board.Block.Data.Offset,
			"explicit rectangle and background metadata are required")
	}
	for _, value := range []float64{board.Rect.Left, board.Rect.Top, board.Rect.Right, board.Rect.Bottom} {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < 0 ||
			value > psdmodel.MaxDimension {
			return renderError(psdfile.ErrUnsupported, "artboard.rectangle", board.Block.Data.Offset,
				"saved artboard rectangles must have nonnegative integer coordinates")
		}
	}
	if board.Rect.Right <= board.Rect.Left || board.Rect.Bottom <= board.Rect.Top ||
		board.Rect.Right > width || board.Rect.Bottom > height {
		return renderError(psdfile.ErrUnsupported, "artboard.rectangle", board.Block.Data.Offset,
			"saved artboard rectangle must lie inside the saved canvas")
	}
	if *board.BackgroundType < 1 || *board.BackgroundType > 4 ||
		*board.BackgroundType == 4 && board.BackgroundRGB == nil {
		return renderError(psdfile.ErrUnsupported, "artboard.background", board.Block.Data.Offset,
			"unrecognized background type or missing custom RGB color")
	}
	for _, entry := range board.UnknownEntries {
		if entry.Key != "guideIndeces" {
			return renderError(psdfile.ErrUnsupported, "artboard.parameter", entry.Data.Offset,
				"unrecognized artboard parameter")
		}
	}
	return nil
}

func validateDocument(document *psdfile.ArtboardData, boards int) error {
	count, origin, expansion := false, false, false
	var originPoint, expansionPoint [2]float64
	for _, entry := range document.OrderedEntries {
		switch entry.Key {
		case "Cnt ":
			value, valid := entry.Value.(int32)
			count = valid && int64(value) == int64(boards)
		case "origin", "autoExpandOffset":
			point, valid := entry.Value.(*psdfile.Descriptor)
			if !valid || len(point.Entries) != 2 {
				return renderError(psdfile.ErrUnsupported, "artboard.origin", entry.Data.Offset,
					"unrecognized saved artboard origin")
			}
			seen, values := [2]bool{}, [2]float64{}
			for _, component := range point.Entries {
				value, valid := component.Value.(float64)
				coordinate := 0
				if component.Key == "Vrtc" {
					coordinate = 1
				}
				if !valid || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
					math.Abs(value) > psdmodel.MaxDimension || seen[coordinate] ||
					component.Key != "Hrzn" && component.Key != "Vrtc" {
					return renderError(psdfile.ErrUnsupported, "artboard.origin", component.Data.Offset,
						"saved origin must have bounded integer horizontal and vertical components")
				}
				seen[coordinate], values[coordinate] = true, value
			}
			if entry.Key == "origin" {
				origin, originPoint = true, values
			} else {
				expansion, expansionPoint = true, values
			}
		case "autoExpandEnabled", "autoNestEnabled", "autoPositionEnabled", "shrinkwrapOnSaveEnabled":
			if _, valid := entry.Value.(bool); !valid {
				return renderError(psdfile.ErrUnsupported, "artboard.document", entry.Data.Offset,
					"unrecognized artboard document option")
			}
		case "docDefaultNewArtboardBackgroundColor", "docDefaultNewArtboardBackgroundType":
			// Retained defaults apply to newly created boards, not this saved board's background.
		default:
			return renderError(psdfile.ErrUnsupported, "artboard.document", entry.Data.Offset,
				"unrecognized document-level artboard parameter")
		}
	}
	if !count || !origin || !expansion || originPoint != expansionPoint {
		return renderError(psdfile.ErrUnsupported, "artboard.document", document.Block.Data.Offset,
			"explicit matching board count and saved origin metadata are required")
	}
	return nil
}

// ValidateLayer enforces the shared top-level artboard group's supported own semantics.
// Only the known compatibility Issue marker is ignored; all unrelated unsupported metadata remains blocking.
func ValidateLayer(layer *psdmodel.Layer, offset int64) error {
	if layer == nil || !layer.Group {
		return renderError(psdfile.ErrUnsupported, "artboard.group", offset,
			"artboard metadata must identify a group")
	}
	if layer.Hidden {
		return nil
	}
	for _, issue := range layer.Unsupported {
		if issue != Issue {
			return renderError(psdfile.ErrUnsupported, "artboard.layer", offset,
				"artboard group has unsupported own semantics")
		}
	}
	if !pixelMaskInactive(layer) ||
		layer.VectorMask != nil && (!layer.VectorMask.Disabled || len(layer.VectorMask.Unsupported) != 0) || layer.Clipping ||
		layer.FillColor != nil || layer.Adjustment != nil || layer.Gradient != nil || layer.Pattern != nil ||
		layer.PatternTile != nil || !effectsInactive(layer) || layer.Placed != nil ||
		layer.Knockout != psdmodel.KnockoutNone || len(layer.ChannelRestrictions) != 0 {
		return renderError(psdfile.ErrUnsupported, "artboard.layer", offset,
			"artboard group has unsupported own semantics")
	}
	if (layer.VectorDensity != nil || layer.VectorFeather != nil) &&
		(layer.VectorMask == nil || !layer.VectorMask.Disabled) {
		return renderError(psdfile.ErrUnsupported, "artboard.layer", offset,
			"artboard vector mask controls require a disabled vector mask")
	}
	for _, blend := range layer.BlendRanges {
		full := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
		if blend.Source != full || blend.Destination != full {
			return renderError(psdfile.ErrUnsupported, "artboard.blendIf", offset, "artboard Blend If is not calibrated")
		}
	}
	if layer.BlendMode != "normal" && layer.BlendMode != "norm" && layer.BlendMode != "pass" ||
		layer.Opacity != nil && *layer.Opacity != 1 || layer.FillOpacity != nil && *layer.FillOpacity != 1 {
		return renderError(psdfile.ErrUnsupported, "artboard.group", offset,
			"artboard group must use normal or pass-through blending at full opacity and fill")
	}
	return nil
}

func effectsInactive(layer *psdmodel.Layer) bool {
	set := layer.Effects
	return set == nil || len(set.Unsupported) == 0 && (!set.Enabled || !set.EffectsVisible)
}

func pixelMaskInactive(layer *psdmodel.Layer) bool {
	if layer.Mask == nil {
		return true
	}
	if !layer.Mask.Disabled {
		return false
	}
	for _, issue := range layer.Mask.Unsupported {
		// The compatibility parser retains this known vector diagnostic on its shared pixel-mask header.
		if issue != "暂不支持矢量蒙版羽化" || layer.VectorMask == nil ||
			!layer.VectorMask.Disabled || layer.VectorFeather == nil {
			return false
		}
	}
	return true
}

// BackgroundColor quantizes an already validated saved artboard background using the existing RGB8 rule.
func BackgroundColor(board *psdfile.ArtboardData) [3]byte {
	if *board.BackgroundType == 1 {
		return [3]byte{255, 255, 255}
	}
	var result [3]byte
	if *board.BackgroundType == 4 {
		for component, value := range *board.BackgroundRGB {
			result[component] = byte(math.Floor(value*255 + 0.5))
		}
	}
	return result
}

func renderError(kind error, path string, offset int64, detail string) error {
	return &psdfile.Error{Kind: kind, Feature: "artboard-render", Path: path, Offset: offset, Detail: detail}
}
