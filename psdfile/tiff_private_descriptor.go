package psdfile

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

// tiffPrivateDescriptorOptions connects bounded descriptor traversal to the owning
// TIFF operation. Swap retains only source runs; its owner charges retained capacity.
// Reserve charges conservative scratch and traversal work, without charging or
// copying skipped strings, media, aliases, paths, or EngineData blob bytes.
type tiffPrivateDescriptorOptions struct {
	Order   binary.ByteOrder
	Limits  Limits
	Reserve func(length int64, path string, offset int64) error
	Count   func(count uint32, path string, offset int64) error
	Swap    func(span Span, width uint8, path string) error
}

// normalizeTIFFPrivateDescriptor walks one explicitly ordered Photoshop descriptor.
// Nil Order uses little-endian TIFF encoding; big-endian descriptors are validated without unit reversals.
// Swap receives original absolute source spans, reversing each width-byte element.
// The consumed span excludes any trailing fields in the supplied bounded span.
// It neither creates a Document nor decodes or reads any opaque blob payload.
func normalizeTIFFPrivateDescriptor(
	ctx context.Context, source io.ReaderAt, span Span, versioned bool,
	options tiffPrivateDescriptorOptions,
) (Span, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Span{}, err
	}
	if source == nil || span.Offset < 0 || span.Length < 0 || span.Length > math.MaxInt64-span.Offset {
		return Span{}, fileError(ErrMalformed, "tiff.private", "descriptor", span.Offset, "invalid bounded descriptor source")
	}
	if options.Order == nil {
		options.Order = binary.LittleEndian
	}
	if options.Order != binary.LittleEndian && options.Order != binary.BigEndian {
		return Span{}, fileError(ErrMalformed, "tiff.private", "descriptor.order", span.Offset,
			"descriptor byte order must be explicitly little-endian or big-endian")
	}
	limits, err := loadLimits([]LoadOptions{{Limits: options.Limits}})
	if err != nil {
		return Span{}, err
	}
	if limits.MaxDescriptorDepth > 256 {
		return Span{}, fileError(
			ErrLimit, "tiff.private", "descriptor.depth", span.Offset, "descriptor depth may not exceed 256",
		)
	}
	cursor := tiffPrivateDescriptorCursor{
		ctx: ctx, source: source, offset: span.Offset, end: span.Offset + span.Length,
		limits: limits, options: options,
	}
	if !cursor.reserve(8, "descriptor.scratch") || !cursor.frame(1, "descriptor") {
		return Span{}, cursor.err
	}
	if versioned {
		versionOffset := cursor.offset
		version := cursor.u32("descriptor.version")
		if cursor.err == nil && version != 16 {
			cursor.fail(ErrUnsupported, "descriptor.version", versionOffset, "unsupported descriptor version")
		}
	}
	cursor.descriptorBody(1, "descriptor", false)
	if !cursor.check() {
		return Span{}, cursor.err
	}
	return Span{Offset: span.Offset, Length: cursor.offset - span.Offset}, nil
}

type tiffPrivateDescriptorCursor struct {
	ctx         context.Context
	source      io.ReaderAt
	offset, end int64
	limits      Limits
	options     tiffPrivateDescriptorOptions
	reserved    int64
	items       int
	err         error
}

func (c *tiffPrivateDescriptorCursor) fail(kind error, path string, offset int64, detail string) {
	if c.err == nil {
		c.err = fileError(kind, "tiff.private", path, offset, detail)
	}
}

func (c *tiffPrivateDescriptorCursor) check() bool {
	if c.err == nil {
		c.err = c.ctx.Err()
	}
	return c.err == nil
}

func (c *tiffPrivateDescriptorCursor) require(length int64, path string) bool {
	if !c.check() {
		return false
	}
	if length < 0 || length > c.end-c.offset {
		c.fail(ErrMalformed, path, c.offset, "descriptor field exceeds its bounded source span")
		return false
	}
	return true
}

func (c *tiffPrivateDescriptorCursor) reserve(length int64, path string) bool {
	if !c.check() {
		return false
	}
	if length < 0 || length > c.limits.MaxMetadataBytes-c.reserved {
		c.fail(ErrLimit, path, c.offset, "descriptor traversal exceeds MaxMetadataBytes")
		return false
	}
	if c.options.Reserve != nil {
		if err := c.options.Reserve(length, path, c.offset); err != nil {
			c.err = err
			return false
		}
	}
	if !c.check() {
		return false
	}
	c.reserved += length
	return true
}

func (c *tiffPrivateDescriptorCursor) count(count uint32, minimumBytes, workBytes int64, path string) bool {
	if !c.check() {
		return false
	}
	if uint64(count) > uint64(c.limits.MaxDescriptorItems-c.items) {
		c.fail(ErrLimit, path, c.offset, "descriptor count exceeds MaxDescriptorItems")
		return false
	}
	if !c.require(int64(count)*minimumBytes, path) || !c.reserve(int64(count)*workBytes, path) {
		return false
	}
	if c.options.Count != nil {
		if err := c.options.Count(count, path, c.offset); err != nil {
			c.err = err
			return false
		}
	}
	if !c.check() {
		return false
	}
	c.items += int(count)
	return true
}

func (c *tiffPrivateDescriptorCursor) frame(depth int, path string) bool {
	if !c.check() {
		return false
	}
	if depth > c.limits.MaxDescriptorDepth {
		c.fail(ErrLimit, path, c.offset, "descriptor nesting exceeds MaxDescriptorDepth")
		return false
	}
	// Full ancestor paths grow with nesting; charge their bounded field suffixes too.
	return c.reserve(128+int64(len(path))*8, path)
}

func (c *tiffPrivateDescriptorCursor) read(data []byte, path string) {
	if !c.require(int64(len(data)), path) {
		return
	}
	if int64(len(data)) > c.limits.MaxReadBytes {
		c.fail(ErrLimit, path, c.offset, "descriptor field exceeds MaxReadBytes")
		return
	}
	count, err := c.source.ReadAt(data, c.offset)
	if count != len(data) || err != nil && err != io.EOF {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		c.err = &Error{
			Kind: ErrMalformed, Feature: "tiff.private", Path: path, Offset: c.offset,
			Detail: "descriptor structural field could not be read", Cause: err,
		}
		return
	}
	c.offset += int64(len(data))
	c.check()
}

func (c *tiffPrivateDescriptorCursor) swap(span Span, width uint8, path string) {
	if !c.check() || span.Length == 0 {
		return
	}
	if c.options.Order == binary.BigEndian {
		return
	}
	if c.options.Swap != nil {
		c.err = c.options.Swap(span, width, path)
	}
	c.check()
}

func (c *tiffPrivateDescriptorCursor) u32(path string) uint32 {
	start := c.offset
	var data [4]byte
	c.read(data[:], path)
	if c.err != nil {
		return 0
	}
	c.swap(Span{Offset: start, Length: 4}, 4, path)
	return c.options.Order.Uint32(data[:])
}

func (c *tiffPrivateDescriptorCursor) signature(path string) string {
	start := c.offset
	var data [4]byte
	c.read(data[:], path)
	if c.err != nil {
		return ""
	}
	c.swap(Span{Offset: start, Length: 4}, 4, path)
	if c.options.Order == binary.BigEndian {
		return string(data[:])
	}
	return string([]byte{data[3], data[2], data[1], data[0]})
}

func (c *tiffPrivateDescriptorCursor) skip(length int64, width uint8, path string) {
	if !c.require(length, path) {
		return
	}
	span := Span{Offset: c.offset, Length: length}
	if width != 0 {
		c.swap(span, width, path)
		if c.err != nil {
			return
		}
	}
	c.offset += length
}

func (c *tiffPrivateDescriptorCursor) identifier(path string) {
	length := c.u32(path + ".length")
	if c.err != nil {
		return
	}
	if length == 0 {
		c.skip(4, 4, path)
		return
	}
	// An explicit-length ID is ASCII, even when its length happens to be four.
	c.skip(int64(length), 0, path)
}

func (c *tiffPrivateDescriptorCursor) unicode(path string) {
	count := c.u32(path + ".count")
	c.skip(int64(count)*2, 2, path)
}

func (c *tiffPrivateDescriptorCursor) class(path string) {
	c.unicode(path + ".name")
	c.identifier(path + ".classID")
}

func (c *tiffPrivateDescriptorCursor) descriptor(depth int, path string, array bool) {
	if !c.frame(depth, path) {
		return
	}
	c.descriptorBody(depth, path, array)
}

func (c *tiffPrivateDescriptorCursor) descriptorBody(depth int, path string, array bool) {
	c.class(path)
	count := c.u32(path + ".count")
	// Charge full field paths before formatting entries, including numeric suffixes.
	workBytes := 128 + (int64(len(path))+48)*16
	// A one-byte explicit key and Boolean use 4+1+4+1 bytes in either order.
	if !c.count(count, 10, workBytes, path+".entries") {
		return
	}
	for index := uint32(0); index < count && c.check(); index++ {
		itemPath := fmt.Sprintf("%s.entries[%d]", path, index)
		c.identifier(itemPath + ".key")
		c.typedValue(depth, itemPath, array, false)
	}
}

func (c *tiffPrivateDescriptorCursor) typedValue(depth int, path string, array, reference bool) {
	kind := c.signature(path + ".type")
	if c.err != nil {
		return
	}
	if reference {
		c.referenceValue(kind, path)
		return
	}
	if array && kind == "UnFl" {
		c.signature(path + ".unit")
		count := c.u32(path + ".count")
		if c.count(count, 8, 0, path+".numbers") {
			c.skip(int64(count)*8, 8, path+".numbers")
		}
		return
	}
	switch kind {
	case "Objc", "GlbO":
		c.descriptor(depth+1, path, false)
	case "VlLs":
		c.list(depth+1, path, false)
	case "obj ":
		c.list(depth+1, path, true)
	case "ObAr":
		c.u32(path + ".prefix")
		c.descriptor(depth+1, path, true)
	case "doub", "comp":
		c.skip(8, 8, path)
	case "UntF":
		c.signature(path + ".unit")
		c.skip(8, 8, path+".number")
	case "UnFl":
		c.signature(path + ".unit")
		c.skip(4, 4, path+".number")
	case "TEXT":
		c.unicode(path)
	case "enum":
		c.identifier(path + ".typeID")
		c.identifier(path + ".valueID")
	case "long":
		c.skip(4, 4, path)
	case "bool":
		c.skip(1, 0, path)
	case "type", "GlbC":
		c.class(path)
	case "alis", "tdta", "Pth ":
		length := c.u32(path + ".length")
		c.skip(int64(length), 0, path)
	default:
		c.fail(ErrUnsupported, path+".type", c.offset-4, "unsupported descriptor OSType")
	}
}

func (c *tiffPrivateDescriptorCursor) list(depth int, path string, reference bool) {
	if !c.frame(depth, path) {
		return
	}
	count := c.u32(path + ".count")
	workBytes := 128 + (int64(len(path))+48)*16
	if !c.count(count, 4, workBytes, path+".items") {
		return
	}
	for index := uint32(0); index < count && c.check(); index++ {
		c.typedValue(depth, fmt.Sprintf("%s.items[%d]", path, index), false, reference)
	}
}

func (c *tiffPrivateDescriptorCursor) referenceValue(kind, path string) {
	switch kind {
	case "prop":
		c.class(path + ".class")
		c.identifier(path + ".keyID")
	case "Clss":
		c.class(path)
	case "Enmr":
		c.class(path + ".class")
		c.identifier(path + ".enum.typeID")
		c.identifier(path + ".enum.valueID")
	case "rele":
		c.class(path + ".class")
		c.skip(4, 4, path+".offset")
	case "Idnt", "indx":
		c.skip(4, 4, path)
	case "name":
		c.class(path + ".class")
		c.unicode(path + ".name")
	default:
		c.fail(ErrUnsupported, path+".type", c.offset-4, "unsupported descriptor reference OSType")
	}
}
