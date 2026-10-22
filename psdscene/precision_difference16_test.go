package psdscene

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json/v2"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
)

type differenceNativeCase struct {
	ID       string       `json:"id"`
	Source   string       `json:"source"`
	Expected [][][]uint16 `json:"expected_rgba16"`
}

type differenceNativeReader struct {
	*bytes.Reader
	forbidden psdfile.Span
	hits      int
}

func (reader *differenceNativeReader) ReadAt(data []byte, offset int64) (int, error) {
	if int64(len(data)) > 0 && reader.forbidden.Length != 0 &&
		offset < reader.forbidden.Offset+reader.forbidden.Length && offset+int64(len(data)) > reader.forbidden.Offset {
		reader.hits++
		return 0, errors.New("saved composite pixel read")
	}
	return reader.Reader.ReadAt(data, offset)
}

func differenceNativeScene(t *testing.T, source *psdfile.Document) *psdprecision.Scene {
	t.Helper()
	scene := &psdprecision.Scene{Width: 12, Height: 2, Encoding: psdprecision.EncodedRGB}
	for index, record := range source.Layers {
		pixels, err := source.DecodeLayer(nil, index)
		if err != nil {
			t.Fatal(err)
		}
		artwork := &psdprecision.FloatImage{Width: 12, Height: 2, SourceDepth: 16,
			Pix: make([]float64, 12*2*4)}
		for _, plane := range pixels.Channels {
			component := map[int16]int{0: 0, 1: 1, 2: 2, -1: 3}[plane.ID]
			for pixel := range 24 {
				artwork.Pix[pixel*4+component] = float64(binary.BigEndian.Uint16(plane.Data[pixel*2:])) / 65535
			}
		}
		opacity := float64(record.Opacity) / 255
		scene.Layers = append(scene.Layers, &psdprecision.Layer{Bounds: record.Bounds, Pixels: artwork,
			BlendMode: record.BlendMode, Opacity: &opacity, Hidden: record.Flags&2 != 0})
	}
	return scene
}

func differenceNativeCompare(t *testing.T, pixels *psdprecision.FloatImage,
	bounds image.Rectangle, expected [][][]uint16) {
	t.Helper()
	if pixels == nil || pixels.Width != bounds.Dx() || pixels.Height != bounds.Dy() || pixels.SourceDepth != 16 {
		t.Fatal("native result geometry or depth differs")
	}
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			offset := ((y-bounds.Min.Y)*pixels.Width + x - bounds.Min.X) * 4
			for component := range 4 {
				value := pixels.Pix[offset+component]
				if value*32768 != math.Trunc(value*32768) || uint16(math.Round(value*65535)) != expected[y][x][component] {
					t.Fatalf("native storage mismatch %d,%d component%d: %g expected%d", x, y, component, value,
						expected[y][x][component])
				}
			}
		}
	}
}

func TestRGB16DifferenceNativeHeldoutsAndThreeLayerStacks(t *testing.T) {
	directory := os.Getenv("PSDKIT_RGB16_DIFFERENCE_NATIVE_DIR")
	if directory == "" {
		t.Skip("set PSDKIT_RGB16_DIFFERENCE_NATIVE_DIR to owned native QA inputs")
	}
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases []differenceNativeCase `json:"cases"`
	}
	if err := json.Unmarshal(data, &manifest, json.MatchCaseInsensitiveNames(true)); err != nil || len(manifest.Cases) != 8 {
		t.Fatalf("native corpus differs: %v", err)
	}
	for _, item := range manifest.Cases {
		t.Run(item.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(directory, item.Source))
			if err != nil {
				t.Fatal(err)
			}
			reader := &differenceNativeReader{Reader: bytes.NewReader(data)}
			source, err := psdfile.Read(reader, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			reader.forbidden = source.Composite.Data
			scene := differenceNativeScene(t, source)
			full, err := psdprecision.RenderRGB16(nil, scene)
			if err != nil {
				t.Fatal(err)
			}
			differenceNativeCompare(t, full, image.Rect(0, 0, 12, 2), item.Expected)
			full, err = RenderPrecision(nil, source)
			if err != nil {
				t.Fatal(err)
			}
			differenceNativeCompare(t, full, image.Rect(0, 0, 12, 2), item.Expected)
			for _, rectangle := range []image.Rectangle{image.Rect(1, 0, 11, 2), image.Rect(0, 1, 12, 2),
				image.Rect(4, 0, 11, 2), image.Rect(0, 0, 2, 2)} {
				region, err := psdprecision.RenderRGB16Region(nil, scene, rectangle)
				if err != nil {
					t.Fatal(err)
				}
				differenceNativeCompare(t, region.Image, rectangle, item.Expected)
				region, err = RenderPrecisionTile(nil, source, rectangle)
				if err != nil {
					t.Fatal(err)
				}
				differenceNativeCompare(t, region.Image, rectangle, item.Expected)
			}
			visits := 0
			err = VisitPrecisionTiles(context.Background(), source, image.Rect(0, 0, 12, 2), image.Pt(5, 1),
				func(region *psdprecision.RegionImage) error {
					visits++
					differenceNativeCompare(t, region.Image, region.Bounds, item.Expected)
					return nil
				})
			if err != nil || visits != 6 || reader.hits != 0 {
				t.Fatalf("native visitor or composite read differs: visits=%d saved=%d err=%v", visits, reader.hits, err)
			}
		})
	}
}
