package psdfont

import (
	"context"
	"unsafe"
)

func (r *cff2VM) applyInstanceMetrics(id uint16) error {
	f := r.sink.face
	if f.variation == nil || len(f.variation.axes) == 0 {
		return nil
	}
	if err := r.sink.reserve(int64(unsafe.Sizeof(InstanceGlyphMetrics{}))); err != nil {
		return err
	}
	bounds, hasInk, err := cffGlyphPreciseBounds(r.sink.ctx, r.sink.glyph)
	if err != nil {
		return err
	}
	step := func(_ string, offset int64) error { return r.sink.step(offset) }
	advance, err := f.instanceAdvance(id, step)
	if err != nil {
		return err
	}
	glyph := r.sink.glyph
	metrics := &InstanceGlyphMetrics{RawAdvanceWidth: glyph.AdvanceWidth, RawLeftSideBearing: glyph.LeftSideBearing,
		AdvanceWidth: advance, OutlineBounds: bounds, HasInk: hasInk}
	if hasInk {
		metrics.LeftSideBearing = bounds[0]
	}
	if f.hvar != nil && len(f.hvar.left) != 0 {
		index := mappedDeltaIndex(f.hvar.left, id)
		delta, err := f.hvar.store.delta(index.outer, index.inner, step)
		if err != nil {
			return err
		}
		metrics.LeftSideBearing = float64(glyph.LeftSideBearing) + delta
	}
	width := bounds[2] - bounds[0]
	metrics.RightSideBearing = advance - metrics.LeftSideBearing - width
	if f.hvar != nil && len(f.hvar.right) != 0 {
		defaultWidth, err := r.rawBaselineOutlineWidth()
		if err != nil {
			return err
		}
		index := mappedDeltaIndex(f.hvar.right, id)
		delta, err := f.hvar.store.delta(index.outer, index.inner, step)
		if err != nil {
			return err
		}
		metrics.RightSideBearing = float64(glyph.AdvanceWidth) - float64(glyph.LeftSideBearing) - defaultWidth + delta
	}
	if !openTypeCoordinate(metrics.AdvanceWidth) || !openTypeCoordinate(metrics.LeftSideBearing) ||
		!openTypeCoordinate(metrics.RightSideBearing) {
		return r.fail(ErrLimit, glyph.Source.Offset, "instance metrics exceed supported precision")
	}
	if err := r.sink.ctx.Err(); err != nil {
		return err
	}
	glyph.InstanceMetrics = metrics
	return nil
}

// rawBaselineOutlineWidth decodes encoded defaults, not a normalized-zero instance.
// A region with only inactive axes contributes even at normalized zero; all blend scalars must be zero here.
func (r *cff2VM) rawBaselineOutlineWidth() (float64, error) {
	bytes := int64(unsafe.Sizeof(cff2VM{})) + int64(unsafe.Sizeof(cff2Font{})) +
		int64(unsafe.Sizeof(itemVariationStore{})) + int64(unsafe.Sizeof(Glyph{}))
	if r.font.store != nil {
		bytes += int64(r.font.store.regionCount) * 8
	}
	if err := r.sink.reserve(bytes); err != nil {
		return 0, err
	}
	font := *r.font
	if font.store != nil {
		store := *font.store
		store.scalars = make([]float64, store.regionCount)
		font.store = &store
	}
	defaultGlyph := &Glyph{Source: r.sink.glyph.Source}
	defaultLimits := r.sink.limits
	defaultLimits.MaxContours -= len(r.sink.glyph.Contours)
	reader := cff2VM{font: &font, private: r.private, vsindex: r.private.vsindex,
		sink: type2Reader{face: r.sink.face, ctx: r.sink.ctx, limits: defaultLimits, glyph: defaultGlyph,
			private: cffPrivate{matrix: cffIdentityMatrix}, widthSeen: true,
			allocated: r.sink.allocated, operations: r.sink.operations, points: r.sink.points, visits: r.sink.visits}}
	if err := reader.execute(defaultGlyph.Source, 0); err != nil {
		return 0, err
	}
	if reader.count != 0 {
		return 0, reader.fail(ErrMalformed, defaultGlyph.Source.Offset, "unused operands at root CFF2 EOF")
	}
	if err := reader.sink.close(); err != nil {
		return 0, err
	}
	r.sink.allocated, r.sink.operations = reader.sink.allocated, reader.sink.operations
	r.sink.points, r.sink.visits = reader.sink.points, reader.sink.visits
	bounds, hasInk, err := cffGlyphPreciseBounds(r.sink.ctx, defaultGlyph)
	if err != nil {
		return 0, err
	}
	if !hasInk {
		return 0, nil
	}
	return bounds[2] - bounds[0], nil
}

// InstanceMetrics returns an owned precise CFF2 instance record using the same bounded outline decode.
// Static fonts return nil. Raw hmtx values remain available on Glyph without integer clamping of instance values.
func (f *Face) InstanceMetrics(ctx context.Context, id uint16, options ...GlyphOptions) (*InstanceGlyphMetrics, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fontError(ErrMalformed, "instance.metrics", 0, "missing font face")
	}
	if f.cff2 == nil || f.variation == nil || len(f.variation.axes) == 0 {
		return nil, nil
	}
	glyph, err := f.Glyph(ctx, id, options...)
	if err != nil {
		return nil, err
	}
	return glyph.InstanceMetrics, nil
}
