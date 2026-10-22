package psdfile

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// DescriptorEncodeOptions bounds descriptor traversal, scratch data and encoded output.
// The relevant Limits fields are MaxMetadataBytes, MaxReadBytes, MaxDescriptorDepth
// and MaxDescriptorItems. Zero values inherit the source document's limits, or
// DefaultLimits for standalone encoding. MaxDescriptorDepth may not exceed 256.
// MaxEncodedBytes defaults to MaxMetadataBytes and bounds the returned payload;
// it is not a bound on total process memory or caller-owned values.
type DescriptorEncodeOptions struct {
	Limits          Limits
	MaxEncodedBytes int64
}

// EncodeDescriptor encodes ordered typed values without requiring a source document.
// It accepts the types returned by DecodeDescriptor, with []byte also accepted for
// alis, tdta and Pth payloads. Nonempty DescriptorBlob values require the document method.
// Strings use UTF-16 with a counted terminating NUL; identifiers use their byte values.
// Versioned descriptors default Version zero to 16 and reject other versions.
func EncodeDescriptor(
	ctx context.Context, descriptor *Descriptor, versioned bool, options ...DescriptorEncodeOptions,
) ([]byte, error) {
	return encodeDescriptor(ctx, nil, descriptor, versioned, options)
}

// EncodeDescriptor encodes current typed values while retaining unchanged source spellings.
// Entries and duplicate keys retain their order. Source spans are hints, never overrides:
// changed values are encoded even when their original spans remain attached. Unchanged
// identifiers, Unicode strings and boolean bytes retain their original representation.
// Changed Unicode strings retain the source field's terminating-NUL convention.
// DescriptorUnit.Number and Bits may each be edited: comparison with the source
// identifies which changed. Conflicting changes are rejected. New units may leave
// Bits zero when Number is supplied, or leave Number nil to supply exact Bits.
// Float bits, including signed zero and NaN payloads, are retained without conversion.
// Only explicit blob values read their payloads; unrelated source blocks are never read.
// The caller must not mutate the supplied values during encoding.
func (d *Document) EncodeDescriptor(
	ctx context.Context, descriptor *Descriptor, versioned bool, options ...DescriptorEncodeOptions,
) ([]byte, error) {
	if d == nil {
		return nil, fileError(ErrMalformed, "descriptor.encode", "descriptor", 0, "missing source document")
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	return encodeDescriptor(ctx, d, descriptor, versioned, options)
}

type descriptorEncoder struct {
	ctx      context.Context
	source   *Document
	limits   Limits
	limit    int64
	metadata int64
	items    int
	output   []byte
	err      error
}

func encodeDescriptor(
	ctx context.Context, source *Document, descriptor *Descriptor, versioned bool,
	options []DescriptorEncodeOptions,
) ([]byte, error) {
	ctx = decodeContext(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, outputLimit, err := descriptorEncodeLimits(source, options)
	if err != nil {
		return nil, err
	}
	encoder := descriptorEncoder{ctx: ctx, source: source, limits: limits, limit: outputLimit}
	if descriptor == nil {
		encoder.fail(ErrMalformed, "descriptor", "missing descriptor")
		return nil, encoder.err
	}
	if versioned {
		version := descriptor.Version
		if version == 0 {
			version = 16
		}
		if version != 16 {
			encoder.fail(ErrUnsupported, "descriptor.version", "unsupported descriptor version")
			return nil, encoder.err
		}
		encoder.u32(version, "descriptor.version")
	}
	encoder.descriptor(descriptor, 1, "descriptor", false)
	if !encoder.check() {
		return nil, encoder.err
	}
	return encoder.output, nil
}

func descriptorEncodeLimits(source *Document, options []DescriptorEncodeOptions) (Limits, int64, error) {
	baseline := DefaultLimits()
	if source != nil {
		baseline = source.limits
	}
	if len(options) > 1 {
		return Limits{}, 0, fileError(
			ErrMalformed, "descriptor.encode", "options", 0, "provide at most one DescriptorEncodeOptions",
		)
	}
	var supplied DescriptorEncodeOptions
	if len(options) == 1 {
		supplied = options[0]
	}
	limits, err := loadLimits([]LoadOptions{{Limits: supplied.Limits}})
	if err != nil {
		return Limits{}, 0, err
	}
	if supplied.Limits.MaxMetadataBytes == 0 {
		limits.MaxMetadataBytes = baseline.MaxMetadataBytes
	}
	if supplied.Limits.MaxReadBytes == 0 {
		limits.MaxReadBytes = baseline.MaxReadBytes
	}
	if supplied.Limits.MaxDescriptorDepth == 0 {
		limits.MaxDescriptorDepth = baseline.MaxDescriptorDepth
	}
	if supplied.Limits.MaxDescriptorItems == 0 {
		limits.MaxDescriptorItems = baseline.MaxDescriptorItems
	}
	if limits.MaxDescriptorDepth > 256 {
		return Limits{}, 0, fileError(
			ErrLimit, "descriptor.encode", "options.Limits.MaxDescriptorDepth", 0, "depth may not exceed 256",
		)
	}
	if supplied.MaxEncodedBytes < 0 {
		return Limits{}, 0, fileError(
			ErrLimit, "descriptor.encode", "options.MaxEncodedBytes", 0, "limit must not be negative",
		)
	}
	outputLimit := supplied.MaxEncodedBytes
	if outputLimit == 0 {
		outputLimit = limits.MaxMetadataBytes
	}
	return limits, min(outputLimit, int64(^uint(0)>>1)), nil
}

func (e *descriptorEncoder) fail(kind error, path, detail string) {
	if e.err == nil {
		e.err = fileError(kind, "descriptor.encode", path, int64(len(e.output)), detail)
	}
}

func (e *descriptorEncoder) check() bool {
	if e.err == nil {
		e.err = e.ctx.Err()
	}
	return e.err == nil
}

func (e *descriptorEncoder) reserveMetadata(length int64, path string) bool {
	if !e.check() {
		return false
	}
	if length < 0 || length > e.limits.MaxMetadataBytes-e.metadata {
		e.fail(ErrLimit, path, "descriptor traversal exceeds MaxMetadataBytes")
		return false
	}
	e.metadata += length
	return true
}

func (e *descriptorEncoder) count(length int, itemBytes int64, path string) bool {
	if !e.check() {
		return false
	}
	if uint64(length) > math.MaxUint32 || length > e.limits.MaxDescriptorItems-e.items {
		e.fail(ErrLimit, path, "descriptor count exceeds MaxDescriptorItems or uint32")
		return false
	}
	if !e.reserveMetadata(int64(length)*itemBytes, path) {
		return false
	}
	e.items += length
	e.u32(uint32(length), path)
	return e.err == nil
}

func (e *descriptorEncoder) depth(depth int, path string) bool {
	if !e.check() {
		return false
	}
	if depth > e.limits.MaxDescriptorDepth {
		e.fail(ErrLimit, path, "descriptor nesting exceeds MaxDescriptorDepth")
	}
	return e.err == nil
}

func (e *descriptorEncoder) appendSpace(length int64, path string) []byte {
	if !e.check() {
		return nil
	}
	if length < 0 || length > e.limit-int64(len(e.output)) {
		e.fail(ErrLimit, path, "descriptor output exceeds MaxEncodedBytes")
		return nil
	}
	start := len(e.output)
	needed := start + int(length)
	if needed > cap(e.output) {
		capacity := min(e.limit, max(int64(needed), int64(cap(e.output))*2, 256))
		storage := make([]byte, needed, int(capacity))
		copy(storage, e.output)
		e.output = storage
	} else {
		e.output = e.output[:needed]
	}
	return e.output[start:]
}

func (e *descriptorEncoder) bytes(data []byte, path string) {
	copy(e.appendSpace(int64(len(data)), path), data)
}

func (e *descriptorEncoder) u32(value uint32, path string) {
	data := e.appendSpace(4, path)
	if data != nil {
		binary.BigEndian.PutUint32(data, value)
	}
}

func (e *descriptorEncoder) u64(value uint64, path string) {
	data := e.appendSpace(8, path)
	if data != nil {
		binary.BigEndian.PutUint64(data, value)
	}
}

func (e *descriptorEncoder) signature(value, path string) {
	if len(value) != 4 {
		e.fail(ErrMalformed, path, "expected a four-byte signature")
		return
	}
	e.bytes([]byte(value), path)
}

func (e *descriptorEncoder) sourceField(span Span, path string) []byte {
	if e.source == nil || span.Length == 0 || !e.check() {
		return nil
	}
	if !e.source.validSpan(span) {
		e.fail(ErrMalformed, path, "source field span is outside the document")
		return nil
	}
	if span.Length > e.limits.MaxReadBytes || span.Length > int64(^uint(0)>>1) {
		e.fail(ErrLimit, path, "source field exceeds MaxReadBytes")
		return nil
	}
	if !e.reserveMetadata(span.Length, path) {
		return nil
	}
	data := make([]byte, int(span.Length))
	e.readSource(data, span, path)
	return data
}

func (e *descriptorEncoder) readSource(data []byte, span Span, path string) {
	if !e.check() {
		return
	}
	chunkLimit := min(int64(64<<10), e.limits.MaxReadBytes)
	for start := int64(0); start < int64(len(data)); {
		if !e.check() {
			return
		}
		end := min(start+chunkLimit, int64(len(data)))
		part := data[start:end]
		count, err := e.source.reader.ReadAt(part, span.Offset+start)
		if count != len(part) || err != nil && !errors.Is(err, io.EOF) {
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			e.err = &Error{
				Kind: ErrMalformed, Feature: "descriptor.encode", Path: path, Offset: span.Offset + start,
				Detail: "original descriptor bytes could not be read", Cause: err,
			}
			return
		}
		start = end
	}
}

func (e *descriptorEncoder) identifier(value string, span Span, path string) {
	if len(value) == 0 || uint64(len(value)) > math.MaxUint32 {
		e.fail(ErrMalformed, path, "identifier must contain one to uint32 bytes")
		return
	}
	if !e.reserveMetadata(int64(len(value)), path) {
		return
	}
	compact := len(value) == 4
	raw := e.sourceField(span, path)
	if raw != nil && e.err == nil {
		if len(raw) < 4 {
			e.fail(ErrMalformed, path, "invalid original identifier encoding")
			return
		}
		length := binary.BigEndian.Uint32(raw)
		originalLength := length
		if length == 0 {
			length = 4
		}
		if uint64(length) != uint64(len(raw)-4) {
			e.fail(ErrMalformed, path, "invalid original identifier length")
			return
		}
		if string(raw[4:]) == value {
			e.bytes(raw, path)
			return
		}
		compact = originalLength == 0 && len(value) == 4
	}
	length := uint32(len(value))
	if compact {
		length = 0
	}
	e.u32(length, path)
	e.bytes([]byte(value), path)
}

func (e *descriptorEncoder) unicode(value string, span Span, path string) {
	if !e.reserveMetadata(int64(len(value)), path) {
		return
	}
	var count uint64
	nextCheck := 0
	for offset := 0; offset < len(value); {
		if offset >= nextCheck {
			if !e.check() {
				return
			}
			nextCheck = offset + 4096
		}
		character, width := utf8.DecodeRuneInString(value[offset:])
		if character == utf8.RuneError && width == 1 {
			e.fail(ErrMalformed, path, "Unicode value is not valid UTF-8")
			return
		}
		count++
		if character > 0xffff {
			count++
		}
		offset += width
	}
	terminated := true
	raw := e.sourceField(span, path)
	if raw != nil && e.err == nil {
		if len(raw) < 4 || uint64(binary.BigEndian.Uint32(raw))*2 != uint64(len(raw)-4) {
			e.fail(ErrMalformed, path, "invalid original Unicode length")
			return
		}
		if descriptorUnicodeEqual(raw, value) {
			e.bytes(raw, path)
			return
		}
		terminated = len(raw) >= 6 && binary.BigEndian.Uint16(raw[len(raw)-2:]) == 0
	}
	terminated = terminated || strings.HasSuffix(value, "\x00")
	if terminated {
		count++
	}
	if count > math.MaxUint32 {
		e.fail(ErrLimit, path, "Unicode length exceeds uint32")
		return
	}
	e.u32(uint32(count), path)
	data := e.appendSpace(int64(count)*2, path)
	if data == nil {
		return
	}
	offset := 0
	nextCheck = 0
	for _, character := range value {
		if offset >= nextCheck {
			if !e.check() {
				return
			}
			nextCheck = offset + 4096
		}
		if character <= 0xffff {
			binary.BigEndian.PutUint16(data[offset:], uint16(character))
			offset += 2
			continue
		}
		first, second := utf16.EncodeRune(character)
		binary.BigEndian.PutUint16(data[offset:], uint16(first))
		binary.BigEndian.PutUint16(data[offset+2:], uint16(second))
		offset += 4
	}
	if terminated {
		binary.BigEndian.PutUint16(data[offset:], 0)
	}
}

func descriptorUnicodeEqual(raw []byte, value string) bool {
	valueOffset := 0
	for offset := 4; offset < len(raw); offset += 2 {
		code := binary.BigEndian.Uint16(raw[offset:])
		if code == 0 && offset == len(raw)-2 {
			break
		}
		character := rune(code)
		if utf16.IsSurrogate(character) {
			character = utf8.RuneError
			if code >= 0xd800 && code <= 0xdbff && offset+2 < len(raw) {
				next := binary.BigEndian.Uint16(raw[offset+2:])
				if next >= 0xdc00 && next <= 0xdfff {
					character = utf16.DecodeRune(rune(code), rune(next))
					offset += 2
				}
			}
		}
		if valueOffset == len(value) {
			return false
		}
		actual, width := utf8.DecodeRuneInString(value[valueOffset:])
		if actual != character {
			return false
		}
		valueOffset += width
	}
	return valueOffset == len(value)
}

func (e *descriptorEncoder) class(value DescriptorClass, path string) {
	if !e.reserveMetadata(int64(unsafe.Sizeof(value)), path) {
		return
	}
	e.unicode(value.Name, value.NameData, path+".name")
	e.identifier(value.ClassID, value.ClassIDData, path+".classID")
}

func (e *descriptorEncoder) descriptor(value *Descriptor, depth int, path string, array bool) {
	if !e.depth(depth, path) {
		return
	}
	if value == nil {
		e.fail(ErrMalformed, path, "missing nested descriptor")
		return
	}
	if !e.reserveMetadata(int64(unsafe.Sizeof(*value)), path) {
		return
	}
	e.class(DescriptorClass{
		Name: value.Name, ClassID: value.ClassID, NameData: value.NameData, ClassIDData: value.ClassIDData,
	}, path)
	e.entries(value.Entries, depth, path, array)
}

func (e *descriptorEncoder) entries(entries []DescriptorEntry, depth int, path string, array bool) {
	if !e.count(len(entries), int64(unsafe.Sizeof(DescriptorEntry{})), path+".entries") {
		return
	}
	for index, entry := range entries {
		itemPath := fmt.Sprintf("%s.entries[%d]", path, index)
		e.identifier(entry.Key, entry.KeyData, itemPath+".key")
		e.typedValue(entry.Type, entry.Value, entry.Data, depth, itemPath, array, false)
		if e.err != nil {
			return
		}
	}
}

func (e *descriptorEncoder) typedValue(
	kind string, value any, data Span, depth int, path string, array, reference bool,
) {
	if !e.check() {
		return
	}
	e.signature(kind, path+".type")
	if reference {
		e.referenceValue(kind, value, path)
		return
	}
	if array && kind == "UnFl" {
		unitArray, valid := value.(DescriptorUnitArray)
		if !valid {
			e.fail(ErrMalformed, path, "ObAr UnFl requires DescriptorUnitArray")
			return
		}
		e.unitArray(unitArray, path)
		return
	}
	e.value(kind, value, data, depth, path)
}

func (e *descriptorEncoder) value(kind string, value any, data Span, depth int, path string) {
	if !e.reserveMetadata(16, path) {
		return
	}
	switch kind {
	case "Objc", "GlbO":
		descriptor, valid := value.(*Descriptor)
		if valid {
			e.descriptor(descriptor, depth+1, path, false)
			return
		}
	case "VlLs":
		items, valid := value.([]DescriptorValue)
		if valid {
			e.list(items, depth+1, path, false)
			return
		}
	case "obj ":
		reference, valid := value.(*DescriptorReference)
		if valid && reference != nil {
			e.list(reference.Items, depth+1, path, true)
			return
		}
	case "ObAr":
		array, valid := value.(*DescriptorObjectArray)
		if valid && array != nil {
			if !e.depth(depth+1, path) || !e.reserveMetadata(int64(unsafe.Sizeof(*array)), path) {
				return
			}
			e.u32(array.Prefix, path+".prefix")
			e.class(DescriptorClass{
				Name: array.Name, ClassID: array.ClassID, NameData: array.NameData, ClassIDData: array.ClassIDData,
			}, path)
			e.entries(array.Entries, depth+1, path, true)
			return
		}
	case "doub":
		number, valid := value.(float64)
		if valid {
			e.u64(math.Float64bits(number), path)
			return
		}
	case "UntF", "UnFl":
		unit, valid := value.(DescriptorUnit)
		if valid {
			e.unit(unit, kind, path)
			return
		}
	case "TEXT":
		text, valid := value.(string)
		if valid {
			e.unicode(text, data, path)
			return
		}
	case "enum":
		enumeration, valid := value.(DescriptorEnum)
		if valid {
			e.enum(enumeration, path)
			return
		}
	case "long":
		number, valid := value.(int32)
		if valid {
			e.u32(uint32(number), path)
			return
		}
	case "comp":
		number, valid := value.(int64)
		if valid {
			e.u64(uint64(number), path)
			return
		}
	case "bool":
		boolean, valid := value.(bool)
		if valid {
			raw := e.sourceField(data, path)
			if raw != nil && len(raw) != 1 {
				e.fail(ErrMalformed, path, "invalid original boolean encoding")
				return
			}
			if len(raw) == 1 && (raw[0] != 0) == boolean {
				e.bytes(raw, path)
				return
			}
			encoded := byte(0)
			if boolean {
				encoded = 1
			}
			e.bytes([]byte{encoded}, path)
			return
		}
	case "type", "GlbC":
		class, valid := value.(DescriptorClass)
		if valid {
			e.class(class, path)
			return
		}
	case "alis", "tdta", "Pth ":
		switch blob := value.(type) {
		case DescriptorBlob:
			e.blob(blob, path)
			return
		case []byte:
			if uint64(len(blob)) > math.MaxUint32 {
				e.fail(ErrLimit, path, "blob length exceeds uint32")
				return
			}
			e.u32(uint32(len(blob)), path+".length")
			e.bytes(blob, path)
			return
		}
	default:
		e.fail(ErrUnsupported, path+".type", "unsupported descriptor OSType")
		return
	}
	e.fail(ErrMalformed, path, "value does not match descriptor OSType")
}

func (e *descriptorEncoder) list(items []DescriptorValue, depth int, path string, reference bool) {
	if !e.depth(depth, path) || !e.count(len(items), int64(unsafe.Sizeof(DescriptorValue{})), path+".items") {
		return
	}
	for index, item := range items {
		itemPath := fmt.Sprintf("%s.items[%d]", path, index)
		e.typedValue(item.Type, item.Value, item.Data, depth, itemPath, false, reference)
		if e.err != nil {
			return
		}
	}
}

func (e *descriptorEncoder) unit(value DescriptorUnit, kind, path string) {
	width := uint8(64)
	if kind == "UnFl" {
		width = 32
	}
	if value.Width != 0 && value.Width != width {
		e.fail(ErrMalformed, path+".width", "unit number width differs from its OSType")
		return
	}
	bits := value.Bits
	if width == 32 && bits > math.MaxUint32 {
		e.fail(ErrMalformed, path+".number", "UnFl bits exceed uint32")
		return
	}
	if value.Number != nil {
		if width == 32 {
			number, valid := value.Number.(float32)
			if !valid {
				e.fail(ErrMalformed, path+".number", "UnFl requires a float32 number")
				return
			}
			bits = uint64(math.Float32bits(number))
		} else {
			number, valid := value.Number.(float64)
			if !valid {
				e.fail(ErrMalformed, path+".number", "UntF requires a float64 number")
				return
			}
			bits = math.Float64bits(number)
		}
		if bits != value.Bits {
			raw := e.sourceField(value.NumberData, path+".number")
			if e.err != nil {
				return
			}
			if raw != nil {
				if len(raw) != int(width)/8 {
					e.fail(ErrMalformed, path+".number", "original unit width differs from its OSType")
					return
				}
				originalBits := uint64(0)
				if width == 32 {
					originalBits = uint64(binary.BigEndian.Uint32(raw))
				} else {
					originalBits = binary.BigEndian.Uint64(raw)
				}
				switch {
				case value.Bits == originalBits:
				case bits == originalBits:
					bits = value.Bits
				default:
					e.fail(ErrMalformed, path+".number", "conflicting unit Number and Bits changes")
					return
				}
			} else if value.Bits != 0 {
				e.fail(ErrMalformed, path+".number", "unit Number and Bits disagree without source provenance")
				return
			}
		}
	}
	e.signature(value.Unit, path+".unit")
	if width == 32 {
		e.u32(uint32(bits), path+".number")
		return
	}
	e.u64(bits, path+".number")
}

func (e *descriptorEncoder) enum(value DescriptorEnum, path string) {
	e.identifier(value.TypeID, value.TypeData, path+".typeID")
	e.identifier(value.ValueID, value.ValueData, path+".valueID")
}

func (e *descriptorEncoder) blob(value DescriptorBlob, path string) {
	if value.Data.Length < 0 || uint64(value.Data.Length) > math.MaxUint32 {
		e.fail(ErrLimit, path, "blob length exceeds uint32")
		return
	}
	if value.Data.Length != 0 && e.source == nil {
		e.fail(ErrUnsupported, path, "span-backed blob requires a source document")
		return
	}
	if e.source != nil && !e.source.validSpan(value.Data) {
		e.fail(ErrMalformed, path, "blob span is outside the document")
		return
	}
	e.u32(uint32(value.Data.Length), path+".length")
	data := e.appendSpace(value.Data.Length, path)
	if len(data) != 0 {
		e.readSource(data, value.Data, path)
	}
}

func (e *descriptorEncoder) referenceValue(kind string, value any, path string) {
	switch kind {
	case "prop":
		property, valid := value.(DescriptorProperty)
		if valid {
			e.class(property.Class, path+".class")
			e.identifier(property.KeyID, property.KeyData, path+".keyID")
			return
		}
	case "Clss":
		class, valid := value.(DescriptorClass)
		if valid {
			e.class(class, path)
			return
		}
	case "Enmr":
		enumerated, valid := value.(DescriptorEnumeratedReference)
		if valid {
			e.class(enumerated.Class, path+".class")
			e.enum(enumerated.Enum, path+".enum")
			return
		}
	case "rele":
		offset, valid := value.(DescriptorOffset)
		if valid {
			e.class(offset.Class, path+".class")
			e.u32(offset.Offset, path+".offset")
			return
		}
	case "Idnt", "indx":
		number, valid := value.(int32)
		if valid {
			e.u32(uint32(number), path)
			return
		}
	case "name":
		named, valid := value.(DescriptorNamedReference)
		if valid {
			e.class(named.Class, path+".class")
			e.unicode(named.Name, named.NameData, path+".name")
			return
		}
	default:
		e.fail(ErrUnsupported, path+".type", "unsupported reference OSType")
		return
	}
	e.fail(ErrMalformed, path, "value does not match reference OSType")
}

func (e *descriptorEncoder) unitArray(value DescriptorUnitArray, path string) {
	e.signature(value.Unit, path+".unit")
	if !e.count(len(value.Numbers), 8, path+".numbers") {
		return
	}
	for _, number := range value.Numbers {
		e.u64(math.Float64bits(number), path+".numbers")
		if e.err != nil {
			return
		}
	}
}
