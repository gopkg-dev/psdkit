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

// OpenTypeTextKerning explicitly selects optional horizontal font kerning.
// Modern private kerning is not inferred from legacy AutoKerning or saved shapes.
type OpenTypeTextKerning uint8

// OpenTypeTextHinting selects an explicit approximate CFF1 source-hint fitting model.
type OpenTypeTextHinting = psdfont.CFFHintMode

// OpenTypeTextBluePolicy selects approximate CFF1 alignment-zone treatment.
type OpenTypeTextBluePolicy = psdfont.CFFBluePolicy

const (
	// OpenTypeTextBlueGrid preserves the default pixel-lattice blue-zone alignment.
	OpenTypeTextBlueGrid = psdfont.CFFBlueGrid
	// OpenTypeTextBlueResolvedOutward preserves outward pixel height of resolvable source zones.
	OpenTypeTextBlueResolvedOutward = psdfont.CFFBlueResolvedOutward
)

const (
	// OpenTypeTextHintNone preserves unhinted outlines.
	OpenTypeTextHintNone = psdfont.CFFHintNone
	// OpenTypeTextHintVertical fits active CFF1 horizontal stems and blue zones in device Y.
	OpenTypeTextHintVertical = psdfont.CFFVertical
	// OpenTypeTextHintFull also fits active CFF1 vertical stems in device X.
	OpenTypeTextHintFull = psdfont.CFFFull
	// OpenTypeTextHintFractional fits stem centers while retaining source fractional widths.
	OpenTypeTextHintFractional = psdfont.CFFFractional
	// OpenTypeTextHintEdgeFractional fits the nearest stem edge without rounding source widths.
	OpenTypeTextHintEdgeFractional = psdfont.CFFEdgeFractional
)

const (
	// OpenTypeTextNone disables the optional kern feature; required font features still execute.
	OpenTypeTextNone OpenTypeTextKerning = iota + 1
	// OpenTypeTextMetrics enables the font's optional OpenType kern feature without legacy kern fallback.
	OpenTypeTextMetrics
)

// OpenTypeFauxBoldPolicy selects an explicit approximate synthetic-weight layout model.
type OpenTypeFauxBoldPolicy uint8

const (
	// OpenTypeFauxBoldOutline preserves the original outline-only model without synthetic advance.
	OpenTypeFauxBoldOutline OpenTypeFauxBoldPolicy = iota
	// OpenTypeFauxBoldExpandedAdvance adds one thirty-sixth of the source em to every shaped glyph's advance.
	// Source spaces and the final glyph participate; source font metrics remain unchanged.
	// Outline copies use an output-space circle centered at (+radius, +radius), retaining the original outline.
	OpenTypeFauxBoldExpandedAdvance
)

// OpenTypeStyleBoundaryPolicy selects an explicit approximate multi-style shaping policy.
type OpenTypeStyleBoundaryPolicy uint8

const (
	// OpenTypeSingleStyle retains the existing default single-style source contract.
	OpenTypeSingleStyle OpenTypeStyleBoundaryPolicy = iota
	// OpenTypeIsolatedStyleRuns shapes each source style separately and paints runs in source order.
	OpenTypeIsolatedStyleRuns
	// OpenTypeCompatibleStyleRuns shapes matching typography across color boundaries.
	// ClusterPaint explicitly selects how a glyph cluster spanning different colors is painted.
	OpenTypeCompatibleStyleRuns
)

// OpenTypeClusterPaintPolicy selects approximate paint ownership for a whole shaped UTF16 cluster.
type OpenTypeClusterPaintPolicy uint8

const (
	// OpenTypeRejectMixedClusterPaint keeps the default rejection of clusters spanning different RGBA styles.
	OpenTypeRejectMixedClusterPaint OpenTypeClusterPaintPolicy = iota
	// OpenTypeFirstStyleClusterPaint paints a whole cluster with the RGBA style covering its logical start.
	// This explicit mathematical policy requires compatible typography and does not certify native antialiasing.
	OpenTypeFirstStyleClusterPaint
)

// OpenTypeTextRenderOptions explicitly selects a bounded approximate OpenType text source render.
// Kerning must be None or Metrics; Optical, unset, and unknown modes are unsupported.
// Shape.Features selects optional glyph features; a kern setting must agree with Kerning.
// The explicit caller policy overrides unexposed modern feature settings, not source text/font/geometry.
// Zero budgets select 64 MiB and 10000 input glyphs; Bounds defaults to the document canvas.
// Multi-style policies share a conservative 100 million scanline-work ceiling and painted-pixel ceiling.
// Shape glyph/node/lookup limits cover the entire layer; each shaping group receives a fixed work share.
// Features and Bounds are copied before any source decode or resolver callback.
// The caller record must exactly match the uniquely indexed source TySh record.
type OpenTypeTextRenderOptions struct {
	MaxWorkingBytes int64
	MaxGlyphs       int
	Samples         int
	Bounds          *image.Rectangle
	Kerning         OpenTypeTextKerning
	Shape           psdfont.ShapeOptions
	StyleBoundaries OpenTypeStyleBoundaryPolicy
	ClusterPaint    OpenTypeClusterPaintPolicy
	// ApproximateTypography enables single-style point paragraphs, tracking, leading and source capitalization.
	// It uses explicit Latin layout without wrapping, optical kerning or saved glyph artwork.
	ApproximateTypography bool
	// FauxBold selects synthetic layout only for source styles carrying FauxBold.
	// ExpandedAdvance requires ApproximateTypography and one complete source style.
	FauxBold OpenTypeFauxBoldPolicy
	// FauxBoldQuantized rounds the expanded-advance model's synthetic extent to half output pixels.
	// It requires ExpandedAdvance and retains its source-em advance model.
	FauxBoldQuantized bool
	// AreaCoverage selects bounded horizontal span integration rather than the legacy sample grid.
	// NoAntialias selects unhinted pixel-center membership; both require ApproximateTypography.
	// The models are mutually exclusive and do not reproduce Photoshop antialias modes.
	AreaCoverage bool
	NoAntialias  bool
	// AreaSamples and CoverageLevels16 select explicit bounded coverage models after the outline union.
	AreaSamples      int
	CoverageLevels16 bool
	// Hinting fits preserved CFF1 source hints before faux-bold expansion.
	// It requires approximate single-style typography and a positive axis-aligned affine.
	// TrueType instructions, CFF2 fitting, counter equalization and stem darkening remain unsupported.
	Hinting    OpenTypeTextHinting
	HintAnchor psdfont.CFFHintAnchor
	HintGrid   psdfont.CFFHintGrid
	// HintBluePolicy and HintSnapBaselineY select explicit fractional-edge fitting refinements.
	// Nondefault values require fractional fitting on world pixel edges.
	HintBluePolicy    OpenTypeTextBluePolicy
	HintSnapBaselineY bool
	// HintSnapBaselineXQuarter rounds each device glyph X baseline to one of four pixel phases before fitting.
	// Source advances and positions remain unmodified; this explicit model requires fractional/world/edges fitting.
	HintSnapBaselineXQuarter bool
}

// RenderOpenTypeTextLayer renders one Latin RGB8 point-text source using explicitly selected OpenType features.
// Glyph artwork comes only from font outlines; saved layer/composite pixels and private shapes are not artwork.
// The default requires one line, horizontal 72ppi, Tracking=0, default paragraphs, Sharp metadata and no warp.
// ApproximateTypography additionally accepts single-style point lines with source tracking, leading and capitalization.
// It supports left or center anchors and a bounded eight-direction outline expansion for source FauxBold.
// FauxBold defaults to outline-only; ExpandedAdvance explicitly selects synthetic advances and anchored expansion.
// Tracking adds spacing between shaped clusters, including the source spaces at either end of each line.
// StyleBoundaries defaults to one complete style; explicit alternatives select approximate multi-style typography.
// Isolated runs shape independently; compatible runs share shaping only when every validated typography field matches.
// ClusterPaint defaults to rejecting mixed-color clusters; FirstStyle uses the logical UTF16 start's entire RGBA.
// Multi-style paints use source-order RGBA8 source-over; native boundary and antialias behavior remain unqualified.
// Modern schema-7 text associations and UTF16 style ranges are checked without interpreting private style keys.
// Coverage and optional CFF1 fitting remain explicit approximations of Photoshop text rendering.
// Layer masks, opacity, effects, blending and broader scene text integration remain separate operations.
func RenderOpenTypeTextLayer(
	ctx context.Context, source *psdfile.Document, record psdfile.TextLayerRecord,
	resolver psdfont.FontResolver, options OpenTypeTextRenderOptions,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, sceneError(psdfile.ErrMalformed, "textOpenType.source", 0, "source is required")
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if options.MaxWorkingBytes < 0 || options.MaxGlyphs < 0 {
		return nil, sceneError(psdfile.ErrLimit,
			"textOpenType.options", 0, "limits must not be negative")
	}
	if options.Kerning != OpenTypeTextNone && options.Kerning != OpenTypeTextMetrics {
		return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.kerning", 0, "choose explicit None or Metrics")
	}
	if options.StyleBoundaries > OpenTypeCompatibleStyleRuns {
		return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.stylePolicy", 0, "unknown style boundary policy")
	}
	if options.ClusterPaint > OpenTypeFirstStyleClusterPaint ||
		options.ClusterPaint == OpenTypeFirstStyleClusterPaint && options.StyleBoundaries != OpenTypeCompatibleStyleRuns {
		return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.clusterPaint", 0,
			"choose a known cluster paint policy; FirstStyle requires compatible style runs")
	}
	if options.ApproximateTypography && options.Samples != 0 && options.Samples != 4 && options.Samples != 8 {
		return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.typographySamples", 0,
			"coverage supports only four or eight samples per axis")
	}
	if err := validateOpenTypeTypographyPolicy(options); err != nil {
		return nil, err
	}
	if options.MaxWorkingBytes == 0 {
		options.MaxWorkingBytes = 64 << 20
	}
	if options.MaxGlyphs == 0 {
		options.MaxGlyphs = 10000
	}
	if source.Header.ColorMode != 3 || source.Header.Depth != 8 || record.Block.Key != "TySh" {
		return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.header", record.Block.Data.Offset,
			"source rendering requires modern RGB8 TySh metadata")
	}
	canvas := image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
	bounds := canvas
	if options.Bounds != nil {
		bounds = *options.Bounds
	}
	if bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y || !bounds.In(canvas) {
		return nil, sceneError(psdfile.ErrMalformed, "textOpenType.bounds", 0, "output rectangle outside canvas")
	}
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if options.MaxWorkingBytes < 16384 || width > int64(^uint(0)>>1)/4/max(1, height) ||
		width > options.MaxWorkingBytes/5/max(1, height) {
		return nil, sceneError(psdfile.ErrLimit, "textOpenType.bounds", 0, "RGBA and coverage exceed byte budget")
	}
	outputBytes := width * height * 5
	featureBytes := int64(unsafe.Sizeof(psdfont.ShapeFeature{}))
	// Reserve fixed bookkeeping and the bounded descriptor-comparison stack before decoding.
	const fixedBytes int64 = 4096 + 2*64*4096
	remaining := options.MaxWorkingBytes - outputBytes - fixedBytes
	if remaining <= 0 || int64(len(options.Shape.Features)) >= remaining/featureBytes {
		return nil, sceneError(psdfile.ErrLimit, "textOpenType.features", 0, "feature ownership exceeds byte budget")
	}
	ownedFeatures := make([]psdfont.ShapeFeature, len(options.Shape.Features), len(options.Shape.Features)+1)
	copy(ownedFeatures, options.Shape.Features)
	hasKern := false
	for _, feature := range ownedFeatures {
		if feature.Tag != "kern" {
			continue
		}
		if hasKern || feature.Enabled != (options.Kerning == OpenTypeTextMetrics) {
			return nil, sceneError(psdfile.ErrMalformed, "textOpenType.features", 0, "duplicate or conflicting kern policy")
		}
		hasKern = true
	}
	if !hasKern {
		ownedFeatures = append(ownedFeatures, psdfont.ShapeFeature{Tag: "kern",
			Enabled: options.Kerning == OpenTypeTextMetrics})
	}
	options.Shape.Features = ownedFeatures
	available := options.MaxWorkingBytes - outputBytes - fixedBytes - int64(cap(ownedFeatures))*featureBytes
	if available < 8192 {
		return nil, sceneError(psdfile.ErrLimit, "textOpenType.metadata", 0, "no metadata budget remains")
	}
	physicalBytes := min(int64(8<<20), available/8)
	physical, err := openTypeTextSourceRecord(ctx, source, record, physicalBytes)
	if err != nil {
		return nil, err
	}
	record = physical
	available -= physicalBytes
	if record.Version != 1 || record.TextVersion != 50 || record.WarpVersion != 1 ||
		!options.ApproximateTypography && strings.ContainsAny(record.Text, "\r\n") {
		return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.schema", record.Block.Data.Offset,
			"only known single-line point-text schema is implemented")
	}
	if err := openTypeTextWarp(record.Warp); err != nil {
		return nil, err
	}
	orientation, orientationOK := textEntryValue(record.TextDescriptor, "Ornt").(psdfile.DescriptorEnum)
	if !orientationOK || orientation.TypeID != "Ornt" || orientation.ValueID != "Hrzn" {
		return nil, sceneError(psdfile.ErrUnsupported,
			"textOpenType.orientation", record.Block.Data.Offset, "horizontal text is required")
	}
	gridding, griddingOK := textEntryValue(record.TextDescriptor, "textGridding").(psdfile.DescriptorEnum)
	if !griddingOK || gridding.TypeID != "textGridding" || gridding.ValueID != "None" {
		return nil, sceneError(psdfile.ErrUnsupported,
			"textOpenType.gridding", record.Block.Data.Offset, "text gridding is unsupported")
	}
	aa, aaOK := textEntryValue(record.TextDescriptor, "AntA").(psdfile.DescriptorEnum)
	if !aaOK || aa.TypeID != "Annt" || aa.ValueID != "antiAliasSharp" {
		return nil, sceneError(psdfile.ErrUnsupported,
			"textOpenType.antialias", record.Block.Data.Offset, "Sharp metadata required for this approximate renderer")
	}
	if err := textResolution(ctx, source); err != nil {
		return nil, err
	}
	engineBytes, typedBytes := min(int64(8<<20), available/8), min(int64(2<<20), available/16)
	associationBytes := min(int64(8<<20), available/4)
	if err := openTypeTextAssociationPolicy(ctx, source, record, associationBytes,
		options.StyleBoundaries, options.ApproximateTypography); err != nil {
		return nil, err
	}
	engine, err := source.DecodeTextEngine(ctx, record, psdtext.DecodeOptions{MaxBytes: engineBytes})
	if err != nil {
		return nil, err
	}
	metadata, err := engine.Metadata(ctx, psdtext.MetadataOptions{MaxBytes: typedBytes,
		MaxRuns: 100000, MaxFonts: 10000})
	if err != nil {
		return nil, err
	}
	if options.ApproximateTypography {
		if err := textPointShape(metadata.Engine); err != nil {
			return nil, err
		}
		remaining := available - engineBytes - typedBytes - associationBytes
		return renderOpenTypeTypography(ctx, metadata, record.Text, record.Transform, resolver,
			options, bounds, remaining)
	}
	if len(metadata.Styles) != 1 && options.StyleBoundaries != OpenTypeSingleStyle {
		if options.Samples != 0 && options.Samples != 4 && options.Samples != 8 {
			return nil, sceneError(psdfile.ErrUnsupported, "textOpenType.styleSamples", 0,
				"coverage supports only four or eight samples per axis")
		}
		if err := textPointShape(metadata.Engine); err != nil {
			return nil, err
		}
		remaining := available - engineBytes - typedBytes - associationBytes
		return renderOpenTypeStyledMetadata(ctx, metadata, record.Text, record.Transform,
			resolver, options, bounds, remaining)
	}
	style, color, styleCopyBytes, err := openTypeTextStyle(metadata, record.Text, typedBytes)
	if err != nil {
		return nil, err
	}
	if err := textPointShape(metadata.Engine); err != nil {
		return nil, err
	}
	layoutBytes := min(int64(16<<20), available-engineBytes-typedBytes-associationBytes-styleCopyBytes)
	if layoutBytes <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "textOpenType.layout", 0, "no layout budget remains")
	}
	run, err := psdfont.LayoutOpenType(ctx, record.Text, style, resolver, options.Shape,
		psdfont.LayoutOptions{MaxWorkingBytes: layoutBytes, MaxGlyphs: options.MaxGlyphs})
	if err != nil {
		return nil, err
	}
	// Layout enforces its ceiling during shaping and glyph decoding, but that unused ceiling is not resident.
	// Keep its conservative peak reservation across affine/raster phases, including shared owned outlines.
	transformBytes := available - engineBytes - typedBytes - associationBytes - styleCopyBytes - run.WorkingBytes()
	transformed, transformReservation, err := textAffineRun(ctx, run, record.Transform, transformBytes)
	if err != nil {
		return nil, err
	}
	rasterBytes := transformBytes - transformReservation
	if rasterBytes <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "textOpenType.raster", 0, "no coverage budget remains")
	}
	coverage, err := psdfont.RasterizeRun(ctx, transformed, bounds,
		psdfont.RasterOptions{MaxWorkingBytes: rasterBytes, Samples: options.Samples})
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
			at := row*result.Stride + column*4
			result.Pix[at], result.Pix[at+1], result.Pix[at+2] = red, green, blue
			result.Pix[at+3] = byte(math.Round(float64(coverage.Pix[row*coverage.Stride+column]) * color[0]))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func openTypeTextSourceRecord(
	ctx context.Context, source *psdfile.Document, record psdfile.TextLayerRecord, budget int64,
) (psdfile.TextLayerRecord, error) {
	var empty psdfile.TextLayerRecord
	view, err := source.ReadTextLayerView(ctx, psdfile.MetadataOptions{MaxBytes: budget})
	if err != nil {
		return empty, err
	}
	var physical psdfile.TextLayerRecord
	matches := 0
	for _, candidate := range view.Records {
		if candidate.LayerSetIndex == record.LayerSetIndex && candidate.LayerIndex == record.LayerIndex &&
			candidate.Block.Data == record.Block.Data {
			physical = candidate
			matches++
		}
	}
	if matches != 1 {
		return empty, sceneError(psdfile.ErrMalformed, "textOpenType.sourceRecord", record.Block.Data.Offset,
			"caller record must identify one physical source TySh block")
	}
	callerScalars, sourceScalars := record, physical
	callerScalars.TextDescriptor, callerScalars.Warp = nil, nil
	sourceScalars.TextDescriptor, sourceScalars.Warp = nil, nil
	comparison := openTypeTextComparison{ctx: ctx}
	equal := callerScalars == sourceScalars && openTypeTextGeometryEqual(record, physical) &&
		comparison.descriptor(record.TextDescriptor, physical.TextDescriptor, 1) &&
		comparison.descriptor(record.Warp, physical.Warp, 1)
	if comparison.err != nil {
		return empty, comparison.err
	}
	if !equal {
		return empty, sceneError(psdfile.ErrMalformed, "textOpenType.sourceRecord", record.Block.Data.Offset,
			"caller text, descriptors, geometry or source spans differ from the indexed TySh block")
	}
	return physical, nil
}

func openTypeTextGeometryEqual(caller, source psdfile.TextLayerRecord) bool {
	for index, value := range source.Transform {
		if math.Float64bits(caller.Transform[index]) != math.Float64bits(value) {
			return false
		}
	}
	for index, value := range source.Bounds {
		if math.Float64bits(caller.Bounds[index]) != math.Float64bits(value) {
			return false
		}
	}
	return true
}

func openTypeTextWarp(warp *psdfile.Descriptor) error {
	if err := textWarp(warp); err != nil {
		return err
	}
	for _, entry := range warp.Entries {
		if entry.Key != "warpStyle" && entry.Key != "warpRotate" {
			continue
		}
		expectedType := "warpStyle"
		if entry.Key == "warpRotate" {
			expectedType = "Ornt"
		}
		value := entry.Value.(psdfile.DescriptorEnum)
		if value.TypeID != expectedType {
			return sceneError(psdfile.ErrUnsupported, "textOpenType.warp", entry.Data.Offset,
				"unknown warp enumeration type")
		}
	}
	return nil
}

// Comparison follows the bounded acyclic source tree, so forged caller cycles do not need a visited allocation.
type openTypeTextComparison struct {
	ctx   context.Context
	nodes int
	err   error
}

func (c *openTypeTextComparison) visit(depth int) bool {
	if c.err != nil {
		return false
	}
	if c.err = c.ctx.Err(); c.err != nil {
		return false
	}
	c.nodes++
	if depth > 64 || c.nodes > 100000 {
		c.err = sceneError(psdfile.ErrLimit, "textOpenType.sourceRecord", 0,
			"source descriptor comparison exceeds depth or node limit")
	}
	return c.err == nil
}

func (c *openTypeTextComparison) descriptor(caller, source *psdfile.Descriptor, depth int) bool {
	if !c.visit(depth) || caller == nil || source == nil {
		return c.err == nil && caller == nil && source == nil
	}
	if caller.Name != source.Name || caller.ClassID != source.ClassID || caller.Version != source.Version ||
		caller.Span != source.Span || caller.VersionData != source.VersionData || caller.NameData != source.NameData ||
		caller.ClassIDData != source.ClassIDData || caller.CountData != source.CountData {
		return false
	}
	return c.entries(caller.Entries, source.Entries, depth)
}

func (c *openTypeTextComparison) entries(caller, source []psdfile.DescriptorEntry, depth int) bool {
	if len(caller) != len(source) || (caller == nil) != (source == nil) {
		return false
	}
	for index, expected := range source {
		actual := caller[index]
		actualValue, expectedValue := actual.Value, expected.Value
		actual.Value, expected.Value = nil, nil
		if actual != expected || !c.value(actualValue, expectedValue, depth) {
			return false
		}
	}
	return true
}

func (c *openTypeTextComparison) values(caller, source []psdfile.DescriptorValue, depth int) bool {
	if len(caller) != len(source) || (caller == nil) != (source == nil) {
		return false
	}
	for index, expected := range source {
		actual := caller[index]
		actualValue, expectedValue := actual.Value, expected.Value
		actual.Value, expected.Value = nil, nil
		if actual != expected || !c.value(actualValue, expectedValue, depth) {
			return false
		}
	}
	return true
}

func (c *openTypeTextComparison) value(caller, source any, depth int) bool {
	if !c.visit(depth) {
		return false
	}
	switch expected := source.(type) {
	case nil:
		return caller == nil
	case int32:
		actual, ok := caller.(int32)
		return ok && actual == expected
	case int64:
		actual, ok := caller.(int64)
		return ok && actual == expected
	case bool:
		actual, ok := caller.(bool)
		return ok && actual == expected
	case string:
		actual, ok := caller.(string)
		return ok && actual == expected
	case float32:
		actual, ok := caller.(float32)
		return ok && math.Float32bits(actual) == math.Float32bits(expected)
	case float64:
		actual, ok := caller.(float64)
		return ok && math.Float64bits(actual) == math.Float64bits(expected)
	case psdfile.DescriptorClass:
		actual, ok := caller.(psdfile.DescriptorClass)
		return ok && actual == expected
	case psdfile.DescriptorEnum:
		actual, ok := caller.(psdfile.DescriptorEnum)
		return ok && actual == expected
	case psdfile.DescriptorBlob:
		actual, ok := caller.(psdfile.DescriptorBlob)
		return ok && actual == expected
	case psdfile.DescriptorProperty:
		actual, ok := caller.(psdfile.DescriptorProperty)
		return ok && actual == expected
	case psdfile.DescriptorEnumeratedReference:
		actual, ok := caller.(psdfile.DescriptorEnumeratedReference)
		return ok && actual == expected
	case psdfile.DescriptorOffset:
		actual, ok := caller.(psdfile.DescriptorOffset)
		return ok && actual == expected
	case psdfile.DescriptorNamedReference:
		actual, ok := caller.(psdfile.DescriptorNamedReference)
		return ok && actual == expected
	case psdfile.DescriptorUnit:
		actual, ok := caller.(psdfile.DescriptorUnit)
		if !ok {
			return false
		}
		actualNumber, expectedNumber := actual.Number, expected.Number
		actual.Number, expected.Number = nil, nil
		return actual == expected && c.value(actualNumber, expectedNumber, depth)
	case psdfile.DescriptorUnitArray:
		actual, ok := caller.(psdfile.DescriptorUnitArray)
		if !ok || actual.Unit != expected.Unit || actual.Span != expected.Span ||
			actual.UnitData != expected.UnitData || actual.CountData != expected.CountData ||
			actual.NumbersData != expected.NumbersData || len(actual.Numbers) != len(expected.Numbers) ||
			(actual.Numbers == nil) != (expected.Numbers == nil) {
			return false
		}
		for index, number := range expected.Numbers {
			if !c.value(actual.Numbers[index], number, depth) {
				return false
			}
		}
		return true
	case *psdfile.Descriptor:
		actual, ok := caller.(*psdfile.Descriptor)
		return ok && c.descriptor(actual, expected, depth+1)
	case []psdfile.DescriptorValue:
		actual, ok := caller.([]psdfile.DescriptorValue)
		return ok && c.values(actual, expected, depth+1)
	case *psdfile.DescriptorReference:
		actual, ok := caller.(*psdfile.DescriptorReference)
		if !ok || actual == nil || expected == nil {
			return ok && actual == nil && expected == nil
		}
		return actual.Span == expected.Span && actual.CountData == expected.CountData &&
			c.values(actual.Items, expected.Items, depth+1)
	case *psdfile.DescriptorObjectArray:
		actual, ok := caller.(*psdfile.DescriptorObjectArray)
		if !ok || actual == nil || expected == nil {
			return ok && actual == nil && expected == nil
		}
		return actual.Prefix == expected.Prefix && actual.Name == expected.Name && actual.ClassID == expected.ClassID &&
			actual.Span == expected.Span && actual.PrefixData == expected.PrefixData && actual.NameData == expected.NameData &&
			actual.ClassIDData == expected.ClassIDData && actual.CountData == expected.CountData &&
			c.entries(actual.Entries, expected.Entries, depth+1)
	default:
		return false
	}
}

func openTypeTextAssociation(
	ctx context.Context, source *psdfile.Document, record psdfile.TextLayerRecord, budget int64,
	policies ...OpenTypeStyleBoundaryPolicy,
) error {
	policy := OpenTypeSingleStyle
	if len(policies) == 1 {
		policy = policies[0]
	}
	return openTypeTextAssociationPolicy(ctx, source, record, budget, policy, false)
}

func openTypeTextAssociationPolicy(
	ctx context.Context, source *psdfile.Document, record psdfile.TextLayerRecord, budget int64,
	policy OpenTypeStyleBoundaryPolicy, typography bool,
) error {
	view, err := source.ReadDocumentTextView(ctx, psdfile.MetadataOptions{MaxBytes: budget / 8})
	if err != nil {
		return err
	}
	if len(view.Records) == 0 {
		return nil
	}
	if len(view.Records) != 1 {
		return sceneError(psdfile.ErrUnsupported, "textOpenType.association", 0,
			"multiple modern text engines have ambiguous source semantics")
	}
	engine, err := source.DecodeDocumentTextEngine(ctx, view.Records[0], psdtext.DecodeOptions{MaxBytes: budget / 2})
	if err != nil {
		return err
	}
	// Global metadata cardinality is independent of the target's input-glyph limit.
	metadata, err := engine.DocumentMetadata(ctx, psdtext.DocumentMetadataOptions{MaxBytes: budget / 8,
		MaxRuns: 100000, MaxTexts: 10000})
	if err != nil {
		return err
	}
	associations, err := source.ReadDocumentTextAssociations(ctx, metadata, psdfile.MetadataOptions{MaxBytes: budget / 4})
	if err != nil {
		return err
	}
	matches := 0
	for _, association := range associations.Records {
		layer := association.Layer
		if layer.LayerSetIndex != record.LayerSetIndex || layer.LayerIndex != record.LayerIndex ||
			layer.Block.Data != record.Block.Data {
			continue
		}
		if layer.Text != record.Text || layer.TextIndex != record.TextIndex || layer.Transform != record.Transform {
			return sceneError(psdfile.ErrMalformed, "textOpenType.association", record.Block.Data.Offset,
				"caller record differs from physical source text metadata")
		}
		text := association.Text
		multi := policy != OpenTypeSingleStyle
		if !multi && (len(text.Styles) != 1 || text.Styles[0].Start != 0 || text.Styles[0].Length != text.UTF16Units) ||
			len(text.Styles) == 0 ||
			len(text.Paragraphs) == 0 || !typography && (len(text.Paragraphs) != 1 ||
			text.Paragraphs[0].Start != 0 || text.Paragraphs[0].Length != text.UTF16Units) {
			return sceneError(psdfile.ErrUnsupported, "textOpenType.association", 0,
				"complete modern character ranges and one paragraph are required")
		}
		paragraphCursor := int64(0)
		for _, paragraph := range text.Paragraphs {
			if paragraph.Start != paragraphCursor || paragraph.Length <= 0 ||
				paragraph.Length > text.UTF16Units-paragraphCursor {
				return sceneError(psdfile.ErrMalformed, "textOpenType.association", 0,
					"modern paragraph ranges must partition editor text")
			}
			paragraphCursor += paragraph.Length
		}
		if paragraphCursor != text.UTF16Units {
			return sceneError(psdfile.ErrMalformed, "textOpenType.association", 0,
				"modern paragraph ranges are incomplete")
		}
		cursor := int64(0)
		byteCursor := 0
		for _, style := range text.Styles {
			if style.Start != cursor || style.Length <= 0 || style.Length > text.UTF16Units-cursor {
				return sceneError(psdfile.ErrMalformed, "textOpenType.association", 0, "modern style coverage is inconsistent")
			}
			end := cursor + style.Length
			if multi {
				for cursor < end {
					if byteCursor%1024 == 0 {
						if err := ctx.Err(); err != nil {
							return err
						}
					}
					if byteCursor >= len(text.Text) {
						return sceneError(psdfile.ErrMalformed, "textOpenType.association", 0,
							"modern character range exceeds editor text")
					}
					character, width := utf8.DecodeRuneInString(text.Text[byteCursor:])
					units := int64(1)
					if character > 0xffff {
						units = 2
					}
					if character == utf8.RuneError && width == 1 || units > end-cursor {
						return sceneError(psdfile.ErrMalformed, "textOpenType.association", 0,
							"modern character boundary splits a UTF16 scalar")
					}
					cursor += units
					byteCursor += width
				}
			} else {
				cursor = end
			}
		}
		if cursor != text.UTF16Units {
			return sceneError(psdfile.ErrMalformed, "textOpenType.association", 0, "modern style coverage is incomplete")
		}
		if len(text.Shapes.Items) > 10000 {
			return sceneError(psdfile.ErrLimit, "textOpenType.association", 0, "private shape count exceeds metadata limit")
		}
		matches++
	}
	if matches != 1 {
		return sceneError(psdfile.ErrUnsupported, "textOpenType.association", 0,
			"one physical modern text association is required")
	}
	return nil
}

func openTypeTextStyle(
	metadata *psdtext.Metadata, text string, budget int64,
) (psdfont.Style, [4]float64, int64, error) {
	var empty psdfont.Style
	var color [4]float64
	if metadata == nil || len(metadata.Styles) != 1 || metadata.Styles[0].Start != 0 ||
		metadata.Styles[0].Length != metadata.UTF16Units || len(metadata.Paragraphs) != 1 ||
		metadata.Paragraphs[0].Start != 0 || metadata.Paragraphs[0].Length != metadata.UTF16Units {
		return empty, color, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.style", 0,
			"one complete style and paragraph are required")
	}
	data := metadata.Styles[0].Style.Data
	if data == nil {
		return empty, color, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.style", 0,
			"complete style dictionary required")
	}
	entryBytes := int64(unsafe.Sizeof(psdtext.Entry{}))
	if budget < 2048 || int64(len(data.Entries)) > (budget-2048)/entryBytes {
		return empty, color, 0, sceneError(psdfile.ErrLimit, "textOpenType.style", 0, "owned style copy exceeds budget")
	}
	reserved := 2048 + int64(len(data.Entries))*entryBytes
	ligatures, discretionary := 0, 0
	for _, entry := range data.Entries {
		if entry.Key == "Ligatures" {
			ligatures++
		}
		if entry.Key == "DLigatures" {
			discretionary++
		}
	}
	if ligatures != 1 || discretionary != 1 {
		return empty, color, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.style", 0,
			"source feature projection entries must be explicit and unique")
	}
	owned := *metadata
	run := metadata.Styles[0]
	node := *data
	node.Entries = make([]psdtext.Entry, len(data.Entries))
	copy(node.Entries, data.Entries)
	for index, entry := range node.Entries {
		if entry.Key != "Ligatures" && entry.Key != "DLigatures" {
			continue
		}
		if entry.Value == nil || entry.Value.Kind != psdtext.Boolean {
			return empty, color, 0, sceneError(psdfile.ErrUnsupported, "textOpenType.style", 0,
				"source ligature projection must be Boolean")
		}
		value := *entry.Value
		value.Bool = false
		node.Entries[index].Value = &value
	}
	run.Style.Data = &node
	owned.Styles = []psdtext.StyleRun{run}
	style, color, err := textStyle(&owned, text)
	style.Kerning = false
	return style, color, reserved, err
}
