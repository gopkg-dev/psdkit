package psdscene

import (
	"context"
	"math"
	"unicode/utf8"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

type openTypeLaidOutSpan struct {
	source openTypeSourceSpan
	run    *psdfont.Run
}

// Independent style shaping is explicit research geometry pending native cross-boundary qualification.
// Font/size changes retain their own scales; all returned glyph clusters use original document UTF16 positions.
func layoutOpenTypeIndependentSpans(
	ctx context.Context, spans []openTypeSourceSpan, resolver psdfont.FontResolver,
	shape psdfont.ShapeOptions, maxGlyphs int, budget int64,
) ([]openTypeLaidOutSpan, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if maxGlyphs <= 0 || budget < 256 {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0, "invalid style layout budgets")
	}
	if shape.MaxGlyphs < 0 || shape.MaxNodes < 0 || shape.MaxLookupVisits < 0 || shape.MaxWorkingBytes < 0 {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0, "invalid shared shaping limits")
	}
	if shape.MaxGlyphs > 0 {
		maxGlyphs = min(maxGlyphs, shape.MaxGlyphs)
	}
	entryBytes := int64(unsafe.Sizeof(openTypeLaidOutSpan{}))
	if int64(len(spans)) > (budget-256)/entryBytes {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0, "owned span layout index exceeds budget")
	}
	count := 0
	active := 0
	for _, span := range spans {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if !utf8.ValidString(span.text) {
			return nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.spanLayout", 0, "invalid UTF8 style text")
		}
		characters := utf8.RuneCountInString(span.text)
		if characters > maxGlyphs-count {
			return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0, "whole text glyph count exceeds limit")
		}
		count += characters
		if characters != 0 {
			active++
		}
	}
	// Independent font parser/lookup phases share fixed portions of the whole-layer shaping work ceilings.
	nodes, visits := shape.MaxNodes, shape.MaxLookupVisits
	if nodes == 0 {
		nodes = 200000
	}
	if visits == 0 {
		visits = 1000000
	}
	shape.MaxNodes, shape.MaxLookupVisits = nodes/max(1, active), visits/max(1, active)
	if shape.MaxNodes <= 0 || shape.MaxLookupVisits <= 0 {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0,
			"whole-layer shaping work cannot cover every nonempty style group")
	}
	retained := 256 + int64(len(spans))*entryBytes
	result := make([]openTypeLaidOutSpan, len(spans))
	pen := float64(0)
	actualGlyphs := 0
	for index, span := range spans {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		result[index].source = span
		if span.text == "" {
			continue
		}
		available := budget - retained
		if available <= 0 {
			return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0, "no glyph layout budget remains")
		}
		run, err := psdfont.LayoutOpenType(ctx, span.text, span.style, resolver, shape,
			psdfont.LayoutOptions{OriginX: pen, MaxWorkingBytes: available, MaxGlyphs: maxGlyphs})
		if err != nil {
			return nil, 0, err
		}
		if run.WorkingBytes() > available {
			return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0, "glyph peak exceeds remaining budget")
		}
		if len(run.Glyphs) > maxGlyphs-actualGlyphs {
			return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0,
				"whole text shaped glyph count exceeds limit")
		}
		actualGlyphs += len(run.Glyphs)
		retained += run.WorkingBytes()
		for glyph := range run.Glyphs {
			if glyph%1024 == 0 {
				if err := ctx.Err(); err != nil {
					return nil, 0, err
				}
			}
			run.Glyphs[glyph].ClusterStart += span.start
		}
		pen += run.AdvanceWidth
		if math.IsNaN(pen) || math.IsInf(pen, 0) || math.Abs(pen) > 1<<40 {
			return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.spanLayout", 0,
				"style run advance exceeds coordinate precision")
		}
		result[index].run = run
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return result, retained, nil
}
