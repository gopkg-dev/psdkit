package psdfile

import "context"

// DecodeDescriptorContext decodes a source descriptor with cancellation during structural reads.
func (d *Document) DecodeDescriptorContext(
	ctx context.Context, span Span, versioned bool, options ...MetadataOptions,
) (*Descriptor, error) {
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	descriptor, _, _, err := d.decodeDescriptorUsage(ctx, span, versioned, limits)
	return descriptor, err
}

func (d *Document) decodeDescriptorUsage(
	ctx context.Context, span Span, versioned bool, limits Limits,
) (*Descriptor, int64, int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, 0, 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	if !d.validSpan(span) {
		return nil, 0, 0, fileError(ErrMalformed, "descriptor", "descriptor", span.Offset, "span is outside the source")
	}
	view := &Document{reader: contextReaderAt{ctx: ctx, reader: d.reader}, size: d.size, limits: limits}
	cursor := descriptorCursor{document: view, offset: span.Offset, end: span.Offset + span.Length}
	var version uint32
	var versionData Span
	if versioned {
		versionData = Span{Offset: cursor.offset, Length: 4}
		version = cursor.u32("descriptor.version")
		if cursor.err == nil && version != 16 {
			return nil, 0, 0, fileError(ErrUnsupported, "descriptor", "descriptor.version", span.Offset, "unknown version")
		}
	}
	result := cursor.descriptor(1, "descriptor", false)
	if err := ctx.Err(); err != nil {
		return nil, 0, 0, err
	}
	if cursor.err != nil {
		return nil, 0, 0, cursor.err
	}
	result.Version, result.VersionData = version, versionData
	result.Span = Span{Offset: span.Offset, Length: cursor.offset - span.Offset}
	return result, cursor.allocated, cursor.items, nil
}
