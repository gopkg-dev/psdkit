package psdcompose_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/psdrendered"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func reviewShadowLayer(bounds image.Rectangle) *psdmodel.Layer {
	pixels := image.NewNRGBA(image.Rect(-2, 4, -2+bounds.Dx(), 4+bounds.Dy()))
	for row := 0; row < bounds.Dy(); row++ {
		for column := 0; column < bounds.Dx(); column++ {
			pixels.SetNRGBA(column-2, row+4, color.NRGBA{
				R: byte(17 + column*29), G: byte(233 - row*31), B: byte(91 + column*7 + row*13),
				A: byte((column*37 + row*71 + 23) % 256),
			})
		}
	}
	return &psdmodel.Layer{Bounds: bounds, Pixels: pixels, BlendMode: "norm"}
}

func reviewShadowEffect() psdeffects.Effect {
	return psdeffects.Effect{Kind: psdeffects.DropShadow, Enabled: true,
		Color: [3]float64{.17, .43, .81}, BlendMode: "mul ", Opacity: .63,
		Size: 2, Spread: .04, Distance: 4, Angle: 54, LayerConceals: true}
}

func reviewShadowSet(effects ...psdeffects.Effect) *psdeffects.Set {
	return &psdeffects.Set{Enabled: true, EffectsVisible: true, Scale: 1, GlobalAngle: 90, Items: effects}
}

// reviewShadowCoverage evaluates the documented sampling policy directly, without production caches or kernels.
func reviewShadowCoverage(shape func(int, int) float64, effect psdeffects.Effect, x, y int) float64 {
	matte := func(px, py int) float64 {
		value := 0.0
		radius := effect.Size * effect.Spread
		for dy := -int(math.Ceil(radius)); dy <= int(math.Ceil(radius)); dy++ {
			for dx := -int(math.Ceil(radius)); dx <= int(math.Ceil(radius)); dx++ {
				weight := math.Max(0, math.Min(1, radius+1-math.Hypot(float64(dx), float64(dy))))
				value = math.Max(value, shape(px+dx, py+dy)*weight)
			}
		}
		return value
	}
	shifted := func(px, py int) float64 {
		angle := effect.Angle * math.Pi / 180
		cx := float64(px) + effect.Distance*math.Cos(angle)
		cy := float64(py) - effect.Distance*math.Sin(angle)
		ix, iy := int(math.Floor(cx)), int(math.Floor(cy))
		fx, fy := cx-float64(ix), cy-float64(iy)
		return (1-fx)*(1-fy)*matte(ix, iy) + fx*(1-fy)*matte(ix+1, iy) +
			(1-fx)*fy*matte(ix, iy+1) + fx*fy*matte(ix+1, iy+1)
	}
	radius := effect.Size * (1 - effect.Spread)
	value, total := 0.0, 0.0
	if radius == 0 {
		value, total = shifted(x, y), 1
	} else {
		for dy := -int(math.Ceil(radius)); dy <= int(math.Ceil(radius)); dy++ {
			for dx := -int(math.Ceil(radius)); dx <= int(math.Ceil(radius)); dx++ {
				weight := math.Max(radius-math.Abs(float64(dx)), 0) *
					math.Max(radius-math.Abs(float64(dy)), 0)
				value += shifted(x+dx, y+dy) * weight
				total += weight
			}
		}
	}
	value /= total
	if effect.LayerConceals {
		value *= 1 - shape(x, y)
	}
	return math.Floor(value*255+.5) / 255
}

// reviewShadowPremult keeps associated RGB and alpha throughout the independent composition reference.
type reviewShadowPremult [4]float64

func reviewShadowOver(
	background reviewShadowPremult, rgb [3]float64, alpha float64, multiply bool,
) reviewShadowPremult {
	result := reviewShadowPremult{0, 0, 0, alpha + background[3]*(1-alpha)}
	for channel := range rgb {
		if multiply {
			result[channel] = background[channel]*(1-alpha) +
				alpha*rgb[channel]*(1-background[3]+background[channel])
			continue
		}
		result[channel] = background[channel]*(1-alpha) + alpha*rgb[channel]
	}
	return result
}

func reviewShadowRGBA(pixel reviewShadowPremult) color.NRGBA {
	if pixel[3] == 0 {
		return color.NRGBA{}
	}
	quantize := func(value float64) byte { return byte(math.Floor(value*255 + .5)) }
	return color.NRGBA{R: quantize(pixel[0] / pixel[3]), G: quantize(pixel[1] / pixel[3]),
		B: quantize(pixel[2] / pixel[3]), A: quantize(pixel[3])}
}

// reviewShadowPartition paints independent outside/inside samples, then averages associated RGBA by source shape.
// Concealing shadows belong only to the outside region; other shadows keep their descriptor order in both.
func reviewShadowPartition(
	background reviewShadowPremult, rgb [3]float64, shape, fill, opacity float64, bodyMultiply bool,
	shadows []psdeffects.Effect, coverages []float64,
) reviewShadowPremult {
	outside, inside := background, background
	for index, effect := range shadows {
		alpha := coverages[index] * effect.Opacity * opacity
		outside = reviewShadowOver(outside, effect.Color, alpha, effect.BlendMode == "mul ")
		if !effect.LayerConceals {
			inside = reviewShadowOver(inside, effect.Color, alpha, effect.BlendMode == "mul ")
		}
	}
	inside = reviewShadowOver(inside, rgb, fill*opacity, bodyMultiply)
	for channel := range outside {
		outside[channel] = outside[channel]*(1-shape) + inside[channel]*shape
	}
	return outside
}

func TestDropShadowReviewRealShapePremultipliedStages(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(5, 3, 11, 8))
	layer.Opacity, layer.FillOpacity = new(.57), new(.29)
	effect := reviewShadowEffect()
	layer.Effects = reviewShadowSet(effect)
	maskPixels := image.NewNRGBA(image.Rect(0, 0, 16, 13))
	for y := range 13 {
		for x := range 16 {
			maskPixels.SetNRGBA(x, y, color.NRGBA{R: byte((x*59 + y*19 + 31) % 256)})
		}
	}
	density := uint8(193)
	layer.Mask = &psdmodel.Mask{Bounds: maskPixels.Rect, Pixels: maskPixels,
		Density: &density, Invert: true, DefaultColor: new(uint8(37))}
	background := reviewShadowLayer(maskPixels.Rect)
	for index := 0; index < len(background.Pixels.Pix); index += 4 {
		copy(background.Pixels.Pix[index:index+4], []byte{93, 181, 217, 109})
	}
	document := &psdmodel.Document{Width: 16, Height: 13, Layers: []*psdmodel.Layer{background, layer}}
	input, maskInput := bytes.Clone(layer.Pixels.Pix), bytes.Clone(maskPixels.Pix)
	maskAt := func(x, y int) float64 {
		stored := float64(maskPixels.NRGBAAt(x, y).R) / 255
		return 1 - stored*float64(density)/255
	}
	for _, global := range []bool{false, true} {
		layer.MaskAsGlobal = &global
		for _, bodyMode := range []string{"norm", "mul "} {
			layer.BlendMode = bodyMode
			for _, shadowMode := range []string{"norm", "mul "} {
				layer.Effects.Items[0].BlendMode = shadowMode
				for _, conceal := range []bool{false, true} {
					layer.Effects.Items[0].LayerConceals = conceal
					effect = layer.Effects.Items[0]
					shape := func(x, y int) float64 {
						if !image.Pt(x, y).In(layer.Bounds) {
							return 0
						}
						xp := x - layer.Bounds.Min.X + layer.Pixels.Rect.Min.X
						yp := y - layer.Bounds.Min.Y + layer.Pixels.Rect.Min.Y
						alpha := float64(layer.Pixels.NRGBAAt(xp, yp).A) / 255
						if !global {
							alpha = math.Floor(alpha*maskAt(x, y)*255+.5) / 255
						}
						return alpha
					}
					actual, err := psdcompose.Render(nil, document, psdcompose.Options{ApproximateDropShadow: true})
					if err != nil {
						t.Fatal(err)
					}
					for y := range 13 {
						for x := range 16 {
							original := reviewShadowOver(reviewShadowPremult{}, [3]float64{93.0 / 255,
								181.0 / 255, 217.0 / 255}, 109.0/255, false)
							raw := effect
							raw.LayerConceals = false
							pixel := layer.Pixels.NRGBAAt(x-layer.Bounds.Min.X+layer.Pixels.Rect.Min.X,
								y-layer.Bounds.Min.Y+layer.Pixels.Rect.Min.Y)
							painted := reviewShadowPartition(original, [3]float64{float64(pixel.R) / 255,
								float64(pixel.G) / 255, float64(pixel.B) / 255}, shape(x, y), .29, .57,
								bodyMode == "mul ", []psdeffects.Effect{effect},
								[]float64{reviewShadowCoverage(shape, raw, x, y)})
							if global {
								amount := maskAt(x, y)
								for channel := range painted {
									painted[channel] = painted[channel]*amount + original[channel]*(1-amount)
								}
							}
							if got, want := actual.NRGBAAt(x, y), reviewShadowRGBA(painted); got != want {
								t.Fatalf("stage=%v body=%q shadow=%q conceal=%v at %d,%d: got=%v want=%v",
									global, bodyMode, shadowMode, conceal, x, y, got, want)
							}
						}
					}
				}
			}
		}
	}
	if !bytes.Equal(input, layer.Pixels.Pix) || !bytes.Equal(maskInput, maskPixels.Pix) {
		t.Fatal("rendering mutated borrowed source or mask pixels")
	}
}

func TestDropShadowReviewOffCanvasFeatherSamplesRemainSourceDependencies(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(-7, 2, -2, 8))
	for index := 0; index < len(layer.Pixels.Pix); index += 4 {
		copy(layer.Pixels.Pix[index:index+4], []byte{117, 93, 197, 255})
	}
	effect := reviewShadowEffect()
	effect.Size, effect.Distance, effect.Angle, effect.LayerConceals = 0, 8, 180, false
	layer.Effects, layer.FillOpacity, layer.MaskAsGlobal = reviewShadowSet(effect), new(0.0),
		new(false)
	mask := image.NewNRGBA(layer.Bounds)
	for y := mask.Rect.Min.Y; y < mask.Rect.Max.Y; y++ {
		for x := mask.Rect.Min.X; x < mask.Rect.Max.X; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{R: byte((x*31 + y*57 + 251) & 255)})
		}
	}
	layer.Mask = &psdmodel.Mask{Bounds: mask.Rect, Pixels: mask, Feather: new(1.0),
		DefaultColor: new(uint8(0))}
	document := &psdmodel.Document{Width: 12, Height: 10, Layers: []*psdmodel.Layer{layer}}
	actual, err := psdcompose.Render(nil, document, psdcompose.Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	// Independently pre-feather source alpha using the already public ordinary masked-layer renderer.
	// Translating the canvas encloses the entire source and convolution support, without changing mask defaults.
	translation := image.Pt(16, 6)
	translated := reviewShadowLayer(layer.Bounds.Add(translation))
	copy(translated.Pixels.Pix, layer.Pixels.Pix)
	translated.Mask = &psdmodel.Mask{Bounds: layer.Mask.Bounds.Add(translation), Pixels: mask,
		Feather: new(1.0), DefaultColor: new(uint8(0))}
	masked, err := psdcompose.Render(nil, &psdmodel.Document{Width: 40, Height: 25,
		Layers: []*psdmodel.Layer{translated}})
	if err != nil {
		t.Fatal(err)
	}
	for y := layer.Bounds.Min.Y; y < layer.Bounds.Max.Y; y++ {
		for x := layer.Bounds.Min.X; x < layer.Bounds.Max.X; x++ {
			alpha := float64(masked.NRGBAAt(x+translation.X, y+translation.Y).A) / 255
			want := reviewShadowRGBA(reviewShadowOver(reviewShadowPremult{}, effect.Color, alpha*effect.Opacity, false))
			if got := actual.NRGBAAt(x+8, y); got != want {
				t.Fatalf("off-canvas source feather was omitted before its shadow entered the canvas at %d,%d: %v != %v",
					x+8, y, got, want)
			}
		}
	}
}

func TestDropShadowReviewHiddenInteractionsAndCancelCause(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(1, 1, 5, 5))
	layer.Effects = reviewShadowSet(reviewShadowEffect())
	layer.Effects.Items[0].Noise = .25
	document := &psdmodel.Document{Width: 7, Height: 7, Layers: []*psdmodel.Layer{layer}}
	option := psdcompose.Options{ApproximateDropShadow: true}
	if output, err := psdcompose.Render(nil, document, option); output != nil ||
		!errors.Is(err, psdcompose.ErrUnsupported) {
		t.Fatalf("visible noise was silently approximated: %v", err)
	}
	layer.Hidden = true
	if _, err := psdcompose.Render(nil, document, option); err != nil {
		t.Fatalf("hidden unsupported effects blocked the empty visible scene: %v", err)
	}
	layer.Hidden, layer.Effects.Items[0].Noise = false, 0
	ctx, cancel := context.WithCancelCause(context.Background())
	cause := errors.New("review cancellation cause")
	cancel(cause)
	if output, err := psdcompose.Render(ctx, document, option); output != nil || !errors.Is(err, context.Canceled) ||
		!errors.Is(context.Cause(ctx), cause) {
		t.Fatalf("canceled rendering produced pixels or changed context cause: %v", err)
	}
}

// reviewShadowGaussian integrates source and destination pixel areas using deterministic Simpson quadrature.
// This reference does not call featherSigma, featherKernel or any compositor cache helper.
func reviewShadowGaussian(mask *psdmodel.Mask, x, y int) float64 {
	const sigma = 1.02
	const radius = 4
	var weights [radius*2 + 1]float64
	total := 0.0
	for offset := -radius; offset <= radius; offset++ {
		const slices = 128
		integrate := func(lower, upper float64) float64 {
			step, sum := (upper-lower)/slices, 0.0
			for sample := 0; sample <= slices; sample++ {
				t := lower + float64(sample)*step
				value := (1 - math.Abs(t)) * math.Exp(-.5*math.Pow((float64(offset)+t)/sigma, 2)) /
					(sigma * math.Sqrt(2*math.Pi))
				coefficient := 2.0
				if sample == 0 || sample == slices {
					coefficient = 1
				} else if sample%2 == 1 {
					coefficient = 4
				}
				sum += coefficient * value
			}
			return sum * step / 3
		}
		weights[offset+radius] = integrate(-1, 0) + integrate(0, 1)
		total += weights[offset+radius]
	}
	value := 0.0
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			sample := 1.0
			if mask.DefaultColor != nil {
				sample = float64(*mask.DefaultColor) / 255
			}
			if image.Pt(x+dx, y+dy).In(mask.Bounds) {
				sample = float64(mask.Pixels.NRGBAAt(x+dx, y+dy).R) / 255
			}
			value += sample * weights[dx+radius] * weights[dy+radius] / (total * total)
		}
	}
	if mask.Invert {
		value = 1 - value
	}
	if mask.Density != nil {
		value = 1 - (1-value)*float64(*mask.Density)/255
	}
	return value
}

func TestDropShadowReviewOffCanvasDefaultExtensionDirectGaussian(t *testing.T) {
	for _, maskDefault := range []uint8{0, 255} {
		layer := reviewShadowLayer(image.Rect(-9, -3, 3, 10))
		for index := 0; index < len(layer.Pixels.Pix); index += 4 {
			layer.Pixels.Pix[index+3] = byte(27 + (index*43)%229)
		}
		mask := image.NewNRGBA(image.Rect(-7, -1, 0, 7))
		for y := mask.Rect.Min.Y; y < mask.Rect.Max.Y; y++ {
			for x := mask.Rect.Min.X; x < mask.Rect.Max.X; x++ {
				mask.SetNRGBA(x, y, color.NRGBA{R: byte((x*31 + y*47 + 73) & 255)})
			}
		}
		layer.Mask = &psdmodel.Mask{Bounds: mask.Rect, Pixels: mask, DefaultColor: &maskDefault,
			Feather: new(1.0), Density: new(uint8(177)), Invert: true}
		layer.MaskAsGlobal, layer.FillOpacity, layer.Opacity = new(false), new(0.0),
			new(.71)
		effect := reviewShadowEffect()
		effect.Distance, effect.Angle, effect.LayerConceals = 12, 0, false
		// Positive displacement brings the source's negative coordinates into view.
		effect.Angle = 180
		layer.Effects = reviewShadowSet(effect)
		document := &psdmodel.Document{Width: 17, Height: 14, Layers: []*psdmodel.Layer{layer}}
		option := psdcompose.Options{ApproximateDropShadow: true}
		full, err := psdcompose.Render(nil, document, option)
		if err != nil {
			t.Fatal(err)
		}
		shapeValues := make(map[image.Point]float64)
		for y := layer.Bounds.Min.Y; y < layer.Bounds.Max.Y; y++ {
			for x := layer.Bounds.Min.X; x < layer.Bounds.Max.X; x++ {
				pixel := layer.Pixels.NRGBAAt(x-layer.Bounds.Min.X+layer.Pixels.Rect.Min.X,
					y-layer.Bounds.Min.Y+layer.Pixels.Rect.Min.Y)
				shapeValues[image.Pt(x, y)] = math.Floor(float64(pixel.A)*reviewShadowGaussian(layer.Mask, x, y)+.5) / 255
			}
		}
		shape := func(x, y int) float64 { return shapeValues[image.Pt(x, y)] }
		for y := 0; y < document.Height; y++ {
			for x := 0; x < document.Width; x++ {
				coverage := reviewShadowCoverage(shape, effect, x, y)
				want := reviewShadowRGBA(reviewShadowOver(reviewShadowPremult{}, effect.Color, coverage*.63*.71, false))
				if got := full.NRGBAAt(x, y); got != want {
					t.Fatalf("default=%d at %d,%d direct area-Gaussian source shadow: %v != %v", maskDefault, x, y, got, want)
				}
			}
		}
		for _, region := range []image.Rectangle{image.Rect(0, 0, 6, 9), image.Rect(8, 1, 16, 12)} {
			regional, err := psdcompose.RenderRegion(nil, document, region, option)
			if err != nil {
				t.Fatal(err)
			}
			for y := region.Min.Y; y < region.Max.Y; y++ {
				for x := region.Min.X; x < region.Max.X; x++ {
					if regional.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
						t.Fatalf("off-canvas mask source changed at ROI %v pixel %d,%d", region, x, y)
					}
				}
			}
		}
		partial := image.Rect(-6, 0, 0, 7)
		layer.Mask.SampleBounds = &partial
		layer.Mask.Pixels = mask.SubImage(partial).(*image.NRGBA)
		if err := psdcompose.ValidateRegion(document, image.Rect(8, 1, 16, 12), option); !errors.Is(err,
			psdcompose.ErrInvalid) {
			t.Fatalf("partial mask samples omitted the negative source halo: %v", err)
		}
	}
}

func TestDropShadowReviewSharedMaskMustIncludeAllShapeSourceBounds(t *testing.T) {
	sharedBounds := image.Rect(-9, -9, -1, 8)
	mask := image.NewNRGBA(sharedBounds)
	for y := sharedBounds.Min.Y; y < sharedBounds.Max.Y; y++ {
		for x := sharedBounds.Min.X; x < sharedBounds.Max.X; x++ {
			mask.SetNRGBA(x, y, color.NRGBA{R: byte((x*17 + y*63 + 39) & 255)})
		}
	}
	shared := &psdmodel.Mask{Bounds: sharedBounds, Pixels: mask, Feather: new(1.0),
		DefaultColor: new(uint8(0))}
	first := reviewShadowLayer(image.Rect(-9, 2, -7, 6))
	second := reviewShadowLayer(image.Rect(-4, -8, -2, -4))
	effect := reviewShadowEffect()
	effect.Size, effect.Distance, effect.Angle, effect.LayerConceals = 0, 9, 180, false
	for _, layer := range []*psdmodel.Layer{first, second} {
		layer.Mask, layer.MaskAsGlobal, layer.FillOpacity = shared, new(false), new(0.0)
		layer.Effects = reviewShadowSet(effect)
	}
	second.Effects.Items[0].Distance, second.Effects.Items[0].Angle = math.Sqrt(117),
		math.Atan2(6, -9)*180/math.Pi
	option := psdcompose.Options{ApproximateDropShadow: true}
	combined, err := psdcompose.Render(nil, &psdmodel.Document{Width: 10, Height: 10,
		Layers: []*psdmodel.Layer{first, second}}, option)
	if err != nil {
		t.Fatal(err)
	}
	separate, err := psdcompose.Render(nil, &psdmodel.Document{Width: 10, Height: 10,
		Layers: []*psdmodel.Layer{second}}, option)
	if err != nil {
		t.Fatal(err)
	}
	// The first shadow occupies x=0..1,y=2..5; the second occupies x=5..6,y=-2..1.
	for y := range 2 {
		for x := 5; x < 7; x++ {
			if combined.NRGBAAt(x, y) != separate.NRGBAAt(x, y) {
				t.Fatalf("shared mask feather failed to include later source bounds at %d,%d: %v != %v",
					x, y, combined.NRGBAAt(x, y), separate.NRGBAAt(x, y))
			}
		}
	}
}

func TestDropShadowReviewProjectionIdentityIncludesRasterHeadersAndPlacementValues(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(*psdmodel.Layer)
	}{
		{"backing", func(layer *psdmodel.Layer) { layer.Pixels.Pix = bytes.Clone(layer.Pixels.Pix) }},
		{"stride", func(layer *psdmodel.Layer) {
			original := layer.Pixels
			padded := image.NewNRGBA(image.Rect(0, 0, original.Rect.Dx()+1, original.Rect.Dy()))
			for y := 0; y < original.Rect.Dy(); y++ {
				copy(padded.Pix[y*padded.Stride:y*padded.Stride+original.Rect.Dx()*4],
					original.Pix[y*original.Stride:y*original.Stride+original.Rect.Dx()*4])
			}
			original.Pix, original.Stride = padded.Pix, padded.Stride
		}},
		{"pixelOrigin", func(layer *psdmodel.Layer) { layer.Pixels.Rect = layer.Pixels.Rect.Add(image.Pt(1, 2)) }},
		{"transform", func(layer *psdmodel.Layer) { layer.Placed.Transform[0] = .5 }},
		{"nonAffine", func(layer *psdmodel.Layer) { layer.Placed.NonAffineTransform = []float64{1, 2, 3, 4} }},
		{"width", func(layer *psdmodel.Layer) { layer.Placed.Width++ }},
		{"identifier", func(layer *psdmodel.Layer) { layer.Placed.ID = "new-source" }},
		{"warp", func(layer *psdmodel.Layer) { layer.Placed.Warp.Value++ }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			layer := reviewShadowLayer(image.Rect(1, 1, 5, 5))
			layer.Placed = &psdmodel.Placed{ID: "projected-source", Type: "raster", Width: 4, Height: 4,
				Transform: []float64{0, 0, 4, 0, 4, 4, 0, 4}, Warp: &psdmodel.Warp{Style: "warpNone"}}
			layer.Effects = reviewShadowSet(reviewShadowEffect())
			layer.RenderedEffects = psdrendered.New(layer.Pixels, layer.Bounds, layer.Placed)
			document := &psdmodel.Document{Width: 8, Height: 8, Layers: []*psdmodel.Layer{layer}}
			option := psdcompose.Options{ApproximateDropShadow: true}
			if err := psdcompose.Validate(document, option); err != nil {
				t.Fatalf("fresh projected identity was rejected: %v", err)
			}
			mutation.mutate(layer)
			if err := psdcompose.Validate(document, option); !errors.Is(err, psdcompose.ErrUnsupported) {
				t.Fatalf("modified %s kept a stale projected raster token valid: %v", mutation.name, err)
			}
		})
	}
}

func TestDropShadowReviewAbsentMaskStageUsesSourceLinkStateAndExplicitOverride(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(3, 2, 8, 7))
	layer.Effects = reviewShadowSet(reviewShadowEffect())
	mask := image.NewNRGBA(image.Rect(1, 0, 11, 10))
	for index := 0; index < len(mask.Pix); index += 4 {
		mask.Pix[index] = byte((index*11 + 37) & 255)
	}
	layer.Mask = &psdmodel.Mask{Bounds: mask.Rect, Pixels: mask}
	document := &psdmodel.Document{Width: 12, Height: 11, Layers: []*psdmodel.Layer{layer}}
	option := psdcompose.Options{ApproximateDropShadow: true}
	for _, relative := range []bool{false, true} {
		layer.Mask.Relative, layer.MaskAsGlobal = relative, nil
		absent, err := psdcompose.Render(nil, document, option)
		if err != nil {
			t.Fatal(err)
		}
		layer.MaskAsGlobal = new(relative)
		explicit, err := psdcompose.Render(nil, document, option)
		if err != nil || !bytes.Equal(absent.Pix, explicit.Pix) {
			t.Fatalf("absent mask effect flag disagreed with registered source link state relative=%v: %v", relative, err)
		}
		layer.MaskAsGlobal = new(!relative)
		opposite, err := psdcompose.Render(nil, document, option)
		if err != nil || bytes.Equal(absent.Pix, opposite.Pix) {
			t.Fatalf("explicit mask stage did not override opposite source link state relative=%v: %v", relative, err)
		}
	}
}

func TestDropShadowReviewNestedOpacityAndLiteralZeroSizeBudget(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(2, 2, 4, 4))
	for index := 0; index < len(layer.Pixels.Pix); index += 4 {
		copy(layer.Pixels.Pix[index:index+4], []byte{179, 53, 113, 127})
	}
	effect := reviewShadowEffect()
	effect.Size, effect.Distance, effect.Angle = 0, 1, 90
	effect.LayerConceals, effect.BlendMode = false, "norm"
	layer.Effects, layer.Opacity, layer.FillOpacity = reviewShadowSet(effect), new(.61),
		new(.37)
	inner := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{layer},
		Opacity: new(.71)}
	outer := &psdmodel.Layer{Group: true, BlendMode: "norm", Children: []*psdmodel.Layer{inner},
		Opacity: new(.53)}
	document := &psdmodel.Document{Width: 7, Height: 7, Layers: []*psdmodel.Layer{outer}}
	option := psdcompose.Options{ApproximateDropShadow: true}
	actual, err := psdcompose.Render(nil, document, option)
	if err != nil {
		t.Fatal(err)
	}
	want := reviewShadowOver(reviewShadowPremult{}, effect.Color, 127.0/255*.63*.61, false)
	want = reviewShadowOver(want, [3]float64{179.0 / 255, 53.0 / 255, 113.0 / 255}, 127.0/255*.37*.61, false)
	for channel := range want {
		want[channel] *= .71 * .53
	}
	if actual.NRGBAAt(2, 3) != reviewShadowRGBA(want) {
		t.Fatalf("ancestor opacity was not applied once to body plus independent shadow: %v != %v",
			actual.NRGBAAt(2, 3), reviewShadowRGBA(want))
	}
	// 196 output + 3*512 nodes + 3*512 recursion + 4 source alpha + 4+512 plane + 8*4+8 scratch.
	const literalWorkingBound = 196 + 3*512 + 3*512 + 4 + 4 + 512 + 8*4 + 8
	option.MaxWorkingBytes = literalWorkingBound - 1
	if output, err := psdcompose.Render(nil, document, option); output != nil || !errors.Is(err, psdcompose.ErrLimit) {
		t.Fatalf("one byte below independently derived working bound succeeded: %v", err)
	}
	option.MaxWorkingBytes++
	if _, err := psdcompose.Render(nil, document, option); err != nil {
		t.Fatalf("independently derived exact working bound failed: %v", err)
	}
}

func TestDropShadowReviewSharedMaskKeepsOrdinaryFeatherBoundary(t *testing.T) {
	bounds := image.Rect(0, 0, 7, 7)
	mask := image.NewNRGBA(bounds)
	for y := range 7 {
		for x := range 7 {
			mask.SetNRGBA(x, y, color.NRGBA{R: byte(23 + x*27 + y*11)})
		}
	}
	shared := &psdmodel.Mask{Bounds: bounds, Pixels: mask, Feather: new(1.0),
		DefaultColor: new(uint8(0))}
	ordinary := reviewShadowLayer(bounds)
	ordinary.Mask = shared
	shadow := reviewShadowLayer(image.Rect(0, 0, 1, 1))
	shadow.Mask, shadow.MaskAsGlobal, shadow.FillOpacity = shared, new(false), new(0.0)
	effect := reviewShadowEffect()
	effect.Size, effect.Distance, effect.Angle = 0, 30, 90
	shadow.Effects = reviewShadowSet(effect)
	option := psdcompose.Options{ApproximateDropShadow: true}
	combined, err := psdcompose.Render(nil, &psdmodel.Document{Width: 7, Height: 7,
		Layers: []*psdmodel.Layer{ordinary, shadow}}, option)
	if err != nil {
		t.Fatal(err)
	}
	separate, err := psdcompose.Render(nil, &psdmodel.Document{Width: 7, Height: 7,
		Layers: []*psdmodel.Layer{ordinary}}, option)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(combined.Pix, separate.Pix) {
		t.Fatal("a fully off-canvas shadow changed its ordinary sibling's shared-mask document-edge feather behavior")
	}
}

func TestDropShadowReviewSharedMaskKeepsFinalStageFeatherBoundary(t *testing.T) {
	bounds := image.Rect(0, 0, 7, 7)
	mask := image.NewNRGBA(bounds)
	for y := range 7 {
		for x := range 7 {
			mask.SetNRGBA(x, y, color.NRGBA{R: byte(23 + x*27 + y*11)})
		}
	}
	shared := &psdmodel.Mask{Bounds: bounds, Pixels: mask, Feather: new(1.0),
		DefaultColor: new(uint8(0))}
	final := reviewShadowLayer(bounds)
	final.Mask, final.MaskAsGlobal = shared, new(true)
	final.Effects = reviewShadowSet(reviewShadowEffect())
	shape := reviewShadowLayer(image.Rect(0, 0, 1, 1))
	shape.Mask, shape.MaskAsGlobal, shape.FillOpacity = shared, new(false), new(0.0)
	effect := reviewShadowEffect()
	effect.Size, effect.Distance, effect.Angle = 0, 30, 90
	shape.Effects = reviewShadowSet(effect)
	option := psdcompose.Options{ApproximateDropShadow: true}
	combined, err := psdcompose.Render(nil, &psdmodel.Document{Width: 7, Height: 7,
		Layers: []*psdmodel.Layer{final, shape}}, option)
	if err != nil {
		t.Fatal(err)
	}
	separate, err := psdcompose.Render(nil, &psdmodel.Document{Width: 7, Height: 7,
		Layers: []*psdmodel.Layer{final}}, option)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(combined.Pix, separate.Pix) {
		t.Fatal("a fully off-canvas shadow changed its final-stage sibling's shared-mask feather behavior")
	}
}

func TestDropShadowReviewUnconcealedPartitionEqualsOrdinarySourceOver(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(2, 3, 8, 9))
	layer.Opacity, layer.FillOpacity = new(.47), new(.63)
	background := reviewShadowLayer(image.Rect(0, 0, 12, 13))
	for index := 0; index < len(background.Pixels.Pix); index += 4 {
		copy(background.Pixels.Pix[index:index+4], []byte{101, 151, 203, 83})
	}
	effect := reviewShadowEffect()
	effect.LayerConceals = false
	layer.Effects = reviewShadowSet(effect)
	document := &psdmodel.Document{Width: 12, Height: 13, Layers: []*psdmodel.Layer{background, layer}}
	shape := func(x, y int) float64 {
		if !image.Pt(x, y).In(layer.Bounds) {
			return 0
		}
		return float64(layer.Pixels.NRGBAAt(x-layer.Bounds.Min.X+layer.Pixels.Rect.Min.X,
			y-layer.Bounds.Min.Y+layer.Pixels.Rect.Min.Y).A) / 255
	}
	for _, bodyMode := range []string{"norm", "mul "} {
		layer.BlendMode = bodyMode
		for _, shadowMode := range []string{"norm", "mul "} {
			layer.Effects.Items[0].BlendMode = shadowMode
			actual, err := psdcompose.Render(nil, document, psdcompose.Options{ApproximateDropShadow: true})
			if err != nil {
				t.Fatal(err)
			}
			for y := 0; y < document.Height; y++ {
				for x := 0; x < document.Width; x++ {
					original := reviewShadowOver(reviewShadowPremult{}, [3]float64{101.0 / 255,
						151.0 / 255, 203.0 / 255}, 83.0/255, false)
					painted := reviewShadowOver(original, effect.Color,
						reviewShadowCoverage(shape, effect, x, y)*.63*.47, shadowMode == "mul ")
					pixel := layer.Pixels.NRGBAAt(x-layer.Bounds.Min.X+layer.Pixels.Rect.Min.X,
						y-layer.Bounds.Min.Y+layer.Pixels.Rect.Min.Y)
					painted = reviewShadowOver(painted, [3]float64{float64(pixel.R) / 255,
						float64(pixel.G) / 255, float64(pixel.B) / 255}, shape(x, y)*.63*.47, bodyMode == "mul ")
					if got, want := actual.NRGBAAt(x, y), reviewShadowRGBA(painted); got != want {
						t.Fatalf("unconcealed ordinary equivalence %q/%q at %d,%d: %v != %v", bodyMode, shadowMode,
							x, y, got, want)
					}
				}
			}
		}
	}
}

func TestDropShadowReviewMixedConcealmentDescriptorOrderOverPartialBackdrop(t *testing.T) {
	layer := reviewShadowLayer(image.Rect(3, 3, 8, 8))
	layer.Opacity, layer.FillOpacity, layer.BlendMode = new(.71), new(.43), "mul "
	background := reviewShadowLayer(image.Rect(0, 0, 12, 12))
	for index := 0; index < len(background.Pixels.Pix); index += 4 {
		copy(background.Pixels.Pix[index:index+4], []byte{91, 169, 223, 97})
	}
	first, second, third := reviewShadowEffect(), reviewShadowEffect(), reviewShadowEffect()
	first.Color, second.Color, third.Color = [3]float64{.81, .19, .37}, [3]float64{.13, .71, .29},
		[3]float64{.27, .43, .89}
	first.Size, second.Size, third.Size = 0, 0, 0
	first.Distance, second.Distance, third.Distance = 0, 1, 2
	first.Angle, second.Angle, third.Angle = 90, 180, 270
	first.Opacity, second.Opacity, third.Opacity = .57, .39, .73
	first.LayerConceals, second.LayerConceals, third.LayerConceals = true, false, true
	first.BlendMode, second.BlendMode, third.BlendMode = "mul ", "norm", "norm"
	document := &psdmodel.Document{Width: 12, Height: 12, Layers: []*psdmodel.Layer{background, layer}}
	shape := func(x, y int) float64 {
		if !image.Pt(x, y).In(layer.Bounds) {
			return 0
		}
		return float64(layer.Pixels.NRGBAAt(x-layer.Bounds.Min.X+layer.Pixels.Rect.Min.X,
			y-layer.Bounds.Min.Y+layer.Pixels.Rect.Min.Y).A) / 255
	}
	var prior *image.NRGBA
	for _, effects := range [][]psdeffects.Effect{{first, second, third}, {third, second, first}, {second, third, first}} {
		layer.Effects = reviewShadowSet(effects...)
		actual, err := psdcompose.Render(nil, document, psdcompose.Options{ApproximateDropShadow: true})
		if err != nil {
			t.Fatal(err)
		}
		for y := 0; y < document.Height; y++ {
			for x := 0; x < document.Width; x++ {
				original := reviewShadowOver(reviewShadowPremult{}, [3]float64{91.0 / 255, 169.0 / 255,
					223.0 / 255}, 97.0/255, false)
				coverages := make([]float64, len(effects))
				for index, effect := range effects {
					effect.LayerConceals = false
					coverages[index] = reviewShadowCoverage(shape, effect, x, y)
				}
				pixel := layer.Pixels.NRGBAAt(x-layer.Bounds.Min.X+layer.Pixels.Rect.Min.X,
					y-layer.Bounds.Min.Y+layer.Pixels.Rect.Min.Y)
				painted := reviewShadowPartition(original, [3]float64{float64(pixel.R) / 255,
					float64(pixel.G) / 255, float64(pixel.B) / 255}, shape(x, y), .43, .71, true, effects, coverages)
				if got, want := actual.NRGBAAt(x, y), reviewShadowRGBA(painted); got != want {
					t.Fatalf("mixed descriptor order at %d,%d: %v != %v", x, y, got, want)
				}
			}
		}
		if prior != nil && bytes.Equal(prior.Pix, actual.Pix) {
			t.Fatal("mixed-concealment descriptor-order control was not informative")
		}
		prior = actual
	}
}
