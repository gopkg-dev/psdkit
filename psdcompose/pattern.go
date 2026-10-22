package psdcompose

import (
	"context"
	"errors"
	"image"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdpattern"
)

type patternCache struct {
	path   string
	pixels *image.NRGBA
}

func (plan *composePlan) registerPattern(layer *psdmodel.Layer, path string) error {
	if layer.Pattern == nil {
		return nil
	}
	if layer.Group || layer.Children != nil || layer.FillColor != nil || layer.Adjustment != nil ||
		layer.Placed != nil || layer.Gradient != nil {
		return composeError(ErrInvalid, path, "pattern conflicts with another live layer kind")
	}
	if layer.PatternTile == nil {
		return composeError(ErrUnsupported, path, "local pattern tile has not been explicitly decoded")
	}
	if err := psdpattern.Validate(layer.PatternTile, *layer.Pattern); err != nil {
		return patternComposeError(err, path)
	}
	plan.patterns[layer] = &patternCache{path: path}
	return nil
}

func (plan *composePlan) reservePatternCaches() error {
	area := int64(plan.window.Dx()) * int64(plan.window.Dy())
	if area == 0 {
		return nil
	}
	if int64(len(plan.patterns)) > (plan.options.MaxWorkingBytes-plan.workingBytes)/4/area {
		return composeError(ErrLimit, "document.patterns", "live pattern caches exceed the working budget")
	}
	plan.workingBytes += int64(len(plan.patterns)) * area * 4
	return nil
}

func (plan *composePlan) preparePatterns(ctx context.Context) error {
	canvas := image.Rect(0, 0, plan.document.Width, plan.document.Height)
	area := int64(plan.window.Dx()) * int64(plan.window.Dy())
	if area == 0 {
		return nil
	}
	for layer, cache := range plan.patterns {
		pixels, err := psdpattern.Render(ctx, layer.PatternTile, *layer.Pattern, canvas,
			psdpattern.Options{MaxPixels: area, Crop: &plan.window,
				MaxWorkingBytes: plan.options.MaxWorkingBytes - plan.workingBytes + area*4})
		if err != nil {
			return patternComposeError(err, cache.path)
		}
		cache.pixels = pixels
	}
	return nil
}

func (plan *composePlan) patternSource(layer *psdmodel.Layer, x, y int) composePixel {
	cache := plan.patterns[layer]
	if cache == nil || cache.pixels == nil {
		return composePixel{}
	}
	pixel := cache.pixels.NRGBAAt(x, y)
	return composePixel{color: [3]float64{float64(pixel.R) / 255, float64(pixel.G) / 255, float64(pixel.B) / 255},
		alpha: float64(pixel.A) / 255}
}

func patternComposeError(err error, path string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalid
	if errors.Is(err, psdpattern.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdpattern.ErrLimit) {
		kind = ErrLimit
	}
	return composeError(kind, path, err.Error())
}
