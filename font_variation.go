package psdkit

import "github.com/gopkg-dev/psdkit/psdfont"

// FontVariationCoordinate selects one explicitly requested user-space font axis value.
type FontVariationCoordinate = psdfont.VariationCoordinate

// FontVariationAxis retains one source fvar axis and its original range.
type FontVariationAxis = psdfont.VariationAxis

// FontVariationInstanceCoordinate records the selected user and F2DOT14 normalized coordinates.
type FontVariationInstanceCoordinate = psdfont.VariationInstanceCoordinate

// FontNamedVariationInstance owns source name IDs and coordinates for a named fvar instance.
type FontNamedVariationInstance = psdfont.NamedVariationInstance

// FontInstanceMetrics retains unrounded instance glyph metrics independently of raw hmtx values.
type FontInstanceMetrics = psdfont.InstanceGlyphMetrics

// FontInstancePosition retains precise positioning before the integer design-unit output profile.
type FontInstancePosition = psdfont.InstancePosition

// FontVariationMetric retains a source MVAR record and its precise selected-instance delta.
type FontVariationMetric = psdfont.VariationMetric
