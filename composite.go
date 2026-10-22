package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

// CompositeOptions bounds RGB8 layer-tree reconstruction and its working allocations.
type CompositeOptions = psdcompose.Options

// KnockoutMode selects the backdrop revealed by a layer's shape before Fill.
type KnockoutMode = psdmodel.Knockout

const (
	// KnockoutNone preserves ordinary source-over behavior.
	KnockoutNone = psdmodel.KnockoutNone
	// KnockoutShallow stops at the enclosing group or clipping base.
	KnockoutShallow = psdmodel.KnockoutShallow
	// KnockoutDeep reveals the document Background across pass-through groups.
	KnockoutDeep = psdmodel.KnockoutDeep
)

var (
	// ErrCompositeUnsupported identifies features not supported by layer-tree reconstruction.
	ErrCompositeUnsupported = psdcompose.ErrUnsupported
	// ErrCompositeInvalid identifies invalid decoded geometry, pixels or layer-tree data.
	ErrCompositeInvalid = psdcompose.ErrInvalid
	// ErrCompositeLimit identifies exceeded layer-tree or working-memory limits.
	ErrCompositeLimit = psdcompose.ErrLimit
)

// Composite rebuilds an owned RGB8 image from layers, independent of the saved preview.
// It borrows an immutable decoded document and never modifies source buffers.
func Composite(ctx context.Context, document *Document, options ...CompositeOptions) (*image.NRGBA, error) {
	return psdcompose.Render(ctx, document, options...)
}

// CompositeRegion rebuilds an owned absolute-coordinate RGB8 window from an immutable layer tree.
func CompositeRegion(ctx context.Context, document *Document, bounds image.Rectangle,
	options ...CompositeOptions,
) (*image.NRGBA, error) {
	return psdcompose.RenderRegion(ctx, document, bounds, options...)
}
