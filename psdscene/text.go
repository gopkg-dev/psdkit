package psdscene

import (
	"context"
	"encoding/binary"
	"image"
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdtext"
)

// TextRenderOptions bounds an explicitly requested approximate text source render.
// Zero selects 64 MiB, 10000 clusters, eight samples per axis and the document canvas.
// Font resolver storage is caller-owned and is budgeted independently by psdfont.Decode/Open.
type TextRenderOptions struct {
	MaxWorkingBytes int64
	MaxGlyphs       int
	Samples         int
	Bounds          *image.Rectangle
	// Kerning explicitly chooses legacy metric pairs or no kerning.
	// Modern Txt2 documents require this override because TySh's legacy AutoKerning
	// projection cannot distinguish actual None from Metrics. Optical kerning is unsupported.
	Kerning *bool
}

// RenderTextLayer creates an owned approximate unhinted source for one RGB8 point-text layer.
// It applies the source's six-value affine transform and saved RGB fill, using an explicit font resolver.
// This initial subset requires 72ppi, one complete character style, default left-aligned paragraphs,
// Sharp antialias metadata and no warp. Modern-only typography is not inferred from cached pixels.
// Layer opacity, masks, effects and blending are applied separately when composing this source.
func RenderTextLayer(
	ctx context.Context, source *psdfile.Document, record psdfile.TextLayerRecord,
	resolver psdfont.FontResolver, options ...TextRenderOptions,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || len(options) > 1 {
		return nil, sceneError(psdfile.ErrMalformed, "text.options", 0, "provide a source and at most one options value")
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	settings := TextRenderOptions{MaxWorkingBytes: 64 << 20, MaxGlyphs: 10000}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxWorkingBytes < 0 || provided.MaxGlyphs < 0 {
			return nil, sceneError(psdfile.ErrLimit, "text.options", 0, "limits must not be negative")
		}
		if provided.MaxWorkingBytes != 0 {
			settings.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		if provided.MaxGlyphs != 0 {
			settings.MaxGlyphs = provided.MaxGlyphs
		}
		settings.Samples, settings.Bounds = provided.Samples, provided.Bounds
		if provided.Kerning != nil {
			value := *provided.Kerning
			settings.Kerning = &value
		}
	}
	if source.Header.ColorMode != 3 || source.Header.Depth != 8 || record.Block.Key != "TySh" {
		return nil, sceneError(psdfile.ErrUnsupported, "text.header", record.Block.Data.Offset,
			"text source rendering requires modern RGB8 TySh metadata")
	}
	if record.Version != 1 || record.TextVersion != 50 || record.WarpVersion != 1 {
		return nil, sceneError(psdfile.ErrUnsupported, "text.version", record.Block.Data.Offset, "unknown text schema")
	}
	if err := textWarp(record.Warp); err != nil {
		return nil, err
	}
	if enum, ok := textEntryValue(record.TextDescriptor, "Ornt").(psdfile.DescriptorEnum); !ok || enum.ValueID != "Hrzn" {
		return nil, sceneError(psdfile.ErrUnsupported, "text.orientation", record.Block.Data.Offset,
			"only horizontal point text is implemented")
	}
	gridding, griddingOK := textEntryValue(record.TextDescriptor, "textGridding").(psdfile.DescriptorEnum)
	if !griddingOK || gridding.ValueID != "None" {
		return nil, sceneError(psdfile.ErrUnsupported, "text.gridding", record.Block.Data.Offset,
			"text gridding is unsupported")
	}
	antialias, antialiasOK := textEntryValue(record.TextDescriptor, "AntA").(psdfile.DescriptorEnum)
	if !antialiasOK || antialias.ValueID != "antiAliasSharp" {
		return nil, sceneError(psdfile.ErrUnsupported, "text.antialias", record.Block.Data.Offset,
			"this approximate renderer supports only Sharp source metadata")
	}
	for _, block := range source.AdditionalInfo {
		if block.Key == "Txt2" && settings.Kerning == nil {
			return nil, sceneError(psdfile.ErrUnsupported, "text.modernKerning", block.Data.Offset,
				"modern kerning is not exposed by legacy TySh; provide an explicit kerning policy")
		}
	}
	if err := textResolution(ctx, source); err != nil {
		return nil, err
	}
	bounds := image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
	if settings.Bounds != nil {
		bounds = *settings.Bounds
	}
	canvas := image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
	if bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y || !bounds.In(canvas) {
		return nil, sceneError(psdfile.ErrMalformed, "text.bounds", 0, "text output rectangle is outside the canvas")
	}
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if settings.MaxWorkingBytes < 8192 || width > int64(^uint(0)>>1)/4/max(1, height) ||
		width > settings.MaxWorkingBytes/5/max(1, height) {
		return nil, sceneError(psdfile.ErrLimit, "text.bounds", 0, "RGBA and coverage exceed the working budget")
	}
	outputReservation := width * height * 5
	available := settings.MaxWorkingBytes - outputReservation - 4096
	if available < 4096 {
		return nil, sceneError(psdfile.ErrLimit, "text.metadata", record.EngineData.Offset,
			"no text metadata budget remains")
	}
	// Reserve bounded metadata partitions so their owned trees remain charged during layout and coverage.
	engineBudget := min(int64(8<<20), available/4)
	metadataBudget := min(int64(2<<20), available/8)
	engine, err := source.DecodeTextEngine(ctx, record, psdtext.DecodeOptions{MaxBytes: engineBudget})
	if err != nil {
		return nil, err
	}
	metadata, err := engine.Metadata(ctx, psdtext.MetadataOptions{MaxBytes: metadataBudget,
		MaxRuns: settings.MaxGlyphs, MaxFonts: settings.MaxGlyphs})
	if err != nil {
		return nil, err
	}
	style, color, err := textStyle(metadata, record.Text)
	if err != nil {
		return nil, err
	}
	if err := textPointShape(metadata.Engine); err != nil {
		return nil, err
	}
	if settings.Kerning != nil {
		style.Kerning = *settings.Kerning
	}
	layoutBudget := min(int64(16<<20), available-engineBudget-metadataBudget)
	run, err := psdfont.Layout(ctx, record.Text, style, resolver,
		psdfont.LayoutOptions{MaxWorkingBytes: layoutBudget, MaxGlyphs: settings.MaxGlyphs})
	if err != nil {
		return nil, err
	}
	transformBudget := available - engineBudget - metadataBudget - layoutBudget
	transformed, reservation, err := textAffineRun(ctx, run, record.Transform, transformBudget)
	if err != nil {
		return nil, err
	}
	rasterBudget := available - engineBudget - metadataBudget - layoutBudget - reservation
	if rasterBudget <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "text.coverage", 0, "no glyph coverage budget remains")
	}
	coverage, err := psdfont.RasterizeRun(ctx, transformed, bounds,
		psdfont.RasterOptions{MaxWorkingBytes: rasterBudget, Samples: settings.Samples})
	if err != nil {
		return nil, err
	}
	result := image.NewNRGBA(bounds)
	red, green, blue := textByte(color[1]), textByte(color[2]), textByte(color[3])
	for row := range bounds.Dy() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range bounds.Dx() {
			if column%1024 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			offset := row*result.Stride + column*4
			alpha := float64(coverage.Pix[row*coverage.Stride+column]) * color[0]
			result.Pix[offset], result.Pix[offset+1], result.Pix[offset+2] = red, green, blue
			result.Pix[offset+3] = byte(math.Round(alpha))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func textEntryValue(descriptor *psdfile.Descriptor, key string) any {
	if descriptor != nil {
		for _, v := range slices.Backward(descriptor.Entries) {
			if v.Key == key {
				return v.Value
			}
		}
	}
	return nil
}

func textWarp(warp *psdfile.Descriptor) error {
	if warp == nil {
		return sceneError(psdfile.ErrMalformed, "text.warp", 0, "text warp descriptor is absent")
	}
	for _, entry := range warp.Entries {
		switch entry.Key {
		case "warpStyle":
			value, ok := entry.Value.(psdfile.DescriptorEnum)
			if !ok || value.ValueID != "warpNone" {
				return sceneError(psdfile.ErrUnsupported, "text.warp", entry.Data.Offset, "text warp is unsupported")
			}
		case "warpValue", "warpPerspective", "warpPerspectiveOther":
			value, ok := entry.Value.(float64)
			if !ok || value != 0 {
				return sceneError(psdfile.ErrUnsupported, "text.warp", entry.Data.Offset, "nonzero text warp is unsupported")
			}
		case "warpRotate":
			value, ok := entry.Value.(psdfile.DescriptorEnum)
			if !ok || value.ValueID != "Hrzn" {
				return sceneError(psdfile.ErrUnsupported, "text.warp", entry.Data.Offset,
					"text warp orientation is unsupported")
			}
		default:
			return sceneError(psdfile.ErrUnsupported, "text.warp", entry.Data.Offset, "unknown warp property")
		}
	}
	if textEntryValue(warp, "warpStyle") == nil {
		return sceneError(psdfile.ErrMalformed, "text.warp", warp.Span.Offset, "text warp style is absent")
	}
	return nil
}

func textResolution(ctx context.Context, source *psdfile.Document) error {
	for _, block := range source.Resources {
		if block.ID != 1005 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if block.Data.Length != 16 {
			return sceneError(psdfile.ErrMalformed, "text.resolution", block.Data.Offset, "invalid resolution-info payload")
		}
		data, err := source.ReadSpan(block.Data)
		if err != nil {
			return err
		}
		if binary.BigEndian.Uint32(data) != 72<<16 || binary.BigEndian.Uint32(data[8:]) != 72<<16 ||
			binary.BigEndian.Uint16(data[4:]) != 1 || binary.BigEndian.Uint16(data[12:]) != 1 {
			return sceneError(psdfile.ErrUnsupported, "text.resolution", block.Data.Offset,
				"this initial text subset requires explicit square 72ppi resolution")
		}
		return nil
	}
	return sceneError(psdfile.ErrUnsupported, "text.resolution", 0, "explicit 72ppi resolution metadata is required")
}

func textByte(value float64) byte { return byte(math.Round(max(0, min(1, value)) * 255)) }
