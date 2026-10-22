package psdkit

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdscene"
)

// TextLayerRecord contains ordered source text descriptors and lazy original EngineData.
type TextLayerRecord = psdfile.TextLayerRecord

// TextLayerView reports the allocation reservation of owned text metadata.
type TextLayerView = psdfile.TextLayerView

// DocumentTextBlock retains one document-wide Txt2 payload as an original lazy span.
type DocumentTextBlock = psdfile.DocumentTextBlock

// DocumentTextView reports lazy document text blocks and their metadata reservation.
type DocumentTextView = psdfile.DocumentTextView

// DocumentTextAssociation pairs a physical text layer with its checked document engine record.
type DocumentTextAssociation = psdfile.DocumentTextAssociation

// DocumentTextAssociationView owns text layer descriptors and borrows checked document engine records.
type DocumentTextAssociationView = psdfile.DocumentTextAssociationView

// TextRenderOptions controls explicitly requested approximate unhinted Latin glyph rendering.
type TextRenderOptions = psdscene.TextRenderOptions

// FontResolver resolves exact PostScript names from caller-selected fonts.
type FontResolver = psdfont.FontResolver

// FontResolverFunc adapts an explicit font resolution function.
type FontResolverFunc = psdfont.ResolverFunc

// FontFace owns a TrueType, CFF1 or selected immutable CFF2 instance without executing hinting.
type FontFace = psdfont.Face

// FontOptions bounds explicit font decoding, selects a TTC face and optionally selects CFF2 axis coordinates.
type FontOptions = psdfont.DecodeOptions

// OpenFont opens the exact caller-selected font file without directory searches or substitutions.
func OpenFont(ctx context.Context, path string, options ...FontOptions) (*FontFace, error) {
	return psdfont.Open(ctx, path, options...)
}

// RenderTextLayer returns owned approximate source pixels for a supported point-text layer.
// Apply layer masks, opacity and blending separately or opt into SceneOptions.ApproximateText.
func RenderTextLayer(ctx context.Context, source *SourceDocument, record TextLayerRecord,
	resolver FontResolver, options ...TextRenderOptions,
) (*image.NRGBA, error) {
	return psdscene.RenderTextLayer(ctx, source, record, resolver, options...)
}
