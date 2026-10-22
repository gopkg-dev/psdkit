package psdedit

import (
	"context"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
)

type writerMemoryBudget struct{ limit, total int64 }

func (b *writerMemoryBudget) add(size int64) error {
	if size < 0 || size > b.limit-b.total {
		return editError(psdfile.ErrLimit, "save.working", "retained state and write buffers exceed MaxDecodeBytes")
	}
	b.total += size
	return nil
}

// writeWorkingBudgetLocked reserves retained backing capacity, metadata and streaming scratch.
// Original spans and zero-revision source streaming do not reserve the full canvas or source channels.
// Sample planes and encoder capacity are reserved per phase, allowing compressed output that fits the budget.
func (e *Editor) writeWorkingBudgetLocked(ctx context.Context, option SaveOptions) (int64, int64, error) {
	buffered := option.Composite != nil || option.CompositeImage != nil || e.visualChanged
	var detect func([]*editNode)
	detect = func(nodes []*editNode) {
		for _, node := range nodes {
			buffered = buffered || node.pixels != nil || len(node.planes) != 0 || node.virtualComposite
			detect(node.children)
		}
	}
	detect(e.roots)
	if !buffered {
		return 0, 0, nil
	}
	budget := writerMemoryBudget{limit: e.source.Limits().MaxDecodeBytes}
	if err := budget.add(e.ownedBytesLocked(nil)); err != nil {
		return 0, 0, err
	}
	var visit func([]*editNode) error
	visit = func(nodes []*editNode) error {
		for _, node := range nodes {
			if err := ctx.Err(); err != nil {
				return err
			}
			if node.pixels != nil {
				if err := budget.add(int64(cap(node.pixels.Pix) - len(node.pixels.Pix))); err != nil {
					return err
				}
			}
			for _, plane := range node.planes {
				if err := budget.add(int64(cap(plane.Data) - len(plane.Data))); err != nil {
					return err
				}
			}
			for _, data := range node.additional {
				if err := budget.add(int64(cap(data) - len(data))); err != nil {
					return err
				}
			}
			metadata := int64(2048) + int64(len(node.name))*8
			if node.sourceIndex >= 0 {
				metadata += int64(len(e.source.Layers[node.sourceIndex].AdditionalInfo)) * 512
			}
			if err := budget.add(metadata); err != nil {
				return err
			}
			if err := visit(node.children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(e.roots); err != nil {
		return 0, 0, err
	}
	for _, data := range e.globalAdditional {
		if err := budget.add(int64(cap(data) - len(data))); err != nil {
			return 0, 0, err
		}
	}
	for _, replacement := range e.linkedOverlays {
		if err := budget.add(int64(cap(replacement.data) - len(replacement.data))); err != nil {
			return 0, 0, err
		}
	}
	if e.nativeTextPreview != nil {
		if err := budget.add(int64(cap(e.nativeTextPreview.Pix) - len(e.nativeTextPreview.Pix))); err != nil {
			return 0, 0, err
		}
	}
	metadata := int64(len(e.source.AdditionalInfo)+len(e.source.Resources)+len(e.linkedRecords)) * 512
	if err := budget.add(metadata + (64 << 10)); err != nil {
		return 0, 0, err
	}
	if option.Composite == nil && (option.CompositeImage != nil || e.visualChanged) {
		if err := e.rgbCompositeSupportedLocked(); err != nil {
			return 0, 0, err
		}
	}
	// During automatic composition only this fixed reservation overlaps the renderer's source snapshot.
	reserve := budget.total - e.ownedBytesLocked(nil)
	return budget.total, reserve, nil
}

// writerOutputLimitLocked caps both old and replacement encoder buffers during capacity growth.
func (e *Editor) writerOutputLimitLocked(
	option SaveOptions, retained, samples int64, compression uint16, rowScratch int64,
) (int64, error) {
	fixed := option.writeFixedBytes
	if fixed == 0 {
		fixed = e.ownedBytesLocked(nil)
	}
	remaining := e.source.Limits().MaxDecodeBytes - fixed
	if retained < 0 || samples < 0 || retained > remaining || samples > remaining-retained {
		return 0, editError(
			psdfile.ErrLimit, "save.working", "sample planes and retained encoded buffers exceed MaxDecodeBytes",
		)
	}
	remaining -= retained + samples
	if compression == 2 || compression == 3 {
		scratch := int64(2<<20) + rowScratch
		if scratch > remaining {
			return 0, editError(psdfile.ErrLimit, "save.working", "ZIP working state exceeds remaining budget")
		}
		remaining -= scratch
	}
	// A reallocation may retain its previous backing array while allocating the new one.
	remaining /= 2
	remaining = min(remaining, option.MaxOutputBytes)
	if remaining <= 0 {
		return 0, editError(psdfile.ErrLimit, "save.working", "no encoder capacity remains in MaxDecodeBytes")
	}
	return remaining, nil
}

func writerPlaneSamples(planes []psdfile.Plane) (int64, error) {
	total := int64(0)
	for _, plane := range planes {
		if int64(cap(plane.Data)) > math.MaxInt64-total {
			return 0, editError(psdfile.ErrLimit, "save.working", "sample capacity exceeds integer bounds")
		}
		total += int64(cap(plane.Data))
	}
	return total, nil
}

func writerEncodedShapeBound(rowBytes, rows, channels int64, compression, version uint16) (int64, error) {
	if rowBytes < 0 || rows < 0 || channels < 1 || rows != 0 && rowBytes > math.MaxInt64/rows/channels {
		return 0, editError(psdfile.ErrLimit, "save.working", "encoded sample dimensions exceed integer bounds")
	}
	length := rowBytes * rows * channels
	if length > math.MaxInt64-2 || rowBytes > math.MaxInt64-127 {
		return 0, editError(psdfile.ErrLimit, "save.working", "encoded length exceeds integer bounds")
	}
	if compression == 1 {
		countWidth := int64(2)
		if version == 2 {
			countWidth = 4
		}
		overhead := (rowBytes+127)/128 + countWidth
		if rows != 0 && overhead > (math.MaxInt64-length-2)/rows/channels {
			return 0, editError(psdfile.ErrLimit, "save.working", "RLE table or rows exceed integer bounds")
		}
		return length + overhead*rows*channels + 2, nil
	}
	if compression == 2 || compression == 3 {
		if length > (math.MaxInt64-(64<<10)-2)/2 {
			return 0, editError(psdfile.ErrLimit, "save.working", "ZIP bound exceeds integer bounds")
		}
		return length*2 + (64 << 10) + 2, nil
	}
	return length + 2, nil
}

func writerPlaneEncodedBound(plane psdfile.Plane, compression, version uint16) (int64, error) {
	return writerEncodedShapeBound(int64(plane.RowBytes), int64(plane.Height), 1, compression, version)
}

func writerCompositeEncodedBound(pixels psdfile.CompositePixels, compression, version uint16) (int64, error) {
	if len(pixels.Channels) == 0 {
		return 0, editError(ErrInvalidEdit, "save.composite", "preview has no source planes")
	}
	return writerEncodedShapeBound(
		int64(pixels.Channels[0].RowBytes), int64(pixels.Height), int64(len(pixels.Channels)), compression, version,
	)
}
