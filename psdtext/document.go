package psdtext

import (
	"context"
	"math"
	"unicode/utf8"
	"unsafe"
)

// DocumentTextRun retains a schema-7 text run and its UTF-16 code-unit range.
// Properties exposes its raw numeric-key dictionary without guessing private styling semantics.
type DocumentTextRun struct {
	Start, Length    int64
	Properties, Data *Node
}

// DocumentTextRecord identifies an entry by the zero-based TySh TextIndex.
// Text includes the native terminal carriage return; Shapes is retained private rendering state.
// The record does not resolve fonts, shape glyphs or claim that its saved shape data is current after edits.
type DocumentTextRecord struct {
	Index                                    int
	Text                                     string
	UTF16Units                               int64
	HasParagraphAdjustments                  bool
	Paragraphs, Styles, ParagraphAdjustments []DocumentTextRun
	Shapes, ShapeReferences, Data            *Node
}

// DocumentMetadata is a typed view of the observed schema-7 Txt2 text records.
// Data, Resources and Engine borrow the owned syntax tree and preserve unknown entries in source order.
type DocumentMetadata struct {
	Schema                  int64
	Texts                   []DocumentTextRecord
	Data, Resources, Engine *Node
}

// DocumentMetadataOptions bounds typed-view allocation and all three run arrays independently from parsing.
// Zero selects 32 MiB, 10000 text records and 100000 total runs.
type DocumentMetadataOptions struct {
	MaxBytes          int64
	MaxTexts, MaxRuns int
}

// DocumentMetadata reads schema-7 Txt2 text records and validates complete UTF-16 run coverage.
// Unknown schema versions return ErrUnsupported while the original tree and bytes remain available.
func (e *EngineData) DocumentMetadata(
	ctx context.Context, options ...DocumentMetadataOptions,
) (*DocumentMetadata, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits := DocumentMetadataOptions{MaxBytes: 32 << 20, MaxTexts: 10000, MaxRuns: 100000}
	if len(options) > 1 {
		return nil, textError(ErrMalformed, "documentText.options", 0, "provide at most one options value")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxBytes < 0 || provided.MaxTexts < 0 || provided.MaxRuns < 0 {
			return nil, textError(ErrLimit, "documentText.options", 0, "limits must not be negative")
		}
		if provided.MaxBytes != 0 {
			limits.MaxBytes = provided.MaxBytes
		}
		if provided.MaxTexts != 0 {
			limits.MaxTexts = provided.MaxTexts
		}
		if provided.MaxRuns != 0 {
			limits.MaxRuns = provided.MaxRuns
		}
	}
	if e == nil || e.Root == nil || e.Root.Kind != Dictionary {
		return nil, textError(ErrMalformed, "documentText", 0, "document text root is not a dictionary")
	}
	reader := documentReader{ctx: ctx, limits: limits}
	if !reader.reserve(int64(unsafe.Sizeof(DocumentMetadata{})), e.Root) {
		return nil, reader.err
	}
	version := reader.value(reader.value(e.Root, "98", Dictionary), "0", Number)
	schema, ok := version.Int64()
	if reader.err != nil {
		return nil, reader.err
	}
	if !ok || schema != 7 {
		return nil, textError(ErrUnsupported, "documentText.schema", version.Span.Offset, "private text schema is not version 7")
	}
	engine := reader.value(e.Root, "1", Dictionary)
	texts := reader.value(engine, "1", List)
	resources := reader.value(e.Root, "0", Dictionary)
	if reader.err != nil {
		return nil, reader.err
	}
	if len(texts.Items) > limits.MaxTexts {
		return nil, textError(ErrLimit, "documentText.texts", texts.Span.Offset, "text count exceeds MaxTexts")
	}
	if !reader.reserve(int64(len(texts.Items))*int64(unsafe.Sizeof(DocumentTextRecord{})), texts) {
		return nil, reader.err
	}
	result := &DocumentMetadata{Schema: schema, Data: e.Root, Resources: resources, Engine: engine,
		Texts: make([]DocumentTextRecord, len(texts.Items))}
	for index, source := range texts.Items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		contents := reader.value(source, "0", Dictionary)
		text := reader.value(contents, "0", String)
		if reader.err != nil {
			return nil, reader.err
		}
		if !reader.reserve(int64(len(text.Text)), text) {
			return nil, reader.err
		}
		if !utf8.ValidString(text.Text) {
			return nil, textError(ErrMalformed, "documentText.text", text.Span.Offset, "text is not valid UTF-8")
		}
		units := int64(0)
		nextCheck := 0
		for byteIndex, character := range text.Text {
			if byteIndex >= nextCheck {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				nextCheck = byteIndex + 1024
			}
			units++
			if character > 0xffff {
				units++
			}
		}
		rendered := reader.value(source, "1", Dictionary)
		record := DocumentTextRecord{Index: index, Text: text.Text, UTF16Units: units, Data: source}
		record.Paragraphs = reader.runs(reader.value(contents, "5", Dictionary), text.Text, units)
		record.Styles = reader.runs(reader.value(contents, "6", Dictionary), text.Text, units)
		adjustments, present := reader.optionalValue(rendered, "1", Dictionary)
		record.HasParagraphAdjustments = present
		if present {
			record.ParagraphAdjustments = reader.runs(adjustments, text.Text, units)
		}
		record.Shapes = reader.value(rendered, "2", List)
		record.ShapeReferences = reader.value(rendered, "0", List)
		if reader.err != nil {
			return nil, reader.err
		}
		result.Texts[index] = record
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

type documentReader struct {
	ctx       context.Context
	limits    DocumentMetadataOptions
	allocated int64
	runCount  int
	err       error
}

func (r *documentReader) fail(kind error, node *Node, detail string) {
	if r.err != nil {
		return
	}
	offset := int64(0)
	if node != nil {
		offset = node.Span.Offset
	}
	r.err = textError(kind, "documentText", offset, detail)
}

func (r *documentReader) reserve(size int64, node *Node) bool {
	if r.err == nil {
		r.err = r.ctx.Err()
	}
	if r.err != nil {
		return false
	}
	if size < 0 || size > r.limits.MaxBytes-r.allocated {
		r.fail(ErrLimit, node, "typed document text exceeds MaxBytes")
		return false
	}
	r.allocated += size
	return true
}

func (r *documentReader) value(node *Node, key string, kind Kind) *Node {
	result, present := r.optionalValue(node, key, kind)
	if r.err == nil && !present {
		r.fail(ErrMalformed, node, "missing numeric-key property")
	}
	return result
}

func (r *documentReader) optionalValue(node *Node, key string, kind Kind) (*Node, bool) {
	if r.err != nil {
		return nil, false
	}
	if node == nil || node.Kind != Dictionary {
		r.fail(ErrMalformed, node, "expected a numeric-key dictionary")
		return nil, false
	}
	var result *Node
	found := false
	for index, entry := range node.Entries {
		if index%1024 == 0 {
			if err := r.ctx.Err(); err != nil {
				r.err = err
				return nil, false
			}
		}
		if entry.Key != key {
			continue
		}
		if found {
			r.fail(ErrUnsupported, entry.Value, "duplicate critical numeric-key property")
			return nil, false
		}
		found = true
		result = entry.Value
	}
	if !found {
		return nil, false
	}
	if result == nil || result.Kind != kind {
		r.fail(ErrMalformed, result, "unexpected numeric-key property type")
		return nil, true
	}
	return result, true
}

func (r *documentReader) runs(parent *Node, text string, units int64) []DocumentTextRun {
	array := r.value(parent, "0", List)
	if r.err != nil {
		return nil
	}
	if len(array.Items) > r.limits.MaxRuns-r.runCount {
		r.fail(ErrLimit, array, "run count exceeds MaxRuns")
		return nil
	}
	if !r.reserve(int64(len(array.Items))*int64(unsafe.Sizeof(DocumentTextRun{})), array) {
		return nil
	}
	r.runCount += len(array.Items)
	result := make([]DocumentTextRun, len(array.Items))
	position := int64(0)
	for index, source := range array.Items {
		properties := r.value(source, "0", Dictionary)
		lengthNode := r.value(source, "1", Number)
		if r.err != nil {
			return nil
		}
		length, ok := lengthNode.Int64()
		if !ok || length < 0 || length > math.MaxInt64-position || length > units-position {
			r.fail(ErrMalformed, lengthNode, "run length exceeds Unicode text or integer bounds")
			return nil
		}
		result[index] = DocumentTextRun{Start: position, Length: length, Properties: properties, Data: source}
		position += length
	}
	if position != units {
		r.fail(ErrMalformed, array, "run lengths do not cover the UTF-16 text")
		return nil
	}
	// Validate endpoints without allocating a second UTF-16 text copy.
	endpoint, unitPosition := 0, int64(0)
	nextCheck := 0
	for byteIndex, character := range text {
		if byteIndex >= nextCheck {
			if err := r.ctx.Err(); err != nil {
				r.err = err
				return nil
			}
			nextCheck = byteIndex + 1024
		}
		width := int64(1)
		if character > 0xffff {
			width = 2
		}
		for endpoint < len(result) && result[endpoint].Start+result[endpoint].Length <= unitPosition {
			endpoint++
		}
		if endpoint < len(result) {
			end := result[endpoint].Start + result[endpoint].Length
			if end > unitPosition && end < unitPosition+width {
				r.fail(ErrMalformed, array, "run boundary splits a UTF-16 surrogate pair")
				return nil
			}
		}
		unitPosition += width
	}
	return result
}
