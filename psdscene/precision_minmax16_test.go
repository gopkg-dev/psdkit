package psdscene_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type minMaxSourceCase struct {
	ID            string `json:"id"`
	Input         string
	ICCSHA256     string `json:"iccSHA256"`
	Width, Height int
	Q15           []uint16
	StoredRGBA    []uint16
}

func minMaxSourceCases(t *testing.T) []minMaxSourceCase {
	t.Helper()
	encoded, err := os.ReadFile("testdata/minmax16/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset struct{ Cases []minMaxSourceCase }
	if err := json.Unmarshal(encoded, &dataset, json.MatchCaseInsensitiveNames(true)); err != nil || len(dataset.Cases) != 8 {
		t.Fatalf("source Fraction fixture family differs: %v", err)
	}
	return dataset.Cases
}

type minMaxFooterReader struct {
	*bytes.Reader
	footer psdfile.Span
	hits   int
}

func (reader *minMaxFooterReader) ReadAt(target []byte, offset int64) (int, error) {
	if len(target) != 0 && reader.footer.Length != 0 && offset < reader.footer.Offset+reader.footer.Length &&
		offset+int64(len(target)) > reader.footer.Offset {
		reader.hits++
		return 0, errors.New("saved preview is not a min/max source")
	}
	return reader.Reader.ReadAt(target, offset)
}

func minMaxSourceCompare(
	t *testing.T, pixels *psdprecision.FloatImage, rectangle image.Rectangle, item minMaxSourceCase,
) {
	t.Helper()
	if pixels == nil || pixels.Width != rectangle.Dx() || pixels.Height != rectangle.Dy() || pixels.SourceDepth != 16 ||
		fmt.Sprintf("%x", sha256.Sum256(pixels.ICCProfile)) != item.ICCSHA256 {
		t.Fatal("source depth, coordinates or original ICC differs")
	}
	for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
		for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
			for component := range 4 {
				original := (y*item.Width+x)*4 + component
				offset := ((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4 + component
				if pixels.Pix[offset]*32768 != float64(item.Q15[original]) ||
					math.Round(pixels.Pix[offset]*65535) != float64(item.StoredRGBA[original]) {
					t.Fatalf("%s (%d,%d) component%d source differed from Fraction", item.ID, x, y, component)
				}
			}
		}
	}
}

func TestPrecisionMinMaxLiteralSourceFullROIVisitorAndICC(t *testing.T) {
	for _, item := range minMaxSourceCases(t) {
		t.Run(item.ID, func(t *testing.T) {
			encoded, err := os.ReadFile(filepath.Join("testdata/minmax16", filepath.Base(item.Input)))
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(encoded)
			reader := &minMaxFooterReader{Reader: bytes.NewReader(encoded)}
			source, err := psdfile.Read(reader, int64(len(encoded)))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = source.Close() })
			reader.footer = source.Composite.Data
			canvas := image.Rect(0, 0, item.Width, item.Height)
			full, err := psdscene.RenderPrecision(nil, source)
			if err != nil {
				t.Fatal(err)
			}
			minMaxSourceCompare(t, full, canvas, item)
			for _, rectangle := range []image.Rectangle{
				image.Rect(0, 0, 3, 2), image.Rect(3, 1, 11, 4), image.Rect(13, 0, 16, 4),
			} {
				region, err := psdscene.RenderPrecisionTile(nil, source, rectangle)
				if err != nil || region.Bounds != rectangle {
					t.Fatalf("source ROI differs: %v", err)
				}
				minMaxSourceCompare(t, region.Image, rectangle, item)
			}
			visits := 0
			err = psdscene.VisitPrecisionTiles(nil, source, canvas, image.Pt(5, 2),
				func(tile *psdprecision.RegionImage) error {
					minMaxSourceCompare(t, tile.Image, tile.Bounds, item)
					visits++
					return nil
				})
			if err != nil || visits != 8 || reader.hits != 0 || !bytes.Equal(encoded, original) {
				t.Fatalf("visitor, source ownership or footer isolation differs: visits=%d hits=%d %v", visits, reader.hits, err)
			}
		})
	}
}

func TestPrecisionMinMaxWholeBackdropOutsideROIAndSourceFailures(t *testing.T) {
	for _, mode := range []string{"dark", "lite"} {
		t.Run(mode, func(t *testing.T) {
			canvas := image.Rect(0, 0, 2, 1)
			bottom := precisionLayerFixture{bounds: canvas, opacity: 255,
				channels: integer16TwoPixelChannels([2]uint16{65535, 65535}, [2]uint16{65535, 32768})}
			top := precisionLayerFixture{bounds: canvas, opacity: 255, mode: mode,
				channels: integer16TwoPixelChannels([2]uint16{32767, 32769}, [2]uint16{65535, 65535})}
			source, _ := precisionSource(t, precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{bottom, top}))
			full, err := psdscene.RenderPrecision(nil, source)
			if err != nil {
				t.Fatal(err)
			}
			region, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(0, 0, 1, 1))
			if err != nil || region.Image.Pix[0] != full.Pix[0] ||
				mode == "dark" && full.Pix[0] != float64(32767)/65535 {
				t.Fatalf("ROI hid partial backdrop alpha or changed float fallback: %v", err)
			}
			bottom.channels = integer16TwoPixelChannels([2]uint16{65535, 65535}, [2]uint16{65535, 65535})
			data := precisionSourceFixture(16, 3, 2, 1, []precisionLayerFixture{bottom, top})
			for _, operation := range []string{"budget", "read-cause", "cancel"} {
				t.Run(operation, func(t *testing.T) {
					source, reader := precisionSource(t, data)
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					options := psdscene.PrecisionOptions{}
					switch operation {
					case "budget":
						reader.forbid, options.MaxWorkingBytes = true, 1100
					case "read-cause":
						reader.failure = psdfile.ErrLimit
					case "cancel":
						reader.cancel = cancel
					}
					pixels, err := psdscene.RenderPrecision(ctx, source, options)
					if pixels != nil {
						t.Fatal("source failure exposed pixels")
					}
					switch operation {
					case "budget":
						if !errors.Is(err, psdfile.ErrLimit) || reader.pixelReads != 0 {
							t.Fatalf("budget preceded by source IO: %v", err)
						}
					case "read-cause":
						var detail *psdfile.Error
						if !errors.Is(err, psdfile.ErrLimit) || !errors.As(err, &detail) || detail.Cause == nil {
							t.Fatalf("ReaderAt original cause lost: %v", err)
						}
					case "cancel":
						if !errors.Is(err, context.Canceled) || reader.pixelReads != 1 {
							t.Fatalf("source cancellation ignored: %v", err)
						}
					}
				})
			}
		})
	}
}
