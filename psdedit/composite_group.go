package psdedit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdcompose"
)

// CompositeGroup renders one edited group and its descendants over transparency.
// The group's own masks, opacity, blend semantics and child ordering are retained.
// Ancestors and siblings are excluded; this is an isolated group preview, not its contribution to a backdrop.
// Transparent borders are trimmed, preserving document coordinates in the returned rectangle.
// An empty group returns one transparent pixel. No editing or saved revisions are changed.
func (e *Editor) CompositeGroup(
	ctx context.Context, id LayerID, options ...psdcompose.Options,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	e.mu.RLock()
	defer e.mu.RUnlock()
	if err := e.checkOpenLocked(); err != nil {
		return nil, err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return nil, err
	}

	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return nil, err
	}
	if !node.group {
		return nil, editError(ErrInvalidEdit, "composite.group", "the selected layer is not a group")
	}
	pixels, err := e.renderCompositeNodesLocked(ctx, []*editNode{node}, options...)
	if err != nil {
		return nil, err
	}
	return trimCompositeAlpha(ctx, pixels)
}

// trimCompositeAlpha compacts the owned output in place, avoiding a second output allocation.
// Its original capacity remains visible to callers that account retained pixel storage.
func trimCompositeAlpha(ctx context.Context, pixels *image.NRGBA) (*image.NRGBA, error) {
	box := image.Rectangle{Min: pixels.Rect.Max, Max: pixels.Rect.Min}
	for y := pixels.Rect.Min.Y; y < pixels.Rect.Max.Y; y++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for x := pixels.Rect.Min.X; x < pixels.Rect.Max.X; x++ {
			at := (y-pixels.Rect.Min.Y)*pixels.Stride + (x-pixels.Rect.Min.X)*4
			if pixels.Pix[at+3] == 0 {
				continue
			}
			box.Min.X, box.Min.Y = min(box.Min.X, x), min(box.Min.Y, y)
			box.Max.X, box.Max.Y = max(box.Max.X, x+1), max(box.Max.Y, y+1)
		}
	}
	if box.Empty() {
		clear(pixels.Pix[:4])
		pixels.Pix = pixels.Pix[:4]
		pixels.Stride = 4
		pixels.Rect = image.Rect(0, 0, 1, 1)
		return pixels, nil
	}
	width, height := box.Dx(), box.Dy()
	for row := range height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		start := (box.Min.Y-pixels.Rect.Min.Y+row)*pixels.Stride + (box.Min.X-pixels.Rect.Min.X)*4
		copy(pixels.Pix[row*width*4:], pixels.Pix[start:start+width*4])
	}
	pixels.Pix = pixels.Pix[:width*height*4]
	pixels.Stride = width * 4
	pixels.Rect = box
	return pixels, nil
}
