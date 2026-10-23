package psdfile

import (
	"bytes"
	"context"
)

// DecodeRasterContents decodes standalone PNG/JPEG and flat RGB8 TIFF contents with bounded storage.
// Options only lower DefaultLimits, using the same decoder and ICC policy as DecodeLinkedRaster.
// Pixels and ICC bytes are owned; the encoded input is borrowed only until the call returns and is never changed.
// WorkingBytes excludes the borrowed encoded input and includes codec scratch, output pixels and ICC storage.
func DecodeRasterContents(ctx context.Context, data []byte, options ...LinkedRasterOptions) (*LinkedRaster, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits := DefaultLimits()
	if int64(len(data)) > limits.MaxFileBytes {
		return nil, fileError(ErrLimit, "rasterContents", "source", 0, "source exceeds MaxFileBytes")
	}
	source := &Document{reader: bytes.NewReader(data), size: int64(len(data)), limits: limits}
	defer func() { _ = source.Close() }()
	return source.DecodeLinkedRaster(ctx, LinkedFileRecord{
		Kind: "liFD", HasData: true, Data: Span{Length: int64(len(data))},
	}, options...)
}
