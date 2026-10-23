package psdedit

import (
	"bytes"
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// applyPixelMaskChange creates or removes the effective user mask before group or leaf rendering.
func applyPixelMaskChange(ctx context.Context, layer *psdmodel.Layer, change *pixelMaskChange) error {
	if change == nil {
		return nil
	}
	if change.removed {
		layer.Mask = nil
		return nil
	}
	plane := change.plane
	if plane == nil || plane.Depth != 8 || plane.Width != change.bounds.Dx() ||
		plane.Height != change.bounds.Dy() || plane.RowBytes != plane.Width ||
		int64(plane.Width)*int64(plane.Height) != int64(len(plane.Data)) {
		return editError(ErrUnsupported, "layers.pixelMask", "live mask overlays require compact RGB8 coverage")
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, plane.Width, plane.Height))
	for row := range plane.Height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for column := range plane.Width {
			value := plane.Data[row*plane.RowBytes+column]
			offset := row*pixels.Stride + column*4
			pixels.Pix[offset], pixels.Pix[offset+1], pixels.Pix[offset+2], pixels.Pix[offset+3] = value, value, value, 255
		}
	}
	layer.Mask = &psdmodel.Mask{Bounds: change.bounds, Pixels: pixels}
	return nil
}

func (editor *Editor) replaceAddedMaskChannelLocked(node *editNode, channelID int16, plane psdfile.Plane) error {
	change := node.pixelMaskChange
	if change.removed || change.plane == nil {
		return editError(ErrInvalidEdit, "layers.channels", "the user mask channel has been removed")
	}
	if plane.ID != channelID || plane.Depth != editor.effectiveDepthLocked() ||
		plane.Width != change.bounds.Dx() || plane.Height != change.bounds.Dy() {
		return editError(ErrInvalidEdit, "layers.channels.shape", "mask channel identifier, depth or dimensions differ")
	}
	length, err := editor.checkPlaneLocked(plane)
	if err != nil {
		return err
	}
	if bytes.Equal(change.plane.Data, plane.Data) {
		return nil
	}
	// Retain the previous mask buffer until the replacement is fully owned and ready to commit.
	if length > editor.source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil) {
		return editError(psdfile.ErrLimit, "layers.channels", "mask channel replacement exceeds the peak working budget")
	}
	owned := make([]byte, len(plane.Data))
	copy(owned, plane.Data)
	plane.Data = owned
	change.plane = &plane
	editor.markChangedLocked(false)
	return nil
}
