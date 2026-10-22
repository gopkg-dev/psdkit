package psdfont

// readLayoutVariationStore retains GDEF's store in the selected immutable instance.
// Only the variation store is consumed here; lookup flags remain checked by Shape.
func (f *Face) readLayoutVariationStore(budget *fontMetadataBudget) error {
	span, exists := f.tables["GDEF"]
	if !exists || f.variation == nil || len(f.variation.axes) == 0 {
		return nil
	}
	data, err := f.table("GDEF", 12)
	if err != nil {
		return err
	}
	version := u32(data, 0)
	if version != 0x10000 && version != 0x10002 && version != 0x10003 {
		return fontError(ErrUnsupported, "GDEF.version", span.Offset, "unsupported GDEF version")
	}
	if version != 0x10003 {
		return nil
	}
	if len(data) < 18 {
		return fontError(ErrMalformed, "GDEF.header", span.Offset, "variation header is truncated")
	}
	offset := uint64(u32(data, 14))
	if offset == 0 {
		return nil
	}
	if offset > uint64(len(data)) {
		return fontError(ErrMalformed, "GDEF.store", span.Offset+14, "store offset exceeds GDEF")
	}
	store, err := readItemVariationStore(data, span.Offset, int(offset), len(f.variation.axes), budget)
	if err != nil {
		return err
	}
	if err := store.prepareScalars(f.variation.normalized, budget); err != nil {
		return err
	}
	f.gdefStore = store
	return budget.ctx.Err()
}

func (p *otParser) off32(base, field, size int) (int, error) {
	if err := p.need(field, 4); err != nil {
		return 0, err
	}
	delta := uint64(shapeU32(p.data[field:]))
	if delta == 0 || base < 0 || base > len(p.data) || delta > uint64(len(p.data)-base) {
		return 0, p.bad(field, "required 32-bit offset exceeds table")
	}
	at := base + int(delta)
	return at, p.need(at, size)
}

// featureAlternates validates every record and chooses the first supported matching record.
// Unknown condition formats and absent axes fail a condition, as required for forward compatibility.
func (p *otParser) featureAlternates(version uint32, featureCount int) (map[uint16]int, error) {
	if version != 0x10001 || shapeU32(p.data[10:]) == 0 {
		return nil, nil
	}
	at, err := p.off32(0, 10, 8)
	if err != nil {
		return nil, err
	}
	if shapeU32(p.data[at:]) != 0x10000 {
		return nil, fontError(ErrUnsupported, "shape.featureVariations", int64(at), "unsupported variation version")
	}
	count := uint64(shapeU32(p.data[at+4:]))
	if count > uint64((len(p.data)-at-8)/8) {
		return nil, p.bad(at+4, "variation records exceed table")
	}
	if err := p.b.take(int(count), 0); err != nil {
		return nil, err
	}
	var selected map[uint16]int
	matched := false
	for index := 0; index < int(count); index++ {
		if err := p.b.take(1, 0); err != nil {
			return nil, err
		}
		position := at + 8 + index*8
		matches := true
		if shapeU32(p.data[position:]) != 0 {
			conditions, err := p.off32(at, position, 2)
			if err != nil {
				return nil, err
			}
			matches, err = p.conditionsMatch(conditions)
			if err != nil {
				return nil, err
			}
		}
		if shapeU32(p.data[position+4:]) == 0 {
			if matches && !matched {
				matched = true
			}
			continue
		}
		substitution, err := p.off32(at, position+4, 4)
		if err != nil {
			return nil, err
		}
		if shapeU32(p.data[substitution:]) != 0x10000 {
			// Unsupported substitution versions do not consume a matching condition record.
			continue
		}
		if err := p.need(substitution, 6); err != nil {
			return nil, err
		}
		n := int(shapeU16(p.data[substitution+4:]))
		if err := p.array(substitution+6, n, 6); err != nil {
			return nil, err
		}
		choose := matches && !matched
		if choose {
			if err := p.b.take(n, int64(n)*64); err != nil {
				return nil, err
			}
			selected = make(map[uint16]int, n)
			matched = true
		}
		previous := -1
		for record := range n {
			if err := p.b.take(1, 0); err != nil {
				return nil, err
			}
			position := substitution + 6 + record*6
			feature := int(shapeU16(p.data[position:]))
			if feature <= previous || feature >= featureCount {
				return nil, p.bad(position, "alternate feature indices must be unique, ordered and in range")
			}
			previous = feature
			alternate, err := p.off32(substitution, position+2, 4)
			if err != nil {
				return nil, err
			}
			nlookups := int(shapeU16(p.data[alternate+2:]))
			if err := p.array(alternate+4, nlookups, 2); err != nil {
				return nil, err
			}
			if choose {
				selected[uint16(feature)] = alternate
			}
		}
	}
	return selected, p.b.ctx.Err()
}

func (p *otParser) conditionsMatch(at int) (bool, error) {
	count := int(shapeU16(p.data[at:]))
	if err := p.array(at+2, count, 4); err != nil {
		return false, err
	}
	matched := true
	for index := range count {
		if err := p.b.take(1, 0); err != nil {
			return false, err
		}
		condition, err := p.off32(at, at+2+index*4, 2)
		if err != nil {
			return false, err
		}
		if shapeU16(p.data[condition:]) != 1 {
			matched = false
			continue
		}
		if err := p.need(condition, 8); err != nil {
			return false, err
		}
		axis := int(shapeU16(p.data[condition+2:]))
		low, high := shapeI16(p.data[condition+4:]), shapeI16(p.data[condition+6:])
		if low < -16384 || high > 16384 || low > high {
			return false, p.bad(condition+4, "condition bounds exceed normalized domain")
		}
		if p.face == nil || p.face.variation == nil || axis >= len(p.face.variation.normalized) {
			matched = false
			continue
		}
		coordinate := p.face.variation.normalized[axis]
		matched = matched && coordinate >= low && coordinate <= high
	}
	return matched, p.b.ctx.Err()
}

func (p *otParser) positionVariation(subtable, field int) (float64, bool, error) {
	if err := p.b.take(1, 0); err != nil {
		return 0, false, err
	}
	offset := int(shapeU16(p.data[field:]))
	if offset == 0 {
		return 0, false, nil
	}
	if subtable < 0 || subtable > len(p.data) || offset > len(p.data)-subtable {
		return 0, false, p.bad(field, "device offset exceeds positioning table")
	}
	at := subtable + offset
	if err := p.need(at, 6); err != nil {
		return 0, false, err
	}
	if shapeU16(p.data[at+4:]) != 0x8000 {
		return 0, false, fontError(ErrUnsupported, "shape.device", int64(at), "hinted device adjustment unsupported")
	}
	if shapeU16(p.data[at:]) == 0xffff && shapeU16(p.data[at+2:]) == 0xffff {
		return 0, false, nil
	}
	if p.face == nil || p.face.variation == nil || len(p.face.variation.axes) == 0 {
		return 0, false, fontError(ErrUnsupported, "shape.variationIndex", int64(at),
			"variation positioning requires an explicitly selected variable instance")
	}
	if p.face == nil || p.face.gdefStore == nil {
		return 0, false, fontError(ErrMalformed, "shape.variationIndex", int64(at), "required GDEF variation store absent")
	}
	delta, err := p.face.gdefStore.delta(shapeU16(p.data[at:]), shapeU16(p.data[at+2:]),
		func(_ string, _ int64) error { return p.b.take(1, 0) })
	return delta, true, err
}
