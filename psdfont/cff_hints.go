package psdfont

import (
	"context"
	"math"
	"unsafe"
)

// CFFHints owns preserved CFF1 hint metadata without executing grid fitting.
// All coordinates remain in the original Type2 charstring space; Matrix maps them to Glyph.Path design units.
// CFF2 and TrueType glyphs leave this field absent. CFF1 endchar components are not supported by this metadata profile.
type CFFHints struct {
	Private CFFHintPrivate
	Matrix  [6]float64
	Stems   []CFFStemHint
	Masks   []CFFHintMask
}

// CFFStemHint records a stem in declaration order, which is also its mask bit position.
// Ghost is zero for an ordinary stem, -20 for a top/right edge, or -21 for a bottom/left edge.
// Edge hints have equal Low and High. Source identifies the declaring operator, whose operands may span subroutines.
type CFFStemHint struct {
	Low, High      float64
	Vertical, Edge bool
	Ghost          int8
	Source         Span
}

// CFFHintMask records original MSB-first mask bytes and the next affected path command index.
// Counter masks declare counter priorities and do not replace the active hint mask.
// StemCount records the declared stem domain at this mask; Source includes the operator and mask bytes.
type CFFHintMask struct {
	CommandIndex, StemCount int
	Active                  []byte
	Counter                 bool
	Source                  Span
}

// CFFHintPrivate records delta-decoded CFF1 Private DICT hint parameters in source charstring units.
// Missing scalar fields use Adobe CFF defaults; a zero source span distinguishes an omitted field.
// StdHW and StdVW are zero when omitted. Arrays are independent owned slices, including across glyph requests.
type CFFHintPrivate struct {
	BlueValues, OtherBlues, FamilyBlues, FamilyOtherBlues []float64
	StemSnapH, StemSnapV                                  []float64
	StdHW, StdVW, BlueScale, BlueShift, BlueFuzz          float64
	ExpansionFactor                                       float64
	ForceBold                                             bool
	LanguageGroup                                         int
	Source                                                CFFHintPrivateSources
}

// CFFHintPrivateSources records exact original operand-and-operator spans for supported Private DICT fields.
// PrivateDICT identifies the whole original dictionary, including fields outside this hint metadata profile.
type CFFHintPrivateSources struct {
	PrivateDICT, BlueValues, OtherBlues, FamilyBlues, FamilyOtherBlues Span
	StemSnapH, StemSnapV, StdHW, StdVW                                 Span
	BlueScale, BlueShift, BlueFuzz, ExpansionFactor                    Span
	ForceBold, LanguageGroup                                           Span
}

func (r *type2Reader) preservePrivateHints() error {
	if err := r.reserve(int64(unsafe.Sizeof(CFFHints{}))); err != nil {
		return err
	}
	metadata := &CFFHints{Matrix: r.private.matrix, Private: CFFHintPrivate{
		BlueScale: .039625, BlueShift: 7, BlueFuzz: 1, ExpansionFactor: .06,
	}}
	span := r.private.hintSource
	metadata.Private.Source.PrivateDICT = span
	if span.Offset < 0 || span.Length < 0 || span.Offset > int64(len(r.face.raw)) ||
		span.Length > int64(len(r.face.raw))-span.Offset {
		return r.fail(ErrMalformed, span.Offset, "Private hint dictionary exceeds owned font")
	}
	data := r.face.raw[int(span.Offset):int(span.Offset+span.Length)]
	var operands [48]float64
	count, start := 0, 0
	for position := 0; position < len(data); {
		offset := span.Offset + int64(position)
		if err := r.step(offset); err != nil {
			return err
		}
		value, next, numeric, err := cffNumber(data, position, true)
		if err != nil {
			return r.fail(ErrMalformed, offset, "invalid Private hint dictionary number")
		}
		if numeric {
			if count == 0 {
				start = position
			}
			if count == len(operands) {
				return r.fail(ErrMalformed, offset, "Private hint dictionary exceeds 48 operands")
			}
			operands[count], count, position = value, count+1, next
			continue
		}
		operator := int(data[position])
		position++
		if operator == 12 {
			if position == len(data) {
				return r.fail(ErrMalformed, offset, "truncated Private hint dictionary escape")
			}
			operator, position = 256+int(data[position]), position+1
		}
		fieldSpan := Span{Offset: span.Offset + int64(start), Length: int64(position - start)}
		if err := r.preservePrivateField(&metadata.Private, operator, operands[:count], fieldSpan); err != nil {
			return err
		}
		count, start = 0, position
	}
	if count != 0 {
		return r.fail(ErrMalformed, span.Offset, "unterminated Private hint dictionary operands")
	}
	r.glyph.CFFHints = metadata
	return r.ctx.Err()
}

func (r *type2Reader) preservePrivateField(
	private *CFFHintPrivate,
	operator int,
	args []float64,
	span Span,
) error {
	var array *[]float64
	var source *Span
	maximum, paired := 0, false
	switch operator {
	case 6:
		array, source, maximum, paired = &private.BlueValues, &private.Source.BlueValues, 14, true
	case 7:
		array, source, maximum, paired = &private.OtherBlues, &private.Source.OtherBlues, 10, true
	case 8:
		array, source, maximum, paired = &private.FamilyBlues, &private.Source.FamilyBlues, 14, true
	case 9:
		array, source, maximum, paired = &private.FamilyOtherBlues, &private.Source.FamilyOtherBlues, 10, true
	case 268:
		array, source, maximum = &private.StemSnapH, &private.Source.StemSnapH, 12
	case 269:
		array, source, maximum = &private.StemSnapV, &private.Source.StemSnapV, 12
	}
	if array != nil {
		if len(args) == 0 || len(args) > maximum || paired && len(args)%2 != 0 {
			return r.fail(ErrMalformed, span.Offset, "invalid Private hint array count")
		}
		if err := r.reserve(int64(len(args)) * 8); err != nil {
			return err
		}
		values := make([]float64, len(args))
		value := 0.0
		for index, delta := range args {
			if err := r.step(span.Offset); err != nil {
				return err
			}
			value += delta
			if !finite(value) || math.Abs(value) > 1<<40 ||
				paired && index%2 == 1 && value < values[index-1] ||
				!paired && (value < 0 || index > 0 && value < values[index-1]) {
				return r.fail(ErrMalformed, span.Offset, "invalid delta-decoded Private hint array")
			}
			values[index] = value
		}
		*array, *source = values, span
		return nil
	}
	var target *float64
	switch operator {
	case 10:
		target, source = &private.StdHW, &private.Source.StdHW
	case 11:
		target, source = &private.StdVW, &private.Source.StdVW
	case 265:
		target, source = &private.BlueScale, &private.Source.BlueScale
	case 266:
		target, source = &private.BlueShift, &private.Source.BlueShift
	case 267:
		target, source = &private.BlueFuzz, &private.Source.BlueFuzz
	case 274:
		target, source = &private.ExpansionFactor, &private.Source.ExpansionFactor
	case 270, 273:
		if len(args) != 1 || args[0] != 0 && args[0] != 1 {
			return r.fail(ErrMalformed, span.Offset, "invalid Private hint boolean or language group")
		}
		if operator == 270 {
			private.ForceBold, private.Source.ForceBold = args[0] != 0, span
		} else {
			private.LanguageGroup, private.Source.LanguageGroup = int(args[0]), span
		}
		return nil
	default:
		return nil
	}
	if len(args) != 1 || !finite(args[0]) || args[0] < 0 || args[0] > 1<<40 ||
		operator == 265 && args[0] == 0 {
		return r.fail(ErrMalformed, span.Offset, "invalid Private hint scalar")
	}
	*target, *source = args[0], span
	return nil
}

func (r *type2Reader) preserveHintStems(operator int, offset int64) error {
	metadata := r.glyph.CFFHints
	vertical := operator == 3 || operator == 23 || operator == 19 || operator == 20
	position := 0.0
	for index := 0; index < r.count; index += 2 {
		if err := r.step(offset); err != nil {
			return err
		}
		first, width := position+r.stack[index], r.stack[index+1]
		position = first + width
		if !finite(first) || !finite(position) || math.Abs(first) > 1<<40 || math.Abs(position) > 1<<40 {
			return r.fail(ErrLimit, offset, "Type2 stem coordinates exceed supported precision")
		}
		stem := CFFStemHint{Low: first, High: position, Vertical: vertical, Source: Span{Offset: offset, Length: 1}}
		if width == -20 || width == -21 {
			stem.Edge, stem.Ghost = true, int8(width)
			if width == -21 {
				stem.Low = position
			}
			stem.High = stem.Low
		} else if width < 0 {
			return r.fail(ErrUnsupported, offset, "Type2 negative stem width has undefined hint semantics")
		}
		if len(metadata.Stems) == cap(metadata.Stems) {
			capacity := min(96, max(8, cap(metadata.Stems)*2))
			if err := r.reserve(int64(capacity) * int64(unsafe.Sizeof(CFFStemHint{}))); err != nil {
				return err
			}
			owned := make([]CFFStemHint, len(metadata.Stems), capacity)
			copy(owned, metadata.Stems)
			metadata.Stems = owned
		}
		metadata.Stems = append(metadata.Stems, stem)
	}
	return r.ctx.Err()
}

func (r *type2Reader) preserveHintMask(operator int, offset int64, active []byte) error {
	metadata := r.glyph.CFFHints
	for index := range active {
		if err := r.step(offset + 1 + int64(index)); err != nil {
			return err
		}
	}
	if len(metadata.Masks) == cap(metadata.Masks) {
		capacity := max(4, cap(metadata.Masks)*2)
		if err := r.reserve(int64(capacity) * int64(unsafe.Sizeof(CFFHintMask{}))); err != nil {
			return err
		}
		owned := make([]CFFHintMask, len(metadata.Masks), capacity)
		copy(owned, metadata.Masks)
		metadata.Masks = owned
	}
	if err := r.reserve(int64(len(active))); err != nil {
		return err
	}
	bits := make([]byte, len(active))
	copy(bits, active)
	metadata.Masks = append(metadata.Masks, CFFHintMask{
		CommandIndex: len(r.glyph.Path), StemCount: r.hints, Active: bits, Counter: operator == 20,
		Source: Span{Offset: offset, Length: int64(1 + len(active))},
	})
	return r.ctx.Err()
}

func openTypeRetainCFFHints(ctx context.Context, b *openTypeLayoutBudget, metadata *CFFHints) error {
	if metadata == nil {
		return nil
	}
	if err := b.reserve(int64(unsafe.Sizeof(CFFHints{}))); err != nil {
		return err
	}
	if err := b.array(cap(metadata.Stems), int64(unsafe.Sizeof(CFFStemHint{}))); err != nil {
		return err
	}
	if err := b.array(cap(metadata.Masks), int64(unsafe.Sizeof(CFFHintMask{}))); err != nil {
		return err
	}
	for _, mask := range metadata.Masks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.array(cap(mask.Active), 1); err != nil {
			return err
		}
	}
	private := metadata.Private
	for _, values := range [][]float64{
		private.BlueValues, private.OtherBlues, private.FamilyBlues,
		private.FamilyOtherBlues, private.StemSnapH, private.StemSnapV,
	} {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := b.array(cap(values), 8); err != nil {
			return err
		}
	}
	return ctx.Err()
}
