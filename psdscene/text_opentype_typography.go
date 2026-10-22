package psdscene

import (
	"context"
	"image"
	"math"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

type openTypeTypography struct {
	style         psdfont.Style
	color         [4]float64
	fauxBold      bool
	allCaps       bool
	fauxAdvance   float64
	preserveHints bool
}

type openTypePointLine struct {
	text          string
	start, length int64
	justification int64
	leading       float64
}

// The terminal engine CR covers metadata only; source paragraph markers still create real empty lines.
func openTypeTypographyLines(
	ctx context.Context, metadata *psdtext.Metadata, text string, budget int64,
) (openTypeTypography, []openTypePointLine, int64, error) {
	var typography openTypeTypography
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return typography, nil, 0, err
	}
	if metadata == nil || metadata.Text != text+"\r" || len(metadata.Styles) != 1 ||
		metadata.Styles[0].Start != 0 || metadata.Styles[0].Length != metadata.UTF16Units ||
		len(metadata.Paragraphs) == 0 {
		return typography, nil, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.typography", 0,
			"one complete character style and matching paragraph editor text are required")
	}
	if err := openTypeTypographyEngine(metadata.Engine); err != nil {
		return typography, nil, 0, err
	}
	typography, err := openTypeTypographyStyle(metadata)
	if err != nil {
		return typography, nil, 0, err
	}
	entryBytes := int64(unsafe.Sizeof(openTypePointLine{}))
	if budget < 256 || int64(len(metadata.Paragraphs)) > (budget-256)/entryBytes {
		return typography, nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.typography", 0,
			"paragraph line index exceeds byte budget")
	}
	reserved := 256 + int64(len(metadata.Paragraphs))*entryBytes
	lines := make([]openTypePointLine, 0, len(metadata.Paragraphs))
	byteBegin, unitBegin, units := 0, int64(0), int64(0)
	for offset := 0; offset < len(metadata.Text); {
		if offset%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return typography, nil, 0, err
			}
		}
		character, width := utf8.DecodeRuneInString(metadata.Text[offset:])
		if character == utf8.RuneError && width == 1 {
			return typography, nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.typography", 0,
				"invalid UTF8 editor text")
		}
		units++
		if character > 0xffff {
			units++
		}
		endByte := offset
		offset += width
		if character != '\r' && character != '\n' {
			continue
		}
		if character == '\r' && offset < len(metadata.Text) && metadata.Text[offset] == '\n' {
			offset++
			units++
		}
		if len(lines) >= len(metadata.Paragraphs) {
			return typography, nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.paragraph", 0,
				"paragraph ranges do not cover the explicit line breaks")
		}
		paragraph := metadata.Paragraphs[len(lines)]
		if paragraph.Start != unitBegin || paragraph.Length != units-unitBegin {
			return typography, nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.paragraph", 0,
				"paragraph ranges must match complete UTF16 lines including their break")
		}
		justification, ratio, err := openTypePointParagraph(paragraph)
		if err != nil {
			return typography, nil, 0, err
		}
		leading := typography.style.Leading
		if *metadata.Styles[0].Style.AutoLeading {
			if paragraph.Style.AutoLeading == nil {
				return typography, nil, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.leading", 0,
					"automatic leading requires an explicit paragraph ratio")
			}
			leading = typography.style.Size * ratio
		}
		if !textFinite(leading) || leading <= 0 || leading > 1<<40 {
			return typography, nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.leading", 0,
				"line leading exceeds supported coordinate precision")
		}
		lines = append(lines, openTypePointLine{text: metadata.Text[byteBegin:endByte],
			start: unitBegin, length: units - unitBegin, justification: justification, leading: leading})
		byteBegin, unitBegin = offset, units
	}
	if byteBegin != len(metadata.Text) || units != metadata.UTF16Units || len(lines) != len(metadata.Paragraphs) {
		return typography, nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.paragraph", 0,
			"paragraph coverage or terminal engine marker is inconsistent")
	}
	return typography, lines, reserved, nil
}

func openTypeTypographyEngine(engine *psdtext.Node) error {
	if engine == nil || engine.Kind != psdtext.Dictionary {
		return sceneError(psdfile.ErrUnsupported, "textOpenType.engine", 0,
			"explicit engine dictionary is required")
	}
	if err := uniqueOpenTypeTypographyProperties(engine); err != nil {
		return err
	}
	if node := engine.Lookup("UseFractionalGlyphWidths"); node != nil &&
		(node.Kind != psdtext.Boolean || !node.Bool) {
		return sceneError(psdfile.ErrUnsupported, "textOpenType.fractionalWidths", node.Span.Offset,
			"point typography requires fractional glyph widths")
	}
	grid := engine.Lookup("GridInfo")
	if grid != nil {
		if grid.Kind != psdtext.Dictionary {
			return sceneError(psdfile.ErrMalformed, "textOpenType.grid", grid.Span.Offset,
				"grid settings must be an explicit dictionary")
		}
		if err := uniqueOpenTypeTypographyProperties(grid); err != nil {
			return err
		}
	}
	for _, key := range []string{"GridIsOn", "AlignLineHeightToGridFlags"} {
		if node := grid.Lookup(key); node != nil && (node.Kind != psdtext.Boolean || node.Bool) {
			return sceneError(psdfile.ErrUnsupported, "textOpenType.grid", node.Span.Offset,
				"grid-aligned point typography is unsupported")
		}
	}
	if node := engine.Lookup("AntiAlias"); node != nil {
		value, ok := node.Int64()
		if !ok || value != 4 {
			return sceneError(psdfile.ErrUnsupported, "textOpenType.engineAntialias", node.Span.Offset,
				"unknown engine antialias metadata")
		}
	}
	return nil
}

func openTypeTypographyStyle(metadata *psdtext.Metadata) (openTypeTypography, error) {
	var result openTypeTypography
	style := metadata.Styles[0].Style
	if style.Data == nil || style.Data.Kind != psdtext.Dictionary || style.Font == nil || *style.Font < 0 ||
		*style.Font >= int64(len(metadata.Fonts)) || style.FontSize == nil || style.Leading == nil ||
		style.Tracking == nil || style.AutoLeading == nil || style.FauxBold == nil || style.AutoKerning == nil ||
		style.FillColor == nil || style.FillColor.Type == nil || *style.FillColor.Type != 1 ||
		len(style.FillColor.Values) != 4 {
		return result, sceneError(psdfile.ErrUnsupported, "textOpenType.typographyStyle", 0,
			"explicit font, size, leading, tracking, faux bold and RGB fill are required")
	}
	if err := uniqueOpenTypeTypographyProperties(style.Data); err != nil {
		return result, err
	}
	if !textFinite(*style.FontSize) || *style.FontSize <= 0 || !textFinite(*style.Leading) ||
		*style.Leading <= 0 || !textFinite(*style.Tracking) {
		return result, sceneError(psdfile.ErrMalformed, "textOpenType.typographyStyle", 0,
			"nonfinite or nonpositive typography metrics")
	}
	font := metadata.Fonts[*style.Font]
	if font.Data != nil {
		if err := uniqueOpenTypeTypographyProperties(font.Data); err != nil {
			return result, err
		}
	}
	if font.Name == "" || font.Synthetic != nil && *font.Synthetic != 0 {
		return result, sceneError(psdfile.ErrUnsupported, "textOpenType.font", 0,
			"synthetic or unnamed font is unsupported")
	}
	for _, key := range []string{"FauxItalic", "HorizontalScale", "VerticalScale", "Kerning", "BaselineShift",
		"FontCaps", "FontBaseline", "Underline", "Strikethrough", "Ligatures", "DLigatures", "NoBreak"} {
		if style.Data.Lookup(key) == nil {
			return result, sceneError(psdfile.ErrUnsupported, "textOpenType.typographyStyle", 0,
				"inherited or incomplete character properties are unsupported")
		}
	}
	for _, entry := range style.Data.Entries {
		switch entry.Key {
		case "Font", "FontSize", "Leading", "Tracking", "AutoLeading", "FauxBold", "AutoKerning", "FillColor", "StrokeColor":
			continue
		case "FontCaps":
			caps, ok := entry.Value.Int64()
			if !ok || caps != 0 && caps != 2 {
				return result, sceneError(psdfile.ErrUnsupported, "textOpenType.caps", style.Data.Span.Offset,
					"only regular or all-capital source text is supported")
			}
			result.allCaps = caps == 2
		case "Ligatures", "DLigatures":
			if entry.Value == nil || entry.Value.Kind != psdtext.Boolean {
				return result, sceneError(psdfile.ErrUnsupported, "textOpenType.typographyStyle", 0,
					"source feature projection must be Boolean")
			}
		default:
			if err := textDefault(entry.Key, entry.Value, textCharacterNumbers, textCharacterBooleans, nil); err != nil {
				return result, err
			}
		}
	}
	for index, value := range style.FillColor.Values {
		if !textFinite(value) || value < 0 || value > 1 {
			return result, sceneError(psdfile.ErrUnsupported, "textOpenType.color", 0, "RGB fill requires unit values")
		}
		result.color[index] = value
	}
	result.style = psdfont.Style{FontName: font.Name, Size: *style.FontSize, Leading: *style.Leading,
		Tracking: *style.Tracking}
	result.fauxBold = *style.FauxBold
	return result, nil
}

func openTypePointParagraph(paragraph psdtext.ParagraphRun) (int64, float64, error) {
	properties := paragraph.Style.Data
	if properties == nil || properties.Kind != psdtext.Dictionary || paragraph.Style.Justification == nil {
		return 0, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.paragraph", 0,
			"explicit point paragraph properties are required")
	}
	if err := uniqueOpenTypeTypographyProperties(properties); err != nil {
		return 0, 0, err
	}
	for _, key := range []string{"FirstLineIndent", "StartIndent", "EndIndent", "SpaceBefore", "SpaceAfter"} {
		if properties.Lookup(key) == nil {
			return 0, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.paragraph", 0,
				"inherited or incomplete point paragraph properties are unsupported")
		}
	}
	justification, ratio := *paragraph.Style.Justification, 1.75
	if justification != 0 && justification != 2 {
		return 0, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.justification", 0,
			"point text requires left or center justification")
	}
	for _, entry := range properties.Entries {
		switch entry.Key {
		case "Justification":
			continue
		case "AutoLeading":
			value, ok := entry.Value.Float64()
			if !ok || !textFinite(value) || value <= 0 || value > 10 {
				return 0, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.leading", 0,
					"automatic leading requires a finite positive ratio at most ten")
			}
			ratio = value
		case "AutoHyphenate", "EveryLineComposer", "Burasagari":
			// These flow flags cannot change a no-wrap Latin point-text line.
			if entry.Value == nil || entry.Value.Kind != psdtext.Boolean {
				return 0, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.paragraph", 0,
					"point paragraph flow flags must be Boolean")
			}
		case "LeadingType":
			value, ok := entry.Value.Int64()
			if !ok || value != 0 && value != 1 {
				return 0, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.paragraph", 0,
					"unknown point paragraph leading type")
			}
		default:
			if err := textDefault(entry.Key, entry.Value, textParagraphNumbers,
				textParagraphBooleans, textParagraphArrays); err != nil {
				return 0, 0, err
			}
		}
	}
	adjustments := paragraph.Data.Lookup("Adjustments")
	if adjustments != nil && (!textArray(adjustments.Lookup("Axis"), []float64{1, 0, 1}) ||
		!textArray(adjustments.Lookup("XY"), []float64{0, 0}) || len(adjustments.Entries) != 2) {
		return 0, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.paragraphAdjustments", 0,
			"paragraph axes or position adjustments are unsupported")
	}
	return justification, ratio, nil
}

func uniqueOpenTypeTypographyProperties(node *psdtext.Node) error {
	if len(node.Entries) > 128 {
		return sceneError(psdfile.ErrLimit, "textOpenType.typographyProperties", node.Span.Offset,
			"character or paragraph property count exceeds limit")
	}
	for index, entry := range node.Entries {
		for _, previous := range node.Entries[:index] {
			if entry.Key == previous.Key {
				return sceneError(psdfile.ErrMalformed, "textOpenType.typographyProperties", node.Span.Offset,
					"duplicate character or paragraph property")
			}
		}
	}
	return nil
}

// Tracking adds one thousandth-em step between shaped clusters; a ligature remains one shaped cluster.
func layoutOpenTypePointLines(
	ctx context.Context, lines []openTypePointLine, typography openTypeTypography, resolver psdfont.FontResolver,
	shape psdfont.ShapeOptions, maxGlyphs int, budget int64,
) ([]*psdfont.Run, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxGlyphs <= 0 || budget < 256 || shape.MaxGlyphs < 0 || shape.MaxNodes < 0 ||
		shape.MaxLookupVisits < 0 || shape.MaxWorkingBytes < 0 {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.lineLayout", 0, "invalid whole-layer layout limits")
	}
	if shape.MaxGlyphs > 0 {
		maxGlyphs = min(maxGlyphs, shape.MaxGlyphs)
	}
	entryBytes := int64(unsafe.Sizeof((*psdfont.Run)(nil)))
	if int64(len(lines)) > (budget-256)/entryBytes {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.lineLayout", 0, "line run index exceeds byte budget")
	}
	// Every source paragraph break consumes one input work unit; the terminal engine marker consumes none.
	count, active := max(0, len(lines)-1), 0
	if count > maxGlyphs {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.lineLayout", 0,
			"whole-layer input paragraph count exceeds limit")
	}
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		if !utf8.ValidString(line.text) {
			return nil, 0, sceneError(psdfile.ErrMalformed, "textOpenType.lineLayout", 0, "invalid UTF8 line")
		}
		characters := utf8.RuneCountInString(line.text)
		if characters > maxGlyphs-count {
			return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.lineLayout", 0,
				"whole-layer input glyph count exceeds limit")
		}
		count += characters
		if characters > 0 {
			active++
		}
	}
	nodes, visits := shape.MaxNodes, shape.MaxLookupVisits
	if nodes == 0 {
		nodes = 200000
	}
	if visits == 0 {
		visits = 1000000
	}
	shape.MaxNodes, shape.MaxLookupVisits = nodes/max(1, active), visits/max(1, active)
	if shape.MaxNodes <= 0 || shape.MaxLookupVisits <= 0 {
		return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.lineLayout", 0,
			"whole-layer shaping work cannot cover all nonempty lines")
	}
	reserved := 256 + int64(len(lines))*entryBytes
	runs := make([]*psdfont.Run, len(lines))
	baseline, actualGlyphs := float64(0), 0
	for index, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		style := typography.style
		style.Tracking = 0
		style.Leading = line.leading
		text := line.text
		textBytes := int64(0)
		if typography.allCaps {
			textBytes = int64(len(text))
			if textBytes > budget-reserved {
				return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.caps", 0, "capitalized text exceeds budget")
			}
			for offset, character := range text {
				if offset%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, 0, err
					}
				}
				if character > 127 {
					return nil, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.caps", 0,
						"all-capital projection currently requires ASCII source scalars")
				}
			}
			text = strings.ToUpper(text)
		}
		if text != "" {
			available := budget - reserved - textBytes
			if available <= 0 {
				return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.lineLayout", 0,
					"no line shaping budget remains")
			}
			run, err := psdfont.LayoutOpenType(ctx, text, style, resolver, shape,
				psdfont.LayoutOptions{MaxWorkingBytes: available, MaxGlyphs: maxGlyphs, OriginY: baseline,
					GlyphLimits: psdfont.GlyphOptions{PreserveCFFHints: typography.preserveHints}})
			if err != nil {
				return nil, 0, err
			}
			if run.WorkingBytes() > available || len(run.Glyphs) > maxGlyphs-actualGlyphs {
				return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.lineLayout", 0,
					"whole-layer shaped glyph or allocation limit exceeded")
			}
			actualGlyphs += len(run.Glyphs)
			reserved += run.WorkingBytes() + textBytes
			if typography.fauxAdvance == 0 {
				if err := positionOpenTypePointLine(ctx, run, line,
					typography.style.Tracking, typography.style.Size); err != nil {
					return nil, 0, err
				}
			} else if err := positionOpenTypePointLineWithAdvance(ctx, run, line,
				typography.style.Tracking, typography.style.Size, typography.fauxAdvance); err != nil {
				return nil, 0, err
			}
			runs[index] = run
		}
		baseline += line.leading
		if !textFinite(baseline) || math.Abs(baseline) > 1<<40 {
			return nil, 0, sceneError(psdfile.ErrLimit, "textOpenType.leading", line.start,
				"paragraph baselines exceed supported coordinate precision")
		}
	}
	return runs, reserved, nil
}

func positionOpenTypePointLine(
	ctx context.Context, run *psdfont.Run, line openTypePointLine, tracking, emSize float64,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Run.Scale converts font units, while source tracking is measured in thousandths of the pixel em.
	step := tracking * emSize / 1000
	advance := run.AdvanceWidth + float64(max(0, len(run.Glyphs)-1))*step
	if !textFinite(step) || !textFinite(advance) || math.Abs(advance) > 1<<40 {
		return sceneError(psdfile.ErrLimit, "textOpenType.tracking", line.start,
			"tracked paragraph advance exceeds supported coordinate precision")
	}
	origin := float64(0)
	if line.justification == 2 {
		origin = -advance / 2
	}
	for index := range run.Glyphs {
		if err := ctx.Err(); err != nil {
			return err
		}
		glyph := &run.Glyphs[index]
		glyph.X += origin + float64(index)*step
		glyph.ClusterStart += line.start
		if index+1 < len(run.Glyphs) {
			glyph.Advance += step
		}
		if !textFinite(glyph.X) || math.Abs(glyph.X) > 1<<40 {
			return sceneError(psdfile.ErrLimit, "textOpenType.tracking", glyph.ClusterStart,
				"tracked glyph origin exceeds supported coordinate precision")
		}
	}
	run.AdvanceWidth = advance
	return nil
}

func renderOpenTypeTypography(
	ctx context.Context, metadata *psdtext.Metadata, text string, transform [6]float64,
	resolver psdfont.FontResolver, options OpenTypeTextRenderOptions, bounds image.Rectangle, budget int64,
) (*image.NRGBA, error) {
	for _, value := range transform {
		if !textFinite(value) {
			return nil, sceneError(psdfile.ErrMalformed, "textOpenType.affine", 0,
				"nonfinite source transform")
		}
	}
	typography, lines, lineBytes, err := openTypeTypographyLines(ctx, metadata, text, budget)
	if err != nil {
		return nil, err
	}
	if typography.fauxBold && options.FauxBold == OpenTypeFauxBoldExpandedAdvance {
		typography.fauxAdvance = typography.style.Size / 36
	}
	typography.preserveHints = options.Hinting != OpenTypeTextHintNone
	if typography.preserveHints && (transform[0] <= 0 || transform[3] <= 0 ||
		transform[1] != 0 || transform[2] != 0) {
		return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.hinting", 0,
			"CFF1 fitting requires a positive axis-aligned affine")
	}
	runs, layoutBytes, err := layoutOpenTypePointLines(ctx, lines, typography, resolver,
		options.Shape, options.MaxGlyphs, budget-lineBytes)
	if err != nil {
		return nil, err
	}
	budget -= lineBytes + layoutBytes
	radius := float64(0)
	if typography.fauxBold {
		effectiveEm := typography.style.Size *
			max(math.Hypot(transform[0], transform[1]), math.Hypot(transform[2], transform[3]))
		radius = effectiveEm / 48
		if options.FauxBoldQuantized {
			radius = math.Round(2*effectiveEm/36) / 4
		}
	}
	if !textFinite(radius) || radius > 64 {
		return nil, sceneError(psdfile.ErrLimit, "textOpenType.fauxBold", 0,
			"faux bold output radius exceeds supported precision")
	}
	anchoredBold := typography.fauxBold && options.FauxBold == OpenTypeFauxBoldExpandedAdvance
	paintRadius := radius
	if anchoredBold {
		// The anchored union reaches two radii in positive output X/Y, including under a source affine.
		paintRadius *= 2
	}
	maxWork := int64(100_000_000)
	paintWork := maxWork
	visibleLines := int64(0)
	for _, run := range runs {
		if run != nil {
			visibleLines++
		}
	}
	scanlineShare := maxWork / max(int64(1), visibleLines)
	result := image.NewNRGBA(bounds)
	for _, run := range runs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if run == nil {
			continue
		}
		paint := &openTypePaintRange{run: run, end: len(run.Glyphs)}
		ink, err := openTypePaintBounds(ctx, paint, transform)
		if err != nil {
			return nil, err
		}
		ink = ink.Inset(-int(math.Ceil(paintRadius))).Intersect(bounds)
		if ink.Empty() && !typography.preserveHints {
			continue
		}
		var transformed *psdfont.Run
		var affineBytes int64
		if typography.preserveHints {
			transformed, err = psdfont.HintRun(ctx, run, transform, psdfont.HintOptions{
				Mode: options.Hinting, Anchor: options.HintAnchor, Grid: options.HintGrid,
				BluePolicy: options.HintBluePolicy, SnapBaselineY: options.HintSnapBaselineY,
				SnapBaselineXQuarter: options.HintSnapBaselineXQuarter,
				MaxWorkingBytes:      budget, MaxOperations: int(scanlineShare)})
			if err == nil {
				affineBytes = transformed.WorkingBytes()
				ink = transformed.Bounds.Inset(-int(math.Ceil(paintRadius))).Intersect(bounds)
			}
		} else {
			transformed, affineBytes, err = textAffineRun(ctx, run, transform, budget)
		}
		if err != nil {
			return nil, err
		}
		if ink.Empty() {
			continue
		}
		if int64(ink.Dx()) > paintWork/int64(ink.Dy()) {
			return nil, sceneError(psdfile.ErrLimit, "textOpenType.typographyPaint", 0,
				"whole-layer painted pixel work exceeds allowance")
		}
		paintWork -= int64(ink.Dx()) * int64(ink.Dy())
		if budget <= affineBytes {
			return nil, sceneError(psdfile.ErrLimit, "textOpenType.typographyRaster", 0,
				"no coverage budget remains")
		}
		coverage, err := psdfont.RasterizeRun(ctx, transformed, ink,
			psdfont.RasterOptions{MaxWorkingBytes: budget - affineBytes, Samples: options.Samples,
				FauxBoldRadius: radius, MaxScanlineWork: scanlineShare,
				AreaCoverage: options.AreaCoverage, NoAntialias: options.NoAntialias,
				FauxBoldAnchored: anchoredBold, AreaSamples: options.AreaSamples,
				CoverageLevels16: options.CoverageLevels16})
		if err != nil {
			return nil, err
		}
		for row := 0; row < ink.Dy(); row++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			for column := 0; column < ink.Dx(); column++ {
				if column%1024 == 0 {
					if err := ctx.Err(); err != nil {
						return nil, err
					}
				}
				alpha := uint32(math.Round(float64(coverage.Pix[row*coverage.Stride+column]) * typography.color[0]))
				if alpha == 0 {
					continue
				}
				at := (row+ink.Min.Y-bounds.Min.Y)*result.Stride + (column+ink.Min.X-bounds.Min.X)*4
				backAlpha := uint32(result.Pix[at+3])
				combined := alpha*255 + backAlpha*(255-alpha)
				for channel := range 3 {
					source := uint32(textByte(typography.color[channel+1]))
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
