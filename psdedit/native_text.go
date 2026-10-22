package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"strconv"
	"strings"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdtext"
)

// ErrStaleNativeTextState identifies a caller snapshot prepared for another editor revision.
var ErrStaleNativeTextState = errors.New("PSD editor changed since the native text snapshot")

// NativeTextStateOptions imports an accurate native snapshot of one existing RGB8 text layer.
// TySh is a complete tagged-block payload; Txt2 is the complete document text engine payload.
// Pixels is accurate straight-alpha artwork with an absolute document-coordinate rectangle.
// CompositeImage is an accurate full-document preview with the retained document dimensions.
// ExpectedRevision is mandatory, including zero, and LayerSetIndex explicitly selects the active physical set.
// All supplied buffers are copied; the caller must keep them immutable until this method returns.
// The caller is responsible for agreement between the native glyph state, layer artwork and preview.
// This method validates source context and synchronization, but does not shape or lay out glyphs.
type NativeTextStateOptions struct {
	ExpectedRevision       uint64
	LayerSetIndex          int
	TySh, Txt2             []byte
	Pixels, CompositeImage *image.NRGBA
}

// SetNativeTextState atomically imports source-compatible native text state, artwork and preview.
// One global schema-7 Txt2 block and one populated active layer set are required.
// Affine bits, warp, descriptor ordering, non-text descriptor entries and unmodified opaque state are preserved.
// Different native text bounds are accepted; moving relative masks or resizing unknown channels is unsupported.
// A native preview remains usable after renaming, and is discarded by every later visual edit.
func (e *Editor) SetNativeTextState(ctx context.Context, id LayerID, options NativeTextStateOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.checkMutableLocked(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.revision != options.ExpectedRevision {
		return ErrStaleNativeTextState
	}
	if err := e.checkRGBLocked(); err != nil {
		return err
	}
	if e.structureChanged {
		return editError(
			ErrUnsupported, "layers.nativeText.structure", "native text import requires the retained source structure",
		)
	}
	if options.LayerSetIndex != e.source.ActiveLayerSet {
		return editError(
			ErrUnsupported, "layers.nativeText.layerSet", "the active physical layer set must be explicitly selected",
		)
	}
	for index, set := range e.source.LayerSets {
		if index != options.LayerSetIndex && len(set.Layers) != 0 {
			return editError(ErrUnsupported, "layers.nativeText.layerSets",
				"multiple populated physical sets require synchronized native snapshots")
		}
	}
	if options.Pixels == nil || options.CompositeImage == nil {
		return editError(
			ErrInvalidEdit, "layers.nativeText.pixels", "accurate layer artwork and document preview are required",
		)
	}
	node, _, _, err := e.findNodeLocked(id)
	if err != nil {
		return err
	}
	tysh, txt2, err := e.nativeTextBlocks(node)
	if err != nil {
		return err
	}
	if err := e.textEditGeometry(node, options.Pixels.Rect); err != nil {
		return err
	}
	width, height := int(e.source.Header.Width), int(e.source.Header.Height)
	if err := compositeImageShape(options.CompositeImage, width, height); err != nil {
		return err
	}
	limits := e.source.Limits()
	available := limits.MaxDecodeBytes - e.ownedBytesLocked(nil)
	artworkBytes, err := textEditPixelBytes(options.Pixels, limits, available)
	if err != nil {
		return err
	}
	previewBytes, err := textEditPixelBytes(options.CompositeImage, limits, available-artworkBytes)
	if err != nil {
		return err
	}
	work := available - artworkBytes - previewBytes - 4096
	if work <= 0 {
		return editError(psdfile.ErrLimit, "layers.nativeText", "accurate artwork and preview exceed peak budget")
	}
	// Separate capped stages reserve all payload copies, bridges, trees, views and comparison scratch at peak.
	stage := min(limits.MaxMetadataBytes, work/32)
	if stage < 4096 || int64(len(options.TySh)) > stage/4 || int64(len(options.Txt2)) > stage/4 {
		return editError(psdfile.ErrLimit, "layers.nativeText", "native payloads exceed peak working budget")
	}
	newTySh := append([]byte(nil), options.TySh...)
	newTxt2 := append([]byte(nil), options.Txt2...)
	oldTySh := node.additional[tysh.Span.Offset]
	if oldTySh == nil {
		if tysh.Data.Length > stage/4 {
			return editError(psdfile.ErrLimit, "layers.nativeText", "source TySh exceeds working budget")
		}
		oldTySh, err = textEditReadPayload(ctx, e.source, tysh.Data)
		if err != nil {
			return err
		}
	}
	oldTxt2 := e.globalAdditional[txt2.Span.Offset]
	if oldTxt2 == nil {
		if txt2.Data.Length > stage/4 {
			return editError(psdfile.ErrLimit, "layers.nativeText", "source Txt2 exceeds working budget")
		}
		oldTxt2, err = textEditReadPayload(ctx, e.source, txt2.Data)
		if err != nil {
			return err
		}
	}
	oldBridge, oldLayer, err := textEditSource(ctx, oldTySh, limits, stage)
	if err != nil {
		return textEditError(err)
	}
	defer func() { _ = oldBridge.Close() }()
	newBridge, newLayer, err := textEditSource(ctx, newTySh, limits, stage)
	if err != nil {
		return textEditError(err)
	}
	defer func() { _ = newBridge.Close() }()
	if err := nativeTextLayerContext(ctx, oldBridge, newBridge, oldLayer, newLayer); err != nil {
		return err
	}
	oldEngine, err := psdtext.DecodeDocument(ctx, oldTxt2, psdtext.DecodeOptions{MaxBytes: stage,
		MaxDepth: min(256, limits.MaxDescriptorDepth), MaxNodes: limits.MaxDescriptorItems})
	if err != nil {
		return textEditError(err)
	}
	newEngine, err := psdtext.DecodeDocument(ctx, newTxt2, psdtext.DecodeOptions{MaxBytes: stage,
		MaxDepth: min(256, limits.MaxDescriptorDepth), MaxNodes: limits.MaxDescriptorItems})
	if err != nil {
		return textEditError(err)
	}
	oldMeta, err := oldEngine.DocumentMetadata(ctx, psdtext.DocumentMetadataOptions{MaxBytes: stage,
		MaxTexts: limits.MaxLayers, MaxRuns: limits.MaxDescriptorItems})
	if err != nil {
		return textEditError(err)
	}
	newMeta, err := newEngine.DocumentMetadata(ctx, psdtext.DocumentMetadataOptions{MaxBytes: stage,
		MaxTexts: limits.MaxLayers, MaxRuns: limits.MaxDescriptorItems})
	if err != nil {
		return textEditError(err)
	}
	oldText, err := oldLayer.ResolveDocumentText(ctx, oldMeta)
	if err != nil {
		return textEditError(err)
	}
	newText, err := newLayer.ResolveDocumentText(ctx, newMeta)
	if err != nil {
		return textEditError(err)
	}
	if oldText.Index != newText.Index || len(oldMeta.Texts) != len(newMeta.Texts) {
		return editError(ErrUnsupported, "layers.nativeText.TextIndex", "native text record identity or count changed")
	}
	if err := nativeTextRunConsistency(ctx, newBridge, newLayer, newText, stage, limits); err != nil {
		return err
	}
	if err := nativeTextFontContext(ctx, oldBridge, newBridge, oldLayer, newLayer, stage); err != nil {
		return err
	}
	if len(oldMeta.Texts) > int(stage/512) {
		return editError(psdfile.ErrLimit, "layers.nativeText.references", "native reference validation exceeds budget")
	}
	if err := nativeTextShapeReferences(ctx, oldMeta); err != nil {
		return err
	}
	if err := nativeTextShapeReferences(ctx, newMeta); err != nil {
		return err
	}
	allowed := []string{"/1/1[" + strconv.Itoa(newText.Index) + "]"}
	for index := range oldMeta.Texts {
		if index == newText.Index {
			continue
		}
		prefix := "/1/1[" + strconv.Itoa(index) + "]"
		allowed = append(allowed, prefix+"/1/0[0]/0", prefix+"/1/2[0]/6[0]/10")
	}
	if err := nativeTextOriginalState(ctx, oldEngine, newEngine, oldTxt2, newTxt2, allowed); err != nil {
		return err
	}
	if err := e.nativeTextOtherLayers(ctx, node, newLayer.TextIndex, newMeta, stage); err != nil {
		return err
	}
	// Retain the original opaque TySh tail, adding only required zero alignment after it.
	tail, err := oldBridge.ReadSpan(oldLayer.Tail)
	if err != nil {
		return textEditError(err)
	}
	tailStart := newLayer.Tail.Offset - newLayer.Block.Data.Offset
	newTySh = append(newTySh[:tailStart:tailStart], tail...)
	newTySh = append(newTySh, make([]byte, (4-len(newTySh)%4)%4)...)
	artwork, err := nativeTextCopyImage(ctx, options.Pixels)
	if err != nil {
		return err
	}
	preview, err := nativeTextCopyImage(ctx, options.CompositeImage)
	if err != nil {
		return err
	}
	if _, err := e.source.ReadAt(nil, 0); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.revision != options.ExpectedRevision {
		return ErrStaleNativeTextState
	}
	if node.additional == nil {
		node.additional = make(map[int64][]byte)
	}
	if e.globalAdditional == nil {
		e.globalAdditional = make(map[int64][]byte)
	}
	node.additional[tysh.Span.Offset] = newTySh
	e.globalAdditional[txt2.Span.Offset] = newTxt2
	node.pixels, node.bounds = artwork, options.Pixels.Rect
	node.setSemanticCost("nativeText", 256)
	e.markChangedLocked(false)
	e.nativeTextPreview, e.nativeTextPreviewRevision = preview, e.revision
	return nil
}

func (e *Editor) nativeTextBlocks(node *editNode) (psdfile.AdditionalBlock, psdfile.AdditionalBlock, error) {
	var tysh, txt2 psdfile.AdditionalBlock
	if node.group || node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) || len(node.planes) != 0 {
		return tysh, txt2, editError(
			ErrUnsupported, "layers.nativeText", "one existing text layer without channel overlays is required",
		)
	}
	globalCount, layerCount := 0, 0
	for _, block := range e.source.AdditionalInfo {
		if block.Key == "Txt2" {
			txt2 = block
			globalCount++
		}
	}
	layer := e.source.Layers[node.sourceIndex]
	var channels [3]int
	alpha := 0
	for _, channel := range layer.Channels {
		if channel.ID >= 0 && channel.ID < 3 {
			channels[channel.ID]++
		}
		if channel.ID == -1 {
			alpha++
		}
	}
	if channels != [3]int{1, 1, 1} || alpha > 1 || layer.Divider != 0 {
		return tysh, txt2, editError(
			ErrUnsupported, "layers.nativeText.channels", "unambiguous source RGB channels are required",
		)
	}
	for _, block := range layer.AdditionalInfo {
		switch block.Key {
		case "TySh":
			tysh = block
			layerCount++
		case "tySh", "Txt2", "SoLd", "SoLE", "PlLd", "SoCo", "GdFl", "PtFl", "vscg", "vstk", "PxSc", "PxSD",
			"brit", "levl", "curv", "expA", "vibA", "hue ", "hue2", "blnc", "blwh", "phfl", "mixr",
			"clrL", "nvrt", "post", "thrs", "grdm", "selc", "CgEd":
			return tysh, txt2, editError(ErrUnsupported, "layers.nativeText", "incompatible semantic source block")
		}
	}
	if globalCount != 1 || layerCount != 1 {
		return tysh, txt2, editError(
			ErrUnsupported, "layers.nativeText", "one unambiguous global Txt2 and layer TySh are required",
		)
	}
	return tysh, txt2, nil
}

func nativeTextLayerContext(
	ctx context.Context, a, b *psdfile.Document, oldLayer, newLayer psdfile.TextLayerRecord,
) error {
	if oldLayer.TransformBits != newLayer.TransformBits || oldLayer.Version != newLayer.Version ||
		oldLayer.TextVersion != newLayer.TextVersion || oldLayer.WarpVersion != newLayer.WarpVersion ||
		!oldLayer.HasTextIndex || !newLayer.HasTextIndex || oldLayer.TextIndex != newLayer.TextIndex {
		return editError(
			ErrUnsupported, "layers.nativeText.context", "native affine, versions or text index differ from the source",
		)
	}
	for _, pair := range [][2]psdfile.Span{{oldLayer.Prefix, newLayer.Prefix},
		{oldLayer.WarpVersionData, newLayer.WarpVersionData}, {oldLayer.WarpDescriptorData, newLayer.WarpDescriptorData}} {
		oldData, err := a.ReadSpan(pair[0])
		if err != nil {
			return textEditError(err)
		}
		newData, err := b.ReadSpan(pair[1])
		if err != nil {
			return textEditError(err)
		}
		if !bytes.Equal(oldData, newData) {
			return editError(ErrUnsupported, "layers.nativeText.context", "native source context bytes changed")
		}
	}
	x, y := oldLayer.TextDescriptor, newLayer.TextDescriptor
	if x.ClassID != y.ClassID || x.Name != y.Name || x.Version != y.Version || len(x.Entries) != len(y.Entries) {
		return editError(ErrUnsupported, "layers.nativeText.descriptor", "native descriptor identity or ordering changed")
	}
	critical := map[string]int{}
	for index, entry := range x.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		other := y.Entries[index]
		if entry.Key != other.Key || entry.Type != other.Type {
			return editError(ErrUnsupported, "layers.nativeText.descriptor", "native descriptor property ordering changed")
		}
		switch entry.Key {
		case "Txt ", "EngineData", "TextIndex":
			critical[entry.Key]++
		}
		if entry.Key == "Txt " || entry.Key == "EngineData" {
			if entry.Key == "EngineData" && entry.Type != "tdta" {
				return editError(ErrUnsupported, "layers.nativeText.descriptor", "native EngineData must retain its blob type")
			}
			continue
		}
		if entry.Key == "bounds" || entry.Key == "boundingBox" {
			if err := nativeTextBoundsContext(ctx, a, b, entry, other); err != nil {
				return err
			}
			continue
		}
		left, err := a.ReadSpan(entry.Span)
		if err != nil {
			return textEditError(err)
		}
		right, err := b.ReadSpan(other.Span)
		if err != nil {
			return textEditError(err)
		}
		if !bytes.Equal(left, right) {
			return editError(ErrUnsupported, "layers.nativeText.descriptor", "an unmodified native property changed")
		}
	}
	if critical["Txt "] != 1 || critical["EngineData"] != 1 || critical["TextIndex"] != 1 {
		return editError(ErrUnsupported, "layers.nativeText.descriptor", "native text properties are ambiguous")
	}
	return nil
}

func nativeTextBoundsContext(
	ctx context.Context, a, b *psdfile.Document, left, right psdfile.DescriptorEntry,
) error {
	x, okA := left.Value.(*psdfile.Descriptor)
	y, okB := right.Value.(*psdfile.Descriptor)
	if left.Type != "Objc" || !okA || !okB || x == nil || y == nil || x.Name != y.Name ||
		x.ClassID != y.ClassID || len(x.Entries) != len(y.Entries) {
		return editError(ErrUnsupported, "layers.nativeText.bounds", "native bounds descriptor context changed")
	}
	seen := map[string]bool{}
	for index, entry := range x.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		other := y.Entries[index]
		if entry.Key != other.Key || entry.Type != other.Type {
			return editError(ErrUnsupported, "layers.nativeText.bounds", "native bounds property ordering changed")
		}
		switch entry.Key {
		case "Left", "Top ", "Rght", "Btom":
			if seen[entry.Key] {
				return editError(ErrUnsupported, "layers.nativeText.bounds", "native bounds has a duplicate coordinate")
			}
			seen[entry.Key] = true
			oldUnit, oldOK := entry.Value.(psdfile.DescriptorUnit)
			newUnit, newOK := other.Value.(psdfile.DescriptorUnit)
			coordinate, coordinateOK := newUnit.Number.(float64)
			if entry.Type != "UntF" || !oldOK || !newOK || oldUnit.Unit != newUnit.Unit || oldUnit.Width != newUnit.Width ||
				!coordinateOK || math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
				return editError(ErrInvalidEdit, "layers.nativeText.bounds",
					"native bounds coordinate type, units or finiteness changed")
			}
		default:
			oldData, err := a.ReadSpan(entry.Span)
			if err != nil {
				return textEditError(err)
			}
			newData, err := b.ReadSpan(other.Span)
			if err != nil {
				return textEditError(err)
			}
			if !bytes.Equal(oldData, newData) {
				return editError(ErrUnsupported, "layers.nativeText.bounds", "opaque bounds property changed")
			}
		}
	}
	if len(seen) != 4 {
		return editError(ErrUnsupported, "layers.nativeText.bounds", "four explicit native bounds coordinates are required")
	}
	return nil
}

func nativeTextRunConsistency(
	ctx context.Context, bridge *psdfile.Document, layer psdfile.TextLayerRecord,
	text *psdtext.DocumentTextRecord, budget int64, limits psdfile.Limits,
) error {
	engine, err := bridge.DecodeTextEngine(ctx, layer, psdtext.DecodeOptions{MaxBytes: budget})
	if err != nil {
		return textEditError(err)
	}
	metadata, err := engine.Metadata(ctx, psdtext.MetadataOptions{MaxBytes: budget,
		MaxRuns: limits.MaxDescriptorItems, MaxFonts: limits.MaxDescriptorItems})
	if err != nil {
		return textEditError(err)
	}
	if metadata.Text != text.Text || metadata.UTF16Units != text.UTF16Units ||
		len(metadata.Styles) != len(text.Styles) || len(metadata.Paragraphs) != len(text.Paragraphs) {
		return editError(ErrInvalidEdit, "layers.nativeText.runs", "native TySh and Txt2 text ranges disagree")
	}
	for index, run := range metadata.Styles {
		if run.Start != text.Styles[index].Start || run.Length != text.Styles[index].Length {
			return editError(ErrInvalidEdit, "layers.nativeText.runs", "native character style ranges disagree")
		}
	}
	for index, run := range metadata.Paragraphs {
		if run.Start != text.Paragraphs[index].Start || run.Length != text.Paragraphs[index].Length {
			return editError(ErrInvalidEdit, "layers.nativeText.runs", "native paragraph ranges disagree")
		}
	}
	return ctx.Err()
}

func nativeTextFontContext(
	ctx context.Context, oldBridge, newBridge *psdfile.Document,
	oldLayer, newLayer psdfile.TextLayerRecord, budget int64,
) error {
	oldEngine, err := oldBridge.DecodeTextEngine(ctx, oldLayer, psdtext.DecodeOptions{MaxBytes: budget})
	if err != nil {
		return textEditError(err)
	}
	newEngine, err := newBridge.DecodeTextEngine(ctx, newLayer, psdtext.DecodeOptions{MaxBytes: budget})
	if err != nil {
		return textEditError(err)
	}
	for _, key := range []string{"ResourceDict", "DocumentResources"} {
		oldResources, newResources := oldEngine.Root.Lookup(key), newEngine.Root.Lookup(key)
		if oldResources == nil && newResources == nil {
			continue
		}
		if oldResources == nil || newResources == nil {
			return editError(ErrUnsupported, "layers.nativeText.fonts", "native text font context changed presence")
		}
		oldEncoded, err := psdtext.Encode(
			ctx, &psdtext.EngineData{Root: oldResources}, psdtext.EncodeOptions{MaxBytes: budget},
		)
		if err != nil {
			return textEditError(err)
		}
		newEncoded, err := psdtext.Encode(
			ctx, &psdtext.EngineData{Root: newResources}, psdtext.EncodeOptions{MaxBytes: budget},
		)
		if err != nil {
			return textEditError(err)
		}
		if !bytes.Equal(oldEncoded, newEncoded) {
			return editError(ErrUnsupported, "layers.nativeText.fonts", "native text font resource bytes differ from the source")
		}
	}
	return ctx.Err()
}

func nativeTextShapeReferences(ctx context.Context, metadata *psdtext.DocumentMetadata) error {
	seen := make(map[int64]struct{}, len(metadata.Texts))
	for _, record := range metadata.Texts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record.ShapeReferences == nil || len(record.ShapeReferences.Items) != 1 ||
			record.Shapes == nil || len(record.Shapes.Items) != 1 {
			return editError(
				ErrUnsupported, "layers.nativeText.references", "one observed native text frame per record is required",
			)
		}
		reference, err := nativeTextUnique(record.ShapeReferences.Items[0], "0", psdtext.Number)
		if err != nil {
			return err
		}
		frames, err := nativeTextUnique(record.Shapes.Items[0], "6", psdtext.List)
		if err != nil {
			return err
		}
		if frames == nil || frames.Kind != psdtext.List || len(frames.Items) != 1 {
			return editError(ErrUnsupported, "layers.nativeText.references", "native text frame structure is unsupported")
		}
		identifier, err := nativeTextUnique(frames.Items[0], "10", psdtext.Number)
		if err != nil {
			return err
		}
		ref, refOK := reference.Int64()
		identity, identityOK := identifier.Int64()
		if !refOK || !identityOK || ref < 0 || identity < 0 || ref != identity {
			return editError(ErrInvalidEdit, "layers.nativeText.references", "native shape reference and identifier disagree")
		}
		if _, exists := seen[identity]; exists {
			return editError(ErrUnsupported, "layers.nativeText.references", "native shape identifiers are not unique")
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func nativeTextUnique(node *psdtext.Node, key string, kind psdtext.Kind) (*psdtext.Node, error) {
	if node == nil || node.Kind != psdtext.Dictionary {
		return nil, editError(ErrUnsupported, "layers.nativeText.references", "native reference dictionary is absent")
	}
	var result *psdtext.Node
	seen := false
	for _, entry := range node.Entries {
		if entry.Key != key {
			continue
		}
		if seen {
			return nil, editError(ErrUnsupported, "layers.nativeText.references", "native reference property is ambiguous")
		}
		seen = true
		result = entry.Value
	}
	if result == nil || result.Kind != kind {
		return nil, editError(ErrInvalidEdit, "layers.nativeText.references", "native reference property has the wrong type")
	}
	return result, nil
}

func nativeTextOriginalState(
	ctx context.Context, a, b *psdtext.EngineData, oldRaw, newRaw []byte, allowed []string,
) error {
	if !nativeTextRawEqual(a.Root, b.Root, oldRaw, newRaw, 0, a.Root.Span.Offset, 0, b.Root.Span.Offset) ||
		!nativeTextRawEqual(a.Root, b.Root, oldRaw, newRaw, a.Root.Span.Offset+a.Root.Span.Length, int64(len(oldRaw)),
			b.Root.Span.Offset+b.Root.Span.Length, int64(len(newRaw))) {
		return editError(ErrUnsupported, "layers.nativeText.opaque", "native outer comments or opaque padding changed")
	}
	return nativeTextCompareNodes(ctx, a.Root, b.Root, oldRaw, newRaw, "", allowed)
}

func nativeTextRawEqual(_, _ *psdtext.Node, a, b []byte, startA, endA, startB, endB int64) bool {
	if startA < 0 || endA < startA || endA > int64(len(a)) || startB < 0 || endB < startB || endB > int64(len(b)) {
		return false
	}
	return bytes.Equal(a[startA:endA], b[startB:endB])
}

func nativeTextCompareNodes(
	ctx context.Context, a, b *psdtext.Node, oldRaw, newRaw []byte, path string, allowed []string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || b == nil || a.Kind != b.Kind {
		return editError(ErrUnsupported, "layers.nativeText.opaque", "native preserved state changed kind")
	}
	descend := false
	for _, candidate := range allowed {
		if path == candidate {
			return nil
		}
		if strings.HasPrefix(candidate, path+"/") || strings.HasPrefix(candidate, path+"[") {
			descend = true
		}
	}
	if !descend {
		if !nativeTextRawEqual(a, b, oldRaw, newRaw,
			a.Span.Offset, a.Span.Offset+a.Span.Length, b.Span.Offset, b.Span.Offset+b.Span.Length) {
			return editError(ErrUnsupported, "layers.nativeText.opaque", "an unmodified opaque native subtree changed")
		}
		return nil
	}
	if len(a.Entries) != len(b.Entries) || len(a.Items) != len(b.Items) {
		return editError(ErrUnsupported, "layers.nativeText.opaque", "preserved native container ordering or count changed")
	}
	positionA, positionB := a.Span.Offset, b.Span.Offset
	for index, entry := range a.Entries {
		other := b.Entries[index]
		if entry.Key != other.Key || !nativeTextRawEqual(a, b, oldRaw, newRaw,
			positionA, entry.Value.Span.Offset, positionB, other.Value.Span.Offset) {
			return editError(ErrUnsupported, "layers.nativeText.opaque", "preserved native property bytes changed")
		}
		err := nativeTextCompareNodes(ctx, entry.Value, other.Value, oldRaw, newRaw, path+"/"+entry.Key, allowed)
		if err != nil {
			return err
		}
		positionA = entry.Value.Span.Offset + entry.Value.Span.Length
		positionB = other.Value.Span.Offset + other.Value.Span.Length
	}
	for index, item := range a.Items {
		other := b.Items[index]
		if !nativeTextRawEqual(a, b, oldRaw, newRaw, positionA, item.Span.Offset, positionB, other.Span.Offset) {
			return editError(ErrUnsupported, "layers.nativeText.opaque", "preserved native array bytes changed")
		}
		err := nativeTextCompareNodes(ctx, item, other, oldRaw, newRaw, path+"["+strconv.Itoa(index)+"]", allowed)
		if err != nil {
			return err
		}
		positionA, positionB = item.Span.Offset+item.Span.Length, other.Span.Offset+other.Span.Length
	}
	if !nativeTextRawEqual(a, b, oldRaw, newRaw,
		positionA, a.Span.Offset+a.Span.Length, positionB, b.Span.Offset+b.Span.Length) {
		return editError(ErrUnsupported, "layers.nativeText.opaque", "preserved native container tail changed")
	}
	return nil
}

func (e *Editor) nativeTextOtherLayers(
	ctx context.Context, target *editNode, targetTextIndex int32, metadata *psdtext.DocumentMetadata, budget int64,
) error {
	view, err := e.source.ReadTextLayerView(ctx, psdfile.MetadataOptions{MaxBytes: budget})
	if err != nil {
		return textEditError(err)
	}
	seen := map[int32]bool{targetTextIndex: true}
	for _, record := range view.Records {
		if record.LayerSetIndex != e.source.ActiveLayerSet || record.LayerIndex == target.sourceIndex {
			continue
		}
		if !record.HasTextIndex || seen[record.TextIndex] {
			return editError(ErrUnsupported, "layers.nativeText.TextIndex", "physical text indices are absent or ambiguous")
		}
		seen[record.TextIndex] = true
		var current *editNode
		var visit func([]*editNode)
		visit = func(nodes []*editNode) {
			for _, node := range nodes {
				if node.sourceIndex == record.LayerIndex {
					current = node
					return
				}
				visit(node.children)
			}
		}
		visit(e.roots)
		if current == nil {
			return editError(ErrUnsupported, "layers.nativeText.structure", "source text layer structure changed")
		}
		if payload := current.additional[record.Block.Span.Offset]; payload != nil {
			bridge, replacement, err := textEditSource(ctx, payload, e.source.Limits(), budget)
			if err != nil {
				return textEditError(err)
			}
			_, err = replacement.ResolveDocumentText(ctx, metadata)
			_ = bridge.Close()
			if err != nil {
				return textEditError(err)
			}
			continue
		}
		if _, err := record.ResolveDocumentText(ctx, metadata); err != nil {
			return textEditError(err)
		}
	}
	return ctx.Err()
}

func nativeTextCopyImage(ctx context.Context, source *image.NRGBA) (*image.NRGBA, error) {
	owned := image.NewNRGBA(image.Rect(0, 0, source.Rect.Dx(), source.Rect.Dy()))
	for row := range owned.Rect.Dy() {
		for column := 0; column < owned.Stride; column += 32 << 10 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			end := min(column+32<<10, owned.Stride)
			copy(owned.Pix[row*owned.Stride+column:row*owned.Stride+end],
				source.Pix[row*source.Stride+column:row*source.Stride+end])
		}
	}
	return owned, nil
}
