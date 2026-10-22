package psdfont

import (
	"context"
	"encoding/binary"
	"math"
	"sort"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// ShapeFeature explicitly enables or disables an optional OpenType feature.
// Required LangSys features execute even when a setting disables their tag.
type ShapeFeature struct {
	Tag     string
	Enabled bool
}

// ShapeOptions selects one Latin Script/LangSys and bounds every parser and lookup traversal.
// Empty Script means latn; empty Language selects the default LangSys.
// Missing scripts or optional features are no-ops; an explicit missing language is unsupported.
// No optional features are enabled by default.
// Single and forward ligature substitutions and pair positioning are supported.
// Lookups must have zero flags; selected instance VariationIndex values are applied in design units.
// This explicit OpenType pipeline does not automatically apply AAT tables.
type ShapeOptions struct {
	Script, Language                     string
	Features                             []ShapeFeature
	MaxWorkingBytes                      int64
	MaxGlyphs, MaxNodes, MaxLookupVisits int
}

// ShapedGlyph retains a source UTF-16 span and positions in font design units.
// Positive Y points upwards. Rune is the first source character of a ligature.
type ShapedGlyph struct {
	GlyphID                              uint16
	Rune                                 rune
	ClusterStart, ClusterLength          int64
	XAdvance, YAdvance, XOffset, YOffset int64
	// InstancePosition retains precise values before final nearest/tie-positive design-unit projection.
	InstancePosition *InstancePosition `json:",omitempty"`
}

// ShapedRun owns its glyph IDs, clusters, and positions; the source Face owns font data.
type ShapedRun struct {
	Glyphs      []ShapedGlyph
	UTF16Units  int64
	UnitsPerEm  uint16
	reservation int64
}

// WorkingBytes reports the conservative peak allocation reserved for shaping.
// The existing owned Face is borrowed during shaping and is excluded from this reservation.
func (r *ShapedRun) WorkingBytes() int64 {
	if r == nil {
		return 0
	}
	return r.reservation
}

type shapeBudget struct {
	ctx                 context.Context
	nodes, visits       int
	bytes               int64
	maxNodes, maxVisits int
	maxBytes            int64
}

func (b *shapeBudget) take(nodes int, bytes int64) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if nodes < 0 || bytes < 0 || nodes > b.maxNodes-b.nodes || bytes > b.maxBytes-b.bytes {
		return fontError(ErrLimit, "shape.budget", 0, "parser nodes or owned bytes exceed limits")
	}
	b.nodes += nodes
	b.bytes += bytes
	return nil
}
func (b *shapeBudget) visit() error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if b.visits >= b.maxVisits {
		return fontError(ErrLimit, "shape.lookup", 0, "lookup visits exceed limit")
	}
	b.visits++
	return nil
}

// Shape executes a selected bounded Latin OpenType pipeline, independent of Layout.
// It uses left-to-right horizontal layout, so GPOS vertical YAdvance adjustments are not applied.
// It never executes legacy kern in addition to selected GPOS.
// Missing glyph zero, complex scripts, nonzero flags, contextual lookups and hinted devices error.
// Selected CFF2 instances retain precise totals and project integer positions with floor(value+.5) once.
func Shape(ctx context.Context, face *Face, text string, options ...ShapeOptions) (*ShapedRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if face == nil {
		return nil, ErrMissingFont
	}
	if face.NumGlyphs == 0 || face.UnitsPerEm < 16 || face.UnitsPerEm > 16384 || len(face.raw) == 0 {
		return nil, fontError(ErrMalformed, "shape.font", 0, "invalid owned face")
	}
	if len(options) > 1 {
		return nil, fontError(ErrMalformed, "shape.options", 0, "provide at most one options value")
	}
	o := ShapeOptions{Script: "latn", MaxWorkingBytes: 32 << 20,
		MaxGlyphs: 10000, MaxNodes: 200000, MaxLookupVisits: 1000000}
	if len(options) == 1 {
		p := options[0]
		if p.MaxWorkingBytes < 0 || p.MaxGlyphs < 0 || p.MaxNodes < 0 || p.MaxLookupVisits < 0 {
			return nil, fontError(ErrLimit, "shape.options", 0, "limits must not be negative")
		}
		if p.Script != "" {
			o.Script = p.Script
		}
		o.Language = p.Language
		o.Features = p.Features
		if p.MaxWorkingBytes != 0 {
			o.MaxWorkingBytes = p.MaxWorkingBytes
		}
		if p.MaxGlyphs != 0 {
			o.MaxGlyphs = p.MaxGlyphs
		}
		if p.MaxNodes != 0 {
			o.MaxNodes = p.MaxNodes
		}
		if p.MaxLookupVisits != 0 {
			o.MaxLookupVisits = p.MaxLookupVisits
		}
	}
	if !validOTTag(o.Script) || o.Language != "" && !validOTTag(o.Language) || !utf8.ValidString(text) {
		return nil, fontError(ErrMalformed, "shape.selection", 0, "invalid tags or UTF-8")
	}
	if o.Script != "latn" && o.Script != "DFLT" {
		return nil, fontError(ErrUnsupported, "shape.script", 0, "only explicit Latin or DFLT selection is supported")
	}
	b := &shapeBudget{ctx: ctx, maxNodes: o.MaxNodes, maxVisits: o.MaxLookupVisits, maxBytes: o.MaxWorkingBytes}
	if err := b.take(1, int64(len(text))+1024); err != nil {
		return nil, err
	}
	if err := b.take(len(o.Features), int64(len(o.Features))*64); err != nil {
		return nil, err
	}
	features := make(map[string]bool, len(o.Features))
	for _, f := range o.Features {
		if !validOTTag(f.Tag) {
			return nil, fontError(ErrMalformed, "shape.feature", 0, "feature tag must contain four printable ASCII bytes")
		}
		if _, ok := features[f.Tag]; ok {
			return nil, fontError(ErrMalformed, "shape.feature", 0, "duplicate feature setting")
		}
		features[f.Tag] = f.Enabled
	}
	count := utf8.RuneCountInString(text)
	if count > o.MaxGlyphs {
		return nil, fontError(ErrLimit, "shape.glyphs", 0, "input glyph count exceeds limit")
	}
	if err := b.take(count, int64(count)*int64(unsafe.Sizeof(ShapedGlyph{}))); err != nil {
		return nil, err
	}
	run := &ShapedRun{UnitsPerEm: face.UnitsPerEm, Glyphs: make([]ShapedGlyph, 0, count)}
	for _, r := range text {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !basicLatin(r) || unicode.Is(unicode.Cf, r) {
			return nil, fontError(ErrUnsupported, "shape.text", run.UTF16Units, "character requires unsupported shaping")
		}
		id, err := face.LookupRune(ctx, r)
		if err != nil {
			return nil, err
		}
		n := int64(1)
		if r > 0xffff {
			n = 2
		}
		run.Glyphs = append(run.Glyphs, ShapedGlyph{GlyphID: id, Rune: r, ClusterStart: run.UTF16Units, ClusterLength: n})
		run.UTF16Units += n
	}
	if err := shapeAdvances(face, run, b); err != nil {
		return nil, err
	}
	for _, tag := range []string{"GSUB", "GPOS"} {
		span, ok := face.tables[tag]
		if !ok {
			continue
		}
		if span.Offset < 0 || span.Length < 0 || span.Offset > int64(len(face.raw)) ||
			span.Length > int64(len(face.raw))-span.Offset {
			return nil, fontError(ErrMalformed, "shape."+tag, span.Offset, "table span outside owned source")
		}
		p := otParser{data: face.raw[span.Offset : span.Offset+span.Length], tag: tag,
			numGlyphs: face.NumGlyphs, b: b, face: face}
		lookups, err := p.selectLookups(o.Script, o.Language, features)
		if err != nil {
			return nil, err
		}
		for _, lookup := range lookups {
			for i := 0; i < len(run.Glyphs); {
				if err := b.visit(); err != nil {
					return nil, err
				}
				step := 1
				for _, sub := range lookup.subs {
					var matched bool
					if tag == "GSUB" {
						matched, err = sub.ligature(run, i, b)
					} else {
						matched, step, err = sub.pair(run, i)
					}
					if err != nil {
						return nil, err
					}
					if matched {
						break
					}
				}
				i += step
			}
		}
		if tag == "GSUB" { // Advances belong to the substituted glyphs, before GPOS adjustments.
			if err := shapeAdvances(face, run, b); err != nil {
				return nil, err
			}
		}
	}
	for index := range run.Glyphs {
		glyph := &run.Glyphs[index]
		if glyph.InstancePosition == nil {
			continue
		}
		position := glyph.InstancePosition
		if !openTypeCoordinate(position.XAdvance) || !openTypeCoordinate(position.YAdvance) ||
			!openTypeCoordinate(position.XOffset) || !openTypeCoordinate(position.YOffset) {
			return nil, fontError(ErrLimit, "shape.instance.position", glyph.ClusterStart, "position exceeds precision range")
		}
		glyph.XAdvance, glyph.YAdvance = int64(math.Floor(position.XAdvance+.5)), int64(math.Floor(position.YAdvance+.5))
		glyph.XOffset, glyph.YOffset = int64(math.Floor(position.XOffset+.5)), int64(math.Floor(position.YOffset+.5))
	}
	run.reservation = b.bytes
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return run, nil
}

func shapeAdvances(f *Face, r *ShapedRun, budget *shapeBudget) error {
	hmtx, err := f.table("hmtx", 0)
	if err != nil {
		return err
	}
	if f.horizontalMetrics == 0 {
		return fontError(ErrMalformed, "shape.hmtx", 0, "no horizontal metrics")
	}
	for i := range r.Glyphs {
		id := r.Glyphs[i].GlyphID
		if id == 0 {
			return ErrMissingGlyph
		}
		if id >= f.NumGlyphs {
			return fontError(ErrMalformed, "shape.glyph", 0, "glyph outside face")
		}
		index := min(id, f.horizontalMetrics-1)
		at := int(index) * 4
		if at > len(hmtx)-2 {
			return fontError(ErrMalformed, "shape.hmtx", 0, "truncated advance")
		}
		r.Glyphs[i].XAdvance = int64(shapeU16(hmtx[at:]))
		if f.cff2 != nil && f.variation != nil && len(f.variation.axes) != 0 {
			if r.Glyphs[i].InstancePosition == nil {
				if err := budget.take(1, int64(unsafe.Sizeof(InstancePosition{}))); err != nil {
					return err
				}
				r.Glyphs[i].InstancePosition = &InstancePosition{}
			}
			advance, err := f.instanceAdvance(id, func(_ string, _ int64) error { return budget.take(1, 0) })
			if err != nil {
				return err
			}
			r.Glyphs[i].InstancePosition.XAdvance = advance
		}
	}
	return nil
}
func validOTTag(s string) bool {
	if len(s) != 4 {
		return false
	}
	for i := range s {
		if s[i] < 32 || s[i] > 126 {
			return false
		}
	}
	return true
}

type otParser struct {
	data      []byte
	tag       string
	numGlyphs uint16
	b         *shapeBudget
	face      *Face
}

func (p *otParser) bad(at int, detail string) error {
	return fontError(ErrMalformed, "shape."+p.tag, int64(at), detail)
}
func (p *otParser) need(at, n int) error {
	if at < 0 || n < 0 || at > len(p.data) || n > len(p.data)-at {
		return p.bad(at, "offset or count outside table")
	}
	return nil
}
func (p *otParser) off(base, field, minSize int) (int, error) {
	if err := p.need(field, 2); err != nil {
		return 0, err
	}
	n := int(shapeU16(p.data[field:]))
	if n == 0 {
		return 0, p.bad(field, "required offset is zero")
	}
	if base > len(p.data)-n {
		return 0, p.bad(field, "offset outside table")
	}
	at := base + n
	return at, p.need(at, minSize)
}
func (p *otParser) array(at, count, size int) error {
	if err := p.b.take(count, int64(count)*int64(size)); err != nil {
		return err
	}
	if count > len(p.data)/max(1, size) {
		return p.bad(at, "count outside table")
	}
	return p.need(at, count*size)
}

type otLookup struct{ subs []otSub }
type otSub struct {
	coverage                 []uint16
	singleIDs                []uint16
	ligs                     [][]otLig
	pairSets                 [][]otPair
	class1, class2           []uint16
	matrix                   []otPair
	classCount1, classCount2 int
	valueFormat2             uint16
}
type otLig struct {
	output     uint16
	components []uint16
}
type otValue struct {
	x, y, xa, ya int64
	variation    *InstancePosition
}
type otPair struct {
	second                  uint16
	firstValue, secondValue otValue
}

func (p *otParser) selectLookups(script, language string, features map[string]bool) ([]otLookup, error) {
	if err := p.need(0, 10); err != nil {
		return nil, err
	}
	version := shapeU32(p.data)
	if version != 0x10000 && version != 0x10001 {
		return nil, fontError(ErrUnsupported, "shape."+p.tag, 0, "layout table version unsupported")
	}
	if version == 0x10001 {
		if err := p.need(0, 14); err != nil {
			return nil, err
		}
	}
	scripts, err := p.off(0, 4, 2)
	if err != nil {
		return nil, err
	}
	fl, err := p.off(0, 6, 2)
	if err != nil {
		return nil, err
	}
	ll, err := p.off(0, 8, 2)
	if err != nil {
		return nil, err
	}
	scriptAt, err := p.tagRecord(scripts, script)
	if err != nil {
		return nil, err
	}
	if scriptAt < 0 {
		return nil, nil
	}
	if err := p.need(scriptAt, 4); err != nil {
		return nil, err
	}
	langAt := -1
	if language == "" {
		if shapeU16(p.data[scriptAt:]) != 0 {
			langAt, err = p.off(scriptAt, scriptAt, 6)
		}
	} else {
		langAt, err = p.tagRecordAt(scriptAt, scriptAt+2, language)
	}
	if err != nil {
		return nil, err
	}
	if langAt < 0 {
		if language != "" {
			return nil, fontError(ErrUnsupported, "shape.language", int64(scriptAt), "explicit language absent")
		}
		return nil, nil
	}
	if err := p.need(langAt, 6); err != nil {
		return nil, err
	}
	if shapeU16(p.data[langAt:]) != 0 {
		return nil, p.bad(langAt, "reserved LangSys lookup order is nonzero")
	}
	required := shapeU16(p.data[langAt+2:])
	n := int(shapeU16(p.data[langAt+4:]))
	if err := p.array(langAt+6, n, 2); err != nil {
		return nil, err
	}
	if err := p.need(fl, 2); err != nil {
		return nil, err
	}
	nf := int(shapeU16(p.data[fl:]))
	if err := p.array(fl+2, nf, 6); err != nil {
		return nil, err
	}
	previousFeature := ""
	for i := range nf {
		tag := string(p.data[fl+2+i*6 : fl+6+i*6])
		// OpenType permits repeated feature tags (different feature records).
		if !validOTTag(tag) || i > 0 && tag < previousFeature {
			return nil, p.bad(fl+2+i*6, "feature records must be ordered")
		}
		previousFeature = tag
	}
	alternates, err := p.featureAlternates(version, nf)
	if err != nil {
		return nil, err
	}
	if err := p.need(ll, 2); err != nil {
		return nil, err
	}
	nl := int(shapeU16(p.data[ll:]))
	if err := p.array(ll+2, nl, 2); err != nil {
		return nil, err
	}
	// Both directory traversal maps reserve conservative bucket growth before allocation.
	if err := p.b.take(n+1+nl, int64(n+1+nl)*128); err != nil {
		return nil, err
	}
	selected := make(map[int]bool)
	selectedFeatures := make(map[uint16]bool)
	if required != 0xffff {
		if int(required) >= nf {
			return nil, p.bad(langAt+2, "required feature index out of range")
		}
		selectedFeatures[required] = true
	}
	for i := range n {
		fi := shapeU16(p.data[langAt+6+2*i:])
		if int(fi) >= nf {
			return nil, p.bad(langAt+6+2*i, "feature index out of range")
		}
		if selectedFeatures[fi] {
			continue
		}
		if features[string(p.data[fl+2+int(fi)*6:fl+6+int(fi)*6])] {
			selectedFeatures[fi] = true
		}
	}
	for fiInt := range nf {
		fi := uint16(fiInt)
		if !selectedFeatures[fi] {
			continue
		}
		at, err := p.off(fl, fl+6+int(fi)*6, 4)
		if err != nil {
			return nil, err
		}
		if alternate, exists := alternates[fi]; exists {
			at = alternate
		}
		// FeatureParams may carry required feature-specific semantics; this scope has none.
		if shapeU16(p.data[at:]) != 0 {
			return nil, fontError(ErrUnsupported, "shape.featureParams", int64(at), "selected feature parameters unsupported")
		}
		n := int(shapeU16(p.data[at+2:]))
		if err := p.array(at+4, n, 2); err != nil {
			return nil, err
		}
		for j := range n {
			li := int(shapeU16(p.data[at+4+2*j:]))
			if li >= nl {
				return nil, p.bad(at, "lookup index out of range")
			}
			selected[li] = true
		}
	}
	if err := p.b.take(len(selected), int64(len(selected))*64); err != nil {
		return nil, err
	}
	indices := make([]int, 0, len(selected))
	for i := range selected {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	result := make([]otLookup, 0, len(indices))
	for _, i := range indices {
		at, err := p.off(ll, ll+2+i*2, 6)
		if err != nil {
			return nil, err
		}
		l, err := p.lookup(at)
		if err != nil {
			return nil, err
		}
		result = append(result, l)
	}
	return result, nil
}

func (p *otParser) tagRecord(base int, wanted string) (int, error) {
	return p.tagRecordAt(base, base, wanted)
}
func (p *otParser) tagRecordAt(base, countAt int, wanted string) (int, error) {
	if err := p.need(countAt, 2); err != nil {
		return 0, err
	}
	n := int(shapeU16(p.data[countAt:]))
	if err := p.array(countAt+2, n, 6); err != nil {
		return 0, err
	}
	result := -1
	previous := ""
	for i := range n {
		at := countAt + 2 + i*6
		tag := string(p.data[at : at+4])
		if !validOTTag(tag) || i > 0 && tag <= previous {
			return 0, p.bad(at, "tag records must be ordered and unique")
		}
		previous = tag
		dest, err := p.off(base, at+4, 2)
		if err != nil {
			return 0, err
		}
		if tag == wanted {
			result = dest
		}
	}
	return result, nil
}

func (p *otParser) lookup(at int) (otLookup, error) {
	typ, flags, n := shapeU16(p.data[at:]), shapeU16(p.data[at+2:]), int(shapeU16(p.data[at+4:]))
	if flags&0x00e0 != 0 {
		return otLookup{}, p.bad(at+2, "reserved lookup flag bits set")
	}
	if flags != 0 {
		return otLookup{}, fontError(ErrUnsupported, "shape.lookupFlags", int64(at+2),
			"nonzero lookup flags require unsupported filtering semantics")
	}
	if err := p.array(at+6, n, 2); err != nil {
		return otLookup{}, err
	}
	if n == 0 {
		return otLookup{}, p.bad(at, "lookup has no subtables")
	}
	if err := p.b.take(n, int64(n)*int64(unsafe.Sizeof(otSub{}))); err != nil {
		return otLookup{}, err
	}
	result := otLookup{subs: make([]otSub, 0, n)}
	innerType := uint16(0)
	for i := range n {
		subAt, err := p.off(at, at+6+i*2, 2)
		if err != nil {
			return otLookup{}, err
		}
		subType := typ
		isExt := p.tag == "GSUB" && typ == 7 || p.tag == "GPOS" && typ == 9
		if isExt {
			if err := p.need(subAt, 8); err != nil {
				return otLookup{}, err
			}
			if shapeU16(p.data[subAt:]) != 1 {
				return otLookup{}, p.bad(subAt, "extension format must be one")
			}
			subType = shapeU16(p.data[subAt+2:])
			delta := uint64(shapeU32(p.data[subAt+4:]))
			if subType == typ || delta < 8 {
				return otLookup{}, p.bad(subAt, "nested or cyclic extension")
			}
			if innerType != 0 && subType != innerType {
				return otLookup{}, p.bad(subAt, "extension lookup types differ")
			}
			innerType = subType
			if delta > uint64(len(p.data)-subAt) {
				return otLookup{}, p.bad(subAt, "extension offset outside table")
			}
			subAt += int(delta)
			if err := p.need(subAt, 2); err != nil {
				return otLookup{}, err
			}
		}
		var sub otSub
		if p.tag == "GSUB" && subType == 1 {
			sub, err = p.singleSub(subAt)
		} else if p.tag == "GSUB" && subType == 4 {
			sub, err = p.ligatureSub(subAt)
		} else if p.tag == "GPOS" && subType == 2 {
			sub, err = p.pairSub(subAt)
		} else {
			return otLookup{}, fontError(ErrUnsupported, "shape.lookupType", int64(subAt), "selected lookup type unsupported")
		}
		if err != nil {
			return otLookup{}, err
		}
		result.subs = append(result.subs, sub)
	}
	return result, nil
}

func (p *otParser) coverage(at int) ([]uint16, error) {
	if err := p.need(at, 4); err != nil {
		return nil, err
	}
	format, n := shapeU16(p.data[at:]), int(shapeU16(p.data[at+2:]))
	var ids []uint16
	switch format {
	case 1:
		if err := p.array(at+4, n, 2); err != nil {
			return nil, err
		}
		ids = make([]uint16, n)
		for i := range ids {
			id := shapeU16(p.data[at+4+i*2:])
			if id >= p.numGlyphs || i > 0 && id <= ids[i-1] {
				return nil, p.bad(at, "invalid coverage glyph ordering")
			}
			ids[i] = id
		}
	case 2:
		if err := p.array(at+4, n, 6); err != nil {
			return nil, err
		}
		total := 0
		last := -1
		for i := range n {
			pos := at + 4 + i*6
			first, end, index := int(shapeU16(p.data[pos:])), int(shapeU16(p.data[pos+2:])), int(shapeU16(p.data[pos+4:]))
			if first > end || first <= last || end >= int(p.numGlyphs) || index != total {
				return nil, p.bad(pos, "invalid coverage range")
			}
			total += end - first + 1
			last = end
		}
		if err := p.b.take(total, int64(total)*2); err != nil {
			return nil, err
		}
		ids = make([]uint16, 0, total)
		for i := range n {
			pos := at + 4 + i*6
			for id, end := int(shapeU16(p.data[pos:])), int(shapeU16(p.data[pos+2:])); id <= end; id++ {
				ids = append(ids, uint16(id))
			}
		}
	default:
		return nil, p.bad(at, "unsupported coverage format")
	}
	return ids, nil
}
func (s *otSub) covered(id uint16) int {
	i := sort.Search(len(s.coverage), func(i int) bool { return s.coverage[i] >= id })
	if i == len(s.coverage) || s.coverage[i] != id {
		return -1
	}
	return i
}

func (p *otParser) ligatureSub(at int) (otSub, error) {
	if err := p.need(at, 6); err != nil {
		return otSub{}, err
	}
	if shapeU16(p.data[at:]) != 1 {
		return otSub{}, p.bad(at, "ligature substitution format must be one")
	}
	covAt, err := p.off(at, at+2, 4)
	if err != nil {
		return otSub{}, err
	}
	cov, err := p.coverage(covAt)
	if err != nil {
		return otSub{}, err
	}
	n := int(shapeU16(p.data[at+4:]))
	if n != len(cov) {
		return otSub{}, p.bad(at, "ligature set count differs from coverage")
	}
	if err := p.array(at+6, n, 2); err != nil {
		return otSub{}, err
	}
	if err := p.b.take(n, int64(n)*int64(unsafe.Sizeof([]otLig{}))); err != nil {
		return otSub{}, err
	}
	s := otSub{coverage: cov, ligs: make([][]otLig, n)}
	for i := range n {
		set, err := p.off(at, at+6+2*i, 2)
		if err != nil {
			return otSub{}, err
		}
		count := int(shapeU16(p.data[set:]))
		if err := p.array(set+2, count, 2); err != nil {
			return otSub{}, err
		}
		if err := p.b.take(count, int64(count)*32); err != nil {
			return otSub{}, err
		}
		s.ligs[i] = make([]otLig, count)
		for j := range count {
			ligAt, err := p.off(set, set+2+2*j, 4)
			if err != nil {
				return otSub{}, err
			}
			output, c := shapeU16(p.data[ligAt:]), int(shapeU16(p.data[ligAt+2:]))
			if output >= p.numGlyphs || c < 2 {
				return otSub{}, p.bad(ligAt, "invalid ligature output or component count")
			}
			if err := p.array(ligAt+4, c-1, 2); err != nil {
				return otSub{}, err
			}
			l := otLig{output: output, components: make([]uint16, c-1)}
			for k := range l.components {
				id := shapeU16(p.data[ligAt+4+2*k:])
				if id >= p.numGlyphs {
					return otSub{}, p.bad(ligAt, "component glyph outside face")
				}
				l.components[k] = id
			}
			s.ligs[i][j] = l
		}
	}
	return s, nil
}

// ligature applies a single or forward-ligature substitution without changing source clusters for singles.
func (s *otSub) ligature(r *ShapedRun, i int, b *shapeBudget) (bool, error) {
	if s.singleIDs != nil {
		if err := b.visit(); err != nil {
			return false, err
		}
		index := s.covered(r.Glyphs[i].GlyphID)
		if index < 0 {
			return false, nil
		}
		r.Glyphs[i].GlyphID = s.singleIDs[index]
		return true, nil
	}
	ci := s.covered(r.Glyphs[i].GlyphID)
	if ci < 0 {
		return false, nil
	}
	for _, lig := range s.ligs[ci] {
		if err := b.visit(); err != nil {
			return false, err
		}
		if len(lig.components) > len(r.Glyphs)-i-1 {
			continue
		}
		matches := true
		for j, id := range lig.components {
			if err := b.visit(); err != nil {
				return false, err
			}
			if r.Glyphs[i+1+j].GlyphID != id {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		last := i + len(lig.components)
		end := r.Glyphs[last].ClusterStart + r.Glyphs[last].ClusterLength
		r.Glyphs[i].GlyphID = lig.output
		r.Glyphs[i].ClusterLength = end - r.Glyphs[i].ClusterStart
		copy(r.Glyphs[i+1:], r.Glyphs[last+1:])
		r.Glyphs = r.Glyphs[:len(r.Glyphs)-len(lig.components)]
		return true, nil
	}
	return false, nil
}

func valueSize(format uint16) int {
	n := 0
	for bit := uint16(1); bit <= 128; bit <<= 1 {
		if format&bit != 0 {
			n += 2
		}
	}
	return n
}
func (p *otParser) value(at int, format uint16, subtable int) (otValue, error) {
	if format&0xff00 != 0 {
		return otValue{}, p.bad(at, "reserved value format bits set")
	}
	if err := p.need(at, valueSize(format)); err != nil {
		return otValue{}, err
	}
	v := otValue{}
	fields := []*int64{&v.x, &v.y, &v.xa, &v.ya}
	for j, d := range fields {
		if format&(1<<j) != 0 {
			*d = int64(shapeI16(p.data[at:]))
			at += 2
		}
	}
	for field := range 4 {
		if format&(16<<field) == 0 {
			continue
		}
		delta, present, err := p.positionVariation(subtable, at)
		if err != nil {
			return otValue{}, err
		}
		at += 2
		if !present {
			continue
		}
		if v.variation == nil {
			if err := p.b.take(1, int64(unsafe.Sizeof(InstancePosition{}))); err != nil {
				return otValue{}, err
			}
			v.variation = &InstancePosition{}
		}
		positions := []*float64{&v.variation.XOffset, &v.variation.YOffset,
			&v.variation.XAdvance, &v.variation.YAdvance}
		*positions[field] = delta
	}
	return v, nil
}
func (p *otParser) classDef(at int, count int) ([]uint16, error) {
	if err := p.need(at, 4); err != nil {
		return nil, err
	}
	if err := p.b.take(int(p.numGlyphs), int64(p.numGlyphs)*2); err != nil {
		return nil, err
	}
	classes := make([]uint16, int(p.numGlyphs))
	format := shapeU16(p.data[at:])
	switch format {
	case 1:
		if err := p.need(at, 6); err != nil {
			return nil, err
		}
		start, n := int(shapeU16(p.data[at+2:])), int(shapeU16(p.data[at+4:]))
		if start > len(classes) || n > len(classes)-start {
			return nil, p.bad(at, "class glyph range outside face")
		}
		if err := p.array(at+6, n, 2); err != nil {
			return nil, err
		}
		for i := range n {
			c := shapeU16(p.data[at+6+2*i:])
			if int(c) >= count {
				return nil, p.bad(at, "class index outside matrix")
			}
			classes[start+i] = c
		}
	case 2:
		n := int(shapeU16(p.data[at+2:]))
		if err := p.array(at+4, n, 6); err != nil {
			return nil, err
		}
		last := -1
		for i := range n {
			pos := at + 4 + 6*i
			start, end, c := int(shapeU16(p.data[pos:])), int(shapeU16(p.data[pos+2:])), shapeU16(p.data[pos+4:])
			if start > end || start <= last || end >= len(classes) || int(c) >= count {
				return nil, p.bad(pos, "invalid class range")
			}
			for id := start; id <= end; id++ {
				classes[id] = c
			}
			last = end
		}
	default:
		return nil, p.bad(at, "invalid class definition format")
	}
	return classes, nil
}
func (p *otParser) pairSub(at int) (otSub, error) {
	if err := p.need(at, 10); err != nil {
		return otSub{}, err
	}
	format, vf1, vf2 := shapeU16(p.data[at:]), shapeU16(p.data[at+4:]), shapeU16(p.data[at+6:])
	if (vf1|vf2)&0xff00 != 0 {
		return otSub{}, p.bad(at+4, "reserved value format bits set")
	}
	covAt, err := p.off(at, at+2, 4)
	if err != nil {
		return otSub{}, err
	}
	cov, err := p.coverage(covAt)
	if err != nil {
		return otSub{}, err
	}
	s := otSub{coverage: cov, valueFormat2: vf2}
	size := valueSize(vf1) + valueSize(vf2)
	switch format {
	case 1:
		n := int(shapeU16(p.data[at+8:]))
		if n != len(cov) {
			return otSub{}, p.bad(at, "pair set count differs from coverage")
		}
		if err := p.array(at+10, n, 2); err != nil {
			return otSub{}, err
		}
		if err := p.b.take(n, int64(n)*int64(unsafe.Sizeof([]otPair{}))); err != nil {
			return otSub{}, err
		}
		s.pairSets = make([][]otPair, n)
		for i := range n {
			set, err := p.off(at, at+10+2*i, 2)
			if err != nil {
				return otSub{}, err
			}
			count := int(shapeU16(p.data[set:]))
			if err := p.array(set+2, count, 2+size); err != nil {
				return otSub{}, err
			}
			if err := p.b.take(count, int64(count)*int64(unsafe.Sizeof(otPair{}))); err != nil {
				return otSub{}, err
			}
			s.pairSets[i] = make([]otPair, count)
			for j := range count {
				pos := set + 2 + j*(2+size)
				id := shapeU16(p.data[pos:])
				if id >= p.numGlyphs || j > 0 && id <= s.pairSets[i][j-1].second {
					return otSub{}, p.bad(pos, "pair glyph records unordered or outside face")
				}
				v1, err := p.value(pos+2, vf1, set)
				if err != nil {
					return otSub{}, err
				}
				v2, err := p.value(pos+2+valueSize(vf1), vf2, set)
				if err != nil {
					return otSub{}, err
				}
				s.pairSets[i][j] = otPair{second: id, firstValue: v1, secondValue: v2}
			}
		}
	case 2:
		if err := p.need(at, 16); err != nil {
			return otSub{}, err
		}
		n1, n2 := int(shapeU16(p.data[at+12:])), int(shapeU16(p.data[at+14:]))
		if n1 == 0 || n2 == 0 {
			return otSub{}, p.bad(at, "empty pair class matrix")
		}
		if n1 > p.b.maxNodes/n2 {
			return otSub{}, fontError(ErrLimit, "shape.pair", int64(at), "class matrix exceeds node limit")
		}
		count := n1 * n2
		if size != 0 {
			if err := p.array(at+16, count, size); err != nil {
				return otSub{}, err
			}
			if err := p.b.take(count, int64(count)*int64(unsafe.Sizeof(otPair{}))); err != nil {
				return otSub{}, err
			}
		} else if err := p.b.take(count, 0); err != nil {
			return otSub{}, err
		}
		c1At, err := p.off(at, at+8, 4)
		if err != nil {
			return otSub{}, err
		}
		c2At, err := p.off(at, at+10, 4)
		if err != nil {
			return otSub{}, err
		}
		s.class1, err = p.classDef(c1At, n1)
		if err != nil {
			return otSub{}, err
		}
		s.class2, err = p.classDef(c2At, n2)
		if err != nil {
			return otSub{}, err
		}
		s.classCount1, s.classCount2 = n1, n2
		if size == 0 {
			// A zero-value matrix still matches Coverage and consumes the first subtable.
			// Its logical records are bounded without allocating zeros.
			return s, nil
		}
		s.matrix = make([]otPair, count)
		for i := range s.matrix {
			pos := at + 16 + i*size
			v1, err := p.value(pos, vf1, at)
			if err != nil {
				return otSub{}, err
			}
			v2, err := p.value(pos+valueSize(vf1), vf2, at)
			if err != nil {
				return otSub{}, err
			}
			s.matrix[i] = otPair{firstValue: v1, secondValue: v2}
		}
	default:
		return otSub{}, p.bad(at, "invalid pair positioning format")
	}
	return s, nil
}
func (s *otSub) pair(r *ShapedRun, i int) (bool, int, error) {
	if i+1 >= len(r.Glyphs) {
		return false, 1, nil
	}
	ci := s.covered(r.Glyphs[i].GlyphID)
	if ci < 0 {
		return false, 1, nil
	}
	var pair otPair
	if s.pairSets != nil {
		pairs := s.pairSets[ci]
		id := r.Glyphs[i+1].GlyphID
		j := sort.Search(len(pairs), func(j int) bool { return pairs[j].second >= id })
		if j == len(pairs) || pairs[j].second != id {
			return false, 1, nil
		}
		pair = pairs[j]
	} else {
		c1, c2 := int(s.class1[r.Glyphs[i].GlyphID]), int(s.class2[r.Glyphs[i+1].GlyphID])
		if len(s.matrix) != 0 {
			pair = s.matrix[c1*s.classCount2+c2]
		}
	}
	if err := applyOTValue(&r.Glyphs[i], pair.firstValue); err != nil {
		return false, 1, err
	}
	if err := applyOTValue(&r.Glyphs[i+1], pair.secondValue); err != nil {
		return false, 1, err
	}
	step := 1
	if s.valueFormat2 != 0 {
		step = 2
	}
	return true, step, nil
}
func applyOTValue(g *ShapedGlyph, v otValue) error {
	if g.InstancePosition != nil {
		g.InstancePosition.XOffset += float64(v.x)
		g.InstancePosition.YOffset += float64(v.y)
		g.InstancePosition.XAdvance += float64(v.xa)
		if v.variation != nil {
			g.InstancePosition.XOffset += v.variation.XOffset
			g.InstancePosition.YOffset += v.variation.YOffset
			g.InstancePosition.XAdvance += v.variation.XAdvance
		}
		return nil
	}
	if v.variation != nil {
		return fontError(ErrUnsupported, "shape.position.variation", g.ClusterStart,
			"variation positioning requires an explicitly selected variable instance")
	}
	for _, f := range []struct {
		p *int64
		n int64
	}{{&g.XOffset, v.x}, {&g.YOffset, v.y}, {&g.XAdvance, v.xa}} {
		if f.n > 0 && *f.p > math.MaxInt64-f.n || f.n < 0 && *f.p < math.MinInt64-f.n {
			return fontError(ErrLimit, "shape.position", g.ClusterStart, "design-unit position overflow")
		}
		*f.p += f.n
	}
	return nil
}

func shapeU16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
func shapeU32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func shapeI16(b []byte) int16  { return int16(shapeU16(b)) }
