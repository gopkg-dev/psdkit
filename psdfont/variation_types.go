package psdfont

import "context"

// VariationCoordinate selects a user-space axis value in an explicitly supplied font.
// Omitted axes use fvar defaults; finite values outside an axis range are clamped before normalization.
type VariationCoordinate struct {
	Tag   string
	Value float64
}

// VariationAxis retains one source fvar axis, including its exact 16.16 values as float64.
type VariationAxis struct {
	Tag                       string
	Minimum, Default, Maximum float64
	Flags, NameID             uint16
	Source                    Span
}

// VariationInstanceCoordinate records the owned request and the normalized selection for one axis.
// Normalized is the signed F2DOT14 value used by all variation consumers and condition comparisons.
// AvarMappedFixed precedes cross-axis remapping; FinalNormalizedFixed includes avar-v2 deltas.
type VariationInstanceCoordinate struct {
	Tag                    string
	Requested              float64
	ClampedFixed           int32
	DefaultNormalizedFixed int32
	AvarMappedFixed        int32
	FinalNormalizedFixed   int32
	Normalized             int16
}

// NamedVariationInstance retains source IDs and coordinates without replacing the base PostScript name.
type NamedVariationInstance struct {
	SubfamilyNameID, Flags uint16
	PostScriptNameID       uint16
	HasPostScriptNameID    bool
	Coordinates            []float64
	Source                 Span
}

// InstanceGlyphMetrics preserves precise instance values separately from raw hmtx and integer bounds.
type InstanceGlyphMetrics struct {
	RawAdvanceWidth    uint16
	RawLeftSideBearing int16
	AdvanceWidth       float64
	LeftSideBearing    float64
	RightSideBearing   float64
	OutlineBounds      [4]float64
	HasInk             bool
}

// InstancePosition retains unrounded applied positions before the integer Shape output profile.
// Shape is horizontal: GPOS YAdvance adjustments are ignored, so YAdvance remains zero.
type InstancePosition struct {
	XAdvance, YAdvance, XOffset, YOffset float64
}

type variationAxisRecord struct {
	axis                           VariationAxis
	minimum, defaultValue, maximum int32
}

type axisVariationMap struct{ from, to int16 }

type fontVariationData struct {
	axes        []variationAxisRecord
	named       []NamedVariationInstance
	avar        [][]axisVariationMap
	avarStore   *itemVariationStore
	avarIndices []deltaSetIndex
	coordinates []VariationInstanceCoordinate
	normalized  []int16
	fvarSource  Span
	avarSource  Span
	copyLimit   int64
	copyWork    int
}

type fontMetadataBudget struct {
	face       *Face
	ctx        context.Context
	limits     DecodeOptions
	operations int
}

func (b *fontMetadataBudget) step(path string, offset int64) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.operations >= b.limits.MaxCFFOperations {
		return fontError(ErrLimit, path, offset, "font variation traversal exceeds MaxCFFOperations")
	}
	b.operations++
	return nil
}

func (b *fontMetadataBudget) reserve(bytes int64, path string, offset int64) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if bytes < 0 || bytes > b.limits.MaxBytes-b.face.reservation {
		return fontError(ErrLimit, path, offset, "owned variation metadata exceeds MaxBytes")
	}
	b.face.reservation += bytes
	return nil
}

func (b *fontMetadataBudget) array(count uint64, element int64, path string, offset int64) error {
	remaining := b.limits.MaxBytes - b.face.reservation
	if remaining < 0 || element <= 0 || count > uint64(^uint(0)>>1) ||
		count > uint64(remaining/element) {
		return fontError(ErrLimit, path, offset, "owned variation array exceeds MaxBytes")
	}
	return b.reserve(int64(count)*element, path, offset)
}
