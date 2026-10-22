package psdfile

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
)

// Open indexes a PSD/PSB file and owns the open file until Document.Close.
func Open(filename string, options ...LoadOptions) (*Document, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if !info.Mode().IsRegular() {
		return nil, errors.Join(
			fileError(ErrMalformed, "source", "source", 0, "source must be a regular file"), file.Close(),
		)
	}
	document, err := Read(file, info.Size(), options...)
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	document.closer = file
	return document, nil
}

// Parse indexes and retains the supplied bytes; callers must not mutate them before Close.
func Parse(data []byte, options ...LoadOptions) (*Document, error) {
	return Read(bytes.NewReader(data), int64(len(data)), options...)
}

// Read indexes a borrowed ReaderAt without closing it, including after Document.Close.
// The caller keeps the source alive and immutable until all document operations have finished.
func Read(reader io.ReaderAt, size int64, options ...LoadOptions) (*Document, error) {
	limits, err := loadLimits(options)
	if err != nil {
		return nil, err
	}
	if reader == nil || size < 26 {
		return nil, fileError(ErrMalformed, "source", "source", 0, "missing or incomplete source")
	}
	if size > limits.MaxFileBytes {
		return nil, fileError(ErrLimit, "source", "source", 0, "source exceeds MaxFileBytes")
	}
	// Only the internal linked reader supplies a container boundary independent of the PSD/PSB image end.
	_, linkedContent := reader.(*linkedDocumentReader)
	document := &Document{
		reader: checkedReaderAt{reader: reader}, size: size, limits: limits,
		Source: Span{Length: size}, ActiveLayerSet: -1, linkedContent: linkedContent,
	}
	if err := document.index(); err != nil {
		return nil, err
	}
	return document, nil
}

func loadLimits(options []LoadOptions) (Limits, error) {
	defaults := DefaultLimits()
	if len(options) == 0 {
		return defaults, nil
	}
	if len(options) != 1 {
		return Limits{}, fileError(ErrMalformed, "options", "options", 0, "provide at most one LoadOptions")
	}
	limits := options[0].Limits
	byteLimits := []*int64{
		&limits.MaxFileBytes, &limits.MaxMetadataBytes, &limits.MaxReadBytes,
		&limits.MaxDecodePixels, &limits.MaxDecodeBytes,
	}
	byteDefaults := []int64{
		defaults.MaxFileBytes, defaults.MaxMetadataBytes, defaults.MaxReadBytes,
		defaults.MaxDecodePixels, defaults.MaxDecodeBytes,
	}
	for index, limit := range byteLimits {
		if *limit < 0 {
			return Limits{}, fileError(ErrLimit, "options", "options.limits", 0, "limits must not be negative")
		}
		if *limit == 0 {
			*limit = byteDefaults[index]
		}
	}
	countLimits := []*int{&limits.MaxBlocks, &limits.MaxLayers, &limits.MaxDescriptorDepth, &limits.MaxDescriptorItems}
	countDefaults := []int{
		defaults.MaxBlocks, defaults.MaxLayers, defaults.MaxDescriptorDepth, defaults.MaxDescriptorItems,
	}
	for index, limit := range countLimits {
		if *limit < 0 {
			return Limits{}, fileError(ErrLimit, "options", "options.limits", 0, "limits must not be negative")
		}
		if *limit == 0 {
			*limit = countDefaults[index]
		}
	}
	return limits, nil
}

// Limits returns the document's effective allocation limits.
func (d *Document) Limits() Limits { return d.limits }

// Close releases an owned file and makes all subsequent source operations return ErrClosed.
// It waits for ongoing reads and decodes; repeated calls are safe.
func (d *Document) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	d.reader = nil
	if d.closer != nil {
		return d.closer.Close()
	}
	return nil
}

func (d *Document) validSpan(span Span) bool {
	return span.Offset >= 0 && span.Length >= 0 && span.Offset <= d.size && span.Length <= d.size-span.Offset
}

// ReadAt reads bounded original bytes while the document remains open.
func (d *Document) ReadAt(data []byte, offset int64) (int, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return 0, ErrClosed
	}
	if offset < 0 || offset > d.size {
		return 0, fileError(ErrMalformed, "source", "source", offset, "offset is outside the source")
	}
	if len(data) == 0 {
		return 0, nil
	}
	length := min(int64(len(data)), d.size-offset)
	count, err := d.reader.ReadAt(data[:length], offset)
	if int64(count) < int64(len(data)) && err == nil {
		err = io.EOF
	}
	return count, err
}

// ReadSpan returns owned original bytes subject to MaxReadBytes; use CopySpan for large payloads.
func (d *Document) ReadSpan(span Span) ([]byte, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if !d.validSpan(span) {
		return nil, fileError(ErrMalformed, "source", "span", span.Offset, "span is outside the source")
	}
	if span.Length > d.limits.MaxReadBytes || uint64(span.Length) > uint64(^uint(0)>>1) {
		return nil, fileError(ErrLimit, "source", "span", span.Offset, "span exceeds MaxReadBytes")
	}
	data := make([]byte, int(span.Length))
	if len(data) == 0 {
		return data, nil
	}
	count, err := d.reader.ReadAt(data, span.Offset)
	if count != len(data) {
		err = errors.Join(io.ErrUnexpectedEOF, err)
	}
	if err != nil && (!errors.Is(err, io.EOF) || count != len(data)) {
		return nil, &Error{Kind: ErrMalformed, Feature: "source", Path: "span", Offset: span.Offset,
			Detail: "original bytes could not be read", Cause: err}
	}
	return data, nil
}

// CopySpan streams an original span without rewriting headers, payloads or padding.
// This is byte preservation for future writers, not an editor or SavePSD implementation.
func (d *Document) CopySpan(ctx context.Context, writer io.Writer, span Span) (int64, error) {
	return d.CopySpanBuffer(ctx, writer, span, nil)
}

// CopySpanBuffer streams an original span using borrowed caller scratch without retaining it.
// A nil buffer selects the same 64 KiB allocation as CopySpan; a non-nil empty buffer is invalid.
// At most the first 64 KiB is used; callers must not access that prefix until this call returns.
// Writer ReaderFrom dispatch is preserved and may bypass scratch, matching io.CopyBuffer behavior.
// Source validation, Close synchronization, cancellation and I/O errors follow CopySpan's rules.
func (d *Document) CopySpanBuffer(
	ctx context.Context, writer io.Writer, span Span, scratch []byte,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return 0, ErrClosed
	}
	if !d.validSpan(span) || writer == nil {
		return 0, fileError(ErrMalformed, "source", "span", span.Offset, "invalid span or missing writer")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if scratch == nil {
		scratch = make([]byte, 64<<10)
	} else if len(scratch) == 0 {
		return 0, fileError(ErrMalformed, "source", "span.buffer", span.Offset, "copy buffer must not be empty")
	} else if len(scratch) > 64<<10 {
		scratch = scratch[:64<<10]
	}
	reader := &cancelReader{ctx: ctx, reader: io.NewSectionReader(d.reader, span.Offset, span.Length)}
	count, err := io.CopyBuffer(writer, reader, scratch)
	if err == nil && count != span.Length {
		err = &Error{Kind: ErrMalformed, Feature: "source", Path: "span", Offset: span.Offset + count,
			Detail: "source ended before the complete original span", Cause: io.ErrUnexpectedEOF}
	} else if err != nil && reader.failure != nil && errors.Is(err, reader.failure) &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		err = &Error{Kind: ErrMalformed, Feature: "source", Path: "span", Offset: span.Offset + count,
			Detail: "original source bytes could not be copied", Cause: err}
	}
	return count, err
}

type cancelReader struct {
	ctx     context.Context
	reader  io.Reader
	failure error
}

// checkedReaderAt prevents a broken borrowed ReaderAt from causing non-progressing stream loops.
type checkedReaderAt struct {
	reader io.ReaderAt
}

func (r checkedReaderAt) ReadAt(data []byte, offset int64) (int, error) {
	count, err := r.reader.ReadAt(data, offset)
	if count < 0 || count > len(data) {
		return 0, io.ErrUnexpectedEOF
	}
	if count < len(data) && err == nil {
		err = io.ErrUnexpectedEOF
	}
	return count, err
}

func (r *cancelReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.reader.Read(data)
	if err != nil {
		r.failure = err
	}
	return count, err
}
