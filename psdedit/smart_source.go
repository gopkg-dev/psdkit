package psdedit

import (
	"context"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// OpenSmartObjectSource opens a placed layer's embedded PSD/PSB or an explicitly resolved external source.
// The caller closes the result. Unedited embedded results borrow the editor's original source.
// Replacement overlays are copied into an independent source; external documents come from resolver.
// Resolver executes without holding the editor lock and may report unavailable or stale links.
func (e *Editor) OpenSmartObjectSource(
	ctx context.Context, id LayerID, resolver psdfile.LinkedDocumentResolver,
) (*psdfile.Document, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.RLock()
	if err := e.checkOpenLocked(); err != nil {
		e.mu.RUnlock()
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		e.mu.RUnlock()
		return nil, err
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		e.mu.RUnlock()
		return nil, err
	}
	placements, err := e.source.SmartObjects(ctx)
	if err != nil {
		e.mu.RUnlock()
		return nil, err
	}
	sourceID := ""
	for _, placement := range placements {
		if placement.LayerSetIndex == e.source.ActiveLayerSet && placement.LayerIndex == node.sourceIndex {
			if sourceID != "" && sourceID != placement.SourceID {
				e.mu.RUnlock()
				return nil, editError(ErrUnsupported, "smartObject.source", "placement has ambiguous content references")
			}
			sourceID = placement.SourceID
		}
	}
	if sourceID == "" {
		e.mu.RUnlock()
		return nil, editError(ErrUnsupported, "smartObject.source", "layer has no modern content reference")
	}
	limits := e.source.Limits()
	if overlay := e.linkedOverlays[sourceID]; overlay != nil {
		if int64(len(overlay.data)) > limits.MaxDecodeBytes-e.ownedBytesLocked(nil) {
			e.mu.RUnlock()
			return nil, editError(psdfile.ErrLimit, "smartObject.source", "owned child source exceeds the working budget")
		}
		owned := append([]byte(nil), overlay.data...)
		e.mu.RUnlock()
		return psdfile.Parse(owned, psdfile.LoadOptions{Limits: limits})
	}
	linked, err := e.source.LinkedFiles(ctx)
	if err != nil {
		e.mu.RUnlock()
		return nil, err
	}
	var selected *psdfile.LinkedFileRecord
	for index := range linked {
		record := &linked[index]
		if record.ID != sourceID {
			continue
		}
		if selected != nil && (selected.Kind != record.Kind || selected.DataSize != record.DataSize) {
			e.mu.RUnlock()
			return nil, editError(ErrUnsupported, "smartObject.source", "shared content references have ambiguous records")
		}
		selected = record
	}
	if selected == nil {
		e.mu.RUnlock()
		return nil, editError(ErrUnsupported, "smartObject.source", "content reference has no linked source record")
	}
	source := e.source
	record := *selected
	e.mu.RUnlock()
	return source.ResolveLinkedDocument(ctx, record, resolver)
}
