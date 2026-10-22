package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"maps"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

// SmartObjectInfo identifies an editable modern placed layer and its shared content reference.
// SourceID is an opaque file identity; callers should avoid exposing it in logs.
type SmartObjectInfo struct {
	ID                 LayerID
	Name, SourceID     string
	Bounds             image.Rectangle
	Width, Height      float64
	SourceType         int32
	SourceKind         string
	Embedded, Replaced bool
	SharedInstances    []LayerID
}

// SmartObjects returns owned placement views for the editor's active source layer set.
func (e *Editor) SmartObjects(ctx context.Context) ([]SmartObjectInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if err := e.checkOpenLocked(); err != nil {
		return nil, err
	}
	placements, err := e.source.SmartObjects(ctx)
	if err != nil {
		return nil, err
	}
	linked, err := e.source.LinkedFiles(ctx)
	if err != nil {
		return nil, err
	}
	byIndex := make(map[int]psdfile.SmartObjectRecord)
	for _, placement := range placements {
		if placement.LayerSetIndex == e.source.ActiveLayerSet {
			byIndex[placement.LayerIndex] = placement
		}
	}
	var result []SmartObjectInfo
	var visit func([]*editNode)
	visit = func(nodes []*editNode) {
		for _, node := range nodes {
			if placement, exists := byIndex[node.sourceIndex]; exists && !node.group {
				info := SmartObjectInfo{ID: node.id, Name: node.name, Bounds: node.bounds,
					SourceID: placement.SourceID, Width: placement.Width, Height: placement.Height,
					SourceType: placement.SourceType, Replaced: e.linkedOverlays[placement.SourceID] != nil}
				if node.placed != nil {
					info.Width, info.Height = float64(node.placed.Width), float64(node.placed.Height)
				}
				for _, record := range linked {
					if record.ID == placement.SourceID {
						info.SourceKind, info.Embedded = record.Kind, record.Kind == "liFD"
						if overlay := e.linkedOverlays[placement.SourceID]; overlay != nil && overlay.embedExternal {
							info.SourceKind, info.Embedded = "liFD", true
						}
						break
					}
				}
				result = append(result, info)
			}
			visit(node.children)
		}
	}
	visit(e.roots)
	peers := make(map[string][]LayerID)
	metadataBytes := int64(len(result)) * 256
	for _, item := range result {
		peers[item.SourceID] = append(peers[item.SourceID], item.ID)
	}
	for index := range result {
		shared := peers[result[index].SourceID]
		bytesNeeded := int64(len(shared)) * 8
		if bytesNeeded > e.source.Limits().MaxMetadataBytes-metadataBytes {
			return nil, editError(psdfile.ErrLimit, "smartObjects.instances", "shared-instance views exceed metadata budget")
		}
		metadataBytes += bytesNeeded
		result[index].SharedInstances = append([]LayerID(nil), shared...)
	}
	return result, ctx.Err()
}

// ReplaceContents replaces an embedded RGB8 PSD/PSB source and updates all its shared placed instances.
// Encoded input and projected caches are copied into owned overlays; original blocks remain immutable.
// Destination corners remain fixed when the prototype canvas changes size.
// Source-space custom controls are scaled per axis; live filters and unsupported Warp are rejected.
// Photoshop's default Replace Contents instead retains pixel scale and the placement center.
func (e *Editor) ReplaceContents(ctx context.Context, id LayerID, data []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.replaceContentsLocked(ctx, id, data, false)
}

// SmartContentsOptions explicitly selects rendering policies for replacement source pixels and caches.
type SmartContentsOptions struct {
	// ApproximateCustomWarp permits the certified mathematical cubic-plus-homography model.
	// It does not claim Photoshop pixel equivalence or enable unsupported quilt geometry.
	ApproximateCustomWarp bool
	// AssumeUntaggedSRGB interprets an untagged replacement as sRGB before conversion to the parent profile.
	AssumeUntaggedSRGB bool
}

// ReplaceContentsWithOptions replaces shared embedded contents using explicit rendering policies.
// The zero value has the same strict behavior and transactional guarantees as ReplaceContents.
func (e *Editor) ReplaceContentsWithOptions(
	ctx context.Context, id LayerID, data []byte, options SmartContentsOptions,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.replaceSmartContentsWithOptionsLocked(ctx, id, data, false, nil, options)
}

func (e *Editor) replaceContentsLocked(ctx context.Context, id LayerID, data []byte, embedExternal bool) error {
	return e.replaceSmartContentsLocked(ctx, id, data, embedExternal, nil)
}

func (e *Editor) replaceSmartContentsLocked(
	ctx context.Context, id LayerID, data []byte, embedExternal bool, external *LinkedContentsUpdate,
) error {
	return e.replaceSmartContentsWithOptionsLocked(ctx, id, data, embedExternal, external, SmartContentsOptions{})
}

func (e *Editor) replaceSmartContentsWithOptionsLocked(
	ctx context.Context, id LayerID, data []byte, embedExternal bool,
	external *LinkedContentsUpdate, options SmartContentsOptions,
) error {
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	limits := e.source.Limits()
	viewBudget := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	if viewBudget <= 0 {
		return editError(psdfile.ErrLimit, "smartObject.metadata", "no working budget remains for metadata views")
	}
	placementView, err := e.source.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: viewBudget})
	if err != nil {
		return err
	}
	placements := placementView.Records
	byIndex := make(map[int]psdfile.SmartObjectRecord)
	byLayer := make(map[int][]psdfile.SmartObjectRecord)
	for _, placement := range placements {
		if placement.LayerSetIndex == e.source.ActiveLayerSet {
			byIndex[placement.LayerIndex] = placement
			byLayer[placement.LayerIndex] = append(byLayer[placement.LayerIndex], placement)
		}
	}
	placement, exists := byIndex[target.sourceIndex]
	if !exists || target.group || placement.SourceType != 2 {
		return editError(ErrUnsupported, "smartObject", "layer is not a modern raster smart-object instance")
	}
	for _, record := range placements {
		if record.LayerSetIndex != e.source.ActiveLayerSet && record.SourceID == placement.SourceID {
			return editError(ErrUnsupported, "smartObject.instances", "shared content is referenced by an inactive layer set")
		}
	}
	for _, records := range byLayer {
		shared := false
		for _, record := range records {
			shared = shared || record.SourceID == placement.SourceID
		}
		if !shared {
			continue
		}
		for _, record := range records {
			if record.SourceID != placement.SourceID || record.SourceType != placement.SourceType ||
				record.Width != placement.Width || record.Height != placement.Height {
				return editError(ErrUnsupported, "smartObject.instances", "placement blocks have ambiguous shared semantics")
			}
		}
	}
	viewBudget -= placementView.MetadataBytes
	if viewBudget <= 0 {
		return editError(psdfile.ErrLimit, "smartObject.metadata", "no working budget remains for linked-file views")
	}
	linkedView, err := e.source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: viewBudget})
	if err != nil {
		return err
	}
	linked := linkedView.Records
	found := false
	for _, record := range linked {
		if record.ID != placement.SourceID {
			continue
		}
		if found {
			return editError(ErrUnsupported, "smartObject.source", "duplicate embedded source references are ambiguous")
		}
		overlay := e.linkedOverlays[record.ID]
		embedded := record.Kind == "liFD" || overlay != nil && overlay.embedExternal
		if (!embedded && ((!embedExternal && external == nil) || record.Kind != "liFE")) ||
			(record.FileType != "8BPS" && record.FileType != "8BPB") {
			return editError(ErrUnsupported, "smartObject.source", "content updates require an embedded PSD/PSB source")
		}
		found = true
		placement.SourceID = record.ID
	}
	if !found {
		return editError(ErrInvalidEdit, "smartObject.source", "referenced embedded source is absent")
	}
	if embedExternal {
		if err := validateExternalEmbed(e.source, placement.SourceID, linked); err != nil {
			return err
		}
	}
	if external != nil {
		if previous := e.linkedOverlays[placement.SourceID]; previous != nil && previous.retainExternal &&
			external.Timestamp == nil && previous.timestamp != nil {
			value := *previous.timestamp
			external.Timestamp = &value
		}
		if _, err := validateLinkedUpdate(placement.SourceID, linked, external.Timestamp); err != nil {
			return err
		}
	}
	if len(data) >= 26 && string(data[:4]) == "8BPS" &&
		float64(binary.BigEndian.Uint32(data[18:22])) == placement.Width &&
		float64(binary.BigEndian.Uint32(data[14:18])) == placement.Height {
		// Equal-size contents need only source identities; release full descriptors before source indexing.
		placementView.MetadataBytes = int64(len(byIndex))*512 + 256
		for index, record := range byIndex {
			record.Descriptor, record.Warp, record.Transform, record.NonAffine = nil, nil, nil, nil
			byIndex[index] = record
			placementView.MetadataBytes += int64(len(record.SourceID))
		}
		placementView.Records, byLayer = nil, nil
		placement.Descriptor, placement.Warp, placement.Transform, placement.NonAffine = nil, nil, nil, nil
	}
	var nodes []*editNode
	var collect func([]*editNode)
	collect = func(items []*editNode) {
		for _, node := range items {
			if peer, ok := byIndex[node.sourceIndex]; ok && peer.SourceID == placement.SourceID && !node.group {
				nodes = append(nodes, node)
			}
			collect(node.children)
		}
	}
	collect(e.roots)
	ownedBytes := e.ownedBytesLocked(nil) - e.linkedRecordsBytes
	if previous := e.linkedOverlays[placement.SourceID]; previous != nil {
		ownedBytes -= linkedReplacementOwnedBytes(previous)
	}
	if external != nil && external.Timestamp != nil {
		ownedBytes += 64
	}
	for _, node := range nodes {
		if len(node.planes) != 0 {
			return editError(ErrInvalidEdit, "smartObject.channels", "source updates cannot mix raw channel overlays")
		}
		if node.pixels != nil {
			ownedBytes -= int64(len(node.pixels.Pix))
		}
		ownedBytes -= node.semanticCosts["placed"]
		for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
			if smartPlacementBlock(block.Key) {
				ownedBytes -= int64(len(node.additional[block.Span.Offset]))
			}
		}
	}
	if len(data) < 26 || int64(len(data)) > limits.MaxFileBytes ||
		int64(len(data)) > limits.MaxDecodeBytes-ownedBytes {
		return editError(psdfile.ErrLimit, "smartObject.content", "encoded replacement exceeds the owned-content budget")
	}
	viewBytes := placementView.MetadataBytes + linkedView.MetadataBytes
	if external != nil && external.Timestamp != nil {
		viewBytes += 64
	}
	parseBudget := limits.MaxDecodeBytes - e.ownedBytesLocked(nil) - viewBytes - int64(len(data))
	if parseBudget <= 0 {
		return editError(psdfile.ErrLimit, "smartObject.working", "metadata and encoded replacement exceed working budget")
	}
	owned := bytes.Clone(data)
	parseLimits := limits
	parseLimits.MaxMetadataBytes = min(limits.MaxMetadataBytes, parseBudget)
	content, err := psdfile.Parse(owned, psdfile.LoadOptions{Limits: parseLimits})
	if err != nil {
		return err
	}
	defer func() { _ = content.Close() }()
	if content.Header.ColorMode != 3 || content.Header.Depth != 8 {
		return editError(ErrUnsupported, "smartObject.content", "replacement must use an RGB8 prototype canvas")
	}
	remaining, err := e.smartSnapshotBudgetLocked(ctx)
	if err != nil {
		return err
	}
	indexBytes := content.IndexMetadataBytes()
	if viewBytes > remaining || int64(len(owned)) >= remaining-viewBytes ||
		indexBytes >= remaining-viewBytes-int64(len(owned)) {
		return editError(psdfile.ErrLimit, "smartObject.working", "replacement index and source exceed working budget")
	}
	workingBytes := limits.MaxDecodeBytes - remaining + int64(len(owned)) + indexBytes + viewBytes
	if float64(content.Header.Width) != placement.Width || float64(content.Header.Height) != placement.Height {
		for _, node := range nodes {
			for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
				if block.Key != "PlLd" && block.Key != "plLd" {
					continue
				}
				length := min(block.Data.Length, 352)
				if length > limits.MaxDecodeBytes-workingBytes {
					return editError(psdfile.ErrLimit, "smartObject.legacy", "legacy header exceeds working budget")
				}
				header, err := readSmartPayload(ctx, e.source, psdfile.Span{Offset: block.Data.Offset, Length: length})
				if err != nil {
					return err
				}
				if _, err := smartLegacyHeader(header); err != nil {
					return err
				}
			}
		}
	}
	// Projection needs placement metadata, never the original raster caches or merged preview.
	canvas := image.Rect(0, 0, int(e.source.Header.Width), int(e.source.Header.Height))
	var physical []*psdmodel.Layer
	if limits.MaxDecodeBytes-workingBytes < 128<<10 {
		// Tiny existing budgets can fit the legacy decoder but not the metadata reader's 64 KiB scratch.
		snapshot, err := e.source.Decode(ctx)
		if err != nil {
			return err
		}
		physical = flattenSnapshot(snapshot.Layers)
	}
	projectionLayers := make([]psdmodel.Layer, len(nodes))
	projected := make([]*image.NRGBA, len(nodes))
	bounds := make([]image.Rectangle, len(nodes))
	updates := make([]smartPlacementUpdate, len(nodes))
	budget := ownedBytes + int64(len(owned)) + linkedView.MetadataBytes
	for index, node := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
			return editError(ErrInvalidEdit, "smartObject.instance", "instance has no original placement")
		}
		if peer := byIndex[node.sourceIndex]; peer.SourceType != 2 ||
			peer.Width != placement.Width || peer.Height != placement.Height {
			return editError(ErrUnsupported, "smartObject.instances", "shared placements have incompatible prototype semantics")
		}
		var layer psdmodel.Layer
		if physical != nil {
			if node.sourceIndex >= len(physical) || physical[node.sourceIndex] == nil {
				return editError(ErrInvalidEdit, "smartObject.instance", "instance has no original placement")
			}
			layer = *physical[node.sourceIndex]
		} else {
			view, err := e.source.ReadLayerMetadata(ctx, node.sourceIndex,
				psdfile.MetadataOptions{MaxBytes: limits.MaxDecodeBytes - workingBytes})
			if err != nil {
				return err
			}
			layer = *view.Layer
		}
		if layer.Placed == nil || len(layer.Placed.Unsupported) != 0 ||
			(psdrender.RequiresWarpRendering(layer.Placed) && layer.Placed.Warp.Style != "custom") {
			return editError(ErrUnsupported, "smartObject.warp", "instance needs unsupported Warp or smart-filter rendering")
		}
		update, err := prepareSmartPlacement(ctx, e.source, layer.Placed, byLayer[node.sourceIndex],
			e.source.Layers[node.sourceIndex].AdditionalInfo, image.Pt(int(content.Header.Width), int(content.Header.Height)),
			min(limits.MaxDecodeBytes-workingBytes, limits.MaxDecodeBytes-budget))
		if err != nil {
			return err
		}
		updates[index] = update
		projectionLayers[index] = psdmodel.Layer{Placed: layer.Placed, Bounds: layer.Bounds}
		if update.placed != nil {
			projectionLayers[index].Placed = update.placed
		} else {
			workingBytes += smartProjectionMetadataBytes(layer.Placed)
		}
		workingBytes += 128
		metadataCost := update.cost
		for _, payload := range update.blocks {
			metadataCost += int64(len(payload))
		}
		budget += metadataCost
		workingBytes += metadataCost
	}
	// Descriptor trees are only needed for validated numeric patches, never for source rendering.
	workingBytes -= placementView.MetadataBytes
	placementView.Records, byIndex = nil, nil
	placement.Descriptor, placement.Warp, placement.Transform, placement.NonAffine = nil, nil, nil, nil
	// Only typed projection geometry remains live; release the conservative source snapshot reservation.
	workingBytes -= limits.MaxDecodeBytes - remaining - e.ownedBytesLocked(nil)
	if workingBytes >= limits.MaxDecodeBytes {
		return editError(psdfile.ErrLimit, "smartObject.prototype", "no working budget remains for source rendering")
	}
	parentProfile, err := psdscene.ReadSourceRGBProfile(ctx, e.source, limits.MaxDecodeBytes-workingBytes)
	if err != nil {
		return err
	}
	if parentProfile == nil {
		parentProfile = []byte{}
	}
	workingBytes += int64(len(parentProfile))
	if workingBytes >= limits.MaxDecodeBytes {
		return editError(psdfile.ErrLimit, "smartObject.profile", "no working budget remains after the parent profile")
	}
	pixels, err := psdscene.Render(ctx, content, psdscene.Options{
		MaxWorkingBytes:    limits.MaxDecodeBytes - workingBytes,
		OutputProfile:      parentProfile,
		AssumeUntaggedSRGB: options.AssumeUntaggedSRGB,
	})
	if err != nil {
		return compositeRenderError(err)
	}
	workingBytes -= int64(len(parentProfile))
	workingBytes += int64(len(pixels.Pix))
	for index := range nodes {
		if err := ctx.Err(); err != nil {
			return err
		}
		layer := projectionLayers[index]
		available := min(limits.MaxDecodeBytes-workingBytes, limits.MaxDecodeBytes-budget)
		if available <= 0 {
			return editError(psdfile.ErrLimit, "smartObject.instances", "projected caches exceed owned-content budget")
		}
		var value *image.NRGBA
		var origin image.Point
		if layer.Placed.Warp != nil && layer.Placed.Warp.Style == "custom" {
			compile := psdwarp.Compile
			if options.ApproximateCustomWarp {
				compile = psdwarp.CompileMathematical
			}
			patch, compileErr := compile(ctx, layer.Placed, psdwarp.Options{
				MaxBytes: available, MaxPixels: limits.MaxDecodePixels,
			})
			if compileErr != nil {
				return smartProjectionError(compileErr)
			}
			value, origin, err = patch.Project(ctx, pixels, canvas)
		} else {
			plan, compileErr := psdrender.CompileProjection(&layer, canvas.Dx(), canvas.Dy(), false)
			if compileErr != nil {
				return errors.Join(ErrUnsupported, compileErr)
			}
			allocation := int64(plan.Width) * int64(plan.Height) * 4
			if allocation > available || allocation > math.MaxInt {
				return editError(psdfile.ErrLimit, "smartObject.instances", "projected caches exceed owned-content budget")
			}
			value, origin, err = psdrender.ProjectPlacedContentsContext(ctx, &layer, pixels, plan)
		}
		if err != nil {
			return smartProjectionError(err)
		}
		projected[index] = value
		bounds[index] = image.Rectangle{Min: origin, Max: origin.Add(value.Rect.Size())}
		budget += int64(len(value.Pix))
		workingBytes += int64(len(value.Pix))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.linkedOverlays == nil {
		e.linkedOverlays = make(map[string]*linkedReplacement)
	}
	e.linkedRecords = linked
	e.linkedRecordsBytes = linkedView.MetadataBytes
	fileType := "8BPS"
	if content.Header.Version == 2 {
		fileType = "8BPB"
	}
	if previous := e.linkedOverlays[placement.SourceID]; previous != nil && previous.embedExternal {
		embedExternal = true
	}
	e.linkedOverlays[placement.SourceID] = &linkedReplacement{
		data: owned, fileType: fileType, embedExternal: embedExternal,
	}
	if external != nil {
		e.linkedOverlays[placement.SourceID].retainExternal = true
		e.linkedOverlays[placement.SourceID].timestamp = external.Timestamp
	}
	for index, node := range nodes {
		node.pixels, node.bounds = projected[index], bounds[index]
		node.placed = updates[index].placed
		node.setSemanticCost("placed", updates[index].cost)
		for _, block := range e.source.Layers[node.sourceIndex].AdditionalInfo {
			if smartPlacementBlock(block.Key) {
				delete(node.additional, block.Span.Offset)
			}
		}
		if len(updates[index].blocks) != 0 && node.additional == nil {
			node.additional = make(map[int64][]byte, len(updates[index].blocks))
		}
		maps.Copy(node.additional, updates[index].blocks)
	}
	e.markChangedLocked(false)
	return nil
}

// ReplaceSmartObjectContents is an explicit alias for ReplaceContents.
func (e *Editor) ReplaceSmartObjectContents(ctx context.Context, id LayerID, data []byte) error {
	return e.ReplaceContents(ctx, id, data)
}

func smartPlacementBlock(key string) bool {
	return key == "SoLd" || key == "SoLE" || key == "PlLd" || key == "plLd"
}

// smartSnapshotBudgetLocked reserves only the original snapshot used to validate and project placements.
// Replacement does not allocate the edited composition tree or planar merged preview.
func (e *Editor) smartSnapshotBudgetLocked(ctx context.Context) (int64, error) {
	limits := e.source.Limits()
	area := int64(e.source.Header.Width) * int64(e.source.Header.Height)
	used := e.ownedBytesLocked(nil)
	metadata, err := e.source.RGB8SnapshotMetadataBytesContext(ctx)
	if err != nil {
		return 0, err
	}
	reserve := func(amount int64) error {
		if amount < 0 || amount > limits.MaxDecodeBytes-used {
			return editError(psdfile.ErrLimit, "smartObject.snapshot", "source snapshot exceeds the working budget")
		}
		used += amount
		return nil
	}
	if area > limits.MaxDecodePixels {
		return 0, editError(psdfile.ErrLimit, "smartObject.snapshot", "source canvas exceeds the pixel budget")
	}
	if err := reserve(area*4 + metadata + int64(len(e.source.Layers))*8); err != nil {
		return 0, err
	}
	reserveRectangle := func(bounds image.Rectangle) error {
		width, height := int64(bounds.Dx()), int64(bounds.Dy())
		if width < 0 || height < 0 || width > psdmodel.MaxDimension || height > psdmodel.MaxDimension ||
			width*height > limits.MaxDecodePixels {
			return editError(psdfile.ErrLimit, "smartObject.snapshot", "source layer exceeds the pixel budget")
		}
		return reserve(width * height * 4)
	}
	for _, record := range e.source.Layers {
		if err := reserve(512); err != nil {
			return 0, err
		}
		if err := reserveRectangle(record.Bounds); err != nil {
			return 0, err
		}
		if record.Mask == nil {
			continue
		}
		if err := reserveRectangle(record.Mask.Bounds); err != nil {
			return 0, err
		}
		if record.Mask.HasReal {
			if err := reserveRectangle(record.Mask.RealBounds); err != nil {
				return 0, err
			}
		}
	}
	return limits.MaxDecodeBytes - used, nil
}

func smartProjectionMetadataBytes(placed *psdmodel.Placed) int64 {
	amount := int64(512 + len(placed.ID) + len(placed.Type) +
		(len(placed.Transform)+len(placed.NonAffineTransform))*8)
	amount += smartMetadataStringBytes(placed.Unsupported)
	if placed.Warp != nil {
		amount += int64(512+len(placed.Warp.Values)*8+len(placed.Warp.MeshPoints)*16) +
			warpMetadataStringBytes(placed.Warp) + warpQuiltMetadataBytes(placed.Warp)
	}
	return amount
}

func smartMetadataStringBytes(values []string) int64 {
	amount := int64(cap(values)) * 16
	for _, value := range values {
		amount += int64(len(value))
	}
	return amount
}

func warpMetadataStringBytes(warp *psdmodel.Warp) int64 {
	amount := int64(len(warp.ClassID) + len(warp.Style) + len(warp.Rotate) + len(warp.StyleEnum) + len(warp.RotateEnum))
	amount += int64(len(warp.MeshClass))
	amount += smartMetadataStringBytes(warp.Fields) + smartMetadataStringBytes(warp.Kinds) +
		smartMetadataStringBytes(warp.Unsupported)
	for _, unit := range warp.MeshUnits {
		amount += int64(len(unit))
	}
	if warp.Bounds != nil {
		for _, unit := range warp.Bounds.Units {
			amount += int64(len(unit))
		}
	}
	return amount
}

func warpQuiltMetadataBytes(warp *psdmodel.Warp) int64 {
	amount := int64(cap(warp.QuiltSlices)) * int64(unsafe.Sizeof(psdmodel.WarpSlice{}))
	for _, slice := range warp.QuiltSlices {
		amount += int64(len(slice.Axis) + len(slice.Location) + len(slice.ClassID) +
			len(slice.Field) + len(slice.Kind) + len(slice.Unit))
		amount += int64(cap(slice.Values)) * 8
	}
	return amount
}

func smartProjectionError(err error) error {
	if errors.Is(err, psdwarp.ErrLimit) {
		return errors.Join(psdfile.ErrLimit, err)
	}
	if errors.Is(err, psdwarp.ErrUnsupported) || errors.Is(err, psdwarp.ErrInvalid) ||
		errors.Is(err, psdrender.ErrUnsupportedCrop) {
		return errors.Join(ErrUnsupported, err)
	}
	return err
}
