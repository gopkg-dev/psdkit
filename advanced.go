package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// VectorMask contains owned Photoshop path records and editable document-space geometry.
type VectorMask = psdvector.Mask

// VectorOptions bounds approximate filled-path rasterization.
type VectorOptions = psdvector.Options

// VectorEncodeOptions bounds owned path encoding.
type VectorEncodeOptions = psdvector.EncodeOptions

// Adjustment contains typed RGB8 tonal controls and retained original encoding.
type Adjustment = psdadjust.Adjustment

// AdjustmentKind identifies a tonal adjustment family.
type AdjustmentKind = psdadjust.Kind

// PhotoFilterParams retains source-native filter color words, density and luminosity policy.
type PhotoFilterParams = psdadjust.PhotoFilterParams

// ChannelMixerParams contains signed RGB channel coefficients and constants.
type ChannelMixerParams = psdadjust.ChannelMixerParams

// MixerChannel stores one output channel's coefficients and constant.
type MixerChannel = psdadjust.MixerChannel

// ColorBalanceParams retains uncalibrated nonneutral color-balance controls.
type ColorBalanceParams = psdadjust.ColorBalanceParams

// HueSaturationParams retains master, Colorize and six ordered circular color-range controls.
type HueSaturationParams = psdadjust.HueSaturationParams

// HueSaturationValues contains signed hue, saturation and lightness controls.
type HueSaturationValues = psdadjust.HueSaturationValues

// HueSaturationRange retains one circular color range and its signed adjustment.
type HueSaturationRange = psdadjust.HueSaturationRange

// SmartFilter contains one typed live filter operation.
type SmartFilter = psdfilter.Filter

// SmartFilterKind identifies a live filter algorithm.
type SmartFilterKind = psdfilter.Kind

// SmartFilterGaussianBlur selects the explicitly approximate integrated Gaussian algorithm.
const SmartFilterGaussianBlur = psdfilter.GaussianBlur

// SmartFilterSurfaceBlur selects the explicitly approximate edge-preserving surface blur.
const SmartFilterSurfaceBlur = psdfilter.SurfaceBlur

// SmartFilterSmartSharpen selects explicitly approximate sharpening with noise reduction.
const SmartFilterSmartSharpen = psdfilter.SmartSharpen

// SmartFilterBrightnessContrast selects explicitly approximate encoded-RGB tonal adjustment.
const SmartFilterBrightnessContrast = psdfilter.BrightnessContrast

// SmartFilterHueSaturation selects the explicit hue and circular color-range adjustment policy.
const SmartFilterHueSaturation = psdfilter.HueSaturation

// SmartFilterPipeline retains a filter execution order and its whole-stack switch.
type SmartFilterPipeline = psdfilter.Stack

// SmartFilterOptions selects algorithms, explicit approximation and working limits.
type SmartFilterOptions = psdfilter.Options

// SmartFilterEstimate reports retained output and peak additional filter storage.
type SmartFilterEstimate = psdfilter.Estimate

// SmartFilterStack retains typed original placement descriptors and filter-mask controls.
type SmartFilterStack = psdfile.SmartFilterStack

// SmartFilterRecord retains one original filter descriptor and its decoded live operation.
type SmartFilterRecord = psdfile.SmartFilterRecord

// DecodeSmartFilters decodes an owned placement's filter controls without reading raster caches.
func DecodeSmartFilters(ctx context.Context, placement SmartObjectRecord,
	options ...MetadataOptions,
) (*SmartFilterStack, error) {
	return psdfile.DecodeSmartFilters(ctx, placement, options...)
}

// ApplySmartFilters executes a bounded pipeline and returns owned pixels.
func ApplySmartFilters(ctx context.Context, source *image.NRGBA, stack SmartFilterPipeline,
	options ...SmartFilterOptions,
) (*image.NRGBA, error) {
	return psdfilter.Apply(ctx, source, stack, options...)
}

// EstimateSmartFilters validates a pipeline and estimates peak additional storage before allocation.
func EstimateSmartFilters(source *image.NRGBA, stack SmartFilterPipeline,
	options ...SmartFilterOptions,
) (SmartFilterEstimate, error) {
	return psdfilter.WorkingEstimate(source, stack, options...)
}

// GradientFill contains live classic RGB gradient controls in source encoded color space.
type GradientFill = psdgradient.Fill

// EffectSet contains ordered modern layer effects and their retained original descriptors.
type EffectSet = psdeffects.Set

// Effect contains one typed layer-effect instance and retained descriptor fields.
type Effect = psdeffects.Effect

// EffectKind identifies an implemented or retained effect family.
type EffectKind = psdeffects.Kind

// GlowSource selects inner-glow edge or center coverage.
type GlowSource = psdeffects.GlowSource

const (
	// EffectInnerShadow selects an inner shadow effect.
	EffectInnerShadow = psdeffects.InnerShadow
	// EffectInnerGlow selects an inner glow effect.
	EffectInnerGlow = psdeffects.InnerGlow
	// GlowSourceEdge paints inner coverage approaching the source edges.
	GlowSourceEdge = psdeffects.Edge
	// GlowSourceCenter paints the complementary inner-glow source.
	GlowSourceCenter = psdeffects.CenterSource
)

// PatternFill contains a local pattern reference and supported periodic sampling controls.
type PatternFill = psdpattern.Fill

// PatternOptions bounds owned pattern rendering in absolute canvas coordinates.
type PatternOptions = psdpattern.Options

// ArtboardRecord associates retained artboard metadata with its source physical layer.
type ArtboardRecord = psdfile.ArtboardRecord

// ArtboardData owns ordered artboard descriptors without promising clipping or background rendering.
type ArtboardData = psdfile.ArtboardData

// RenderPattern samples an explicitly supplied RGB8 tile without preset or filesystem lookups.
func RenderPattern(ctx context.Context, tile *image.NRGBA, fill PatternFill, bounds image.Rectangle,
	options ...PatternOptions,
) (*image.NRGBA, error) {
	return psdpattern.Render(ctx, tile, fill, bounds, options...)
}

const (
	// AdjustmentInvert complements encoded RGB channels.
	AdjustmentInvert = psdadjust.Invert
	// AdjustmentThreshold selects black or white by luminance.
	AdjustmentThreshold = psdadjust.Threshold
	// AdjustmentPosterize quantizes color channels to selected levels.
	AdjustmentPosterize = psdadjust.Posterize
	// AdjustmentLevels adjusts input/output ranges and gamma.
	AdjustmentLevels = psdadjust.Levels
	// AdjustmentCurves applies channel and composite tonal curves.
	AdjustmentCurves = psdadjust.Curves
	// AdjustmentExposure applies native-verified RGB8 exposure controls.
	AdjustmentExposure = psdadjust.Exposure
	// AdjustmentChannelMixer applies a calibrated RGB8 channel matrix and constants.
	AdjustmentChannelMixer = psdadjust.ChannelMixer
	// AdjustmentColorBalance identifies retained color-balance metadata with neutral-only rendering.
	AdjustmentColorBalance = psdadjust.ColorBalance
	// AdjustmentHueSaturation selects RGB8 hue, saturation and lightness controls.
	AdjustmentHueSaturation = psdadjust.HueSaturation
	// AdjustmentPhotoFilter selects typed filter metadata; only zero-density RGB8 rendering is verified.
	AdjustmentPhotoFilter = psdadjust.PhotoFilter
)

// RasterizeVectorMask returns owned approximate filled-path coverage for absolute canvas bounds.
func RasterizeVectorMask(ctx context.Context, mask *VectorMask, bounds image.Rectangle,
	options ...VectorOptions,
) (*image.Alpha, error) {
	return psdvector.Rasterize(ctx, mask, bounds, options...)
}

// EncodeVectorMask returns an owned vmsk/vsms payload containing the mask's current typed fields.
func EncodeVectorMask(ctx context.Context, mask *VectorMask, options ...VectorEncodeOptions) ([]byte, error) {
	return psdvector.EncodeMask(ctx, mask, options...)
}
