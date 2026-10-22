package psdparse

import "github.com/gopkg-dev/psdkit/psdmodel"

func (p *parser) resources(r *cursor) {
	for r.remaining() >= 12 && r.err == nil {
		if r.sig() != "8BIM" {
			r.fail("invalid PSD image resource signature")
			return
		}
		id := r.u16()
		r.pascal(2)
		length := int64(r.u32())
		resource := r.section(length)
		if id == 1050 {
			p.slices(resource)
		}
		r.absorb(resource)
		if length%2 != 0 && r.remaining() > 0 {
			r.skip(1)
		}
	}
}

func (p *parser) slices(r *cursor) {
	version := r.u32()
	switch version {
	case 6:
		r.skip(16)
		r.unicode()
		count := r.u32()
		if count > 10000 {
			r.fail("PSD slice count exceeds limit")
			return
		}
		for index := uint32(0); index < count && r.err == nil; index++ {
			r.u32()
			r.u32()
			origin := r.u32()
			if origin == 1 {
				r.u32()
			}
			name := r.unicode()
			kind := r.u32()
			left, top, right, bottom := int(r.i32()), int(r.i32()), int(r.i32()), int(r.i32())
			for range 4 {
				r.unicode()
			}
			r.u8()
			r.unicode()
			r.skip(12)
			if origin == 2 && kind == 1 {
				p.document.Slices = append(p.document.Slices, psdmodel.Slice{
					Name: name, Left: left, Top: top, Width: right - left, Height: bottom - top,
					Origin: "userGenerated", Type: "image"})
			}
		}
		// Optional trailing descriptors only contain slice outsets, unused by the editor.
	case 7, 8:
		value := readVersionDescriptor(r)
		items, _ := value["slices"].([]any)
		for _, item := range items {
			slice := desc(item)
			origin, kind := enumValue(slice["origin"]), enumValue(slice["Type"])
			if origin != "userGenerated" || kind != "Img " {
				continue
			}
			bounds := desc(slice["bounds"])
			left, top := int(number(bounds["Left"])), int(number(bounds["Top "]))
			p.document.Slices = append(p.document.Slices, psdmodel.Slice{Name: text(slice["Nm  "]), Left: left, Top: top,
				Width: int(number(bounds["Rght"])) - left, Height: int(number(bounds["Btom"])) - top,
				Origin: "userGenerated", Type: "image"})
		}
	default:
		r.fail("unsupported Photoshop slice version %d", version)
	}
}
