package psdfile

import "context"

// LinkedDocumentResolver supplies an external PSD/PSB source selected by the caller.
// It receives retained link metadata, never a path guessed or opened by the library.
// A successful result transfers responsibility for closing that document to the caller.
type LinkedDocumentResolver func(context.Context, LinkedFileRecord) (*Document, error)

// ResolveLinkedDocument indexes embedded PSD/PSB data or invokes an explicit external resolver.
// Embedded results borrow this document's source and require it to remain open.
// External links are never read from disk automatically, including links with cached bytes.
func (d *Document) ResolveLinkedDocument(
	ctx context.Context, record LinkedFileRecord, resolver LinkedDocumentResolver,
) (*Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.RLock()
	closed := d.closed
	d.mu.RUnlock()
	if closed {
		return nil, ErrClosed
	}
	if record.Kind == "liFD" {
		return d.OpenLinkedDocument(ctx, record)
	}
	if record.Kind != "liFE" || resolver == nil {
		return nil, fileError(ErrUnsupported, "linked.resolve", "linkedFiles", record.Span.Offset,
			"external PSD/PSB sources require an explicit resolver")
	}
	result, err := resolver(ctx, record)
	if err != nil {
		return nil, err
	}
	if result == nil || result == d {
		return nil, fileError(ErrMalformed, "linked.resolve", "linkedFiles", record.Span.Offset,
			"resolver must return an independently owned non-nil source document")
	}
	if err := ctx.Err(); err != nil {
		_ = result.Close()
		return nil, err
	}
	result.mu.RLock()
	resultClosed := result.closed
	result.mu.RUnlock()
	if resultClosed {
		return nil, ErrClosed
	}
	return result, nil
}
