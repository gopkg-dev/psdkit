package psdtext

import (
	"context"
	"math"
	"unicode/utf8"
	"unsafe"
)

// Font preserves a font's requested PostScript name without selecting an installed substitute.
// Data refers to the owned EngineData node and retains unrecognized font fields.
type Font struct {
	Name                    string
	Script, Type, Synthetic *int64
	Data                    *Node
}

// Color preserves the engine color space tag and ordered component values.
// For native Type=1, Values is alpha followed by red, green and blue.
type Color struct {
	Type   *int64
	Values []float64
	Data   *Node
}

// Style exposes common character properties while Data retains every engine property.
// Nil fields mean absent; no font, leading or kerning defaults are invented.
type Style struct {
	Font                                                *int64
	FontSize, Leading, Tracking, Kerning, BaselineShift *float64
	HorizontalScale, VerticalScale                      *float64
	FauxBold, FauxItalic, AutoLeading, AutoKerning      *bool
	Underline, Strikethrough, Ligatures, NoBreak        *bool
	FillColor, StrokeColor                              *Color
	Data                                                *Node
}

// ParagraphStyle exposes paragraph properties without claiming text layout support.
type ParagraphStyle struct {
	Justification                           *int64
	FirstLineIndent, StartIndent, EndIndent *float64
	SpaceBefore, SpaceAfter, AutoLeading    *float64
	AutoHyphenate, EveryLineComposer        *bool
	Data                                    *Node
}

// StyleRun identifies a UTF-16 code-unit range, including any terminal paragraph marker.
type StyleRun struct {
	Start, Length int64
	Style         Style
	Data          *Node
}

// ParagraphRun identifies a UTF-16 code-unit range and retains paragraph adjustments.
type ParagraphRun struct {
	Start, Length int64
	Style         ParagraphStyle
	Data          *Node
}

// Metadata is a typed view of a decoded engine tree, including its terminal carriage return.
// Data pointers borrow nodes from that owned tree; treat those nodes as read-only while using this view.
// Scalars and slices are independent; unknown properties remain available through the tree.
type Metadata struct {
	Text              string
	UTF16Units        int64
	Fonts             []Font
	Styles            []StyleRun
	Paragraphs        []ParagraphRun
	DefaultStyle      Style
	DefaultParagraph  ParagraphStyle
	Engine, Resources *Node
	DocumentResources *Node
}

// MetadataOptions bounds typed-view allocation and run/font counts independently from parsing.
// Zero selects 32 MiB, 100000 runs and 10000 fonts.
type MetadataOptions struct {
	MaxBytes int64
	MaxRuns  int
	MaxFonts int
}

// Metadata decodes typed text, font, character-run and paragraph-run properties on demand.
// It retains UTF-16 run coordinates rather than incorrectly indexing Go's UTF-8 bytes.
func (e *EngineData) Metadata(ctx context.Context, options ...MetadataOptions) (*Metadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits := MetadataOptions{MaxBytes: 32 << 20, MaxRuns: 100_000, MaxFonts: 10_000}
	if len(options) > 1 {
		return nil, textError(ErrMalformed, "metadata.options", 0, "provide at most one options value")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxBytes < 0 || provided.MaxRuns < 0 || provided.MaxFonts < 0 {
			return nil, textError(ErrLimit, "metadata.options", 0, "limits must not be negative")
		}
		if provided.MaxBytes != 0 {
			limits.MaxBytes = provided.MaxBytes
		}
		if provided.MaxRuns != 0 {
			limits.MaxRuns = provided.MaxRuns
		}
		if provided.MaxFonts != 0 {
			limits.MaxFonts = provided.MaxFonts
		}
	}
	if e == nil || e.Root == nil || e.Root.Kind != Dictionary {
		return nil, textError(ErrMalformed, "metadata", 0, "engine root is not a dictionary")
	}
	reader := metadataReader{ctx: ctx, limits: limits}
	if !reader.reserve(int64(unsafe.Sizeof(Metadata{})), "metadata", e.Root) {
		return nil, reader.err
	}
	engine := e.Root.Lookup("EngineDict")
	if engine == nil || engine.Kind != Dictionary {
		return nil, textError(ErrMalformed, "metadata.EngineDict", e.Root.Span.Offset, "engine dictionary is absent")
	}
	result := &Metadata{Engine: engine, Resources: e.Root.Lookup("ResourceDict"),
		DocumentResources: e.Root.Lookup("DocumentResources")}
	text := engine.Lookup("Editor").Lookup("Text")
	if text == nil || text.Kind != String {
		return nil, textError(ErrMalformed, "metadata.Editor.Text", engine.Span.Offset, "Unicode editor text is absent")
	}
	if !reader.reserve(int64(len(text.Text)), "metadata.Editor.Text", text) {
		return nil, reader.err
	}
	if !utf8.ValidString(text.Text) {
		return nil, textError(ErrMalformed, "metadata.Editor.Text", text.Span.Offset, "edited text is not valid UTF-8")
	}
	result.Text = text.Text
	nextCheck := 0
	for index, value := range result.Text {
		if index >= nextCheck {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			nextCheck = index + 1024
		}
		result.UTF16Units++
		if value > 0xffff {
			result.UTF16Units++
		}
	}
	resources := result.Resources
	if resources == nil {
		resources = result.DocumentResources
	}
	fontSet := resources.Lookup("FontSet")
	if fontSet != nil {
		if fontSet.Kind != List {
			return nil, textError(ErrMalformed, "metadata.FontSet", fontSet.Span.Offset, "font set is not an array")
		}
		if len(fontSet.Items) > limits.MaxFonts ||
			!reader.reserve(int64(len(fontSet.Items))*256, "metadata.FontSet", fontSet) {
			if reader.err == nil {
				reader.fail(ErrLimit, "metadata.FontSet", fontSet, "font count exceeds MaxFonts")
			}
			return nil, reader.err
		}
		result.Fonts = make([]Font, len(fontSet.Items))
		for index, value := range fontSet.Items {
			if !reader.dictionary(value, "metadata.FontSet") {
				return nil, reader.err
			}
			name := value.Lookup("Name")
			if name == nil || name.Kind != String {
				reader.fail(ErrMalformed, "metadata.FontSet.Name", value, "font name is not a Unicode string")
				return nil, reader.err
			}
			result.Fonts[index] = Font{Name: name.Text, Data: value,
				Script: reader.integer(value, "Script"), Type: reader.integer(value, "FontType"),
				Synthetic: reader.integer(value, "Synthetic")}
		}
	}
	styleRun, paragraphRun := engine.Lookup("StyleRun"), engine.Lookup("ParagraphRun")
	result.DefaultStyle = reader.style(styleRun.Lookup("DefaultRunData").Lookup("StyleSheet").Lookup("StyleSheetData"))
	result.DefaultParagraph = reader.paragraph(
		paragraphRun.Lookup("DefaultRunData").Lookup("ParagraphSheet").Lookup("Properties"))
	styleNodes, styleLengths := reader.runs(styleRun, "metadata.StyleRun", result.UTF16Units)
	result.Styles = make([]StyleRun, len(styleNodes))
	start := int64(0)
	for index, value := range styleNodes {
		if !reader.dictionary(value, "metadata.StyleRun.RunArray") {
			return nil, reader.err
		}
		style := reader.style(value.Lookup("StyleSheet").Lookup("StyleSheetData"))
		if style.Font != nil && (*style.Font < 0 || *style.Font >= int64(len(result.Fonts))) {
			reader.fail(ErrMalformed, "metadata.StyleRun.Font", value, "font index is outside FontSet")
		}
		result.Styles[index] = StyleRun{Start: start, Length: styleLengths[index], Style: style, Data: value}
		start += styleLengths[index]
	}
	paragraphNodes, paragraphLengths := reader.runs(paragraphRun, "metadata.ParagraphRun", result.UTF16Units)
	result.Paragraphs = make([]ParagraphRun, len(paragraphNodes))
	start = 0
	for index, value := range paragraphNodes {
		if !reader.dictionary(value, "metadata.ParagraphRun.RunArray") {
			return nil, reader.err
		}
		style := reader.paragraph(value.Lookup("ParagraphSheet").Lookup("Properties"))
		result.Paragraphs[index] = ParagraphRun{Start: start, Length: paragraphLengths[index], Style: style, Data: value}
		start += paragraphLengths[index]
	}
	if reader.err != nil {
		return nil, reader.err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type metadataReader struct {
	ctx       context.Context
	limits    MetadataOptions
	allocated int64
	runCount  int
	err       error
}

func (r *metadataReader) fail(kind error, path string, node *Node, detail string) {
	if r.err == nil {
		offset := int64(0)
		if node != nil {
			offset = node.Span.Offset
		}
		r.err = textError(kind, path, offset, detail)
	}
}

func (r *metadataReader) reserve(length int64, path string, node *Node) bool {
	if r.err == nil {
		r.err = r.ctx.Err()
	}
	if r.err != nil {
		return false
	}
	if length < 0 || length > r.limits.MaxBytes-r.allocated || uint64(length) > uint64(^uint(0)>>1) {
		r.fail(ErrLimit, path, node, "typed metadata exceeds MaxBytes")
		return false
	}
	r.allocated += length
	return true
}

func (r *metadataReader) dictionary(node *Node, path string) bool {
	if r.err == nil {
		r.err = r.ctx.Err()
	}
	if r.err != nil {
		return false
	}
	if node == nil || node.Kind != Dictionary {
		r.fail(ErrMalformed, path, node, "run or font value is not a dictionary")
		return false
	}
	return true
}

func (r *metadataReader) integer(parent *Node, key string) *int64 {
	node := parent.Lookup(key)
	if node == nil || r.err != nil {
		return nil
	}
	value, ok := node.Int64()
	if !ok {
		r.fail(ErrMalformed, "metadata."+key, node, "property is not a bounded integer")
		return nil
	}
	return &value
}

func (r *metadataReader) number(parent *Node, key string) *float64 {
	node := parent.Lookup(key)
	if node == nil || r.err != nil {
		return nil
	}
	value, ok := node.Float64()
	if !ok {
		r.fail(ErrMalformed, "metadata."+key, node, "property is not a finite number")
		return nil
	}
	return &value
}

func (r *metadataReader) boolean(parent *Node, key string) *bool {
	node := parent.Lookup(key)
	if node == nil || r.err != nil {
		return nil
	}
	if node.Kind != Boolean {
		r.fail(ErrMalformed, "metadata."+key, node, "property is not a boolean")
		return nil
	}
	value := node.Bool
	return &value
}

func (r *metadataReader) color(parent *Node, key string) *Color {
	node := parent.Lookup(key)
	if node == nil || r.err != nil {
		return nil
	}
	if !r.dictionary(node, "metadata."+key) {
		return nil
	}
	values := node.Lookup("Values")
	if values == nil || values.Kind != List {
		r.fail(ErrMalformed, "metadata."+key, node, "color has no component array")
		return nil
	}
	if len(values.Items) > 16 {
		r.fail(ErrUnsupported, "metadata."+key, node, "color has more than 16 components")
		return nil
	}
	result := &Color{Type: r.integer(node, "Type"), Values: make([]float64, len(values.Items)), Data: node}
	for index, item := range values.Items {
		value, ok := item.Float64()
		if !ok {
			r.fail(ErrMalformed, "metadata."+key, item, "color component is not finite")
			return nil
		}
		result.Values[index] = value
	}
	return result
}

func (r *metadataReader) style(node *Node) Style {
	if node == nil || r.err != nil {
		return Style{Data: node}
	}
	if !r.dictionary(node, "metadata.StyleSheetData") || !r.reserve(512, "metadata.StyleSheetData", node) {
		return Style{Data: node}
	}
	return Style{Data: node, Font: r.integer(node, "Font"), FontSize: r.number(node, "FontSize"),
		Leading: r.number(node, "Leading"), Tracking: r.number(node, "Tracking"), Kerning: r.number(node, "Kerning"),
		BaselineShift: r.number(node, "BaselineShift"), HorizontalScale: r.number(node, "HorizontalScale"),
		VerticalScale: r.number(node, "VerticalScale"), FauxBold: r.boolean(node, "FauxBold"),
		FauxItalic: r.boolean(node, "FauxItalic"), AutoLeading: r.boolean(node, "AutoLeading"),
		AutoKerning: r.boolean(node, "AutoKerning"), Underline: r.boolean(node, "Underline"),
		Strikethrough: r.boolean(node, "Strikethrough"), Ligatures: r.boolean(node, "Ligatures"),
		NoBreak: r.boolean(node, "NoBreak"), FillColor: r.color(node, "FillColor"),
		StrokeColor: r.color(node, "StrokeColor")}
}

func (r *metadataReader) paragraph(node *Node) ParagraphStyle {
	if node == nil || r.err != nil {
		return ParagraphStyle{Data: node}
	}
	if !r.dictionary(node, "metadata.ParagraphSheet.Properties") ||
		!r.reserve(256, "metadata.ParagraphSheet.Properties", node) {
		return ParagraphStyle{Data: node}
	}
	return ParagraphStyle{Data: node, Justification: r.integer(node, "Justification"),
		FirstLineIndent: r.number(node, "FirstLineIndent"), StartIndent: r.number(node, "StartIndent"),
		EndIndent: r.number(node, "EndIndent"), SpaceBefore: r.number(node, "SpaceBefore"),
		SpaceAfter: r.number(node, "SpaceAfter"), AutoLeading: r.number(node, "AutoLeading"),
		AutoHyphenate: r.boolean(node, "AutoHyphenate"), EveryLineComposer: r.boolean(node, "EveryLineComposer")}
}

func (r *metadataReader) runs(parent *Node, path string, textUnits int64) ([]*Node, []int64) {
	if parent == nil || r.err != nil {
		return nil, nil
	}
	if !r.dictionary(parent, path) {
		return nil, nil
	}
	array, lengths := parent.Lookup("RunArray"), parent.Lookup("RunLengthArray")
	if array == nil && lengths == nil {
		return nil, nil
	}
	if array == nil || lengths == nil || array.Kind != List || lengths.Kind != List ||
		len(array.Items) != len(lengths.Items) {
		r.fail(ErrMalformed, path, parent, "run values and lengths are absent or have different counts")
		return nil, nil
	}
	if len(array.Items) > r.limits.MaxRuns-r.runCount || !r.reserve(int64(len(array.Items))*1024, path, parent) {
		if r.err == nil {
			r.fail(ErrLimit, path, parent, "run count exceeds MaxRuns")
		}
		return nil, nil
	}
	r.runCount += len(array.Items)
	result := make([]int64, len(lengths.Items))
	total := int64(0)
	for index, node := range lengths.Items {
		if err := r.ctx.Err(); err != nil {
			r.err = err
			return nil, nil
		}
		length, ok := node.Int64()
		if !ok || length < 0 || length > math.MaxInt64-total || total+length > textUnits {
			r.fail(ErrMalformed, path, node, "run length exceeds Unicode text or integer bounds")
			return nil, nil
		}
		total += length
		result[index] = length
	}
	if total != textUnits {
		r.fail(ErrMalformed, path, parent, "run lengths do not cover the editor's UTF-16 code units")
		return nil, nil
	}
	return array.Items, result
}
