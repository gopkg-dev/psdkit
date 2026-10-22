package psdfile

import (
	"context"
	"errors"
	"io"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

// DecodeAdjustment returns an owned typed view of one retained adjustment tagged block.
// Original bytes and source spans remain available even when an unsupported interpretation is returned.
// It never changes source data or decodes pixels and does not approximate modern CgEd parameters.
func (d *Document) DecodeAdjustment(
	ctx context.Context, block AdditionalBlock,
) (*psdadjust.Adjustment, error) {
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
	if !d.validSpan(block.Data) {
		return nil, fileError(ErrMalformed, "adjustment", "adjustment.span", block.Data.Offset, "span is outside the source")
	}
	budget := min(d.limits.MaxMetadataBytes, d.limits.MaxDecodeBytes)
	if block.Data.Length > d.limits.MaxReadBytes || block.Data.Length > budget/2 ||
		uint64(block.Data.Length) > uint64(^uint(0)>>1) {
		return nil, fileError(ErrLimit, "adjustment", "adjustment.payload", block.Data.Offset,
			"payload exceeds decode budgets")
	}
	data := make([]byte, int(block.Data.Length))
	if len(data) != 0 {
		count, err := d.reader.ReadAt(data, block.Data.Offset)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if count != len(data) || err != nil && !errors.Is(err, io.EOF) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return nil, &Error{Kind: ErrMalformed, Feature: "adjustment", Path: "adjustment.payload", Offset: block.Data.Offset,
				Detail: "retained adjustment bytes could not be read", Cause: err}
		}
	}
	adjustment, err := psdadjust.Decode(ctx, block.Key, data, psdadjust.DecodeOptions{
		SourceOffset: block.Data.Offset, MaxBytes: budget,
	})
	if err == nil {
		return adjustment, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return adjustment, err
	}
	kind := ErrMalformed
	if errors.Is(err, psdadjust.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdadjust.ErrLimit) {
		kind = ErrLimit
	}
	path, detail, offset := "adjustment", "adjustment payload could not be decoded", block.Data.Offset
	if decodeError, ok := errors.AsType[*psdadjust.Error](err); ok {
		path, detail, offset = decodeError.Path, decodeError.Detail, decodeError.Offset
	}
	return adjustment, &Error{Kind: kind, Feature: "adjustment", Path: path, Offset: offset, Detail: detail, Cause: err}
}
