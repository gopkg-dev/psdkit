package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"

	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type smartContentsSource struct {
	document      *psdfile.Document
	width, height int
	fileType      string
	retainedBytes int64
	raster        *psdfile.LinkedRaster
}

// decodeSmartContentsSource borrows immutable encoded bytes and charges only additional decoded/index storage.
func decodeSmartContentsSource(
	ctx context.Context, data []byte, limits psdfile.Limits, available int64,
) (*smartContentsSource, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if available <= 0 {
		return nil, editError(psdfile.ErrLimit, "smartObject.content", "no source decoding budget remains")
	}
	if limits.MaxFileBytes > 0 && int64(len(data)) > limits.MaxFileBytes {
		return nil, editError(psdfile.ErrLimit, "smartObject.content", "encoded replacement exceeds the file budget")
	}
	if limits.MaxDecodeBytes > 0 {
		available = min(available, limits.MaxDecodeBytes)
	}
	if bytes.HasPrefix(data, []byte("8BPS")) {
		if limits.MaxMetadataBytes == 0 {
			limits.MaxMetadataBytes = psdfile.DefaultLimits().MaxMetadataBytes
		}
		limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, available)
		document, err := psdfile.Parse(data, psdfile.LoadOptions{Limits: limits})
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			_ = document.Close()
			return nil, err
		}
		if document.Header.ColorMode != 3 || document.Header.Depth != 8 {
			_ = document.Close()
			return nil, editError(ErrUnsupported, "smartObject.content", "replacement must use an RGB8 prototype canvas")
		}
		fileType := "8BPS"
		if document.Header.Version == 2 {
			fileType = "8BPB"
		}
		return &smartContentsSource{
			document: document, width: int(document.Header.Width), height: int(document.Header.Height),
			fileType: fileType, retainedBytes: document.IndexMetadataBytes(),
		}, nil
	}
	raster, err := psdfile.DecodeRasterContents(ctx, data, psdfile.LinkedRasterOptions{
		MaxPixels: limits.MaxDecodePixels, MaxWorkingBytes: available,
	})
	if err != nil {
		return nil, compositeRenderError(err)
	}
	fileType := "PNGf"
	switch raster.Format {
	case "jpeg":
		fileType = "JPEG"
	case "tiff":
		fileType = "TIFF"
	}
	return &smartContentsSource{
		raster: raster, width: raster.Pixels.Rect.Dx(), height: raster.Pixels.Rect.Dy(), fileType: fileType,
		retainedBytes: int64(len(raster.Pixels.Pix)) + int64(len(raster.ICCProfile)),
	}, nil
}

func (source *smartContentsSource) close() error {
	if source == nil {
		return nil
	}
	var err error
	if source.document != nil {
		err = source.document.Close()
	}
	source.document, source.raster, source.retainedBytes = nil, nil, 0
	return err
}

// render uses a budget excluding retainedBytes; unconverted raster output shares already-owned source pixels.
// A nil parent profile means an untagged destination, matching the editor's explicit profile policy.
func (source *smartContentsSource) render(
	ctx context.Context, parentProfile []byte, assumeUntaggedSRGB bool, available int64,
) (*image.NRGBA, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || source.document == nil && source.raster == nil {
		return nil, editError(psdfile.ErrClosed, "smartObject.content", "replacement source is closed")
	}
	if available <= 0 {
		return nil, editError(psdfile.ErrLimit, "smartObject.content", "no source rendering budget remains")
	}
	if source.document != nil {
		if parentProfile == nil {
			parentProfile = []byte{}
		}
		pixels, err := psdscene.Render(ctx, source.document, psdscene.Options{
			MaxWorkingBytes: available, OutputProfile: parentProfile, AssumeUntaggedSRGB: assumeUntaggedSRGB,
		})
		return pixels, compositeRenderError(err)
	}
	raster := source.raster
	if bytes.Equal(raster.ICCProfile, parentProfile) && len(raster.ICCProfile) != 0 ||
		len(raster.ICCProfile) == 0 && len(parentProfile) == 0 && !assumeUntaggedSRGB {
		return raster.Pixels, nil
	}
	if (len(raster.ICCProfile) == 0 || len(parentProfile) == 0) && !assumeUntaggedSRGB {
		return nil, editError(ErrUnsupported, "smartObject.profile",
			"a required profile conversion has an untagged source or destination")
	}
	pixels, err := psdcolor.TransformProfile(ctx, raster.Pixels, raster.ICCProfile, parentProfile,
		psdcolor.ProfileTransformOptions{AssumeSRGB: assumeUntaggedSRGB, MaxWorkingBytes: available})
	if err != nil {
		return nil, smartContentsProfileError(err)
	}
	return pixels, nil
}

func smartContentsProfileError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := psdfile.ErrMalformed
	if errors.Is(err, psdcolor.ErrUnsupported) {
		kind = ErrUnsupported
	} else if errors.Is(err, psdcolor.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "edit", Path: "smartObject.profile",
		Detail: "RGB profile transform failed", Cause: err}
}
