package psdweb

import (
	"context"
	_ "embed"
	"encoding/json/v2"
	"fmt"
	"image"
	"io"
	"reflect"
	"strings"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdvector"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

//go:embed api-docs.json
var documentationBytes []byte

type apiDoc struct {
	Description string
	Parameters  []string
}

var documentation = func() map[string]apiDoc {
	result := map[string]apiDoc{}
	if err := json.Unmarshal(documentationBytes, &result); err != nil {
		panic(fmt.Errorf("psdweb: invalid embedded API documentation: %w", err))
	}
	return result
}()

var contextType = reflect.TypeFor[context.Context]()
var writerType = reflect.TypeFor[io.Writer]()
var fontResolverType = reflect.TypeFor[psdfont.FontResolver]()

func injected(kind reflect.Type) bool {
	return kind == contextType || kind == writerType || kind == fontResolverType || kind.Kind() == reflect.Func
}

func (e *Engine) registerOperations() {
	for _, item := range []struct {
		id, group, label, api string
		function              any
	}{
		{"document.structure", "文档与源精度", "原始文档结构", "psdweb.DocumentStructure", e.documentStructure},
		{"document.defaultLimits", "文档与源精度", "默认源读取预算", "psdkit.DefaultSourceLimits", psdkit.DefaultSourceLimits},
		{"preview.saved", "文档与源精度", "保存预览", "psdkit.DecodeDisplayComposite", psdkit.DecodeDisplayComposite},
		{"preview.region", "文档与源精度", "保存预览区域", "psdkit.DecodeDisplayRegion", psdkit.DecodeDisplayRegion},
		{"render.source", "渲染与画板", "实时重绘文档", "psdkit.RenderSource", psdkit.RenderSource},
		{"render.precision", "色彩与源精度", "16 / 32 位源精度重绘", "psdkit.RenderSourcePrecision", psdkit.RenderSourcePrecision},
		{"render.precisionTile", "色彩与源精度", "源精度区域重绘", "psdkit.RenderSourcePrecisionTile", psdkit.RenderSourcePrecisionTile},
		{"render.tiles", "渲染与画板", "分块渲染导出 ZIP", "psdweb.RenderTiles", e.renderTiles},
		{"render.precisionTiles", "色彩与源精度", "源精度分块 ZIP", "psdweb.RenderPrecisionTiles", e.renderPrecisionTiles},
		{"render.tile", "渲染与画板", "实时区域重绘", "psdkit.RenderSourceTile", psdkit.RenderSourceTile},
		{"render.artboard", "渲染与画板", "渲染指定画板", "psdkit.RenderArtboard", psdkit.RenderArtboard},
		{"render.artboards", "渲染与画板", "渲染所有画板", "psdkit.RenderArtboards", psdkit.RenderArtboards},
		{"render.placed", "智能对象与 Warp", "实时重绘智能对象", "psdkit.RenderSourcePlacedLayer", psdkit.RenderSourcePlacedLayer},
		{"render.composite", "渲染与画板", "合成已解码图层", "psdkit.Composite", psdkit.Composite},
		{"render.compositeRegion", "渲染与画板", "区域合成", "psdkit.CompositeRegion", psdkit.CompositeRegion},
		{"render.legacy", "渲染与画板", "配置式套图引擎", "psdkit.NewRenderer", psdkit.NewRenderer},
		{"render.inspect", "渲染与画板", "套图对象与资格检查", "psdkit.Inspect", psdkit.Inspect},
		{"render.defaultConfig", "渲染与画板", "默认套图配置", "psdkit.DefaultConfig", psdkit.DefaultConfig},
		{"render.validateConfig", "渲染与画板", "校验套图配置", "psdkit.ValidateConfig", psdkit.ValidateConfig},
		{"image.prepare", "创建与导出", "尺寸与裁片输出", "psdkit.PrepareOutput", psdkit.PrepareOutput},
		{"image.validateOptions", "创建与导出", "校验裁片与导出选项", "psdkit.ValidateOptions", psdkit.ValidateOptions},
		{"image.assertSize", "创建与导出", "校验图像尺寸", "psdkit.AssertImageSize", psdkit.AssertImageSize},
		{"image.encode", "创建与导出", "编码 PNG / JPEG", "psdkit.Encode", psdkit.Encode},
		{"warp.uniform.compile", "智能对象与 Warp", "编译均匀分片网格", "psdwarp.CompileUniformGrid", psdwarp.CompileUniformGrid},
		{"warp.uniform.project", "智能对象与 Warp", "均匀分片 Warp", "psdweb.ProjectUniform", e.projectUniform},
		{"warp.patch.compile", "智能对象与 Warp", "编译单片 Warp", "psdwarp.Compile", psdwarp.Compile},
		{"warp.patch.mathematical", "智能对象与 Warp", "显式数学透视 Warp", "psdwarp.CompileMathematical", psdwarp.CompileMathematical},
		{"warp.patch.project", "智能对象与 Warp", "单片 Warp 投影", "psdwarp.Project", psdwarp.Project},
		{"warp.preset.compile", "智能对象与 Warp", "编译预设 Warp 几何", "psdwarp.CompilePresetGeometry", psdwarp.CompilePresetGeometry},
		{"font.decode", "字体与文字", "载入 TTF / OTF / TTC", "psdfont.Decode", psdfont.Decode},
		{"font.layout", "字体与文字", "文字排版", "psdfont.Layout", psdfont.Layout},
		{"font.layoutOpenType", "字体与文字", "OpenType 排版", "psdfont.LayoutOpenType", psdfont.LayoutOpenType},
		{"font.shape", "字体与文字", "GSUB / GPOS 字形塑形", "psdfont.Shape", psdfont.Shape},
		{"font.raster", "字体与文字", "字形光栅化", "psdfont.RasterizeRun", psdfont.RasterizeRun},
		{"font.hint", "字体与文字", "显式字体 Hinting", "psdfont.HintRun", psdfont.HintRun},
		{"text.render", "字体与文字", "实时文字层", "psdkit.RenderTextLayer", psdkit.RenderTextLayer},
		{"text.openType", "字体与文字", "实时复杂文字层", "psdkit.RenderOpenTypeTextLayer", psdkit.RenderOpenTypeTextLayer},
		{"gradient.render", "矢量与填充", "Classic 渐变", "psdgradient.Render", psdgradient.Render},
		{"gradient.validate", "矢量与填充", "校验渐变", "psdgradient.Validate", psdgradient.Validate},
		{"pattern.render", "矢量与填充", "图案填充", "psdpattern.Render", psdpattern.Render},
		{"pattern.validate", "矢量与填充", "校验图案", "psdpattern.Validate", psdpattern.Validate},
		{"vector.raster", "矢量与填充", "矢量蒙版", "psdvector.Rasterize", psdvector.Rasterize},
		{"vector.rgba", "矢量与填充", "矢量形状光栅化", "psdvector.RasterizeNRGBA", psdvector.RasterizeNRGBA},
		{"vector.encodeMask", "矢量与填充", "编码矢量蒙版", "psdvector.EncodeMask", psdvector.EncodeMask},
		{"vector.encodePath", "矢量与填充", "编码路径资源", "psdvector.EncodePath", psdvector.EncodePath},
		{"vector.decodeMask", "矢量与填充", "解析矢量蒙版", "psdvector.DecodeMask", psdvector.DecodeMask},
		{"vector.decodePath", "矢量与填充", "解析路径", "psdvector.DecodePath", psdvector.DecodePath},
		{"effects.raster", "图层效果与调整", "渲染效果平面", "psdeffects.Rasterize", psdeffects.Rasterize},
		{"effects.estimate", "图层效果与调整", "效果预算", "psdeffects.EstimateWorking", psdeffects.EstimateWorking},
		{"effects.validate", "图层效果与调整", "校验效果", "psdeffects.Validate", psdeffects.Validate},
		{"adjustment.apply", "图层效果与调整", "调整图像", "psdadjust.Apply", psdadjust.Apply},
		{"adjustment.compile", "图层效果与调整", "编译调整", "psdadjust.Compile", psdadjust.Compile},
		{"filters.apply", "智能滤镜", "运行滤镜栈", "psdfilter.Apply", psdfilter.Apply},
		{"filters.decode", "智能滤镜", "解析智能滤镜栈", "psdkit.DecodeSmartFilters", psdkit.DecodeSmartFilters},
		{"filters.region", "智能滤镜", "区域滤镜", "psdfilter.ApplyRegion", psdfilter.ApplyRegion},
		{"filters.estimate", "智能滤镜", "滤镜预算", "psdfilter.WorkingEstimate", psdfilter.WorkingEstimate},
		{"filters.halo", "智能滤镜", "滤镜采样范围", "psdfilter.RequiredHalo", psdfilter.RequiredHalo},
		{"color.convert", "色彩与源精度", "源样本转显示像素", "psdkit.ConvertSamples", psdkit.ConvertSamples},
		{"color.defaults", "色彩与源精度", "默认色彩策略", "psdkit.DefaultColorOptions", psdkit.DefaultColorOptions},
		{"color.transform", "色彩与源精度", "RGB ICC 转换", "psdkit.TransformProfile", psdkit.TransformProfile},
		{"color.estimate", "色彩与源精度", "ICC 转换预算", "psdkit.ProfileTransformWorkingBytes", psdkit.ProfileTransformWorkingBytes},
		{"color.profile", "色彩与源精度", "原始 ICC", "psdkit.ReadSourceRGBProfile", psdkit.ReadSourceRGBProfile},
		{"raster.buildPSD", "创建与导出", "图片转可编辑 PSD / PSB", "psdkit.BuildRasterContents", psdkit.BuildRasterContents},
		{"raster.estimate", "创建与导出", "图片 PSD 预算", "psdkit.RasterContentsWorkingBytes", psdkit.RasterContentsWorkingBytes},
		{"cache.create", "文档与源精度", "建立区域 LRU 缓存", "psdfile.NewRegionCache", psdfile.NewRegionCache},
	} {
		e.registerFunction(item.id, item.group, item.label, item.api, item.function)
	}
	for _, item := range []struct {
		kind, group string
		prototype   any
	}{
		{"document", "文档与源精度", (*psdfile.Document)(nil)},
		{"editor", "图层与编辑", (*psdkit.Editor)(nil)},
		{"font", "字体与文字", (*psdfont.Face)(nil)},
		{"uniform", "智能对象与 Warp", (*psdwarp.UniformGrid)(nil)},
		{"patch", "智能对象与 Warp", (*psdwarp.Patch)(nil)},
		{"preset", "智能对象与 Warp", (*psdwarp.PresetGeometry)(nil)},
		{"tiff", "嵌入 TIFF", (*psdfile.TIFFLayerSource)(nil)},
		{"cache", "文档与源精度", (*psdfile.RegionCache)(nil)},
		{"renderer", "渲染与画板", (*psdrender.Engine)(nil)},
		{"adjustment", "图层效果与调整", (*psdadjust.Transform)(nil)},
	} {
		typeOf := reflect.TypeOf(item.prototype)
		for method := range typeOf.Methods() {
			if method.Name == "Close" || method.Name == "Save" || method.Name == "SavePSD" || method.Name == "ReadAt" {
				continue
			}
			api := typeOf.Elem().PkgPath() + "." + typeOf.Elem().Name() + "." + method.Name
			api = strings.TrimPrefix(api, "github.com/gopkg-dev/psdkit/")
			group := methodGroup(item.group, method.Name)
			operation := e.makeBinding(item.kind+"."+method.Name, group, method.Name, api, method.Type, 1)
			operation.method, operation.capability.TargetKind = method.Name, item.kind
			e.bindings[operation.capability.ID] = operation
		}
	}
	e.registerSystemCapabilities()
}

func methodGroup(fallback, name string) string {
	for _, item := range []struct {
		group string
		words []string
	}{
		{"智能滤镜", []string{"Filter"}},
		{"字体与文字", []string{"Text", "EngineData"}},
		{"矢量与填充", []string{"Vector", "Path", "Pattern", "Gradient", "SolidFill"}},
		{"图层效果与调整", []string{"Effects", "Adjustment"}},
		{"智能对象与 Warp", []string{"SmartObject", "Linked", "Contents", "Placed"}},
		{"渲染与画板", []string{"Artboard"}},
	} {
		for _, word := range item.words {
			if strings.Contains(name, word) {
				return item.group
			}
		}
	}
	return fallback
}

func (e *Engine) registerFunction(id, group, label, api string, function any) {
	value := reflect.ValueOf(function)
	operation := e.makeBinding(id, group, label, api, value.Type(), 0)
	operation.function = value
	e.bindings[id] = operation
}

func (e *Engine) makeBinding(id, group, label, api string, kind reflect.Type, skip int) binding {
	doc := documentation[api]
	if api == "psdweb.ProjectUniform" {
		doc = apiDoc{Description: "读取原始 placed 元数据，以调用方图片执行显式均匀分片 Warp；禁止读取父层保存像素。",
			Parameters: []string{"ctx", "source", "layerIndex", "pixels", "canvas", "options"}}
	}
	if api == "psdweb.DocumentStructure" {
		doc = apiDoc{Description: "只读导出原始文档结构，包括资源、附加块、通道区间和层集；不解码像素。",
			Parameters: []string{"ctx", "source"}}
	}
	if api == "psdweb.RenderTiles" || api == "psdweb.RenderPrecisionTiles" {
		doc = apiDoc{Description: "通过 Go 分块访问器依次导出，保留绝对位置；ZIP 总字节受工作区输出预算限制。",
			Parameters: []string{"ctx", "source", "bounds", "size", "options"}}
	}
	operation := binding{capability: Capability{ID: id, Group: group, Label: label, GoAPI: api,
		Description: doc.Description, Parameters: []Parameter{}}, variadic: kind.IsVariadic()}
	for index := skip; index < kind.NumIn(); index++ {
		argumentType := kind.In(index)
		name := "arg" + strconvItoa(index-skip)
		if index-skip < len(doc.Parameters) {
			name = doc.Parameters[index-skip]
		}
		operation.argumentTypes = append(operation.argumentTypes, argumentType)
		operation.argumentNames = append(operation.argumentNames, name)
		if injected(argumentType) {
			continue
		}
		optional := kind.IsVariadic() && index == kind.NumIn()-1
		if optional {
			argumentType = argumentType.Elem()
		}
		operation.capability.Parameters = append(operation.capability.Parameters, Parameter{
			Name: name, Type: argumentType.String(), Optional: optional,
			Example: argumentExample(id, name, argumentType), Schema: typeSchema(argumentType, 0),
		})
	}
	if strings.Contains(id, "uniform") || strings.Contains(id, "preset") || strings.Contains(id, "mathematical") {
		operation.capability.Policy = "显式几何/预览；保留库的精度门禁，不接入自动整图。"
	}
	if strings.HasPrefix(id, "preview.") {
		operation.capability.Policy = "显示保存预览，不是实时图层重绘。"
	}
	if strings.HasPrefix(id, "render.precision") {
		operation.capability.Policy = "保留原始精度/HDR；浮点数据作为二进制导出，不自动压到 RGB8。"
	}
	return operation
}

func resourceKind(kind reflect.Type) string {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	switch kind.PkgPath() + "." + kind.Name() {
	case "github.com/gopkg-dev/psdkit/psdfile.Document":
		return "document"
	case "github.com/gopkg-dev/psdkit/psdedit.Editor":
		return "editor"
	case "github.com/gopkg-dev/psdkit/psdfont.Face":
		return "font"
	case "github.com/gopkg-dev/psdkit/psdwarp.UniformGrid":
		return "uniform"
	case "github.com/gopkg-dev/psdkit/psdwarp.Patch":
		return "patch"
	case "github.com/gopkg-dev/psdkit/psdwarp.PresetGeometry":
		return "preset"
	case "github.com/gopkg-dev/psdkit/psdfile.TIFFLayerSource":
		return "tiff"
	case "github.com/gopkg-dev/psdkit/psdfile.RegionCache":
		return "cache"
	case "github.com/gopkg-dev/psdkit/psdrender.Engine":
		return "renderer"
	case "github.com/gopkg-dev/psdkit/psdadjust.Transform":
		return "adjustment"
	case "github.com/gopkg-dev/psdkit/psdmodel.Document":
		return "decoded"
	}
	return "value"
}

func (e *Engine) documentStructure(ctx context.Context, source *psdfile.Document) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	view, err := e.exportValue(reflect.ValueOf(source), 0)
	if err != nil {
		return nil, err
	}
	return view.(map[string]any), nil
}

func (e *Engine) projectUniform(
	ctx context.Context, source *psdfile.Document, layerIndex int, pixels *image.NRGBA,
	canvas image.Rectangle, options ...psdwarp.Options,
) (*image.NRGBA, ProjectionAudit, error) {
	guarded, audit, closeSource, err := e.projectionSource(source)
	if err != nil {
		return nil, audit, err
	}
	if closeSource {
		defer func() { _ = guarded.Close() }()
	}
	output, err := projectUniform(ctx, guarded, layerIndex, pixels, canvas, e.config, options...)
	audit.SourceRGBASHA256 = pixelHash(pixels)
	if reader := audit.reader; reader != nil {
		audit.ParentSavedPixelReads, audit.MetadataReadBytes = reader.denied, reader.readBytes
	}
	if err != nil {
		err = &projectionFailure{cause: err, audit: audit}
	}
	return output, audit, err
}
