package psdedit

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// New creates an editor borrowing an immutable source; closing the editor does not close the source.
func New(source *psdfile.Document) (*Editor, error) {
	if source == nil {
		return nil, editError(ErrInvalidEdit, "source", "source document is required")
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	editor := &Editor{source: source, nextID: LayerID(len(source.Layers)) + 1, nextPSDID: 1}
	if err := editor.initializePSDIDs(); err != nil {
		return nil, err
	}
	type groupFrame struct {
		end      int
		children []*editNode
	}
	var frames []groupFrame
	appendNode := func(node *editNode) {
		if len(frames) == 0 {
			editor.roots = append(editor.roots, node)
			return
		}
		frame := &frames[len(frames)-1]
		frame.children = append(frame.children, node)
	}
	for index, layer := range source.Layers {
		if layer.Divider == 3 {
			frames = append(frames, groupFrame{end: index})
			continue
		}
		node := &editNode{
			id: LayerID(index + 1), sourceIndex: index, endSourceIndex: -1, psdID: layer.ID,
			name: layer.Name, visible: layer.Flags&2 == 0, opacity: layer.Opacity, bounds: layer.Bounds,
		}
		if layer.Divider == 1 || layer.Divider == 2 {
			if len(frames) == 0 {
				return nil, editError(ErrInvalidEdit, "layers.groups", "group header has no preceding end marker")
			}
			frame := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			node.group, node.expanded = true, layer.Divider == 1
			node.children, node.endSourceIndex = frame.children, frame.end
			node.endPSDID = source.Layers[frame.end].ID
		}
		appendNode(node)
	}
	if len(frames) != 0 {
		return nil, editError(ErrInvalidEdit, "layers.groups", "group end marker has no following header")
	}
	if len(source.Layers) == 0 && source.Header.ColorMode == 3 && source.Header.Depth == 8 &&
		source.Header.Channels == 3 {
		psdID, err := editor.reservePSDIDsLocked(1)
		if err != nil {
			// An exhausted source can still be inspected and copied without materializing the virtual background.
			psdID = 0
		}
		editor.roots = []*editNode{{
			id: editor.nextID, sourceIndex: -2, endSourceIndex: -1, psdID: psdID,
			name: "Background", visible: true, opacity: 255, virtualComposite: true,
			bounds: image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height)),
		}}
		editor.nextID++
	}
	return editor, nil
}

// Open indexes an owned file and closes that source when the returned editor is closed.
func Open(filename string, options ...psdfile.LoadOptions) (*Editor, error) {
	source, err := psdfile.Open(filename, options...)
	if err != nil {
		return nil, err
	}
	editor, err := New(source)
	if err != nil {
		return nil, errors.Join(err, source.Close())
	}
	editor.ownsSource = true
	return editor, nil
}

// Close waits for ongoing editor operations, then releases any owned source; repeated calls are safe.
func (editor *Editor) Close() error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if editor.closed {
		return nil
	}
	editor.closed = true
	editor.roots = nil
	editor.linkedOverlays = nil
	editor.linkedRecords = nil
	editor.linkedRecordsBytes = 0
	editor.globalAdditional = nil
	editor.nativeTextPreview = nil
	if editor.ownsSource {
		return editor.source.Close()
	}
	return nil
}

// Layers returns owned flat metadata in physical bottom-to-top order, excluding group end markers.
func (editor *Editor) Layers() ([]LayerInfo, error) {
	editor.mu.RLock()
	defer editor.mu.RUnlock()
	if err := editor.checkOpenLocked(); err != nil {
		return nil, err
	}
	var layers []LayerInfo
	var visit func([]*editNode, LayerID) error
	visit = func(nodes []*editNode, parent LayerID) error {
		for _, node := range nodes {
			if err := visit(node.children, node.id); err != nil {
				return err
			}
			info, err := editor.layerInfoLocked(node, parent, false)
			if err != nil {
				return err
			}
			layers = append(layers, info)
		}
		return nil
	}
	if err := visit(editor.roots, 0); err != nil {
		return nil, err
	}
	return layers, nil
}

// Layer returns owned metadata for one stable editor handle, including its descendant tree.
func (editor *Editor) Layer(id LayerID) (LayerInfo, error) {
	editor.mu.RLock()
	defer editor.mu.RUnlock()
	if err := editor.checkOpenLocked(); err != nil {
		return LayerInfo{}, err
	}
	node, parent, _, err := editor.findNodeLocked(id)
	if err != nil {
		return LayerInfo{}, err
	}
	var parentID LayerID
	if parent != nil {
		parentID = parent.id
	}
	return editor.layerInfoLocked(node, parentID, true)
}

// Tree returns an owned hierarchy whose sibling arrays are ordered bottom to top.
func (editor *Editor) Tree() ([]LayerInfo, error) {
	editor.mu.RLock()
	defer editor.mu.RUnlock()
	if err := editor.checkOpenLocked(); err != nil {
		return nil, err
	}
	nodes := make([]LayerInfo, len(editor.roots))
	for index, node := range editor.roots {
		info, err := editor.layerInfoLocked(node, 0, true)
		if err != nil {
			return nil, err
		}
		nodes[index] = info
	}
	return nodes, nil
}

// Changes returns revisions and persistent differences from the original source.
func (editor *Editor) Changes() (Changes, error) {
	editor.mu.RLock()
	defer editor.mu.RUnlock()
	if err := editor.checkOpenLocked(); err != nil {
		return Changes{}, err
	}
	return Changes{
		Revision: editor.revision, SavedRevision: editor.savedRevision,
		Dirty:            editor.revision != editor.savedRevision,
		CompositeChanged: editor.visualChanged, StructureChanged: editor.structureChanged,
	}, nil
}

// Rename changes one layer's Unicode name; an identical name leaves its revision unchanged.
func (editor *Editor) Rename(id LayerID, name string) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	if err := editor.checkNameLocked(name); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.name == name {
		return nil
	}
	node.name, node.nameChanged = name, true
	editor.revision++
	if editor.nativeTextPreview != nil {
		editor.nativeTextPreviewRevision = editor.revision
	}
	return nil
}

// SetVisible changes the layer's visibility without changing its source flags.
func (editor *Editor) SetVisible(id LayerID, visible bool) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.visible == visible {
		return nil
	}
	node.visible, node.visibleChanged = visible, true
	editor.markChangedLocked(false)
	return nil
}

// SetOpacity changes the layer's byte opacity without modifying its source record.
func (editor *Editor) SetOpacity(id LayerID, opacity uint8) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.opacity == opacity {
		return nil
	}
	node.opacity, node.opacityChanged = opacity, true
	editor.markChangedLocked(false)
	return nil
}

// ReplacePixels copies straight-alpha RGB8 raster pixels, retaining the layer's dimensions and position.
// Existing masks and extra source channels remain untouched; channel overlays cannot be mixed with this edit.
func (editor *Editor) ReplacePixels(id LayerID, pixels *image.NRGBA) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	if err := editor.checkRGBRasterLocked(node); err != nil {
		return err
	}
	if len(node.planes) != 0 {
		return editError(ErrInvalidEdit, "layers.pixels", "RGBA and channel replacements cannot be mixed")
	}
	owned, err := editor.copyImageLocked(pixels, node)
	if err != nil {
		return err
	}
	if owned.Bounds().Dx() != node.bounds.Dx() || owned.Bounds().Dy() != node.bounds.Dy() {
		return editError(ErrUnsupported, "layers.pixels.bounds", "replacement dimensions must match the layer")
	}
	if node.pixels != nil && bytes.Equal(node.pixels.Pix, owned.Pix) {
		return nil
	}
	node.pixels = owned
	editor.markChangedLocked(false)
	return nil
}

// ReplaceChannel copies one existing raster channel at its source precision and original geometry.
// RowBytes and Data must use the compact source layout, including packed MSB-first bitmap rows.
// User-mask channels created by AddPixelMask can also be replaced, including on new layers and groups.
// Removed user-mask channels must be recreated with AddPixelMask before their samples can be replaced.
func (editor *Editor) ReplaceChannel(id LayerID, channelID int16, plane psdfile.Plane) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	node, _, _, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	if node.pixelMaskChange != nil && channelID == node.pixelMaskChange.channelID {
		return editor.replaceAddedMaskChannelLocked(node, channelID, plane)
	}
	if node.group || node.sourceIndex < 0 || editor.source.Layers[node.sourceIndex].Divider != 0 ||
		editor.semanticRasterLocked(node) {
		return editError(ErrUnsupported, "layers.channels", "replacement requires an existing ordinary raster")
	}
	if node.pixels != nil {
		return editError(ErrInvalidEdit, "layers.channels", "RGBA and channel replacements cannot be mixed")
	}
	var channel *psdfile.ChannelRecord
	for index := range editor.source.Layers[node.sourceIndex].Channels {
		candidate, retained := effectiveSourceChannel(node, editor.source.Layers[node.sourceIndex].Channels[index])
		if !retained || candidate.ID != channelID {
			continue
		}
		if channel != nil {
			return editError(ErrInvalidEdit, "layers.channels", "duplicate channel identifiers are ambiguous")
		}
		channel = &candidate
	}
	if channel == nil {
		return editError(ErrInvalidEdit, "layers.channels", "source channel identifier was not found")
	}
	if plane.ID != channelID || plane.Depth != editor.effectiveDepthLocked() ||
		plane.Width != channel.Bounds.Dx() || plane.Height != channel.Bounds.Dy() {
		return editError(ErrInvalidEdit, "layers.channels.shape", "channel identifier, depth or dimensions differ")
	}
	length, err := editor.checkPlaneLocked(plane)
	if err != nil {
		return err
	}
	previous := node.planes[channelID]
	if bytes.Equal(previous.Data, plane.Data) && previous.Depth == plane.Depth &&
		previous.Width == plane.Width && previous.Height == plane.Height {
		return nil
	}
	ownedBytes := editor.ownedBytesLocked(nil) - int64(len(previous.Data))
	if length > editor.source.Limits().MaxDecodeBytes-ownedBytes {
		return editError(psdfile.ErrLimit, "layers.channels", "owned replacements exceed MaxDecodeBytes")
	}
	plane.Data = bytes.Clone(plane.Data)
	if node.planes == nil {
		node.planes = make(map[int16]psdfile.Plane)
	}
	node.planes[channelID] = plane
	editor.markChangedLocked(false)
	return nil
}

// AddRaster inserts an owned RGB8 raster at a bottom-to-top sibling position; parent zero denotes the canvas.
func (editor *Editor) AddRaster(
	parent LayerID, position int, name string, pixels *image.NRGBA, origin image.Point,
) (LayerID, error) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkStructureMutableLocked(); err != nil {
		return 0, err
	}
	if err := editor.checkRGBLocked(); err != nil {
		return 0, err
	}
	if err := editor.checkNameLocked(name); err != nil {
		return 0, err
	}
	_, children, err := editor.targetChildrenLocked(parent)
	if err != nil {
		return 0, err
	}
	if position < 0 || position > len(*children) {
		return 0, editError(ErrInvalidEdit, "layers.position", "position is outside the target siblings")
	}
	if err := editor.checkLayerBudgetLocked(1); err != nil {
		return 0, err
	}
	owned, err := editor.copyImageLocked(pixels, nil)
	if err != nil {
		return 0, err
	}
	width, height := int64(owned.Rect.Dx()), int64(owned.Rect.Dy())
	if int64(origin.X) < math.MinInt32 || int64(origin.Y) < math.MinInt32 ||
		int64(origin.X) > math.MaxInt32-width || int64(origin.Y) > math.MaxInt32-height {
		return 0, editError(ErrInvalidEdit, "layers.bounds", "layer coordinates must fit signed 32-bit fields")
	}
	psdID, err := editor.reservePSDIDsLocked(1)
	if err != nil {
		return 0, err
	}
	node := &editNode{
		id: editor.nextID, sourceIndex: -1, endSourceIndex: -1, psdID: psdID, name: name,
		visible: true, opacity: 255, pixels: owned,
		bounds: image.Rect(origin.X, origin.Y, origin.X+int(width), origin.Y+int(height)),
	}
	editor.nextID++
	*children = slices.Insert(*children, position, node)
	editor.markChangedLocked(true)
	return node.id, nil
}

// AddGroup inserts an empty group with distinct PSD identifiers for its header and end marker.
func (editor *Editor) AddGroup(parent LayerID, position int, name string, expanded bool) (LayerID, error) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkStructureMutableLocked(); err != nil {
		return 0, err
	}
	if err := editor.checkNameLocked(name); err != nil {
		return 0, err
	}
	_, children, err := editor.targetChildrenLocked(parent)
	if err != nil {
		return 0, err
	}
	if position < 0 || position > len(*children) {
		return 0, editError(ErrInvalidEdit, "layers.position", "position is outside the target siblings")
	}
	if err := editor.checkLayerBudgetLocked(2); err != nil {
		return 0, err
	}
	psdID, err := editor.reservePSDIDsLocked(2)
	if err != nil {
		return 0, err
	}
	node := &editNode{
		id: editor.nextID, sourceIndex: -1, endSourceIndex: -1, psdID: psdID, endPSDID: psdID + 1,
		name: name, visible: true, opacity: 255, group: true, expanded: expanded,
	}
	editor.nextID++
	*children = slices.Insert(*children, position, node)
	editor.markChangedLocked(true)
	return node.id, nil
}

// Move relocates a layer or group; position refers to the target siblings after removing the moving node.
// Moving a group into itself or any descendant is rejected without changing the document.
func (editor *Editor) Move(id, parent LayerID, position int) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkStructureMutableLocked(); err != nil {
		return err
	}
	node, oldParent, index, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	newParent, target, err := editor.targetChildrenLocked(parent)
	if err != nil {
		return err
	}
	if newParent != nil && nodeContains(node, newParent.id) {
		return editError(ErrInvalidEdit, "layers.parent", "a group cannot contain itself or its ancestor")
	}
	targetLength := len(*target)
	if oldParent == newParent {
		targetLength--
	}
	if position < 0 || position > targetLength {
		return editError(ErrInvalidEdit, "layers.position", "position is outside the target siblings")
	}
	if oldParent == newParent && position == index {
		return nil
	}
	previous := &editor.roots
	if oldParent != nil {
		previous = &oldParent.children
	}
	*previous = slices.Delete(*previous, index, index+1)
	*target = slices.Insert(*target, position, node)
	editor.markChangedLocked(true)
	return nil
}

// Remove removes a layer and all of its descendants without reusing any editor or PSD identifiers.
func (editor *Editor) Remove(id LayerID) error {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if err := editor.checkStructureMutableLocked(); err != nil {
		return err
	}
	_, parent, index, err := editor.findNodeLocked(id)
	if err != nil {
		return err
	}
	children := &editor.roots
	if parent != nil {
		children = &parent.children
	}
	*children = slices.Delete(*children, index, index+1)
	editor.markChangedLocked(true)
	return nil
}

func (editor *Editor) checkOpenLocked() error {
	if editor.closed || editor.source == nil {
		return psdfile.ErrClosed
	}
	_, err := editor.source.ReadAt(nil, 0)
	return err
}

func (editor *Editor) checkMutableLocked() error {
	if err := editor.checkOpenLocked(); err != nil {
		return err
	}
	if len(editor.source.LayerSets) > 1 {
		return editError(ErrUnsupported, "layerSets", "editing multiple layer-information sets is unsupported")
	}
	for _, node := range editor.roots {
		if node.virtualComposite && node.psdID == 0 {
			return editError(psdfile.ErrLimit, "layers.psdID", "virtual background has no writable layer identifier")
		}
	}
	return nil
}

func (editor *Editor) checkStructureMutableLocked() error {
	if err := editor.checkMutableLocked(); err != nil {
		return err
	}
	for _, layer := range editor.source.Layers {
		if layer.Divider > 3 {
			return editError(ErrUnsupported, "layers.groups", "unknown hierarchy metadata prevents structural edits")
		}
	}
	return nil
}

func (editor *Editor) effectiveDepthLocked() uint16 {
	index := editor.source.ActiveLayerSet
	if index >= 0 && index < len(editor.source.LayerSets) {
		return editor.source.LayerSets[index].Depth
	}
	return editor.source.Header.Depth
}

func (editor *Editor) findNodeLocked(id LayerID) (*editNode, *editNode, int, error) {
	var search func([]*editNode, *editNode) (*editNode, *editNode, int)
	search = func(nodes []*editNode, parent *editNode) (*editNode, *editNode, int) {
		for index, node := range nodes {
			if node.id == id {
				return node, parent, index
			}
			if found, owner, position := search(node.children, node); found != nil {
				return found, owner, position
			}
		}
		return nil, nil, -1
	}
	if node, parent, index := search(editor.roots, nil); node != nil {
		return node, parent, index, nil
	}
	return nil, nil, -1, editError(ErrInvalidEdit, "layers.id", "layer handle was not found")
}

func (editor *Editor) targetChildrenLocked(parent LayerID) (*editNode, *[]*editNode, error) {
	if parent == 0 {
		return nil, &editor.roots, nil
	}
	node, _, _, err := editor.findNodeLocked(parent)
	if err != nil {
		return nil, nil, err
	}
	if !node.group {
		return nil, nil, editError(ErrInvalidEdit, "layers.parent", "target parent is not a group")
	}
	return node, &node.children, nil
}

func (editor *Editor) layerInfoLocked(node *editNode, parent LayerID, withChildren bool) (LayerInfo, error) {
	fill, err := editor.fillOpacityLocked(node)
	if err != nil {
		return LayerInfo{}, err
	}
	blend, err := editor.blendModeLocked(node)
	if err != nil {
		return LayerInfo{}, err
	}
	info := LayerInfo{
		ID: node.id, Parent: parent, Name: node.name, Visible: node.visible, Opacity: node.opacity,
		FillOpacity: fill, BlendMode: blend, Clipping: editor.clippingLocked(node),
		Bounds: node.bounds, Group: node.group, Expanded: node.expanded,
		SourceIndex: node.sourceIndex, PSDID: node.psdID,
	}
	if withChildren {
		info.Children = make([]LayerInfo, len(node.children))
		for index, child := range node.children {
			value, err := editor.layerInfoLocked(child, node.id, true)
			if err != nil {
				return LayerInfo{}, err
			}
			info.Children[index] = value
		}
	}
	return info, nil
}

func (editor *Editor) writeLayersLocked() []writeLayer {
	var layers []writeLayer
	var visit func([]*editNode)
	visit = func(nodes []*editNode) {
		for _, node := range nodes {
			if !node.group {
				layers = append(layers, writeLayer{node: node, sourceIndex: node.sourceIndex})
				continue
			}
			layers = append(layers, writeLayer{node: node, sourceIndex: node.endSourceIndex, divider: 3, end: true})
			visit(node.children)
			divider := uint32(2)
			if node.expanded {
				divider = 1
			}
			layers = append(layers, writeLayer{node: node, sourceIndex: node.sourceIndex, divider: divider})
		}
	}
	visit(editor.roots)
	return layers
}

func (editor *Editor) initializePSDIDs() error {
	var highest uint32
	for _, set := range editor.source.LayerSets {
		for _, layer := range set.Layers {
			if layer.HasID {
				highest = max(highest, layer.ID)
			}
		}
	}
	for _, layer := range editor.source.Layers {
		if layer.HasID {
			highest = max(highest, layer.ID)
		}
	}
	if highest == math.MaxUint32 {
		editor.nextPSDID = 0
		return nil
	}
	editor.nextPSDID = highest + 1
	for _, resource := range editor.source.Resources {
		if resource.ID != 1044 {
			continue
		}
		if resource.Data.Length != 4 {
			return editError(ErrInvalidEdit, "resources.layerIDSeed", "layer identifier seed must be four bytes")
		}
		data, err := editor.source.ReadSpan(resource.Data)
		if err != nil {
			return err
		}
		editor.nextPSDID = max(editor.nextPSDID, binary.BigEndian.Uint32(data))
	}
	return nil
}

func (editor *Editor) reservePSDIDsLocked(count uint32) (uint32, error) {
	// Keep one representable value above every allocated identifier for image resource 1044.
	if editor.nextPSDID == 0 || count == 0 || count > math.MaxUint32-editor.nextPSDID {
		return 0, editError(psdfile.ErrLimit, "layers.psdID", "PSD layer identifier space is exhausted")
	}
	first := editor.nextPSDID
	editor.nextPSDID += count
	return first, nil
}

func (editor *Editor) markChangedLocked(structure bool) {
	editor.nativeTextPreview = nil
	editor.revision++
	editor.visualChanged = true
	editor.structureChanged = editor.structureChanged || structure
}

func (editor *Editor) checkNameLocked(name string) error {
	if !utf8.ValidString(name) {
		return editError(ErrInvalidEdit, "layers.name", "name must contain valid UTF-8")
	}
	if strings.IndexByte(name, 0) >= 0 {
		return editError(ErrInvalidEdit, "layers.name", "names must not contain NUL terminators")
	}
	if int64(len(name)) > editor.source.Limits().MaxMetadataBytes/4 {
		return editError(psdfile.ErrLimit, "layers.name", "Unicode name exceeds the metadata budget")
	}
	return nil
}

func (editor *Editor) checkLayerBudgetLocked(added int) error {
	count := len(editor.writeLayersLocked())
	if count > math.MaxInt16-added || count > editor.source.Limits().MaxLayers-added {
		return editError(psdfile.ErrLimit, "layers", "layer count exceeds the file or configured limit")
	}
	if editor.nextID == 0 || editor.nextID == LayerID(math.MaxUint64) {
		return editError(psdfile.ErrLimit, "layers.id", "editor layer handle space is exhausted")
	}
	return nil
}

func (editor *Editor) checkRGBLocked() error {
	if editor.source.Header.ColorMode != 3 || editor.source.Header.Depth != 8 || editor.effectiveDepthLocked() != 8 {
		return editError(ErrUnsupported, "layers.pixels", "RGBA replacements require an RGB8 document")
	}
	return nil
}

func (editor *Editor) checkRGBRasterLocked(node *editNode) error {
	if err := editor.checkRGBLocked(); err != nil {
		return err
	}
	if node.group || editor.semanticRasterLocked(node) {
		return editError(ErrUnsupported, "layers.pixels", "RGBA replacements require an ordinary raster layer")
	}
	if node.sourceIndex >= 0 {
		if editor.source.Layers[node.sourceIndex].Divider != 0 {
			return editError(ErrUnsupported, "layers.pixels", "hierarchy marker pixels are not an ordinary raster")
		}
		var counts [3]int
		var alphaCount int
		for _, channel := range editor.source.Layers[node.sourceIndex].Channels {
			if channel.ID >= 0 && channel.ID < 3 {
				counts[channel.ID]++
			}
			if channel.ID == -1 {
				alphaCount++
			}
		}
		if counts != [3]int{1, 1, 1} || alphaCount > 1 {
			return editError(ErrUnsupported, "layers.pixels", "source raster must have three unambiguous RGB channels")
		}
	}
	return nil
}

func (editor *Editor) semanticRasterLocked(node *editNode) bool {
	if node.sourceIndex < 0 {
		return false
	}
	for _, block := range editor.source.Layers[node.sourceIndex].AdditionalInfo {
		switch block.Key {
		case "SoLd", "SoLE", "PlLd", "TySh", "tySh", "SoCo", "GdFl", "PtFl", "vscg", "vstk", "PxSc", "PxSD",
			"brit", "levl", "curv", "expA", "vibA", "hue ", "hue2", "blnc", "blwh", "phfl", "mixr",
			"clrL", "nvrt", "post", "thrs", "grdm", "selc", "CgEd":
			return true
		}
	}
	return false
}

func (editor *Editor) copyImageLocked(pixels *image.NRGBA, replacing *editNode) (*image.NRGBA, error) {
	if pixels == nil {
		return nil, editError(ErrInvalidEdit, "layers.pixels", "replacement pixels are required")
	}
	for _, coordinate := range []int{pixels.Rect.Min.X, pixels.Rect.Min.Y, pixels.Rect.Max.X, pixels.Rect.Max.Y} {
		if int64(coordinate) < math.MinInt32 || int64(coordinate) > math.MaxInt32 {
			return nil, editError(ErrInvalidEdit, "layers.pixels.bounds", "image rectangle is outside signed 32-bit fields")
		}
	}
	width := int64(pixels.Rect.Max.X) - int64(pixels.Rect.Min.X)
	height := int64(pixels.Rect.Max.Y) - int64(pixels.Rect.Min.Y)
	if width <= 0 || height <= 0 || pixels.Stride < 0 {
		return nil, editError(ErrInvalidEdit, "layers.pixels.bounds", "image dimensions and stride must be positive")
	}
	limits := editor.source.Limits()
	if width > limits.MaxDecodePixels/height {
		return nil, editError(psdfile.ErrLimit, "layers.pixels", "image exceeds MaxDecodePixels")
	}
	area := width * height
	retained := editor.ownedBytesLocked(nil)
	if replacing != nil && replacing.pixels != nil {
		retained -= int64(len(replacing.pixels.Pix))
	}
	if area > limits.MaxDecodeBytes/4 || area*4 > limits.MaxDecodeBytes-retained {
		return nil, editError(psdfile.ErrLimit, "layers.pixels", "owned replacements exceed MaxDecodeBytes")
	}
	rowBytes := int(width * 4)
	if pixels.Stride < rowBytes || len(pixels.Pix) < rowBytes ||
		height-1 > int64((len(pixels.Pix)-rowBytes)/pixels.Stride) {
		return nil, editError(ErrInvalidEdit, "layers.pixels", "image stride or backing pixel slice is truncated")
	}
	owned := image.NewNRGBA(image.Rect(0, 0, int(width), int(height)))
	for row := 0; row < int(height); row++ {
		copy(owned.Pix[row*owned.Stride:row*owned.Stride+rowBytes], pixels.Pix[row*pixels.Stride:row*pixels.Stride+rowBytes])
	}
	return owned, nil
}

func (editor *Editor) checkPlaneLocked(plane psdfile.Plane) (int64, error) {
	if plane.Width < 0 || plane.Height < 0 || plane.RowBytes < 0 {
		return 0, editError(ErrInvalidEdit, "layers.channels.shape", "negative channel dimensions are invalid")
	}
	width, height := int64(plane.Width), int64(plane.Height)
	limits := editor.source.Limits()
	if height > 0 && width > limits.MaxDecodePixels/height {
		return 0, editError(psdfile.ErrLimit, "layers.channels", "channel exceeds MaxDecodePixels")
	}
	var rowBytes int64
	switch plane.Depth {
	case 1:
		rowBytes = width / 8
		if width%8 != 0 {
			rowBytes++
		}
	case 8, 16, 32:
		if width > math.MaxInt64/int64(plane.Depth/8) {
			return 0, editError(psdfile.ErrLimit, "layers.channels", "channel row size overflows")
		}
		rowBytes = width * int64(plane.Depth/8)
	default:
		return 0, editError(ErrInvalidEdit, "layers.channels.depth", "channel depth must be 1, 8, 16 or 32")
	}
	if rowBytes > 0 && height > limits.MaxDecodeBytes/rowBytes {
		return 0, editError(psdfile.ErrLimit, "layers.channels", "channel exceeds MaxDecodeBytes")
	}
	length := rowBytes * height
	if int64(plane.RowBytes) != rowBytes || int64(len(plane.Data)) != length {
		return 0, editError(ErrInvalidEdit, "layers.channels.shape", "row size or sample length differs from dimensions")
	}
	return length, nil
}

func (editor *Editor) ownedBytesLocked(excluded *editNode) int64 {
	total := editor.linkedRecordsBytes
	for _, data := range editor.globalAdditional {
		total += int64(len(data))
	}
	if editor.nativeTextPreview != nil {
		total += int64(len(editor.nativeTextPreview.Pix))
	}
	var visit func([]*editNode)
	visit = func(nodes []*editNode) {
		for _, node := range nodes {
			if node != excluded {
				total += int64(len(node.maskData))
				if node.vectorMaskChange != nil {
					total += int64(cap(node.vectorMaskChange.data))
				}
				if node.pixelMaskChange != nil && node.pixelMaskChange.plane != nil {
					total += int64(cap(node.pixelMaskChange.plane.Data))
				}
				if node.advancedBlending != nil {
					total += node.advancedBlending.ownedBytes()
				}
				if node.fillOpacityAdded {
					// Reserve the deferred canonical payload before the writer creates its tagged block.
					total += 4
				}
				if node.pixels != nil {
					total += int64(len(node.pixels.Pix))
				}
				for _, plane := range node.planes {
					total += int64(len(plane.Data))
				}
				for _, data := range node.additional {
					total += int64(len(data))
				}
				total += node.semanticBytes
			}
			visit(node.children)
		}
	}
	visit(editor.roots)
	for _, replacement := range editor.linkedOverlays {
		total += linkedReplacementOwnedBytes(replacement)
	}
	return total
}

func nodeContains(node *editNode, id LayerID) bool {
	if node.id == id {
		return true
	}
	for _, child := range node.children {
		if nodeContains(child, id) {
			return true
		}
	}
	return false
}
