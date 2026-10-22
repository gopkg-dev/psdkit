package psdfont

import (
	"context"
	"math"
	"strconv"
	"strings"
	"unsafe"
)

type cffFont struct {
	charstrings      []Span
	globalSubrs      []Span
	strings          []Span
	private          []cffPrivate
	fdSelect         []uint8
	charset          []uint16
	cid              bool
	stringCount      int
	matrix           [6]float64
	rawTopMatrix     [6]float64
	topMatrixPresent bool
	topMatrixSpan    Span
}

type cffPrivate struct {
	subrs                      []Span
	defaultWidth, nominalWidth float64
	hintSource                 Span
	matrix                     [6]float64
	rawFDMatrix                [6]float64
	fdMatrixPresent            bool
	fdMatrixSpan               Span
}

type cffDict struct {
	charstrings, charset, privateOffset, privateSize int
	fdArray, fdSelect, subrs                         int
	charType, paintType                              float64
	defaultWidth, nominalWidth                       float64
	matrix                                           [6]float64
	hasMatrix, cid, synthetic                        bool
	hasPrivate                                       bool
	matrixOffset                                     int64
	matrixSpan                                       Span
}

type cffParser struct {
	face          *Face
	ctx           context.Context
	data          []byte
	base          int64
	limits        DecodeOptions
	operations    int
	matrixScratch *cffMatrixScratch
}

func (p *cffParser) step(offset int) error {
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if p.operations >= p.limits.MaxCFFOperations {
		return fontError(ErrLimit, "cff.work", p.base+int64(offset), "CFF traversal exceeds MaxCFFOperations")
	}
	p.operations++
	return nil
}

func (p *cffParser) reserve(length int64, offset int) error {
	if length < 0 || length > p.limits.MaxBytes-p.face.reservation {
		return fontError(ErrLimit, "cff.memory", p.base+int64(offset), "owned CFF metadata exceeds MaxBytes")
	}
	p.face.reservation += length
	return nil
}

func (p *cffParser) malformed(offset int, detail string) error {
	return fontError(ErrMalformed, "cff.structure", p.base+int64(offset), detail)
}

func (p *cffParser) index(offset int) ([]Span, int, error) {
	if err := p.step(offset); err != nil {
		return nil, 0, err
	}
	if offset < 0 || offset > len(p.data)-2 {
		return nil, 0, p.malformed(offset, "truncated INDEX count")
	}
	count := int(u16(p.data, offset))
	if count == 0 {
		return nil, offset + 2, nil
	}
	if offset+3 > len(p.data) {
		return nil, 0, p.malformed(offset, "truncated INDEX offSize")
	}
	width := int(p.data[offset+2])
	if width < 1 || width > 4 || count+1 > (len(p.data)-offset-3)/width {
		return nil, 0, p.malformed(offset, "invalid INDEX offset array")
	}
	if err := p.reserve(int64(count)*int64(unsafe.Sizeof(Span{}))+24, offset); err != nil {
		return nil, 0, err
	}
	begin := offset + 3 + (count+1)*width
	readOffset := func(index int) uint32 {
		position := offset + 3 + index*width
		value := uint32(0)
		for _, octet := range p.data[position : position+width] {
			value = value<<8 | uint32(octet)
		}
		return value
	}
	previous := readOffset(0)
	if previous != 1 {
		return nil, 0, p.malformed(offset, "INDEX first offset must be one")
	}
	result := make([]Span, count)
	for index := range count {
		if err := p.step(offset + 3 + index*width); err != nil {
			return nil, 0, err
		}
		value := readOffset(index + 1)
		if value < previous || uint64(value) > uint64(len(p.data)-begin)+1 {
			return nil, 0, p.malformed(offset, "INDEX offsets are unordered or out of bounds")
		}
		result[index] = Span{Offset: p.base + int64(begin) + int64(previous) - 1, Length: int64(value - previous)}
		previous = value
	}
	return result, begin + int(previous) - 1, nil
}

func cffNumber(data []byte, position int, dict bool) (float64, int, bool, error) {
	if position >= len(data) {
		return 0, position, false, ErrMalformed
	}
	first := data[position]
	position++
	if first >= 32 && first <= 246 {
		return float64(int(first) - 139), position, true, nil
	}
	if first >= 247 && first <= 254 {
		if position >= len(data) {
			return 0, position, true, ErrMalformed
		}
		value := int(first-247)*256 + int(data[position]) + 108
		if first >= 251 {
			value = -(int(first-251)*256 + int(data[position]) + 108)
		}
		return float64(value), position + 1, true, nil
	}
	if first == 28 {
		if position > len(data)-2 {
			return 0, position, true, ErrMalformed
		}
		return float64(i16(data, position)), position + 2, true, nil
	}
	if first == 29 && dict || first == 255 && !dict {
		if position > len(data)-4 {
			return 0, position, true, ErrMalformed
		}
		value := float64(int32(u32(data, position)))
		if !dict {
			value /= 65536
		}
		return value, position + 4, true, nil
	}
	if first != 30 || !dict {
		return 0, position - 1, false, nil
	}
	var number strings.Builder
	for position < len(data) && number.Len() <= 64 {
		octet := data[position]
		position++
		for _, nibble := range [2]byte{octet >> 4, octet & 15} {
			switch nibble {
			case 10:
				number.WriteByte('.')
			case 11:
				number.WriteByte('E')
			case 12:
				number.WriteString("E-")
			case 13:
				return 0, position, true, ErrMalformed
			case 14:
				number.WriteByte('-')
			case 15:
				value, err := strconv.ParseFloat(number.String(), 64)
				if err != nil || !finite(value) {
					return 0, position, true, ErrMalformed
				}
				return value, position, true, nil
			default:
				number.WriteByte('0' + nibble)
			}
		}
	}
	return 0, position, true, ErrMalformed
}

func (p *cffParser) dictionary(span Span) (cffDict, error) {
	result := cffDict{charstrings: -1, fdArray: -1, fdSelect: -1, subrs: -1, charType: 2, matrixOffset: span.Offset}
	data := p.face.raw[int(span.Offset):int(span.Offset+span.Length)]
	var operands [48]float64
	var seen [512]bool
	count := 0
	operandStart := 0
	for position := 0; position < len(data); {
		if err := p.step(int(span.Offset-p.base) + position); err != nil {
			return result, err
		}
		value, next, numeric, err := cffNumber(data, position, true)
		if err != nil {
			return result, p.malformed(int(span.Offset-p.base)+position, "invalid DICT number")
		}
		if numeric {
			if count == 0 {
				operandStart = position
			}
			if count == len(operands) {
				return result, p.malformed(int(span.Offset-p.base)+position, "DICT stack exceeds 48 operands")
			}
			operands[count], count, position = value, count+1, next
			continue
		}
		operator := int(data[position])
		position++
		if operator == 12 {
			if position == len(data) {
				return result, p.malformed(int(span.Offset-p.base)+position, "truncated DICT escape")
			}
			operator, position = 256+int(data[position]), position+1
		}
		if operator > 511 || seen[operator] {
			return result, p.malformed(int(span.Offset-p.base)+position, "duplicate DICT key")
		}
		seen[operator] = true
		args := operands[:count]
		readInteger := func(index int) (int, error) {
			if index >= len(args) || args[index] < 0 || args[index] > float64(len(p.data)) ||
				math.Trunc(args[index]) != args[index] {
				return 0, p.malformed(int(span.Offset-p.base)+position, "invalid DICT length or offset")
			}
			return int(args[index]), nil
		}
		expected := -1
		switch operator {
		case 15, 17, 19, 292, 293:
			expected = 1
		case 18:
			expected = 2
		case 286:
			expected = 3
		case 263:
			expected = 6
		case 261, 262, 20, 21:
			expected = 1
		}
		if expected >= 0 && len(args) != expected {
			return result, p.malformed(int(span.Offset-p.base)+position, "wrong DICT operand count")
		}
		switch operator {
		case 15, 17, 19, 292, 293:
			value, err := readInteger(0)
			if err != nil {
				return result, err
			}
			switch operator {
			case 15:
				result.charset = value
			case 17:
				result.charstrings = value
			case 19:
				result.subrs = value
			case 292:
				result.fdArray = value
			case 293:
				result.fdSelect = value
			}
		case 18:
			result.privateSize, err = readInteger(0)
			result.hasPrivate = true
			if err == nil {
				result.privateOffset, err = readInteger(1)
			}
			if err != nil {
				return result, err
			}
		case 261:
			result.paintType = args[0]
		case 262:
			result.charType = args[0]
		case 263:
			copy(result.matrix[:], args)
			result.hasMatrix = true
			result.matrixOffset = span.Offset + int64(position-2)
			result.matrixSpan = Span{Offset: span.Offset + int64(operandStart), Length: int64(position - operandStart)}
		case 276:
			result.synthetic = true
		case 286:
			result.cid = true
		case 20:
			result.defaultWidth = args[0]
		case 21:
			result.nominalWidth = args[0]
		default:
			valid := operator >= 0 && operator <= 11 || operator == 13 || operator == 14 || operator == 16 ||
				operator >= 256 && operator <= 260 || operator >= 264 && operator <= 275 ||
				operator >= 277 && operator <= 279 || operator >= 287 && operator <= 291 || operator == 294
			if !valid {
				return result, fontError(ErrUnsupported, "cff.dict.operator", span.Offset+int64(position-1),
					"reserved or unsupported DICT operator")
			}
		}
		count = 0
	}
	if count != 0 {
		return result, p.malformed(int(span.Offset-p.base), "unterminated DICT operands")
	}
	return result, nil
}

func (p *cffParser) privateDictionary(dict cffDict, matrix [6]float64) (cffPrivate, error) {
	result := cffPrivate{matrix: matrix}
	if !dict.hasPrivate {
		return result, p.malformed(dict.privateOffset, "required Private DICT operator is absent")
	}
	if dict.privateOffset > len(p.data)-dict.privateSize {
		return result, p.malformed(dict.privateOffset, "Private DICT exceeds CFF table")
	}
	result.hintSource = Span{Offset: p.base + int64(dict.privateOffset), Length: int64(dict.privateSize)}
	private, err := p.dictionary(result.hintSource)
	if err != nil {
		return result, err
	}
	result.defaultWidth, result.nominalWidth = private.defaultWidth, private.nominalWidth
	if math.Abs(result.defaultWidth) > 1<<40 || math.Abs(result.nominalWidth) > 1<<40 {
		return result, p.malformed(dict.privateOffset, "Type2 width defaults exceed supported precision")
	}
	if private.subrs >= 0 {
		if private.subrs > len(p.data)-dict.privateOffset {
			return result, p.malformed(dict.privateOffset, "local Subrs offset exceeds CFF table")
		}
		result.subrs, _, err = p.index(dict.privateOffset + private.subrs)
	}
	return result, err
}

func (p *cffParser) charset(offset, stringCount int, cid bool) ([]uint16, error) {
	count := int(p.face.NumGlyphs)
	if err := p.reserve(int64(count)*2+65536, offset); err != nil {
		return nil, err
	}
	result := make([]uint16, count)
	if offset <= 2 {
		if cid {
			return nil, p.malformed(offset, "CID CFF cannot use a predefined charset")
		}
		if offset == 0 {
			if count > 229 {
				return nil, p.malformed(offset, "ISOAdobe charset is shorter than glyph count")
			}
			for index := range count {
				result[index] = uint16(index)
			}
			return result, nil
		}
		predefined := cffExpertCharset
		if offset == 2 {
			predefined = cffExpertSubsetCharset
		}
		if count > len(predefined) {
			return nil, p.malformed(offset, "predefined charset is shorter than glyph count")
		}
		copy(result, predefined[:count])
		return result, nil
	}
	if offset >= len(p.data) {
		return nil, p.malformed(offset, "charset format is outside table")
	}
	format := p.data[offset]
	position := offset + 1
	if format > 2 {
		return nil, p.malformed(offset, "unknown charset format")
	}
	seen := make([]bool, 65536)
	seen[0] = true
	for index := 1; index < count; {
		if err := p.step(position); err != nil {
			return nil, err
		}
		if position > len(p.data)-2 {
			return nil, p.malformed(position, "truncated charset range")
		}
		first, length := uint32(u16(p.data, position)), 1
		position += 2
		if format != 0 {
			width := int(format)
			if position > len(p.data)-width {
				return nil, p.malformed(position, "truncated charset range count")
			}
			length += int(p.data[position])
			if format == 2 {
				length = int(u16(p.data, position)) + 1
			}
			position += width
		}
		if length > count-index || first+uint32(length)-1 > 65535 {
			return nil, p.malformed(position, "charset range exceeds glyphs or SID/CID domain")
		}
		for rangeIndex := range length {
			if err := p.step(position); err != nil {
				return nil, err
			}
			value := uint16(first + uint32(rangeIndex))
			if seen[value] || !cid && (value >= 65000 || int(value) >= 391+stringCount) {
				return nil, p.malformed(position, "duplicate or absent charset SID/CID")
			}
			seen[value], result[index] = true, value
			index++
		}
	}
	return result, nil
}

func (p *cffParser) fdSelectors(offset, fonts int) ([]uint8, error) {
	count := int(p.face.NumGlyphs)
	if offset < 0 || offset >= len(p.data) {
		return nil, p.malformed(offset, "missing FDSelect format")
	}
	if err := p.reserve(int64(count), offset); err != nil {
		return nil, err
	}
	result := make([]uint8, count)
	format := p.data[offset]
	position := offset + 1
	if format == 0 {
		if count > len(p.data)-position {
			return nil, p.malformed(position, "truncated FDSelect format zero")
		}
		for index, selector := range p.data[position : position+count] {
			if err := p.step(position + index); err != nil {
				return nil, err
			}
			if int(selector) >= fonts {
				return nil, p.malformed(position+index, "FDSelect refers to absent Font DICT")
			}
			result[index] = selector
		}
		return result, nil
	}
	if format != 3 || position > len(p.data)-2 {
		return nil, p.malformed(offset, "unsupported or truncated FDSelect")
	}
	ranges := int(u16(p.data, position))
	position += 2
	if ranges == 0 || ranges > count || ranges > (len(p.data)-position-2)/3 {
		return nil, p.malformed(position, "invalid FDSelect range array")
	}
	for index := range ranges {
		if err := p.step(position + index*3); err != nil {
			return nil, err
		}
		at := position + index*3
		first, selector := int(u16(p.data, at)), p.data[at+2]
		next := int(u16(p.data, at+3))
		if index == 0 && first != 0 || first >= next || next > count || int(selector) >= fonts ||
			index == ranges-1 && next != count {
			return nil, p.malformed(at, "FDSelect range order, selector or sentinel differs")
		}
		for glyph := first; glyph < next; glyph++ {
			if err := p.step(at); err != nil {
				return nil, err
			}
			result[glyph] = selector
		}
	}
	return result, nil
}

func (f *Face) decodeCFF(ctx context.Context, limits DecodeOptions) error {
	data, err := f.table("CFF ", 4)
	if err != nil {
		return err
	}
	p := cffParser{face: f, ctx: ctx, data: data, base: f.tables["CFF "].Offset, limits: limits}
	if data[0] != 1 || int(data[2]) < 4 || int(data[2]) > len(data) || data[3] < 1 || data[3] > 4 {
		return fontError(ErrUnsupported, "cff.header", p.base, "unsupported CFF version or header")
	}
	if err := p.reserve(2048+int64(unsafe.Sizeof(cffFont{})), 0); err != nil {
		return err
	}
	names, position, err := p.index(int(data[2]))
	if err != nil {
		return err
	}
	tops, position, err := p.index(position)
	if err != nil {
		return err
	}
	if len(names) != 1 || len(tops) != 1 {
		return fontError(ErrUnsupported, "cff.fontset", p.base, "OpenType CFF requires one name and Top DICT")
	}
	stringsIndex, position, err := p.index(position)
	if err != nil {
		return err
	}
	global, _, err := p.index(position)
	if err != nil {
		return err
	}
	top, err := p.dictionary(tops[0])
	if err != nil {
		return err
	}
	if top.synthetic || top.paintType != 0 || top.charType != 2 || top.charstrings < 0 {
		return fontError(ErrUnsupported, "cff.outlines", tops[0].Offset, "CFF requires nonsynthetic filled Type2 outlines")
	}
	charstrings, _, err := p.index(top.charstrings)
	if err != nil {
		return err
	}
	if len(charstrings) != int(f.NumGlyphs) {
		return p.malformed(top.charstrings, "CharStrings INDEX differs from maxp glyph count")
	}
	charset, err := p.charset(top.charset, len(stringsIndex), top.cid)
	if err != nil {
		return err
	}
	matrix := cffIdentityMatrix
	if !top.cid {
		matrix, err = p.resolveMatrix(top, nil)
		if err != nil {
			return err
		}
	}
	font := &cffFont{charstrings: charstrings, globalSubrs: global, strings: stringsIndex, charset: charset,
		cid: top.cid, stringCount: len(stringsIndex), matrix: matrix}
	font.rawTopMatrix, font.topMatrixPresent, font.topMatrixSpan = top.matrix, top.hasMatrix, top.matrixSpan
	if !top.cid {
		if top.fdArray >= 0 || top.fdSelect >= 0 {
			return p.malformed(0, "name-keyed CFF has CID selectors")
		}
		if err := p.reserve(int64(unsafe.Sizeof(cffPrivate{})), 0); err != nil {
			return err
		}
		private, err := p.privateDictionary(top, matrix)
		if err != nil {
			return err
		}
		font.private = []cffPrivate{private}
	} else {
		if top.fdArray < 0 || top.fdSelect < 0 {
			return p.malformed(0, "CID CFF lacks FDArray or FDSelect")
		}
		fds, _, err := p.index(top.fdArray)
		if err != nil {
			return err
		}
		if len(fds) == 0 || len(fds) > 256 {
			return p.malformed(top.fdArray, "CID FDArray must contain one to 256 dictionaries")
		}
		if err := p.reserve(int64(len(fds))*int64(unsafe.Sizeof(cffPrivate{})), top.fdArray); err != nil {
			return err
		}
		font.private = make([]cffPrivate, len(fds))
		for index, span := range fds {
			fd, err := p.dictionary(span)
			if err != nil {
				return err
			}
			fdMatrix, err := p.resolveMatrix(top, &fd)
			if err != nil {
				return err
			}
			font.private[index], err = p.privateDictionary(fd, fdMatrix)
			if err != nil {
				return err
			}
			font.private[index].rawFDMatrix = fd.matrix
			font.private[index].fdMatrixPresent = fd.hasMatrix
			font.private[index].fdMatrixSpan = fd.matrixSpan
		}
		font.fdSelect, err = p.fdSelectors(top.fdSelect, len(fds))
		if err != nil {
			return err
		}
	}
	f.cff = font
	return ctx.Err()
}
