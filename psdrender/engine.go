package psdrender

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/gopkg-dev/psdkit/psdcompose"
)

// ErrUnsupported identifies features outside supported RGB8 replacement rendering.
var ErrUnsupported = errors.New("PSD contains unsupported rendering features")

// Engine owns one immutable document and bounded geometry/static-prefix caches.
// Concurrent callers serialize; use separate engines sharing one immutable Document for parallel groups.
type Engine struct {
	document    *Document
	metadata    Metadata
	projections map[*Layer]*ProjectionPlan
	prefix      *image.NRGBA
	prefixEnd   int
	tree        bool
	mu          sync.Mutex
}

func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

// Inspect collects capability metadata without creating render caches.
func Inspect(document *Document) (Metadata, error) {
	if document == nil {
		return Metadata{}, errors.New("missing PSD document")
	}
	if err := AssertImageSize(document.Width, document.Height); err != nil {
		return Metadata{}, err
	}
	compositionError := psdcompose.Validate(document)
	if compositionError != nil && !errors.Is(compositionError, psdcompose.ErrUnsupported) {
		return Metadata{}, compositionError
	}
	metadata := Metadata{Width: document.Width, Height: document.Height, ObjectNamesVersion: 1,
		Objects: []Object{}, Slices: []Slice{}, BackgroundLayers: []BackgroundLayer{},
		Warnings:       append([]string{"预览来自 PSD 保存合成图；替换结果由独立引擎重绘，双线性采样和色彩管理可能与 Photoshop 存在差异。"}, document.Warnings...),
		BlockingIssues: append([]string{}, document.BlockingIssues...)}
	if compositionError != nil {
		metadata.BlockingIssues = append(metadata.BlockingIssues, compositionError.Error())
	}
	if document.Composite != nil {
		if err := validateImage(document.Composite); err != nil {
			return Metadata{}, err
		}
	}
	objects := make(map[string]int)
	count := 0
	decodedPixels := int64(document.Width) * int64(document.Height)
	resident := int64(0)
	seenPixels := make(map[*image.NRGBA]bool)
	addPixels := func(pixels *image.NRGBA) {
		if pixels != nil && !seenPixels[pixels] {
			resident += int64(len(pixels.Pix))
			seenPixels[pixels] = true
		}
	}
	addPixels(document.Composite)
	var visit func([]*Layer, bool) error
	visit = func(layers []*Layer, hidden bool) error {
		baseInvisible := true
		for _, layer := range layers {
			count++
			if layer == nil {
				return errors.New("PSD contains an empty layer record")
			}
			if count > MaxLayers {
				return errors.New("PSD 图层超过 500 层限制")
			}
			for _, pixels := range []*image.NRGBA{layer.Pixels} {
				if pixels != nil {
					if err := validateImage(pixels); err != nil {
						return err
					}
					decodedPixels += int64(pixels.Rect.Dx()) * int64(pixels.Rect.Dy())
					addPixels(pixels)
				}
			}
			if layer.Mask != nil && layer.Mask.Pixels != nil {
				if err := validateImage(layer.Mask.Pixels); err != nil {
					return err
				}
				decodedPixels += int64(layer.Mask.Pixels.Rect.Dx()) * int64(layer.Mask.Pixels.Rect.Dy())
				addPixels(layer.Mask.Pixels)
			}
			if decodedPixels > MaxDecodedPixels {
				return errors.New("PSD 图层和蒙版总像素超过 6000 万")
			}
			invisible := hidden || layer.Hidden || layerOpacity(layer) == 0
			if layer.Clipping {
				invisible = invisible || baseInvisible
			} else {
				baseInvisible = invisible
			}
			block := func(reason string) {
				metadata.BlockingIssues = append(metadata.BlockingIssues, fmt.Sprintf("图层「%s」：%s", layer.Name, reason))
			}
			if invisible {
				if err := visit(layer.Children, true); err != nil {
					return err
				}
				continue
			}
			if layer.Group || layer.Children != nil {
				if err := visit(layer.Children, false); err != nil {
					return err
				}
				continue
			}
			for _, reason := range layer.Unsupported {
				block(reason)
			}
			if layer.Mask != nil && !layer.Mask.Disabled {
				for _, reason := range layer.Mask.Unsupported {
					block(reason)
				}
			}
			for _, opacity := range []*float64{layer.Opacity, layer.FillOpacity} {
				if opacity != nil && (!finite(*opacity) || *opacity < 0 || *opacity > 1) {
					block("图层不透明度无效")
				}
			}
			if layer.Pixels == nil && layer.Bounds.Dx() > 0 && layer.Bounds.Dy() > 0 {
				block("没有可用的栅格像素")
			}
			placed := layer.Placed
			if placed == nil {
				continue
			}
			if placed.Type != "raster" {
				block("仅支持栅格智能对象")
			}
			if RequiresWarpRendering(placed) {
				block("暂不支持 Photoshop Warp 网格或曲面变形")
			}
			for _, reason := range placed.Unsupported {
				block(reason)
			}
			if _, err := PlacedCorners(placed); err != nil {
				block(err.Error())
			}
			if err := AssertImageSize(placed.Width, placed.Height); err != nil {
				block(err.Error())
			}
			if placed.ID == "" {
				block("缺少智能对象标识")
				continue
			}
			objectIndex, exists := objects[placed.ID]
			if !exists {
				name, linked := document.LinkedNames[placed.ID]
				if !linked {
					name = layer.Name
				}
				objectIndex = len(metadata.Objects)
				objects[placed.ID] = objectIndex
				metadata.Objects = append(metadata.Objects, Object{ID: placed.ID, Name: name, Width: placed.Width, Height: placed.Height, Instances: []Instance{}})
			}
			object := &metadata.Objects[objectIndex]
			if object.Width != placed.Width || object.Height != placed.Height {
				block("同一智能对象实例的原始尺寸不一致")
			}
			object.Instances = append(object.Instances, Instance{ID: fmt.Sprintf("%s:%d", placed.ID, count-1), Name: layer.Name,
				Left: layer.Bounds.Min.X, Top: layer.Bounds.Min.Y, Width: layer.Bounds.Dx(), Height: layer.Bounds.Dy()})
		}
		return nil
	}
	if err := visit(document.Layers, false); err != nil {
		return Metadata{}, err
	}
	if len(document.Layers) == 0 {
		metadata.Warnings = append(metadata.Warnings, "文档没有独立图层，将保留保存合成图。")
	}
	objectPixels := int64(0)
	for _, object := range metadata.Objects {
		objectPixels += int64(object.Width) * int64(object.Height)
	}
	if objectPixels > MaxInputPixels {
		metadata.BlockingIssues = append(metadata.BlockingIssues, "智能对象原始画布合计超过 4000 万像素")
	}
	for _, slice := range document.Slices {
		if (slice.Origin != "" && slice.Origin != "userGenerated") || (slice.Type != "" && slice.Type != "image") {
			continue
		}
		if slice.Left < 0 || slice.Top < 0 || slice.Width <= 0 || slice.Height <= 0 ||
			slice.Left > document.Width-slice.Width || slice.Top > document.Height-slice.Height {
			metadata.Warnings = append(metadata.Warnings, "已忽略空白、越界或无效的用户切片。")
			continue
		}
		slice.Name = strings.TrimSpace(slice.Name)
		metadata.Slices = append(metadata.Slices, slice)
	}
	sort.SliceStable(metadata.Slices, func(first, second int) bool {
		if metadata.Slices[first].Top != metadata.Slices[second].Top {
			return metadata.Slices[first].Top < metadata.Slices[second].Top
		}
		return metadata.Slices[first].Left < metadata.Slices[second].Left
	})
	for index := range metadata.Slices {
		if metadata.Slices[index].Name == "" {
			metadata.Slices[index].Name = fmt.Sprintf("切片-%02d", index+1)
		}
	}
	metadata.BlockingIssues, metadata.Warnings = unique(metadata.BlockingIssues), unique(metadata.Warnings)
	if len(metadata.BlockingIssues) == 0 {
		for index, layer := range document.Layers {
			if layer.Hidden || layer.Group || layer.Children != nil || layer.Pixels == nil {
				continue
			}
			background := BackgroundLayer{ID: fmt.Sprintf("layer:%d", index), Name: layer.Name,
				Left: layer.Bounds.Min.X, Top: layer.Bounds.Min.Y, Width: layer.Pixels.Rect.Dx(), Height: layer.Pixels.Rect.Dy()}
			if layer.Placed != nil {
				background.ObjectID = layer.Placed.ID
			}
			metadata.BackgroundLayers = append(metadata.BackgroundLayers, background)
		}
	}
	canvasBytes := int64(document.Width) * int64(document.Height) * 4
	// Includes immutable rasters, prefix/output, projection/encoding scratch,
	// fitted objects and bounded source decode/trim buffers. Excludes compressed files.
	metadata.WorkingBytes = resident + 5*canvasBytes + objectPixels*4 + MaxInputPixels*8 + MaxPixels*4
	if needsTreeComposite(document.Layers) {
		metadata.BackgroundLayers = []BackgroundLayer{}
	}
	return metadata, nil
}

// New validates a parsed document and rejects unsupported features before rendering.
func New(document *Document) (*Engine, error) {
	metadata, err := Inspect(document)
	if err != nil {
		return nil, err
	}
	if len(metadata.BlockingIssues) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnsupported, strings.Join(metadata.BlockingIssues, "；"))
	}
	return &Engine{
		document: document, metadata: metadata, projections: make(map[*Layer]*ProjectionPlan), prefixEnd: -1,
		tree: needsTreeComposite(document.Layers),
	}, nil
}

// Metadata returns a detached report suitable for a frontend API response.
func (engine *Engine) Metadata() Metadata {
	metadata := engine.metadata
	metadata.Objects = append([]Object{}, metadata.Objects...)
	for index := range metadata.Objects {
		metadata.Objects[index].Instances = append([]Instance{}, metadata.Objects[index].Instances...)
	}
	metadata.Slices = append([]Slice{}, metadata.Slices...)
	metadata.Warnings = append([]string{}, metadata.Warnings...)
	metadata.BlockingIssues = append([]string{}, metadata.BlockingIssues...)
	metadata.BackgroundLayers = append([]BackgroundLayer{}, metadata.BackgroundLayers...)
	return metadata
}

// DefaultConfig enables every smart object under its complete source filename.
func DefaultConfig(metadata Metadata) Config {
	config := Config{Fit: "cover", Align: "center", Objects: make([]ObjectConfig, 0, len(metadata.Objects))}
	for _, object := range metadata.Objects {
		config.Objects = append(config.Objects, ObjectConfig{ObjectID: object.ID, Tag: object.Name, Enabled: true})
	}
	return config
}

// ValidateConfig rejects stale references, duplicate targets and print/background conflicts.
func ValidateConfig(config Config, metadata Metadata) error {
	if err := validateFit(config.Fit, config.Align); err != nil {
		return err
	}
	known := make(map[string]bool, len(metadata.Objects))
	for _, object := range metadata.Objects {
		known[object.ID] = true
	}
	seen, enabled := make(map[string]bool), make(map[string]bool)
	for _, object := range config.Objects {
		if seen[object.ObjectID] || !known[object.ObjectID] {
			return errors.New("PSD 对象配置无效，请重新导入模板")
		}
		seen[object.ObjectID] = true
		if object.Enabled {
			if strings.TrimSpace(object.Tag) == "" {
				return errors.New("启用对象的印花标签不能为空")
			}
			enabled[object.ObjectID] = true
		}
	}
	if len(metadata.Objects) > 0 && len(enabled) == 0 {
		return errors.New("请至少启用一个印花对象")
	}
	backgrounds := make(map[string]BackgroundLayer, len(metadata.BackgroundLayers))
	for _, layer := range metadata.BackgroundLayers {
		backgrounds[layer.ID] = layer
	}
	seenRows := make(map[string]bool)
	for _, row := range config.RandomImageRows {
		layer, exists := backgrounds[row.LayerID]
		if !exists || seenRows[row.LayerID] {
			return errors.New("PSD 随机图层不存在或重复，请重新选择背景图层")
		}
		seenRows[row.LayerID] = true
		if row.Enabled && len(row.RandomImages) > 0 && layer.ObjectID != "" && enabled[layer.ObjectID] {
			return fmt.Errorf("图层「%s」不能同时启用印花和随机图", layer.Name)
		}
	}
	return nil
}

func (engine *Engine) backgroundLayer(id string) (*Layer, error) {
	for _, background := range engine.metadata.BackgroundLayers {
		if background.ID != id {
			continue
		}
		index, err := strconv.Atoi(strings.TrimPrefix(id, "layer:"))
		if err != nil || index < 0 || index >= len(engine.document.Layers) {
			break
		}
		return engine.document.Layers[index], nil
	}
	return nil, fmt.Errorf("随机图目标图层「%s」不可替换", id)
}

func checkInputs(replacements, backgrounds map[string]*image.NRGBA) error {
	seen := make(map[*image.NRGBA]bool)
	pixels := int64(0)
	for _, inputs := range []map[string]*image.NRGBA{replacements, backgrounds} {
		for _, source := range inputs {
			if seen[source] {
				continue
			}
			if err := validateImage(source); err != nil {
				return err
			}
			seen[source] = true
			pixels += int64(source.Rect.Dx()) * int64(source.Rect.Dy())
			if pixels > MaxInputPixels {
				return errors.New("本组印花和背景总像素超过 4000 万")
			}
		}
	}
	return nil
}

// Render paints a group while reusing only immutable template geometry and static layers.
func (engine *Engine) Render(
	ctx context.Context, config Config, replacements, backgroundReplacements map[string]*image.NRGBA,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateConfig(config, engine.metadata); err != nil {
		return nil, err
	}
	if err := checkInputs(replacements, backgroundReplacements); err != nil {
		return nil, err
	}
	choices := make(map[string]ObjectConfig, len(config.Objects))
	for _, choice := range config.Objects {
		choices[choice.ObjectID] = choice
	}
	backgrounds := make(map[*Layer]*image.NRGBA, len(backgroundReplacements))
	for id, replacement := range backgroundReplacements {
		layer, err := engine.backgroundLayer(id)
		if err != nil {
			return nil, err
		}
		if layer.Placed != nil && choices[layer.Placed.ID].Enabled {
			return nil, errors.New("同一图层不能同时启用印花和随机图")
		}
		backgrounds[layer] = replacement
	}
	type fitKey struct {
		tag           string
		width, height int
	}
	fitCache := make(map[fitKey]*image.NRGBA)
	fitted := make(map[string]*image.NRGBA)
	for _, object := range engine.metadata.Objects {
		choice := choices[object.ID]
		if !choice.Enabled {
			continue
		}
		tag := strings.TrimSpace(choice.Tag)
		source := replacements[tag]
		if source == nil {
			return nil, fmt.Errorf("缺少印花「%s」的图片", tag)
		}
		key := fitKey{tag, object.Width, object.Height}
		pixels := fitCache[key]
		if pixels == nil {
			var err error
			pixels, err = fitImage(ctx, source, object.Width, object.Height, config.Fit, config.Align)
			if err != nil {
				return nil, err
			}
			fitCache[key] = pixels
		}
		fitted[object.ID] = pixels
	}
	document := engine.document
	if len(document.Layers) == 0 {
		if document.Composite == nil {
			return nil, errors.New("PSD 没有可输出的像素")
		}
		return document.Composite, nil
	}
	if engine.tree {
		return engine.renderTree(ctx, fitted, backgrounds)
	}
	prefixEnd := len(document.Layers)
	for index, layer := range document.Layers {
		if layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		if backgrounds[layer] != nil || (layer.Placed != nil && fitted[layer.Placed.ID] != nil) {
			prefixEnd = index
			break
		}
	}
	if prefixEnd != engine.prefixEnd {
		engine.prefix, engine.prefixEnd = nil, -1
		if prefixEnd > 0 {
			prefix, _ := EmptyImage(document.Width, document.Height)
			for _, layer := range document.Layers[:prefixEnd] {
				if layer.Hidden || layer.Pixels == nil {
					continue
				}
				if err := compositeLayer(ctx, prefix, layer, layer.Pixels); err != nil {
					return nil, err
				}
			}
			engine.prefix = prefix
		}
		engine.prefixEnd = prefixEnd
	}
	output, _ := EmptyImage(document.Width, document.Height)
	if engine.prefix != nil {
		copy(output.Pix, engine.prefix.Pix)
	}
	for _, layer := range document.Layers[prefixEnd:] {
		if layer.Hidden || layerOpacity(layer) == 0 {
			continue
		}
		if background := backgrounds[layer]; background != nil {
			if err := compositeBackground(ctx, output, layer, background); err != nil {
				return nil, err
			}
			continue
		}
		var replacement *image.NRGBA
		if layer.Placed != nil {
			replacement = fitted[layer.Placed.ID]
		}
		if replacement == nil {
			if layer.Pixels != nil {
				if err := compositeLayer(ctx, output, layer, layer.Pixels); err != nil {
					return nil, err
				}
			}
			continue
		}
		plan := engine.projections[layer]
		if plan == nil {
			var err error
			plan, err = CompileProjection(layer, document.Width, document.Height, true)
			if err != nil {
				return nil, err
			}
			engine.projections[layer] = plan
		}
		projected, origin, err := projectImage(ctx, layer, replacement, plan)
		if err != nil {
			return nil, err
		}
		projectedLayer := *layer
		projectedLayer.Bounds = image.Rectangle{Min: origin, Max: origin.Add(projected.Rect.Size())}
		if err := compositeLayer(ctx, output, &projectedLayer, projected); err != nil {
			return nil, err
		}
	}
	return output, nil
}

// RenderWithOptions trims each unique input print once, leaving random background bounds unchanged.
func (engine *Engine) RenderWithOptions(
	ctx context.Context, config Config, replacements, backgrounds map[string]*image.NRGBA, options Options,
) (*image.NRGBA, error) {
	if err := ValidateOptions(options); err != nil {
		return nil, err
	}
	if err := checkInputs(replacements, backgrounds); err != nil {
		return nil, err
	}
	if !options.TrimTransparent {
		return engine.Render(ctx, config, replacements, backgrounds)
	}
	trimmed := make(map[*image.NRGBA]*image.NRGBA)
	inputs := make(map[string]*image.NRGBA, len(replacements))
	for tag, source := range replacements {
		pixels := trimmed[source]
		if pixels == nil {
			var err error
			pixels, err = TrimInputTransparency(source)
			if err != nil {
				return nil, err
			}
			trimmed[source] = pixels
		}
		inputs[tag] = pixels
	}
	return engine.Render(ctx, config, inputs, backgrounds)
}

// Regions returns sorted user slices or one complete document crop.
func (engine *Engine) Regions(options Options) []Slice {
	if options.Slices && len(engine.metadata.Slices) > 0 {
		return append([]Slice{}, engine.metadata.Slices...)
	}
	return []Slice{{Name: "完整样机", Width: engine.document.Width, Height: engine.document.Height}}
}
