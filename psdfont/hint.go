package psdfont

import (
	"context"
	"image"
	"math"
	"unsafe"
)

// CFFHintMode selects an explicit bounded CFF1 outline fitting policy.
type CFFHintMode uint8

const (
	// CFFHintNone converts the supplied affine without fitting design coordinates.
	CFFHintNone CFFHintMode = iota
	// CFFVertical fits active horizontal stems and source blue zones in device Y only.
	// Design X coordinates and advances remain fractional and are not grid fitted.
	CFFVertical
	// CFFFull additionally fits active vertical stems in device X.
	CFFFull
	// CFFFractional aligns active stem midpoints in both axes while preserving source widths.
	// It does not replace widths with standard/snap widths or enforce a one-pixel minimum.
	CFFFractional
	// CFFEdgeFractional moves each active stem by the smallest shift placing either edge on the lattice.
	// Both axes preserve exact source widths; source blue-zone captures remain locked.
	CFFEdgeFractional
)

// CFFHintAnchor selects the coordinate origin of the fitting lattice.
type CFFHintAnchor uint8

const (
	// CFFHintWorld anchors the lattice to absolute document coordinates.
	CFFHintWorld CFFHintAnchor = iota
	// CFFHintBaseline anchors the lattice to each glyph's original fractional device baseline.
	CFFHintBaseline
)

// CFFHintGrid selects integer pixel edges or half-integer pixel centers.
type CFFHintGrid uint8

const (
	// CFFHintEdges fits stem positions to integer coordinates relative to Anchor.
	CFFHintEdges CFFHintGrid = iota
	// CFFHintCenters fits stem positions to half-integer coordinates relative to Anchor.
	CFFHintCenters
)

// CFFBluePolicy selects the explicit CFF1 source alignment-zone treatment.
type CFFBluePolicy uint8

const (
	// CFFBlueGrid fits captured edges to the configured device lattice.
	CFFBlueGrid CFFBluePolicy = iota
	// CFFBlueResolvedOutward rounds resolvable blue-zone flats in their outward direction in device Y.
	// A source zone is resolvable when its original height occupies at least half a device pixel.
	// Top flats use ceil and bottom flats use floor before adding the source-to-device offset.
	// This bounded approximation retains source BlueScale suppression and relative BlueShift overshoots.
	// It supports only fractional fitting with the world/edges lattice.
	CFFBlueResolvedOutward
)

// HintOptions bounds independent device outlines and the work used to fit CFF1 source hints.
// Zero limits select 64 MiB and one million operations. Mode zero performs no fitting.
// This policy does not execute TrueType bytecode, CFF2 hints, counter equalization or stem darkening.
type HintOptions struct {
	Mode       CFFHintMode
	Anchor     CFFHintAnchor
	Grid       CFFHintGrid
	BluePolicy CFFBluePolicy
	// SnapBaselineY rounds image-down device baselines with floor(y+.5) before source outline conversion.
	// Source FontMatrix translations remain fractional and are applied after the baseline rounding.
	// This bounded approximation supports only fractional fitting with the world/edges lattice.
	SnapBaselineY bool
	// SnapBaselineXQuarter rounds each device X baseline with floor(x*4+.5)/4 before fitting.
	// Source FontMatrix translations remain fractional and are applied after baseline rounding.
	// This explicit approximation supports only fractional fitting with the world/edges lattice.
	SnapBaselineXQuarter bool
	MaxWorkingBytes      int64
	MaxOperations        int
}

type hintWork struct {
	ctx        context.Context
	operations int
	limit      int
}

func (work *hintWork) step() error {
	if err := work.ctx.Err(); err != nil {
		return err
	}
	if work.operations >= work.limit {
		return fontError(ErrLimit, "hint.work", 0, "outline fitting exceeds MaxOperations")
	}
	work.operations++
	return nil
}

// HintRun returns independent device paths with Scale=1 and positioned glyph origins at zero.
// Device paths use upward-positive Y, matching RasterizeRun's outline convention.
// Input paths, original hmtx metrics, cluster advances and fractional baselines are never modified.
// Fitting requires opt-in CFF1 metadata and positive axis-aligned source and caller matrices.
// Active mask ranges are respected; counter masks retain their source identity but do not change active hints.
// Ordinary active stems must not overlap or produce a reversed piecewise map.
// Fractional policies reject paired blue captures requiring a change to the original stem width.
func HintRun(
	ctx context.Context, run *Run, transform [6]float64, options ...HintOptions,
) (*Run, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration := HintOptions{MaxWorkingBytes: 64 << 20, MaxOperations: 1_000_000}
	if len(options) > 1 {
		return nil, fontError(ErrMalformed, "hint.options", 0, "provide at most one options value")
	}
	if len(options) == 1 {
		configuration = options[0]
		if configuration.MaxWorkingBytes < 0 || configuration.MaxOperations < 0 {
			return nil, fontError(ErrLimit, "hint.options", 0, "limits must not be negative")
		}
		if configuration.MaxWorkingBytes == 0 {
			configuration.MaxWorkingBytes = 64 << 20
		}
		if configuration.MaxOperations == 0 {
			configuration.MaxOperations = 1_000_000
		}
	}
	if configuration.Mode > CFFEdgeFractional {
		return nil, fontError(ErrUnsupported, "hint.mode", 0, "unknown CFF1 outline fitting policy")
	}
	if configuration.Anchor > CFFHintBaseline || configuration.Grid > CFFHintCenters {
		return nil, fontError(ErrUnsupported, "hint.lattice", 0, "unknown CFF1 fitting lattice")
	}
	if configuration.BluePolicy > CFFBlueResolvedOutward {
		return nil, fontError(ErrUnsupported, "hint.bluePolicy", 0, "unknown CFF1 blue-zone policy")
	}
	if configuration.BluePolicy != CFFBlueGrid || configuration.SnapBaselineY || configuration.SnapBaselineXQuarter {
		if configuration.Mode != CFFFractional && configuration.Mode != CFFEdgeFractional ||
			configuration.Anchor != CFFHintWorld || configuration.Grid != CFFHintEdges {
			return nil, fontError(ErrUnsupported, "hint.bluePolicy", 0,
				"resolved zones and baseline rounding require fractional fitting on world pixel edges")
		}
	}
	if run == nil || !finite(run.Scale) || run.Scale <= 0 {
		return nil, fontError(ErrMalformed, "hint.run", 0, "missing run or invalid scale")
	}
	for _, value := range transform {
		if !finite(value) || math.Abs(value) > 1<<40 {
			return nil, fontError(ErrMalformed, "hint.affine", 0, "nonfinite or excessive source affine")
		}
	}
	if configuration.Mode != CFFHintNone &&
		(transform[0] <= 0 || transform[3] <= 0 || transform[1] != 0 || transform[2] != 0) {
		return nil, fontError(ErrUnsupported, "hint.affine", 0, "fitting requires a positive axis-aligned affine")
	}
	metadataBytes := int64(unsafe.Sizeof(PositionedGlyph{}) + unsafe.Sizeof(Glyph{}))
	reserved := int64(1024) + int64(unsafe.Sizeof(hintGlyphMaps{}))
	if configuration.MaxWorkingBytes < reserved ||
		int64(len(run.Glyphs)) > (configuration.MaxWorkingBytes-reserved)/metadataBytes {
		return nil, fontError(ErrLimit, "hint.memory", 0, "device glyph metadata exceeds MaxWorkingBytes")
	}
	reserved += int64(len(run.Glyphs)) * metadataBytes
	result := &Run{FontName: run.FontName, Scale: 1, Leading: run.Leading,
		AdvanceWidth: run.AdvanceWidth, UTF16Units: run.UTF16Units, Approximate: true,
		Glyphs: make([]PositionedGlyph, len(run.Glyphs))}
	work := hintWork{ctx: ctx, limit: configuration.MaxOperations}
	ink := [4]float64{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	coordinateLimit := min(float64(1<<40), float64(int(^uint(0)>>1)-1))
	for glyphIndex, positioned := range run.Glyphs {
		if err := work.step(); err != nil {
			return nil, err
		}
		result.Glyphs[glyphIndex] = positioned
		if positioned.LineBreak {
			continue
		}
		if positioned.Glyph == nil || !finite(positioned.X) || !finite(positioned.Y) {
			return nil, fontError(ErrMalformed, "hint.glyph", positioned.ClusterStart, "missing glyph or baseline")
		}
		source := positioned.Glyph
		commandBytes := int64(unsafe.Sizeof(Command{}))
		if int64(len(source.Path)) > (configuration.MaxWorkingBytes-reserved)/commandBytes {
			return nil, fontError(ErrLimit, "hint.memory", positioned.ClusterStart,
				"owned device commands exceed MaxWorkingBytes")
		}
		reserved += int64(len(source.Path)) * commandBytes
		glyph := &Glyph{ID: source.ID, AdvanceWidth: source.AdvanceWidth, LeftSideBearing: source.LeftSideBearing,
			CharstringWidth: source.CharstringWidth, Source: source.Source, Bounds: source.Bounds,
			Path: make([]Command, len(source.Path))}
		maps := hintGlyphMaps{}
		if configuration.Mode != CFFHintNone {
			if err := maps.initialize(&work, source.CFFHints, transform, run.Scale, positioned, configuration); err != nil {
				return nil, err
			}
		}
		baselineX := transform[0]*positioned.X + transform[2]*positioned.Y + transform[4]
		baselineY := -(transform[1]*positioned.X + transform[3]*positioned.Y + transform[5])
		if configuration.SnapBaselineXQuarter {
			baselineX = hintRoundQuarter(baselineX)
		}
		if configuration.SnapBaselineY {
			baselineY = -hintRound(-baselineY)
		}
		open := false
		for commandIndex, command := range source.Path {
			if err := work.step(); err != nil {
				return nil, err
			}
			if configuration.Mode != CFFHintNone {
				if err := maps.command(&work, source.CFFHints, commandIndex, configuration.Mode); err != nil {
					return nil, err
				}
			}
			convert := func(px, py float64) (float64, float64, error) {
				if !finite(px) || !finite(py) {
					return 0, 0, fontError(ErrMalformed, "hint.path", 0, "nonfinite path point")
				}
				dx := baselineX + run.Scale*(transform[0]*px-transform[2]*py)
				dy := baselineY + run.Scale*(-transform[1]*px+transform[3]*py)
				var err error
				if configuration.Mode != CFFHintNone {
					dy, err = maps.y.mapPoint(&work, dy)
					if err != nil {
						return 0, 0, err
					}
				}
				if configuration.Mode == CFFFull || configuration.Mode == CFFFractional ||
					configuration.Mode == CFFEdgeFractional {
					dx, err = maps.x.mapPoint(&work, dx)
					if err != nil {
						return 0, 0, err
					}
				}
				if !finite(dx) || !finite(dy) || math.Abs(dx) > coordinateLimit || math.Abs(dy) > coordinateLimit {
					return 0, 0, fontError(ErrLimit, "hint.path", 0, "device point exceeds supported precision")
				}
				ink[0], ink[1] = min(ink[0], dx), min(ink[1], -dy)
				ink[2], ink[3] = max(ink[2], dx), max(ink[3], -dy)
				return dx, dy, nil
			}
			glyph.Path[commandIndex] = command
			switch command.Op {
			case Move:
				if open {
					return nil, fontError(ErrMalformed, "hint.path", 0, "contour is not closed before Move")
				}
				open = true
			case Line, Quad, Cubic:
				if !open {
					return nil, fontError(ErrMalformed, "hint.path", 0, "segment precedes Move")
				}
			case Close:
				if !open {
					return nil, fontError(ErrMalformed, "hint.path", 0, "Close precedes Move")
				}
				open = false
				continue
			default:
				return nil, fontError(ErrUnsupported, "hint.path", 0, "unknown path command")
			}
			var err error
			glyph.Path[commandIndex].X, glyph.Path[commandIndex].Y, err = convert(command.X, command.Y)
			if err != nil {
				return nil, err
			}
			if command.Op == Quad || command.Op == Cubic {
				glyph.Path[commandIndex].ControlX, glyph.Path[commandIndex].ControlY, err =
					convert(command.ControlX, command.ControlY)
				if err != nil {
					return nil, err
				}
			}
			if command.Op == Cubic {
				glyph.Path[commandIndex].Control2X, glyph.Path[commandIndex].Control2Y, err =
					convert(command.Control2X, command.Control2Y)
				if err != nil {
					return nil, err
				}
			}
		}
		if open {
			return nil, fontError(ErrMalformed, "hint.path", 0, "final contour is not closed")
		}
		glyph.reservation = int64(unsafe.Sizeof(Glyph{})) + int64(len(source.Path))*commandBytes
		result.Glyphs[glyphIndex].X, result.Glyphs[glyphIndex].Y, result.Glyphs[glyphIndex].Glyph = 0, 0, glyph
	}
	if !math.IsInf(ink[0], 1) {
		result.Bounds = image.Rect(int(math.Floor(ink[0])), int(math.Floor(ink[1])),
			int(math.Ceil(ink[2])), int(math.Ceil(ink[3])))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result.reservation = reserved
	return result, nil
}
