package psdscene

import (
	"context"
	"image"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func (engine *renderer) renderLinkedRaster(
	ctx context.Context, source *psdfile.Document, record psdfile.LinkedFileRecord, destination *sceneProfile, depth int,
) (*image.NRGBA, error) {
	return engine.renderLinkedRasterLimited(ctx, source, record, destination, depth, source.Limits().MaxDecodePixels)
}

func (engine *renderer) renderLinkedRasterLimited(
	ctx context.Context, source *psdfile.Document, record psdfile.LinkedFileRecord,
	destination *sceneProfile, depth int, maxPixels int64,
) (*image.NRGBA, error) {
	if depth > engine.options.MaxDepth || engine.documents >= engine.options.MaxDocuments || maxPixels <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.contents", record.Data.Offset,
			"recursive content limits exceeded")
	}
	engine.documents++
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "smartObject.raster", record.Data.Offset,
			"no raster decoding budget remains")
	}
	decoded, err := source.DecodeLinkedRaster(ctx, record, psdfile.LinkedRasterOptions{
		MaxWorkingBytes: available, CenteredJPEGChroma: engine.options.CenteredJPEGChroma,
		MaxPixels: min(maxPixels, source.Limits().MaxDecodePixels),
	})
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(int64(len(decoded.Pixels.Pix)+len(decoded.ICCProfile)),
		"smartObject.raster", record.Data.Offset); err != nil {
		return nil, err
	}
	pixels, err := engine.convertProfile(ctx, decoded.Pixels, &sceneProfile{data: decoded.ICCProfile}, destination)
	if err != nil {
		return nil, err
	}
	engine.working -= int64(len(decoded.ICCProfile))
	return pixels, nil
}
