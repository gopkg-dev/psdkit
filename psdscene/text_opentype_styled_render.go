package psdscene

import (
	"context"
	"image"
	"math"
	"sort"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

type openTypePaintRange struct {
	run        *psdfont.Run
	begin, end int
	color      [4]float64
	bounds     image.Rectangle
}

func renderOpenTypeStyledMetadata(
	ctx context.Context, metadata *psdtext.Metadata, text string, transform [6]float64,
	resolver psdfont.FontResolver, options OpenTypeTextRenderOptions, bounds image.Rectangle, budget int64,
) (*image.NRGBA, error) {
	spans, spanBytes, err := openTypeTextSpans(ctx, metadata, text, budget)
	if err != nil {
		return nil, err
	}
	budget -= spanBytes
	layoutSpans, groupBytes, err := openTypeStyleGroups(ctx, spans, text, options.StyleBoundaries, budget)
	if err != nil {
		return nil, err
	}
	laidOut, layoutBytes, err := layoutOpenTypeIndependentSpans(ctx, layoutSpans, resolver, options.Shape,
		options.MaxGlyphs, budget-groupBytes)
	if err != nil {
		return nil, err
	}
	budget -= groupBytes + layoutBytes
	glyphCount := 0
	for _, span := range laidOut {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if span.run != nil {
			if len(span.run.Glyphs) > options.MaxGlyphs-glyphCount {
				return nil, sceneError(psdfile.ErrLimit, "textOpenType.stylePaint", 0,
					"whole text shaped glyph count exceeds limit")
			}
			glyphCount += len(span.run.Glyphs)
		}
	}
	paintEntryBytes := int64(unsafe.Sizeof(openTypePaintRange{}))
	if int64(glyphCount) > budget/paintEntryBytes {
		return nil, sceneError(psdfile.ErrLimit, "textOpenType.stylePaint", 0, "glyph color index exceeds budget")
	}
	paintBytes := int64(glyphCount) * paintEntryBytes
	paints := make([]openTypePaintRange, 0, glyphCount)
	// Monotone disjoint clusters visit a source span at most once per glyph plus each crossed source boundary.
	colorVisits := int64(len(spans)) + int64(glyphCount)
	glyphUnits := metadata.UTF16Units - 1
	for _, span := range laidOut {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if span.run == nil {
			continue
		}
		previousEnd := span.source.start
		groupEnd := min(glyphUnits, span.source.start+span.source.length)
		for index, glyph := range span.run.Glyphs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if glyph.ClusterStart < previousEnd || glyph.ClusterStart > groupEnd || glyph.ClusterLength <= 0 ||
				glyph.ClusterLength > groupEnd-glyph.ClusterStart {
				return nil, sceneError(psdfile.ErrMalformed, "textOpenType.styleColor", 0,
					"glyph clusters must be monotone, disjoint and contained in their typography group")
			}
			previousEnd = glyph.ClusterStart + glyph.ClusterLength
			color, err := openTypeClusterColor(ctx, spans, glyph.ClusterStart, glyph.ClusterLength,
				options.ClusterPaint, &colorVisits)
			if err != nil {
				return nil, err
			}
			if len(paints) > 0 && paints[len(paints)-1].run == span.run && paints[len(paints)-1].color == color {
				paints[len(paints)-1].end = index + 1
			} else {
				paints = append(paints, openTypePaintRange{run: span.run, begin: index, end: index + 1, color: color})
			}
		}
	}
	budget -= paintBytes
	visiblePaints := int64(0)
	const maxLayerWork int64 = 100_000_000
	remainingPaintWork := maxLayerWork
	for index := range paints {
		paint := &paints[index]
		ink, err := openTypePaintBounds(ctx, paint, transform)
		if err != nil {
			return nil, err
		}
		paint.bounds = ink.Intersect(bounds)
		if paint.bounds.Empty() {
			continue
		}
		width, height := int64(paint.bounds.Dx()), int64(paint.bounds.Dy())
		if width > remainingPaintWork/height {
			return nil, sceneError(psdfile.ErrLimit, "textOpenType.stylePaint", 0,
				"cumulative painted pixel work exceeds whole-layer budget")
		}
		remainingPaintWork -= width * height
		visiblePaints++
	}
	// Each visible raster owns one fixed share; even adversarial color alternation cannot reset the layer ceiling.
	scanlineShare := maxLayerWork / max(int64(1), visiblePaints)
	result := image.NewNRGBA(bounds)
	for _, paint := range paints {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if paint.bounds.Empty() {
			continue
		}
		subset := *paint.run
		subset.Glyphs = paint.run.Glyphs[paint.begin:paint.end:paint.end]
		transformed, affineBytes, err := textAffineRun(ctx, &subset, transform, budget)
		if err != nil {
			return nil, err
		}
		rasterBytes := budget - affineBytes
		if rasterBytes <= 0 {
			return nil, sceneError(psdfile.ErrLimit, "textOpenType.styleRaster", 0, "no coverage budget remains")
		}
		coverage, err := psdfont.RasterizeRun(ctx, transformed, paint.bounds,
			psdfont.RasterOptions{Samples: options.Samples, MaxWorkingBytes: rasterBytes,
				MaxScanlineWork: scanlineShare})
		if err != nil {
			return nil, err
		}
		for row := 0; row < paint.bounds.Dy(); row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := 0; column < paint.bounds.Dx(); column++ {
				if column%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				alpha := uint32(math.Round(float64(coverage.Pix[row*coverage.Stride+column]) * paint.color[0]))
				if alpha == 0 {
					continue
				}
				at := (row+paint.bounds.Min.Y-bounds.Min.Y)*result.Stride +
					(column+paint.bounds.Min.X-bounds.Min.X)*4
				backAlpha := uint32(result.Pix[at+3])
				combined := alpha*255 + backAlpha*(255-alpha)
				for channel := range 3 {
					source := uint32(textByte(paint.color[channel+1]))
					value := source*alpha*255 + uint32(result.Pix[at+channel])*backAlpha*(255-alpha)
					result.Pix[at+channel] = byte((value + combined/2) / combined)
				}
				result.Pix[at+3] = byte((combined + 127) / 255)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func openTypeStyleGroups(
	ctx context.Context, spans []openTypeSourceSpan, text string, policy OpenTypeStyleBoundaryPolicy, budget int64,
) ([]openTypeSourceSpan, int64, error) {
	if policy != OpenTypeCompatibleStyleRuns {
		return spans, 0, nil
	}
	entryBytes := int64(unsafe.Sizeof(openTypeSourceSpan{}))
	if int64(len(spans)) > budget/entryBytes {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.styleGroups", 0, "style group index exceeds budget")
	}
	groups := make([]openTypeSourceSpan, 0, len(spans))
	for _, span := range spans {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if len(groups) > 0 && groups[len(groups)-1].style == span.style {
			previous := &groups[len(groups)-1]
			previous.text = text[previous.byteStart:span.byteEnd]
			previous.byteEnd = span.byteEnd
			previous.length = span.start + span.length - previous.start
		} else {
			groups = append(groups, span)
		}
	}
	return groups, int64(cap(groups)) * entryBytes, nil
}

func openTypeClusterColor(
	ctx context.Context, spans []openTypeSourceSpan, start, length int64,
	policy OpenTypeClusterPaintPolicy, visits *int64,
) ([4]float64, error) {
	var color [4]float64
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return color, err
	}
	if policy > OpenTypeFirstStyleClusterPaint {
		return color, sceneError(psdfile.ErrUnsupported, "textOpenType.styleColor", 0, "unknown cluster paint policy")
	}
	found, mixed := false, false
	if start < 0 || length <= 0 || length > math.MaxInt64-start {
		return color, sceneError(psdfile.ErrMalformed, "textOpenType.styleColor", 0, "invalid glyph UTF16 cluster")
	}
	end := start + length
	first := sort.Search(len(spans), func(index int) bool { return spans[index].start+spans[index].length > start })
	covered := start
	for index := first; index < len(spans) && spans[index].start < end; index++ {
		if err := ctx.Err(); err != nil {
			return [4]float64{}, err
		}
		if visits == nil || *visits <= 0 {
			return [4]float64{}, sceneError(psdfile.ErrLimit, "textOpenType.styleColor", 0,
				"whole-layer cluster style traversal exceeds work allowance")
		}
		*visits -= 1
		span := spans[index]
		if span.length <= 0 || span.start < 0 || span.length > math.MaxInt64-span.start ||
			!found && span.start > start || found && span.start != covered {
			return [4]float64{}, sceneError(psdfile.ErrMalformed, "textOpenType.styleColor", 0,
				"glyph cluster source styles have a gap, overlap or invalid range")
		}
		covered = span.start + span.length
		if !found {
			color = span.color
			found = true
		} else if color != span.color {
			mixed = true
		}
	}
	if !found || first >= len(spans) || spans[first].start > start || covered < end {
		return color, sceneError(psdfile.ErrMalformed, "textOpenType.styleColor", 0, "glyph cluster has no source style")
	}
	if mixed && policy == OpenTypeRejectMixedClusterPaint {
		return [4]float64{}, sceneError(psdfile.ErrUnsupported, "textOpenType.styleColor", 0,
			"glyph ligature cluster spans different colors; select an explicit cluster paint policy")
	}
	if err := ctx.Err(); err != nil {
		return [4]float64{}, err
	}
	return color, nil
}
