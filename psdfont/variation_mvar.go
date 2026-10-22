package psdfont

import (
	"context"
	"unsafe"
)

// VariationMetric retains an MVAR record and its precise selected-instance delta.
// Source spans the complete valueRecordSize, including compatible extension bytes.
// RawSource identifies the target field, independently from the MVAR record.
// RawValue and EffectiveValue are defined only when HasRawValue is true.
// Unknown tags and absent target fields retain their delta without inventing a raw value.
type VariationMetric struct {
	Tag                                    string
	Delta                                  float64
	Source                                 Span
	ValueRecordSize                        uint16
	DeltaSetOuterIndex, DeltaSetInnerIndex uint16
	SourceTable, SourceField               string
	RawSource                              Span
	RawValue                               int64
	HasRawValue                            bool
	EffectiveValue                         float64
}

type metricVariation struct {
	metrics []VariationMetric
	source  Span
}

func (f *Face) readMetricVariations(budget *fontMetadataBudget) error {
	span, exists := f.tables["MVAR"]
	if !exists {
		return nil
	}
	if f.variation != nil && len(f.variation.axes) == 0 {
		return nil
	}
	if err := budget.step("MVAR.header", span.Offset); err != nil {
		return err
	}
	if f.variation == nil {
		return fontError(ErrMalformed, "MVAR.fvar", span.Offset, "MVAR requires fvar metadata")
	}
	data, err := f.table("MVAR", 12)
	if err != nil {
		return err
	}
	if u16(data, 0) != 1 {
		return fontError(ErrUnsupported, "MVAR.version", span.Offset, "unsupported MVAR major version")
	}
	if u16(data, 4) != 0 {
		return fontError(ErrMalformed, "MVAR.reserved", span.Offset+4, "reserved MVAR field is nonzero")
	}
	size, count, storeOffset := u16(data, 6), u16(data, 8), u16(data, 10)
	if size == 0 || count != 0 && size < 8 ||
		uint64(count)*uint64(size) > uint64(len(data)-12) {
		return fontError(ErrMalformed, "MVAR.records", span.Offset+6, "value records exceed their source")
	}
	if count == 0 && storeOffset != 0 || count != 0 && storeOffset == 0 {
		return fontError(ErrMalformed, "MVAR.store", span.Offset+10, "store offset disagrees with value record count")
	}
	if err := budget.reserve(int64(unsafe.Sizeof(metricVariation{})), "MVAR.memory", span.Offset); err != nil {
		return err
	}
	if err := budget.array(uint64(count), int64(unsafe.Sizeof(VariationMetric{}))+4,
		"MVAR.records.memory", span.Offset+12); err != nil {
		return err
	}
	result := &metricVariation{metrics: make([]VariationMetric, int(count)), source: span}
	if count == 0 {
		if err := budget.ctx.Err(); err != nil {
			return err
		}
		f.mvar = result
		return nil
	}
	store, err := readItemVariationStore(data, span.Offset, int(storeOffset), len(f.variation.axes), budget)
	if err != nil {
		return err
	}
	if err := store.prepareScalars(f.variation.normalized, budget); err != nil {
		return err
	}
	var previous uint32
	gaspRecords := 0
	for index := range result.metrics {
		position := 12 + index*int(size)
		if err := budget.step("MVAR.record", span.Offset+int64(position)); err != nil {
			return err
		}
		tagValue := u32(data, position)
		if index > 0 && tagValue < previous {
			return fontError(ErrMalformed, "MVAR.record.tag", span.Offset+int64(position),
				"value tags are not in binary order")
		}
		previous = tagValue
		metric := VariationMetric{Tag: string(data[position : position+4]),
			Source: Span{Offset: span.Offset + int64(position), Length: int64(size)}, ValueRecordSize: size,
			DeltaSetOuterIndex: u16(data, position+4), DeltaSetInnerIndex: u16(data, position+6)}
		metric.Delta, err = store.delta(metric.DeltaSetOuterIndex, metric.DeltaSetInnerIndex, budget.step)
		if err != nil {
			return err
		}
		if err := f.readVariationMetricRaw(&metric, budget); err != nil {
			return err
		}
		if metric.SourceTable == "gasp" {
			gaspRecords++
		}
		if metric.HasRawValue {
			metric.EffectiveValue = float64(metric.RawValue) + metric.Delta
		}
		result.metrics[index] = metric
	}
	if gaspRecords != 0 {
		if err := budget.step("MVAR.gasp.records", span.Offset); err != nil {
			return err
		}
		if _, exists := f.tables["gasp"]; exists {
			gaspData, err := f.table("gasp", 4)
			if err != nil {
				return err
			}
			if gaspRecords > int(u16(gaspData, 2))-1 {
				return fontError(ErrMalformed, "MVAR.gasp.records", span.Offset,
					"gasp metric record count exceeds numRanges minus one")
			}
		}
	}
	if err := budget.ctx.Err(); err != nil {
		return err
	}
	f.mvar = result
	return nil
}

type variationMetricTarget struct {
	table, field string
	offset       int
	unsigned     bool
	minimumOS2   uint16
}

func mvarMetricTarget(tag string) (variationMetricTarget, bool) {
	var target variationMetricTarget
	switch tag {
	case "hasc":
		target = variationMetricTarget{table: "OS/2", field: "sTypoAscender", offset: 68}
	case "hdsc":
		target = variationMetricTarget{table: "OS/2", field: "sTypoDescender", offset: 70}
	case "hlgp":
		target = variationMetricTarget{table: "OS/2", field: "sTypoLineGap", offset: 72}
	case "hcla":
		target = variationMetricTarget{table: "OS/2", field: "usWinAscent", offset: 74, unsigned: true}
	case "hcld":
		target = variationMetricTarget{table: "OS/2", field: "usWinDescent", offset: 76, unsigned: true}
	case "xhgt":
		target = variationMetricTarget{table: "OS/2", field: "sxHeight", offset: 86, minimumOS2: 2}
	case "cpht":
		target = variationMetricTarget{table: "OS/2", field: "sCapHeight", offset: 88, minimumOS2: 2}
	case "sbxs":
		target = variationMetricTarget{table: "OS/2", field: "ySubscriptXSize", offset: 10}
	case "sbys":
		target = variationMetricTarget{table: "OS/2", field: "ySubscriptYSize", offset: 12}
	case "sbxo":
		target = variationMetricTarget{table: "OS/2", field: "ySubscriptXOffset", offset: 14}
	case "sbyo":
		target = variationMetricTarget{table: "OS/2", field: "ySubscriptYOffset", offset: 16}
	case "spxs":
		target = variationMetricTarget{table: "OS/2", field: "ySuperscriptXSize", offset: 18}
	case "spys":
		target = variationMetricTarget{table: "OS/2", field: "ySuperscriptYSize", offset: 20}
	case "spxo":
		target = variationMetricTarget{table: "OS/2", field: "ySuperscriptXOffset", offset: 22}
	case "spyo":
		target = variationMetricTarget{table: "OS/2", field: "ySuperscriptYOffset", offset: 24}
	case "strs":
		target = variationMetricTarget{table: "OS/2", field: "yStrikeoutSize", offset: 26}
	case "stro":
		target = variationMetricTarget{table: "OS/2", field: "yStrikeoutPosition", offset: 28}
	case "hcrs":
		target = variationMetricTarget{table: "hhea", field: "caretSlopeRise", offset: 18}
	case "hcrn":
		target = variationMetricTarget{table: "hhea", field: "caretSlopeRun", offset: 20}
	case "hcof":
		target = variationMetricTarget{table: "hhea", field: "caretOffset", offset: 22}
	case "vasc":
		target = variationMetricTarget{table: "vhea", field: "ascent", offset: 4}
	case "vdsc":
		target = variationMetricTarget{table: "vhea", field: "descent", offset: 6}
	case "vlgp":
		target = variationMetricTarget{table: "vhea", field: "lineGap", offset: 8}
	case "vcrs":
		target = variationMetricTarget{table: "vhea", field: "caretSlopeRise", offset: 18}
	case "vcrn":
		target = variationMetricTarget{table: "vhea", field: "caretSlopeRun", offset: 20}
	case "vcof":
		target = variationMetricTarget{table: "vhea", field: "caretOffset", offset: 22}
	case "unds":
		target = variationMetricTarget{table: "post", field: "underlineThickness", offset: 10}
	case "undo":
		target = variationMetricTarget{table: "post", field: "underlinePosition", offset: 8}
	default:
		if len(tag) != 4 || tag[:3] != "gsp" || tag[3] < '0' || tag[3] > '9' {
			return variationMetricTarget{}, false
		}
		fields := [...]string{"gaspRange[0].rangeMaxPPEM", "gaspRange[1].rangeMaxPPEM", "gaspRange[2].rangeMaxPPEM",
			"gaspRange[3].rangeMaxPPEM", "gaspRange[4].rangeMaxPPEM", "gaspRange[5].rangeMaxPPEM",
			"gaspRange[6].rangeMaxPPEM", "gaspRange[7].rangeMaxPPEM", "gaspRange[8].rangeMaxPPEM",
			"gaspRange[9].rangeMaxPPEM"}
		index := int(tag[3] - '0')
		target = variationMetricTarget{table: "gasp", field: fields[index], offset: 4 + index*4, unsigned: true}
	}
	return target, true
}

func (f *Face) readVariationMetricRaw(metric *VariationMetric, budget *fontMetadataBudget) error {
	if err := budget.step("MVAR.target", metric.Source.Offset); err != nil {
		return err
	}
	target, known := mvarMetricTarget(metric.Tag)
	if !known {
		return nil
	}
	metric.SourceTable, metric.SourceField = target.table, target.field
	span, exists := f.tables[target.table]
	if !exists {
		return nil
	}
	data, err := f.table(target.table, 2)
	if err != nil {
		return err
	}
	if target.table == "OS/2" && u16(data, 0) < target.minimumOS2 {
		return nil
	}
	if target.table == "vhea" {
		if len(data) < 4 {
			return fontError(ErrMalformed, "MVAR.target.vhea", span.Offset, "vhea version is truncated")
		}
		if u32(data, 0) == 0x00011000 {
			switch metric.Tag {
			case "vasc":
				metric.SourceField = "vertTypoAscender"
			case "vdsc":
				metric.SourceField = "vertTypoDescender"
			case "vlgp":
				metric.SourceField = "vertTypoLineGap"
			}
		}
	}
	if target.table == "gasp" {
		if len(data) < 4 {
			return fontError(ErrMalformed, "MVAR.target.gasp", span.Offset, "gasp header is truncated")
		}
		count := int(u16(data, 2))
		if count > (len(data)-4)/4 || (target.offset-4)/4 >= count-1 {
			return fontError(ErrMalformed, "MVAR.target.gasp", span.Offset+2,
				"gasp ranges are truncated or the MVAR record targets the final or absent range")
		}
	}
	if target.offset > len(data)-2 {
		return fontError(ErrMalformed, "MVAR.target", span.Offset+int64(target.offset), "target field is truncated")
	}
	metric.RawSource = Span{Offset: span.Offset + int64(target.offset), Length: 2}
	metric.RawValue = int64(i16(data, target.offset))
	if target.unsigned {
		metric.RawValue = int64(u16(data, target.offset))
	}
	metric.HasRawValue = true
	return nil
}

// VariationMetrics returns an owned copy of all MVAR records in binary tag order.
// Deltas remain precise values in their source field's units; hhea leading is not modified.
func (f *Face) VariationMetrics(ctx context.Context) ([]VariationMetric, error) {
	budget, err := f.variationCopyBudget(ctx)
	if err != nil {
		return nil, err
	}
	if f.mvar == nil {
		return nil, nil
	}
	if err := budget.array(uint64(len(f.mvar.metrics)), int64(unsafe.Sizeof(VariationMetric{})),
		"MVAR.copy.memory", f.mvar.source.Offset); err != nil {
		return nil, err
	}
	result := make([]VariationMetric, len(f.mvar.metrics))
	for index, metric := range f.mvar.metrics {
		if err := budget.step("MVAR.copy.record", metric.Source.Offset); err != nil {
			return nil, err
		}
		result[index] = metric
	}
	if err := budget.ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
