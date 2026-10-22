package psdfont

import (
	"context"
	"image"
	"math"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// Style supplies one explicit font and pixel-size character style.
// Tracking is in thousandths of an em. Zero Leading derives the font's hhea line height.
type Style struct {
	FontName                string
	Size, Leading, Tracking float64
	Kerning                 bool
}

// PositionedGlyph retains the original UTF-16 cluster range and baseline position.
// A LineBreak has no Glyph; CRLF is one break with ClusterLength=2.
// Repeated glyph outlines may share an owned, read-only Glyph inside this run.
type PositionedGlyph struct {
	Rune                        rune
	ClusterStart, ClusterLength int64
	X, Y, Advance               float64
	Glyph                       *Glyph
	LineBreak                   bool
}

// Run owns an approximate unhinted layout without font substitution or complex shaping.
// Bounds covers design-space ink after scaling, not a Photoshop text-box or warp bound.
type Run struct {
	FontName       string
	Glyphs         []PositionedGlyph
	Scale, Leading float64
	AdvanceWidth   float64
	UTF16Units     int64
	Bounds         image.Rectangle
	Approximate    bool
	reservation    int64
}

// WorkingBytes reports the conservative allocation peak recorded when Layout or LayoutOpenType produced this run.
// Transient shaping and outline decoding are included where applicable; the borrowed Face is excluded.
// It covers the returned owned storage, but does not recompute memory after callers mutate public fields.
// A nil run or a caller-constructed run has no recorded reservation and returns zero.
func (r *Run) WorkingBytes() int64 {
	if r == nil {
		return 0
	}
	return r.reservation
}

// LayoutOptions bounds character traversal, owned glyph caches and positioned-run metadata.
// Zero selects 64 MiB and 10000 clusters. OriginX/OriginY is the first baseline in pixel coordinates.
type LayoutOptions struct {
	OriginX, OriginY float64
	MaxWorkingBytes  int64
	MaxGlyphs        int
	GlyphLimits      GlyphOptions
}

// Layout places a single-style Latin run using explicit font resolution and legacy kern pairs.
// It rejects combining marks, bidirectional text, tabs, complex scripts, GSUB shaping requests
// and missing characters. Native hinting, fractional Photoshop spacing and text warps are not reproduced.
func Layout(
	ctx context.Context, text string, style Style, resolver FontResolver, options ...LayoutOptions,
) (*Run, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits := LayoutOptions{MaxWorkingBytes: 64 << 20, MaxGlyphs: 10000}
	if len(options) > 1 {
		return nil, fontError(ErrMalformed, "layout.options", 0, "provide at most one options value")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxWorkingBytes < 0 || provided.MaxGlyphs < 0 {
			return nil, fontError(ErrLimit, "layout.options", 0, "limits must not be negative")
		}
		if provided.MaxWorkingBytes != 0 {
			limits.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxGlyphs != 0 {
			limits.MaxGlyphs = provided.MaxGlyphs
		}
		limits.OriginX, limits.OriginY, limits.GlyphLimits = provided.OriginX, provided.OriginY, provided.GlyphLimits
	}
	if !finite(style.Size) || style.Size <= 0 || !finite(style.Leading) || style.Leading < 0 ||
		!finite(style.Tracking) || !finite(limits.OriginX) || !finite(limits.OriginY) || !utf8.ValidString(text) {
		return nil, fontError(ErrMalformed, "layout.style", 0, "invalid style, origin or UTF-8 text")
	}
	if int64(len(text))+1024 > limits.MaxWorkingBytes {
		return nil, fontError(ErrLimit, "layout.text", 0, "text traversal exceeds MaxWorkingBytes")
	}
	count := 0
	for index := 0; index < len(text); {
		if count >= limits.MaxGlyphs {
			return nil, fontError(ErrLimit, "layout.clusters", int64(index), "cluster count exceeds MaxGlyphs")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		character, width := utf8.DecodeRuneInString(text[index:])
		if character != '\n' && character != '\r' &&
			(!basicLatin(character) || unicode.Is(unicode.Cf, character)) {
			return nil, fontError(ErrUnsupported, "layout.script", int64(index),
				"character requires unsupported shaping or layout")
		}
		index += width
		if character == '\r' && index < len(text) && text[index] == '\n' {
			index++
		}
		count++
	}
	if resolver == nil || style.FontName == "" {
		return nil, ErrMissingFont
	}
	face, err := resolver.ResolveFont(ctx, style.FontName)
	if err != nil {
		return nil, err
	}
	if face == nil {
		return nil, ErrMissingFont
	}
	if face.PostScriptName != "" && face.PostScriptName != style.FontName {
		return nil, fontError(ErrMissingFont, "layout.font", 0, "resolver returned a different PostScript font")
	}
	if face.UnitsPerEm < 16 || face.UnitsPerEm > 16384 {
		return nil, fontError(ErrMalformed, "layout.font", 0, "font has an invalid design-unit scale")
	}
	reservation := int64(len(text)) + 1024 + int64(count)*int64(unsafe.Sizeof(PositionedGlyph{})+96)
	if reservation > limits.MaxWorkingBytes || uint64(count) > uint64(^uint(0)>>1) {
		return nil, fontError(ErrLimit, "layout.clusters", 0, "owned positioned glyphs exceed MaxWorkingBytes")
	}
	result := &Run{FontName: style.FontName, Scale: style.Size / float64(face.UnitsPerEm),
		Leading: style.Leading, Approximate: true, Glyphs: make([]PositionedGlyph, 0, count)}
	if result.Leading == 0 {
		result.Leading = float64(int32(face.Ascender)-int32(face.Descender)+int32(face.LineGap)) * result.Scale
	}
	if !finite(result.Leading) || result.Leading <= 0 {
		return nil, fontError(ErrMalformed, "layout.leading", 0, "font line height is not positive and finite")
	}
	if math.Abs(limits.OriginX) > 1<<40 || math.Abs(limits.OriginY) > 1<<40 || result.Leading > 1<<40 {
		return nil, fontError(ErrLimit, "layout.origin", 0, "baseline or leading exceeds supported coordinate precision")
	}
	cache := make(map[uint16]*Glyph)
	penX, penY, cluster := limits.OriginX, limits.OriginY, int64(0)
	previous, hasPrevious := uint16(0), false
	instanceLines, completedLine := face.cff2 != nil && face.variation != nil && len(face.variation.axes) != 0, false
	ink := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	for position := 0; position < len(text); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		character, width := utf8.DecodeRuneInString(text[position:])
		position += width
		clusterLength := int64(1)
		if character > 0xffff {
			clusterLength++
		}
		if character == '\r' || character == '\n' {
			if character == '\r' && position < len(text) && text[position] == '\n' {
				position++
				clusterLength++
			}
			result.Glyphs = append(result.Glyphs, PositionedGlyph{Rune: character, ClusterStart: cluster,
				ClusterLength: clusterLength, X: penX, Y: penY, LineBreak: true})
			if instanceLines && !completedLine {
				result.AdvanceWidth = penX - limits.OriginX
			} else {
				result.AdvanceWidth = max(result.AdvanceWidth, penX-limits.OriginX)
			}
			completedLine = true
			penX, penY, hasPrevious = limits.OriginX, penY+result.Leading, false
			if !finite(penY) || math.Abs(penY) > 1<<40 {
				return nil, fontError(ErrLimit, "layout.position", cluster, "line baselines exceed supported precision")
			}
			cluster += clusterLength
			continue
		}
		id, err := face.LookupRune(ctx, character)
		if err != nil {
			return nil, err
		}
		if hasPrevious && style.Kerning {
			adjustment, err := face.Kerning(ctx, previous, id)
			if err != nil {
				return nil, err
			}
			penX += float64(adjustment) * result.Scale
		}
		glyph := cache[id]
		if glyph == nil {
			glyphLimits := limits.GlyphLimits
			remaining := limits.MaxWorkingBytes - reservation
			if remaining <= 0 {
				return nil, fontError(ErrLimit, "layout.glyphCache", 0, "owned glyph cache exceeds MaxWorkingBytes")
			}
			if glyphLimits.MaxWorkingBytes == 0 {
				glyphLimits.MaxWorkingBytes = min(16<<20, remaining)
			} else {
				glyphLimits.MaxWorkingBytes = min(glyphLimits.MaxWorkingBytes, remaining)
			}
			glyph, err = face.Glyph(ctx, id, glyphLimits)
			if err != nil {
				return nil, err
			}
			reservation += glyph.WorkingBytes()
			cache[id] = glyph
		}
		designAdvance := float64(glyph.AdvanceWidth)
		if glyph.InstanceMetrics != nil {
			designAdvance = math.Floor(glyph.InstanceMetrics.AdvanceWidth + .5)
		}
		advance := designAdvance*result.Scale + style.Tracking*style.Size/1000
		if !finite(penX) || !finite(penY) || !finite(advance) || math.Abs(penX) > 1<<40 || math.Abs(penY) > 1<<40 {
			return nil, fontError(ErrLimit, "layout.position", cluster, "layout exceeds supported coordinate precision")
		}
		result.Glyphs = append(result.Glyphs, PositionedGlyph{Rune: character, ClusterStart: cluster,
			ClusterLength: clusterLength, X: penX, Y: penY, Advance: advance, Glyph: glyph})
		for _, contour := range glyph.Contours {
			for _, point := range contour {
				x, y := penX+point.X*result.Scale, penY-point.Y*result.Scale
				ink[0], ink[1], ink[2], ink[3] = min(ink[0], x), min(ink[1], y), max(ink[2], x), max(ink[3], y)
			}
		}
		penX += advance
		if !finite(penX) || math.Abs(penX) > 1<<40 {
			return nil, fontError(ErrLimit, "layout.advance", cluster, "advance exceeds supported coordinate precision")
		}
		cluster += clusterLength
		previous, hasPrevious = id, true
	}
	if instanceLines && !completedLine {
		result.AdvanceWidth = penX - limits.OriginX
	} else {
		result.AdvanceWidth = max(result.AdvanceWidth, penX-limits.OriginX)
	}
	result.UTF16Units, result.reservation = cluster, reservation
	if !math.IsInf(ink[0], 1) {
		for _, value := range ink {
			if !finite(value) || math.Abs(value) > min(float64(int(^uint(0)>>1)-1), float64(1<<40)) {
				return nil, fontError(ErrLimit, "layout.bounds", 0, "ink bounds exceed platform or precision limits")
			}
		}
		result.Bounds = image.Rect(int(math.Floor(ink[0])), int(math.Floor(ink[1])),
			int(math.Ceil(ink[2])), int(math.Ceil(ink[3])))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func basicLatin(character rune) bool {
	return character >= 32 && character <= 126 || unicode.In(character, unicode.Latin) ||
		character >= 0xa0 && character <= 0xff || character >= 0x2010 && character <= 0x2027
}
