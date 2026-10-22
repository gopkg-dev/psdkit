package psdfont

import (
	"context"
	"unsafe"
)

// Glyph decodes a design-space outline into independent owned points and quadratic or cubic commands.
// TrueType instructions and Type2 hints never execute pixel-grid fitting.
// Name-keyed CFF1 endchar components resolve StandardEncoding names, including .notdef, inside this font.
// Their raw offsets precede FontMatrix application; an existing parent path is retained before both components.
// Parent hmtx and Type2 width remain authoritative. Nested components and cycles are rejected; CID is unsupported.
func (f *Face) Glyph(ctx context.Context, id uint16, options ...GlyphOptions) (*Glyph, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil || f.raw == nil || id >= f.NumGlyphs {
		return nil, fontError(ErrMalformed, "glyph.index", 0, "missing face or glyph outside the font")
	}
	limits := GlyphOptions{MaxWorkingBytes: 16 << 20, MaxPoints: 65536, MaxContours: 8192,
		MaxComponents: 4096, MaxDepth: 32, MaxOperations: 1_000_000}
	if len(options) > 1 {
		return nil, fontError(ErrMalformed, "glyph.options", 0, "provide at most one options value")
	}
	if len(options) == 1 {
		provided := options[0]
		limits.PreserveCFFHints = provided.PreserveCFFHints
		if provided.MaxWorkingBytes < 0 || provided.MaxPoints < 0 || provided.MaxContours < 0 ||
			provided.MaxComponents < 0 || provided.MaxDepth < 0 || provided.MaxDepth > 256 || provided.MaxOperations < 0 {
			return nil, fontError(ErrLimit, "glyph.options", 0, "negative limits or depth above 256")
		}
		if provided.MaxWorkingBytes != 0 {
			limits.MaxWorkingBytes = provided.MaxWorkingBytes
		}
		for _, pair := range [][2]*int{
			{&limits.MaxPoints, &provided.MaxPoints}, {&limits.MaxContours, &provided.MaxContours},
			{&limits.MaxComponents, &provided.MaxComponents}, {&limits.MaxDepth, &provided.MaxDepth},
			{&limits.MaxOperations, &provided.MaxOperations},
		} {
			if *pair[1] != 0 {
				*pair[0] = *pair[1]
			}
		}
	}
	if f.cff2 != nil {
		return f.cff2Glyph(ctx, id, limits)
	}
	if f.cff != nil {
		return f.cffGlyph(ctx, id, limits)
	}
	reader := glyphReader{face: f, ctx: ctx, limits: limits, active: make(map[uint16]bool)}
	if err := reader.reserve(1024, 0); err != nil {
		return nil, err
	}
	glyph, err := reader.decode(id, 1)
	if err != nil {
		return nil, err
	}
	points := 0
	for _, contour := range glyph.Contours {
		points += len(contour)
	}
	commandCount := points + len(glyph.Contours)*2
	if err := reader.reserve(int64(commandCount)*int64(unsafe.Sizeof(Command{})), glyph.Source.Offset); err != nil {
		return nil, err
	}
	glyph.Path = make([]Command, 0, commandCount)
	for _, contour := range glyph.Contours {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		glyph.Path = appendContour(glyph.Path, contour)
	}
	glyph.reservation = reader.allocated
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return glyph, nil
}

type glyphReader struct {
	face                     *Face
	ctx                      context.Context
	limits                   GlyphOptions
	active                   map[uint16]bool
	allocated                int64
	points, contours, visits int
}

func (r *glyphReader) reserve(length int64, offset int64) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if length < 0 || length > r.limits.MaxWorkingBytes-r.allocated || uint64(length) > uint64(^uint(0)>>1) {
		return fontError(ErrLimit, "glyph.memory", offset, "owned outline exceeds MaxWorkingBytes")
	}
	r.allocated += length
	return nil
}

func (f *Face) glyphSpan(id uint16) (Span, error) {
	width := 2
	if f.locaLong {
		width = 4
	}
	loca, err := f.table("loca", (int(id)+2)*width)
	if err != nil {
		return Span{}, err
	}
	begin, end := uint32(u16(loca, int(id)*width))*2, uint32(u16(loca, (int(id)+1)*width))*2
	if f.locaLong {
		begin, end = u32(loca, int(id)*4), u32(loca, (int(id)+1)*4)
	}
	span := f.tables["glyf"]
	if end < begin || int64(end) > span.Length {
		return Span{}, fontError(ErrMalformed, "glyph.span", span.Offset, "glyph is outside glyf")
	}
	return Span{Offset: span.Offset + int64(begin), Length: int64(end - begin)}, nil
}

func (f *Face) metrics(id uint16) (uint16, int16, error) {
	data, err := f.table("hmtx", int(f.horizontalMetrics)*4+max(0, int(id)-int(f.horizontalMetrics)+1)*2)
	if err != nil {
		return 0, 0, err
	}
	if id < f.horizontalMetrics {
		return u16(data, int(id)*4), i16(data, int(id)*4+2), nil
	}
	return u16(data, (int(f.horizontalMetrics)-1)*4),
		i16(data, int(f.horizontalMetrics)*4+(int(id)-int(f.horizontalMetrics))*2), nil
}

func (r *glyphReader) decode(id uint16, depth int) (*Glyph, error) {
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if id >= r.face.NumGlyphs {
		return nil, fontError(ErrMalformed, "glyph.component", 0, "component glyph index is outside the font")
	}
	if r.active[id] {
		return nil, fontError(ErrMalformed, "glyph.component", 0, "cyclic compound glyph graph")
	}
	if depth > r.limits.MaxDepth || r.visits >= r.limits.MaxComponents {
		return nil, fontError(ErrLimit, "glyph.components", 0, "component nesting or visit count exceeds limits")
	}
	if err := r.reserve(256, 0); err != nil {
		return nil, err
	}
	r.visits++
	r.active[id] = true
	defer delete(r.active, id)
	span, err := r.face.glyphSpan(id)
	if err != nil {
		return nil, err
	}
	advance, bearing, err := r.face.metrics(id)
	if err != nil {
		return nil, err
	}
	glyph := &Glyph{ID: id, Source: span, AdvanceWidth: advance, LeftSideBearing: bearing}
	if span.Length == 0 {
		return glyph, nil
	}
	if span.Length < 10 {
		return nil, fontError(ErrMalformed, "glyph.header", span.Offset, "truncated glyph header")
	}
	data := r.face.raw[int(span.Offset):int(span.Offset+span.Length)]
	count := int(i16(data, 0))
	for index := range 4 {
		glyph.Bounds[index] = i16(data, 2+index*2)
	}
	if glyph.Bounds[0] > glyph.Bounds[2] || glyph.Bounds[1] > glyph.Bounds[3] {
		return nil, fontError(ErrMalformed, "glyph.bounds", span.Offset+2, "glyph bounds are reversed")
	}
	if count >= 0 {
		return glyph, r.simple(glyph, data, count)
	}
	return glyph, r.compound(glyph, data, depth)
}

func (r *glyphReader) instructions(glyph *Glyph, data []byte, position int) (int, error) {
	if position+2 > len(data) {
		return position, fontError(ErrMalformed, "glyph.instructions", glyph.Source.Offset+int64(position),
			"truncated instruction length")
	}
	length := int(u16(data, position))
	position += 2
	if length > len(data)-position {
		return position, fontError(ErrMalformed, "glyph.instructions", glyph.Source.Offset+int64(position),
			"instruction bytes exceed the glyph span")
	}
	if length != 0 {
		if err := r.reserve(32, glyph.Source.Offset+int64(position)); err != nil {
			return position, err
		}
		glyph.Instructions = append(glyph.Instructions, Span{Offset: glyph.Source.Offset + int64(position),
			Length: int64(length)})
	}
	return position + length, nil
}

func (r *glyphReader) simple(glyph *Glyph, data []byte, contours int) error {
	if contours > r.limits.MaxContours-r.contours {
		return fontError(ErrLimit, "glyph.contours", glyph.Source.Offset, "contour count exceeds MaxContours")
	}
	if contours == 0 {
		if len(data) > 10 {
			_, err := r.instructions(glyph, data, 10)
			return err
		}
		return nil
	}
	if 10+contours*2+2 > len(data) {
		return fontError(ErrMalformed, "glyph.endpoints", glyph.Source.Offset+10, "truncated contour endpoint array")
	}
	points := int(u16(data, 10+(contours-1)*2)) + 1
	if points > r.limits.MaxPoints-r.points {
		return fontError(ErrLimit, "glyph.points", glyph.Source.Offset+10, "point count exceeds MaxPoints")
	}
	previous := -1
	for index := range contours {
		end := int(u16(data, 10+index*2))
		if end <= previous {
			return fontError(ErrMalformed, "glyph.endpoints", glyph.Source.Offset+int64(10+index*2),
				"contour endpoints are not strictly ordered")
		}
		previous = end
	}
	pointBytes := int64(points)*int64(unsafe.Sizeof(Point{})+1) + int64(contours)*24
	if err := r.reserve(pointBytes, glyph.Source.Offset); err != nil {
		return err
	}
	r.points, r.contours = r.points+points, r.contours+contours
	position, err := r.instructions(glyph, data, 10+contours*2)
	if err != nil {
		return err
	}
	flags := make([]byte, points)
	for index := 0; index < points; {
		if index%256 == 0 && r.ctx.Err() != nil {
			return r.ctx.Err()
		}
		if position >= len(data) {
			return fontError(ErrMalformed, "glyph.flags", glyph.Source.Offset+int64(position), "truncated point flags")
		}
		flag := data[position]
		position++
		if flag&0x80 != 0 {
			return fontError(ErrUnsupported, "glyph.flags", glyph.Source.Offset+int64(position-1),
				"reserved point flag is set")
		}
		repeat := 1
		if flag&8 != 0 {
			if position >= len(data) {
				return fontError(ErrMalformed, "glyph.flags", glyph.Source.Offset+int64(position), "missing flag repeat count")
			}
			repeat += int(data[position])
			position++
		}
		if repeat > points-index {
			return fontError(ErrMalformed, "glyph.flags", glyph.Source.Offset+int64(position), "repeat exceeds point count")
		}
		for count := range repeat {
			flags[index+count] = flag
		}
		index += repeat
	}
	owned := make([]Point, points)
	for axis := range 2 {
		coordinate := int32(0)
		for index, flag := range flags {
			if index%256 == 0 && r.ctx.Err() != nil {
				return r.ctx.Err()
			}
			short, positive := byte(2<<axis), byte(16<<axis)
			if flag&short != 0 {
				if position >= len(data) {
					return fontError(ErrMalformed, "glyph.coordinate", glyph.Source.Offset+int64(position),
						"missing short vector")
				}
				delta := int32(data[position])
				position++
				if flag&positive == 0 {
					delta = -delta
				}
				coordinate += delta
			} else if flag&positive == 0 {
				if position+2 > len(data) {
					return fontError(ErrMalformed, "glyph.coordinate", glyph.Source.Offset+int64(position), "missing long vector")
				}
				coordinate += int32(i16(data, position))
				position += 2
			}
			if coordinate < -32768 || coordinate > 32767 {
				return fontError(ErrMalformed, "glyph.coordinate", glyph.Source.Offset+int64(position),
					"accumulated design coordinate exceeds FWORD range")
			}
			if axis == 0 {
				owned[index].X = float64(coordinate)
			} else {
				owned[index].Y = float64(coordinate)
			}
			owned[index].OnCurve = flag&1 != 0
		}
	}
	glyph.Contours = make([][]Point, contours)
	begin := 0
	for index := range contours {
		end := int(u16(data, 10+index*2)) + 1
		glyph.Contours[index] = owned[begin:end:end]
		begin = end
	}
	return nil
}

func (r *glyphReader) compound(glyph *Glyph, data []byte, depth int) error {
	position := 10
	instructions := false
	components := 0
	for {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if position+4 > len(data) {
			return fontError(ErrMalformed, "glyph.component", glyph.Source.Offset+int64(position),
				"truncated component header")
		}
		flags, id := u16(data, position), u16(data, position+2)
		position += 4
		if components == 0 && flags&2 == 0 {
			return fontError(ErrMalformed, "glyph.component.arguments", glyph.Source.Offset+int64(position-4),
				"first component requires an explicit offset")
		}
		components++
		if flags&0xe010 != 0 || flags&0x1800 == 0x1800 ||
			flags&8 != 0 && flags&0xc0 != 0 || flags&0xc0 == 0xc0 {
			return fontError(ErrUnsupported, "glyph.component.flags", glyph.Source.Offset+int64(position-4),
				"reserved or conflicting component transformation flags")
		}
		width := 2
		if flags&1 != 0 {
			width = 4
		}
		if position+width > len(data) {
			return fontError(ErrMalformed, "glyph.component.arguments", glyph.Source.Offset+int64(position),
				"truncated arguments")
		}
		arg1, arg2 := int(data[position]), int(data[position+1])
		if width == 4 {
			arg1, arg2 = int(u16(data, position)), int(u16(data, position+2))
		}
		if flags&2 != 0 {
			if width == 4 {
				arg1, arg2 = int(int16(arg1)), int(int16(arg2))
			} else {
				arg1, arg2 = int(int8(arg1)), int(int8(arg2))
			}
		}
		position += width
		matrix := [4]float64{1, 0, 0, 1}
		count := 0
		if flags&8 != 0 {
			count = 1
		} else if flags&0x40 != 0 {
			count = 2
		} else if flags&0x80 != 0 {
			count = 4
		}
		if position+count*2 > len(data) {
			return fontError(ErrMalformed, "glyph.component.transform", glyph.Source.Offset+int64(position),
				"truncated F2Dot14 matrix")
		}
		switch count {
		case 1:
			matrix[0] = float64(i16(data, position)) / 16384
			matrix[3] = matrix[0]
		case 2:
			matrix[0], matrix[3] = float64(i16(data, position))/16384, float64(i16(data, position+2))/16384
		case 4:
			for index := range 4 {
				matrix[index] = float64(i16(data, position+index*2)) / 16384
			}
		}
		position += count * 2
		child, err := r.decode(id, depth+1)
		if err != nil {
			return err
		}
		for _, contour := range child.Contours {
			for index := range contour {
				if index%256 == 0 && r.ctx.Err() != nil {
					return r.ctx.Err()
				}
				point := &contour[index]
				x, y := point.X, point.Y
				point.X, point.Y = matrix[0]*x+matrix[2]*y, matrix[1]*x+matrix[3]*y
			}
		}
		dx, dy := float64(arg1), float64(arg2)
		if flags&2 != 0 {
			if flags&0x800 != 0 {
				dx, dy = matrix[0]*dx+matrix[2]*dy, matrix[1]*dx+matrix[3]*dy
			}
		} else {
			parentPoint, parentOK := glyphPoint(glyph.Contours, arg1)
			childPoint, childOK := glyphPoint(child.Contours, arg2)
			if !parentOK || !childOK {
				return fontError(ErrUnsupported, "glyph.component.pointMatch", glyph.Source.Offset+int64(position),
					"phantom-point or absent-point matching requires a TrueType hinting engine")
			}
			dx, dy = parentPoint.X-childPoint.X, parentPoint.Y-childPoint.Y
		}
		for _, contour := range child.Contours {
			for index := range contour {
				if index%256 == 0 && r.ctx.Err() != nil {
					return r.ctx.Err()
				}
				contour[index].X += dx
				contour[index].Y += dy
			}
		}
		componentBytes := int64(len(child.Contours))*48 + int64(len(child.Instructions))*32
		if err := r.reserve(componentBytes, glyph.Source.Offset); err != nil {
			return err
		}
		glyph.Contours = append(glyph.Contours, child.Contours...)
		glyph.Instructions = append(glyph.Instructions, child.Instructions...)
		glyph.HasComponentMetrics = glyph.HasComponentMetrics || child.HasComponentMetrics || flags&0x200 != 0
		instructions = instructions || flags&0x100 != 0
		if flags&0x20 == 0 {
			break
		}
	}
	if instructions {
		_, err := r.instructions(glyph, data, position)
		return err
	}
	return nil
}

func glyphPoint(contours [][]Point, index int) (Point, bool) {
	if index < 0 {
		return Point{}, false
	}
	for _, contour := range contours {
		if index < len(contour) {
			return contour[index], true
		}
		index -= len(contour)
	}
	return Point{}, false
}

func appendContour(path []Command, contour []Point) []Command {
	if len(contour) == 0 {
		return path
	}
	first, last := contour[0], contour[len(contour)-1]
	start, begin, end := first, 1, len(contour)
	if !first.OnCurve {
		begin = 0
		if last.OnCurve {
			start, end = last, len(contour)-1
		} else {
			start = Point{X: (first.X + last.X) / 2, Y: (first.Y + last.Y) / 2, OnCurve: true}
		}
	}
	path = append(path, Command{Op: Move, X: start.X, Y: start.Y})
	control, pending := Point{}, false
	for _, point := range contour[begin:end] {
		if point.OnCurve {
			if pending {
				path = append(path, Command{Op: Quad, X: point.X, Y: point.Y, ControlX: control.X, ControlY: control.Y})
			} else {
				path = append(path, Command{Op: Line, X: point.X, Y: point.Y})
			}
			pending = false
		} else {
			if pending {
				path = append(path, Command{Op: Quad, X: (control.X + point.X) / 2, Y: (control.Y + point.Y) / 2,
					ControlX: control.X, ControlY: control.Y})
			}
			control, pending = point, true
		}
	}
	if pending {
		path = append(path, Command{Op: Quad, X: start.X, Y: start.Y, ControlX: control.X, ControlY: control.Y})
	}
	return append(path, Command{Op: Close})
}
