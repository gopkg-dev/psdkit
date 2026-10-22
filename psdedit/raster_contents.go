package psdedit

import (
	"context"
	"errors"
	"image"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// RasterContentsOptions controls an editable, single-layer RGB8 PSD/PSB prototype.
// Zero budgets select DefaultLimits; zero Version selects PSD and empty LayerName selects "Image".
// ICCProfile explicitly declares the input sample encoding and is preserved in resource 1039.
// Only validated RGB matrix/TRC profiles are accepted. Empty ICCProfile produces an untagged document.
// MaxWorkingBytes bounds conservative simultaneous internal storage, including encoder capacities and final output.
type RasterContentsOptions struct {
	Version                                     uint16
	LayerName                                   string
	ICCProfile                                  []byte
	MaxPixels, MaxWorkingBytes, MaxEncodedBytes int64
}

// BuildRasterContents returns owned PSD/PSB bytes suitable for ReplaceContents or an explicit linked resolver.
// It retains the complete input canvas, straight-alpha layer samples and RGB values underneath zero alpha.
// Input offsets are normalized to canvas origin; row padding is ignored and input bytes remain untouched.
// Raw compression keeps allocation bounds predictable. Profile declaration does not convert the input pixels.
func BuildRasterContents(ctx context.Context, input *image.NRGBA, options ...RasterContentsOptions) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	configuration, width, height, peak, outputCapacity, err := rasterContentsPreflight(ctx, input, options)
	if err != nil {
		return nil, err
	}
	if peak > configuration.MaxWorkingBytes {
		return nil, editError(psdfile.ErrLimit, "rasterContents.working", "internal storage exceeds MaxWorkingBytes")
	}
	limits := psdfile.DefaultLimits()
	limits.MaxDecodePixels = min(limits.MaxDecodePixels, configuration.MaxPixels)
	limits.MaxDecodeBytes = min(limits.MaxDecodeBytes, configuration.MaxWorkingBytes)
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, configuration.MaxWorkingBytes)
	limits.MaxFileBytes = min(limits.MaxFileBytes, configuration.MaxEncodedBytes)
	editor, err := Create(ctx, width, height, CreateOptions{
		Version: configuration.Version, Limits: limits, ICCProfile: configuration.ICCProfile,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = editor.Close() }()
	if err := editor.ReplacePixels(1, input); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := editor.Rename(1, configuration.LayerName); err != nil {
		return nil, err
	}
	output := &rasterContentsBuffer{ctx: ctx, capacity: outputCapacity}
	if err := editor.Write(ctx, output, SaveOptions{MaxOutputBytes: configuration.MaxEncodedBytes}); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return output.data[:output.position:output.position], nil
}

// RasterContentsWorkingBytes reports BuildRasterContents' conservative peak reservation without copying pixels.
// It validates shape, options and the ICC envelope; successful estimation does not prove supported ICC curves.
// MaxWorkingBytes is enforced by BuildRasterContents, allowing this estimator to size a caller's chosen budget.
func RasterContentsWorkingBytes(ctx context.Context, input *image.NRGBA,
	options ...RasterContentsOptions,
) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, _, _, peak, _, err := rasterContentsPreflight(ctx, input, options)
	return peak, err
}

func rasterContentsPreflight(ctx context.Context, input *image.NRGBA, options []RasterContentsOptions,
) (RasterContentsOptions, int, int, int64, int64, error) {
	var configuration RasterContentsOptions
	if err := ctx.Err(); err != nil {
		return configuration, 0, 0, 0, 0, err
	}
	if len(options) > 1 {
		return configuration, 0, 0, 0, 0, editError(ErrInvalidEdit, "rasterContents.options", "too many options values")
	}
	if len(options) == 1 {
		configuration = options[0]
	}
	if configuration.Version == 0 {
		configuration.Version = 1
	}
	if configuration.Version != 1 && configuration.Version != 2 {
		return configuration, 0, 0, 0, 0, editError(ErrInvalidEdit, "rasterContents.version", "version must be PSD or PSB")
	}
	if configuration.MaxPixels < 0 || configuration.MaxWorkingBytes < 0 || configuration.MaxEncodedBytes < 0 {
		return configuration, 0, 0, 0, 0, editError(psdfile.ErrLimit, "rasterContents.options", "negative budget")
	}
	defaults := psdfile.DefaultLimits()
	if configuration.MaxPixels == 0 {
		configuration.MaxPixels = defaults.MaxDecodePixels
	}
	if configuration.MaxWorkingBytes == 0 {
		configuration.MaxWorkingBytes = defaults.MaxDecodeBytes
	}
	if configuration.MaxEncodedBytes == 0 {
		configuration.MaxEncodedBytes = defaults.MaxFileBytes
	}
	if configuration.LayerName == "" {
		configuration.LayerName = "Image"
	}
	if !utf8.ValidString(configuration.LayerName) || strings.IndexByte(configuration.LayerName, 0) >= 0 {
		return configuration, 0, 0, 0, 0, editError(ErrInvalidEdit, "rasterContents.name", "invalid layer name")
	}
	if input == nil || input.Rect.Max.X <= input.Rect.Min.X || input.Rect.Max.Y <= input.Rect.Min.Y {
		return configuration, 0, 0, 0, 0, editError(ErrInvalidEdit, "rasterContents.image", "positive image required")
	}
	for _, coordinate := range []int{input.Rect.Min.X, input.Rect.Min.Y, input.Rect.Max.X, input.Rect.Max.Y} {
		if int64(coordinate) < math.MinInt32 || int64(coordinate) > math.MaxInt32 {
			return configuration, 0, 0, 0, 0, editError(ErrInvalidEdit, "rasterContents.image", "invalid image rectangle")
		}
	}
	width, height := input.Rect.Dx(), input.Rect.Dy()
	if width > psdmodel.MaxDimension || height > psdmodel.MaxDimension ||
		int64(width) > min(configuration.MaxPixels, int64(psdmodel.MaxPixels))/int64(height) {
		return configuration, 0, 0, 0, 0, editError(psdfile.ErrLimit, "rasterContents.image", "pixel budget exceeded")
	}
	if err := compositeImageShape(input, width, height); err != nil {
		return configuration, 0, 0, 0, 0, err
	}
	area, profileLength, nameLength := int64(width)*int64(height), int64(len(configuration.ICCProfile)),
		int64(len(configuration.LayerName))
	if nameLength > defaults.MaxMetadataBytes/4 {
		return configuration, 0, 0, 0, 0, editError(psdfile.ErrLimit, "rasterContents.name", "name budget exceeded")
	}
	profileCost, err := rasterProfileWorkingBytes(ctx, configuration.ICCProfile)
	if err != nil {
		return configuration, 0, 0, 0, 0, err
	}
	// Covers encoder growth with old/new capacities, planar samples, snapshots, writer plans and final byte storage.
	// The fixed allowance includes CopySpan's 64 KiB scratch and the bounded one-layer metadata structures.
	peak := max(40*area+(128<<10)+16*nameLength+2*profileLength, profileCost+(8<<10))
	capacity := min(8*area+1024+4*nameLength+profileLength, configuration.MaxEncodedBytes)
	if capacity > math.MaxInt {
		return configuration, 0, 0, 0, 0, editError(psdfile.ErrLimit, "rasterContents.output", "output size overflows")
	}
	return configuration, width, height, peak, capacity, nil
}

func rasterProfileWorkingBytes(ctx context.Context, profile []byte) (int64, error) {
	if len(profile) == 0 {
		return 0, nil
	}
	dummy := &image.NRGBA{Pix: []byte{0, 0, 0, 255}, Stride: 4, Rect: image.Rect(0, 0, 1, 1)}
	amount, err := psdcolor.ProfileTransformWorkingBytes(ctx, dummy, profile, profile,
		psdcolor.ProfileTransformOptions{MaxProfileBytes: 4 << 20, MaxWorkingBytes: math.MaxInt64})
	return amount + 256, rasterProfileError(err)
}

func validateRasterProfile(ctx context.Context, profile []byte, budget int64) error {
	if len(profile) == 0 {
		return nil
	}
	amount, err := rasterProfileWorkingBytes(ctx, profile)
	if err != nil {
		return err
	}
	if amount > budget || budget <= 256 {
		return editError(psdfile.ErrLimit, "create.profile", "ICC validation exceeds the working budget")
	}
	dummy := &image.NRGBA{Pix: []byte{0, 0, 0, 255}, Stride: 4, Rect: image.Rect(0, 0, 1, 1)}
	_, err = psdcolor.TransformProfile(ctx, dummy, profile, profile,
		psdcolor.ProfileTransformOptions{MaxProfileBytes: 4 << 20, MaxWorkingBytes: budget - 256})
	return rasterProfileError(err)
}

func rasterProfileError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := ErrInvalidEdit
	if errors.Is(err, psdcolor.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdcolor.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return errors.Join(editError(kind, "create.profile", "unsupported or invalid RGB matrix/TRC profile"), err)
}

type rasterContentsBuffer struct {
	ctx      context.Context
	data     []byte
	position int
	capacity int64
}

func (output *rasterContentsBuffer) Write(data []byte) (int, error) {
	if err := output.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > output.capacity-int64(output.position) {
		return 0, editError(psdfile.ErrLimit, "rasterContents.output", "encoded output exceeds its fixed capacity")
	}
	if output.data == nil {
		// Allocate only after the complete write plan exists, never during its encoder growth.
		output.data = make([]byte, int(output.capacity))
	}
	copy(output.data[output.position:], data)
	output.position += len(data)
	return len(data), nil
}
