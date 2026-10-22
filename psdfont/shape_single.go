package psdfont

func (p *otParser) singleSub(at int) (otSub, error) {
	if err := p.need(at, 6); err != nil {
		return otSub{}, err
	}
	format := shapeU16(p.data[at:])
	if format != 1 && format != 2 {
		return otSub{}, p.bad(at, "single substitution format must be one or two")
	}
	coverageAt, err := p.off(at, at+2, 4)
	if err != nil {
		return otSub{}, err
	}
	coverage, err := p.coverage(coverageAt)
	if err != nil {
		return otSub{}, err
	}
	if format == 2 {
		count := int(shapeU16(p.data[at+4:]))
		if count != len(coverage) {
			return otSub{}, p.bad(at+4, "single output count differs from coverage")
		}
		if err := p.array(at+6, count, 2); err != nil {
			return otSub{}, err
		}
	} else if err := p.b.take(len(coverage), int64(len(coverage))*2); err != nil {
		return otSub{}, err
	}
	result := otSub{coverage: coverage, singleIDs: make([]uint16, len(coverage))}
	delta := int32(shapeI16(p.data[at+4:]))
	for index, glyph := range coverage {
		if err := p.b.take(1, 0); err != nil {
			return otSub{}, err
		}
		output := uint16(int32(glyph) + delta)
		if format == 2 {
			output = shapeU16(p.data[at+6+index*2:])
		}
		if output >= p.numGlyphs {
			return otSub{}, p.bad(at, "single substitution output exceeds glyph count")
		}
		result.singleIDs[index] = output
	}
	return result, p.b.ctx.Err()
}
