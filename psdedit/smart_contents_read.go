package psdedit

import (
	"context"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// ReadSmartObjectContents returns an independently owned copy of a placed layer's current encoded source.
// Embedded and edited sources need no resolver. Unedited external sources require explicit caller-owned access;
// neither retained paths nor saved external caches are used as an implicit source of current external contents.
// PNG/JPEG/TIFF bytes remain in their original format. OpenSmartObjectSource remains the PSD/PSB document API.
// A concurrent edit while resolving external contents returns ErrStaleLinkedEdit instead of stale source bytes.
func (e *Editor) ReadSmartObjectContents(
	ctx context.Context, id LayerID, resolver LinkedContentsResolver,
) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.RLock()
	record, metadata, err := e.smartContentsRecordLocked(ctx, id)
	if err != nil {
		e.mu.RUnlock()
		return nil, err
	}
	if overlay := e.linkedOverlays[record.ID]; overlay != nil {
		data, err := e.copySmartContentsLocked(ctx, overlay.data, metadata)
		e.mu.RUnlock()
		return data, err
	}
	if record.Kind == "liFD" && record.HasData {
		available := e.source.Limits().MaxDecodeBytes - e.ownedBytesLocked(nil) - metadata
		if record.Data.Length > available {
			e.mu.RUnlock()
			return nil, editError(psdfile.ErrLimit, "smartObject.contents", "source copy exceeds the working budget")
		}
		data, err := readSmartPayload(ctx, e.source, record.Data)
		e.mu.RUnlock()
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return data, nil
	}
	if record.Kind != "liFE" || resolver == nil {
		e.mu.RUnlock()
		return nil, editError(ErrUnsupported, "smartObject.contents", "external contents require an explicit resolver")
	}
	revision, source := e.revision, e.source
	e.mu.RUnlock()
	data, err := resolver(ctx, record)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if err := e.checkOpenLocked(); err != nil {
		return nil, err
	}
	if e.revision != revision || e.source != source {
		return nil, ErrStaleLinkedEdit
	}
	return e.copySmartContentsLocked(ctx, data, metadata)
}

func (e *Editor) copySmartContentsLocked(ctx context.Context, data []byte, metadata int64) ([]byte, error) {
	limits := e.source.Limits()
	if int64(len(data)) > limits.MaxFileBytes ||
		int64(len(data)) > limits.MaxDecodeBytes-e.ownedBytesLocked(nil)-metadata {
		return nil, editError(psdfile.ErrLimit, "smartObject.contents", "source copy exceeds the working budget")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	owned := make([]byte, len(data))
	copy(owned, data)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return owned, nil
}

func (e *Editor) smartContentsRecordLocked(
	ctx context.Context, id LayerID,
) (psdfile.LinkedFileRecord, int64, error) {
	empty := psdfile.LinkedFileRecord{}
	if err := e.checkOpenLocked(); err != nil {
		return empty, 0, err
	}
	if err := ctx.Err(); err != nil {
		return empty, 0, err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return empty, 0, err
	}
	available := e.source.Limits().MaxDecodeBytes - e.ownedBytesLocked(nil)
	if available <= 0 {
		return empty, 0, editError(psdfile.ErrLimit, "smartObject.contents", "no metadata working budget remains")
	}
	placements, err := e.source.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return empty, 0, err
	}
	identity := ""
	for _, placement := range placements.Records {
		if placement.LayerSetIndex != e.source.ActiveLayerSet || placement.LayerIndex != node.sourceIndex {
			continue
		}
		if identity != "" && identity != placement.SourceID {
			return empty, 0, editError(ErrUnsupported, "smartObject.contents", "placement has ambiguous source references")
		}
		identity = placement.SourceID
	}
	if identity == "" || node.group {
		return empty, 0, editError(ErrUnsupported, "smartObject.contents", "layer has no modern source reference")
	}
	available -= placements.MetadataBytes
	if available <= 0 {
		return empty, 0, editError(psdfile.ErrLimit, "smartObject.contents", "no linked metadata working budget remains")
	}
	linked, err := e.source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: available})
	if err != nil {
		return empty, 0, err
	}
	var selected *psdfile.LinkedFileRecord
	for index := range linked.Records {
		record := &linked.Records[index]
		if record.ID != identity {
			continue
		}
		if selected != nil {
			return empty, 0, editError(ErrUnsupported, "smartObject.contents", "source identity has duplicate records")
		}
		selected = record
	}
	if selected == nil {
		return empty, 0, editError(ErrUnsupported, "smartObject.contents", "referenced source record is absent")
	}
	return *selected, placements.MetadataBytes + linked.MetadataBytes, ctx.Err()
}
