package psdscene

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"slices"

	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdfile"
)

type sceneProfile struct{ data []byte }

// ReadSourceRGBProfile returns an owned ICC profile, or nil when none was saved.
// It reads the last ICC resource, never guesses a working profile and checks cancellation between chunks.
// Zero maxBytes selects four MiB; a supplied value only lowers that limit.
func ReadSourceRGBProfile(ctx context.Context, source *psdfile.Document, maxBytes int64) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if source == nil || maxBytes < 0 {
		return nil, sceneError(psdfile.ErrMalformed, "color.profile", 0, "invalid source or profile budget")
	}
	if _, err := source.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	if source.Header.ColorMode != 3 {
		return nil, sceneError(psdfile.ErrUnsupported, "color.profile", 0,
			"RGB profile extraction requires an RGB source document")
	}
	if maxBytes == 0 || maxBytes > 4<<20 {
		maxBytes = 4 << 20
	}
	maxBytes = min(maxBytes, source.Limits().MaxReadBytes)
	for _, v := range slices.Backward(source.Resources) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resource := v
		if resource.ID != 1039 {
			continue
		}
		if resource.Data.Length < 132 {
			return nil, sceneError(psdfile.ErrMalformed, "color.profile", resource.Data.Offset,
				"saved ICC resource has no complete header")
		}
		if resource.Data.Length > maxBytes {
			return nil, sceneError(psdfile.ErrLimit, "color.profile", resource.Data.Offset,
				"saved ICC resource exceeds the profile budget")
		}
		result := make([]byte, int(resource.Data.Length))
		for position := 0; position < len(result); {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			length := min(64<<10, len(result)-position)
			count, err := source.ReadAt(result[position:position+length], resource.Data.Offset+int64(position))
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil && !errors.Is(err, io.EOF) {
				return nil, err
			}
			if count != length {
				return nil, &psdfile.Error{Kind: psdfile.ErrMalformed, Feature: "scene", Path: "color.profile",
					Offset: resource.Data.Offset + int64(position), Detail: "ICC resource could not be read completely",
					Cause: errors.Join(io.ErrUnexpectedEOF, err)}
			}
			position += count
		}
		return result, nil
	}
	return nil, nil
}

func (engine *renderer) readProfile(ctx context.Context, source *psdfile.Document) (*sceneProfile, error) {
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "color.profile", 0, "no profile read budget remains")
	}
	data, err := ReadSourceRGBProfile(ctx, source, available)
	if err != nil {
		return nil, err
	}
	if err := engine.reserve(int64(len(data)), "color.profile", 0); err != nil {
		return nil, err
	}
	return &sceneProfile{data: data}, nil
}

func (engine *renderer) convertProfile(
	ctx context.Context, pixels *image.NRGBA, source, destination *sceneProfile,
) (*image.NRGBA, error) {
	if bytes.Equal(source.data, destination.data) && len(source.data) != 0 ||
		len(source.data) == 0 && len(destination.data) == 0 && !engine.options.AssumeUntaggedSRGB {
		return pixels, nil
	}
	if (len(source.data) == 0 || len(destination.data) == 0) && !engine.options.AssumeUntaggedSRGB {
		return nil, sceneError(psdfile.ErrUnsupported, "color.profile", 0,
			"a required profile conversion has an untagged source or destination")
	}
	available := engine.options.MaxWorkingBytes - engine.working
	if available <= 0 {
		return nil, sceneError(psdfile.ErrLimit, "color.transform", 0, "no profile conversion budget remains")
	}
	settings := psdcolor.ProfileTransformOptions{AssumeSRGB: engine.options.AssumeUntaggedSRGB,
		MaxWorkingBytes: available}
	working, err := psdcolor.ProfileTransformWorkingBytes(ctx, pixels, source.data, destination.data, settings)
	if err != nil {
		return nil, sceneProfileError(err)
	}
	if err := engine.reserve(working, "color.transform", 0); err != nil {
		return nil, err
	}
	converted, err := psdcolor.TransformProfile(ctx, pixels, source.data, destination.data, settings)
	if err != nil {
		return nil, sceneProfileError(err)
	}
	engine.working -= working
	return converted, nil
}

func sceneProfileError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	kind := psdfile.ErrMalformed
	if errors.Is(err, psdcolor.ErrUnsupported) {
		kind = psdfile.ErrUnsupported
	} else if errors.Is(err, psdcolor.ErrLimit) {
		kind = psdfile.ErrLimit
	}
	return &psdfile.Error{Kind: kind, Feature: "scene", Path: "color.transform", Detail: "RGB profile transform failed",
		Cause: err}
}
