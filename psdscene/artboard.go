package psdscene

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// ArtboardRenderOptions bounds RGB8 artboard rendering and its independent source snapshot.
// Zero MaxWorkingBytes selects 256 MiB; Compose may lower the remaining compositor budget.
type ArtboardRenderOptions struct {
	MaxWorkingBytes int64
	Compose         psdcompose.Options
	// LayerIndex selects a physical source layer for RenderArtboard in a document with multiple boards.
	// Nil requires exactly one artboard. RenderArtboards requires nil and renders every board.
	LayerIndex *int
}

// ArtboardImage associates an owned zero-origin render with one retained artboard's saved coordinates.
type ArtboardImage struct {
	LayerIndex int
	LayerID    uint32
	Name       string
	Bounds     image.Rectangle
	Pixels     *image.NRGBA
}

// RenderArtboard rebuilds one top-level PSD/PSB RGB8 artboard from its live layer tree and retained background.
// Its owned zero-origin image clips to the saved board rectangle and excludes other boards and outside layers.
// Children use the RGB8 compositor's supported blend, mask, adjustment, fill and effect semantics.
// Saved origin metadata has already been applied to layer coordinates and is never applied a second time.
func RenderArtboard(
	ctx context.Context, source *psdfile.Document, options ...ArtboardRenderOptions,
) (*image.NRGBA, error) {
	results, err := renderArtboardSet(ctx, source, false, options...)
	if err != nil {
		return nil, err
	}
	return results[0].Pixels, nil
}

// RenderArtboards rebuilds every top-level RGB8 artboard independently, in physical source-layer order.
// Every image is owned and cropped to its board; overlapping boards never contribute to one another.
// The source is decoded once, and the working limit includes all returned images and transient composition.
func RenderArtboards(
	ctx context.Context, source *psdfile.Document, options ...ArtboardRenderOptions,
) ([]ArtboardImage, error) {
	return renderArtboardSet(ctx, source, true, options...)
}

func renderArtboardSet(
	ctx context.Context, source *psdfile.Document, all bool, options ...ArtboardRenderOptions,
) ([]ArtboardImage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || len(options) > 1 {
		return nil, artboardRenderError(psdfile.ErrMalformed, "options", 0, "provide a source and at most one option value")
	}
	configuration := ArtboardRenderOptions{MaxWorkingBytes: 256 << 20}
	if len(options) == 1 {
		configuration = options[0]
		if configuration.MaxWorkingBytes == 0 {
			configuration.MaxWorkingBytes = 256 << 20
		}
	}
	if configuration.MaxWorkingBytes < 0 || configuration.Compose.MaxWorkingBytes < 0 ||
		configuration.Compose.MaxDepth < 0 || configuration.Compose.MaxLayers < 0 ||
		configuration.Compose.MaxFeatherRadius < 0 || math.IsNaN(configuration.Compose.MaxFeatherRadius) ||
		math.IsInf(configuration.Compose.MaxFeatherRadius, 0) {
		return nil, artboardRenderError(psdfile.ErrMalformed, "options", 0, "rendering limits must be finite and nonnegative")
	}
	if all && configuration.LayerIndex != nil {
		return nil, artboardRenderError(psdfile.ErrMalformed, "options.layerIndex", 0,
			"RenderArtboards does not accept a single-board selector")
	}
	if source.Header.Version != 1 && source.Header.Version != 2 ||
		source.Header.Depth != 8 || source.Header.ColorMode != 3 ||
		source.Header.Channels < 3 || source.Header.Channels > 16 {
		return nil, artboardRenderError(psdfile.ErrUnsupported, "header", 0,
			"artboard rendering requires PSD/PSB RGB8")
	}
	if source.Header.Width == 0 || source.Header.Height == 0 || source.Header.Width > psdmodel.MaxDimension ||
		source.Header.Height > psdmodel.MaxDimension {
		return nil, artboardRenderError(psdfile.ErrLimit, "header", 0, "saved canvas exceeds the snapshot dimension limit")
	}
	budget := renderer{options: Options{MaxWorkingBytes: configuration.MaxWorkingBytes}}
	views, err := source.Artboards(ctx, psdfile.MetadataOptions{MaxBytes: configuration.MaxWorkingBytes})
	if err != nil {
		return nil, err
	}
	boards, err := artboardRenderMetadata(views, source)
	if err != nil {
		return nil, err
	}
	if !all {
		if configuration.LayerIndex == nil {
			if len(boards) != 1 {
				return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.selection", 0,
					"select a physical LayerIndex or use RenderArtboards for multiple boards")
			}
		} else {
			index := -1
			for candidate, record := range boards {
				if record.LayerIndex == *configuration.LayerIndex {
					index = candidate
				}
			}
			if index < 0 {
				return nil, artboardRenderError(psdfile.ErrMalformed, "artboard.selection", 0,
					"selected physical source layer is not an artboard")
			}
			boards = boards[index : index+1]
		}
	}
	for _, record := range views {
		err := budget.reserve(record.Data.MetadataBytes+256, "artboard.metadata", record.Data.Block.Data.Offset)
		if err != nil {
			return nil, err
		}
	}
	if err := artboardSnapshotBudget(ctx, source, &budget); err != nil {
		return nil, err
	}
	snapshot, err := source.Decode(ctx)
	if err != nil {
		return nil, err
	}
	physical := flatten(snapshot.Layers)
	if len(physical) != len(source.Layers) {
		return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.tree", 0,
			"decoded tree differs from the physical source layers")
	}
	issues := snapshot.BlockingIssues[:0]
	for _, issue := range snapshot.BlockingIssues {
		if issue != "首版暂不支持画板文档" {
			issues = append(issues, issue)
		}
	}
	snapshot.BlockingIssues = issues
	// The compatibility decoder also owns a footer image; composition always reads the layer tree.
	snapshot.Composite = nil
	if err := budget.reserve(int64(len(boards))*128, "artboard.outputs", 0); err != nil {
		return nil, err
	}
	results := make([]ArtboardImage, 0, len(boards))
	patterns := patternSceneSources{tiles: make(map[string]*image.NRGBA)}
	invisible := hiddenLayers(snapshot.Layers)
	for _, record := range boards {
		group := physical[record.LayerIndex]
		topLevel := false
		for _, layer := range snapshot.Layers {
			topLevel = topLevel || layer == group
		}
		if !topLevel {
			return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.tree", record.Data.Block.Span.Offset,
				"independent artboards must be top-level groups")
		}
		if err := artboardRenderLayer(group, record.Data.Block.Span.Offset); err != nil {
			return nil, err
		}
		for _, layer := range flatten(group.Children) {
			if layer == nil || invisible[layer] {
				continue
			}
			if layer.Placed != nil {
				return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.smartObject", record.Data.Block.Span.Offset,
					"independent artboard smart objects require live source resolution")
			}
			if err := budget.renderPattern(ctx, source, layer, &patterns); err != nil {
				return nil, err
			}
		}
		result, err := renderOneArtboard(ctx, snapshot, group, record, configuration, &budget)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func renderOneArtboard(
	ctx context.Context,
	snapshot *psdmodel.Document,
	group *psdmodel.Layer,
	record psdfile.ArtboardRecord,
	configuration ArtboardRenderOptions,
	budget *renderer,
) (ArtboardImage, error) {
	board := record.Data
	bounds := image.Rect(int(board.Rect.Left), int(board.Rect.Top), int(board.Rect.Right), int(board.Rect.Bottom))
	result := ArtboardImage{LayerIndex: record.LayerIndex, LayerID: record.LayerID, Name: record.LayerName, Bounds: bounds}
	outputBytes := int64(bounds.Dx()) * int64(bounds.Dy()) * 4
	if err := budget.reserve(outputBytes, "artboard.output", board.Block.Data.Offset); err != nil {
		return ArtboardImage{}, err
	}
	transient := int64(512) + int64(len(group.Children)+1)*8
	if *board.BackgroundType != 3 {
		transient += outputBytes + 512
	}
	if err := budget.reserve(transient, "artboard.background", board.Block.Data.Offset); err != nil {
		return ArtboardImage{}, err
	}
	defer func() { budget.working -= transient }()
	ownedGroup := *group
	if *board.BackgroundType != 3 {
		background := image.NewNRGBA(bounds)
		color := artboardBackgroundColor(board)
		for y := range bounds.Dy() {
			if err := ctx.Err(); err != nil {
				return ArtboardImage{}, err
			}
			for x := range bounds.Dx() {
				offset := y*background.Stride + x*4
				copy(background.Pix[offset:offset+3], color[:])
				background.Pix[offset+3] = 255
			}
		}
		children := make([]*psdmodel.Layer, len(group.Children)+1)
		children[0] = &psdmodel.Layer{Name: "Artboard background", Bounds: bounds,
			Pixels: background, BlendMode: "normal"}
		copy(children[1:], group.Children)
		ownedGroup.Children = children
	}
	remaining := configuration.MaxWorkingBytes - budget.working
	if remaining <= 0 {
		return ArtboardImage{}, artboardRenderError(psdfile.ErrLimit, "artboard.composite", 0,
			"no compositor storage remains")
	}
	compose := configuration.Compose
	if compose.MaxWorkingBytes == 0 || compose.MaxWorkingBytes > remaining {
		compose.MaxWorkingBytes = remaining
	}
	boardDocument := *snapshot
	boardDocument.Layers = []*psdmodel.Layer{&ownedGroup}
	canvas, err := psdcompose.Render(ctx, &boardDocument, compose)
	if err != nil {
		return ArtboardImage{}, artboardComposeError(err)
	}
	result.Pixels = image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for y := range bounds.Dy() {
		if err := ctx.Err(); err != nil {
			return ArtboardImage{}, err
		}
		start := canvas.PixOffset(bounds.Min.X, bounds.Min.Y+y)
		copy(result.Pixels.Pix[y*result.Pixels.Stride:y*result.Pixels.Stride+bounds.Dx()*4],
			canvas.Pix[start:start+bounds.Dx()*4])
	}
	return result, nil
}

func artboardRenderMetadata(views []psdfile.ArtboardRecord, source *psdfile.Document) (
	[]psdfile.ArtboardRecord, error,
) {
	var document *psdfile.ArtboardData
	var boards []psdfile.ArtboardRecord
	seen := make(map[int]bool)
	for _, view := range views {
		if view.Data == nil || len(view.Data.Unsupported) != 0 {
			return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.metadata", 0,
				"unrecognized or ambiguous artboard metadata")
		}
		if view.LayerIndex >= 0 {
			if seen[view.LayerIndex] || view.LayerIndex >= len(source.Layers) || view.Data.Block.Key == "artd" {
				return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.count", view.Data.Block.Data.Offset,
					"duplicate or ambiguous per-layer artboard descriptors")
			}
			seen[view.LayerIndex] = true
			if err := artboardRectangleMetadata(view.Data, source); err != nil {
				return nil, err
			}
			boards = append(boards, view)
		} else {
			if document != nil || view.Data.Block.Key != "artd" {
				return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.document", view.Data.Block.Data.Offset,
					"one document-level artd descriptor is required")
			}
			document = view.Data
		}
	}
	if len(boards) == 0 || document == nil {
		return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.rectangle", 0,
			"explicit board and document-level metadata are required")
	}
	if err := artboardDocumentMetadata(document, len(boards)); err != nil {
		return nil, err
	}
	return boards, nil
}

func artboardRectangleMetadata(board *psdfile.ArtboardData, source *psdfile.Document) error {
	if board.Rect == nil || board.BackgroundType == nil {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.rectangle", board.Block.Data.Offset,
			"explicit rectangle and background metadata are required")
	}
	for _, value := range []float64{board.Rect.Left, board.Rect.Top, board.Rect.Right, board.Rect.Bottom} {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < 0 ||
			value > psdmodel.MaxDimension {
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.rectangle", board.Block.Data.Offset,
				"saved artboard rectangles must have nonnegative integer coordinates")
		}
	}
	if board.Rect.Right <= board.Rect.Left || board.Rect.Bottom <= board.Rect.Top ||
		board.Rect.Right > float64(source.Header.Width) || board.Rect.Bottom > float64(source.Header.Height) {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.rectangle", board.Block.Data.Offset,
			"saved artboard rectangle must lie inside the saved canvas")
	}
	if *board.BackgroundType < 1 || *board.BackgroundType > 4 ||
		*board.BackgroundType == 4 && board.BackgroundRGB == nil {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.background", board.Block.Data.Offset,
			"unrecognized background type or missing custom RGB color")
	}
	for _, entry := range board.UnknownEntries {
		if entry.Key != "guideIndeces" {
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.parameter", entry.Data.Offset,
				"unrecognized artboard parameter")
		}
	}
	return nil
}

func artboardDocumentMetadata(document *psdfile.ArtboardData, boards int) error {
	count, origin, expansion := false, false, false
	var originPoint, expansionPoint [2]float64
	for _, entry := range document.OrderedEntries {
		switch entry.Key {
		case "Cnt ":
			value, valid := entry.Value.(int32)
			count = valid && int64(value) == int64(boards)
		case "origin", "autoExpandOffset":
			point, valid := entry.Value.(*psdfile.Descriptor)
			if !valid || len(point.Entries) != 2 {
				return artboardRenderError(psdfile.ErrUnsupported, "artboard.origin", entry.Data.Offset,
					"unrecognized saved artboard origin")
			}
			seen, values := [2]bool{}, [2]float64{}
			for _, component := range point.Entries {
				value, valid := component.Value.(float64)
				coordinate := 0
				if component.Key == "Vrtc" {
					coordinate = 1
				}
				if !valid || math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value ||
					math.Abs(value) > psdmodel.MaxDimension || seen[coordinate] ||
					component.Key != "Hrzn" && component.Key != "Vrtc" {
					return artboardRenderError(psdfile.ErrUnsupported, "artboard.origin", component.Data.Offset,
						"saved origin must have bounded integer horizontal and vertical components")
				}
				seen[coordinate], values[coordinate] = true, value
			}
			if entry.Key == "origin" {
				origin, originPoint = true, values
			} else {
				expansion, expansionPoint = true, values
			}
		case "autoExpandEnabled", "autoNestEnabled", "autoPositionEnabled", "shrinkwrapOnSaveEnabled":
			if _, valid := entry.Value.(bool); !valid {
				return artboardRenderError(psdfile.ErrUnsupported, "artboard.document", entry.Data.Offset,
					"unrecognized artboard document option")
			}
		case "docDefaultNewArtboardBackgroundColor", "docDefaultNewArtboardBackgroundType":
			// Retained defaults apply to newly created boards, not this saved board's background.
		default:
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.document", entry.Data.Offset,
				"unrecognized document-level artboard parameter")
		}
	}
	if !count || !origin || !expansion || originPoint != expansionPoint {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.document", document.Block.Data.Offset,
			"explicit matching board count and saved origin metadata are required")
	}
	return nil
}

func artboardRenderLayer(layer *psdmodel.Layer, offset int64) error {
	if layer == nil || !layer.Group {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.group", offset,
			"artboard metadata must identify a group")
	}
	if layer.Hidden {
		return nil
	}
	if layer.Mask != nil || layer.VectorMask != nil || layer.Clipping ||
		layer.FillColor != nil || layer.Adjustment != nil || layer.Gradient != nil || layer.Pattern != nil ||
		layer.PatternTile != nil || layer.Effects != nil || layer.Placed != nil || len(layer.Unsupported) != 0 {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.layer", offset,
			"artboard group has unsupported own semantics")
	}
	for _, blend := range layer.BlendRanges {
		full := psdmodel.BlendSlider{WhiteLow: 255, WhiteHigh: 255}
		if blend.Source != full || blend.Destination != full {
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.blendIf", offset, "artboard Blend If is not calibrated")
		}
	}
	if layer.BlendMode != "normal" && layer.BlendMode != "norm" && layer.BlendMode != "pass" ||
		layer.Opacity != nil && *layer.Opacity != 1 || layer.FillOpacity != nil && *layer.FillOpacity != 1 {
		return artboardRenderError(psdfile.ErrUnsupported, "artboard.group", offset,
			"artboard group must use normal or pass-through blending at full opacity and fill")
	}
	return nil
}

func artboardSnapshotBudget(ctx context.Context, source *psdfile.Document, budget *renderer) error {
	metadata, err := source.RGB8SnapshotMetadataBytesContext(ctx)
	if err != nil {
		return err
	}
	if err := budget.reserve(metadata+int64(len(source.Layers))*1024, "artboard.snapshot.metadata", 0); err != nil {
		return err
	}
	// Reserve both decoded planes and RGBA images plus all encoded pixel spans conservatively.
	area := int64(source.Header.Width) * int64(source.Header.Height)
	if err := budget.reserve(area*(int64(source.Header.Channels)+4)+source.Composite.Span.Length+(64<<10),
		"artboard.snapshot.composite", 0); err != nil {
		return err
	}
	for _, layer := range source.Layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Mask != nil {
			for _, bounds := range []image.Rectangle{layer.Mask.Bounds, layer.Mask.RealBounds} {
				// Photoshop uses this flagged empty sentinel for a vector-derived raster cache.
				if layer.Mask.Flags&8 != 0 && bounds == image.Rect(0, 0, 0, -1) {
					continue
				}
				width, height := int64(bounds.Dx()), int64(bounds.Dy())
				if width < 0 || height < 0 || width > psdmodel.MaxDimension || height > psdmodel.MaxDimension {
					return artboardRenderError(psdfile.ErrLimit, "artboard.mask.bounds", layer.Mask.Span.Offset,
						"invalid mask dimensions")
				}
				if err := budget.reserve(width*height*8, "artboard.snapshot.mask", layer.Mask.Span.Offset); err != nil {
					return err
				}
			}
		}
		width, height := int64(layer.Bounds.Dx()), int64(layer.Bounds.Dy())
		if width < 0 || height < 0 || width > psdmodel.MaxDimension || height > psdmodel.MaxDimension {
			return artboardRenderError(psdfile.ErrLimit, "artboard.layer.bounds", layer.Span.Offset, "invalid raster dimensions")
		}
		if err := budget.reserve(width*height*8, "artboard.snapshot.raster", layer.Span.Offset); err != nil {
			return err
		}
		seen := [6]bool{}
		for _, channel := range layer.Channels {
			if channel.ID < -3 || channel.ID > 2 {
				return artboardRenderError(psdfile.ErrUnsupported, "artboard.layer.channel", channel.Span.Offset,
					"extra raster channels have uncalibrated semantics")
			}
			if channel.ID < -1 && layer.Mask == nil {
				return artboardRenderError(psdfile.ErrUnsupported, "artboard.layer.channel", channel.Span.Offset,
					"mask channels require retained pixel-mask geometry")
			}
			if seen[channel.ID+3] {
				return artboardRenderError(psdfile.ErrUnsupported, "artboard.layer.channel", channel.Span.Offset,
					"duplicate raster channel identifiers are ambiguous")
			}
			seen[channel.ID+3] = true
			if err := budget.reserve(channel.Span.Length, "artboard.snapshot.encoded", channel.Span.Offset); err != nil {
				return err
			}
		}
		if !layer.Bounds.Empty() && (!seen[3] || !seen[4] || !seen[5]) {
			return artboardRenderError(psdfile.ErrUnsupported, "artboard.layer.channel", layer.Span.Offset,
				"ordinary RGB raster channels are required")
		}
	}
	return nil
}

func artboardBackgroundColor(board *psdfile.ArtboardData) [3]byte {
	if *board.BackgroundType == 1 {
		return [3]byte{255, 255, 255}
	}
	var result [3]byte
	if *board.BackgroundType == 4 {
		for component, value := range *board.BackgroundRGB {
			result[component] = byte(math.Floor(value*255 + 0.5))
		}
	}
	return result
}

func artboardComposeError(err error) error {
	kind := psdfile.ErrMalformed
	if errors.Is(err, psdcompose.ErrUnsupported) {
		kind = psdfile.ErrUnsupported
	} else if errors.Is(err, psdcompose.ErrLimit) {
		kind = psdfile.ErrLimit
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &psdfile.Error{Kind: kind, Cause: err, Feature: "artboard-render",
		Path: "artboard.composite", Detail: err.Error()}
}

func artboardRenderError(kind error, path string, offset int64, detail string) error {
	return &psdfile.Error{Kind: kind, Feature: "artboard-render", Path: path, Offset: offset, Detail: detail}
}
