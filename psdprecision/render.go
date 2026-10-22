package psdprecision

import (
	"context"
	"fmt"
	"image"
	"math"
)

type precisionPlan struct {
	scene   *Scene
	options Options
	area    int
	groups  int
	layers  int
	active  map[*Layer]bool
	buffers [][]float64
}

// Render applies ordinary source-over in the scene's declared color domain and returns owned straight samples.
// Color values remain finite HDR values; only alpha and mask coverage are restricted to [0,1].
func Render(ctx context.Context, scene *Scene, options ...Options) (*FloatImage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	settings, err := renderOptions(options)
	if err != nil {
		return nil, err
	}
	if scene == nil || scene.Encoding > LinearRGB {
		return nil, fmt.Errorf("%w: missing scene or invalid color encoding", ErrInvalid)
	}
	area, err := checkedArea(scene.Width, scene.Height)
	if err != nil {
		return nil, err
	}
	if int64(area) > settings.MaxPixels {
		return nil, fmt.Errorf("%w: canvas exceeds MaxPixels", ErrLimit)
	}
	plan := precisionPlan{scene: scene, options: settings, area: area, active: make(map[*Layer]bool)}
	if err := plan.validate(ctx, scene.Layers, 1, 0, true); err != nil {
		return nil, err
	}
	if int64(area) > settings.MaxWorkingBytes/32/int64(plan.groups+1) {
		return nil, fmt.Errorf("%w: output and isolated group buffers exceed MaxWorkingBytes", ErrLimit)
	}
	result := &FloatImage{Width: scene.Width, Height: scene.Height, Encoding: scene.Encoding,
		Pix: make([]float64, area*4)}
	plan.buffers = make([][]float64, plan.groups)
	if err := plan.paint(ctx, scene.Layers, result.Pix, 0); err != nil {
		return nil, err
	}
	for pixel := range area {
		if pixel&4095 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		offset := pixel * 4
		alpha := result.Pix[offset+3]
		if alpha == 0 {
			result.Pix[offset], result.Pix[offset+1], result.Pix[offset+2] = 0, 0, 0
			continue
		}
		for component := range 3 {
			result.Pix[offset+component] /= alpha
		}
	}
	return result, ctx.Err()
}

func renderOptions(options []Options) (Options, error) {
	settings := Options{MaxWorkingBytes: 256 << 20, MaxPixels: 20_000_000, MaxLayers: 10_000, MaxDepth: 64}
	if len(options) > 1 {
		return Options{}, fmt.Errorf("%w: provide at most one Options", ErrInvalid)
	}
	if len(options) == 0 {
		return settings, nil
	}
	provided := options[0]
	if provided.MaxWorkingBytes < 0 || provided.MaxPixels < 0 || provided.MaxLayers < 0 || provided.MaxDepth < 0 {
		return Options{}, fmt.Errorf("%w: limits must be nonnegative", ErrInvalid)
	}
	if provided.MaxWorkingBytes != 0 {
		settings.MaxWorkingBytes = provided.MaxWorkingBytes
	}
	if provided.MaxPixels != 0 {
		settings.MaxPixels = provided.MaxPixels
	}
	if provided.MaxLayers != 0 {
		settings.MaxLayers = provided.MaxLayers
	}
	if provided.MaxDepth != 0 {
		settings.MaxDepth = provided.MaxDepth
	}
	if settings.MaxDepth > 64 || provided.NonFinite > NonFiniteClamp {
		return Options{}, fmt.Errorf("%w: invalid nesting limit or non-finite policy", ErrInvalid)
	}
	settings.NonFinite = provided.NonFinite
	return settings, nil
}

func checkedArea(width, height int) (int, error) {
	if width < 0 || height < 0 {
		return 0, fmt.Errorf("%w: negative image dimensions", ErrInvalid)
	}
	intLimit := int(^uint(0) >> 1)
	if height != 0 && width > intLimit/4/height {
		return 0, fmt.Errorf("%w: sample count exceeds platform integer range", ErrLimit)
	}
	return width * height, nil
}

func (plan *precisionPlan) validate(
	ctx context.Context, layers []*Layer, depth, groups int, ancestorVisible bool,
) error {
	if depth > plan.options.MaxDepth && len(layers) != 0 {
		return fmt.Errorf("%w: tree exceeds MaxDepth", ErrLimit)
	}
	for index, layer := range layers {
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer == nil || plan.active[layer] {
			return fmt.Errorf("%w: nil layer or cyclic tree", ErrInvalid)
		}
		plan.layers++
		if plan.layers > plan.options.MaxLayers {
			return fmt.Errorf("%w: tree exceeds MaxLayers", ErrLimit)
		}
		opacity := layerOpacity(layer)
		if !validCoverage(opacity) {
			return fmt.Errorf("%w: invalid layer opacity", ErrInvalid)
		}
		visible := ancestorVisible && !layer.Hidden && opacity != 0
		if visible && len(layer.Unsupported) != 0 {
			return fmt.Errorf("%w: %s", ErrUnsupported, layer.Unsupported[0])
		}
		if visible && !SupportsBlend(layer.BlendMode, plan.scene.Encoding) &&
			(layer.Children == nil || normalizedBlend(layer.BlendMode) != "pass") {
			return fmt.Errorf("%w: blend mode %q", ErrUnsupported, layer.BlendMode)
		}
		if visible && layer.Mask != nil && !layer.Mask.Disabled {
			if err := validateMask(layer.Mask); err != nil {
				return err
			}
		}
		if visible {
			if err := validateRanges(layer.BlendRanges); err != nil {
				return err
			}
			if layer.Children != nil && (layer.Clipping || normalizedBlend(layer.BlendMode) == "pass" &&
				len(layer.BlendRanges) != 0) {
				return fmt.Errorf("%w: clipped or Blend If pass-through group", ErrUnsupported)
			}
			if layer.Children != nil && index+1 < len(layers) && layers[index+1] != nil && layers[index+1].Clipping {
				return fmt.Errorf("%w: clipping base group", ErrUnsupported)
			}
		}
		if layer.Children != nil {
			if layer.Pixels != nil {
				return fmt.Errorf("%w: group also contains raster samples", ErrInvalid)
			}
			nextGroups := groups
			if visible && groupNeedsBuffer(layer) {
				nextGroups++
				plan.groups = max(plan.groups, nextGroups)
			}
			plan.active[layer] = true
			if err := plan.validate(ctx, layer.Children, depth+1, nextGroups, visible); err != nil {
				return err
			}
			delete(plan.active, layer)
			continue
		}
		if !visible || layer.Pixels == nil {
			continue
		}
		if layer.Bounds.Max.X < layer.Bounds.Min.X || layer.Bounds.Max.Y < layer.Bounds.Min.Y {
			return fmt.Errorf("%w: negative layer bounds", ErrInvalid)
		}
		pixels := layer.Pixels
		area, err := checkedArea(pixels.Width, pixels.Height)
		if err != nil {
			return err
		}
		if pixels.Encoding != plan.scene.Encoding || pixels.Width != layer.Bounds.Dx() ||
			pixels.Height != layer.Bounds.Dy() || len(pixels.Pix) != area*4 {
			return fmt.Errorf("%w: raster shape, samples or encoding differ from layer", ErrInvalid)
		}
	}
	return nil
}

func validateMask(mask *Mask) error {
	if mask.Bounds.Max.X < mask.Bounds.Min.X || mask.Bounds.Max.Y < mask.Bounds.Min.Y {
		return fmt.Errorf("%w: negative mask bounds", ErrInvalid)
	}
	area, err := checkedArea(mask.Bounds.Dx(), mask.Bounds.Dy())
	if err != nil {
		return err
	}
	if len(mask.Values) != area || !validCoverage(mask.Default) || mask.Density != nil && !validCoverage(*mask.Density) {
		return fmt.Errorf("%w: invalid mask shape, outside value or density", ErrInvalid)
	}
	return nil
}

func layerOpacity(layer *Layer) float64 {
	if layer.Opacity == nil {
		return 1
	}
	return *layer.Opacity
}

func validCoverage(value float64) bool {
	return !math.IsNaN(value) && value >= 0 && value <= 1
}

func (plan *precisionPlan) sample(value float64, coverage bool) (float64, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		switch plan.options.NonFinite {
		case NonFiniteReject:
			return 0, ErrNonFinite
		case NonFiniteZero:
			value = 0
		case NonFiniteClamp:
			if math.IsInf(value, 1) {
				value = 1
			} else {
				value = 0
			}
		}
	}
	if coverage && !validCoverage(value) {
		return 0, fmt.Errorf("%w: alpha or mask sample is outside [0,1]", ErrInvalid)
	}
	return value, nil
}

func (plan *precisionPlan) maskCoverage(mask *Mask, x, y int) (float64, error) {
	if mask == nil || mask.Disabled {
		return 1, nil
	}
	value := mask.Default
	if image.Pt(x, y).In(mask.Bounds) {
		value = mask.Values[(y-mask.Bounds.Min.Y)*mask.Bounds.Dx()+x-mask.Bounds.Min.X]
	}
	value, err := plan.sample(value, true)
	if err != nil {
		return 0, err
	}
	if mask.Invert {
		value = 1 - value
	}
	if mask.Density != nil {
		density := *mask.Density
		value = 1 - (1-value)*density
	}
	return value, nil
}

func (plan *precisionPlan) paint(ctx context.Context, layers []*Layer, target []float64, groupLevel int) error {
	canvas := image.Rect(0, 0, plan.scene.Width, plan.scene.Height)
	for index := 0; index < len(layers); {
		start := index
		layer := layers[index]
		end := index + 1
		for end < len(layers) && layers[end].Clipping {
			end++
		}
		index = end
		if err := ctx.Err(); err != nil {
			return err
		}
		if layer.Clipping || layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		if end > start+1 {
			if layer.Children != nil {
				return fmt.Errorf("%w: clipping base group", ErrUnsupported)
			}
			bounds := layer.Bounds.Intersect(canvas)
			for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				for x := bounds.Min.X; x < bounds.Max.X; x++ {
					if x&4095 == 0 {
						if err := ctx.Err(); err != nil {
							return err
						}
					}
					offset := (y*plan.scene.Width + x) * 4
					if err := plan.clippingPixel(layer, layers[start+1:end], target, offset, x, y); err != nil {
						return err
					}
				}
			}
			continue
		}
		if layer.Children != nil {
			if !groupNeedsBuffer(layer) {
				if err := plan.paint(ctx, layer.Children, target, groupLevel); err != nil {
					return err
				}
				continue
			}
			if plan.buffers[groupLevel] == nil {
				plan.buffers[groupLevel] = make([]float64, plan.area*4)
			}
			group := plan.buffers[groupLevel]
			pass := normalizedBlend(layer.BlendMode) == "pass"
			if pass {
				copy(group, target)
			} else {
				clear(group)
			}
			if err := plan.paint(ctx, layer.Children, group, groupLevel+1); err != nil {
				return err
			}
			for y := 0; y < plan.scene.Height; y++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				for x := 0; x < plan.scene.Width; x++ {
					if x&4095 == 0 {
						if err := ctx.Err(); err != nil {
							return err
						}
					}
					coverage, err := plan.maskCoverage(layer.Mask, x, y)
					if err != nil {
						return err
					}
					coverage *= layerOpacity(layer)
					offset := (y*plan.scene.Width + x) * 4
					if pass {
						for component := range 4 {
							target[offset+component] = target[offset+component]*(1-coverage) + group[offset+component]*coverage
						}
						continue
					}
					var source [3]float64
					alpha := group[offset+3]
					if alpha != 0 {
						for component := range source {
							source[component] = group[offset+component] / alpha
						}
					}
					backdrop, backdropAlpha := straightBackdrop(target, offset)
					coverage *= rangeCoverage(layer.BlendRanges, source, backdrop, backdropAlpha)
					if err := plan.blendPremultiplied(target, offset, source, alpha*coverage, layer.BlendMode); err != nil {
						return err
					}
				}
			}
			continue
		}
		if layer.Pixels == nil {
			continue
		}
		bounds := layer.Bounds.Intersect(canvas)
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				if (x-bounds.Min.X)&4095 == 0 {
					if err := ctx.Err(); err != nil {
						return err
					}
				}
				color, alpha, err := plan.sourcePixel(layer, x, y)
				if err != nil {
					return err
				}
				offset := (y*plan.scene.Width + x) * 4
				if err := plan.applyPixel(layer, color, alpha, target, offset, x, y, layer.BlendMode); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
