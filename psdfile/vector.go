package psdfile

import (
	"context"
	"errors"
	"io"

	"github.com/gopkg-dev/psdkit/psdvector"
)

// DecodeVectorMask decodes a retained vmsk/vsms payload into owned typed path records.
// The span excludes the additional-block header and external alignment bytes.
// Metadata and record counts are bounded by MaxMetadataBytes/MaxDecodeBytes and MaxDescriptorItems.
// It does not decode source pixels or change the retained vector block.
func (d *Document) DecodeVectorMask(ctx context.Context, span Span) (*psdvector.Mask, error) {
	return d.decodeVectorSource(ctx, span, true)
}

// DecodePath decodes retained headerless records from a saved or working path image resource.
// It uses the same ownership, metadata budgets and source lifecycle as DecodeVectorMask.
func (d *Document) DecodePath(ctx context.Context, span Span) (*psdvector.Mask, error) {
	return d.decodeVectorSource(ctx, span, false)
}

// DecodePathResource checks a saved-path or working-path resource ID before decoding its records.
// Resource 2999 contains a clipping-path name reference, which remains available through ReadSpan/CopySpan.
func (d *Document) DecodePathResource(ctx context.Context, block ResourceBlock) (*psdvector.Mask, error) {
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if block.ID == 2999 {
		return nil, fileError(ErrUnsupported, "vectorPath", "vectorPath.resource", block.Data.Offset,
			"resource contains a clipping-path name reference, not path geometry")
	}
	if block.ID != 1025 && (block.ID < 2000 || block.ID > 2997) {
		return nil, fileError(ErrUnsupported, "vectorPath", "vectorPath.resource", block.Data.Offset,
			"resource ID does not contain a saved or working path")
	}
	return d.DecodePath(ctx, block.Data)
}

func (d *Document) decodeVectorSource(ctx context.Context, span Span, maskHeader bool) (*psdvector.Mask, error) {
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
	minimum := int64(0)
	if maskHeader {
		minimum = 8
	}
	if !d.validSpan(span) || span.Length < minimum {
		return nil, fileError(ErrMalformed, "vectorMask", "vectorMask.span", span.Offset,
			"vector payload is outside the source or lacks its header")
	}
	budget := min(d.limits.MaxReadBytes, d.limits.MaxMetadataBytes, d.limits.MaxDecodeBytes)
	count := (span.Length - minimum) / 26
	if span.Length > budget || count > int64(d.limits.MaxDescriptorItems) ||
		budget-span.Length < 512 || count > (budget-span.Length-512)/256 ||
		uint64(span.Length) > uint64(^uint(0)>>1) {
		return nil, fileError(ErrLimit, "vectorMask", "vectorMask.records", span.Offset,
			"vector payload and owned path records exceed configured budgets")
	}
	data := make([]byte, int(span.Length))
	reader := contextReaderAt{ctx: ctx, reader: d.reader}
	countRead, readErr := reader.ReadAt(data, span.Offset)
	if countRead != len(data) || (readErr != nil && !errors.Is(readErr, io.EOF)) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if readErr == nil {
			readErr = io.ErrUnexpectedEOF
		}
		return nil, &Error{Kind: ErrMalformed, Feature: "vectorMask", Path: "vectorMask.payload", Offset: span.Offset,
			Detail: "retained vector payload could not be read", Cause: readErr}
	}
	options := psdvector.DecodeOptions{
		SourceOffset: span.Offset, MaxBytes: budget, MaxRecords: d.limits.MaxDescriptorItems,
	}
	var mask *psdvector.Mask
	var err error
	if maskHeader {
		mask, err = psdvector.DecodeMask(ctx, data, int(d.Header.Width), int(d.Header.Height), options)
	} else {
		mask, err = psdvector.DecodePath(ctx, data, int(d.Header.Width), int(d.Header.Height), options)
	}
	if err != nil {
		return nil, vectorDecodeError(err, span.Offset)
	}
	return mask, nil
}

// DecodeVectorMaskBlock checks the additional-block key before decoding its retained vector payload.
func (d *Document) DecodeVectorMaskBlock(
	ctx context.Context, block AdditionalBlock,
) (*psdvector.Mask, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if block.Key != "vmsk" && block.Key != "vsms" {
		return nil, fileError(ErrUnsupported, "vectorMask", "vectorMask.key", block.Data.Offset,
			"additional block does not contain a vector mask")
	}
	return d.DecodeVectorMask(ctx, block.Data)
}

func vectorDecodeError(err error, offset int64) error {
	kind := ErrMalformed
	switch {
	case errors.Is(err, psdvector.ErrUnsupported):
		kind = ErrUnsupported
	case errors.Is(err, psdvector.ErrLimit):
		kind = ErrLimit
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	path, detail := "vectorMask", "retained vector-mask decoding failed"
	if vectorErr, ok := errors.AsType[*psdvector.Error](err); ok {
		path, detail, offset = vectorErr.Path, vectorErr.Detail, vectorErr.Offset
	}
	return &Error{Kind: kind, Feature: "vectorMask", Path: path, Offset: offset, Detail: detail, Cause: err}
}
