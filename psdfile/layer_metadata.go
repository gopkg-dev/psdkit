package psdfile

import (
	"context"
	"errors"
	"math"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdparse"
)

// LayerMetadataView contains owned live layer semantics without decoded channels or merged preview.
// MetadataBytes conservatively reserves retained semantics; WorkingBytes also includes the parser's 64 KiB read cache.
type LayerMetadataView struct {
	Layer                       *psdmodel.Layer
	Divider                     uint32
	PixelMaskChannel            int16
	MetadataBytes, WorkingBytes int64
	Warnings, BlockingIssues    []string
}

// LayerMetadata decodes one active layer's semantics while leaving pixel and embedded content deferred.
func (d *Document) LayerMetadata(ctx context.Context, index int, options ...MetadataOptions) (*psdmodel.Layer, error) {
	view, err := d.ReadLayerMetadata(ctx, index, options...)
	return view.Layer, err
}

// ReadLayerMetadata returns an owned semantic view for one indexed physical layer.
// Large document/layer dimensions are retained; eager renderer pixel limits do not apply to metadata alone.
func (d *Document) ReadLayerMetadata(
	ctx context.Context, index int, options ...MetadataOptions,
) (LayerMetadataView, error) {
	var result LayerMetadataView
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return result, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return result, err
	}
	if index < 0 || index >= len(d.Layers) {
		return result, fileError(ErrMalformed, "layerMetadata", "layer.index", 0, "layer index is outside the active set")
	}
	record := d.Layers[index]
	if !d.validSpan(record.Span) {
		return result, fileError(ErrMalformed, "layerMetadata", "layer.span", record.Span.Offset, "invalid record interval")
	}
	retained := int64(2048) + int64(len(record.Name))*4
	add := func(length int64) bool {
		if length < 0 || length > (math.MaxInt64-retained)/32 {
			return false
		}
		retained += length * 32
		return true
	}
	if !add(record.MaskData.Length) || !add(record.BlendRanges.Length) || !add(record.NameData.Length) {
		return result, fileError(ErrLimit, "layerMetadata", "working", record.Span.Offset, "metadata size overflows")
	}
	for _, block := range record.AdditionalInfo {
		switch block.Key {
		case "SoLd", "SoLE":
			// Raw filter/alias media blobs remain spans and must not be charged as an eagerly decoded tree.
			if block.Data.Length < 12 {
				return result, fileError(ErrMalformed, "layerMetadata", "placement", block.Data.Offset,
					"incomplete modern placement metadata")
			}
			available := limits.MaxMetadataBytes - retained - (64 << 10)
			if available <= 0 {
				return result, fileError(ErrLimit, "layerMetadata", "placement", block.Data.Offset,
					"no placement metadata budget remains")
			}
			viewLimits := limits
			viewLimits.MaxMetadataBytes = available / 2
			if viewLimits.MaxMetadataBytes <= 0 {
				return result, fileError(ErrLimit, "layerMetadata", "placement", block.Data.Offset,
					"no descriptor metadata budget remains")
			}
			view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: viewLimits}
			_, allocated, _, err := view.decodeDescriptorUsage(ctx,
				Span{Offset: block.Data.Offset + 8, Length: block.Data.Length - 8}, true, viewLimits)
			if err != nil {
				return result, err
			}
			retained += allocated*2 + 256
		case "luni", "lsct", "lsdk", "PlLd", "vmsk", "vsms", "lfx2", "lmfx", "lfxs",
			"lrFX", "brit", "levl", "curv", "expA", "nvrt", "post", "thrs", "mixr", "hue2", "hue ", "SoCo",
			"GdFl", "PtFl", "phfl":
			if !add(block.Data.Length) {
				return result, fileError(ErrLimit, "layerMetadata", "working", block.Data.Offset, "metadata size overflows")
			}
		}
	}
	if retained > limits.MaxMetadataBytes-(64<<10) {
		return result, fileError(ErrLimit, "layerMetadata", "working", record.Span.Offset,
			"owned metadata and parser scratch exceed the working limit")
	}
	reader := contextReaderAt{ctx: ctx, reader: d.reader}
	decoded, err := psdparse.ParseLayerMetadata(ctx, reader, psdparse.LayerMetadataOptions{
		Version: d.Header.Version, Width: int(d.Header.Width), Height: int(d.Header.Height),
		Offset: record.Span.Offset, Size: record.Span.Length,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return result, err
		}
		return result, &Error{Kind: ErrMalformed, Feature: "layerMetadata", Path: "layer.record", Offset: record.Span.Offset,
			Detail: "layer metadata could not be decoded", Cause: err}
	}
	view := &Document{reader: reader, size: d.size, limits: limits, Header: d.Header,
		Resources: d.Resources, Layers: []LayerRecord{record}}
	snapshot := &psdmodel.Document{Layers: []*psdmodel.Layer{decoded.Layer}}
	if err := view.augmentLegacySemantics(ctx, snapshot); err != nil {
		return result, err
	}
	result = LayerMetadataView{Layer: decoded.Layer, Divider: decoded.Divider, PixelMaskChannel: decoded.PixelMaskChannel,
		MetadataBytes: retained, WorkingBytes: retained + 64<<10,
		Warnings: decoded.Warnings, BlockingIssues: decoded.BlockingIssues}
	return result, nil
}
