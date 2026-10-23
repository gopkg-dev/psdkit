package psdparse

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

func (p *parser) additional(r *cursor, record *layerRecord) {
	for r.remaining() >= 12 && r.err == nil {
		if record == nil {
			for r.remaining() > 0 && r.err == nil {
				if r.u8() != 0 {
					r.position--
					break
				}
			}
			if r.remaining() < 12 {
				break
			}
		}
		signature := r.sig()
		if signature != "8BIM" && signature != "8B64" {
			r.fail("invalid additional PSD information signature %q at offset %d", signature, r.position-4)
			return
		}
		key := r.sig()
		var length int64
		if signature == "8B64" || (p.version == 2 && psbAdditionalLength64(key)) {
			length = checkedLength64(r)
		} else {
			length = int64(r.u32())
		}
		section := r.section(length)
		p.additionalEntry(section, key, record)
		r.absorb(section)
		if length%2 != 0 && r.remaining() > 0 {
			r.skip(1)
		}
	}
}

func psbAdditionalLength64(key string) bool {
	switch key {
	case "LMsk", "Lr16", "Lr32", "Layr", "Mt16", "Mt32", "Mtrn", "Alph", "FMsk", "lnk2",
		"FEid", "FXid", "PxSD":
		return true
	default:
		return false
	}
}

func (p *parser) additionalEntry(r *cursor, key string, record *layerRecord) {
	if strings.HasPrefix(key, "lnk") && (key == "lnkD" || key == "lnk2" || key == "lnk3" || key == "lnkE") {
		p.linked(r)
		return
	}
	if key == "artb" || key == "artd" || key == "abdd" {
		p.document.BlockingIssues = append(p.document.BlockingIssues, "首版暂不支持画板文档")
		return
	}
	if record == nil {
		if key == "Mtrn" {
			p.globalAlpha = true
		}
		if key == "Layr" || key == "Lr16" || key == "Lr32" {
			r.fail("unsupported alternate PSD layer-information section %q", key)
		}
		return
	}
	layer := record.layer
	switch key {
	case "luni":
		layer.Name = r.unicode()
	case "lsct", "lsdk":
		record.divider = int(r.u32())
		layer.Group = record.divider == 1 || record.divider == 2
		if record.divider > 3 {
			layer.Unsupported = append(layer.Unsupported, "暂不支持未知图层组分隔类型")
		}
		if r.remaining() >= 8 {
			if r.sig() != "8BIM" {
				r.fail("invalid PSD group blend signature")
			}
			blend := r.sig()
			if blend == "norm" {
				blend = "normal"
			}
			layer.BlendMode = blend
		}
	case "iOpa":
		opacity := float64(r.u8()) / 255
		layer.FillOpacity = &opacity
	case "clbl":
		layer.BlendClipped = advancedBoolean(r, key)
	case "infx":
		layer.BlendInterior = advancedBoolean(r, key)
	case "lmgm":
		layer.MaskAsGlobal = advancedBoolean(r, key)
	case "vmgm":
		layer.VectorMaskAsGlobal = advancedBoolean(r, key)
	case "tsly":
		layer.TransparencyShapes = advancedBoolean(r, key)
	case "SoLd", "SoLE":
		layer.Placed = readPlaced(r)
	case "PlLd":
		placed := readOldPlaced(r)
		if layer.Placed == nil {
			layer.Placed = placed
		}
	case "vmsk", "vsms":
		remaining := int64(32<<20) - p.vectorBytes
		if r.remaining() > remaining {
			r.fail("PSD vector-mask metadata exceeds the aggregate budget")
			return
		}
		start := r.position
		mask, err := psdvector.DecodeMask(context.Background(), r.bytes(r.remaining()), p.document.Width,
			p.document.Height, psdvector.DecodeOptions{SourceOffset: start, MaxBytes: remaining})
		if err != nil {
			r.err = err
			return
		}
		p.vectorBytes += int64(cap(mask.Records))*int64(unsafe.Sizeof(psdvector.Record{})) +
			int64(cap(mask.Subpaths))*int64(unsafe.Sizeof(psdvector.Subpath{})) + int64(len(mask.Padding))
		for _, path := range mask.Subpaths {
			p.vectorBytes += int64(cap(path.Knots)) * int64(unsafe.Sizeof(psdvector.Knot{}))
		}
		if p.vectorBytes > 32<<20 {
			r.fail("PSD vector-mask metadata exceeds the aggregate budget")
			return
		}
		layer.VectorMask = mask
	case "knko":
		if r.remaining() != 4 {
			r.fail("invalid PSD Knockout setting length")
			return
		}
		layer.Knockout = psdmodel.Knockout(r.u8())
		r.skip(3)
	case "brst":
		if r.remaining()%4 != 0 || r.remaining()/4 > int64(56-len(layer.ChannelRestrictions)) {
			r.fail("invalid PSD channel blending restrictions length")
			return
		}
		for r.remaining() > 0 && r.err == nil {
			layer.ChannelRestrictions = append(layer.ChannelRestrictions, r.u32())
		}
	case "FEid", "FXid":
		layer.Unsupported = append(layer.Unsupported, "暂不支持动态滤镜蒙版")
	case "PxSc", "PxSD":
		layer.Unsupported = append(layer.Unsupported, "暂不支持视频或外部像素来源图层")
	case "lfx2", "lmfx", "lfxs":
		version := r.u32()
		effects := readVersionDescriptor(r)
		enabled := version != 0 || objectEffectsEnabled(effects)
		record.effectsEnabled = &enabled
	case "lrFX":
		if record.effectsEnabled == nil {
			enabled := legacyEffectsEnabled(r)
			record.effectsEnabled = &enabled
		}
	case "brit", "levl", "curv", "expA", "nvrt", "post", "thrs", "mixr", "blnc", "hue2", "hue ", "phfl":
		remaining := int64(32<<20) - p.adjustmentBytes
		if r.remaining() > remaining {
			r.fail("PSD adjustment metadata exceeds the aggregate budget")
			return
		}
		start := r.position
		adjustment, err := psdadjust.Decode(context.Background(), key, r.bytes(r.remaining()),
			psdadjust.DecodeOptions{SourceOffset: start, MaxBytes: remaining})
		if err != nil {
			if errors.Is(err, psdadjust.ErrUnsupported) {
				layer.Unsupported = append(layer.Unsupported, "unsupported adjustment encoding")
				return
			}
			r.err = err
			return
		}
		p.adjustmentBytes += int64(len(adjustment.Raw)) + 16<<10
		if p.adjustmentBytes > 32<<20 {
			r.fail("PSD adjustment metadata exceeds the aggregate budget")
			return
		}
		layer.Adjustment = adjustment
	case "vibA", "blwh", "clrL", "grdm", "selc", "CgEd":
		layer.Unsupported = append(layer.Unsupported, "暂不支持调整图层")
	case "SoCo":
		fill := readVersionDescriptor(r)
		color := desc(fill["Clr "])
		if color != nil {
			values := [3]float64{number(color["Rd  "]) / 255, number(color["Grn "]) / 255, number(color["Bl  "]) / 255}
			if color["Rd  "] != nil && color["Grn "] != nil && color["Bl  "] != nil {
				layer.FillColor = &values
			} else {
				layer.Unsupported = append(layer.Unsupported, "solid fill uses unsupported color components")
			}
		} else {
			layer.Unsupported = append(layer.Unsupported, "solid fill uses unsupported color components")
		}
	case "TySh", "tySh", "GdFl", "PtFl", "vscg", "vstk":
		warning := "文字和形状图层保留 PSD 已保存像素，首版不重新排字或绘制矢量。"
		if !slices.Contains(p.document.Warnings, warning) {
			p.document.Warnings = append(p.document.Warnings, warning)
		}
	case "lyid", "lspf", "fxrp", "lyvr", "lnsr", "shmd", "lycl", "lclr", "fxid", "FMsk", "sn2P", "vowv",
		"vogk", "pths", "cinf", "CAI ", "OCIO", "lnkF":
		// These entries are editing metadata or already represented by saved RGB pixels.
	default:
		warning := fmt.Sprintf("未解析 PSD 扩展字段：%s", strings.TrimSpace(key))
		if !slices.Contains(p.document.Warnings, warning) {
			p.document.Warnings = append(p.document.Warnings, warning)
		}
	}
}

func objectEffectsEnabled(effects descriptor) bool {
	if keys, ok := effects["_orderedKeys"].([]string); ok {
		for _, key := range keys {
			if key == "_orderedKeys" || key == "_orderedKinds" {
				return true
			}
		}
	}
	if switchValue, exists := effects["masterFXSwitch"]; exists {
		enabled, valid := switchValue.(bool)
		if !valid {
			return true
		}
		if !enabled {
			return false
		}
	}
	for key, value := range effects {
		switch key {
		case "_classID", "_orderedKeys", "_orderedKinds", "masterFXSwitch":
			continue
		case "Scl ", "scale":
			if !effectScale(value) {
				return true
			}
			continue
		case "numModifyingFX":
			count, valid := value.(float64)
			if !valid || count != 0 {
				return true
			}
			continue
		}
		if !knownEffect(key) || effectEntryEnabled(value) {
			return true
		}
	}
	return false
}

func effectScale(value any) bool {
	var scale float64
	switch typed := value.(type) {
	case float64:
		scale = typed
	case unitValue:
		scale = typed.Value
	default:
		return false
	}
	return scale >= 0 && !math.IsNaN(scale) && !math.IsInf(scale, 0)
}

func knownEffect(key string) bool {
	switch strings.TrimSuffix(key, "Multi") {
	case "DrSh", "dropShadow", "IrSh", "innerShadow", "OrGl", "outerGlow", "IrGl", "innerGlow",
		"ebbl", "bevelEmboss", "SoFi", "solidFill", "GrFl", "gradientFill", "patternFill", "PtFl",
		"FrFX", "frameFX", "ChFX", "chromeFX":
		return true
	default:
		return false
	}
}

func effectEntryEnabled(value any) bool {
	switch typed := value.(type) {
	case descriptor:
		enabled, valid := typed["enab"].(bool)
		return !valid || enabled
	case []any:
		for _, item := range typed {
			effect, valid := item.(descriptor)
			if !valid || effectEntryEnabled(effect) {
				return true
			}
		}
		return false
	default:
		return true
	}
}

func advancedBoolean(r *cursor, key string) *bool {
	if r.remaining() != 4 {
		r.fail("invalid PSD advanced blending flag %q length", key)
		return nil
	}
	value := r.u8()
	if value > 1 {
		r.fail("invalid PSD advanced blending flag %q value", key)
		return nil
	}
	r.skip(3)
	enabled := value != 0
	return &enabled
}

func legacyEffectsEnabled(r *cursor) bool {
	r.u16()
	count := int(r.u16())
	enabled := true
	if count > 1000 {
		r.fail("PSD contains too many legacy effects")
		return true
	}
	for index := 0; index < count && r.err == nil; index++ {
		if r.sig() != "8BIM" {
			r.fail("invalid legacy Photoshop effects signature")
			return true
		}
		kind := r.sig()
		effect := r.section(int64(r.u32()))
		if kind == "cmnS" {
			effect.u32()
			enabled = effect.u8() != 0
			r.absorb(effect)
		}
	}
	return enabled
}

func readPlaced(r *cursor) *psdmodel.Placed {
	if r.sig() != "soLD" {
		r.fail("invalid smart-object descriptor signature")
		return nil
	}
	version := r.u32()
	if version != 4 && version != 5 {
		r.fail("unsupported smart-object descriptor version %d", version)
		return nil
	}
	value := readVersionDescriptor(r)
	size := desc(value["Sz  "])
	placed := &psdmodel.Placed{ID: text(value["Idnt"]), Type: placedType(int(number(value["Type"]))),
		Width: int(number(size["Wdth"])), Height: int(number(size["Hght"])), Transform: numbers(value["Trnf"]),
		NonAffineTransform: numbers(value["nonAffineTransform"])}
	if number(size["Wdth"]) != float64(placed.Width) || number(size["Hght"]) != float64(placed.Height) {
		placed.Unsupported = append(placed.Unsupported, "智能对象原始画布尺寸必须为整数")
	}
	if slices.Equal(placed.Transform, placed.NonAffineTransform) {
		placed.NonAffineTransform = nil
	}
	warp := desc(value["warp"])
	if quilt := desc(value["quiltWarp"]); quilt != nil {
		warp = quilt
	}
	placed.Warp = readWarp(warp)
	if filters := desc(value["filterFX"]); filters != nil && boolean(filters["enab"]) {
		items, _ := filters["filterFXList"].([]any)
		for _, item := range items {
			if boolean(desc(item)["enab"]) {
				placed.Unsupported = append(placed.Unsupported, "暂不支持智能滤镜")
				break
			}
		}
	}
	return placed
}

func readOldPlaced(r *cursor) *psdmodel.Placed {
	if r.sig() != "plcL" || r.u32() != 3 {
		r.fail("invalid legacy smart-object header")
		return nil
	}
	placed := &psdmodel.Placed{ID: r.pascal(1)}
	r.u32()
	r.u32()
	r.u32()
	placed.Type = placedType(int(r.u32()))
	placed.Transform = make([]float64, 8)
	for index := range placed.Transform {
		placed.Transform[index] = r.f64()
	}
	if r.u32() != 0 {
		r.fail("unsupported legacy warp version")
		return placed
	}
	placed.Warp = readWarp(readVersionDescriptor(r))
	// Legacy PlLd contains no original canvas size; render validation reports that missing information.
	return placed
}

func placedType(value int) string {
	if value < 0 || value > 3 {
		return "unknown"
	}
	return []string{"unknown", "vector", "raster", "image stack"}[value]
}

func readWarp(value descriptor) *psdmodel.Warp {
	if value == nil {
		return nil
	}
	style := enumValue(value["warpStyle"])
	switch style {
	case "warpNone", "":
		style = "none"
	case "warpCustom":
		style = "custom"
	}
	rotate := enumValue(value["warpRotate"])
	switch rotate {
	case "", "Hrzn":
		rotate = "horizontal"
	case "Vrtc":
		rotate = "vertical"
	}
	warp := &psdmodel.Warp{
		Style: style, Rotate: rotate, Value: number(value["warpValue"]), Values: numbers(value["warpValues"]),
		Perspective: number(value["warpPerspective"]), PerspectiveOther: number(value["warpPerspectiveOther"]),
		UOrder: int(number(value["uOrder"])), VOrder: int(number(value["vOrder"]))}
	readWarpProperties(value, warp)
	readWarpQuiltProperties(value, warp, "warp")
	_, rows := value["deformNumRows"]
	_, columns := value["deformNumCols"]
	_, xQuilt := value["quiltSliceX"]
	_, yQuilt := value["quiltSliceY"]
	warp.Split = rows || columns || xQuilt || yQuilt || warp.ClassID == "quiltWarp"
	if bounds := desc(value["bounds"]); bounds != nil {
		warp.Bounds = &psdmodel.WarpBounds{Left: number(bounds["Left"]), Top: number(bounds["Top "]),
			Right: number(bounds["Rght"]), Bottom: number(bounds["Btom"]), Pixels: true}
		for index, key := range []string{"Left", "Top ", "Rght", "Btom"} {
			if bounds[key] == nil {
				warp.Bounds.Pixels = false
			}
			if unit, ok := bounds[key].(unitValue); ok {
				warp.Bounds.Units[index] = unit.Unit
				if unit.Unit != "#Pxl" {
					warp.Bounds.Pixels = false
				}
			}
		}
		readWarpBoundsProperties(bounds, warp)
	}
	if envelope := desc(value["customEnvelopeWarp"]); envelope != nil {
		warp.HasEnvelope = true
		_, xQuilt := envelope["quiltSliceX"]
		_, yQuilt := envelope["quiltSliceY"]
		warp.Split = warp.Split || xQuilt || yQuilt
		items, _ := envelope["meshPoints"].([]objectArrayItem)
		readWarpEnvelopeProperties(envelope, warp)
		readWarpQuiltProperties(envelope, warp, "envelope")
		var xs, ys []float64
		for _, item := range items {
			warp.MeshClass = item.ClassID
			if item.Kind == "Hrzn" {
				if xs != nil {
					warp.Unsupported = append(warp.Unsupported, "duplicate horizontal mesh coordinates")
				}
				xs = item.Values
				warp.MeshUnits[0] = item.Unit
			}
			if item.Kind == "Vrtc" {
				if ys != nil {
					warp.Unsupported = append(warp.Unsupported, "duplicate vertical mesh coordinates")
				}
				ys = item.Values
				warp.MeshUnits[1] = item.Unit
			}
			if item.Kind != "Hrzn" && item.Kind != "Vrtc" || item.Unit != "#Pxl" {
				warp.Unsupported = append(warp.Unsupported, "unknown mesh coordinate axis or unit")
			}
			if item.ClassID != "" && item.ClassID != "rationalPoint" {
				warp.Unsupported = append(warp.Unsupported, "unknown mesh coordinate class: "+item.ClassID)
			}
		}
		for index := range xs {
			y := math.NaN()
			if index < len(ys) {
				y = ys[index]
			}
			warp.MeshPoints = append(warp.MeshPoints, psdmodel.Point{X: xs[index], Y: y})
		}
	}
	return warp
}

func (p *parser) linked(r *cursor) {
	for r.remaining() > 8 && r.err == nil {
		length := checkedLength64(r)
		entry := r.section(length)
		kind := entry.sig()
		version := entry.u32()
		if (kind != "liFD" && kind != "liFE" && kind != "liFA") || version < 1 || version > 7 {
			r.fail("unsupported linked-file record type or version")
			return
		}
		id, name := entry.pascal(1), entry.unicode()
		p.document.LinkedNames[id] = name
		r.absorb(entry)
		padding := (4 - length%4) % 4
		if padding <= r.remaining() {
			r.skip(padding)
		}
	}
}
