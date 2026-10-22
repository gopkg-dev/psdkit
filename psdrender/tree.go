package psdrender

import (
	"context"
	"errors"
	"fmt"
	"image"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func needsTreeComposite(layers []*Layer) bool {
	for _, layer := range layers {
		if layer.Group || layer.Children != nil || layer.Clipping || hasBlendRanges(layer) ||
			layer.VectorMask != nil || layer.Adjustment != nil || layer.FillColor != nil || layer.Gradient != nil ||
			layer.Effects != nil || layer.Pattern != nil ||
			(layer.BlendMode != "" && layer.BlendMode != "normal" && layer.BlendMode != "norm") {
			return true
		}
		if mask := layer.Mask; mask != nil && !mask.Disabled &&
			(mask.Invert || (mask.Density != nil && *mask.Density != 255) ||
				(mask.Feather != nil && *mask.Feather != 0)) {
			return true
		}
	}
	return false
}

func hasBlendRanges(layer *Layer) bool {
	defaultSlider := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
	for _, value := range layer.BlendRanges {
		if value.Source != defaultSlider || value.Destination != defaultSlider {
			return true
		}
	}
	return false
}

func (engine *Engine) renderTree(
	ctx context.Context, fitted map[string]*image.NRGBA, backgrounds map[*Layer]*image.NRGBA,
) (*image.NRGBA, error) {
	if len(backgrounds) != 0 {
		return nil, fmt.Errorf("%w: background footprint replacement requires a flat normal stack", ErrUnsupported)
	}
	snapshot := *engine.document
	const treeWorkingLimit = int64(256 << 20)
	workingBytes := int64(snapshot.Width)*int64(snapshot.Height)*4 + int64(MaxLayers+32)*1024
	reserve := func(amount int64) error {
		if amount < 0 || amount > treeWorkingLimit-workingBytes {
			return fmt.Errorf("%w: tree replacements exceed the 256 MiB working budget", psdcompose.ErrLimit)
		}
		workingBytes += amount
		return nil
	}
	seenInputs := make(map[*image.NRGBA]bool, len(fitted))
	for _, pixels := range fitted {
		if !seenInputs[pixels] {
			if err := reserve(int64(len(pixels.Pix))); err != nil {
				return nil, err
			}
			seenInputs[pixels] = true
		}
	}
	var clone func([]*Layer, bool) ([]*Layer, error)
	clone = func(layers []*Layer, hidden bool) ([]*Layer, error) {
		result := make([]*Layer, 0, len(layers))
		baseInvisible := true
		for _, layer := range layers {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			value := *layer
			invisible := hidden || layer.Hidden || layerOpacity(layer) == 0
			if layer.Clipping {
				invisible = invisible || baseInvisible
			} else {
				baseInvisible = invisible
			}
			if layer.Group || layer.Children != nil {
				children, err := clone(layer.Children, invisible)
				if err != nil {
					return nil, err
				}
				value.Children = children
			} else if layer.Placed != nil && !invisible {
				if replacement := fitted[layer.Placed.ID]; replacement != nil {
					if err := reserve(int64(snapshot.Height)*16 + 512); err != nil {
						return nil, err
					}
					plan := engine.projections[layer]
					if plan == nil {
						var err error
						plan, err = CompileProjection(layer, snapshot.Width, snapshot.Height, true)
						if err != nil {
							return nil, err
						}
						engine.projections[layer] = plan
					}
					if err := reserve(int64(max(1, plan.Width)) * int64(max(1, plan.Height)) * 4); err != nil {
						return nil, err
					}
					pixels, origin, err := projectImage(ctx, layer, replacement, plan)
					if err != nil {
						return nil, err
					}
					value.Pixels = pixels
					value.Bounds = image.Rectangle{Min: origin, Max: origin.Add(pixels.Rect.Size())}
				}
			}
			result = append(result, &value)
		}
		return result, nil
	}
	layers, err := clone(snapshot.Layers, false)
	if err != nil {
		return nil, err
	}
	snapshot.Layers = layers
	canvasBytes := int64(snapshot.Width) * int64(snapshot.Height) * 4
	output, err := psdcompose.Render(ctx, &snapshot,
		psdcompose.Options{MaxWorkingBytes: treeWorkingLimit - workingBytes + canvasBytes})
	if err != nil {
		if errors.Is(err, psdcompose.ErrUnsupported) {
			return nil, fmt.Errorf("%w: %w", ErrUnsupported, err)
		}
		return nil, err
	}
	return output, nil
}
