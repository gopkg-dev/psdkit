package psdrender

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"

	"github.com/gopkg-dev/psdkit/internal/canvaspix"
	jpeg "github.com/gopkg-dev/psdkit/psdrender/internal/canvasjpeg"
)

// ErrImageSize identifies unsafe or invalid image dimensions.
var ErrImageSize = errors.New("PSD image dimensions exceed the supported limits")

// AssertImageSize applies the frontend's 20 MP and 30000-pixel limits before allocation.
func AssertImageSize(width, height int) error {
	if width <= 0 || height <= 0 || width > MaxDimension || height > MaxDimension ||
		int64(width)*int64(height) > MaxPixels {
		return fmt.Errorf("%w: %dx%d", ErrImageSize, width, height)
	}
	return nil
}

func validateImage(source *image.NRGBA) error {
	if source == nil {
		return errors.New("missing RGBA image")
	}
	if source.Rect.Max.X < source.Rect.Min.X || source.Rect.Max.Y < source.Rect.Min.Y {
		return errors.New("RGBA image has reversed rectangle edges")
	}
	width, height := source.Rect.Dx(), source.Rect.Dy()
	if err := AssertImageSize(width, height); err != nil {
		return err
	}
	if source.Stride < width*4 || len(source.Pix) < width*4 ||
		height-1 > (len(source.Pix)-width*4)/source.Stride {
		return errors.New("RGBA pixel buffer is incomplete")
	}
	return nil
}

// EmptyImage creates a bounded transparent straight-alpha buffer.
func EmptyImage(width, height int) (*image.NRGBA, error) {
	if err := AssertImageSize(width, height); err != nil {
		return nil, err
	}
	return image.NewNRGBA(image.Rect(0, 0, width, height)), nil
}

// ClampByte matches ECMAScript ToUint8Clamp, including ties to even and NaN handling.
func ClampByte(value float64) uint8 {
	if !(value > 0) {
		return 0
	}
	if value >= 255 {
		return 255
	}
	return uint8(math.RoundToEven(value))
}

func rowCancelled(ctx context.Context, row int) error {
	if row&63 != 0 {
		return nil
	}
	return ctx.Err()
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func sample(source *image.NRGBA, x, y float64, target []uint8, offset int) {
	ix, iy := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(ix), y-float64(iy)
	var alpha, red, green, blue float64
	width, height := source.Rect.Dx(), source.Rect.Dy()
	for dy := range 2 {
		for dx := range 2 {
			sx, sy := max(0, min(width-1, ix+dx)), max(0, min(height-1, iy+dy))
			index := sy*source.Stride + sx*4
			wx, wy := 1-fx, 1-fy
			if dx != 0 {
				wx = fx
			}
			if dy != 0 {
				wy = fy
			}
			weight := wx * wy * float64(source.Pix[index+3]) / 255
			alpha += weight
			red += float64(float64(source.Pix[index]) * weight)
			green += float64(float64(source.Pix[index+1]) * weight)
			blue += float64(float64(source.Pix[index+2]) * weight)
		}
	}
	if alpha > 0 {
		target[offset], target[offset+1] = ClampByte(red/alpha), ClampByte(green/alpha)
		target[offset+2] = ClampByte(blue / alpha)
	}
	target[offset+3] = ClampByte(alpha * 255)
}

var alignments = map[string][2]float64{
	"top-left": {0, 0}, "top": {.5, 0}, "top-right": {1, 0},
	"left": {0, .5}, "center": {.5, .5}, "right": {1, .5},
	"bottom-left": {0, 1}, "bottom": {.5, 1}, "bottom-right": {1, 1},
}

func validateFit(fit, align string) error {
	_, known := alignments[align]
	if (fit != "cover" && fit != "contain" && fit != "fill") || !known {
		return errors.New("印花填充或对齐方式无效")
	}
	return nil
}

// FitImage fits an input with premultiplied bilinear sampling and editor alignment semantics.
// An opaque identity input may be borrowed; callers must treat the returned image as immutable.
func FitImage(source *image.NRGBA, width, height int, fit, align string) (*image.NRGBA, error) {
	return fitImage(context.Background(), source, width, height, fit, align)
}

func fitImage(
	ctx context.Context, source *image.NRGBA, width, height int, fit, align string,
) (*image.NRGBA, error) {
	if err := validateImage(source); err != nil {
		return nil, err
	}
	if err := AssertImageSize(width, height); err != nil {
		return nil, err
	}
	if err := validateFit(fit, align); err != nil {
		return nil, err
	}
	sw, sh := source.Rect.Dx(), source.Rect.Dy()
	if sw == width && sh == height && source.Opaque() {
		return source, nil
	}
	result, _ := EmptyImage(width, height)
	scale := math.Min(float64(width)/float64(sw), float64(height)/float64(sh))
	if fit == "cover" {
		scale = math.Max(float64(width)/float64(sw), float64(height)/float64(sh))
	}
	drawWidth, drawHeight := float64(sw)*scale, float64(sh)*scale
	if fit == "fill" {
		drawWidth, drawHeight = float64(width), float64(height)
	}
	alignXY := alignments[align]
	left, top := (float64(width)-drawWidth)*alignXY[0], (float64(height)-drawHeight)*alignXY[1]
	for y := range height {
		if err := rowCancelled(ctx, y); err != nil {
			return nil, err
		}
		for x := range width {
			u, v := (float64(x)+.5-left)/drawWidth, (float64(y)+.5-top)/drawHeight
			if u < 0 || u > 1 || v < 0 || v > 1 {
				continue
			}
			sample(source, float64(u*float64(sw))-.5, float64(v*float64(sh))-.5, result.Pix, y*result.Stride+x*4)
		}
	}
	return result, nil
}

func layerOpacity(layer *Layer) float64 {
	opacity, fillOpacity := 1.0, 1.0
	if layer.Opacity != nil {
		opacity = *layer.Opacity
	}
	if layer.FillOpacity != nil {
		fillOpacity = *layer.FillOpacity
	}
	return opacity * fillOpacity
}

// LayerMaskAlpha reads absolute grayscale coverage, including default, inversion and density.
func LayerMaskAlpha(layer *Layer, x, y int) float64 {
	mask := layer.Mask
	if mask == nil || mask.Disabled {
		return 1
	}
	pixels := mask.Pixels
	coverage := 1.0
	if mask.DefaultColor != nil {
		coverage = float64(*mask.DefaultColor) / 255
	}
	if pixels != nil && image.Pt(x, y).In(mask.Bounds) {
		mx, my := x-mask.Bounds.Min.X, y-mask.Bounds.Min.Y
		coverage = float64(pixels.Pix[my*pixels.Stride+mx*4]) / 255
	}
	if mask.Invert {
		coverage = 1 - coverage
	}
	if mask.Density != nil {
		coverage = 1 - (1-coverage)*float64(*mask.Density)/255
	}
	return coverage
}

// CompositeLayer paints a source over the destination with saved opacity, fill opacity and mask.
func CompositeLayer(target *image.NRGBA, layer *Layer, source *image.NRGBA) error {
	return compositeLayer(context.Background(), target, layer, source)
}

func compositeLayer(ctx context.Context, target *image.NRGBA, layer *Layer, source *image.NRGBA) error {
	if err := validateImage(target); err != nil {
		return err
	}
	if err := validateImage(source); err != nil {
		return err
	}
	if layer == nil {
		return errors.New("missing PSD layer")
	}
	if layer.Mask != nil && layer.Mask.Pixels != nil {
		if err := validateImage(layer.Mask.Pixels); err != nil {
			return err
		}
	}
	left, top, opacity := layer.Bounds.Min.X, layer.Bounds.Min.Y, layerOpacity(layer)
	if opacity == 0 {
		return nil
	}
	startX, endX := max(0, -left), min(source.Rect.Dx(), target.Rect.Dx()-left)
	for sy := max(0, -top); sy < min(source.Rect.Dy(), target.Rect.Dy()-top); sy++ {
		if err := rowCancelled(ctx, sy); err != nil {
			return err
		}
		for sx := startX; sx < endX; sx++ {
			x, y := sx+left, sy+top
			si, di := sy*source.Stride+sx*4, y*target.Stride+x*4
			if source.Pix[si+3] == 0 {
				continue
			}
			a := float64(source.Pix[si+3]) / 255 * opacity * LayerMaskAlpha(layer, x, y)
			if a == 0 {
				continue
			}
			if a == 1 {
				copy(target.Pix[di:di+4], source.Pix[si:si+4])
				continue
			}
			b := float64(target.Pix[di+3]) / 255
			alpha := a + float64(b*(1-a))
			for channel := range 3 {
				target.Pix[di+channel] = ClampByte((float64(float64(source.Pix[si+channel])*a) +
					float64(float64(target.Pix[di+channel])*b*(1-a))) / alpha)
			}
			target.Pix[di+3] = ClampByte(alpha * 255)
		}
	}
	return nil
}

// CompositeBackgroundReplacement replaces the original footprint, including candidate transparency.
func CompositeBackgroundReplacement(target *image.NRGBA, layer *Layer, replacement *image.NRGBA) error {
	return compositeBackground(context.Background(), target, layer, replacement)
}

func compositeBackground(ctx context.Context, target *image.NRGBA, layer *Layer, replacement *image.NRGBA) error {
	if layer == nil || layer.Pixels == nil {
		return errors.New("随机图目标图层缺少原始像素")
	}
	if err := validateImage(target); err != nil {
		return err
	}
	if err := validateImage(layer.Pixels); err != nil {
		return err
	}
	if layer.Mask != nil && layer.Mask.Pixels != nil {
		if err := validateImage(layer.Mask.Pixels); err != nil {
			return err
		}
	}
	opacity := layerOpacity(layer)
	if opacity == 0 {
		return nil
	}
	original := layer.Pixels
	fitted, err := fitImage(ctx, replacement, original.Rect.Dx(), original.Rect.Dy(), "fill", "center")
	if err != nil {
		return err
	}
	left, top := layer.Bounds.Min.X, layer.Bounds.Min.Y
	startX, endX := max(0, -left), min(original.Rect.Dx(), target.Rect.Dx()-left)
	for sy := max(0, -top); sy < min(original.Rect.Dy(), target.Rect.Dy()-top); sy++ {
		if err := rowCancelled(ctx, sy); err != nil {
			return err
		}
		for sx := startX; sx < endX; sx++ {
			x, y := sx+left, sy+top
			oi, si, di := sy*original.Stride+sx*4, sy*fitted.Stride+sx*4, y*target.Stride+x*4
			coverage := float64(original.Pix[oi+3]) / 255 * opacity * LayerMaskAlpha(layer, x, y)
			if coverage == 0 {
				continue
			}
			newAlpha := float64(fitted.Pix[si+3]) / 255 * coverage
			oldWeight := float64(target.Pix[di+3]) / 255 * (1 - coverage)
			alpha := newAlpha + oldWeight
			if alpha == 0 {
				clear(target.Pix[di : di+4])
				continue
			}
			for channel := range 3 {
				target.Pix[di+channel] = ClampByte((float64(float64(fitted.Pix[si+channel])*newAlpha) +
					float64(float64(target.Pix[di+channel])*oldWeight)) / alpha)
			}
			target.Pix[di+3] = ClampByte(alpha * 255)
		}
	}
	return nil
}

// CropImage copies a valid local crop, or borrows the source for a full-frame crop.
func CropImage(source *image.NRGBA, rect image.Rectangle) (*image.NRGBA, error) {
	if err := validateImage(source); err != nil {
		return nil, err
	}
	bounds := image.Rect(0, 0, source.Rect.Dx(), source.Rect.Dy())
	if rect == bounds {
		return source, nil
	}
	if !rect.In(bounds) || rect.Empty() {
		return nil, errors.New("PSD crop is empty or outside the document")
	}
	result, err := EmptyImage(rect.Dx(), rect.Dy())
	if err != nil {
		return nil, err
	}
	for y := 0; y < rect.Dy(); y++ {
		start := (y+rect.Min.Y)*source.Stride + rect.Min.X*4
		copy(result.Pix[y*result.Stride:y*result.Stride+rect.Dx()*4], source.Pix[start:start+rect.Dx()*4])
	}
	return result, nil
}

// TrimInputTransparency removes fully transparent borders only from input print pixels.
func TrimInputTransparency(source *image.NRGBA) (*image.NRGBA, error) {
	if err := validateImage(source); err != nil {
		return nil, err
	}
	left, top, right, bottom := source.Rect.Dx(), source.Rect.Dy(), -1, -1
	for y := 0; y < source.Rect.Dy(); y++ {
		for x := 0; x < source.Rect.Dx(); x++ {
			if source.Pix[y*source.Stride+x*4+3] == 0 {
				continue
			}
			left, top, right, bottom = min(left, x), min(top, y), max(right, x), max(bottom, y)
		}
	}
	if right < 0 {
		return EmptyImage(1, 1)
	}
	return CropImage(source, image.Rect(left, top, right+1, bottom+1))
}

// ValidateOptions rejects unsupported output formats and invalid longest-edge limits.
func ValidateOptions(options Options) error {
	if options.Format != "png" && options.Format != "jpeg" {
		return errors.New("输出格式无效")
	}
	if options.MaxSize < 0 || options.MaxSize > MaxDimension {
		return errors.New("输出最长边限制无效")
	}
	return nil
}

// PrepareOutput only shrinks a result; input trimming never changes output slice bounds.
func PrepareOutput(source *image.NRGBA, options Options) (*image.NRGBA, error) {
	if err := validateImage(source); err != nil {
		return nil, err
	}
	if err := ValidateOptions(options); err != nil {
		return nil, err
	}
	width, height := source.Rect.Dx(), source.Rect.Dy()
	if options.MaxSize > 0 && max(width, height) > options.MaxSize {
		scale := float64(options.MaxSize) / float64(max(width, height))
		return FitImage(source, max(1, int(math.Floor(float64(width)*scale+.5))),
			max(1, int(math.Floor(float64(height)*scale+.5))), "fill", "center")
	}
	return source, nil
}

// FlattenWhite returns owned pixels with frontend-compatible white JPEG flattening.
func FlattenWhite(source *image.NRGBA) (*image.NRGBA, error) {
	if err := validateImage(source); err != nil {
		return nil, err
	}
	result, _ := EmptyImage(source.Rect.Dx(), source.Rect.Dy())
	for y := 0; y < source.Rect.Dy(); y++ {
		for x := 0; x < source.Rect.Dx(); x++ {
			si, di := y*source.Stride+x*4, y*result.Stride+x*4
			alpha := float64(source.Pix[si+3]) / 255
			for channel := range 3 {
				result.Pix[di+channel] = ClampByte(float64(float64(source.Pix[si+channel])*alpha) + float64(255*(1-alpha)))
			}
			result.Pix[di+3] = 255
		}
	}
	return result, nil
}

// Encode writes standard PNG or quality-95 JPEG, with no browser or native library dependency.
func Encode(writer io.Writer, source *image.NRGBA, format string) error {
	if err := validateImage(source); err != nil {
		return err
	}
	switch format {
	case "png":
		encodedPixels, err := CanvasPixels(source)
		if err != nil {
			return err
		}
		encoder := png.Encoder{CompressionLevel: png.BestSpeed}
		return encoder.Encode(writer, encodedPixels)
	case "jpeg":
		flattened, err := FlattenWhite(source)
		if err != nil {
			return err
		}
		return jpeg.Encode(writer, flattened, &jpeg.Options{Quality: 95})
	default:
		return errors.New("输出格式无效")
	}
}

// CanvasPixels matches an 8-bit sRGB Canvas premultiply/readback round trip before PNG encoding.
// Opaque images are borrowed; translucent images receive an owned buffer so templates remain immutable.
func CanvasPixels(source *image.NRGBA) (*image.NRGBA, error) {
	if err := validateImage(source); err != nil {
		return nil, err
	}
	return canvaspix.ForPNG(source), nil
}
