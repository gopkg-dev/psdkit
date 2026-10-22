// Package psdscene rebuilds bounded RGB8 documents and recursively resolves placed PSD/PSB content.
package psdscene

import (
	"context"
	"errors"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

// Options controls recursive content resolution and the total retained rendering storage.
// Zero limits select 256 MiB, sixteen content levels and sixty-four decoded documents.
// CachedSpecialLayers explicitly selects saved gradient/pattern/stroke rasters and text without ApproximateText.
// ApproximateText keeps live priority; cached selection preserves all unrelated unsupported semantics.
type Options struct {
	MaxWorkingBytes        int64
	MaxDepth, MaxDocuments int
	ExternalResolver       psdfile.LinkedDocumentResolver
	CachedSpecialLayers    bool
	// CachedTextLayers explicitly uses saved typography pixels without selecting other special-layer caches.
	// Explicit live OpenType text keeps priority; placed contents and smart filters remain live.
	CachedTextLayers bool
	// ApproximateVectorStroke enables bounded solid inside strokes from original vector paths.
	ApproximateVectorStroke bool
	// ApproximateCustomWarp selects the certified cubic-plus-homography mathematical projection.
	// It does not certify Photoshop's projective custom-warp composition.
	ApproximateCustomWarp bool
	Compose               psdcompose.Options
	// ApproximatePlacedProjection selects full-canvas bilinear sampling for transparent-border projective placements.
	// It preserves original source geometry and does not claim Photoshop-native placement pixels.
	ApproximatePlacedProjection bool
	// ApproximateText explicitly opts into unhinted single-style Latin glyph rendering.
	// FontResolver supplies exact fonts; no cached pixels or automatic font substitution are used.
	ApproximateText *TextRenderOptions
	FontResolver    psdfont.FontResolver
	// ApproximateOpenTypeText selects explicit Latin OpenType glyphs through the RGB8 regional source engine.
	// It is mutually exclusive with ApproximateText and propagates through original placed PSD/PSB contents.
	// Its Bounds limits only glyph artwork; ordinary layers keep the complete document canvas.
	ApproximateOpenTypeText *OpenTypeTextRenderOptions
	// OutputProfile explicitly requests encoded output in a supplied RGB ICC profile.
	// Nil preserves document encoding; an empty non-nil profile targets an untagged encoding.
	// A tagged/untagged conversion requires AssumeUntaggedSRGB; two untagged encodings remain unchanged.
	OutputProfile []byte
	// AssumeUntaggedSRGB supplies sRGB for absent profiles during a required conversion.
	AssumeUntaggedSRGB bool
	// CenteredJPEGChroma explicitly selects centered linear reconstruction for embedded JPEG content.
	// Default decoding uses Go's codec; native JPEG pixel equality is not guaranteed by either policy.
	CenteredJPEGChroma bool
	// SmartFilters explicitly selects live filter algorithms and approximation policy.
	// Nil rejects active filters; saved filter RGB arrays never supply rendered content.
	SmartFilters *psdfilter.Options
}

type renderer struct {
	options   Options
	working   int64
	documents int
	active    map[*psdfile.Document]bool
}

// Render rebuilds a document from layers, including recursively rendered embedded PSD/PSB smart objects.
// It preserves source documents and returns an owned image in their encoded RGB color space.
// ICC display conversion and source-precision rendering remain separate operations.
func Render(ctx context.Context, source *psdfile.Document, options ...Options) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || len(options) > 1 {
		return nil, sceneError(psdfile.ErrMalformed, "options", 0, "provide a source and at most one option value")
	}
	configuration := Options{MaxWorkingBytes: 256 << 20, MaxDepth: 16, MaxDocuments: 64}
	if len(options) == 1 {
		provided := options[0]
		if provided.ApproximateText != nil && provided.ApproximateOpenTypeText != nil {
			return nil, sceneError(psdfile.ErrMalformed, "text.policy", 0,
				"choose only one explicit approximate text policy")
		}
		if provided.MaxWorkingBytes < 0 || provided.MaxDepth < 0 || provided.MaxDocuments < 0 {
			return nil, sceneError(psdfile.ErrMalformed, "options", 0, "limits must be nonnegative")
		}
		if provided.MaxWorkingBytes != 0 {
			configuration.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxDepth != 0 {
			configuration.MaxDepth = provided.MaxDepth
		}
		if provided.MaxDocuments != 0 {
			configuration.MaxDocuments = provided.MaxDocuments
		}
		configuration.ExternalResolver = provided.ExternalResolver
		configuration.CachedSpecialLayers = provided.CachedSpecialLayers
		configuration.CachedTextLayers = provided.CachedTextLayers
		configuration.ApproximateVectorStroke = provided.ApproximateVectorStroke
		configuration.ApproximateCustomWarp = provided.ApproximateCustomWarp
		configuration.Compose = provided.Compose
		configuration.ApproximatePlacedProjection = provided.ApproximatePlacedProjection
		configuration.FontResolver = provided.FontResolver
		configuration.AssumeUntaggedSRGB = provided.AssumeUntaggedSRGB
		configuration.CenteredJPEGChroma = provided.CenteredJPEGChroma
		if provided.SmartFilters != nil {
			owned := *provided.SmartFilters
			configuration.SmartFilters = &owned
		}
		if provided.OutputProfile != nil {
			if int64(len(provided.OutputProfile)) > 4<<20 ||
				int64(len(provided.OutputProfile)) >= configuration.MaxWorkingBytes {
				return nil, sceneError(psdfile.ErrLimit, "color.outputProfile", 0,
					"owned output profile exceeds the profile or working budget")
			}
			configuration.OutputProfile = append([]byte{}, provided.OutputProfile...)
		}
		if provided.ApproximateText != nil {
			owned := *provided.ApproximateText
			if owned.Bounds != nil {
				bounds := *owned.Bounds
				owned.Bounds = &bounds
			}
			if owned.Kerning != nil {
				kerning := *owned.Kerning
				owned.Kerning = &kerning
			}
			configuration.ApproximateText = &owned
		}
		if provided.ApproximateOpenTypeText != nil {
			owned, _, err := ownOpenTypeTextOptions(provided.ApproximateOpenTypeText,
				configuration.MaxWorkingBytes-int64(cap(configuration.OutputProfile)))
			if err != nil {
				return nil, err
			}
			configuration.ApproximateOpenTypeText = owned
		}
	}
	if configuration.MaxDepth > 64 {
		return nil, sceneError(psdfile.ErrLimit, "options.maxDepth", 0, "content nesting may not exceed 64")
	}
	if configuration.ApproximateOpenTypeText != nil {
		return RenderTile(ctx, source, image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height)),
			TileOptions{MaxWorkingBytes: configuration.MaxWorkingBytes, MaxPixels: configuration.MaxWorkingBytes / 4,
				MaxDepth: configuration.MaxDepth, MaxDocuments: configuration.MaxDocuments, Compose: configuration.Compose,
				CachedSpecialLayers: configuration.CachedSpecialLayers, ExternalResolver: configuration.ExternalResolver,
				CachedTextLayers: configuration.CachedTextLayers, ApproximateVectorStroke: configuration.ApproximateVectorStroke,
				ApproximateCustomWarp:       configuration.ApproximateCustomWarp,
				ApproximatePlacedProjection: configuration.ApproximatePlacedProjection,
				AssumeUntaggedSRGB:          configuration.AssumeUntaggedSRGB, CenteredJPEGChroma: configuration.CenteredJPEGChroma,
				SmartFilters: configuration.SmartFilters, OutputProfile: configuration.OutputProfile, ownedProfile: true,
				ApproximateOpenTypeText: configuration.ApproximateOpenTypeText, FontResolver: configuration.FontResolver,
				ownedOpenTypeText: true})
	}
	engine := renderer{options: configuration, active: make(map[*psdfile.Document]bool),
		working: int64(len(configuration.OutputProfile))}
	var destination *sceneProfile
	if configuration.OutputProfile != nil {
		destination = &sceneProfile{data: configuration.OutputProfile}
	}
	return engine.render(ctx, source, 1, destination)
}

func (engine *renderer) reserve(amount int64, path string, offset int64) error {
	if amount < 0 || amount > engine.options.MaxWorkingBytes-engine.working {
		return sceneError(psdfile.ErrLimit, path, offset, "retained source snapshots and output exceed the working budget")
	}
	engine.working += amount
	return nil
}

func (engine *renderer) render(ctx context.Context, source *psdfile.Document, depth int,
	destination *sceneProfile,
) (*image.NRGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if depth > engine.options.MaxDepth || engine.documents >= engine.options.MaxDocuments {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.contents", 0, "recursive content limits exceeded")
	}
	if engine.active[source] {
		return nil, sceneError(psdfile.ErrMalformed, "smartObject.contents", 0, "recursive external content cycle")
	}
	engine.active[source] = true
	defer delete(engine.active, source)
	engine.documents++
	startWorking := engine.working
	liveFilterPolicy := engine.options.SmartFilters != nil &&
		(engine.options.SmartFilters.ApproximateSurfaceBlur || engine.options.SmartFilters.ApproximateSmartSharpen ||
			engine.options.SmartFilters.ApproximateBrightnessContrast || engine.options.SmartFilters.ApproximateHueSaturation)
	if (engine.options.Compose.ApproximateDropShadow || engine.options.Compose.ApproximatePlacedColorOverlay ||
		engine.options.ApproximatePlacedProjection ||
		engine.options.ApproximateVectorStroke || engine.options.CachedTextLayers ||
		engine.options.ApproximateCustomWarp || liveFilterPolicy || sourceHasArtboards(source)) &&
		len(source.Layers) != 0 {
		if engine.options.ApproximateText != nil {
			return nil, sceneError(psdfile.ErrUnsupported, "smartObject.text.policy", 0,
				"live placed source policies require the explicit OpenType policy for source text")
		}
		available := engine.options.MaxWorkingBytes - engine.working
		if available <= 0 {
			return nil, sceneError(psdfile.ErrLimit, "smartObject.source", 0, "no live source rendering budget remains")
		}
		settings := TileOptions{MaxWorkingBytes: available, MaxPixels: available / 4,
			MaxDepth: engine.options.MaxDepth, MaxDocuments: engine.options.MaxDocuments,
			ExternalResolver: engine.options.ExternalResolver, Compose: engine.options.Compose,
			ApproximatePlacedProjection: engine.options.ApproximatePlacedProjection,
			CachedTextLayers:            engine.options.CachedTextLayers,
			ApproximateVectorStroke:     engine.options.ApproximateVectorStroke,
			ApproximateCustomWarp:       engine.options.ApproximateCustomWarp,
			CachedSpecialLayers:         engine.options.CachedSpecialLayers, AssumeUntaggedSRGB: engine.options.AssumeUntaggedSRGB,
			CenteredJPEGChroma: engine.options.CenteredJPEGChroma, SmartFilters: engine.options.SmartFilters,
			ApproximateOpenTypeText: engine.options.ApproximateOpenTypeText, FontResolver: engine.options.FontResolver,
			sourceRenderer: engine, sourceDepth: depth}
		if destination != nil {
			settings.OutputProfile = destination.data
			if settings.OutputProfile == nil {
				settings.OutputProfile = []byte{}
			}
		}
		pixels, err := RenderTile(ctx, source,
			image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height)), settings)
		if err != nil {
			return nil, err
		}
		engine.working = startWorking + int64(len(pixels.Pix))
		return pixels, nil
	}
	var profile *sceneProfile
	readProfile := func() (*sceneProfile, error) {
		if profile != nil {
			return profile, nil
		}
		decoded, err := engine.readProfile(ctx, source)
		if err != nil {
			return nil, err
		}
		profile = decoded
		return profile, nil
	}
	metadata, err := source.RGB8SnapshotMetadataBytesContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(metadata+int64(len(source.Layers))*1024, "layers.metadata", 0); err != nil {
		return nil, err
	}
	area := int64(source.Header.Width) * int64(source.Header.Height)
	if area > engine.options.MaxWorkingBytes/4 {
		return nil, sceneError(psdfile.ErrLimit, "header", 0, "canvas exceeds the working budget")
	}
	if len(source.Layers) == 0 {
		if err := engine.reserve(area*4, "composite", source.Composite.Span.Offset); err != nil {
			return nil, err
		}
	}
	for _, record := range source.Layers {
		if err := engine.reserveRectangle(record.Bounds, record.Span.Offset); err != nil {
			return nil, err
		}
		if record.Mask != nil {
			if err := engine.reserveRectangle(record.Mask.Bounds, record.Mask.Span.Offset); err != nil {
				return nil, err
			}
			if record.Mask.HasReal {
				if err := engine.reserveRectangle(record.Mask.RealBounds, record.Mask.Span.Offset); err != nil {
					return nil, err
				}
			}
		}
	}
	var snapshot *psdmodel.Document
	if len(source.Layers) == 0 {
		snapshot, err = source.Decode(ctx)
	} else {
		snapshot, err = source.DecodeLayers(ctx)
	}
	if err != nil {
		return nil, err
	}
	if len(snapshot.Layers) == 0 {
		if snapshot.Composite == nil {
			return nil, sceneError(psdfile.ErrUnsupported, "composite", 0, "flat source has no decoded pixel image")
		}
		pixels := snapshot.Composite
		if destination != nil {
			profile, err := readProfile()
			if err != nil {
				return nil, err
			}
			pixels, err = engine.convertProfile(ctx, pixels, profile, destination)
			if err != nil {
				return nil, err
			}
		}
		engine.working = startWorking + int64(len(pixels.Pix))
		return pixels, nil
	}
	if snapshot.Composite != nil {
		engine.working -= int64(len(snapshot.Composite.Pix))
		snapshot.Composite = nil
	}
	physical := flatten(snapshot.Layers)
	if len(physical) != len(source.Layers) {
		return nil, sceneError(psdfile.ErrUnsupported, "layers", 0, "decoded tree differs from the active physical layer set")
	}
	remainingMetadata := engine.options.MaxWorkingBytes - engine.working
	if remainingMetadata <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.metadata", 0, "no metadata budget remains")
	}
	placementView, err := source.ReadSmartObjectView(ctx, psdfile.MetadataOptions{MaxBytes: remainingMetadata})
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(placementView.MetadataBytes, "smartObject.metadata", 0); err != nil {
		return nil, err
	}
	placements := placementView.Records
	remainingMetadata = engine.options.MaxWorkingBytes - engine.working
	if remainingMetadata <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.metadata", 0, "no metadata budget remains")
	}
	linkedView, err := source.ReadLinkedFileView(ctx, psdfile.MetadataOptions{MaxBytes: remainingMetadata})
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(linkedView.MetadataBytes, "smartObject.metadata", 0); err != nil {
		return nil, err
	}
	linked := linkedView.Records
	textByIndex, err := engine.textRecords(ctx, source)
	if err != nil {
		return nil, err
	}
	byIndex := make(map[int]psdfile.SmartObjectRecord)
	for _, placement := range placements {
		if placement.LayerSetIndex == source.ActiveLayerSet {
			byIndex[placement.LayerIndex] = placement
		}
	}
	byID := make(map[string]psdfile.LinkedFileRecord)
	ambiguous := make(map[string]bool)
	for _, record := range linked {
		if previous, exists := byID[record.ID]; exists && (previous.Kind != record.Kind || previous.Data != record.Data) {
			ambiguous[record.ID] = true
		}
		byID[record.ID] = record
	}
	invisible := hiddenLayers(snapshot.Layers)
	content := make(map[string]*image.NRGBA)
	patternSources := patternSceneSources{tiles: make(map[string]*image.NRGBA)}
	for index, layer := range physical {
		if layer == nil || invisible[layer] {
			continue
		}
		textRendered, err := engine.renderText(ctx, source, index, layer, textByIndex)
		if err != nil {
			return nil, err
		}
		if engine.options.CachedSpecialLayers {
			if err := selectCachedSpecial(ctx, source.Layers[index], layer, engine.options.ApproximateText != nil); err != nil {
				return nil, err
			}
		}
		if err := engine.renderPattern(ctx, source, layer, &patternSources); err != nil {
			return nil, err
		}
		if !engine.options.CachedSpecialLayers {
			for _, block := range source.Layers[index].AdditionalInfo {
				switch block.Key {
				case "GdFl":
					if layer.Gradient != nil {
						continue
					}
					return nil, sceneError(psdfile.ErrUnsupported, "layers.semantic", block.Data.Offset, "live gradient descriptor is unavailable")
				case "TySh":
					if textRendered {
						continue
					}
					return nil, sceneError(psdfile.ErrUnsupported, "layers.semantic", block.Data.Offset,
						"live text requires an explicit approximate rendering policy and font resolver")
				case "PtFl":
					if layer.Pattern != nil && layer.PatternTile != nil {
						continue
					}
					return nil, sceneError(psdfile.ErrUnsupported, "layers.semantic", block.Data.Offset,
						"live pattern descriptor or local tile is unavailable")
				case "tySh", "vscg", "vstk":
					return nil, sceneError(psdfile.ErrUnsupported, "layers.semantic", block.Data.Offset, "live text, gradient, pattern or stroke semantics are unavailable")
				}
			}
		}
		if layer.Placed == nil {
			continue
		}
		if psdrender.RequiresWarpRendering(layer.Placed) &&
			(layer.Placed.Warp == nil || layer.Placed.Warp.Style != "custom") {
			return nil, sceneError(psdfile.ErrUnsupported, "smartObject.warp", source.Layers[index].Span.Offset,
				"the active Warp preset or quilt surface requires a live implementation")
		}
		placement, exists := byIndex[index]
		if !exists || placement.SourceType != 2 {
			return nil, sceneError(psdfile.ErrUnsupported, "smartObject.contents", source.Layers[index].Span.Offset, "placed layer has no modern raster content reference")
		}
		record, exists := byID[placement.SourceID]
		if ambiguous[placement.SourceID] {
			return nil, sceneError(psdfile.ErrUnsupported, "smartObject.contents", placement.Block.Span.Offset, "duplicate content records are ambiguous")
		}
		if !exists {
			return nil, sceneError(psdfile.ErrUnsupported, "smartObject.contents", placement.Block.Span.Offset, "placed source record is missing")
		}
		prototype := content[placement.SourceID]
		if prototype == nil {
			parentProfile, err := readProfile()
			if err != nil {
				return nil, err
			}
			format, err := source.LinkedRasterFormat(ctx, record)
			if err != nil {
				return nil, err
			}
			if record.Kind == "liFD" && format != "" {
				prototype, err = engine.renderLinkedRaster(ctx, source, record, parentProfile, depth+1)
				if err != nil {
					return nil, err
				}
				content[placement.SourceID] = prototype
			} else {
				var child *psdfile.Document
				if record.Kind == "liFD" {
					limits := source.Limits()
					remaining := engine.options.MaxWorkingBytes - engine.working
					if remaining <= 0 {
						return nil, sceneError(psdfile.ErrLimit, "smartObject.index", record.Data.Offset,
							"no child indexing budget remains")
					}
					limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, remaining)
					child, err = source.OpenLinkedDocument(ctx, record, psdfile.LoadOptions{Limits: limits})
				} else {
					if engine.working >= engine.options.MaxWorkingBytes {
						return nil, sceneError(psdfile.ErrLimit, "smartObject.index", record.Data.Offset,
							"no external resolution budget remains")
					}
					child, err = source.ResolveLinkedDocument(ctx, record, engine.options.ExternalResolver)
				}
				if err != nil {
					return nil, err
				}
				if engine.active[child] {
					return nil, sceneError(psdfile.ErrMalformed, "smartObject.contents", record.Span.Offset, "resolver returned an ancestor source")
				}
				childIndexBytes := child.IndexMetadataBytes()
				if err := engine.reserve(childIndexBytes, "smartObject.index", record.Data.Offset); err != nil {
					_ = child.Close()
					return nil, err
				}
				prototype, err = engine.render(ctx, child, depth+1, parentProfile)
				closeErr := child.Close()
				engine.working -= childIndexBytes
				if err != nil {
					return nil, err
				}
				if closeErr != nil {
					return nil, closeErr
				}
				content[placement.SourceID] = prototype
			}
		}
		if prototype.Rect.Dx() != layer.Placed.Width || prototype.Rect.Dy() != layer.Placed.Height {
			return nil, sceneError(psdfile.ErrUnsupported, "smartObject.size", placement.Block.Data.Offset, "prototype dimensions differ from placement metadata")
		}
		filters, err := engine.prepareSmartFilters(ctx, placement, layer)
		if err != nil {
			return nil, err
		}
		var pixels *image.NRGBA
		var origin image.Point
		var projectErr error
		if layer.Placed.Warp != nil && layer.Placed.Warp.Style == "custom" {
			patch, err := psdwarp.Compile(ctx, layer.Placed)
			if err != nil {
				return nil, err
			}
			bounds, err := patch.Bounds(image.Rect(0, 0, snapshot.Width, snapshot.Height))
			if err != nil {
				return nil, err
			}
			if err := engine.reserveRectangle(bounds, placement.Block.Data.Offset); err != nil {
				return nil, err
			}
			pixels, origin, projectErr = psdrender.ProjectWarp(ctx, layer, prototype,
				image.Rect(0, 0, snapshot.Width, snapshot.Height),
				psdrender.WarpOptions{MaxBytes: engine.options.MaxWorkingBytes - engine.working +
					int64(bounds.Dx())*int64(bounds.Dy())*4})
		} else {
			plan, err := psdrender.CompileProjection(layer, snapshot.Width, snapshot.Height, false)
			if err != nil {
				return nil, err
			}
			if err := engine.reserve(int64(plan.Width)*int64(plan.Height)*4,
				"smartObject.projection", placement.Block.Data.Offset); err != nil {
				return nil, err
			}
			pixels, origin, projectErr = projectPlacedSource(ctx, layer, prototype, plan,
				engine.options.ApproximatePlacedProjection)
		}
		if projectErr != nil {
			return nil, sceneProjectionError(projectErr, "smartObject.projection", placement.Block.Data.Offset)
		}
		layer.Pixels = pixels
		layer.Bounds = image.Rectangle{Min: origin, Max: origin.Add(pixels.Rect.Size())}
		if err := engine.applySmartFilters(ctx, source, layer, filters,
			image.Rect(0, 0, snapshot.Width, snapshot.Height)); err != nil {
			return nil, err
		}
	}
	remaining := engine.options.MaxWorkingBytes - engine.working
	if remaining <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "composite", 0, "no working storage remains for composition")
	}
	composeOptions := engine.options.Compose
	if composeOptions.MaxWorkingBytes == 0 || composeOptions.MaxWorkingBytes > remaining {
		composeOptions.MaxWorkingBytes = remaining
	}
	result, err := psdcompose.Render(ctx, snapshot, composeOptions)
	if err != nil {
		return nil, err
	}
	if destination != nil {
		profile, err := readProfile()
		if err != nil {
			return nil, err
		}
		if err := engine.reserve(int64(len(result.Pix)), "color.sourcePixels", 0); err != nil {
			return nil, err
		}
		result, err = engine.convertProfile(ctx, result, profile, destination)
		if err != nil {
			return nil, err
		}
	}
	engine.working = startWorking + int64(len(result.Pix))
	return result, nil
}

func (engine *renderer) reserveRectangle(bounds image.Rectangle, offset int64) error {
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if width < 0 || height < 0 || width > math.MaxInt64/4 || height != 0 && width > math.MaxInt64/4/height {
		return sceneError(psdfile.ErrMalformed, "layers.bounds", offset, "invalid or overflowing image geometry")
	}
	return engine.reserve(width*height*4, "layers.pixels", offset)
}

func flatten(layers []*psdmodel.Layer) []*psdmodel.Layer {
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

func hiddenLayers(layers []*psdmodel.Layer) map[*psdmodel.Layer]bool {
	result := make(map[*psdmodel.Layer]bool)
	var visit func([]*psdmodel.Layer, bool)
	visit = func(nodes []*psdmodel.Layer, ancestor bool) {
		baseHidden := true
		for _, layer := range nodes {
			hidden := ancestor || layer.Hidden || layer.Opacity != nil && *layer.Opacity == 0
			if layer.Clipping {
				hidden = hidden || baseHidden
			} else {
				baseHidden = hidden
			}
			result[layer] = hidden
			visit(layer.Children, hidden)
		}
	}
	visit(layers, false)
	return result
}

func sceneError(kind error, path string, offset int64, detail string) error {
	return &psdfile.Error{Kind: kind, Feature: "scene", Path: path, Offset: offset, Detail: detail}
}

func sceneProjectionError(err error, path string, offset int64) error {
	if errors.Is(err, psdwarp.ErrUnsupported) {
		return &psdfile.Error{Kind: psdfile.ErrUnsupported, Feature: "scene", Path: path, Offset: offset,
			Detail: "original source warp geometry is outside the certified live projection model", Cause: err}
	}
	if errors.Is(err, psdwarp.ErrLimit) {
		return &psdfile.Error{Kind: psdfile.ErrLimit, Feature: "scene", Path: path, Offset: offset,
			Detail: "original source warp geometry or projection exceeds the working budget", Cause: err}
	}
	if !errors.Is(err, psdrender.ErrUnsupportedCrop) {
		return err
	}
	return &psdfile.Error{Kind: psdfile.ErrUnsupported, Feature: "scene", Path: path, Offset: offset,
		Detail: "transparent PSD/PSB source borders require a calibrated placement", Cause: err}
}
