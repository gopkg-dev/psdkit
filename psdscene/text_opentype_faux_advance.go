package psdscene

import (
	"context"
	"math"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

// positionOpenTypePointLineWithAdvance keeps source tracking separate from synthetic glyph advances.
// Every shaped glyph, including source spaces and the last glyph, receives syntheticAdvance.
// The terminal engine CR is metadata and never appears in the supplied shaped run.
func positionOpenTypePointLineWithAdvance(
	ctx context.Context, run *psdfont.Run, line openTypePointLine, tracking, emSize, syntheticAdvance float64,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if run == nil || !textFinite(syntheticAdvance) || syntheticAdvance < 0 {
		return sceneError(psdfile.ErrMalformed, "textOpenType.syntheticAdvance", line.start,
			"a run and finite nonnegative synthetic glyph advance are required")
	}
	step := tracking * emSize / 1000
	width := run.AdvanceWidth + float64(max(0, len(run.Glyphs)-1))*step +
		float64(len(run.Glyphs))*syntheticAdvance
	if !textFinite(step) || !textFinite(width) || math.Abs(width) > 1<<40 {
		return sceneError(psdfile.ErrLimit, "textOpenType.syntheticAdvance", line.start,
			"tracked synthetic paragraph advance exceeds supported coordinate precision")
	}
	origin := float64(0)
	if line.justification == 2 {
		origin = -width / 2
	}
	// Check the entire owned run before mutation so cancellation and invalid positions leave it intact.
	for index, glyph := range run.Glyphs {
		if err := ctx.Err(); err != nil {
			return err
		}
		x := glyph.X + origin + float64(index)*(step+syntheticAdvance)
		advance := glyph.Advance + syntheticAdvance
		if index+1 < len(run.Glyphs) {
			advance += step
		}
		if !textFinite(x) || math.Abs(x) > 1<<40 || !textFinite(advance) || math.Abs(advance) > 1<<40 {
			return sceneError(psdfile.ErrLimit, "textOpenType.syntheticAdvance", glyph.ClusterStart,
				"synthetic glyph position or advance exceeds supported coordinate precision")
		}
		if line.start > 0 && glyph.ClusterStart > math.MaxInt64-line.start ||
			line.start < 0 && glyph.ClusterStart < math.MinInt64-line.start {
			return sceneError(psdfile.ErrLimit, "textOpenType.syntheticAdvance", glyph.ClusterStart,
				"source cluster index exceeds supported integer precision")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for index := range run.Glyphs {
		glyph := &run.Glyphs[index]
		glyph.X += origin + float64(index)*(step+syntheticAdvance)
		glyph.ClusterStart += line.start
		glyph.Advance += syntheticAdvance
		if index+1 < len(run.Glyphs) {
			glyph.Advance += step
		}
	}
	run.AdvanceWidth = width
	return nil
}
