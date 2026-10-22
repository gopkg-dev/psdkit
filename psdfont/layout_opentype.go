package psdfont

import (
	"context"
	"image"
	"math"
	"unicode/utf8"
	"unsafe"
)

// LayoutOpenType places one explicit single-line Latin OpenType run with owned unhinted outlines.
// ShapeOptions alone controls optional features; Style.Kerning must be false and Tracking must be zero.
// It never calls legacy kern or applies AAT, hinting, paragraph layout, or Photoshop antialiasing.
// Glyph X/Y include shaped placement offsets; Advance and AdvanceWidth are pixel X advances.
// Positive design-unit Y offsets and advances move upwards in the downwards-positive pixel canvas.
// Scale products round to float64 before translation, keeping integer bounds consistent across FMA targets.
// LayoutOptions bounds the maximum of the Shape and outline-cache phases before allocations.
// The resolver's already-owned Face is borrowed, not copied or retained; its decode budget is independent.
// Repeated outlines share a read-only Glyph within this returned Run and never with another run or the Face.
func LayoutOpenType(
	ctx context.Context, text string, style Style, resolver FontResolver,
	shapeOptions ShapeOptions, options ...LayoutOptions,
) (*Run, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, err := openTypeLayoutOptions(options)
	if err != nil {
		return nil, err
	}
	if !finite(style.Size) || style.Size <= 0 || !finite(style.Leading) || style.Leading < 0 ||
		!finite(style.Tracking) || !finite(limits.OriginX) || !finite(limits.OriginY) || !utf8.ValidString(text) {
		return nil, fontError(ErrMalformed, "layoutOpenType.style", 0, "invalid style, origin or UTF-8")
	}
	if style.Kerning || style.Tracking != 0 {
		return nil, fontError(ErrUnsupported, "layoutOpenType.style", 0,
			"use ShapeOptions for kerning and zero tracking")
	}
	if !openTypeCoordinate(limits.OriginX) || !openTypeCoordinate(limits.OriginY) {
		return nil, fontError(ErrLimit, "layoutOpenType.origin", 0, "origin exceeds supported coordinate precision")
	}
	const baseBytes = int64(1024)
	if limits.MaxWorkingBytes <= baseBytes || int64(len(text)) > limits.MaxWorkingBytes-baseBytes {
		return nil, fontError(ErrLimit, "layoutOpenType.text", 0, "text or layout metadata exceeds byte budget")
	}
	if resolver == nil || style.FontName == "" {
		return nil, ErrMissingFont
	}
	face, err := resolver.ResolveFont(ctx, style.FontName)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if face == nil {
		return nil, ErrMissingFont
	}
	if face.PostScriptName != "" && face.PostScriptName != style.FontName {
		return nil, fontError(ErrMissingFont, "layoutOpenType.font", 0, "resolver returned a different font")
	}
	if face.UnitsPerEm < 16 || face.UnitsPerEm > 16384 {
		return nil, fontError(ErrMalformed, "layoutOpenType.font", 0, "invalid font design-unit scale")
	}
	scale := style.Size / float64(face.UnitsPerEm)
	leading := style.Leading
	if leading == 0 {
		leading = float64(int32(face.Ascender)-int32(face.Descender)+int32(face.LineGap)) * scale
	}
	if !finite(scale) || scale <= 0 || !finite(leading) || leading <= 0 {
		return nil, fontError(ErrMalformed, "layoutOpenType.metrics", 0, "invalid scale or line height")
	}
	if !openTypeCoordinate(leading) {
		return nil, fontError(ErrLimit, "layoutOpenType.leading", 0, "line height exceeds supported precision")
	}
	if shapeOptions.MaxWorkingBytes < 0 || shapeOptions.MaxGlyphs < 0 {
		return nil, fontError(ErrLimit, "layoutOpenType.shape", 0, "shape limits must not be negative")
	}
	shapeBytes := shapeOptions.MaxWorkingBytes
	if shapeBytes == 0 {
		shapeBytes = 32 << 20
	}
	shapeOptions.MaxWorkingBytes = min(shapeBytes, limits.MaxWorkingBytes-baseBytes)
	shapeGlyphs := shapeOptions.MaxGlyphs
	if shapeGlyphs == 0 {
		shapeGlyphs = 10000
	}
	shapeOptions.MaxGlyphs = min(shapeGlyphs, limits.MaxGlyphs)
	shaped, err := Shape(ctx, face, text, shapeOptions)
	if err != nil {
		return nil, err
	}
	budget := openTypeLayoutBudget{limit: limits.MaxWorkingBytes, current: baseBytes,
		peak: baseBytes + shaped.WorkingBytes()}
	if err := budget.array(cap(shaped.Glyphs), int64(unsafe.Sizeof(ShapedGlyph{}))); err != nil {
		return nil, err
	}
	for _, placement := range shaped.Glyphs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if placement.InstancePosition != nil {
			if err := budget.reserve(int64(unsafe.Sizeof(InstancePosition{}))); err != nil {
				return nil, err
			}
		}
	}
	if err := budget.array(len(shaped.Glyphs), int64(unsafe.Sizeof(PositionedGlyph{}))); err != nil {
		return nil, err
	}
	result := &Run{FontName: style.FontName, Scale: scale, Leading: leading,
		Approximate: true, UTF16Units: shaped.UTF16Units, Glyphs: make([]PositionedGlyph, len(shaped.Glyphs))}
	cache := make(map[uint16]*Glyph)
	penX, penY := limits.OriginX, limits.OriginY
	ink := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for index, placement := range shaped.Glyphs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		glyph := cache[placement.GlyphID]
		if glyph == nil {
			// Preflight conservative map entry and bucket-growth storage before decoding or insertion.
			if err := budget.reserve(128); err != nil {
				return nil, err
			}
			glyphLimits := limits.GlyphLimits
			remaining := budget.limit - budget.current
			if remaining <= 0 {
				return nil, fontError(ErrLimit, "layoutOpenType.glyphCache", 0, "outline cache exceeds byte budget")
			}
			if glyphLimits.MaxWorkingBytes == 0 {
				glyphLimits.MaxWorkingBytes = 16 << 20
			}
			glyphLimits.MaxWorkingBytes = min(glyphLimits.MaxWorkingBytes, remaining)
			glyph, err = face.Glyph(ctx, placement.GlyphID, glyphLimits)
			if err != nil {
				return nil, err
			}
			// Glyph enforced the remaining total budget during decoding; only actual retained caps stay resident.
			budget.peak = max(budget.peak, budget.current+glyph.WorkingBytes())
			if err := openTypeRetainGlyph(ctx, &budget, glyph); err != nil {
				return nil, err
			}
			cache[placement.GlyphID] = glyph
		}
		x := penX + openTypeScaled(float64(placement.XOffset), scale)
		y := penY - openTypeScaled(float64(placement.YOffset), scale)
		advanceX := openTypeScaled(float64(placement.XAdvance), scale)
		advanceY := openTypeScaled(float64(placement.YAdvance), scale)
		if !openTypeCoordinate(x) || !openTypeCoordinate(y) ||
			!openTypeCoordinate(advanceX) || !openTypeCoordinate(advanceY) {
			return nil, fontError(ErrLimit, "layoutOpenType.position", placement.ClusterStart,
				"glyph placement or advance exceeds supported precision")
		}
		result.Glyphs[index] = PositionedGlyph{Rune: placement.Rune, ClusterStart: placement.ClusterStart,
			ClusterLength: placement.ClusterLength, X: x, Y: y, Advance: advanceX, Glyph: glyph}
		for _, contour := range glyph.Contours {
			for pointIndex, point := range contour {
				if pointIndex%256 == 0 && ctx.Err() != nil {
					return nil, ctx.Err()
				}
				px, py := x+openTypeScaled(point.X, scale), y-openTypeScaled(point.Y, scale)
				if !openTypeCoordinate(px) || !openTypeCoordinate(py) {
					return nil, fontError(ErrLimit, "layoutOpenType.bounds", placement.ClusterStart,
						"outline bounds exceed supported precision")
				}
				ink[0], ink[1], ink[2], ink[3] = min(ink[0], px), min(ink[1], py), max(ink[2], px), max(ink[3], py)
			}
		}
		penX, penY = penX+advanceX, penY-advanceY
		if !openTypeCoordinate(penX) || !openTypeCoordinate(penY) {
			return nil, fontError(ErrLimit, "layoutOpenType.advance", placement.ClusterStart,
				"pen position exceeds supported precision")
		}
	}
	result.AdvanceWidth = penX - limits.OriginX
	if !openTypeCoordinate(result.AdvanceWidth) {
		return nil, fontError(ErrLimit, "layoutOpenType.advance", 0, "net advance exceeds supported precision")
	}
	if !math.IsInf(ink[0], 1) {
		coordinateLimit := min(float64(int(^uint(0)>>1)-1), float64(1<<40))
		for _, value := range ink {
			if !finite(value) || math.Abs(value) > coordinateLimit {
				return nil, fontError(ErrLimit, "layoutOpenType.bounds", 0, "bounds exceed platform integer range")
			}
		}
		result.Bounds = image.Rect(int(math.Floor(ink[0])), int(math.Floor(ink[1])),
			int(math.Ceil(ink[2])), int(math.Ceil(ink[3])))
	}
	result.reservation = budget.peak
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func openTypeLayoutOptions(options []LayoutOptions) (LayoutOptions, error) {
	result := LayoutOptions{MaxWorkingBytes: 64 << 20, MaxGlyphs: 10000}
	if len(options) > 1 {
		return result, fontError(ErrMalformed, "layoutOpenType.options", 0, "provide at most one options value")
	}
	if len(options) == 0 {
		return result, nil
	}
	p := options[0]
	g := p.GlyphLimits
	if p.MaxWorkingBytes < 0 || p.MaxGlyphs < 0 || g.MaxWorkingBytes < 0 || g.MaxPoints < 0 ||
		g.MaxContours < 0 || g.MaxComponents < 0 || g.MaxDepth < 0 || g.MaxDepth > 256 || g.MaxOperations < 0 {
		return result, fontError(ErrLimit, "layoutOpenType.options", 0, "negative limits or depth above 256")
	}
	if p.MaxWorkingBytes != 0 {
		result.MaxWorkingBytes = p.MaxWorkingBytes
	}
	if p.MaxGlyphs != 0 {
		result.MaxGlyphs = p.MaxGlyphs
	}
	result.OriginX, result.OriginY, result.GlyphLimits = p.OriginX, p.OriginY, p.GlyphLimits
	return result, nil
}

type openTypeLayoutBudget struct{ limit, current, peak int64 }

func (b *openTypeLayoutBudget) reserve(bytes int64) error {
	if bytes < 0 || bytes > b.limit-b.current {
		return fontError(ErrLimit, "layoutOpenType.memory", 0, "owned layout exceeds byte budget")
	}
	b.current += bytes
	b.peak = max(b.peak, b.current)
	return nil
}

func (b *openTypeLayoutBudget) array(count int, bytes int64) error {
	if count < 0 || bytes <= 0 || int64(count) > (b.limit-b.current)/bytes {
		return fontError(ErrLimit, "layoutOpenType.memory", 0, "owned array exceeds byte budget")
	}
	return b.reserve(int64(count) * bytes)
}

func openTypeRetainGlyph(ctx context.Context, b *openTypeLayoutBudget, g *Glyph) error {
	if err := openTypeRetainCFFHints(ctx, b, g.CFFHints); err != nil {
		return err
	}
	if g.InstanceMetrics != nil {
		if err := b.reserve(int64(unsafe.Sizeof(InstanceGlyphMetrics{}))); err != nil {
			return err
		}
	}
	if err := b.reserve(int64(unsafe.Sizeof(Glyph{}))); err != nil {
		return err
	}
	if err := b.array(cap(g.Contours), int64(unsafe.Sizeof([]Point{}))); err != nil {
		return err
	}
	for _, contour := range g.Contours {
		if err := b.array(cap(contour), int64(unsafe.Sizeof(Point{}))); err != nil {
			return err
		}
	}
	if err := b.array(cap(g.Path), int64(unsafe.Sizeof(Command{}))); err != nil {
		return err
	}
	if err := b.array(cap(g.ComponentSources), int64(unsafe.Sizeof(Span{}))); err != nil {
		return err
	}
	return b.array(cap(g.Instructions), int64(unsafe.Sizeof(Span{})))
}

func openTypeCoordinate(value float64) bool { return finite(value) && math.Abs(value) <= 1<<40 }

func openTypeScaled(value, scale float64) float64 { return float64(value * scale) }
