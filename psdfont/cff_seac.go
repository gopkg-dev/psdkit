package psdfont

import "unsafe"

// standardComponent resolves StandardEncoding by glyph name, independently of cmap and the CFF Encoding.
func (r *type2Reader) standardComponent(code int, offset int64) (uint16, error) {
	if code < 0 || code >= len(cffStandardEncoding) {
		return 0, r.fail(ErrMalformed, offset, "endchar component code is outside StandardEncoding")
	}
	encoding := cffStandardEncoding[code]
	found, component := false, uint16(0)
	for index, sid := range r.face.cff.charset {
		if err := r.step(offset); err != nil {
			return 0, err
		}
		matches := sid == encoding.standardSID
		if sid >= 391 {
			stringIndex := int(sid) - 391
			if stringIndex >= len(r.face.cff.strings) {
				return 0, r.fail(ErrMalformed, offset, "endchar charset name refers to absent String INDEX")
			}
			span := r.face.cff.strings[stringIndex]
			if span.Offset < 0 || span.Length < 0 || span.Offset > int64(len(r.face.raw)) ||
				span.Length > int64(len(r.face.raw))-span.Offset {
				return 0, r.fail(ErrMalformed, offset, "endchar component name span exceeds owned font")
			}
			data := r.face.raw[span.Offset : span.Offset+span.Length]
			matches = len(data) == len(encoding.glyphName)
			if matches {
				for at, value := range data {
					if value != encoding.glyphName[at] {
						matches = false
						break
					}
				}
			}
		}
		if !matches {
			continue
		}
		if found {
			return 0, r.fail(ErrUnsupported, offset, "endchar component glyph name is ambiguous")
		}
		found, component = true, uint16(index)
	}
	if !found {
		return 0, r.fail(ErrMissingGlyph, offset, "endchar component glyph name is absent")
	}
	return component, nil
}

func (r *type2Reader) endcharComponents(offset int64, depth int) error {
	if r.face.cff.cid {
		return r.fail(ErrUnsupported, offset, "CID-keyed CFF lacks the endchar glyph-name domain")
	}
	if r.component {
		return r.fail(ErrMalformed, offset, "Type2 endchar components must not be nested")
	}
	adx, ady := r.stack[0], r.stack[1]
	baseCode, err := r.integer(r.stack[2], offset)
	if err != nil {
		return err
	}
	accentCode, err := r.integer(r.stack[3], offset)
	if err != nil {
		return err
	}
	base, err := r.standardComponent(baseCode, offset)
	if err != nil {
		return err
	}
	accent, err := r.standardComponent(accentCode, offset)
	if err != nil {
		return err
	}
	var spans [2]Span
	for index, id := range [2]uint16{base, accent} {
		if int(id) >= len(r.face.cff.charstrings) {
			return r.fail(ErrMalformed, offset, "endchar component is outside owned CharStrings")
		}
		spans[index] = r.face.cff.charstrings[id]
		if spans[index] == r.glyph.Source {
			return r.fail(ErrMalformed, offset, "cyclic Type2 endchar component")
		}
	}
	if err := r.close(); err != nil {
		return err
	}
	// One saved program frame and two provenance spans are charged before allocating or entering components.
	if err := r.reserve(int64(unsafe.Sizeof(type2Reader{})) + 2*int64(unsafe.Sizeof(Span{}))); err != nil {
		return err
	}
	r.glyph.ComponentSources = []Span{spans[0], spans[1]}
	parent := *r
	width := r.glyph.CharstringWidth
	for index, span := range spans {
		// Program registers restart, while the complete composite shares counters and one owned outline.
		r.stack, r.transient, r.stored = [48]float64{}, [32]float64{}, [32]bool{}
		r.count, r.hints, r.widthSeen, r.open = 0, 0, false, false
		r.x, r.y, r.offsetX, r.offsetY = 0, 0, 0, 0
		r.active, r.component = [11]Span{}, true
		r.programDepth = parent.programDepth + depth + 1
		if index == 1 {
			r.offsetX, r.offsetY = adx, ady
		}
		ended, err := r.execute(span, 0, false)
		if err != nil {
			return err
		}
		if !ended {
			return r.fail(ErrMalformed, span.Offset, "endchar component did not terminate")
		}
	}
	allocated, operations, visits, points := r.allocated, r.operations, r.visits, r.points
	*r = parent
	r.allocated, r.operations, r.visits, r.points = allocated, operations, visits, points
	r.glyph.CharstringWidth, r.count = width, 0
	return r.ctx.Err()
}
