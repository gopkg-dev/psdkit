package psdfile

import "context"

// RGB8SnapshotMetadataBytes reports a conservative reservation for advanced decoded layer metadata.
// It excludes indexed source metadata and rasters, reading only linked-name structure lengths when present.
func (d *Document) RGB8SnapshotMetadataBytes() (int64, error) {
	return d.RGB8SnapshotMetadataBytesContext(context.Background())
}

// RGB8SnapshotMetadataBytesContext adds cancellation to bounded snapshot metadata preflight.
// Unicode names and embedded media remain unread; only their original declared lengths are inspected.
func (d *Document) RGB8SnapshotMetadataBytesContext(ctx context.Context) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return d.rgb8MetadataBytesLocked(ctx)
}

func (d *Document) rgb8MetadataBytesLocked(ctx context.Context) (int64, error) {
	var total int64
	reserve := func(amount, offset int64) error {
		if amount < 0 || amount > d.limits.MaxDecodeBytes-total {
			return fileError(ErrLimit, "rgba8", "layers.metadata", offset,
				"owned snapshot metadata exceeds the decode budget")
		}
		total += amount
		return nil
	}
	for _, layer := range d.Layers {
		// Account for the independently decoded names, layer nodes and slider storage.
		if err := reserve(int64(len(layer.Name))*4+512+layer.BlendRanges.Length*4, layer.Span.Offset); err != nil {
			return 0, err
		}
		for _, block := range layer.AdditionalInfo {
			factor, overhead := int64(0), int64(512)
			switch block.Key {
			case "brst":
				factor, overhead = 1, 0
			case "vmsk", "vsms":
				// Records, knots and subpath headers expand compact 26-byte geometry records.
				factor = 16
			case "GdFl", "PtFl", "lfx2", "lmfx":
				factor = 32
			case "SoLd", "SoLE", "PlLd":
				// RGB8 snapshots retain typed placement fields, not the temporary full descriptor tree.
				// Coordinates occupy their encoded width and UTF-8 can expand UTF-16 strings by up to 3/2.
				factor, overhead = 2, 256
			case "brit", "levl", "curv", "expA", "nvrt", "post", "thrs", "mixr", "blnc", "hue2", "hue ", "phfl":
				factor, overhead = 8, 16<<10
			default:
				continue
			}
			remaining := d.limits.MaxDecodeBytes - total
			if overhead > remaining || block.Data.Length > (remaining-overhead)/factor {
				return 0, fileError(ErrLimit, "rgba8", "layers.additional", block.Data.Offset,
					"advanced snapshot metadata exceeds the decode budget")
			}
			total += overhead + block.Data.Length*factor
		}
	}
	for _, resource := range d.Resources {
		if resource.ID != 1050 {
			continue
		}
		if resource.Data.Length > (d.limits.MaxDecodeBytes-total)/16 {
			return 0, fileError(ErrLimit, "rgba8", "slices.metadata", resource.Data.Offset,
				"slice metadata exceeds the decode budget")
		}
		if err := reserve(resource.Data.Length*16, resource.Data.Offset); err != nil {
			return 0, err
		}
	}
	linked, err := d.rgb8LinkedMetadataBytesLocked(ctx, d.limits.MaxDecodeBytes-total)
	if err != nil {
		return 0, err
	}
	return total + linked, nil
}
