package psdweb

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/png"
	"math"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func (e *Engine) renderTiles(ctx context.Context, source *psdfile.Document, bounds image.Rectangle,
	size image.Point, options ...psdkit.TileOptions,
) (FileOutput, error) {
	buffer := &boundedBuffer{limit: e.config.MaxOutputBytes}
	archive := zip.NewWriter(buffer)
	count := 0
	err := psdkit.VisitSourceTiles(ctx, source, bounds, size, func(pixels *image.NRGBA) error {
		count++
		if count > 4096 {
			return ErrLimit
		}
		name := fmt.Sprintf("tile_%d_%d.png", pixels.Rect.Min.X, pixels.Rect.Min.Y)
		file, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return err
		}
		return png.Encode(file, pixels)
	}, options...)
	if err != nil {
		_ = archive.Close()
		return FileOutput{}, err
	}
	if err := archive.Close(); err != nil {
		return FileOutput{}, err
	}
	return FileOutput{Name: "tiles.zip", MIME: "application/zip", Data: buffer.Bytes()}, nil
}

func (e *Engine) renderPrecisionTiles(ctx context.Context, source *psdfile.Document, bounds image.Rectangle,
	size image.Point, options ...psdkit.TileOptions,
) (FileOutput, error) {
	buffer := &boundedBuffer{limit: e.config.MaxOutputBytes}
	archive := zip.NewWriter(buffer)
	count := 0
	err := psdkit.VisitSourcePrecisionTiles(ctx, source, bounds, size, func(tile *psdkit.FloatRegion) error {
		count++
		if count > 4096 {
			return ErrLimit
		}
		metadata, err := json.Marshal(map[string]any{"bounds": tile.Bounds, "width": tile.Image.Width,
			"height": tile.Image.Height, "encoding": tile.Image.Encoding, "sourceDepth": tile.Image.SourceDepth,
			"iccProfile": tile.Image.ICCProfile, "sampleFormat": "little-endian float64 RGBA"})
		if err != nil {
			return err
		}
		file, err := archive.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("tile_%04d.json", count), Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err := file.Write(metadata); err != nil {
			return err
		}
		samples, err := archive.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("tile_%04d.f64le", count), Method: zip.Store})
		if err != nil {
			return err
		}
		var scratch [8192]byte
		for start := 0; start < len(tile.Image.Pix); {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(len(tile.Image.Pix), start+len(scratch)/8)
			for index, sample := range tile.Image.Pix[start:end] {
				binary.LittleEndian.PutUint64(scratch[index*8:], math.Float64bits(sample))
			}
			if _, err := samples.Write(scratch[:(end-start)*8]); err != nil {
				return err
			}
			start = end
		}
		return nil
	}, options...)
	if err != nil {
		_ = archive.Close()
		return FileOutput{}, err
	}
	if err := archive.Close(); err != nil {
		return FileOutput{}, err
	}
	return FileOutput{Name: "precision-tiles.zip", MIME: "application/zip", Data: buffer.Bytes()}, nil
}
