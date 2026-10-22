package psdfilter

import (
	"context"
	"image"
)

// ApplyRegion filters an owned input window with explicit complete-image opacity facts.
// fullInputOpaque contains one fact per Stack.Filters entry, describing that filter's complete input image.
// A false fact keeps premultiplied precision even when the local window happens to be opaque.
// True facts are rejected when local alpha contradicts them; callers prove the complete-image facts independently.
// Required filter halos must be present in source, and callers retain only their intended interior output region.
func ApplyRegion(
	ctx context.Context, source *image.NRGBA, stack Stack, fullInputOpaque []bool, options ...Options,
) (*image.NRGBA, error) {
	if fullInputOpaque == nil {
		return nil, filterError(ErrInvalid, "region.opacity", "regional filtering requires complete-image opacity facts")
	}
	return apply(ctx, source, stack, fullInputOpaque, options)
}
