package psdedit

import (
	"context"
	"errors"
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdartboard"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdscene"
)

// prepareCompositeLocked prepares owned merged bytes without changing the immutable source or edit overlays.
func (e *Editor) prepareCompositeLocked(
	ctx context.Context, options SaveOptions,
) (preparedComposite, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return preparedComposite{}, err
	}
	if e.closed || e.source == nil {
		return preparedComposite{}, psdfile.ErrClosed
	}
	result := preparedComposite{header: e.source.Header, globalAlpha: e.source.GlobalAlpha}
	if options.Composite != nil && options.CompositeImage != nil {
		return result, editError(ErrInvalidEdit, "save.composite", "replacement previews are mutually exclusive")
	}
	if !e.visualChanged && options.Composite == nil && options.CompositeImage == nil {
		return result, nil
	}
	if options.MaxOutputBytes < 0 {
		return result, editError(psdfile.ErrLimit, "save.MaxOutputBytes", "limit must not be negative")
	}
	var planes *psdfile.CompositePixels
	externalInputBytes := int64(0)
	var err error
	switch {
	case options.Composite != nil:
		planes = options.Composite
		if err := e.validateCompositeLocked(planes, &result); err != nil {
			return result, err
		}
	case options.CompositeImage != nil:
		if err := e.rgbCompositeSupportedLocked(); err != nil {
			return result, err
		}
		externalInputBytes = int64(cap(options.CompositeImage.Pix))
		planes, err = e.imageCompositeForWriteLocked(ctx, options.CompositeImage, &result, options, externalInputBytes)
	case e.nativeTextPreview != nil && e.nativeTextPreviewRevision == e.revision:
		if err := e.rgbCompositeSupportedLocked(); err != nil {
			return result, err
		}
		planes, err = e.imageCompositeForWriteLocked(ctx, e.nativeTextPreview, &result, options, 0)
	default:
		if err := e.rgbCompositeSupportedLocked(); err != nil {
			return result, err
		}
		workingBytes, budgetErr := e.compositeBudgetLocked()
		if budgetErr != nil {
			return result, budgetErr
		}
		if options.reservedForRender >= workingBytes {
			return result, editError(psdfile.ErrLimit, "save.composite", "future encoded buffers exhaust composition budget")
		}
		pixels, renderErr := e.renderCompositeLocked(ctx, psdcompose.Options{
			MaxWorkingBytes:                 workingBytes - options.reservedForRender,
			ApproximateHueSaturation:        options.ApproximateHueSaturation,
			ApproximateDropShadow:           options.ApproximateDropShadow,
			ApproximateClippedGroupBackdrop: options.ApproximateClippedGroupBackdrop,
			MaxClippedGroupEvaluations:      options.MaxClippedGroupEvaluations,
		})
		if renderErr != nil {
			return result, renderErr
		}
		externalInputBytes = int64(cap(pixels.Pix))
		planes, err = e.imageCompositeForWriteLocked(ctx, pixels, &result, options, externalInputBytes)
	}
	if err != nil {
		return result, err
	}
	limit := min(e.source.Limits().MaxFileBytes, e.source.Limits().MaxDecodeBytes)
	if options.MaxOutputBytes != 0 {
		limit = min(limit, options.MaxOutputBytes)
	}
	compression := options.Compression
	if compression == 2 || compression == 3 {
		// Native Photoshop supports Raw/RLE merged data; layer-channel compression remains independent.
		compression = 0
		if result.header.Depth <= 8 {
			compression = 1
		}
	}
	encodedBound, err := writerCompositeEncodedBound(*planes, compression, result.header.Version)
	if err != nil {
		return result, err
	}
	samples, err := writerPlaneSamples(planes.Channels)
	if err != nil {
		return result, err
	}
	capacityLimit, err := e.writerOutputLimitLocked(options, externalInputBytes, samples, compression, 0)
	if err != nil {
		return result, err
	}
	if compression == 0 && encodedBound > capacityLimit {
		return result, editError(psdfile.ErrLimit, "save.working", "Raw composite capacity exceeds remaining working budget")
	}
	limit = min(limit, capacityLimit)
	limit = min(limit, encodedBound)
	result.data, err = psdfile.EncodeCompositeWithOptions(
		ctx, *planes, compression, result.header.Version,
		psdfile.EncoderOptions{Limits: e.source.Limits(), MaxEncodedBytes: limit},
	)
	return result, err
}

func (e *Editor) imageCompositeForWriteLocked(
	ctx context.Context, pixels *image.NRGBA, result *preparedComposite, options SaveOptions, externalInputBytes int64,
) (*psdfile.CompositePixels, error) {
	if err := compositeImageShape(pixels, int(e.source.Header.Width), int(e.source.Header.Height)); err != nil {
		return nil, err
	}
	channels := int64(e.source.Header.Channels)
	if channels == 3 && !pixels.Opaque() {
		channels = 4
	}
	width, height := int64(pixels.Rect.Dx()), int64(pixels.Rect.Dy())
	if height == 0 || channels <= 0 || width > e.source.Limits().MaxDecodeBytes/height/channels {
		return nil, editError(psdfile.ErrLimit, "save.working", "preview planes exceed MaxDecodeBytes")
	}
	if _, err := e.writerOutputLimitLocked(options, externalInputBytes, width*height*channels, 0, 0); err != nil {
		return nil, err
	}
	return e.imageCompositeLocked(ctx, pixels, result)
}

func (e *Editor) rgbCompositeSupportedLocked() error {
	header := e.source.Header
	if header.Depth != 8 || header.ColorMode != 3 ||
		(header.Channels != 3 && (header.Channels != 4 || !e.source.GlobalAlpha)) {
		return editError(
			ErrUnsupported, "save.composite",
			"RGBA preview reconstruction requires RGB8 with three color channels and optional merged transparency",
		)
	}
	return nil
}

func (e *Editor) validateCompositeLocked(
	pixels *psdfile.CompositePixels, result *preparedComposite,
) error {
	header := e.source.Header
	if pixels.Width != int(header.Width) || pixels.Height != int(header.Height) ||
		pixels.Depth != header.Depth || pixels.ColorMode != header.ColorMode {
		return editError(ErrInvalidEdit, "save.composite", "preview shape, depth or color mode differs from the document")
	}
	channels := len(pixels.Channels)
	if channels != int(header.Channels) {
		if header.Depth != 8 || header.ColorMode != 3 || header.Channels != 3 || channels != 4 {
			return editError(ErrInvalidEdit, "save.composite.channels", "preview channel count differs from the document")
		}
		result.header.Channels, result.globalAlpha = 4, true
	}
	if result.globalAlpha && len(e.roots) == 0 {
		return editError(ErrUnsupported, "save.composite", "merged transparency requires a writable layer record")
	}
	for index, plane := range pixels.Channels {
		if plane.ID != int16(index) || plane.Depth != pixels.Depth ||
			plane.Width != pixels.Width || plane.Height != pixels.Height {
			return editError(ErrInvalidEdit, "save.composite.channels", "preview channels have inconsistent IDs or shape")
		}
	}
	return nil
}

// imageCompositeLocked stores RGB merged pixels over white, as required for a transparency-bearing PSD preview.
func (e *Editor) imageCompositeLocked(
	ctx context.Context, pixels *image.NRGBA, result *preparedComposite,
) (*psdfile.CompositePixels, error) {
	header := e.source.Header
	if err := compositeImageShape(pixels, int(header.Width), int(header.Height)); err != nil {
		return nil, err
	}
	area := int64(header.Width) * int64(header.Height)
	limits := e.source.Limits()
	if area > limits.MaxDecodePixels || area > limits.MaxDecodeBytes/8 {
		return nil, editError(psdfile.ErrLimit, "save.composite", "RGBA preview and planar samples exceed decode budgets")
	}
	opaque := pixels.Opaque()
	channels := int(header.Channels)
	if channels == 3 && !opaque {
		channels, result.header.Channels, result.globalAlpha = 4, 4, true
	}
	if result.globalAlpha && len(e.roots) == 0 {
		return nil, editError(ErrUnsupported, "save.composite", "merged transparency requires a writable layer record")
	}
	planes := &psdfile.CompositePixels{
		Width: int(header.Width), Height: int(header.Height), Depth: 8, ColorMode: 3,
		Channels: make([]psdfile.Plane, channels),
	}
	for index := range channels {
		planes.Channels[index] = psdfile.Plane{
			ID: int16(index), Width: planes.Width, Height: planes.Height, Depth: 8,
			RowBytes: planes.Width, Data: make([]byte, int(area)),
		}
	}
	for row := range planes.Height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range planes.Width {
			sourceOffset, targetOffset := row*pixels.Stride+column*4, row*planes.Width+column
			alpha := uint32(pixels.Pix[sourceOffset+3])
			for component := range 3 {
				value := uint32(pixels.Pix[sourceOffset+component])
				if result.globalAlpha {
					value = (value*alpha + 255*(255-alpha) + 127) / 255
				}
				planes.Channels[component].Data[targetOffset] = byte(value)
			}
			if channels == 4 {
				planes.Channels[3].Data[targetOffset] = byte(alpha)
			}
		}
	}
	return planes, nil
}

func compositeImageShape(pixels *image.NRGBA, width, height int) error {
	if pixels == nil || width <= 0 || height <= 0 ||
		width > int(^uint(0)>>1)/4 || pixels.Rect.Max.X < pixels.Rect.Min.X ||
		pixels.Rect.Max.Y < pixels.Rect.Min.Y ||
		pixels.Rect.Dx() != width || pixels.Rect.Dy() != height ||
		pixels.Stride < width*4 || len(pixels.Pix) < width*4 ||
		height-1 > (len(pixels.Pix)-width*4)/pixels.Stride {
		return editError(ErrInvalidEdit, "save.pixels", "RGBA image dimensions, stride or backing buffer are invalid")
	}
	return nil
}

// compositeBudgetLocked reserves the snapshot, retained overlays and sample planes before composition.
func (e *Editor) compositeBudgetLocked() (int64, error) {
	limits := e.source.Limits()
	area := int64(e.source.Header.Width) * int64(e.source.Header.Height)
	if area > limits.MaxDecodePixels || area > limits.MaxDecodeBytes/8 {
		return 0, editError(psdfile.ErrLimit, "save.composite", "canvas and merged samples exceed decode budgets")
	}
	// Reserve the source preview, merged sample planes and retained owned edit overlays.
	bytesNeeded := area*8 + e.ownedBytesLocked(nil)
	metadataBytes, err := e.source.RGB8SnapshotMetadataBytes()
	if err != nil {
		return 0, err
	}
	if metadataBytes > limits.MaxDecodeBytes-bytesNeeded {
		return 0, editError(psdfile.ErrLimit, "save.composite", "advanced decoded metadata exceeds the working budget")
	}
	bytesNeeded += metadataBytes
	if bytesNeeded > limits.MaxDecodeBytes {
		return 0, editError(psdfile.ErrLimit, "save.composite", "owned edits and canvas samples exceed decode budgets")
	}
	add := func(bounds image.Rectangle) error {
		width, height := int64(bounds.Dx()), int64(bounds.Dy())
		if width < 0 || height < 0 || width > psdmodel.MaxDimension || height > psdmodel.MaxDimension ||
			width*height > limits.MaxDecodePixels || width*height > (limits.MaxDecodeBytes-bytesNeeded)/4 {
			return editError(psdfile.ErrLimit, "save.composite", "decoded layers and working buffers exceed decode budgets")
		}
		bytesNeeded += width * height * 4
		return nil
	}
	for _, record := range e.source.Layers {
		if 512 > limits.MaxDecodeBytes-bytesNeeded {
			return 0, editError(psdfile.ErrLimit, "save.composite", "decoded source metadata exceeds decode budgets")
		}
		bytesNeeded += 512
		if err := add(record.Bounds); err != nil {
			return 0, err
		}
		if record.Mask != nil {
			if err := add(record.Mask.Bounds); err != nil {
				return 0, err
			}
			if record.Mask.HasReal {
				if err := add(record.Mask.RealBounds); err != nil {
					return 0, err
				}
			}
		}
	}
	var visit func([]*editNode) error
	visit = func(nodes []*editNode) error {
		for _, node := range nodes {
			// The editor snapshot allocates copied layer metadata, opacity values and tree pointer slices.
			if 512 > limits.MaxDecodeBytes-bytesNeeded {
				return editError(psdfile.ErrLimit, "save.composite", "edited tree metadata exceeds decode budgets")
			}
			bytesNeeded += 512
			if node.pixelMaskChange != nil && !node.pixelMaskChange.removed {
				if err := add(node.pixelMaskChange.bounds); err != nil {
					return err
				}
			}
			if len(node.planes) != 0 {
				if err := add(node.bounds); err != nil {
					return err
				}
				if plane, exists := node.planes[-2]; exists {
					if err := add(image.Rect(0, 0, plane.Width, plane.Height)); err != nil {
						return err
					}
				}
			}
			if err := visit(node.children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(e.roots); err != nil {
		return 0, err
	}
	if int64(len(e.source.Layers))*8 > limits.MaxDecodeBytes-bytesNeeded {
		return 0, editError(psdfile.ErrLimit, "save.composite", "physical snapshot index exceeds decode budgets")
	}
	bytesNeeded += int64(len(e.source.Layers)) * 8
	remaining := limits.MaxDecodeBytes - bytesNeeded
	if remaining <= 0 {
		return 0, editError(psdfile.ErrLimit, "save.composite", "no working budget remains for layer composition")
	}
	return remaining, nil
}

func (e *Editor) renderCompositeLocked(ctx context.Context, options ...psdcompose.Options) (*image.NRGBA, error) {
	return e.renderCompositeNodesLocked(ctx, e.roots, options...)
}

func (e *Editor) renderCompositeNodesLocked(
	ctx context.Context, roots []*editNode, options ...psdcompose.Options,
) (*image.NRGBA, error) {
	if len(options) > 1 {
		return nil, editError(ErrInvalidEdit, "composite.options", "provide at most one composite options value")
	}
	workingBytes, err := e.compositeBudgetLocked()
	if err != nil {
		return nil, err
	}
	configuration := psdcompose.Options{MaxWorkingBytes: workingBytes}
	if len(options) == 1 {
		configuration = options[0]
		if configuration.MaxWorkingBytes < 0 {
			return nil, editError(ErrInvalidEdit, "composite.options", "working budget must not be negative")
		}
		if configuration.MaxWorkingBytes == 0 || configuration.MaxWorkingBytes > workingBytes {
			configuration.MaxWorkingBytes = workingBytes
		}
	}
	boards, boardMetadataBytes, err := e.prepareCompositeArtboardsLocked(ctx, configuration.MaxWorkingBytes)
	if err != nil {
		return nil, compositeRenderError(err)
	}
	workingBytes -= boardMetadataBytes
	configuration.MaxWorkingBytes -= boardMetadataBytes
	if configuration.MaxWorkingBytes <= 0 {
		return nil, editError(psdfile.ErrLimit, "save.artboards", "artboard metadata consumes composition budget")
	}
	var snapshot *psdmodel.Document
	var physical []*psdmodel.Layer
	if len(e.source.Layers) == 0 {
		snapshot, err = e.source.Decode(ctx)
	} else if configuration.ApproximateDropShadow {
		snapshot, physical, err = e.dropShadowSnapshotLocked(ctx, &workingBytes)
	} else {
		snapshot, err = e.source.DecodeLayers(ctx)
	}
	if err != nil {
		return nil, compositeRenderError(err)
	}
	if physical == nil {
		physical = flattenSnapshot(snapshot.Layers)
	}
	if len(physical) != len(e.source.Layers) {
		return nil, editError(ErrUnsupported, "save.composite", "decoded tree does not match original physical layer records")
	}
	var patterns patternEditorSources
	canvas := image.Rect(0, 0, int(e.source.Header.Width), int(e.source.Header.Height))
	var visit func([]*editNode, bool) ([]*psdmodel.Layer, error)
	visit = func(nodes []*editNode, hidden bool) ([]*psdmodel.Layer, error) {
		result := make([]*psdmodel.Layer, 0, len(nodes))
		for _, node := range nodes {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			invisible := hidden || !node.visible || node.opacity == 0
			layer := &psdmodel.Layer{BlendMode: "normal"}
			if node.sourceIndex >= 0 {
				if node.sourceIndex >= len(physical) || physical[node.sourceIndex] == nil {
					return nil, editError(ErrInvalidEdit, "save.layers", "layer handle has no matching original record")
				}
				layerCopy := *physical[node.sourceIndex]
				layer = &layerCopy
				record := e.source.Layers[node.sourceIndex]
				if !invisible && record.Divider > 3 {
					return nil, editError(ErrUnsupported, "save.layers.divider", "source group divider has unknown semantics")
				}
				for _, channel := range record.Channels {
					if node.pixelMaskChange != nil && node.pixelMaskChange.removed &&
						channel.ID == node.pixelMaskChange.channelID {
						continue
					}
					knownRealMask := channel.ID == -3 && record.Mask != nil && record.Mask.HasReal &&
						record.Mask.Flags&8 != 0 && layer.VectorMask != nil
					if !invisible && (channel.ID < -2 || channel.ID > 2) && !knownRealMask {
						return nil, editError(
							ErrUnsupported, "save.layers.channels", "source channel has unsupported rendering semantics",
						)
					}
				}
			}
			layer.Name, layer.Bounds, layer.Hidden, layer.Group = node.name, node.bounds, !node.visible, node.group
			opacity := float64(node.opacity) / 255
			layer.Opacity = &opacity
			if node.fillOpacityChanged {
				fill := float64(node.fillOpacity) / 255
				layer.FillOpacity = &fill
			}
			if node.blendModeChanged {
				layer.BlendMode = node.blendMode
			} else if node.group && node.sourceIndex < 0 {
				layer.BlendMode = "pass"
			}
			if node.clippingChanged {
				layer.Clipping = node.clipping
			}
			if node.blendRangesChanged {
				layer.BlendRanges = node.blendRanges
			}
			applyAdvancedBlending(layer, node)
			if node.vectorMaskChange != nil && node.vectorMaskChange.dropGenerated && !node.vectorMaskChange.promoteReal {
				layer.Mask = nil
			}
			if node.vectorMaskChange != nil && node.maskData != nil && node.pixelMask == nil {
				// Removed shared parameters must not reappear from the source after vector re-addition.
				layer.VectorDensity, layer.VectorFeather = nil, nil
			}
			if err := applyPixelMaskChange(ctx, layer, node.pixelMaskChange); err != nil {
				return nil, err
			}
			applyPixelMaskSettings(layer, node.pixelMask)
			if node.vectorMask != nil {
				layer.VectorMask = node.vectorMask
			}
			if node.vectorMaskChange != nil && node.vectorMaskChange.removed {
				layer.VectorMask, layer.VectorDensity, layer.VectorFeather = nil, nil, nil
			}
			if node.group {
				children, err := visit(node.children, invisible)
				if err != nil {
					return nil, err
				}
				layer.Children = children
				prepared, cost, err := boards.prepare(ctx, node, layer, canvas,
					min(configuration.MaxWorkingBytes, workingBytes))
				if err != nil {
					return nil, compositeRenderError(err)
				}
				layer = prepared
				workingBytes -= cost
				configuration.MaxWorkingBytes -= cost
				result = append(result, layer)
				continue
			}
			layer.Children = nil
			if node.pixels != nil {
				layer.Pixels = node.pixels
			} else if node.virtualComposite || node.sourceIndex == -2 {
				layer.Pixels = snapshot.Composite
			}
			if node.fillColor != nil {
				layer.FillColor = node.fillColor
			}
			if node.adjustment != nil {
				layer.Adjustment = node.adjustment
			}
			if node.gradient != nil {
				layer.Gradient = node.gradient
			}
			if node.pattern != nil {
				layer.Pattern, layer.PatternTile = node.pattern, node.patternTile
			} else if !invisible && layer.Pattern != nil {
				if err := e.decodeCompositePatternLocked(ctx, layer, &patterns, &workingBytes); err != nil {
					return nil, err
				}
			}
			if node.effects != nil {
				layer.Effects = node.effects
			}
			if node.placed != nil {
				layer.Placed = node.placed
			}
			if !invisible && configuration.ApproximateDropShadow && layer.Placed != nil &&
				layer.Effects != nil && effectsPureDropShadow(*layer.Effects) {
				if node.placed != nil || node.pixels != nil || len(node.planes) != 0 {
					return nil, editError(ErrUnsupported, "save.layers.effects",
						"placed shadow previews require unchanged original placement and source channels")
				}
				projectionBudget := workingBytes / 2
				if projectionBudget <= 0 {
					return nil, editError(psdfile.ErrLimit, "save.layers.effects", "no placed source budget remains")
				}
				live, err := psdscene.RenderPlacedLayer(ctx, e.source, node.sourceIndex, psdscene.Options{
					MaxWorkingBytes: projectionBudget, Compose: configuration,
				})
				if err != nil {
					return nil, compositeRenderError(err)
				}
				area := int64(live.Pixels.Rect.Dx()) * int64(live.Pixels.Rect.Dy())
				if area > projectionBudget/4 {
					return nil, editError(psdfile.ErrLimit, "save.layers.effects",
						"rendered placed artwork exceeds preview working budget")
				}
				// The rendered pixels, placement snapshot and resolver-owned source metadata coexist with composition.
				workingBytes -= projectionBudget
				layer.Pixels, layer.Bounds, layer.Placed = live.Pixels, live.Bounds, live.Placed
				layer.RenderedEffects = live.RenderedEffects
			}
			if err := compositeOverlayPlanes(ctx, layer, node.planes); err != nil {
				return nil, err
			}
			result = append(result, layer)
		}
		return result, nil
	}
	layers, err := visit(roots, false)
	if err != nil {
		return nil, err
	}
	// The original merged preview is only a pixel source for a virtual background, never a render fallback.
	snapshot.Layers, snapshot.Composite = layers, nil
	if boards != nil {
		// Only validated artboard metadata is consumed; unrelated source blockers remain active.
		issues := snapshot.BlockingIssues[:0]
		for _, issue := range snapshot.BlockingIssues {
			if issue != psdartboard.Issue {
				issues = append(issues, issue)
			}
		}
		snapshot.BlockingIssues = issues
	}
	configuration.MaxWorkingBytes = min(configuration.MaxWorkingBytes, workingBytes)
	if configuration.MaxWorkingBytes <= 0 {
		return nil, editError(psdfile.ErrLimit, "save.artboards", "prepared artboards consume composition budget")
	}
	pixels, err := psdcompose.Render(ctx, snapshot, configuration)
	if err != nil {
		return nil, compositeRenderError(err)
	}
	return pixels, nil
}

// Composite renders the current edited RGB8 layer tree into independently owned RGBA pixels.
// It applies the same feature checks and total working budgets used for an automatically rebuilt saved image.
// Rendering leaves the edit revision and saved revision unchanged.
// Supported top-level RGB8 artboards retain their saved clipping and background without a supplied composite.
// An optional composition policy may tighten working limits or explicitly select approximate Hue/Saturation.
func (e *Editor) Composite(ctx context.Context, options ...psdcompose.Options) (*image.NRGBA, error) {
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
	return e.renderCompositeLocked(ctx, options...)
}

func compositeRenderError(err error) error {
	kind := error(nil)
	switch {
	case errors.Is(err, psdcompose.ErrUnsupported), errors.Is(err, psdfile.ErrUnsupported):
		kind = ErrUnsupported
	case errors.Is(err, psdcompose.ErrLimit):
		kind = psdfile.ErrLimit
	case errors.Is(err, psdcompose.ErrInvalid):
		kind = ErrInvalidEdit
	}
	if kind == nil {
		return err
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "save.composite", Detail: err.Error(), Cause: err}
}

// flattenSnapshot inserts discarded group-end placeholders to retain original physical-index identity.
func flattenSnapshot(layers []*psdmodel.Layer) []*psdmodel.Layer {
	var result []*psdmodel.Layer
	var visit func([]*psdmodel.Layer)
	visit = func(nodes []*psdmodel.Layer) {
		for _, layer := range nodes {
			if layer.Children != nil {
				result = append(result, nil)
				visit(layer.Children)
			}
			result = append(result, layer)
		}
	}
	visit(layers)
	return result
}

func compositeOverlayPlanes(ctx context.Context, layer *psdmodel.Layer, planes map[int16]psdfile.Plane) error {
	if len(planes) == 0 {
		return nil
	}
	if layer.Pixels == nil {
		return editError(ErrUnsupported, "save.layers", "raw channel overlay has no cached raster base")
	}
	result := image.NewNRGBA(image.Rect(0, 0, layer.Pixels.Rect.Dx(), layer.Pixels.Rect.Dy()))
	for row := range result.Rect.Dy() {
		if err := ctx.Err(); err != nil {
			return err
		}
		copy(result.Pix[row*result.Stride:], layer.Pixels.Pix[row*layer.Pixels.Stride:][:result.Rect.Dx()*4])
	}
	layer.Pixels = result
	for id, plane := range planes {
		if plane.ID != id || id < -2 || id > 2 {
			return editError(ErrUnsupported, "save.layers.channels", "raw overlay channel has unsupported rendering semantics")
		}
		target, component := result, int(id)
		if id == -1 {
			component = 3
		}
		if id == -2 {
			if layer.Mask == nil || layer.Mask.Pixels == nil {
				return editError(ErrUnsupported, "save.layers.mask", "raw mask overlay has no matching saved mask")
			}
			maskCopy := *layer.Mask
			target = image.NewNRGBA(image.Rect(0, 0, maskCopy.Bounds.Dx(), maskCopy.Bounds.Dy()))
			maskCopy.Pixels, layer.Mask, component = target, &maskCopy, 0
		}
		if plane.Depth != 8 || plane.Width != target.Rect.Dx() || plane.Height != target.Rect.Dy() ||
			plane.RowBytes != plane.Width || int64(plane.Width)*int64(plane.Height) != int64(len(plane.Data)) {
			return editError(ErrInvalidEdit, "save.layers.channels", "raw overlay shape differs from the saved RGB8 pixels")
		}
		for row := range plane.Height {
			if err := ctx.Err(); err != nil {
				return err
			}
			for column := range plane.Width {
				offset, value := row*target.Stride+column*4, plane.Data[row*plane.RowBytes+column]
				target.Pix[offset+component] = value
				if id == -2 {
					target.Pix[offset+1], target.Pix[offset+2], target.Pix[offset+3] = value, value, 255
				}
			}
		}
	}
	return nil
}

// nodePixelsLocked borrows owned replacement pixels or decodes independent saved source pixels.
func (e *Editor) nodePixelsLocked(ctx context.Context, node *editNode) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if node.pixels != nil {
		return node.pixels, nil
	}
	if err := e.rgbCompositeSupportedLocked(); err != nil {
		return nil, err
	}
	if node.sourceIndex == -2 || node.virtualComposite {
		area := int64(e.source.Header.Width) * int64(e.source.Header.Height)
		if area > e.source.Limits().MaxDecodePixels || area > e.source.Limits().MaxDecodeBytes/8 {
			return nil, editError(psdfile.ErrLimit, "save.layers", "virtual background decode exceeds sample budgets")
		}
		planes, err := e.source.DecodeComposite(ctx)
		if err != nil {
			return nil, err
		}
		return compositePlanesRGBA(ctx, planes.Channels, planes.Width, planes.Height, true, e.source.GlobalAlpha)
	}
	if node.sourceIndex < 0 || node.sourceIndex >= len(e.source.Layers) {
		return nil, editError(ErrInvalidEdit, "save.layers", "layer has no source or replacement pixels")
	}
	record := e.source.Layers[node.sourceIndex]
	limits := e.source.Limits()
	area := int64(record.Bounds.Dx()) * int64(record.Bounds.Dy())
	if area < 0 || area > limits.MaxDecodePixels || area > limits.MaxDecodeBytes/4 {
		return nil, editError(psdfile.ErrLimit, "save.layers", "layer RGBA conversion exceeds sample budgets")
	}
	bytesNeeded := area * 4
	for _, channel := range record.Channels {
		channelArea := int64(channel.Bounds.Dx()) * int64(channel.Bounds.Dy())
		if channelArea < 0 || channelArea > limits.MaxDecodeBytes-bytesNeeded {
			return nil, editError(psdfile.ErrLimit, "save.layers", "layer samples and RGBA conversion exceed budgets")
		}
		bytesNeeded += channelArea
	}
	planes, err := e.source.DecodeLayer(ctx, node.sourceIndex)
	if err != nil {
		return nil, err
	}
	return compositePlanesRGBA(ctx, planes.Channels, planes.Bounds.Dx(), planes.Bounds.Dy(), false, false)
}

func compositePlanesRGBA(
	ctx context.Context, planes []psdfile.Plane, width, height int, merged, globalAlpha bool,
) (*image.NRGBA, error) {
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for offset := 3; offset < len(pixels.Pix); offset += 4 {
		pixels.Pix[offset] = 255
	}
	for _, plane := range planes {
		component := int(plane.ID)
		if !merged && plane.ID == -1 {
			component = 3
		}
		if component < 0 || component > 3 || (merged && component == 3 && !globalAlpha) {
			continue
		}
		if plane.Depth != 8 || plane.Width != width || plane.Height != height ||
			plane.RowBytes != width || int64(width)*int64(height) != int64(len(plane.Data)) {
			return nil, editError(ErrInvalidEdit, "save.layers.channels", "source plane does not match RGB8 geometry")
		}
		for row := range height {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := range width {
				pixels.Pix[row*pixels.Stride+column*4+component] = plane.Data[row*width+column]
			}
		}
	}
	if merged && globalAlpha {
		for row := range height {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := range width {
				offset := row*pixels.Stride + column*4
				alpha := float64(pixels.Pix[offset+3]) / 255
				if alpha == 0 {
					clear(pixels.Pix[offset : offset+3])
					continue
				}
				for component := range 3 {
					pixels.Pix[offset+component] = psdrender.ClampByte(
						(float64(pixels.Pix[offset+component])-255)/alpha + 255,
					)
				}
			}
		}
	}
	return pixels, nil
}
