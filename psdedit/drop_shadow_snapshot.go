package psdedit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// dropShadowSnapshotLocked leaves original placed RGB and merged previews unread.
// The editor's indexed tree supplies group ordering after ordinary source channels are decoded.
func (e *Editor) dropShadowSnapshotLocked(ctx context.Context, budget *int64,
) (*psdmodel.Document, []*psdmodel.Layer, error) {
	physical := make([]*psdmodel.Layer, len(e.source.Layers))
	for index, record := range e.source.Layers {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		view, err := e.source.ReadLayerMetadata(ctx, index, psdfile.MetadataOptions{MaxBytes: *budget})
		if err != nil {
			return nil, nil, err
		}
		if view.MetadataBytes >= *budget {
			return nil, nil, editError(psdfile.ErrLimit, "save.layers.metadata", "no source decode budget remains")
		}
		*budget -= view.MetadataBytes
		if view.Divider == 3 {
			continue
		}
		layer := view.Layer
		if layer == nil {
			return nil, nil, editError(ErrInvalidEdit, "save.layers.metadata", "source layer metadata is missing")
		}
		layer.Unsupported = append(layer.Unsupported, view.BlockingIssues...)
		physical[index] = layer
		if index == 0 && view.Divider == 0 {
			hasAlpha := false
			for _, channel := range record.Channels {
				hasAlpha = hasAlpha || channel.ID == -1
			}
			layer.Background = !hasAlpha && len(record.Channels) >= 3
		}
		if layer.Placed == nil && view.Divider == 0 && !record.Bounds.Empty() {
			pixels, err := e.shadowSnapshotPixels(ctx, index, record.Bounds, *budget)
			if err != nil {
				return nil, nil, err
			}
			layer.Pixels = pixels
		}
		if layer.Mask != nil {
			maskBounds := layer.Mask.Bounds
			for channelIndex, channel := range record.Channels {
				if channel.ID != view.PixelMaskChannel {
					continue
				}
				pixels, err := e.shadowSnapshotChannel(ctx, index, channelIndex, maskBounds, *budget)
				if err != nil {
					return nil, nil, err
				}
				layer.Mask.Pixels = pixels
				break
			}
		}
	}
	return &psdmodel.Document{Width: int(e.source.Header.Width), Height: int(e.source.Header.Height)}, physical, nil
}

func (e *Editor) shadowSnapshotPixels(ctx context.Context, index int, bounds image.Rectangle,
	budget int64,
) (*image.NRGBA, error) {
	if int64(bounds.Dx())*int64(bounds.Dy()) > budget/4 {
		return nil, editError(psdfile.ErrLimit, "save.layers.pixels", "source raster exceeds remaining working budget")
	}
	pixels := image.NewNRGBA(bounds)
	budget -= int64(cap(pixels.Pix))
	if budget <= 0 {
		return nil, editError(psdfile.ErrLimit, "save.layers.pixels", "no source channel scratch budget remains")
	}
	for position := 3; position < len(pixels.Pix); position += 4 {
		pixels.Pix[position] = 255
	}
	var seen [4]bool
	for channelIndex, channel := range e.source.Layers[index].Channels {
		component := int(channel.ID)
		if component == -1 {
			component = 3
		} else if component < 0 || component > 2 {
			continue
		}
		if seen[component] {
			return nil, editError(ErrInvalidEdit, "save.layers.channels", "duplicate RGB or alpha channel")
		}
		seen[component] = true
		local := bounds.Sub(channel.Bounds.Min)
		plane, err := e.source.DecodeChannelRegion(ctx, index, channelIndex, local,
			psdfile.RegionOptions{MaxWorkingBytes: budget})
		if err != nil {
			return nil, err
		}
		for row := range bounds.Dy() {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := range bounds.Dx() {
				pixels.Pix[row*pixels.Stride+column*4+component] = plane.Data[row*plane.RowBytes+column]
			}
		}
	}
	for component := range 3 {
		if !seen[component] {
			return nil, editError(ErrInvalidEdit, "save.layers.channels", "required RGB channel is missing")
		}
	}
	return pixels, nil
}

func (e *Editor) shadowSnapshotChannel(ctx context.Context, layerIndex, channelIndex int,
	bounds image.Rectangle, budget int64,
) (*image.NRGBA, error) {
	if bounds.Empty() {
		return nil, nil
	}
	if int64(bounds.Dx())*int64(bounds.Dy()) > budget/4 {
		return nil, editError(psdfile.ErrLimit, "save.layers.mask", "source mask exceeds remaining working budget")
	}
	budget -= int64(bounds.Dx()) * int64(bounds.Dy()) * 4
	if budget <= 0 {
		return nil, editError(psdfile.ErrLimit, "save.layers.mask", "no source mask scratch budget remains")
	}
	local := bounds.Sub(e.source.Layers[layerIndex].Channels[channelIndex].Bounds.Min)
	plane, err := e.source.DecodeChannelRegion(ctx, layerIndex, channelIndex, local,
		psdfile.RegionOptions{MaxWorkingBytes: budget})
	if err != nil {
		return nil, err
	}
	pixels := image.NewNRGBA(bounds)
	for row := range bounds.Dy() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range bounds.Dx() {
			pixels.Pix[row*pixels.Stride+column*4] = plane.Data[row*plane.RowBytes+column]
		}
	}
	return pixels, nil
}
