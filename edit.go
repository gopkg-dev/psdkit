package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdedit"
)

// Editor applies controlled overlays to an immutable source and writes PSD/PSB files.
// Blend, clipping, Blend If, mask and advanced blending edits preserve the same source ownership.
type Editor = psdedit.Editor

// LayerID is a stable editing handle, independent of the physical record index and PSD lyid.
type LayerID = psdedit.LayerID

// EditLayerInfo is an owned layer-tree view with bottom-to-top sibling order.
type EditLayerInfo = psdedit.LayerInfo

// AdvancedBlendingOptions selects optional advanced blending metadata changes.
type AdvancedBlendingOptions = psdedit.AdvancedBlendingOptions

// PixelMaskSettings describes editable properties of an existing pixel mask.
type PixelMaskSettings = psdedit.PixelMaskSettings

// SmartObjectInfo reports a placed layer's shared source and editable handle.
type SmartObjectInfo = psdedit.SmartObjectInfo

// SmartContentsOptions selects explicit source-color and custom-Warp policies for content replacement.
type SmartContentsOptions = psdedit.SmartContentsOptions

// SmartTransformOptions selects explicit source-color and projection policies for one placed instance.
type SmartTransformOptions = psdedit.SmartTransformOptions

// PatternFillEditOptions supplies accurate artwork when editing known pattern parameters outside live sampling.
type PatternFillEditOptions = psdedit.PatternFillEditOptions

// EditChanges reports revisions, unsaved changes and composite/structure changes relative to the source.
type EditChanges = psdedit.Changes

// SaveOptions controls changed-channel compression, explicit composite input and output limits.
type SaveOptions = psdedit.SaveOptions

// CreateOptions selects a new RGB8 document's PSD/PSB version and allocation limits.
type CreateOptions = psdedit.CreateOptions

// TextEditOptions supplies accurate replacement source pixels for a supported legacy text edit.
type TextEditOptions = psdedit.TextEditOptions

// NativeTextStateOptions imports caller-supplied native text state, accurate layer artwork and document preview.
type NativeTextStateOptions = psdedit.NativeTextStateOptions

// LinkedContentsResolver explicitly supplies encoded content for an external smart-object link.
type LinkedContentsResolver = psdedit.LinkedContentsResolver

// LinkedContentsUpdate supplies caller-owned external contents and optional timestamp metadata.
type LinkedContentsUpdate = psdedit.LinkedContentsUpdate

// LinkedContentsUpdateResolver supplies explicitly selected contents while preserving their external link.
type LinkedContentsUpdateResolver = psdedit.LinkedContentsUpdateResolver

// SmartFilterEditOptions selects automatic source rendering or accurate supplied artwork for filter parameter edits.
type SmartFilterEditOptions = psdedit.SmartFilterEditOptions

// EffectsEditOptions selects explicit rendering policies for controlled effect parameter edits.
type EffectsEditOptions = psdedit.EffectsEditOptions

// RasterContentsOptions controls owned single-layer PSD/PSB construction from RGB8 pixels.
type RasterContentsOptions = psdedit.RasterContentsOptions

// BuildRasterContents builds editable PSD/PSB content without cropping or changing input samples.
func BuildRasterContents(ctx context.Context, input *image.NRGBA,
	options ...RasterContentsOptions,
) ([]byte, error) {
	return psdedit.BuildRasterContents(ctx, input, options...)
}

// RasterContentsWorkingBytes estimates conservative peak storage before constructing a prototype.
func RasterContentsWorkingBytes(ctx context.Context, input *image.NRGBA,
	options ...RasterContentsOptions,
) (int64, error) {
	return psdedit.RasterContentsWorkingBytes(ctx, input, options...)
}

var (
	// ErrInvalidEdit identifies invalid editing handles, layer operations or replacement inputs.
	ErrInvalidEdit = psdedit.ErrInvalidEdit
	// ErrUnsupportedEdit identifies an edit or automatic composite feature deferred to later phases.
	ErrUnsupportedEdit = psdedit.ErrUnsupported
	// ErrStaleLinkedEdit rejects a resolver result after concurrent editor changes.
	ErrStaleLinkedEdit = psdedit.ErrStaleLinkedEdit
	// ErrStaleNativeTextState rejects an accurate native text snapshot prepared for another editor revision.
	ErrStaleNativeTextState = psdedit.ErrStaleNativeTextState
)

// NewEditor borrows an indexed source; closing the editor does not close that source.
func NewEditor(source *SourceDocument) (*Editor, error) {
	return psdedit.New(source)
}

// OpenEditor owns an indexed PSD/PSB source and releases it when the editor is closed.
func OpenEditor(filename string, options ...LoadOptions) (*Editor, error) {
	return psdedit.Open(filename, options...)
}

// CreateDocument returns an owned editor containing one transparent canvas-sized RGB8 layer.
// Populate handle 1 with ReplacePixels or add layers and groups before SavePSD.
func CreateDocument(ctx context.Context, width, height int, options ...CreateOptions) (*Editor, error) {
	return psdedit.Create(ctx, width, height, options...)
}
