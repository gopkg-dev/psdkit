package psdfont

import (
	"context"
	"errors"
	"image"
	"math"
	"unsafe"

	"github.com/gopkg-dev/psdkit/psdvector"
)

// RasterOptions bounds approximate unhinted glyph coverage independently from layout.
// Zero selects 64 MiB, an 8 by 8 sample grid and the vector rasterizer's bounded defaults.
type RasterOptions struct {
	MaxWorkingBytes int64
	Samples         int
	MaxSegments     int
	MaxScanlineWork int64
	// AreaCoverage selects horizontal span integration with 64 fixed vertical midpoint samples.
	// The explicit unhinted model does not reproduce Photoshop antialias modes.
	AreaCoverage bool
	// AreaSamples selects 4, 8, 16 or 64 vertical midpoint strata; zero selects 64.
	// An explicit value requires AreaCoverage.
	AreaSamples int
	// CoverageLevels16 quantizes the completed union to sixteen uniformly spaced alpha levels.
	// It is an explicit coverage model, not a reproduction of Photoshop's private Sharp rasterizer.
	CoverageLevels16 bool
	// NoAntialias selects unhinted pixel-center membership and emits only 0 or 255.
	// It cannot be combined with AreaCoverage.
	NoAntialias bool
	// FauxBoldRadius selects an explicit approximate outline union in output pixels.
	// Zero preserves unmodified outlines. Positive values union the original outline with eight
	// translated copies on a circle of this radius before computing coverage once.
	// This does not change advances, replace the supplied font, execute hinting or reproduce native faux bold.
	FauxBoldRadius float64
	// FauxBoldAnchored centers the eight faux-bold translates at (+radius,+radius) in output coordinates.
	// The original outline is retained; the positive model extends at most twice the radius right and down.
	// It affects only a positive FauxBoldRadius and does not change layout advances.
	FauxBoldAnchored bool
}

// RasterizeRun returns independent grayscale glyph coverage in the supplied pixel rectangle.
// Quadratic outlines are converted exactly to cubic controls; cubic outlines retain both controls.
// It does not apply TrueType hinting, Photoshop antialias modes, text warps or color.
func RasterizeRun(
	ctx context.Context, run *Run, bounds image.Rectangle, options ...RasterOptions,
) (*image.Alpha, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration := RasterOptions{MaxWorkingBytes: 64 << 20}
	if len(options) > 1 {
		return nil, fontError(ErrMalformed, "raster.options", 0, "provide at most one options value")
	}
	if len(options) == 1 {
		configuration = options[0]
		if configuration.MaxWorkingBytes < 0 || configuration.MaxSegments < 0 || configuration.MaxScanlineWork < 0 {
			return nil, fontError(ErrLimit, "raster.options", 0, "limits must not be negative")
		}
		if configuration.MaxWorkingBytes == 0 {
			configuration.MaxWorkingBytes = 64 << 20
		}
	}
	if run == nil || !finite(run.Scale) || run.Scale <= 0 || bounds.Max.X < bounds.Min.X || bounds.Max.Y < bounds.Min.Y {
		return nil, fontError(ErrMalformed, "raster.run", 0, "missing run, invalid scale or reversed rectangle")
	}
	if !finite(configuration.FauxBoldRadius) || configuration.FauxBoldRadius < 0 {
		return nil, fontError(ErrMalformed, "raster.fauxBold", 0, "radius must be finite and nonnegative")
	}
	if configuration.FauxBoldRadius > 64 {
		return nil, fontError(ErrLimit, "raster.fauxBold", 0, "radius exceeds the bounded outline model")
	}
	if configuration.NoAntialias && (configuration.AreaCoverage || configuration.CoverageLevels16) {
		return nil, fontError(ErrMalformed, "raster.coverage", 0,
			"area coverage and pixel-center membership cannot be selected together")
	}
	if configuration.AreaSamples != 0 && (!configuration.AreaCoverage ||
		configuration.AreaSamples != 4 && configuration.AreaSamples != 8 &&
			configuration.AreaSamples != 16 && configuration.AreaSamples != 64) {
		return nil, fontError(ErrMalformed, "raster.coverage", 0,
			"explicit area strata require area coverage and 4, 8, 16 or 64 samples")
	}
	if configuration.Samples != 0 && configuration.Samples != 4 && configuration.Samples != 8 {
		return nil, fontError(ErrUnsupported, "raster.samples", 0, "coverage supports only four or eight samples per axis")
	}
	if math.Abs(float64(bounds.Min.X)) > 1<<40 || math.Abs(float64(bounds.Min.Y)) > 1<<40 ||
		math.Abs(float64(bounds.Max.X)) > 1<<40 || math.Abs(float64(bounds.Max.Y)) > 1<<40 {
		return nil, fontError(ErrLimit, "raster.bounds", 0, "rectangle exceeds supported subpixel precision")
	}
	if configuration.MaxWorkingBytes < 128 || bounds.Dx() < 0 || bounds.Dy() < 0 {
		return nil, fontError(ErrLimit, "raster.bounds", 0, "owned output or rectangle exceeds platform budget")
	}
	if bounds.Empty() {
		return image.NewAlpha(bounds), nil
	}
	mask := &psdvector.Mask{Width: 1, Height: 1, HasFillRule: true, FillRule: psdvector.NonZero}
	outlineCopies := int64(1)
	if configuration.FauxBoldRadius > 0 {
		outlineCopies = 9
	}
	reserved := int64(1024)
	for _, positioned := range run.Glyphs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if positioned.LineBreak {
			continue
		}
		if positioned.Glyph == nil || !finite(positioned.X) || !finite(positioned.Y) {
			return nil, fontError(ErrMalformed, "raster.glyph", positioned.ClusterStart, "nil glyph or nonfinite baseline")
		}
		glyph := positioned.Glyph
		contours := 0
		for _, command := range glyph.Path {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if command.Op == Move {
				contours++
			}
		}
		knotBytes := int64(unsafe.Sizeof(psdvector.Knot{})) * 3 * outlineCopies
		pathBytes := int64(unsafe.Sizeof(psdvector.Subpath{})) * 6 * outlineCopies
		remaining := configuration.MaxWorkingBytes - reserved
		if int64(len(glyph.Path)) > remaining/knotBytes || int64(contours) > remaining/pathBytes {
			return nil, fontError(ErrLimit, "raster.paths", positioned.ClusterStart,
				"cubic path growth exceeds MaxWorkingBytes")
		}
		reservation := int64(len(glyph.Path))*knotBytes + int64(contours)*pathBytes
		if reservation > configuration.MaxWorkingBytes-reserved {
			return nil, fontError(ErrLimit, "raster.paths", positioned.ClusterStart,
				"owned cubic paths exceed MaxWorkingBytes")
		}
		reserved += reservation
		paths, err := glyphPaths(ctx, glyph.Path, positioned.X, positioned.Y, run.Scale)
		if err != nil {
			return nil, err
		}
		if configuration.FauxBoldRadius == 0 {
			mask.Subpaths = append(mask.Subpaths, paths...)
			continue
		}
		paths, err = fauxBoldPathsWithAnchor(ctx, paths, configuration.FauxBoldRadius, configuration.FauxBoldAnchored)
		if err != nil {
			return nil, err
		}
		mask.Subpaths = append(mask.Subpaths, paths...)
	}
	remaining := configuration.MaxWorkingBytes - reserved
	if remaining <= 0 {
		return nil, fontError(ErrLimit, "raster.paths", 0, "raster work area exceeds MaxWorkingBytes")
	}
	result, err := psdvector.Rasterize(ctx, mask, bounds, psdvector.Options{MaxWorkingBytes: remaining,
		Samples: configuration.Samples, MaxSegments: configuration.MaxSegments,
		MaxScanlineWork: configuration.MaxScanlineWork, AreaCoverage: configuration.AreaCoverage,
		NoAntialias: configuration.NoAntialias, AreaSamples: configuration.AreaSamples,
		CoverageLevels16: configuration.CoverageLevels16})
	if err != nil {
		kind := ErrMalformed
		if errors.Is(err, psdvector.ErrLimit) {
			kind = ErrLimit
		} else if errors.Is(err, psdvector.ErrUnsupported) {
			kind = ErrUnsupported
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fontError(kind, "raster.coverage", 0, "bounded vector coverage could not be produced")
	}
	return result, nil
}

func glyphPaths(ctx context.Context, commands []Command, x, y, scale float64) ([]psdvector.Subpath, error) {
	var result []psdvector.Subpath
	var path *psdvector.Subpath
	point := func(px, py float64) psdvector.Point { return psdvector.Point{X: x + px*scale, Y: y - py*scale} }
	for commandIndex, command := range commands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !finite(command.X) || !finite(command.Y) || !finite(command.ControlX) || !finite(command.ControlY) ||
			!finite(command.Control2X) || !finite(command.Control2Y) {
			return nil, fontError(ErrMalformed, "raster.path", 0, "path has nonfinite geometry")
		}
		target := point(command.X, command.Y)
		switch command.Op {
		case Move:
			if path != nil {
				return nil, fontError(ErrMalformed, "raster.path", 0, "previous contour is not closed")
			}
			operation := int16(-1)
			if len(result) == 0 {
				operation = 1
			}
			result = append(result, psdvector.Subpath{Closed: true, Operation: operation})
			path = &result[len(result)-1]
			path.Knots = append(path.Knots, psdvector.Knot{Anchor: target, Preceding: target, Leaving: target})
		case Line, Quad, Cubic:
			if path == nil {
				return nil, fontError(ErrMalformed, "raster.path", 0, "segment precedes its contour start")
			}
			previous := &path.Knots[len(path.Knots)-1]
			incoming := target
			switch command.Op {
			case Quad:
				control := point(command.ControlX, command.ControlY)
				previous.Leaving = psdvector.Point{X: previous.Anchor.X + (control.X-previous.Anchor.X)*2/3,
					Y: previous.Anchor.Y + (control.Y-previous.Anchor.Y)*2/3}
				incoming = psdvector.Point{X: target.X + (control.X-target.X)*2/3, Y: target.Y + (control.Y-target.Y)*2/3}
			case Cubic:
				previous.Leaving = point(command.ControlX, command.ControlY)
				incoming = point(command.Control2X, command.Control2Y)
			}
			closing := commandIndex+1 < len(commands) && commands[commandIndex+1].Op == Close
			if closing && target.X == path.Knots[0].Anchor.X && target.Y == path.Knots[0].Anchor.Y {
				path.Knots[0].Preceding = incoming
			} else {
				path.Knots = append(path.Knots, psdvector.Knot{Anchor: target, Preceding: incoming, Leaving: target})
			}
		case Close:
			if path == nil {
				return nil, fontError(ErrMalformed, "raster.path", 0, "close precedes its contour start")
			}
			path = nil
		default:
			return nil, fontError(ErrUnsupported, "raster.path", 0, "unknown path operation")
		}
	}
	if path != nil {
		return nil, fontError(ErrMalformed, "raster.path", 0, "final contour is not closed")
	}
	return result, nil
}
