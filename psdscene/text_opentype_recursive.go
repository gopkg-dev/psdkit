package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
)

// One traversal shares ancestor identity and total prototype cardinality across owned child frames.
type openTypeTraversal struct {
	active    map[*psdfile.Document]bool
	documents int
}

func (engine *rgbTileRenderer) enterOpenTypeDocument() (func(), error) {
	if engine.settings.ApproximateOpenTypeText == nil {
		return func() {}, nil
	}
	state := engine.settings.openTypeTraversal
	if state == nil {
		capacity := min(engine.settings.MaxDepth, engine.settings.MaxDocuments)
		// Prove the bounded map reservation before multiplication or allocation, independently of outer validation.
		if capacity <= 0 || engine.working < 0 || engine.working > engine.settings.MaxWorkingBytes {
			return nil, sceneError(psdfile.ErrLimit, "tile.text.recursion", 0, "invalid recursion storage budget")
		}
		available := engine.settings.MaxWorkingBytes - engine.working
		if available < 256 || int64(capacity) > (available-256)/128 {
			return nil, sceneError(psdfile.ErrLimit, "tile.text.recursion", 0, "ancestor map exceeds remaining budget")
		}
		amount := 256 + int64(capacity)*128
		if err := engine.reserve(amount, "tile.text.recursion", 0); err != nil {
			return nil, err
		}
		state = &openTypeTraversal{active: make(map[*psdfile.Document]bool, capacity)}
		engine.settings.openTypeTraversal = state
		engine.settings.openTypeDepth = 1
	}
	if err := engine.checkOpenTypeContent(engine.settings.openTypeDepth); err != nil {
		return nil, err
	}
	if state.active[engine.source] {
		return nil, sceneError(psdfile.ErrMalformed, "tile.text.recursion", 0, "recursive original-source cycle")
	}
	state.active[engine.source] = true
	state.documents++
	engine.legacy.active = state.active
	engine.legacy.documents = state.documents
	return func() { delete(state.active, engine.source) }, nil
}

func (engine *rgbTileRenderer) checkOpenTypeContent(depth int) error {
	state := engine.settings.openTypeTraversal
	if depth > engine.settings.MaxDepth || state.documents >= engine.settings.MaxDocuments {
		return sceneError(psdfile.ErrLimit, "tile.text.recursion", 0, "recursive original-source limits exceeded")
	}
	return nil
}

func (engine *rgbTileRenderer) openTypePrototype(
	ctx context.Context, record psdfile.LinkedFileRecord,
	format string, destination *sceneProfile,
) (*image.NRGBA, error) {
	depth := engine.settings.openTypeDepth + 1
	if err := engine.checkOpenTypeContent(depth); err != nil {
		return nil, err
	}
	state := engine.settings.openTypeTraversal
	engine.legacy.working, engine.legacy.documents = engine.working, state.documents
	if record.Kind == "liFD" && format != "" || engine.legacy.resolvesExternalRaster(record) {
		var pixels *image.NRGBA
		var err error
		if record.Kind == "liFD" {
			pixels, err = engine.legacy.renderLinkedRasterLimited(ctx, engine.source, record,
				destination, depth, engine.settings.MaxPixels)
		} else {
			pixels, err = engine.legacy.renderExternalRasterLimited(ctx, engine.source, record,
				destination, depth, engine.settings.MaxPixels)
		}
		state.documents = engine.legacy.documents
		engine.working = engine.legacy.working
		return pixels, err
	}
	available := engine.settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "tile.text.child", record.Data.Offset, "no child indexing budget remains")
	}
	var child *psdfile.Document
	var err error
	if record.Kind == "liFD" {
		limits := engine.source.Limits()
		limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, available)
		child, err = engine.source.OpenLinkedDocument(ctx, record, psdfile.LoadOptions{Limits: limits})
	} else {
		// Callback-owned indexing uses the caller's source limits; its transferred index is charged before decoding.
		child, err = engine.source.ResolveLinkedDocument(ctx, record, engine.settings.ExternalResolver)
	}
	if err != nil {
		return nil, err
	}
	if state.active[child] {
		// An ancestor belongs to the outer frame; rejecting it must not close that owner.
		return nil, sceneError(psdfile.ErrMalformed, "tile.text.recursion", record.Data.Offset,
			"resolver returned an ancestor original source")
	}
	indexBytes := child.IndexMetadataBytes()
	if err := engine.reserve(indexBytes, "tile.text.child.index", record.Data.Offset); err != nil {
		_ = child.Close()
		return nil, err
	}
	var pixels *image.NRGBA
	if len(child.Layers) == 0 {
		// A flattened original has only its original image data, with no editable text-layer cache to bypass.
		engine.legacy.working, engine.legacy.documents = engine.working, state.documents
		pixels, err = engine.legacy.render(ctx, child, depth, destination)
		engine.working, state.documents = engine.legacy.working, engine.legacy.documents
	} else {
		pixels, err = engine.renderOpenTypeChild(ctx, child, depth, destination)
	}
	closeErr := child.Close()
	engine.working -= indexBytes
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return pixels, nil
}

func (engine *rgbTileRenderer) renderOpenTypeChild(
	ctx context.Context, child *psdfile.Document, depth int, destination *sceneProfile,
) (*image.NRGBA, error) {
	available := engine.settings.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "tile.text.child", 0, "no child glyph-frame budget remains")
	}
	policy := *engine.settings.ApproximateOpenTypeText
	// Parent artwork limits are expressed in parent document coordinates, never in the child glyph domain.
	policy.Bounds = nil
	settings := engine.settings
	settings.MaxWorkingBytes = available
	settings.MaxPixels = min(engine.settings.MaxPixels, child.Limits().MaxDecodePixels, available/4)
	settings.ApproximateOpenTypeText = &policy
	settings.ownedOpenTypeText = true
	settings.OutputProfile = nil
	settings.ownedProfile = false
	settings.openTypeDepth = depth
	canvas := image.Rect(0, 0, int(child.Header.Width), int(child.Header.Height))
	pixels, err := RenderTile(ctx, child, canvas, settings)
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(int64(cap(pixels.Pix)), "tile.text.child.pixels", 0); err != nil {
		return nil, err
	}
	engine.legacy.working = engine.working
	profile, err := engine.legacy.readProfile(ctx, child)
	if err != nil {
		return nil, err
	}
	pixels, err = engine.legacy.convertProfile(ctx, pixels, profile, destination)
	if err != nil {
		return nil, err
	}
	engine.legacy.working -= int64(len(profile.data))
	engine.working = engine.legacy.working
	return pixels, nil
}
