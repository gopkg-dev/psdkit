package psdscene

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/internal/psdartboard"
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
	for _, record := range views {
		if record.Data == nil {
			return nil, artboardRenderError(psdfile.ErrUnsupported, "artboard.metadata", 0,
				"unrecognized or ambiguous artboard metadata")
		}
		err := budget.reserve(record.Data.MetadataBytes+256, "artboard.metadata", record.Data.Block.Data.Offset)
		if err != nil {
			return nil, err
		}
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
		if issue != psdartboard.Issue {
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
	window := image.Rect(0, 0, int(snapshot.Width), int(snapshot.Height))
	ownedGroup, transient, err := psdartboard.Prepare(ctx, group, board, window,
		configuration.MaxWorkingBytes-budget.working)
	if err != nil {
		return ArtboardImage{}, err
	}
	if err := budget.reserve(transient, "artboard.background", board.Block.Data.Offset); err != nil {
		return ArtboardImage{}, err
	}
	defer func() { budget.working -= transient }()
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
	boardDocument.Layers = []*psdmodel.Layer{ownedGroup}
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
	return psdartboard.ValidateMetadata(views, source)
}

func artboardRenderLayer(layer *psdmodel.Layer, offset int64) error {
	return psdartboard.ValidateLayer(layer, offset)
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
