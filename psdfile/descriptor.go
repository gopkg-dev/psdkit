package psdfile

import (
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

// Descriptor retains descriptor entries in source order, including duplicate keys.
// Span includes the version prefix when DecodeDescriptor is called with versioned=true.
// NameData and ClassIDData include their original length words.
type Descriptor struct {
	Name, ClassID                                       string
	Version                                             uint32
	Entries                                             []DescriptorEntry
	Span, VersionData, NameData, ClassIDData, CountData Span
}

// DescriptorEntry preserves a key, OSType, typed value and its original encoded spans.
// KeyData includes the length word; Data excludes the key and four-byte OSType.
type DescriptorEntry struct {
	Key, Type                     string
	Value                         any
	Span, KeyData, TypeData, Data Span
}

// DescriptorValue is a typed list or reference item with original source spans.
// Span includes its four-byte OSType and Data identifies only its value encoding.
type DescriptorValue struct {
	Type                 string
	Value                any
	Span, TypeData, Data Span
}

// DescriptorClass preserves a class name and ID without normalizing its encoding.
type DescriptorClass struct {
	Name, ClassID               string
	Span, NameData, ClassIDData Span
}

// DescriptorEnum keeps enumeration type and value IDs as separate fields.
type DescriptorEnum struct {
	TypeID, ValueID           string
	Span, TypeData, ValueData Span
}

// DescriptorUnit retains a scalar unit number and the exact original IEEE bits.
// Width is 32 for UnFl and 64 for UntF; Number has the matching float32 or float64 type.
type DescriptorUnit struct {
	Unit                       string
	Width                      uint8
	Number                     any
	Bits                       uint64
	Span, UnitData, NumberData Span
}

// DescriptorBlob indexes an alias, raw-data or path payload without allocating its bytes.
// Span includes the length word, while Data identifies only the original blob.
type DescriptorBlob struct {
	Span, Data Span
}

// DescriptorReference preserves the order and types of Action reference items.
type DescriptorReference struct {
	Items           []DescriptorValue
	Span, CountData Span
}

// DescriptorProperty is a property reference's class and key identifier.
type DescriptorProperty struct {
	Class   DescriptorClass
	KeyID   string
	KeyData Span
}

// DescriptorEnumeratedReference associates an enumeration with its referenced class.
type DescriptorEnumeratedReference struct {
	Class DescriptorClass
	Enum  DescriptorEnum
}

// DescriptorOffset is a class-relative reference offset preserving its unsigned wire value.
type DescriptorOffset struct {
	Class      DescriptorClass
	Offset     uint32
	OffsetData Span
}

// DescriptorNamedReference retains both class name and referenced object name.
type DescriptorNamedReference struct {
	Class    DescriptorClass
	Name     string
	NameData Span
}

// DescriptorObjectArray preserves the ObAr prefix and ordered descriptor-like columns.
// Prefix is retained verbatim because its semantics are not specified by Adobe's public format guide.
type DescriptorObjectArray struct {
	Prefix                                             uint32
	Name, ClassID                                      string
	Entries                                            []DescriptorEntry
	Span, PrefixData, NameData, ClassIDData, CountData Span
}

// DescriptorUnitArray contains an ObAr UnFl column, encoded as unit, count and float64 samples.
// Original IEEE sample bytes remain available through NumbersData.
type DescriptorUnitArray struct {
	Unit                                   string
	Numbers                                []float64
	Span, UnitData, CountData, NumbersData Span
}

// DecodeDescriptor explicitly decodes an Action descriptor within a bounded original source span.
// Typed values are int32, int64, bool, float64, string, the public descriptor types,
// *Descriptor, *DescriptorObjectArray, *DescriptorReference, or []DescriptorValue.
// It never decodes pixel data or mutates the indexed raw blocks. Unsupported OSTypes
// return ErrUnsupported; the supplied span remains available through ReadSpan/CopySpan.
// A descriptor may be followed by other fields; its returned Span records consumed bytes only.
func (d *Document) DecodeDescriptor(span Span, versioned bool) (*Descriptor, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil, ErrClosed
	}
	if !d.validSpan(span) {
		return nil, fileError(ErrMalformed, "descriptor", "descriptor", span.Offset, "span is outside the source")
	}
	cursor := descriptorCursor{document: d, offset: span.Offset, end: span.Offset + span.Length}
	var version uint32
	var versionData Span
	if versioned {
		versionData = Span{Offset: cursor.offset, Length: 4}
		version = cursor.u32("descriptor.version")
		if cursor.err == nil && version != 16 {
			return nil, fileError(
				ErrUnsupported, "descriptor", "descriptor.version", versionData.Offset,
				"unsupported descriptor version",
			)
		}
	}
	result := cursor.descriptor(1, "descriptor", false)
	if cursor.err != nil {
		return nil, cursor.err
	}
	result.Version, result.VersionData = version, versionData
	result.Span = Span{Offset: span.Offset, Length: cursor.offset - span.Offset}
	return result, nil
}

type descriptorCursor struct {
	document    *Document
	offset, end int64
	allocated   int64
	items       int
	err         error
}

func (c *descriptorCursor) fail(kind error, path string, offset int64, detail string) {
	if c.err == nil {
		c.err = fileError(kind, "descriptor", path, offset, detail)
	}
}

func (c *descriptorCursor) require(length int64, path string) bool {
	if c.err != nil {
		return false
	}
	if length < 0 || length > c.end-c.offset {
		c.fail(ErrMalformed, path, c.offset, "descriptor field extends beyond its source span")
		return false
	}
	return true
}

func (c *descriptorCursor) reserve(length int64, path string) bool {
	if c.err != nil {
		return false
	}
	if length < 0 || length > c.document.limits.MaxMetadataBytes-c.allocated ||
		uint64(length) > uint64(^uint(0)>>1) {
		c.fail(ErrLimit, path, c.offset, "descriptor allocation exceeds MaxMetadataBytes")
		return false
	}
	c.allocated += length
	return true
}

func (c *descriptorCursor) count(count uint32, itemBytes int64, minimumBytes int64, path string) bool {
	if c.err != nil {
		return false
	}
	if uint64(count) > uint64(c.document.limits.MaxDescriptorItems-c.items) {
		c.fail(ErrLimit, path, c.offset, "descriptor count exceeds MaxDescriptorItems")
		return false
	}
	if !c.require(int64(count)*minimumBytes, path) || !c.reserve(int64(count)*itemBytes, path) {
		return false
	}
	c.items += int(count)
	return true
}

func (c *descriptorCursor) depth(depth int, path string) bool {
	if depth > c.document.limits.MaxDescriptorDepth {
		c.fail(ErrLimit, path, c.offset, "descriptor nesting exceeds MaxDescriptorDepth")
	}
	return c.err == nil
}

func (c *descriptorCursor) readInto(data []byte, path string) {
	if !c.require(int64(len(data)), path) {
		return
	}
	if int64(len(data)) > c.document.limits.MaxReadBytes {
		c.fail(ErrLimit, path, c.offset, "descriptor field exceeds MaxReadBytes")
		return
	}
	count, err := c.document.reader.ReadAt(data, c.offset)
	if count != len(data) || err != nil && !errors.Is(err, io.EOF) {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		c.err = &Error{
			Kind: ErrMalformed, Feature: "descriptor", Path: path, Offset: c.offset,
			Detail: "original descriptor bytes could not be read", Cause: err,
		}
		return
	}
	c.offset += int64(len(data))
}

func (c *descriptorCursor) u32(path string) uint32 {
	var data [4]byte
	c.readInto(data[:], path)
	return binary.BigEndian.Uint32(data[:])
}

func (c *descriptorCursor) u64(path string) uint64 {
	var data [8]byte
	c.readInto(data[:], path)
	return binary.BigEndian.Uint64(data[:])
}

func (c *descriptorCursor) signature(path string) string {
	var data [4]byte
	if !c.reserve(4, path) {
		return ""
	}
	c.readInto(data[:], path)
	return string(data[:])
}

func (c *descriptorCursor) bytes(length int64, path string) []byte {
	if !c.require(length, path) {
		return nil
	}
	if length > c.document.limits.MaxReadBytes || uint64(length) > uint64(^uint(0)>>1) {
		c.fail(ErrLimit, path, c.offset, "descriptor field exceeds MaxReadBytes")
		return nil
	}
	if !c.reserve(length, path) {
		return nil
	}
	data := make([]byte, int(length))
	c.readInto(data, path)
	return data
}

func (c *descriptorCursor) identifier(path string) (string, Span) {
	start := c.offset
	length := c.u32(path)
	if length == 0 {
		length = 4
	}
	if !c.reserve(int64(length), path) {
		return "", Span{}
	}
	data := c.bytes(int64(length), path)
	return string(data), Span{Offset: start, Length: c.offset - start}
}

func (c *descriptorCursor) unicode(path string) (string, Span) {
	start := c.offset
	count := c.u32(path)
	length := int64(count) * 2
	if !c.require(length, path) || !c.reserve(int64(count)*3, path) {
		return "", Span{}
	}
	data := c.bytes(length, path)
	if c.err != nil {
		return "", Span{}
	}
	var builder strings.Builder
	builder.Grow(int(count) * 3)
	for offset := 0; offset < len(data); offset += 2 {
		code := binary.BigEndian.Uint16(data[offset:])
		if code == 0 && offset == len(data)-2 {
			break
		}
		character := rune(code)
		if utf16.IsSurrogate(character) {
			character = utf8.RuneError
			if code >= 0xd800 && code <= 0xdbff && offset+2 < len(data) {
				next := binary.BigEndian.Uint16(data[offset+2:])
				if next >= 0xdc00 && next <= 0xdfff {
					character = utf16.DecodeRune(rune(code), rune(next))
					offset += 2
				}
			}
		}
		builder.WriteRune(character)
	}
	return builder.String(), Span{Offset: start, Length: c.offset - start}
}

func (c *descriptorCursor) class(path string) DescriptorClass {
	start := c.offset
	var result DescriptorClass
	result.Name, result.NameData = c.unicode(path + ".name")
	result.ClassID, result.ClassIDData = c.identifier(path + ".classID")
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) descriptor(depth int, path string, array bool) *Descriptor {
	if !c.depth(depth, path) || !c.reserve(int64(unsafe.Sizeof(Descriptor{})), path) {
		return nil
	}
	start := c.offset
	class := c.class(path)
	result := &Descriptor{
		Name: class.Name, ClassID: class.ClassID, NameData: class.NameData, ClassIDData: class.ClassIDData,
	}
	result.CountData = Span{Offset: c.offset, Length: 4}
	count := c.u32(path + ".count")
	// A one-byte explicit key and Boolean use 4+1+4+1 bytes.
	if !c.count(count, int64(unsafe.Sizeof(DescriptorEntry{})), 10, path+".entries") {
		return nil
	}
	result.Entries = make([]DescriptorEntry, int(count))
	for index := range result.Entries {
		itemPath := fmt.Sprintf("%s.entries[%d]", path, index)
		entry := &result.Entries[index]
		entryStart := c.offset
		entry.Key, entry.KeyData = c.identifier(itemPath + ".key")
		value := c.typedValue(depth, itemPath, array, false)
		if c.err != nil {
			return nil
		}
		entry.Type, entry.Value = value.Type, value.Value
		entry.TypeData, entry.Data = value.TypeData, value.Data
		entry.Span = Span{Offset: entryStart, Length: c.offset - entryStart}
	}
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) typedValue(depth int, path string, array, reference bool) DescriptorValue {
	start := c.offset
	result := DescriptorValue{TypeData: Span{Offset: start, Length: 4}}
	result.Type = c.signature(path + ".type")
	valueStart := c.offset
	if c.err != nil {
		return result
	}
	if reference {
		result.Value = c.referenceValue(result.Type, path)
	} else if array && result.Type == "UnFl" {
		result.Value = c.unitArray(path)
	} else {
		result.Value = c.value(result.Type, depth, path)
	}
	result.Data = Span{Offset: valueStart, Length: c.offset - valueStart}
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) value(kind string, depth int, path string) any {
	if !c.reserve(16, path) {
		return nil
	}
	switch kind {
	case "Objc", "GlbO":
		return c.descriptor(depth+1, path, false)
	case "VlLs":
		return c.list(depth+1, path, false)
	case "obj ":
		return c.reference(depth+1, path)
	case "ObAr":
		return c.objectArray(depth+1, path)
	case "doub":
		return math.Float64frombits(c.u64(path))
	case "UntF", "UnFl":
		return c.unit(kind, path)
	case "TEXT":
		value, _ := c.unicode(path)
		return value
	case "enum":
		return c.enum(path)
	case "long":
		return int32(c.u32(path))
	case "comp":
		return int64(c.u64(path))
	case "bool":
		var data [1]byte
		c.readInto(data[:], path)
		return data[0] != 0
	case "type", "GlbC":
		if !c.reserve(int64(unsafe.Sizeof(DescriptorClass{})), path) {
			return nil
		}
		return c.class(path)
	case "alis", "tdta", "Pth ":
		return c.blob(path)
	default:
		c.fail(ErrUnsupported, path+".type", c.offset-4, "unsupported descriptor OSType")
		return nil
	}
}

func (c *descriptorCursor) unit(kind, path string) DescriptorUnit {
	start := c.offset
	var result DescriptorUnit
	if !c.reserve(int64(unsafe.Sizeof(result)), path) {
		return result
	}
	result.UnitData = Span{Offset: c.offset, Length: 4}
	result.Unit = c.signature(path + ".unit")
	result.NumberData = Span{Offset: c.offset, Length: 8}
	result.Width = 64
	if kind == "UnFl" {
		result.Width, result.NumberData.Length = 32, 4
		result.Bits = uint64(c.u32(path + ".number"))
		result.Number = math.Float32frombits(uint32(result.Bits))
	} else {
		result.Bits = c.u64(path + ".number")
		result.Number = math.Float64frombits(result.Bits)
	}
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) enum(path string) DescriptorEnum {
	start := c.offset
	var result DescriptorEnum
	if !c.reserve(int64(unsafe.Sizeof(result)), path) {
		return result
	}
	result.TypeID, result.TypeData = c.identifier(path + ".typeID")
	result.ValueID, result.ValueData = c.identifier(path + ".valueID")
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) blob(path string) DescriptorBlob {
	start := c.offset
	var result DescriptorBlob
	if !c.reserve(int64(unsafe.Sizeof(result)), path) {
		return result
	}
	length := int64(c.u32(path + ".length"))
	if !c.require(length, path) {
		return result
	}
	result.Data = Span{Offset: c.offset, Length: length}
	c.offset += length
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) list(depth int, path string, reference bool) []DescriptorValue {
	if !c.depth(depth, path) {
		return nil
	}
	count := c.u32(path + ".count")
	if !c.count(count, int64(unsafe.Sizeof(DescriptorValue{})), 4, path+".items") {
		return nil
	}
	result := make([]DescriptorValue, int(count))
	for index := range result {
		itemPath := fmt.Sprintf("%s.items[%d]", path, index)
		result[index] = c.typedValue(depth, itemPath, false, reference)
		if c.err != nil {
			return nil
		}
	}
	return result
}

func (c *descriptorCursor) reference(depth int, path string) *DescriptorReference {
	start := c.offset
	if !c.reserve(int64(unsafe.Sizeof(DescriptorReference{})), path) {
		return nil
	}
	result := &DescriptorReference{CountData: Span{Offset: start, Length: 4}}
	result.Items = c.list(depth, path, true)
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) referenceValue(kind, path string) any {
	switch kind {
	case "prop":
		if !c.reserve(int64(unsafe.Sizeof(DescriptorProperty{})), path) {
			return nil
		}
		result := DescriptorProperty{Class: c.class(path + ".class")}
		result.KeyID, result.KeyData = c.identifier(path + ".keyID")
		return result
	case "Clss":
		if !c.reserve(int64(unsafe.Sizeof(DescriptorClass{})), path) {
			return nil
		}
		return c.class(path)
	case "Enmr":
		if !c.reserve(int64(unsafe.Sizeof(DescriptorEnumeratedReference{})), path) {
			return nil
		}
		return DescriptorEnumeratedReference{Class: c.class(path + ".class"), Enum: c.enum(path + ".enum")}
	case "rele":
		if !c.reserve(int64(unsafe.Sizeof(DescriptorOffset{})), path) {
			return nil
		}
		result := DescriptorOffset{Class: c.class(path + ".class")}
		result.OffsetData = Span{Offset: c.offset, Length: 4}
		result.Offset = c.u32(path + ".offset")
		return result
	case "Idnt", "indx":
		if !c.reserve(4, path) {
			return nil
		}
		return int32(c.u32(path))
	case "name":
		if !c.reserve(int64(unsafe.Sizeof(DescriptorNamedReference{})), path) {
			return nil
		}
		result := DescriptorNamedReference{Class: c.class(path + ".class")}
		result.Name, result.NameData = c.unicode(path + ".name")
		return result
	default:
		c.fail(ErrUnsupported, path+".type", c.offset-4, "unsupported reference OSType")
		return nil
	}
}

func (c *descriptorCursor) objectArray(depth int, path string) *DescriptorObjectArray {
	start := c.offset
	if !c.reserve(int64(unsafe.Sizeof(DescriptorObjectArray{})), path) {
		return nil
	}
	result := &DescriptorObjectArray{PrefixData: Span{Offset: start, Length: 4}}
	result.Prefix = c.u32(path + ".prefix")
	body := c.descriptor(depth, path, true)
	if c.err != nil {
		return nil
	}
	result.Name, result.ClassID, result.Entries = body.Name, body.ClassID, body.Entries
	result.NameData, result.ClassIDData, result.CountData = body.NameData, body.ClassIDData, body.CountData
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}

func (c *descriptorCursor) unitArray(path string) DescriptorUnitArray {
	start := c.offset
	var result DescriptorUnitArray
	if !c.reserve(int64(unsafe.Sizeof(result)), path) {
		return result
	}
	result.UnitData = Span{Offset: c.offset, Length: 4}
	result.Unit = c.signature(path + ".unit")
	result.CountData = Span{Offset: c.offset, Length: 4}
	count := c.u32(path + ".count")
	if !c.count(count, 8, 8, path+".numbers") {
		return result
	}
	result.NumbersData = Span{Offset: c.offset, Length: int64(count) * 8}
	result.Numbers = make([]float64, int(count))
	for index := range result.Numbers {
		result.Numbers[index] = math.Float64frombits(c.u64(path + ".numbers"))
		if c.err != nil {
			return result
		}
	}
	result.Span = Span{Offset: start, Length: c.offset - start}
	return result
}
