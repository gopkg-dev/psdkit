// Package psdtext decodes and preserves Photoshop's text EngineData metadata.
// It does not resolve fonts, shape glyphs, lay out paragraphs or render text.
package psdtext

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
)

var (
	// ErrMalformed indicates truncated or inconsistent EngineData syntax or metadata.
	ErrMalformed = errors.New("psdtext: malformed EngineData")
	// ErrUnsupported indicates syntax or text metadata outside the implemented subset.
	ErrUnsupported = errors.New("psdtext: unsupported EngineData")
	// ErrLimit indicates an exceeded allocation, node or nesting limit.
	ErrLimit = errors.New("psdtext: resource limit exceeded")
)

// Error identifies a bounded field without including its possibly private text.
type Error struct {
	Kind   error
	Path   string
	Offset int64
	Detail string
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("%v at %s offset %d: %s", e.Kind, e.Path, e.Offset, e.Detail)
}

// Unwrap exposes the category for errors.Is.
func (e *Error) Unwrap() error { return e.Kind }

// Span identifies original EngineData bytes, adjusted by DecodeOptions.SourceOffset.
type Span struct {
	Offset int64
	Length int64
}

// Kind identifies a non-executable EngineData value.
type Kind uint8

const (
	// Dictionary stores ordered entries, including duplicate property names.
	Dictionary Kind = iota + 1
	// List stores ordered values.
	List
	// Name stores a literal slash-prefixed ASCII name.
	Name
	// Number stores an exact numeric spelling without rounding it to float64.
	Number
	// Boolean stores true or false.
	Boolean
	// String stores a strictly decoded UTF-16BE literal string with its BOM removed.
	String
	// Bytes stores a non-Unicode literal or hex string without assuming a character encoding.
	Bytes
)

// Entry preserves a dictionary key's original order and source location.
type Entry struct {
	Key      string
	KeySpan  Span
	Value    *Node
	keyRaw   []byte
	keyValue string
}

// Node is an owned, editable syntax value. Only the field selected by Kind is meaningful.
// Lexeme preserves integer size, exponent notation and decimal precision for Number.
type Node struct {
	Kind     Kind
	Span     Span
	Entries  []Entry
	Items    []*Node
	Name     string
	Lexeme   string
	Bool     bool
	Text     string
	Data     []byte
	original [32]byte
	encoded  []byte
}

// Lookup returns the last value for a key, while Entries retains any earlier duplicates.
func (n *Node) Lookup(key string) *Node {
	if n == nil || n.Kind != Dictionary {
		return nil
	}
	for _, v := range slices.Backward(n.Entries) {
		if v.Key == key {
			return v.Value
		}
	}
	return nil
}

// Float64 converts a Number only when its exact spelling fits a finite float64.
func (n *Node) Float64() (float64, bool) {
	if n == nil || n.Kind != Number {
		return 0, false
	}
	value, err := strconv.ParseFloat(n.Lexeme, 64)
	return value, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Int64 converts an integer Number without passing through floating-point arithmetic.
func (n *Node) Int64() (int64, bool) {
	if n == nil || n.Kind != Number {
		return 0, false
	}
	value, err := strconv.ParseInt(n.Lexeme, 10, 64)
	return value, err == nil
}

// EngineData retains its editable syntax tree and private, immutable original bytes.
type EngineData struct {
	Root               *Node
	Span               Span
	raw                []byte
	rootBegin, rootEnd int
	implicitRoot       bool
}

// OriginalBytes returns an independent exact copy, including whitespace, comments and padding.
func (e *EngineData) OriginalBytes() []byte {
	if e == nil {
		return nil
	}
	return append([]byte(nil), e.raw...)
}

// DecodeOptions bounds the owned original bytes, syntax nodes and decoded values.
// Zero selects defaults: 32 MiB, 100000 nodes and depth 32; depth may not exceed 256.
type DecodeOptions struct {
	SourceOffset int64
	MaxBytes     int64
	MaxNodes     int
	MaxDepth     int
}

// EncodeOptions bounds validation storage and the owned output bytes.
// Zero selects the same byte, node and depth defaults as DecodeOptions.
type EncodeOptions struct {
	MaxBytes int64
	MaxNodes int
	MaxDepth int
}

func textError(kind error, path string, offset int64, detail string) error {
	return &Error{Kind: kind, Path: path, Offset: offset, Detail: detail}
}

func decodeLimits(options []DecodeOptions) (DecodeOptions, error) {
	result := DecodeOptions{MaxBytes: 32 << 20, MaxNodes: 100_000, MaxDepth: 32}
	if len(options) > 1 {
		return result, textError(ErrMalformed, "options", 0, "provide at most one options value")
	}
	if len(options) == 0 {
		return result, nil
	}
	provided := options[0]
	if provided.SourceOffset < 0 || provided.MaxBytes < 0 || provided.MaxNodes < 0 ||
		provided.MaxDepth < 0 || provided.MaxDepth > 256 {
		return result, textError(ErrLimit, "options", 0, "negative limits or depth above 256")
	}
	result.SourceOffset = provided.SourceOffset
	if provided.MaxBytes != 0 {
		result.MaxBytes = provided.MaxBytes
	}
	if provided.MaxNodes != 0 {
		result.MaxNodes = provided.MaxNodes
	}
	if provided.MaxDepth != 0 {
		result.MaxDepth = provided.MaxDepth
	}
	return result, nil
}
