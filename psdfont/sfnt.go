package psdfont

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"unicode/utf16"
	"unicode/utf8"
)

// Open reads only the explicit caller-supplied file and closes it before returning an owned face.
func Open(ctx context.Context, filename string, options ...DecodeOptions) (*Face, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	configuration, err := prepareFontDecodeConfiguration(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits := configuration.limits
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() < 12 {
		return nil, fontError(ErrMalformed, "source", 0, "font source is not a regular sfnt file")
	}
	// Decode retains a second owned copy while the file buffer is still live.
	if configuration.requestBytes > limits.MaxBytes ||
		info.Size() > (limits.MaxBytes-configuration.requestBytes)/2 || uint64(info.Size()) > uint64(^uint(0)>>1) {
		return nil, fontError(ErrLimit, "source", 0, "file buffer and retained copy exceed MaxBytes")
	}
	data := make([]byte, int(info.Size()))
	for position := 0; position < len(data); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := file.Read(data[position:min(len(data), position+(64<<10))])
		position += count
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if count == 0 {
			return nil, io.ErrNoProgress
		}
	}
	limits.MaxBytes -= int64(len(data))
	configuration.limits = limits
	return decodeConfiguredFont(ctx, data, configuration)
}

// Decode reads one static TrueType or CFF1 sfnt face or an explicitly selected TTC face.
// CFF2, variable fonts, bitmap-only faces and unsupported collection versions are rejected.
func Decode(ctx context.Context, data []byte, options ...DecodeOptions) (*Face, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	configuration, err := prepareFontDecodeConfiguration(ctx, options)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return decodeConfiguredFont(ctx, data, configuration)
}

func decodeConfiguredFont(ctx context.Context, data []byte, configuration fontDecodeConfiguration) (*Face, error) {
	limits := configuration.limits
	var err error
	if len(data) < 12 {
		return nil, fontError(ErrMalformed, "sfnt.header", 0, "incomplete font header")
	}
	if configuration.requestBytes > limits.MaxBytes-1024 ||
		int64(len(data)) > limits.MaxBytes-1024-configuration.requestBytes {
		return nil, fontError(ErrLimit, "sfnt", 0, "retained font bytes exceed MaxBytes")
	}
	offset := uint32(0)
	if string(data[:4]) == "ttcf" {
		version, count := u32(data, 4), u32(data, 8)
		if version != 0x00010000 && version != 0x00020000 {
			return nil, fontError(ErrUnsupported, "ttc.version", 4, "unsupported collection version")
		}
		if count == 0 || uint64(count)*4+12 > uint64(len(data)) {
			return nil, fontError(ErrMalformed, "ttc.faces", 8, "empty or truncated face directory")
		}
		if uint64(count) > uint64(limits.MaxFaces) {
			return nil, fontError(ErrLimit, "ttc.faces", 8, "face directory exceeds MaxFaces")
		}
		if uint64(limits.FaceIndex) >= uint64(count) {
			return nil, fontError(ErrMalformed, "ttc.faceIndex", 8, "selected face is outside the collection")
		}
		if version == 0x00020000 && uint64(count)*4+24 > uint64(len(data)) {
			return nil, fontError(ErrMalformed, "ttc.header", 8, "collection lacks its version-two signature fields")
		}
		offset = u32(data, 12+limits.FaceIndex*4)
	} else if limits.FaceIndex != 0 {
		return nil, fontError(ErrMalformed, "sfnt.faceIndex", 0, "standalone font contains only face zero")
	}
	if uint64(offset)+12 > uint64(len(data)) {
		return nil, fontError(ErrMalformed, "sfnt.header", int64(offset), "selected face header is outside the source")
	}
	base := int(offset)
	version := u32(data, base)
	isCFF := version == 0x4f54544f
	if version != 0x00010000 && version != 0x74727565 && !isCFF {
		return nil, fontError(ErrUnsupported, "sfnt.outlines", int64(offset), "unsupported sfnt outline version")
	}
	count := int(u16(data, base+4))
	if count == 0 {
		return nil, fontError(ErrMalformed, "sfnt.tables", int64(offset)+4, "empty font table directory")
	}
	if count > limits.MaxTables {
		return nil, fontError(ErrLimit, "sfnt.tables", int64(offset)+4, "table count exceeds MaxTables")
	}
	if uint64(offset)+12+uint64(count)*16 > uint64(len(data)) {
		return nil, fontError(ErrMalformed, "sfnt.tables", int64(offset)+12, "truncated table directory")
	}
	reservation := int64(len(data)) + 1024 + int64(count)*128 + configuration.requestBytes
	if reservation > limits.MaxBytes {
		return nil, fontError(ErrLimit, "sfnt.tables", int64(offset)+12, "owned table metadata exceeds MaxBytes")
	}
	face := &Face{FaceIndex: limits.FaceIndex, raw: append([]byte(nil), data...),
		Tables: make([]Table, count), tables: make(map[string]Span, count), reservation: reservation}
	for index := range count {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		position := base + 12 + index*16
		tag := string(data[position : position+4])
		span := Span{Offset: int64(u32(data, position+8)), Length: int64(u32(data, position+12))}
		if span.Offset > int64(len(data)) || span.Length > int64(len(data))-span.Offset {
			return nil, fontError(ErrMalformed, "sfnt.table", int64(position), "table is outside the supplied source")
		}
		if _, duplicate := face.tables[tag]; duplicate {
			return nil, fontError(ErrMalformed, "sfnt.table", int64(position), "duplicate table identifier")
		}
		face.tables[tag] = span
		face.Tables[index] = Table{Tag: tag, Checksum: u32(data, position+4), Span: span}
	}
	_, hasCFF2 := face.tables["CFF2"]
	if hasCFF2 && !isCFF {
		return nil, fontError(ErrUnsupported, "sfnt.outlines", 0, "CFF2 requires an OTTO sfnt signature")
	}
	isCFF2 := isCFF && hasCFF2
	for _, tag := range []string{"fvar", "gvar"} {
		if _, found := face.tables[tag]; found {
			if !isCFF2 || tag == "gvar" {
				return nil, fontError(ErrUnsupported, "sfnt.outlines", 0, "variable TrueType or CFF1 outlines are unsupported")
			}
		}
	}
	if len(limits.Variations) != 0 && !isCFF2 {
		return nil, fontError(ErrUnsupported, "variation.request", 0, "instance outline pipeline is not yet connected")
	}
	if _, found := face.tables["CFF "]; found && isCFF2 || !found && isCFF && !isCFF2 || found && !isCFF {
		return nil, fontError(ErrUnsupported, "sfnt.outlines", 0, "sfnt signature and outline table disagree")
	}
	if isCFF {
		for _, tag := range []string{"glyf", "loca"} {
			if _, found := face.tables[tag]; found {
				return nil, fontError(ErrUnsupported, "sfnt.outlines", 0, "conflicting CFF and TrueType outlines")
			}
		}
	}
	head, err := face.table("head", 54)
	if err != nil {
		return nil, err
	}
	if u32(head, 0) != 0x00010000 {
		return nil, fontError(ErrUnsupported, "head.version", face.tables["head"].Offset, "unsupported head version")
	}
	if u32(head, 12) != 0x5f0f3cf5 {
		return nil, fontError(ErrMalformed, "head.magic", face.tables["head"].Offset+12, "invalid head magic number")
	}
	face.UnitsPerEm = u16(head, 18)
	if face.UnitsPerEm < 16 || face.UnitsPerEm > 16384 {
		return nil, fontError(ErrMalformed, "head.unitsPerEm", face.tables["head"].Offset+18, "invalid design-unit scale")
	}
	format := i16(head, 50)
	if format != 0 && format != 1 || i16(head, 52) != 0 {
		return nil, fontError(ErrUnsupported, "head.format", face.tables["head"].Offset+50, "unsupported glyph/loca format")
	}
	face.locaLong = format == 1
	maxp, err := face.table("maxp", 6)
	if err != nil {
		return nil, err
	}
	if !isCFF && (u32(maxp, 0) != 0x00010000 || len(maxp) < 32) {
		return nil, fontError(ErrUnsupported, "maxp.version", face.tables["maxp"].Offset,
			"TrueType requires version-one maxp")
	}
	if isCFF && u32(maxp, 0) != 0x00005000 {
		return nil, fontError(ErrUnsupported, "maxp.version", face.tables["maxp"].Offset,
			"CFF1 requires version-half maxp")
	}
	face.NumGlyphs = u16(maxp, 4)
	if face.NumGlyphs == 0 {
		return nil, fontError(ErrMalformed, "maxp.numGlyphs", face.tables["maxp"].Offset+4, "font has no glyphs")
	}
	if int(face.NumGlyphs) > limits.MaxGlyphs {
		return nil, fontError(ErrLimit, "maxp.numGlyphs", face.tables["maxp"].Offset+4, "glyph count exceeds MaxGlyphs")
	}
	hhea, err := face.table("hhea", 36)
	if err != nil {
		return nil, err
	}
	if u32(hhea, 0) != 0x00010000 {
		return nil, fontError(ErrUnsupported, "hhea.version", face.tables["hhea"].Offset, "unsupported metric version")
	}
	face.Ascender, face.Descender, face.LineGap = i16(hhea, 4), i16(hhea, 6), i16(hhea, 8)
	if i16(hhea, 32) != 0 {
		return nil, fontError(ErrUnsupported, "hhea.format", face.tables["hhea"].Offset+32,
			"unsupported horizontal metric data format")
	}
	face.horizontalMetrics = u16(hhea, 34)
	if face.horizontalMetrics == 0 || face.horizontalMetrics > face.NumGlyphs {
		return nil, fontError(ErrMalformed, "hhea.metrics", face.tables["hhea"].Offset+34,
			"invalid horizontal metric count")
	}
	metricBytes := int(face.horizontalMetrics)*4 + int(face.NumGlyphs-face.horizontalMetrics)*2
	if _, err := face.table("hmtx", metricBytes); err != nil {
		return nil, err
	}
	if isCFF {
		if isCFF2 {
			budget := &fontMetadataBudget{face: face, ctx: ctx, limits: limits, operations: configuration.requestWork}
			face.variation, err = readFvarMetadata(ctx, face, budget)
			if err != nil {
				return nil, err
			}
			if face.variation != nil {
				if err := readAvarMetadata(face, face.variation, budget); err != nil {
					return nil, err
				}
				if err := selectVariationCoordinates(face.variation, limits.Variations, budget); err != nil {
					return nil, err
				}
			} else if len(limits.Variations) != 0 {
				return nil, fontError(ErrMalformed, "variation.request", 0, "font has no requested variation axes")
			}
			if err := face.decodeCFF2(budget); err != nil {
				return nil, err
			}
			if err := face.readHorizontalVariations(budget); err != nil {
				return nil, err
			}
			if err := face.readMetricVariations(budget); err != nil {
				return nil, err
			}
			if err := face.readLayoutVariationStore(budget); err != nil {
				return nil, err
			}
		} else if err := face.decodeCFF(ctx, limits); err != nil {
			return nil, err
		}
	} else {
		locaWidth := 2
		if face.locaLong {
			locaWidth = 4
		}
		loca, err := face.table("loca", (int(face.NumGlyphs)+1)*locaWidth)
		if err != nil {
			return nil, err
		}
		glyf, err := face.table("glyf", 0)
		if err != nil {
			return nil, err
		}
		previous := uint32(0)
		for index := 0; index <= int(face.NumGlyphs); index++ {
			if index%256 == 0 && ctx.Err() != nil {
				return nil, ctx.Err()
			}
			value := uint32(u16(loca, index*locaWidth)) * 2
			if face.locaLong {
				value = u32(loca, index*4)
			}
			if value < previous || uint64(value) > uint64(len(glyf)) {
				return nil, fontError(ErrMalformed, "loca.offset", face.tables["loca"].Offset+int64(index*locaWidth),
					"glyph offsets are not ordered within glyf")
			}
			previous = value
		}
	}
	if err := face.decodeCmap(ctx, limits); err != nil {
		return nil, err
	}
	if err := face.decodeKern(ctx, limits, face.reservation); err != nil {
		return nil, err
	}
	if err := face.decodeName(ctx, limits, face.reservation); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return face, nil
}

func (f *Face) table(tag string, minimum int) ([]byte, error) {
	span, found := f.tables[tag]
	if !found || span.Length < int64(minimum) {
		return nil, fontError(ErrMalformed, "sfnt."+tag, span.Offset, "required table is absent or truncated")
	}
	return f.raw[int(span.Offset):int(span.Offset+span.Length)], nil
}

func u16(data []byte, offset int) uint16 { return binary.BigEndian.Uint16(data[offset:]) }
func i16(data []byte, offset int) int16  { return int16(u16(data, offset)) }
func u32(data []byte, offset int) uint32 { return binary.BigEndian.Uint32(data[offset:]) }

func (f *Face) decodeName(ctx context.Context, limits DecodeOptions, reserved int64) error {
	span, found := f.tables["name"]
	if !found {
		return nil
	}
	data, err := f.table("name", 6)
	if err != nil {
		return err
	}
	format, count, storage := u16(data, 0), int(u16(data, 2)), int(u16(data, 4))
	if format > 1 {
		return fontError(ErrUnsupported, "name.format", span.Offset, "unsupported name table version")
	}
	if count > limits.MaxCmapEntries {
		return fontError(ErrLimit, "name.records", span.Offset+2, "name count exceeds configured traversal limit")
	}
	if 6+count*12 > len(data) || storage < 6+count*12 || storage > len(data) {
		return fontError(ErrMalformed, "name.records", span.Offset+2, "name directory or storage is outside the table")
	}
	if format == 1 {
		position := 6 + count*12
		if position+2 > len(data) {
			return fontError(ErrMalformed, "name.languages", span.Offset+int64(position), "missing language-tag count")
		}
		languages := int(u16(data, position))
		if languages > limits.MaxCmapEntries {
			return fontError(ErrLimit, "name.languages", span.Offset+int64(position),
				"language count exceeds traversal limit")
		}
		if position+2+languages*4 > storage {
			return fontError(ErrMalformed, "name.languages", span.Offset+int64(position),
				"language records exceed name storage")
		}
		for index := range languages {
			if err := ctx.Err(); err != nil {
				return err
			}
			at := position + 2 + index*4
			length, offset := int(u16(data, at)), int(u16(data, at+2))
			if offset > len(data)-storage || length > len(data)-storage-offset {
				return fontError(ErrMalformed, "name.languages", span.Offset+int64(at),
					"language string is outside name storage")
			}
		}
	}
	best := -1
	for index := range count {
		if err := ctx.Err(); err != nil {
			return err
		}
		position := 6 + index*12
		platform, encoding, nameID := u16(data, position), u16(data, position+2), u16(data, position+6)
		length, offset := int(u16(data, position+8)), int(u16(data, position+10))
		if offset > len(data)-storage || length > len(data)-storage-offset {
			return fontError(ErrMalformed, "name.value", span.Offset+int64(position), "name string is outside the table")
		}
		if nameID != 6 {
			continue
		}
		score := -1
		unicode := platform == 0 || platform == 3 && (encoding == 1 || encoding == 10)
		if unicode {
			score = 2
		} else if platform == 1 && encoding == 0 {
			score = 1
		}
		if score <= best {
			continue
		}
		if int64(length)*4 > limits.MaxBytes-reserved {
			return fontError(ErrLimit, "name.value", span.Offset+int64(storage+offset), "owned font name exceeds MaxBytes")
		}
		reserved += int64(length) * 4
		f.reservation = reserved
		value := data[storage+offset : storage+offset+length]
		if unicode {
			if len(value)%2 != 0 {
				return fontError(ErrMalformed, "name.value", span.Offset+int64(storage+offset),
					"Unicode font name has odd length")
			}
			decoded := make([]byte, 0, length*2)
			for cursor := 0; cursor < len(value); cursor += 2 {
				unit := u16(value, cursor)
				character := rune(unit)
				if unit >= 0xd800 && unit <= 0xdbff {
					if cursor+3 >= len(value) || u16(value, cursor+2) < 0xdc00 || u16(value, cursor+2) > 0xdfff {
						return fontError(ErrMalformed, "name.value", span.Offset+int64(storage+offset), "unpaired name surrogate")
					}
					character = utf16.DecodeRune(rune(unit), rune(u16(value, cursor+2)))
					cursor += 2
				} else if unit >= 0xdc00 && unit <= 0xdfff {
					return fontError(ErrMalformed, "name.value", span.Offset+int64(storage+offset), "unpaired name surrogate")
				}
				decoded = utf8.AppendRune(decoded, character)
			}
			f.PostScriptName = string(decoded)
		} else {
			for _, character := range value {
				if character >= 128 {
					return fontError(ErrUnsupported, "name.value", span.Offset+int64(storage+offset),
						"non-ASCII Mac name encoding")
				}
			}
			f.PostScriptName = string(value)
		}
		best = score
	}
	return nil
}
