package psdkit

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/gopkg-dev/psdkit/psdcolor"
)

func displayMetadata(ctx context.Context, source *SourceDocument, option DisplayOptions,
	configuration ColorOptions,
) (ColorOptions, error) {
	maxProfile := configuration.MaxProfileBytes
	if maxProfile == 0 {
		maxProfile = 4 << 20
		configuration.MaxProfileBytes = maxProfile
	}
	if configuration.ICCProfile == nil && !option.IgnoreSourceProfile {
		for _, resource := range slices.Backward(source.Resources) {

			if resource.ID != 1039 {
				continue
			}
			if resource.Data.Length > maxProfile {
				return ColorOptions{}, fmt.Errorf("%w: source ICC profile exceeds budget", psdcolor.ErrLimit)
			}
			profile, err := source.ReadSpan(resource.Data)
			if err != nil {
				return ColorOptions{}, err
			}
			configuration.ICCProfile = profile
			break
		}
	}
	if source.Header.ColorMode == 2 && configuration.Palette == nil {
		span := source.ColorModeData
		if span.Length < 4 {
			return ColorOptions{}, fmt.Errorf("%w: indexed source palette is absent", psdcolor.ErrInvalid)
		}
		span.Offset, span.Length = span.Offset+4, span.Length-4
		palette, err := source.ReadSpan(span)
		if err != nil {
			return ColorOptions{}, err
		}
		configuration.Palette = palette
	}
	if !option.IgnoreTransparency {
		if source.Header.ColorMode == 2 && configuration.TransparencyIndex == nil {
			for _, resource := range source.Resources {
				if resource.ID != 1047 || resource.Data.Length < 2 {
					continue
				}
				data, err := source.ReadSpan(RawSpan{Offset: resource.Data.Offset, Length: 2})
				if err != nil {
					return ColorOptions{}, err
				}
				if value := binary.BigEndian.Uint16(data); value < 256 {
					index := uint8(value)
					configuration.TransparencyIndex = &index
				}
			}
		}
		mergedTransparency := source.GlobalAlpha
		for _, block := range source.AdditionalInfo {
			mergedTransparency = mergedTransparency ||
				(source.Header.Depth == 8 && block.Key == "Mtrn") ||
				(source.Header.Depth == 16 && block.Key == "Mt16") ||
				(source.Header.Depth == 32 && block.Key == "Mt32")
		}
		if mergedTransparency && configuration.Alpha == psdcolor.AlphaNone {
			configuration.Alpha = psdcolor.AlphaWhiteMatte
		}
	}
	if err := ctx.Err(); err != nil {
		return ColorOptions{}, err
	}
	return configuration, nil
}

func validateDisplayMetadata(ctx context.Context, source *SourceDocument, configuration ColorOptions) error {
	metadata := &CompositePixels{Depth: source.Header.Depth, ColorMode: source.Header.ColorMode,
		Channels: make([]Plane, int(source.Header.Channels))}
	for index := range metadata.Channels {
		metadata.Channels[index] = Plane{ID: int16(index), Depth: source.Header.Depth}
	}
	_, err := psdcolor.Convert(ctx, metadata, configuration)
	return err
}
