package psdfont

import (
	"math"
	"unsafe"
)

type cff2Font struct {
	charstrings, globalSubrs []Span
	private                  []cff2Private
	fdSelect                 []uint16
	store                    *itemVariationStore
	fontMatrix               [6]float64
	fontMatrixPresent        bool
	fontMatrixSource         Span
}

type cff2Private struct {
	subrs   []Span
	vsindex uint16
	source  Span
}

type cff2Dictionary struct {
	charstrings, fdArray, fdSelect, vstore int
	privateSize, privateOffset, subrs      int
	hasPrivate, hasMatrix                  bool
	hasVSIndex                             bool
	matrix                                 [6]float64
	matrixSource                           Span
	vsindex                                uint16
}

type cff2Parser struct {
	face   *Face
	data   []byte
	base   int64
	budget *fontMetadataBudget
	store  *itemVariationStore
}

func (p *cff2Parser) dictionary(span Span, kind int) (cff2Dictionary, error) {
	result := cff2Dictionary{charstrings: -1, fdArray: -1, fdSelect: -1, vstore: -1, subrs: -1}
	if span.Offset < p.base || span.Length < 0 || span.Offset-p.base > int64(len(p.data)) ||
		span.Length > int64(len(p.data))-(span.Offset-p.base) {
		return result, fontError(ErrMalformed, "cff2.dict", span.Offset, "DICT exceeds CFF2 table")
	}
	data := p.data[span.Offset-p.base : span.Offset-p.base+span.Length]
	var stack [513]float64
	count, operandStart := 0, 0
	vsindexSeen, blended := false, false
	for position := 0; position < len(data); {
		offset := span.Offset + int64(position)
		if err := p.budget.step("cff2.dict", offset); err != nil {
			return result, err
		}
		value, next, numeric, err := cffNumber(data, position, true)
		if err != nil {
			return result, fontError(ErrMalformed, "cff2.dict.number", offset, "invalid DICT number")
		}
		if numeric {
			if count == len(stack) {
				return result, fontError(ErrLimit, "cff2.dict.stack", offset, "DICT operands exceed 513")
			}
			if count == 0 {
				operandStart = position
			}
			stack[count], count, position = value, count+1, next
			continue
		}
		operator := int(data[position])
		position++
		if operator == 12 {
			if position == len(data) {
				return result, fontError(ErrMalformed, "cff2.dict.operator", offset, "truncated escaped operator")
			}
			operator = 256 + int(data[position])
			position++
		}
		args := stack[:count]
		integer := func(index int) (int, error) {
			if index >= len(args) || math.Trunc(args[index]) != args[index] || args[index] < 0 ||
				args[index] > float64(len(p.data)) {
				return 0, fontError(ErrMalformed, "cff2.dict.offset", offset, "invalid DICT offset or length")
			}
			return int(args[index]), nil
		}
		if kind == 2 && operator == 22 {
			if vsindexSeen || blended || len(args) != 1 || args[0] < 0 || args[0] > 65535 || math.Trunc(args[0]) != args[0] {
				return result, fontError(ErrMalformed, "cff2.private.vsindex", offset, "invalid private vsindex order or value")
			}
			result.vsindex, vsindexSeen = uint16(args[0]), true
			result.hasVSIndex = true
			count = 0
			continue
		}
		if kind == 2 && operator == 23 {
			if p.store == nil || int(result.vsindex) >= len(p.store.data) {
				return result, fontError(ErrMalformed, "cff2.private.blend", offset, "blend has no selected variation data")
			}
			var err error
			count, err = cff2Blend(stack[:], count, p.store, result.vsindex, offset, p.budget.step)
			if err != nil {
				return result, err
			}
			blended = true
			continue
		}
		switch {
		case kind == 0 && operator == 263:
			if len(args) != 6 {
				return result, fontError(ErrMalformed, "cff2.fontMatrix", offset, "FontMatrix requires six values")
			}
			copy(result.matrix[:], args)
			result.hasMatrix = true
			result.matrixSource = Span{Offset: span.Offset + int64(operandStart), Length: int64(position - operandStart)}
		case kind == 0 && (operator == 17 || operator == 292 || operator == 293 || operator == 24):
			if len(args) != 1 {
				return result, fontError(ErrMalformed, "cff2.dict.count", offset, "wrong Top DICT operand count")
			}
			value, err := integer(0)
			if err != nil {
				return result, err
			}
			switch operator {
			case 17:
				result.charstrings = value
			case 292:
				result.fdArray = value
			case 293:
				result.fdSelect = value
			case 24:
				result.vstore = value
			}
		case kind == 1 && operator == 18:
			if len(args) != 2 {
				return result, fontError(ErrMalformed, "cff2.private", offset, "Private requires size and offset")
			}
			result.privateSize, err = integer(0)
			if err == nil {
				result.privateOffset, err = integer(1)
			}
			if err != nil {
				return result, err
			}
			result.hasPrivate = true
		case kind == 2 && operator == 19:
			if len(args) != 1 {
				return result, fontError(ErrMalformed, "cff2.subrs", offset, "Subrs requires one offset")
			}
			result.subrs, err = integer(0)
			if err != nil {
				return result, err
			}
		}
		// CFF2 explicitly ignores unsupported operators and clears their operands.
		count = 0
	}
	if count != 0 {
		return result, fontError(ErrMalformed, "cff2.dict", span.Offset+span.Length, "unterminated DICT operands")
	}
	return result, nil
}

func cff2Blend(
	stack []float64, count int, store *itemVariationStore, vsindex uint16, offset int64,
	step func(string, int64) error,
) (int, error) {
	if count == 0 || store == nil || int(vsindex) >= len(store.data) || len(store.scalars) != store.regionCount {
		return 0, fontError(ErrMalformed, "cff2.blend", offset, "blend lacks operands or variation data")
	}
	number := stack[count-1]
	if math.Trunc(number) != number || number < 1 || number > float64(len(stack)) {
		return 0, fontError(ErrMalformed, "cff2.blend.count", offset, "invalid blend value count")
	}
	regions := store.data[vsindex].regionIDs
	values := int(number)
	if values > (count-1)/(len(regions)+1) {
		return 0, fontError(ErrMalformed, "cff2.blend.stack", offset, "blend deltas exceed operand stack")
	}
	begin := count - 1 - values*(len(regions)+1)
	for value := range values {
		result := stack[begin+value]
		for region, id := range regions {
			if err := step("cff2.blend.delta", offset); err != nil {
				return 0, err
			}
			result += stack[begin+values+value*len(regions)+region] * store.scalars[id]
		}
		if !finite(result) || math.Abs(result) > 1<<40 {
			return 0, fontError(ErrLimit, "cff2.blend.value", offset, "blend exceeds supported precision")
		}
		stack[begin+value] = result
	}
	return begin + values, nil
}

func (f *Face) decodeCFF2(budget *fontMetadataBudget) error {
	data, err := f.table("CFF2", 5)
	if err != nil {
		return err
	}
	base := f.tables["CFF2"].Offset
	header, topLength := int(data[2]), int(u16(data, 3))
	if data[0] != 2 || data[1] != 0 || header < 5 || header > len(data) || topLength > len(data)-header {
		return fontError(ErrMalformed, "cff2.header", base, "invalid CFF2 header or Top DICT length")
	}
	if err := budget.reserve(int64(unsafe.Sizeof(cff2Font{}))+int64(unsafe.Sizeof(cff2Parser{}))+8192,
		"cff2.memory", base); err != nil {
		return err
	}
	parser := cff2Parser{face: f, data: data, base: base, budget: budget}
	top, err := parser.dictionary(Span{Offset: base + int64(header), Length: int64(topLength)}, 0)
	if err != nil {
		return err
	}
	matrix := [6]float64{.001, 0, 0, .001, 0, 0}
	if top.hasMatrix {
		matrix = top.matrix
	}
	if !finite(matrix[0]) || matrix[0] <= 0 || matrix[0] != matrix[3] || matrix[1] != 0 || matrix[2] != 0 ||
		matrix[4] != 0 || matrix[5] != 0 || math.Floor(1/matrix[0]+.5) != float64(f.UnitsPerEm) ||
		f.UnitsPerEm != 1000 && !top.hasMatrix {
		return fontError(ErrMalformed, "cff2.fontMatrix", top.matrixSource.Offset,
			"uniform FontMatrix reciprocal does not identify the head design grid")
	}
	global, _, err := readCFF2Index(data, base, header+topLength, budget)
	if err != nil {
		return err
	}
	if top.vstore >= 0 {
		if top.vstore > len(data)-2 {
			return fontError(ErrMalformed, "cff2.vstore", base+int64(top.vstore), "missing variation store length")
		}
		length := int(u16(data, top.vstore))
		begin, end := top.vstore+2, len(data)
		if length != 65535 {
			if length > len(data)-begin {
				return fontError(ErrMalformed, "cff2.vstore", base+int64(top.vstore), "variation store exceeds CFF2 table")
			}
			end = begin + length
		}
		axes := 0
		if f.variation != nil {
			axes = len(f.variation.axes)
		}
		parser.store, err = readItemVariationStore(data[begin:end], base+int64(begin), 0, axes, budget)
		if err != nil {
			return err
		}
		var coordinates []int16
		if f.variation != nil {
			coordinates = f.variation.normalized
		}
		if err := parser.store.prepareScalars(coordinates, budget); err != nil {
			return err
		}
	}
	if top.charstrings < 0 || top.fdArray < 0 {
		return fontError(ErrMalformed, "cff2.top", base+int64(header), "required CharStrings or FDArray is absent")
	}
	charstrings, _, err := readCFF2Index(data, base, top.charstrings, budget)
	if err != nil {
		return err
	}
	if len(charstrings) != int(f.NumGlyphs) {
		return fontError(ErrMalformed, "cff2.charstrings", base+int64(top.charstrings), "glyph count differs from maxp")
	}
	fonts, _, err := readCFF2Index(data, base, top.fdArray, budget)
	if err != nil {
		return err
	}
	if len(fonts) == 0 || uint64(len(fonts)) > uint64(^uint32(0)) {
		return fontError(ErrMalformed, "cff2.fdArray", base+int64(top.fdArray), "invalid Font DICT count")
	}
	if err := budget.array(uint64(len(fonts)), int64(unsafe.Sizeof(cff2Private{})), "cff2.private", base); err != nil {
		return err
	}
	result := &cff2Font{charstrings: charstrings, globalSubrs: global, private: make([]cff2Private, len(fonts)),
		store: parser.store, fontMatrix: matrix, fontMatrixPresent: top.hasMatrix, fontMatrixSource: top.matrixSource}
	for index, span := range fonts {
		dictionary, err := parser.dictionary(span, 1)
		if err != nil {
			return err
		}
		if !dictionary.hasPrivate || dictionary.privateOffset > len(data)-dictionary.privateSize {
			return fontError(ErrMalformed, "cff2.private", span.Offset, "required Private DICT exceeds CFF2 source")
		}
		privateSpan := Span{Offset: base + int64(dictionary.privateOffset), Length: int64(dictionary.privateSize)}
		private, err := parser.dictionary(privateSpan, 2)
		if err != nil {
			return err
		}
		if parser.store != nil && int(private.vsindex) >= len(parser.store.data) {
			return fontError(ErrMalformed, "cff2.private.vsindex", privateSpan.Offset,
				"private variation index exceeds store")
		}
		if private.hasVSIndex && parser.store == nil {
			return fontError(ErrMalformed, "cff2.private.vsindex", privateSpan.Offset,
				"private variation index requires a variation store")
		}
		row := cff2Private{source: privateSpan, vsindex: private.vsindex}
		if private.subrs >= 0 {
			if private.subrs > len(data)-dictionary.privateOffset {
				return fontError(ErrMalformed, "cff2.subrs", privateSpan.Offset, "Subrs exceeds CFF2 table")
			}
			row.subrs, _, err = readCFF2Index(data, base, dictionary.privateOffset+private.subrs, budget)
			if err != nil {
				return err
			}
		}
		result.private[index] = row
	}
	if top.fdSelect >= 0 {
		result.fdSelect, err = readCFF2FDSelect(data, base, top.fdSelect, f.NumGlyphs, uint32(len(fonts)), budget)
		if err != nil {
			return err
		}
	} else if len(fonts) != 1 {
		return fontError(ErrMalformed, "cff2.fdSelect", base, "multiple Font DICTs require FDSelect")
	}
	if err := budget.ctx.Err(); err != nil {
		return err
	}
	f.cff2 = result
	return nil
}
