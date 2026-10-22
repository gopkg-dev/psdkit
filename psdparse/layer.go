package psdparse

import (
	"image"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

type channel struct {
	id     int16
	length int64
}
type layerRecord struct {
	layer          *psdmodel.Layer
	channels       []channel
	divider        int
	realMask       image.Rectangle
	renderedMask   bool
	userMask       *psdmodel.Mask
	effectsEnabled *bool
}

func (p *parser) layerSection(r *cursor) {
	if r.remaining() == 0 {
		return
	}
	info := r.section(p.structureLength(r))
	var records []*layerRecord
	if info.remaining() > 0 {
		count := int(info.i16())
		if count < 0 {
			p.globalAlpha = true
			count = -count
		}
		if count > psdmodel.MaxLayers {
			r.fail("PSD exceeds 500 layers")
			return
		}
		records = make([]*layerRecord, 0, count)
		for index := 0; index < count && info.err == nil; index++ {
			records = append(records, p.layerRecord(info))
		}
		if info.err == nil {
			for _, record := range records {
				p.layerPixels(info, record)
				if info.err != nil {
					break
				}
			}
		}
	}
	r.absorb(info)
	if r.err != nil {
		return
	}
	if r.remaining() >= 4 {
		r.skip(int64(r.u32()))
	}
	p.additional(r, nil)
	if r.err != nil {
		return
	}
	p.document.Layers = buildLayers(records, r)
	if len(p.document.Layers) != 0 {
		bottom := p.document.Layers[0]
		for _, record := range records {
			if record.layer != bottom || bottom.Group || len(record.channels) < 3 {
				continue
			}
			// Photoshop Background records omit the transparency channel, unlike ordinary opaque layers.
			bottom.Background = true
			for _, channel := range record.channels {
				if channel.id == -1 {
					bottom.Background = false
				}
			}
			break
		}
	}
}

func (p *parser) layerRecord(r *cursor) *layerRecord {
	layer := &psdmodel.Layer{Bounds: rectangle(r)}
	record := &layerRecord{layer: layer}
	p.account(layer.Bounds, r)
	count := int(r.u16())
	if count > 56 {
		r.fail("PSD layer channel count exceeds 56")
		return record
	}
	for range count {
		id := r.i16()
		record.channels = append(record.channels, channel{id: id, length: p.structureLength(r)})
	}
	if r.sig() != "8BIM" {
		r.fail("invalid PSD blend mode signature")
		return record
	}
	layer.BlendMode = r.sig()
	if layer.BlendMode == "norm" {
		layer.BlendMode = "normal"
	}
	opacity := float64(r.u8()) / 255
	layer.Opacity = &opacity
	layer.Clipping = r.u8() != 0
	flags := r.u8()
	layer.Hidden = flags&2 != 0
	r.u8()
	extra := r.section(int64(r.u32()))
	if extra.remaining() == 0 {
		return record
	}
	p.mask(extra, record)
	ranges := extra.section(int64(extra.u32()))
	p.blendingRanges(ranges, layer)
	extra.absorb(ranges)
	layer.Name = extra.pascal(4)
	p.additional(extra, record)
	if record.effectsEnabled != nil && *record.effectsEnabled {
		layer.Unsupported = append(layer.Unsupported, "暂不支持图层效果")
	}
	r.absorb(extra)
	return record
}

func (p *parser) blendingRanges(r *cursor, layer *psdmodel.Layer) {
	if r.remaining()%8 != 0 || r.remaining() > 57*8 {
		r.fail("invalid PSD blending ranges length")
		return
	}
	for r.remaining() > 0 && r.err == nil {
		rangeValue := psdmodel.BlendRange{Source: blendSlider(r), Destination: blendSlider(r)}
		if !orderedBlendSlider(rangeValue.Source) || !orderedBlendSlider(rangeValue.Destination) {
			r.fail("invalid PSD blending range split endpoints")
			return
		}
		layer.BlendRanges = append(layer.BlendRanges, rangeValue)
	}
}

func blendSlider(r *cursor) psdmodel.BlendSlider {
	return psdmodel.BlendSlider{
		BlackLow: r.u8(), BlackHigh: r.u8(), WhiteLow: r.u8(), WhiteHigh: r.u8(),
	}
}

func orderedBlendSlider(value psdmodel.BlendSlider) bool {
	return value.BlackLow <= value.BlackHigh && value.WhiteLow <= value.WhiteHigh
}

func (p *parser) mask(r *cursor, record *layerRecord) {
	maskData := r.section(int64(r.u32()))
	hasReal := slices.ContainsFunc(record.channels, func(value channel) bool { return value.id == -3 })
	if maskData.remaining() == 0 {
		if hasReal {
			r.fail("real mask channel has no mask header")
		}
		return
	}
	if maskData.remaining() < 18 {
		r.fail("incomplete PSD layer mask")
		return
	}
	bounds := rectangle(maskData)
	color := maskData.u8()
	flags := maskData.u8()
	record.renderedMask = flags&8 != 0
	if flags&8 != 0 && bounds == (image.Rectangle{Max: image.Pt(0, -1)}) {
		bounds = image.Rectangle{}
	}
	mask := &psdmodel.Mask{
		Bounds: bounds, DefaultColor: &color, Disabled: flags&2 != 0, Relative: flags&1 != 0, Invert: flags&4 != 0,
	}
	// Stored mask bounds use document coordinates; the original relative-position flag is retained.
	record.layer.Mask = mask
	p.account(bounds, maskData)
	if flags&0xe0 != 0 {
		mask.Unsupported = append(mask.Unsupported, "暂不支持未知像素蒙版标记")
	}
	var realFlags, realColor uint8
	if hasReal {
		realFlags, realColor = maskData.u8(), maskData.u8()
		record.realMask = rectangle(maskData)
		p.account(record.realMask, maskData)
	}
	if flags&16 != 0 {
		parameters := maskData.u8()
		if parameters&1 != 0 {
			density := maskData.u8()
			mask.Density = &density
		}
		if parameters&2 != 0 {
			feather := maskData.f64()
			mask.Feather = &feather
			if feather < 0 || math.IsNaN(feather) || math.IsInf(feather, 0) {
				maskData.fail("invalid PSD user mask feather")
			}
		}
		if parameters&4 != 0 {
			density := maskData.u8()
			record.layer.VectorDensity = &density
		}
		if parameters&8 != 0 {
			feather := maskData.f64()
			record.layer.VectorFeather = &feather
			if feather < 0 || math.IsNaN(feather) || math.IsInf(feather, 0) {
				maskData.fail("invalid PSD vector mask feather")
			} else if feather != 0 {
				mask.Unsupported = append(mask.Unsupported, "暂不支持矢量蒙版羽化")
			}
		}
		if parameters&0xf0 != 0 {
			mask.Unsupported = append(mask.Unsupported, "暂不支持未知蒙版参数")
		}
	}
	if hasReal {
		if record.renderedMask {
			userMask := *mask
			userMask.Bounds, userMask.DefaultColor = record.realMask, &realColor
			userMask.Relative, userMask.Disabled, userMask.Invert = realFlags&1 != 0, realFlags&2 != 0, realFlags&4 != 0
			record.userMask = &userMask
		} else if realFlags&2 == 0 {
			record.layer.Unsupported = append(record.layer.Unsupported, "暂不支持双重蒙版")
		}
	}
	r.absorb(maskData)
}

func (p *parser) layerPixels(r *cursor, record *layerRecord) {
	layer := record.layer
	if len(record.channels) > 0 {
		layer.Pixels = freshImage(layer.Bounds)
	}
	for _, channel := range record.channels {
		section := r.section(channel.length)
		var target *image.NRGBA
		component := int(channel.id)
		switch channel.id {
		case 0, 1, 2:
			target = layer.Pixels
		case -1:
			target = layer.Pixels
			component = 3
		case -2:
			if layer.Mask != nil {
				layer.Mask.Pixels = freshImage(layer.Mask.Bounds)
				target = layer.Mask.Pixels
				component = 0
			}
		case -3:
			if record.userMask != nil {
				record.userMask.Pixels = freshImage(record.userMask.Bounds)
				target, component = record.userMask.Pixels, 0
			}
		}
		if target != nil && section.remaining() > 0 {
			p.decodePlane(section, target, component)
			if channel.id == -2 || channel.id == -3 {
				for offset := 0; offset < len(target.Pix); offset += 4 {
					target.Pix[offset+1], target.Pix[offset+2] = target.Pix[offset], target.Pix[offset]
				}
			}
		}
		r.absorb(section)
		if r.err != nil {
			return
		}
	}
	if layer.VectorMask != nil && layer.Mask != nil {
		if record.renderedMask {
			layer.Mask = record.userMask
		} else if layer.Mask.Pixels == nil {
			layer.Mask = nil
		}
	}
	if layer.FillColor != nil && layer.Pixels != nil {
		for offset := 0; offset < len(layer.Pixels.Pix); offset += 4 {
			for component, value := range layer.FillColor {
				layer.Pixels.Pix[offset+component] = uint8(math.Round(max(0, min(1, value)) * 255))
			}
			layer.Pixels.Pix[offset+3] = 255
		}
	}
}

func buildLayers(records []*layerRecord, r *cursor) []*psdmodel.Layer {
	var root []*psdmodel.Layer
	stack := []*[]*psdmodel.Layer{&root}
	for _, record := range slices.Backward(records) {

		layer := record.layer
		switch record.divider {
		case 1, 2:
			layer.Children = []*psdmodel.Layer{}
			parent := stack[len(stack)-1]
			*parent = append([]*psdmodel.Layer{layer}, (*parent)...)
			stack = append(stack, &layer.Children)
		case 3:
			if len(stack) == 1 {
				r.fail("PSD group boundaries are unbalanced")
				return nil
			}
			stack = stack[:len(stack)-1]
		default:
			parent := stack[len(stack)-1]
			*parent = append([]*psdmodel.Layer{layer}, (*parent)...)
		}
	}
	if len(stack) != 1 {
		r.fail("PSD group boundaries are unbalanced")
	}
	return root
}
