package psdfile

import (
	"context"
	"io"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdparse"
)

// Decode creates an independent RGB8 snapshot for the existing renderer.
// Use DecodeLayer/DecodeChannel/DecodeComposite to retain other source depths and color modes.
// This compatibility path retains the renderer's original dimensions and total-pixel guards.
func (d *Document) Decode(ctx context.Context) (*psdmodel.Document, error) {
	return d.decodeRGB8(ctx, true)
}

// DecodeLayers returns an owned RGB8 layer tree without decoding the saved merged image.
// It preserves eager layer limits; large regional reconstruction uses ReadLayerMetadata and channel ROI APIs.
func (d *Document) DecodeLayers(ctx context.Context) (*psdmodel.Document, error) {
	return d.decodeRGB8(ctx, false)
}

func (d *Document) decodeRGB8(ctx context.Context, withPreview bool) (*psdmodel.Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.Header.Depth != 8 || d.Header.ColorMode != 3 || d.Header.Channels < 3 || d.Header.Channels > 16 {
		return nil, fileError(ErrUnsupported, "rgba8", "header", 0, "legacy renderer requires RGB8 with 3-16 channels")
	}
	if d.ActiveLayerSet >= 0 && d.LayerSets[d.ActiveLayerSet].Key != "main" {
		return nil, fileError(
			ErrUnsupported, "rgba8", "layerSets", d.LayerSets[d.ActiveLayerSet].Span.Offset,
			"alternate layer sets support source-plane decoding, not the legacy renderer",
		)
	}
	if len(d.Layers) > psdmodel.MaxLayers || d.size > psdmodel.MaxFileBytes {
		return nil, fileError(ErrLimit, "rgba8", "source", 0, "legacy renderer's layer or file budget exceeded")
	}
	canvas := imageArea(int64(d.Header.Width), int64(d.Header.Height))
	if err := d.legacyArea(canvas, int64(d.Header.Width), int64(d.Header.Height), "header", 0); err != nil {
		return nil, err
	}
	pixels := canvas
	if !withPreview {
		pixels = 0
	}
	for index, layer := range d.Layers {
		width, height := int64(layer.Bounds.Dx()), int64(layer.Bounds.Dy())
		area := imageArea(width, height)
		if err := d.legacyArea(area, width, height, "layers", layer.Span.Offset); err != nil {
			return nil, err
		}
		pixels += area
		if layer.Mask != nil {
			for _, bounds := range []struct{ width, height int64 }{
				{int64(layer.Mask.Bounds.Dx()), int64(layer.Mask.Bounds.Dy())},
				{int64(layer.Mask.RealBounds.Dx()), int64(layer.Mask.RealBounds.Dy())},
			} {
				maskArea := imageArea(bounds.width, bounds.height)
				if err := d.legacyArea(maskArea, bounds.width, bounds.height, "layers.mask", layer.Mask.Span.Offset); err != nil {
					return nil, err
				}
				pixels += maskArea
			}
		}
		if pixels > psdmodel.MaxDecodedPixels || pixels > d.limits.MaxDecodeBytes/4 {
			return nil, fileError(ErrLimit, "rgba8", "layers", d.Layers[index].Span.Offset, "RGBA decode budget exceeded")
		}
	}
	metadataBytes, err := d.rgb8MetadataBytesLocked(ctx)
	if err != nil {
		return nil, err
	}
	if pixels > (d.limits.MaxDecodeBytes-metadataBytes)/4 {
		return nil, fileError(ErrLimit, "rgba8", "composite", d.Composite.Span.Offset, "RGBA decode budget exceeded")
	}
	var decoded *psdmodel.Document
	if withPreview {
		decoded, err = psdparse.ParseReader(contextReaderAt{ctx: ctx, reader: d.reader}, d.size)
	} else {
		decoded, err = psdparse.ParseLayerReader(contextReaderAt{ctx: ctx, reader: d.reader}, d.size)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: ErrMalformed, Feature: "rgba8", Path: "decode", Offset: 0,
			Detail: "legacy RGB8 decoding failed", Cause: err}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := d.augmentLegacySemantics(ctx, decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func imageArea(width, height int64) int64 {
	if width <= 0 || height <= 0 {
		return 0
	}
	return width * height
}

func (d *Document) legacyArea(area, width, height int64, path string, offset int64) error {
	if width < 0 || height < 0 || width > psdmodel.MaxDimension || height > psdmodel.MaxDimension ||
		area > psdmodel.MaxPixels || area > d.limits.MaxDecodePixels {
		return fileError(ErrLimit, "rgba8", path, offset, "RGBA image exceeds the legacy or configured pixel budget")
	}
	return nil
}

type contextReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r contextReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.ReadAt(data, offset)
}
