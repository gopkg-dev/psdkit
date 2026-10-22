package psdfile

import (
	"context"
	"errors"
	"slices"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func (d *Document) augmentLegacySemantics(ctx context.Context, decoded *psdmodel.Document) error {
	var physical []*psdmodel.Layer
	var visit func([]*psdmodel.Layer)
	visit = func(layers []*psdmodel.Layer) {
		for _, layer := range layers {
			if layer.Children != nil {
				physical = append(physical, nil)
				visit(layer.Children)
			}
			physical = append(physical, layer)
		}
	}
	visit(decoded.Layers)
	if len(physical) != len(d.Layers) {
		return fileError(ErrMalformed, "rgba8", "layers.semantic", 0, "decoded tree differs from indexed layer records")
	}
	// The parent Decode holds its source lock; this view uses its own lock and borrows immutable metadata.
	view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: d.limits,
		Header: d.Header, Resources: d.Resources}
	for index, layer := range physical {
		if layer == nil {
			continue
		}
		for _, block := range d.Layers[index].AdditionalInfo {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch block.Key {
			case "PtFl":
				pattern, err := view.DecodePatternFill(ctx, block)
				if err != nil && !errors.Is(err, ErrUnsupported) {
					return err
				}
				if pattern == nil {
					layer.Unsupported = append(layer.Unsupported, "unsupported pattern fill descriptor")
				} else {
					layer.Pattern = pattern.Fill
				}
			case "GdFl":
				gradient, err := view.DecodeGradientFill(ctx, block)
				if err != nil && !errors.Is(err, ErrUnsupported) {
					return err
				}
				if gradient == nil {
					layer.Unsupported = append(layer.Unsupported, "unsupported gradient descriptor")
				} else {
					layer.Gradient = gradient.Fill
				}
			case "lfx2", "lmfx":
				effects, err := view.DecodeEffects(ctx, block)
				if err != nil && !errors.Is(err, ErrUnsupported) {
					return err
				}
				if effects != nil {
					layer.Effects = effects
					layer.Unsupported = slices.DeleteFunc(layer.Unsupported, func(value string) bool { return value == "暂不支持图层效果" })
				}
			}
		}
	}
	return nil
}
