package psdcompose

import (
	"context"
	"errors"
	"image"

	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type gradientCache struct {
	path   string
	pixels *image.NRGBA
}

func (plan *composePlan) registerGradient(layer *psdmodel.Layer, path string) error {
	if layer.Gradient == nil {
		return nil
	}
	if layer.Group || layer.Children != nil || layer.FillColor != nil || layer.Adjustment != nil ||
		layer.Placed != nil || layer.Pattern != nil {
		return composeError(ErrInvalid, path, "gradient conflicts with another live layer kind")
	}
	if err := psdgradient.Validate(*layer.Gradient); err != nil {
		return gradientComposeError(err, path)
	}
	plan.gradients[layer] = &gradientCache{path: path}
	return nil
}

func (plan *composePlan) reserveGradientCaches() error {
	area := int64(plan.window.Dx()) * int64(plan.window.Dy())
	if area == 0 {
		return nil
	}
	if int64(len(plan.gradients)) > (plan.options.MaxWorkingBytes-plan.workingBytes)/4/area {
		return composeError(ErrLimit, "document.gradients", "live gradient caches exceed the working budget")
	}
	plan.workingBytes += int64(len(plan.gradients)) * area * 4
	return nil
}

func (plan *composePlan) prepareGradients(ctx context.Context) error {
	canvas := image.Rect(0, 0, plan.document.Width, plan.document.Height)
	area := int64(plan.window.Dx()) * int64(plan.window.Dy())
	if area == 0 {
		return nil
	}
	for layer, cache := range plan.gradients {
		options := psdgradient.Options{MaxPixels: area, Crop: &plan.window,
			MaxWorkingBytes: plan.options.MaxWorkingBytes - plan.workingBytes + area*4}
		if !layer.Bounds.Empty() {
			bounds := layer.Bounds
			options.AlignBounds = &bounds
		}
		pixels, err := psdgradient.Render(ctx, *layer.Gradient, canvas, options)
		if err != nil {
			return gradientComposeError(err, cache.path)
		}
		cache.pixels = pixels
	}
	return nil
}

func gradientComposeError(err error, path string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalid
	if errors.Is(err, psdgradient.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdgradient.ErrLimit) {
		kind = ErrLimit
	}
	return composeError(kind, path, err.Error())
}

func (plan *composePlan) gradientSource(layer *psdmodel.Layer, x, y int) composePixel {
	cache := plan.gradients[layer]
	if cache == nil || cache.pixels == nil {
		return composePixel{}
	}
	pixel := cache.pixels.NRGBAAt(x, y)
	return composePixel{color: [3]float64{float64(pixel.R) / 255, float64(pixel.G) / 255, float64(pixel.B) / 255},
		alpha: float64(pixel.A) / 255}
}
