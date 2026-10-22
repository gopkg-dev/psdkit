package psdkit

import (
	"context"
	"fmt"
	"image"

	"github.com/gopkg-dev/psdkit/psdcolor"
)

// RegionDisplayOptions controls color context and compressed-source work for a saved-image region.
type RegionDisplayOptions struct {
	Display DisplayOptions
	Decode  RegionOptions
}

// DecodeDisplayRegion returns an owned sRGB8 display tile at absolute saved-canvas coordinates.
// Raw/RLE reads selected pixels; ZIP still validates its complete shared compressed stream.
// It displays stored merged samples and does not reconstruct document layers or alter source precision.
func DecodeDisplayRegion(ctx context.Context, source *SourceDocument, rectangle image.Rectangle,
	options ...RegionDisplayOptions,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil || len(options) > 1 {
		return nil, fmt.Errorf("%w: source and at most one region display option are required", psdcolor.ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if rectangle.Min.X < 0 || rectangle.Min.Y < 0 || rectangle.Max.X < rectangle.Min.X ||
		rectangle.Max.Y < rectangle.Min.Y || rectangle.Max.X > int(source.Header.Width) ||
		rectangle.Max.Y > int(source.Header.Height) {
		return nil, fmt.Errorf("%w: display region must be inside the saved canvas", psdcolor.ErrInvalid)
	}
	var option RegionDisplayOptions
	if len(options) == 1 {
		option = options[0]
	}
	configuration := option.Display.Color
	if configuration.MaxPixels < 0 || configuration.MaxOutputBytes < 0 || configuration.MaxProfileBytes < 0 {
		return nil, fmt.Errorf("%w: display budgets must not be negative", psdcolor.ErrInvalid)
	}
	limits := source.Limits()
	if configuration.MaxPixels == 0 {
		configuration.MaxPixels = limits.MaxDecodePixels
	}
	if configuration.MaxOutputBytes == 0 {
		configuration.MaxOutputBytes = limits.MaxDecodeBytes
	}
	width, height := int64(rectangle.Dx()), int64(rectangle.Dy())
	area, rowBytes := width*height, (width*int64(source.Header.Depth)+7)/8
	sampleBytes := rowBytes * height * int64(source.Header.Channels)
	workingLimit := limits.MaxDecodeBytes
	if option.Decode.MaxWorkingBytes < 0 {
		return nil, fmt.Errorf("%w: decode budget must not be negative", psdcolor.ErrInvalid)
	}
	if option.Decode.MaxWorkingBytes > 0 {
		workingLimit = min(workingLimit, option.Decode.MaxWorkingBytes)
	}
	if area > configuration.MaxPixels || area > configuration.MaxOutputBytes/4 ||
		area > workingLimit/4 || sampleBytes > workingLimit-area*4 {
		return nil, fmt.Errorf("%w: region samples and display tile exceed budget", psdcolor.ErrLimit)
	}
	configuration, err := displayMetadata(ctx, source, option.Display, configuration)
	if err != nil {
		return nil, err
	}
	if err := validateDisplayMetadata(ctx, source, configuration); err != nil {
		return nil, err
	}
	samples := &CompositePixels{Width: int(width), Height: int(height), Depth: source.Header.Depth,
		ColorMode: source.Header.ColorMode, Channels: make([]Plane, int(source.Header.Channels))}
	retained := area * 4
	for index := range samples.Channels {
		decodeOptions := option.Decode
		decodeOptions.MaxWorkingBytes = workingLimit - retained
		if decodeOptions.MaxWorkingBytes <= 0 && sampleBytes != 0 {
			return nil, fmt.Errorf("%w: no working storage remains for region samples", psdcolor.ErrLimit)
		}
		plane, err := source.DecodeCompositeChannelRegion(ctx, index, rectangle, decodeOptions)
		if err != nil {
			return nil, err
		}
		samples.Channels[index] = *plane
		retained += int64(cap(plane.Data))
	}
	result, err := psdcolor.Convert(ctx, samples, configuration)
	if err != nil {
		return nil, err
	}
	result.Rect = rectangle
	return result, nil
}
