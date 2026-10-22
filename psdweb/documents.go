package psdweb

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"reflect"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func (e *Engine) loadOptions() psdfile.LoadOptions {
	limits := psdfile.DefaultLimits()
	limits.MaxFileBytes, limits.MaxDecodeBytes = e.config.MaxBytes, e.config.MaxBytes
	limits.MaxDecodePixels = e.config.MaxPixels
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, e.config.MaxBytes)
	limits.MaxReadBytes = min(limits.MaxReadBytes, e.config.MaxOutputBytes)
	return psdfile.LoadOptions{Limits: limits}
}

func (e *Engine) openDocument(ctx context.Context, request Request) (any, error) {
	if len(request.Args) == 0 {
		return nil, fmt.Errorf("%w: provide an asset", ErrProtocol)
	}
	var input struct {
		Asset string `json:"$asset"`
	}
	if err := json.Unmarshal(request.Args[0], &input); err != nil {
		return nil, err
	}
	item, exists := e.assets[input.Asset]
	if !exists {
		return nil, fmt.Errorf("%w: document asset not found", ErrProtocol)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	source, err := psdkit.ParseDocument(item.data, e.loadOptions())
	if err != nil {
		return nil, err
	}
	editor, err := psdkit.NewEditor(source)
	if err != nil {
		_ = source.Close()
		return nil, err
	}
	documentID, err := e.keep(reflect.ValueOf(source), "", item.id)
	if err != nil {
		_ = editor.Close()
		_ = source.Close()
		return nil, err
	}
	editorID, err := e.keep(reflect.ValueOf(editor), documentID, item.id)
	if err != nil {
		_ = e.release(documentID)
		_ = editor.Close()
		return nil, err
	}
	layers, err := editor.Tree()
	if err != nil {
		_ = e.release(documentID)
		return nil, err
	}
	return map[string]any{"document": documentID, "editor": editorID, "asset": item.id,
		"name": item.name, "header": source.Header, "layers": layers}, nil
}

func (e *Engine) createDocument(ctx context.Context, request Request, demonstration bool) (any, error) {
	width, height, version := 640, 512, uint16(1)
	if len(request.Args) > 0 {
		if err := json.Unmarshal(request.Args[0], &width); err != nil {
			return nil, err
		}
	}
	if len(request.Args) > 1 {
		if err := json.Unmarshal(request.Args[1], &height); err != nil {
			return nil, err
		}
	}
	if len(request.Args) > 2 {
		if err := json.Unmarshal(request.Args[2], &version); err != nil {
			return nil, err
		}
	}
	if width <= 0 || height <= 0 || int64(width) > e.config.MaxPixels/int64(height) ||
		int64(width) > e.config.MaxBytes/4/int64(height) {
		return nil, ErrLimit
	}
	editor, err := psdkit.CreateDocument(ctx, width, height,
		psdkit.CreateOptions{Version: version, Limits: e.loadOptions().Limits})
	if err != nil {
		return nil, err
	}
	defer func() { _ = editor.Close() }()
	if demonstration {
		if err := populateDemo(ctx, editor, width, height); err != nil {
			return nil, err
		}
	}
	output := &boundedBuffer{limit: e.config.MaxOutputBytes}
	if err := editor.WritePSD(ctx, output, psdkit.SaveOptions{MaxOutputBytes: e.config.MaxOutputBytes}); err != nil {
		return nil, err
	}
	name := request.Name
	if name == "" {
		name = "psdkit-demo.psd"
		if version == 2 {
			name = "psdkit-demo.psb"
		}
	}
	assetView, err := e.putAsset(Request{Name: name, Kind: "document"}, output.Bytes())
	if err != nil {
		return nil, err
	}
	id := assetView.(map[string]any)["asset"].(string)
	argument, _ := json.Marshal(map[string]string{"$asset": id})
	return e.openDocument(ctx, Request{Args: []jsontext.Value{argument}})
}

func projectUniform(
	ctx context.Context, source *psdfile.Document, layerIndex int, pixels *image.NRGBA,
	canvas image.Rectangle, config Config, options ...psdwarp.Options,
) (*image.NRGBA, error) {
	if source == nil {
		return nil, ErrProtocol
	}
	if canvas.Empty() {
		canvas = image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
	}
	width, height := int64(canvas.Dx()), int64(canvas.Dy())
	if width <= 0 || height <= 0 || width > config.MaxPixels/height || width > config.MaxBytes/8/height {
		return nil, ErrLimit
	}
	view, err := source.ReadLayerMetadata(ctx, layerIndex)
	if err != nil {
		return nil, err
	}
	grid, err := psdwarp.CompileUniformGrid(ctx, view.Layer.Placed, options...)
	if err != nil {
		return nil, err
	}
	warped, origin, err := grid.Project(ctx, pixels, canvas)
	if err != nil {
		return nil, err
	}
	frame := image.NewNRGBA(canvas)
	draw.Draw(frame, warped.Rect.Add(origin), warped, warped.Rect.Min, draw.Src)
	return frame, nil
}

func populateDemo(ctx context.Context, editor *psdkit.Editor, width, height int) error {
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		if err := ctx.Err(); err != nil {
			return err
		}
		for x := range width {
			// 深蓝到蓝紫的纵向渐变，右上方叠加一团蓝色光晕；与网站品牌色一致。
			t := float64(y)/float64(height)*.75 + float64(x)/float64(width)*.25
			red, green, blue := 7+t*36, 11+t*15, 38+t*60
			glow := max(0, 1-math.Hypot(float64(x)-float64(width)*.72, float64(y)-float64(height)*.32)/(float64(height)*.7))
			glow *= glow * .6
			red, green, blue = red+(79-red)*glow, green+(123-green)*glow, blue+(255-blue)*glow
			alpha := byte(255)
			if math.Hypot(float64(x)-float64(width)*.25, float64(y)-float64(height)*.3) < float64(height)*.12 {
				alpha = 0
			}
			pixels.SetNRGBA(x, y, color.NRGBA{R: byte(red), G: byte(green), B: byte(blue), A: alpha})
		}
	}
	if err := editor.ReplacePixels(1, pixels); err != nil {
		return err
	}
	if err := editor.Rename(1, "渐变像素"); err != nil {
		return err
	}
	group, err := editor.AddGroup(0, 1, "图形元素", true)
	if err != nil {
		return err
	}
	for index, name := range []string{"洋红矩形", "蓝色三角", "白色线条", "青色圆形"} {
		size := max(16, min(width, height)/5)
		shape := image.NewNRGBA(image.Rect(0, 0, size, size))
		for row := range size {
			for column := range size {
				pixel := color.NRGBA{R: 255, G: 91, B: 214, A: 255}
				if index == 1 {
					pixel = color.NRGBA{R: 109, G: 92, B: 255, A: 255}
					if column < size/2-row/2 || column > size/2+row/2 {
						pixel.A = 0
					}
				}
				if index == 2 {
					pixel = color.NRGBA{R: 255, G: 255, B: 255}
					if (row+column)%32 < 3 {
						pixel.A = 255
					}
				}
				if index == 3 {
					pixel = color.NRGBA{R: 34, G: 228, B: 255, A: 210}
					if math.Hypot(float64(column-size/2), float64(row-size/2)) > float64(size)/2 {
						pixel.A = 0
					}
				}
				shape.SetNRGBA(column, row, pixel)
			}
		}
		origin := image.Pt(width*2/3, height/5+index*height/6)
		if index == 2 {
			origin = image.Pt(width/8, height*2/3)
		}
		if index == 3 {
			origin = image.Pt(width/2, height/2)
		}
		if _, err := editor.AddRaster(group, index, name, shape, origin); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) registerSystemCapabilities() {
	for _, item := range []struct {
		id, label, group, description string
		parameters                    []Parameter
	}{
		{"system.capabilities", "能力目录", "工作区", "浏览器适配的原始 Go API 与参数类型。", nil},
		{"system.state", "资源状态", "工作区", "列出本地资产、会话与预算。", nil},
		{"system.reset", "清空工作区", "工作区", "关闭所有会话并释放本地资源。", nil},
		{"asset.put", "导入文件", "工作区", "以可转移二进制导入 PSD、图片、字体或 ICC。", nil},
		{"asset.remove", "删除本地资产", "工作区", "存在依赖时拒绝删除。", nil},
		{"resource.release", "关闭资源或文档", "工作区", "关闭文档会一并释放它的派生资源。", nil},
		{"document.open", "打开 PSD / PSB", "文档与源精度", "从内存原字节索引，保留未知块并按需解码。",
			[]Parameter{{Name: "asset", Type: "binary asset", Example: map[string]string{"$asset": ""}}}},
		{"document.create", "新建 PSD / PSB", "创建与导出", "新建透明 RGB8 文档，版本 1/2 分别是 PSD/PSB。",
			[]Parameter{{Name: "width", Type: "int", Example: 640}, {Name: "height", Type: "int", Example: 512},
				{Name: "version", Type: "uint16", Example: 1}}},
		{"demo.create", "新建示例", "创建与导出", "通过 Go 创建可编辑示例，所有画面均为真实文档像素。", nil},
	} {
		e.bindings[item.id] = binding{capability: Capability{ID: item.id, Label: item.label,
			Group: item.group, GoAPI: "psdweb.Engine.Execute", Description: item.description,
			Parameters: item.parameters}}
	}
	for id, kind := range map[string]string{"asset.remove": "asset", "resource.release": "resource"} {
		operation := e.bindings[id]
		operation.capability.TargetKind = kind
		e.bindings[id] = operation
	}
}
