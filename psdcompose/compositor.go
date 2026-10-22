package psdcompose

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type composePixel struct {
	color    [3]float64
	alpha    float64
	residual float64
}

type composePlan struct {
	document           *psdmodel.Document
	window             image.Rectangle
	hasKnockout        bool
	wholeColorAlpha    bool
	ordinaryGrayRanges bool
	hasCustomRanges    bool
	options            Options
	count              int
	depth              int
	seen               map[*psdmodel.Layer]bool
	unsupported        error
	feathers           map[*psdmodel.Mask]*featherMask
	shadowFeathers     map[*psdmodel.Mask]*featherMask
	vectors            map[*psdvector.Mask]*vectorCoverage
	gradients          map[*psdmodel.Layer]*gradientCache
	patterns           map[*psdmodel.Layer]*patternCache
	passRanges         map[*psdmodel.Layer]passRangeStage
	effects            map[*psdmodel.Layer]*effectCache
	adjustments        map[*psdmodel.Layer]*psdadjust.Transform
	adjustmentLayers   []*psdmodel.Layer
	workingBytes       int64
	layerSalts         map[*psdmodel.Layer]uint64
	renderContext      context.Context
	renderFailure      error
	pixelEvaluations   int64
}

// Validate checks a decoded tree's structure, visible capabilities and rendering budgets.
// Hidden content is still checked for cycles, image validity and node budgets.
func Validate(document *psdmodel.Document, options ...Options) error {
	_, err := preparePlan(document, options)
	return err
}

// ValidateRegion checks supported semantics and storage for an absolute document-coordinate output window.
// It borrows regional samples and does not read or allocate raster output.
func ValidateRegion(document *psdmodel.Document, rectangle image.Rectangle, options ...Options) error {
	_, err := preparePlanRegion(document, options, rectangle)
	return err
}

// Render rebuilds an owned straight-alpha image from bottom-to-top layer trees.
// It ignores Document.Composite and does not change the document or its pixel buffers.
// The document and all reachable metadata must remain read-only for the duration of the call.
func Render(
	ctx context.Context, document *psdmodel.Document, options ...Options,
) (*image.NRGBA, error) {
	if document == nil {
		return nil, composeError(ErrInvalid, "document", "document is nil")
	}
	return RenderRegion(ctx, document, image.Rect(0, 0, document.Width, document.Height), options...)
}

// RenderRegion rebuilds an absolute document-coordinate window without allocating the complete canvas.
// Layer geometry, procedural coordinates and deterministic dissolve noise keep their document origins.
// The returned image owns its samples and has Rect equal to rectangle.
func RenderRegion(
	ctx context.Context, document *psdmodel.Document, rectangle image.Rectangle, options ...Options,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan, err := preparePlanRegion(document, options, rectangle)
	if err != nil {
		return nil, err
	}
	plan.renderContext = ctx
	if err := plan.prepareVectors(ctx); err != nil {
		return nil, err
	}
	if err := plan.prepareFeathers(ctx); err != nil {
		return nil, err
	}
	if err := plan.prepareGradients(ctx); err != nil {
		return nil, err
	}
	if err := plan.preparePatterns(ctx); err != nil {
		return nil, err
	}
	if err := plan.prepareEffects(ctx); err != nil {
		return nil, err
	}
	result := image.NewNRGBA(rectangle)
	if err := plan.renderPixels(ctx, result); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func preparePlan(document *psdmodel.Document, options []Options) (*composePlan, error) {
	if document == nil {
		return nil, composeError(ErrInvalid, "document", "document is nil")
	}
	return preparePlanRegion(document, options, image.Rect(0, 0, document.Width, document.Height))
}

func preparePlanRegion(
	document *psdmodel.Document, options []Options, rectangle image.Rectangle,
) (*composePlan, error) {
	if document == nil {
		return nil, composeError(ErrInvalid, "document", "document is nil")
	}
	if len(options) > 1 {
		return nil, composeError(ErrInvalid, "options", "at most one option value is accepted")
	}
	configuration := Options{MaxWorkingBytes: 256 << 20, MaxDepth: 32, MaxLayers: 500, MaxFeatherRadius: 1000}
	configuration.MaxClippedGroupEvaluations = 4096
	if len(options) == 1 {
		provided := options[0]
		if provided.Workers < 0 || provided.Workers > 64 {
			return nil, composeError(ErrInvalid, "options.Workers", "worker count must be in 0..64")
		}
		configuration.Workers = provided.Workers
		configuration.ApproximateHueSaturation = provided.ApproximateHueSaturation
		configuration.ApproximateDropShadow = provided.ApproximateDropShadow
		configuration.ApproximatePlacedColorOverlay = provided.ApproximatePlacedColorOverlay
		configuration.ApproximateClippedGroupBackdrop = provided.ApproximateClippedGroupBackdrop
		if provided.MaxClippedGroupEvaluations < 0 {
			return nil, composeError(ErrInvalid, "options.MaxClippedGroupEvaluations", "limit must not be negative")
		}
		if provided.MaxClippedGroupEvaluations != 0 {
			configuration.MaxClippedGroupEvaluations = provided.MaxClippedGroupEvaluations
		}
		if provided.MaxWorkingBytes < 0 || provided.MaxDepth < 0 || provided.MaxLayers < 0 ||
			provided.MaxFeatherRadius < 0 || math.IsNaN(provided.MaxFeatherRadius) ||
			math.IsInf(provided.MaxFeatherRadius, 0) {
			return nil, composeError(ErrInvalid, "options", "limits must not be negative")
		}
		if provided.MaxWorkingBytes != 0 {
			configuration.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxDepth != 0 {
			configuration.MaxDepth = provided.MaxDepth
		}
		if provided.MaxLayers != 0 {
			configuration.MaxLayers = provided.MaxLayers
		}
		if provided.MaxFeatherRadius != 0 {
			configuration.MaxFeatherRadius = provided.MaxFeatherRadius
		}
		if provided.DissolveSeed != nil {
			seed := *provided.DissolveSeed
			configuration.DissolveSeed = &seed
		}
	}
	if configuration.MaxDepth > 256 {
		return nil, composeError(ErrLimit, "options.MaxDepth", "nesting limit may not exceed 256")
	}
	width, height := document.Width, document.Height
	if width <= 0 || height <= 0 {
		return nil, composeError(ErrInvalid, "document.size", "canvas dimensions must be positive")
	}
	if rectangle.Min.X < 0 || rectangle.Min.Y < 0 || rectangle.Max.X < rectangle.Min.X ||
		rectangle.Max.Y < rectangle.Min.Y || rectangle.Max.X > width || rectangle.Max.Y > height {
		return nil, composeError(ErrInvalid, "document.region", "window must lie inside the document canvas")
	}
	windowWidth, windowHeight := int64(rectangle.Dx()), int64(rectangle.Dy())
	intLimit := int64(^uint(0) >> 1)
	if windowHeight != 0 && (windowWidth > intLimit/4/windowHeight ||
		windowWidth > configuration.MaxWorkingBytes/4/windowHeight) {
		return nil, composeError(ErrLimit, "document.size", "output image exceeds the working budget")
	}
	plan := &composePlan{
		document: document, window: rectangle, options: configuration, seen: make(map[*psdmodel.Layer]bool),
		feathers:    make(map[*psdmodel.Mask]*featherMask),
		vectors:     make(map[*psdvector.Mask]*vectorCoverage),
		gradients:   make(map[*psdmodel.Layer]*gradientCache),
		patterns:    make(map[*psdmodel.Layer]*patternCache),
		passRanges:  make(map[*psdmodel.Layer]passRangeStage),
		effects:     make(map[*psdmodel.Layer]*effectCache),
		adjustments: make(map[*psdmodel.Layer]*psdadjust.Transform),
		layerSalts:  make(map[*psdmodel.Layer]uint64),
	}
	if len(document.BlockingIssues) != 0 {
		plan.unsupported = composeError(ErrUnsupported, "document", document.BlockingIssues[0])
	}
	if err := plan.checkLayers(document.Layers, 1, false, "document.layers"); err != nil {
		return nil, err
	}
	if plan.hasKnockout && passRangeVisibleKnockout(document.Layers, false) {
		if err := plan.restrictPassRangeKnockout(document.Layers, "document.layers"); err != nil {
			return nil, err
		}
	}
	plan.wholeColorAlpha = wholeColorAlphaDocument(document)
	plan.ordinaryGrayRanges = plan.ordinaryGrayRangeDocument()
	if err := passRangesClipBaseCheck(document.Layers); err != nil {
		return nil, err
	}
	if err := plan.checkEffectClipping(document.Layers, "document.layers"); err != nil {
		return nil, err
	}
	canvasBytes := windowWidth * windowHeight * 4
	// This reserves tree bookkeeping and bounded recursion in addition to the owned canvas.
	if int64(plan.count) > (configuration.MaxWorkingBytes-canvasBytes)/512 ||
		int64(plan.depth) > (configuration.MaxWorkingBytes-canvasBytes-int64(plan.count)*512)/512 {
		return nil, composeError(ErrLimit, "document.layers", "tree working storage exceeds the budget")
	}
	plan.workingBytes = canvasBytes + int64(plan.count)*512 + int64(plan.depth)*512
	if err := plan.prepareAdjustments(); err != nil {
		return nil, err
	}
	if err := plan.reserveVectorCaches(); err != nil {
		return nil, err
	}
	if err := plan.reserveGradientCaches(); err != nil {
		return nil, err
	}
	if err := plan.reservePatternCaches(); err != nil {
		return nil, err
	}
	if err := plan.reserveFeatherCaches(); err != nil {
		return nil, err
	}
	if err := plan.reserveEffectCaches(); err != nil {
		return nil, err
	}
	if err := plan.checkFeatherBudget(); err != nil {
		return nil, err
	}
	if plan.unsupported != nil {
		return nil, plan.unsupported
	}
	return plan, nil
}

func (plan *composePlan) checkLayers(
	layers []*psdmodel.Layer, depth int, hidden bool, path string,
) error {
	if len(layers) > plan.options.MaxLayers-plan.count {
		return composeError(ErrLimit, path, "layer count exceeds the configured limit")
	}
	if len(layers) != 0 && depth > plan.options.MaxDepth {
		return composeError(ErrLimit, path, "layer nesting exceeds the configured limit")
	}
	clippingHidden := true
	for index, layer := range layers {
		layerPath := fmt.Sprintf("%s[%d]", path, index)
		if layer == nil {
			return composeError(ErrInvalid, layerPath, "layer is nil")
		}
		if _, encountered := plan.seen[layer]; encountered {
			return composeError(ErrInvalid, layerPath, "layer tree contains a cycle or shared node")
		}
		canvasBytes := int64(plan.window.Dx()) * int64(plan.window.Dy()) * 4
		if int64(plan.count+1) > (plan.options.MaxWorkingBytes-canvasBytes)/512 {
			return composeError(ErrLimit, layerPath, "tree working storage exceeds the budget")
		}
		plan.seen[layer] = true
		plan.count++
		plan.layerSalts[layer] = uint64(plan.count)
		plan.depth = max(plan.depth, depth)
		if err := plan.checkAdvanced(layer, layerPath); err != nil {
			return err
		}
		if _, _, err := composeDimensions(layer.Bounds, layerPath+".bounds"); err != nil {
			return err
		}
		if err := composeImage(layer.Pixels, layer.Bounds, layerPath+".pixels"); err != nil {
			return err
		}
		if layer.PatternTile != nil {
			if _, _, err := composeDimensions(layer.PatternTile.Rect, layerPath+".pattern.tile"); err != nil {
				return err
			}
			if err := composeImage(layer.PatternTile, layer.PatternTile.Rect, layerPath+".pattern.tile"); err != nil {
				return err
			}
		}
		for _, opacity := range []*float64{layer.Opacity, layer.FillOpacity} {
			if opacity != nil && (math.IsNaN(*opacity) || math.IsInf(*opacity, 0) || *opacity < 0 || *opacity > 1) {
				return composeError(ErrInvalid, layerPath+".opacity", "opacity must be finite and between zero and one")
			}
		}
		if layer.FillColor != nil {
			for _, component := range *layer.FillColor {
				if math.IsNaN(component) || math.IsInf(component, 0) || component < 0 || component > 1 {
					return composeError(ErrInvalid, layerPath+".fillColor", "RGB components must be finite and between zero and one")
				}
			}
		}
		if layer.VectorFeather != nil && (math.IsNaN(*layer.VectorFeather) ||
			math.IsInf(*layer.VectorFeather, 0) || *layer.VectorFeather < 0) {
			return composeError(ErrInvalid, layerPath+".vectorFeather", "vector feather must be finite and nonnegative")
		}
		if err := composeRanges(layer.BlendRanges, layerPath+".blendRanges"); err != nil {
			return err
		}
		plan.hasCustomRanges = plan.hasCustomRanges || composeCustomRanges(layer.BlendRanges)
		if layer.Mask != nil {
			if _, _, err := composeDimensions(layer.Mask.Bounds, layerPath+".mask.bounds"); err != nil {
				return err
			}
			maskBounds := composeMaskSampleBounds(layer.Mask)
			if maskBounds.Max.X < maskBounds.Min.X || maskBounds.Max.Y < maskBounds.Min.Y ||
				!maskBounds.Empty() && !maskBounds.In(layer.Mask.Bounds) {
				return composeError(ErrInvalid, layerPath+".mask.sampleBounds", "samples must lie inside mask geometry")
			}
			if err := composeImage(layer.Mask.Pixels, maskBounds, layerPath+".mask.pixels"); err != nil {
				return err
			}
			if layer.Mask.SampleBounds != nil && !layer.Mask.Disabled {
				radius := 0
				if layer.Mask.Feather != nil && *layer.Mask.Feather > 0 {
					requested := *layer.Mask.Feather
					kernelBudget := min(plan.options.MaxWorkingBytes, int64(^uint(0)>>1)) / 64
					if requested > plan.options.MaxFeatherRadius || math.IsInf(requested, 0) ||
						featherSigma(requested) > float64(kernelBudget) {
						return composeError(ErrLimit, layerPath+".mask.feather", "feather exceeds neighborhood limit")
					}
					radius = int(math.Ceil(featherSigma(requested) * 3))
				}
				sampleWindow := plan.window
				if plan.dropShadowShapesWithMask(layer) {
					sampleWindow = sampleWindow.Union(layer.Bounds)
				}
				var required image.Rectangle
				if plan.dropShadowShapesWithMask(layer) {
					required = featherSupportBounds(sampleWindow, layer.Mask.Bounds, radius)
				} else {
					required = layer.Mask.Bounds.Intersect(sampleWindow.Inset(-radius))
				}
				if !required.Empty() && (layer.Mask.Pixels == nil || !required.In(maskBounds)) {
					return composeError(ErrInvalid, layerPath+".mask.sampleBounds", "samples omit required window halo")
				}
			}
			if layer.Mask.Feather != nil && (math.IsNaN(*layer.Mask.Feather) ||
				math.IsInf(*layer.Mask.Feather, 0) || *layer.Mask.Feather < 0) {
				return composeError(ErrInvalid, layerPath+".mask.feather", "mask feather must be finite and nonnegative")
			}
		}
		invisible := hidden || layer.Hidden || composeOpacity(layer.Opacity) == 0
		if layer.Clipping {
			invisible = invisible || clippingHidden
		} else {
			clippingHidden = invisible
		}
		if !invisible {
			if err := plan.registerEffects(layer, layerPath+".effects"); err != nil {
				if !errors.Is(err, ErrUnsupported) {
					return err
				}
				if plan.unsupported == nil {
					plan.unsupported = err
				}
			}
			if err := plan.registerGradient(layer, layerPath+".gradient"); err != nil {
				if !errors.Is(err, ErrUnsupported) {
					return err
				}
				if plan.unsupported == nil {
					plan.unsupported = err
				}
			}
			if layer.Adjustment != nil {
				plan.adjustmentLayers = append(plan.adjustmentLayers, layer)
			}
			if err := plan.registerPattern(layer, layerPath+".pattern"); err != nil {
				if !errors.Is(err, ErrUnsupported) {
					return err
				}
				if plan.unsupported == nil {
					plan.unsupported = err
				}
			}
			if err := plan.registerVector(layer, layerPath+".vectorMask"); err != nil && plan.unsupported == nil {
				plan.unsupported = err
			}
			if err := plan.registerFeather(layer.Mask, layerPath+".mask.feather"); err != nil {
				return err
			}
			if err := plan.capability(layer, layerPath); err != nil {
				if !errors.Is(err, ErrUnsupported) {
					return err
				}
				if plan.unsupported == nil {
					plan.unsupported = err
				}
			}
		}
		if err := plan.checkLayers(layer.Children, depth+1, invisible, layerPath+".children"); err != nil {
			return err
		}
		if err := plan.checkKnockoutShape(layer, layerPath); err != nil && !invisible && plan.unsupported == nil {
			plan.unsupported = err
		}
	}
	return nil
}

func composeDimensions(bounds image.Rectangle, path string) (int, int, error) {
	if bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y {
		return 0, 0, composeError(ErrInvalid, path, "rectangle has reversed edges")
	}
	width := uint64(bounds.Max.X) - uint64(bounds.Min.X)
	height := uint64(bounds.Max.Y) - uint64(bounds.Min.Y)
	if width > uint64(^uint(0)>>1) || height > uint64(^uint(0)>>1) {
		return 0, 0, composeError(ErrInvalid, path, "rectangle dimensions overflow an integer")
	}
	return int(width), int(height), nil
}

func composeImage(pixels *image.NRGBA, bounds image.Rectangle, path string) error {
	if pixels == nil {
		return nil
	}
	width, height, err := composeDimensions(pixels.Rect, path+".rect")
	if err != nil {
		return err
	}
	if width != bounds.Dx() || height != bounds.Dy() {
		return composeError(ErrInvalid, path, "pixel dimensions differ from absolute layer bounds")
	}
	if width == 0 || height == 0 {
		return nil
	}
	if width > int(^uint(0)>>1)/4 || pixels.Stride < width*4 || len(pixels.Pix) < width*4 ||
		height-1 > (len(pixels.Pix)-width*4)/pixels.Stride {
		return composeError(ErrInvalid, path, "pixel stride or backing buffer is invalid")
	}
	return nil
}

func composeRanges(ranges []psdmodel.BlendRange, path string) error {
	for _, blendingRange := range ranges {
		for _, slider := range []psdmodel.BlendSlider{blendingRange.Source, blendingRange.Destination} {
			if slider.BlackLow > slider.BlackHigh || slider.WhiteLow > slider.WhiteHigh {
				return composeError(ErrInvalid, path, "Blend If slider endpoints are not ordered")
			}
		}
	}
	return nil
}

func (plan *composePlan) capability(layer *psdmodel.Layer, path string) error {
	if err := advancedCapability(layer, path); err != nil {
		return err
	}
	if len(layer.Unsupported) != 0 {
		return composeError(ErrUnsupported, path, layer.Unsupported[0])
	}
	if layer.Placed != nil && len(layer.Placed.Unsupported) != 0 {
		return composeError(ErrUnsupported, path+".placed", layer.Placed.Unsupported[0])
	}
	if len(layer.BlendRanges) > 4 && composeCustomRanges(layer.BlendRanges[4:]) {
		return composeError(ErrUnsupported, path+".blendRanges", "only composite gray and RGB ranges are supported")
	}
	group := layer.Group || layer.Children != nil
	if layer.BlendMode == "pass" && !group {
		return composeError(ErrInvalid, path+".blendMode", "pass-through is only valid for a group")
	}
	dissolve := dissolveMode(layer.BlendMode) && plan.options.DissolveSeed != nil
	if !SupportedBlend(layer.BlendMode) && (!group || layer.BlendMode != "pass") && !dissolve {
		return composeError(ErrUnsupported, path+".blendMode", "unsupported blend mode "+layer.BlendMode)
	}
	if group && layer.BlendMode == "pass" && composeCustomRanges(layer.BlendRanges) {
		stage := passGroupRangeStage(layer, len(plan.document.Warnings) == 0)
		if stage == passRangeNone {
			return composeError(ErrUnsupported, path+".blendRanges",
				"pass-through group Blend If requires calibrated flat Normal or single raster gray sources")
		}
		plan.passRanges[layer] = stage
	}
	if !group && layer.Pixels == nil && layer.FillColor == nil && layer.Adjustment == nil &&
		layer.Gradient == nil && layer.Pattern == nil && !layer.Bounds.Empty() {
		return composeError(ErrUnsupported, path+".pixels", "visible layer has no saved raster pixels")
	}
	if layer.Mask != nil && !layer.Mask.Disabled {
		if len(layer.Mask.Unsupported) != 0 {
			return composeError(ErrUnsupported, path+".mask", layer.Mask.Unsupported[0])
		}
	}
	return nil
}

func composeCustomRanges(ranges []psdmodel.BlendRange) bool {
	for _, blendingRange := range ranges {
		for _, slider := range []psdmodel.BlendSlider{blendingRange.Source, blendingRange.Destination} {
			if slider.BlackLow != 0 || slider.BlackHigh != 0 || slider.WhiteLow != 255 || slider.WhiteHigh != 255 {
				return true
			}
		}
	}
	return false
}

func (plan *composePlan) stack(layers []*psdmodel.Layer, backdrop composePixel, x, y int) composePixel {
	if !plan.renderLive() {
		return composePixel{}
	}
	if plan.hasKnockout {
		target := plan.knockoutBackdrop(x, y)
		return plan.knockoutStack(layers, backdrop, knockoutScope{shallow: target, deep: target}, x, y)
	}
	for index := 0; index < len(layers); {
		if !plan.renderLive() {
			return composePixel{}
		}
		base := layers[index]
		end := index + 1
		for end < len(layers) && layers[end].Clipping {
			if !plan.renderLive() {
				return composePixel{}
			}
			end++
		}
		if !base.Clipping && !base.Hidden && composeOpacity(base.Opacity) != 0 {
			if end == index+1 && (base.Group || base.Children != nil) &&
				plan.maskedGroupEmpty(base, backdrop, x, y) {
				index = end
				continue
			}
			if base.Adjustment != nil {
				backdrop = plan.adjust(base, backdrop, x, y, 1)
				index++
				continue
			}
			if end > index+1 && (base.BlendClipped == nil || *base.BlendClipped) {
				source := plan.clippingSource(base, backdrop, x, y)
				if source.alpha != 0 {
					chain := source
					// The base transparency clips the whole chain after its colors are blended.
					chain.alpha = 1
					for _, clipped := range layers[index+1 : end] {
						if !plan.renderLive() {
							return composePixel{}
						}
						if !clipped.Hidden {
							chain = plan.apply(clipped, chain, x, y, 1)
						}
					}
					source.color = chain.color
					mode := base.BlendMode
					if mode == "pass" {
						mode = "norm"
					}
					backdrop = plan.paintWithMode(base, mode, source, backdrop, x, y, 1)
				}
			} else if end > index+1 {
				source := plan.clippingSource(base, backdrop, x, y)
				if source.alpha != 0 {
					blended := source.color
					if !dissolveMode(base.BlendMode) {
						mode := base.BlendMode
						if mode == "pass" {
							mode = "norm"
						}
						blended, _ = advancedBlendRGB(base, mode, backdrop.color, source.color, composeOpacity(base.FillOpacity))
					}
					chain := composePixel{alpha: 1}
					for component := range 3 {
						chain.color[component] = (1-backdrop.alpha)*source.color[component] +
							backdrop.alpha*blended[component]
					}
					for _, clipped := range layers[index+1 : end] {
						if !plan.renderLive() {
							return composePixel{}
						}
						if !clipped.Hidden {
							chain = plan.apply(clipped, chain, x, y, 1)
						}
					}
					source.color = chain.color
					mode := "norm"
					if dissolveMode(base.BlendMode) {
						mode = "diss"
					}
					backdrop = plan.paintWithMode(base, mode, source, backdrop, x, y, 1)
				}
			} else {
				source := plan.source(base, backdrop, x, y)
				backdrop = plan.paint(base, source, backdrop, x, y, 1)
			}
		}
		index = end
	}
	return backdrop
}

func (plan *composePlan) source(layer *psdmodel.Layer, backdrop composePixel, x, y int) composePixel {
	if !plan.enterLayerSource() {
		return composePixel{}
	}
	if layer.Group || layer.Children != nil {
		if stage := plan.passRanges[layer]; stage >= passRangeSingleNormal {
			return plan.singlePassRangeSource(layer, stage, backdrop, x, y)
		}
		if layer.BlendMode != "pass" || plan.passRanges[layer] != passRangeNone {
			backdrop = composePixel{}
		}
		return plan.stack(layer.Children, backdrop, x, y)
	}
	if layer.FillColor != nil {
		return composePixel{color: *layer.FillColor, alpha: 1}
	}
	if layer.Gradient != nil {
		return plan.gradientSource(layer, x, y)
	}
	if layer.Pattern != nil {
		return plan.patternSource(layer, x, y)
	}
	if layer.Pixels == nil || !image.Pt(x, y).In(layer.Bounds) {
		return composePixel{}
	}
	offset := (y-layer.Bounds.Min.Y)*layer.Pixels.Stride + (x-layer.Bounds.Min.X)*4
	return composePixel{
		color: [3]float64{
			float64(layer.Pixels.Pix[offset]) / 255,
			float64(layer.Pixels.Pix[offset+1]) / 255,
			float64(layer.Pixels.Pix[offset+2]) / 255,
		},
		alpha: float64(layer.Pixels.Pix[offset+3]) / 255,
	}
}

func (plan *composePlan) apply(
	layer *psdmodel.Layer, backdrop composePixel, x, y int, clip float64,
) composePixel {
	if layer.Hidden || composeOpacity(layer.Opacity) == 0 || clip == 0 {
		return backdrop
	}
	if layer.Adjustment != nil {
		return plan.adjust(layer, backdrop, x, y, clip)
	}
	source := plan.source(layer, backdrop, x, y)
	return plan.paint(layer, source, backdrop, x, y, clip)
}

func (plan *composePlan) paint(
	layer *psdmodel.Layer, source, backdrop composePixel, x, y int, clip float64,
) composePixel {
	return plan.paintWithMode(layer, layer.BlendMode, source, backdrop, x, y, clip)
}

func (plan *composePlan) paintWithMode(
	layer *psdmodel.Layer, mode string, source, backdrop composePixel, x, y int, clip float64,
) composePixel {
	stage := plan.passRanges[layer]
	if mode == "pass" && stage != passRangeNone {
		mode = "norm"
	}
	if plan.effects[layer] != nil {
		return plan.paintEffects(layer, mode, source, backdrop, x, y, clip)
	}
	if mode == "pass" {
		amount := clip * composeOpacity(layer.Opacity) * composeOpacity(layer.FillOpacity) *
			plan.mask(layer.Mask, x, y) * plan.vectorAlpha(layer, x, y)
		return restrictChannels(layer, composeInterpolate(backdrop, source, amount), backdrop)
	}
	coverage, fill := advancedCoverageFill(layer, source.alpha, clip,
		plan.mask(layer.Mask, x, y)*plan.vectorAlpha(layer, x, y))
	// Fractional backdrops retain the original floating-point evaluation of identity gates.
	rangeActive := plan.hasCustomRanges || backdrop.alpha != 0 && backdrop.alpha != 1
	if rangeActive {
		if stage >= passRangeSingleNormal {
			coverage *= singlePassGrayRanges(layer.BlendRanges[0], source.color, backdrop.color, backdrop.alpha)
		} else if plan.ordinaryGrayRanges && composeCustomRanges(layer.BlendRanges) {
			sourceGate, destinationGate := ordinaryGrayRangeGates(
				layer.BlendRanges[0], source.color, backdrop.color, backdrop.alpha,
			)
			coverage *= destinationGate
			fill *= sourceGate
		} else {
			coverage *= composeBlendIf(layer.BlendRanges, source.color, backdrop.color, backdrop.alpha)
		}
	}
	if coverage == 0 {
		return backdrop
	}
	if !rangeActive && coverage == 1 && fill == 1 && (mode == "normal" || mode == "norm" || mode == "") {
		return restrictChannels(layer, composePixel{color: source.color, alpha: 1}, backdrop)
	}
	paintAlpha := coverage * fill
	if plan.wholeColorAlpha && clip == 1 &&
		(normalizeBlend(mode) == "dkCl" || normalizeBlend(mode) == "lgCl") {
		controlByte := math.Round(composeOpacity(layer.Opacity) * composeOpacity(layer.FillOpacity) * 255)
		paintAlpha = math.Round(source.alpha*controlByte) / 255
	}
	if dissolveMode(mode) {
		if dissolvePainted(*plan.options.DissolveSeed, plan.layerSalts[layer], x, y, paintAlpha) {
			return composePixel{color: source.color, alpha: 1}
		}
		return backdrop
	}
	outputAlpha := paintAlpha + backdrop.alpha*(1-paintAlpha)
	if outputAlpha == 0 {
		return composePixel{}
	}
	blended, _ := advancedBlendRGB(layer, mode, backdrop.color, source.color, fill)
	var result composePixel
	result.alpha = outputAlpha
	result.residual = backdrop.residual * (1 - paintAlpha)
	for component := range 3 {
		premultiplied := backdrop.alpha * backdrop.color[component] * (1 - paintAlpha)
		if blendUsesSpecialFill(mode) {
			premultiplied += paintAlpha * (1 - backdrop.alpha) * source.color[component]
			premultiplied += coverage * backdrop.alpha * (blended[component] - backdrop.color[component])
			premultiplied += paintAlpha * backdrop.alpha * backdrop.color[component]
		} else {
			premultiplied += paintAlpha * ((1-backdrop.alpha)*source.color[component] + backdrop.alpha*blended[component])
		}
		result.color[component] = composeClamp(premultiplied / outputAlpha)
	}
	return restrictChannels(layer, result, backdrop)
}

func composeInterpolate(backdrop, source composePixel, amount float64) composePixel {
	alpha := backdrop.alpha*(1-amount) + source.alpha*amount
	if alpha == 0 {
		return composePixel{}
	}
	result := composePixel{alpha: alpha, residual: backdrop.residual*(1-amount) + source.residual*amount}
	for component := range 3 {
		result.color[component] = composeClamp((backdrop.color[component]*backdrop.alpha*(1-amount) +
			source.color[component]*source.alpha*amount) / alpha)
	}
	return result
}

func composeMask(mask *psdmodel.Mask, x, y int) float64 {
	if mask == nil || mask.Disabled {
		return 1
	}
	value := float64(1)
	if mask.DefaultColor != nil {
		value = float64(*mask.DefaultColor) / 255
	}
	if bounds := composeMaskSampleBounds(mask); mask.Pixels != nil && image.Pt(x, y).In(bounds) {
		offset := (y-bounds.Min.Y)*mask.Pixels.Stride + (x-bounds.Min.X)*4
		value = float64(mask.Pixels.Pix[offset]) / 255
	}
	if mask.Invert {
		value = 1 - value
	}
	if mask.Density != nil {
		value = 1 - (1-value)*float64(*mask.Density)/255
	}
	return value
}

func composeMaskSampleBounds(mask *psdmodel.Mask) image.Rectangle {
	if mask.SampleBounds != nil {
		return *mask.SampleBounds
	}
	return mask.Bounds
}

func composeBlendIf(ranges []psdmodel.BlendRange, source, backdrop [3]float64, backdropAlpha float64) float64 {
	sourceCoverage, destinationCoverage := float64(1), float64(1)
	for index, blendingRange := range ranges[:min(len(ranges), 4)] {
		sourceValue, backdropValue := float64(0), float64(0)
		if index == 0 {
			sourceValue = .299*source[0] + .587*source[1] + .114*source[2]
			backdropValue = .299*backdrop[0] + .587*backdrop[1] + .114*backdrop[2]
		} else {
			sourceValue, backdropValue = source[index-1], backdrop[index-1]
		}
		sourceCoverage *= composeSlider(blendingRange.Source, sourceValue)
		destinationCoverage *= 1 - backdropAlpha + backdropAlpha*composeSlider(blendingRange.Destination, backdropValue)
	}
	return sourceCoverage * destinationCoverage
}

func composeSlider(slider psdmodel.BlendSlider, value float64) float64 {
	value *= 255
	black := float64(1)
	if slider.BlackLow != 0 || slider.BlackHigh != 0 {
		if value < float64(slider.BlackLow) {
			black = 0
		} else if slider.BlackHigh > slider.BlackLow && value < float64(slider.BlackHigh) {
			black = (value - float64(slider.BlackLow) + 1) / float64(int(slider.BlackHigh)-int(slider.BlackLow)+1)
		}
	}
	white := float64(1)
	if slider.WhiteLow != 255 || slider.WhiteHigh != 255 {
		if value > float64(slider.WhiteHigh) {
			white = 0
		} else if slider.WhiteHigh > slider.WhiteLow && value > float64(slider.WhiteLow) {
			white = (float64(slider.WhiteHigh) - value + 1) / float64(int(slider.WhiteHigh)-int(slider.WhiteLow)+1)
		}
	}
	return min(black, white)
}

func composeOpacity(value *float64) float64 {
	if value == nil {
		return 1
	}
	return *value
}

func composeClamp(value float64) float64 { return max(0, min(1, value)) }

func composeByte(value float64) byte { return byte(math.Round(composeClamp(value) * 255)) }
