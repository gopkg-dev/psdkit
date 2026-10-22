// Package psdmodel defines decoded document data shared by PSD readers and renderers.
// It represents an RGB8 rendering snapshot; lossless format data lives in psdfile.
package psdmodel

import (
	"image"

	"github.com/gopkg-dev/psdkit/internal/psdrendered"
	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// Document contains decoded PSD data. Layers are ordered bottom to top.
// Callers must treat a document and its pixel buffers as read-only while a renderer uses them.
// Original resources, encoded channels and embedded files belong to the separate source document.
type Document struct {
	Width, Height            int
	Layers                   []*Layer
	Composite                *image.NRGBA
	Slices                   []Slice
	LinkedNames              map[string]string
	Warnings, BlockingIssues []string
}

// Layer keeps saved pixels and absolute document placement separate.
// Group distinguishes an empty folder from a leaf without pixels.
// A nil BlendClipped or TransparencyShapes uses Photoshop's true default.
// Nil BlendInterior, MaskAsGlobal and VectorMaskAsGlobal preserve the absent flag state.
// ChannelRestrictions retains original restricted channel IDs; RGB uses 0, 1 and 2.
// Background identifies the bottom source raster without a transparency channel, independently of its name.
type Layer struct {
	Name                 string
	Bounds               image.Rectangle
	Hidden               bool
	Group                bool
	Pixels               *image.NRGBA
	Opacity, FillOpacity *float64
	BlendMode            string
	BlendRanges          []BlendRange
	BlendClipped         *bool
	BlendInterior        *bool
	MaskAsGlobal         *bool
	VectorMaskAsGlobal   *bool
	TransparencyShapes   *bool
	Knockout             Knockout
	ChannelRestrictions  []uint32
	Background           bool
	Clipping             bool
	Mask                 *Mask
	VectorMask           *psdvector.Mask
	VectorDensity        *uint8
	VectorFeather        *float64
	FillColor            *[3]float64
	Adjustment           *psdadjust.Adjustment
	Gradient             *psdgradient.Fill
	Pattern              *psdpattern.Fill
	PatternTile          *image.NRGBA
	Effects              *psdeffects.Set
	Placed               *Placed
	// RenderedEffects binds a freshly projected placed-layer raster for effect composition.
	// It is runtime-only and invalidated by replacing Pixels, Bounds or Placed.
	RenderedEffects *psdrendered.Raster
	Children        []*Layer
	Unsupported     []string
}

// Knockout selects the backdrop revealed through the layer's shape before Fill is applied.
type Knockout uint8

const (
	// KnockoutNone leaves underlying layers intact.
	KnockoutNone Knockout = iota
	// KnockoutShallow reveals the enclosing group or clipping boundary.
	KnockoutShallow
	// KnockoutDeep reveals the document Background across pass-through groups.
	KnockoutDeep
)

// BlendSlider contains the split black and white endpoints of one Blend If slider.
// BlackLow/BlackHigh and WhiteLow/WhiteHigh delimit each transition in the 0 to 255 range.
type BlendSlider struct {
	BlackLow, BlackHigh, WhiteLow, WhiteHigh uint8
}

// BlendRange pairs the source and underlying-color sliders for one Blend If component.
// Layer.BlendRanges starts with composite gray, followed by red, green and blue in RGB documents.
// A missing range uses the full default interval {0, 0, 255, 255} for both sliders.
type BlendRange struct {
	Source, Destination BlendSlider
}

// Mask uses the first pixel channel as coverage; a nil default means white.
// Bounds are stored document coordinates; Relative retains the source relative-position flag without shifting Bounds.
// Density defaults to 255 and Feather defaults to zero; nonzero feather uses a bounded coverage cache.
type Mask struct {
	Bounds image.Rectangle
	// SampleBounds locates a decoded pixel window while Bounds retains the original mask geometry.
	// A nil SampleBounds uses Bounds for both geometry and pixel sampling.
	SampleBounds *image.Rectangle
	Pixels       *image.NRGBA
	Disabled     bool
	Relative     bool
	Invert       bool
	Density      *uint8
	Feather      *float64
	DefaultColor *uint8
	Unsupported  []string
}

// Placed describes one smart-object instance and its saved transform metadata.
type Placed struct {
	ID, Type                      string
	Width, Height                 int
	Transform, NonAffineTransform []float64
	Warp                          *Warp
	Unsupported                   []string
}

// Point is a double-precision canvas coordinate.
type Point struct{ X, Y float64 }

// WarpBounds preserves the coordinate units of all four descriptor edges.
type WarpBounds struct {
	Left, Top, Right, Bottom float64
	Pixels                   bool
	// Units retains the original edge units in left, top, right, bottom order; doubles have empty units.
	Units [4]string
}

// WarpSlice preserves one original quilt object-array column without normalizing its source coordinates.
// Axis is quiltSliceX or quiltSliceY; Location is warp or envelope. Repeated columns remain separate entries.
type WarpSlice struct {
	Axis, Location, ClassID, Field, Kind, Unit string
	Prefix                                     uint32
	Values                                     []float64
}

// Warp preserves native preset parameters and custom-envelope geometry without inventing absent values.
// Split must be set for any quilt slices or deformNumRows/deformNumCols descriptor.
type Warp struct {
	ClassID       string
	Style, Rotate string
	// StyleEnum and RotateEnum retain their original enum category and value.
	StyleEnum, RotateEnum                string
	Value, Perspective, PerspectiveOther float64
	Values                               []float64
	HasValue, HasValues                  bool
	UOrder, VOrder                       int
	// DeformRows and DeformColumns retain the native control-point counts, independently of the cubic orders.
	DeformRows, DeformColumns       int
	HasDeformRows, HasDeformColumns bool
	QuiltSlices                     []WarpSlice
	Bounds                          *WarpBounds
	HasEnvelope, Split              bool
	MeshPoints                      []Point
	// MeshUnits retains horizontal and vertical object-array units.
	MeshUnits [2]string
	MeshClass string
	// Fields retains source order, including duplicate keys; raw descriptor bytes remain in the source document.
	Fields, Kinds []string
	Unsupported   []string
}

// Slice is a document crop. Empty Origin and Type represent a validated image slice.
type Slice struct {
	Name   string `json:"name"`
	Left   int    `json:"left"`
	Top    int    `json:"top"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Origin string `json:"-"`
	Type   string `json:"-"`
}
