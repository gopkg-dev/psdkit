// Command color exports source-precision saved previews or individual original layer planes as sRGB8 PNG.
package main

import (
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdcolor"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	input := flag.String("psd", "", "PSD/PSB source")
	output := flag.String("out", "output/display.png", "owned sRGB8 PNG destination")
	layer := flag.Int("layer", -1, "optional physical source layer index; skips saved white-matte preview")
	unmanaged := flag.Bool("unmanaged", false, "explicitly allow approximate CMYK when no ICC exists")
	ignoreProfile := flag.Bool("ignore-profile", false, "explicitly disregard an embedded source profile")
	clampHDR := flag.Bool("clamp-hdr", false, "explicitly clip finite HDR colors to the display range")
	intent := flag.String("intent", "relative", "ICC intent: relative, perceptual or saturation")
	flag.Parse()
	if *input == "" {
		return fmt.Errorf("-psd is required; generate an input with examples/create or supply your PSD/PSB")
	}
	source, err := psdkit.OpenDocument(*input)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	settings := psdkit.DisplayOptions{IgnoreSourceProfile: *ignoreProfile,
		Color: psdcolor.Options{AllowUnmanagedCMYK: *unmanaged}}
	switch *intent {
	case "relative":
	case "perceptual":
		settings.Color.Intent = psdcolor.IntentPerceptual
	case "saturation":
		settings.Color.Intent = psdcolor.IntentSaturation
	default:
		return fmt.Errorf("%w: unknown ICC intent %q", psdkit.ErrColorInvalid, *intent)
	}
	if *clampHDR {
		settings.Color.ToneMap = psdcolor.ToneMapClamp
	}
	var display *image.NRGBA
	if *layer < 0 {
		display, err = psdkit.DecodeDisplayComposite(context.Background(), source, settings)
	} else {
		display, err = decodeLayerDisplay(context.Background(), source, *layer, settings)
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		return err
	}
	file, err := os.Create(*output)
	if err != nil {
		return err
	}
	if err := png.Encode(file, display); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	fmt.Printf("%s: %dx%d sRGB display; source mode=%d depth=%d\n",
		*output, display.Rect.Dx(), display.Rect.Dy(), source.Header.ColorMode, source.Header.Depth)
	return nil
}

func decodeLayerDisplay(
	ctx context.Context, source *psdkit.SourceDocument, index int, settings psdkit.DisplayOptions,
) (*image.NRGBA, error) {
	configuration := settings.Color
	if configuration.ICCProfile == nil && !settings.IgnoreSourceProfile {
		for _, resource := range slices.Backward(source.Resources) {

			if resource.ID != 1039 {
				continue
			}
			profile, err := source.ReadSpan(resource.Data)
			if err != nil {
				return nil, err
			}
			configuration.ICCProfile = profile
			break
		}
	}
	if source.Header.ColorMode == 2 {
		span := source.ColorModeData
		if span.Length < 4 {
			return nil, fmt.Errorf("%w: source palette is missing", psdkit.ErrColorInvalid)
		}
		span.Offset, span.Length = span.Offset+4, span.Length-4
		palette, err := source.ReadSpan(span)
		if err != nil {
			return nil, err
		}
		configuration.Palette = palette
		for _, resource := range source.Resources {
			if resource.ID != 1047 || resource.Data.Length != 2 {
				continue
			}
			data, err := source.ReadSpan(resource.Data)
			if err != nil {
				return nil, err
			}
			if value := binary.BigEndian.Uint16(data); value < 256 {
				transparent := uint8(value)
				configuration.TransparencyIndex = &transparent
			}
		}
	}
	layer, err := source.DecodeLayer(ctx, index)
	if err != nil {
		return nil, err
	}
	components := 3
	switch source.Header.ColorMode {
	case 0, 1, 2:
		components = 1
	case 4:
		components = 4
	}
	planes := make([]psdkit.Plane, components)
	var alpha *psdkit.Plane
	for channel := range layer.Channels {
		plane := &layer.Channels[channel]
		if plane.ID >= 0 && int(plane.ID) < components {
			planes[plane.ID] = *plane
		}
		if plane.ID == -1 {
			alpha = plane
		}
	}
	configuration.Alpha = psdcolor.AlphaNone
	if alpha != nil {
		planes = append(planes, *alpha)
		configuration.Alpha = psdcolor.AlphaStraight
		configuration.AlphaChannel = components
	}
	pixels := &psdkit.CompositePixels{Width: layer.Bounds.Dx(), Height: layer.Bounds.Dy(),
		Depth: planes[0].Depth, ColorMode: source.Header.ColorMode, Channels: planes}
	return psdkit.ConvertSamples(ctx, pixels, configuration)
}
