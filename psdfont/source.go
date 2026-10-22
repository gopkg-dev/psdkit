package psdfont

import "context"

// ReadSpan returns an independent copy of original font bytes, including retained instructions.
// A zero or omitted byte budget selects 8 MiB; no file is reopened and no bytecode executes.
func (f *Face) ReadSpan(ctx context.Context, span Span, maxBytes ...int64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(maxBytes) > 1 {
		return nil, fontError(ErrMalformed, "source.options", span.Offset, "provide at most one byte budget")
	}
	budget := int64(8 << 20)
	if len(maxBytes) == 1 {
		if maxBytes[0] < 0 {
			return nil, fontError(ErrLimit, "source.options", span.Offset, "byte budget must not be negative")
		}
		if maxBytes[0] != 0 {
			budget = maxBytes[0]
		}
	}
	if f == nil || span.Offset < 0 || span.Length < 0 || span.Offset > int64(len(f.raw)) ||
		span.Length > int64(len(f.raw))-span.Offset {
		return nil, fontError(ErrMalformed, "source.span", span.Offset, "font span is outside the owned source")
	}
	if span.Length > budget || uint64(span.Length) > uint64(^uint(0)>>1) {
		return nil, fontError(ErrLimit, "source.span", span.Offset, "font span exceeds the byte budget")
	}
	result := append([]byte(nil), f.raw[int(span.Offset):int(span.Offset+span.Length)]...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
