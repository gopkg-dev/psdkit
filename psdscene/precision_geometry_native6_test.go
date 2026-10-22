package psdscene_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdprecision"
)

type geometry34Layer struct {
	Name        string      `json:"name"`
	Bounds      [4]int      `json:"bounds"`
	RawCodes    [][4]uint16 `json:"raw_codes"`
	Mode        string      `json:"mode"`
	OpacityByte uint8       `json:"opacity_byte"`
}

type geometry34Case struct {
	ID                    string            `json:"id"`
	Source                string            `json:"source"`
	SourceSHA256          string            `json:"source_sha256"`
	Width                 int               `json:"width"`
	Height                int               `json:"height"`
	Layers                []geometry34Layer `json:"layers"`
	ICCHex                string            `json:"icc_hex"`
	NativeObservedMask    []bool            `json:"native_observed_mask"`
	NativeObservedRGBA16  []*[4]uint16      `json:"native_observed_rgba16"`
	ExpectedQ15           [][4]uint32       `json:"expected_Q15"`
	ExpectedRGBA16Storage [][4]uint16       `json:"expected_rgba16_storage"`
	ROIWindows            [][4]int          `json:"roi_windows"`
	VisitorTileSize       [2]int            `json:"visitor_tile_size"`
}

type geometry34Reader struct {
	*bytes.Reader
	forbidden psdkit.RawSpan
	hits      int
	reads     int
}

func (reader *geometry34Reader) ReadAt(data []byte, offset int64) (int, error) {
	reader.reads++
	if len(data) != 0 && offset < reader.forbidden.Offset+reader.forbidden.Length &&
		offset+int64(len(data)) > reader.forbidden.Offset && reader.forbidden.Length != 0 {
		reader.hits++
		return 0, errors.New("wave34 saved composite pixel payload read")
	}
	return reader.Reader.ReadAt(data, offset)
}

func geometry34Rect(values [4]int) image.Rectangle {
	return image.Rect(values[0], values[1], values[2], values[3])
}

func geometry34Cases(t *testing.T) []geometry34Case {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "rgb16-geometry-native6", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases                     []geometry34Case `json:"cases"`
		NativeObservedPixels      int              `json:"native_observed_pixels"`
		MathOnlyOutsideBBoxPixels int              `json:"math_only_outside_bbox_pixels"`
	}
	if err := json.Unmarshal(data, &manifest, json.MatchCaseInsensitiveNames(true)); err != nil || len(manifest.Cases) != 6 ||
		manifest.NativeObservedPixels != 256 || manifest.MathOnlyOutsideBBoxPixels != 32 {
		t.Fatalf("fixed native6 observation partition differs: %v", err)
	}
	return manifest.Cases
}

func geometry34Scene(t *testing.T, item geometry34Case) *psdprecision.Scene {
	t.Helper()
	scene := &psdprecision.Scene{Width: item.Width, Height: item.Height, Encoding: psdprecision.EncodedRGB}
	for _, layer := range item.Layers {
		bounds := geometry34Rect(layer.Bounds)
		if len(layer.RawCodes) != bounds.Dx()*bounds.Dy() {
			t.Fatal("fixed raw plane geometry differs")
		}
		pixels := &psdprecision.FloatImage{Width: bounds.Dx(), Height: bounds.Dy(),
			Encoding: psdprecision.EncodedRGB, SourceDepth: 16, Pix: make([]float64, len(layer.RawCodes)*4)}
		for index, raw := range layer.RawCodes {
			for component := range 4 {
				pixels.Pix[index*4+component] = float64(raw[component]) / 65535
			}
		}
		opacity := float64(layer.OpacityByte) / 255
		scene.Layers = append(scene.Layers, &psdprecision.Layer{Name: layer.Name, Bounds: bounds,
			Pixels: pixels, BlendMode: layer.Mode, Opacity: &opacity})
	}
	return scene
}

func geometry34Equal(t *testing.T, actual *psdprecision.FloatImage, region image.Rectangle, item geometry34Case) {
	t.Helper()
	if actual == nil || actual.Width != region.Dx() || actual.Height != region.Dy() ||
		actual.SourceDepth != 16 || actual.Encoding != psdprecision.EncodedRGB ||
		len(actual.Pix) != region.Dx()*region.Dy()*4 {
		t.Fatal("wave34 output geometry/depth/encoding differs")
	}
	for y := region.Min.Y; y < region.Max.Y; y++ {
		for x := region.Min.X; x < region.Max.X; x++ {
			global := y*item.Width + x
			local := ((y-region.Min.Y)*region.Dx() + x - region.Min.X) * 4
			for component := range 4 {
				value := actual.Pix[local+component]
				if math.Float64bits(value) != math.Float64bits(float64(item.ExpectedQ15[global][component])/32768) {
					t.Fatalf("Fraction Q15 mismatch absolute(%d,%d) component%d: %g", x, y, component, value)
				}
				stored := uint16(math.Round(value * 65535))
				if stored != item.ExpectedRGBA16Storage[global][component] {
					t.Fatal("declared final storage projection differs")
				}
				if item.NativeObservedMask[global] {
					if item.NativeObservedRGBA16[global] == nil || stored != item.NativeObservedRGBA16[global][component] {
						t.Fatalf("observed native RAW mismatch absolute(%d,%d) component%d", x, y, component)
					}
				} else if value != 0 {
					t.Fatal("unobserved outside-BBox mathematical zero policy differs")
				}
			}
		}
	}
}

func TestRGB16GeometryNative6SceneSourceAndAbsoluteRegions(t *testing.T) {
	for _, item := range geometry34Cases(t) {
		t.Run(item.ID, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "rgb16-geometry-native6", item.Source))
			if err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256(data)
			if hex.EncodeToString(before[:]) != item.SourceSHA256 {
				t.Fatal("original portable PSD bytes differ")
			}
			reader := &geometry34Reader{Reader: bytes.NewReader(data)}
			source, err := psdkit.ReadDocument(reader, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			reader.forbidden = source.Composite.Data
			if source.Composite.Compression == 1 {
				tableBytes := int64(source.Header.Height) * int64(source.Header.Channels) *
					int64(source.Composite.RowCountWidth)
				reader.forbidden.Offset += tableBytes
				reader.forbidden.Length -= tableBytes
			}
			profile, err := hex.DecodeString(item.ICCHex)
			if err != nil {
				t.Fatal(err)
			}
			scene := geometry34Scene(t, item)
			canvas := image.Rect(0, 0, item.Width, item.Height)
			external, err := psdprecision.RenderRGB16(nil, scene)
			if err != nil {
				t.Fatal(err)
			}
			geometry34Equal(t, external, canvas, item)
			full, err := psdkit.RenderSourcePrecision(nil, source)
			if err != nil {
				t.Fatal(err)
			}
			geometry34Equal(t, full, canvas, item)
			if !bytes.Equal(full.ICCProfile, profile) {
				t.Fatal("precision source ICC provenance differs")
			}
			for _, window := range item.ROIWindows {
				bounds := geometry34Rect(window)
				sceneRegion, err := psdprecision.RenderRGB16Region(nil, scene, bounds)
				if err != nil || sceneRegion == nil || sceneRegion.Bounds != bounds {
					t.Fatalf("absolute external scene region: %v", err)
				}
				geometry34Equal(t, sceneRegion.Image, bounds, item)
				region, err := psdkit.RenderSourcePrecisionTile(nil, source, bounds)
				if err != nil || region == nil || region.Bounds != bounds {
					t.Fatalf("absolute source region: %v", err)
				}
				geometry34Equal(t, region.Image, bounds, item)
				if !bytes.Equal(region.Image.ICCProfile, profile) {
					t.Fatal("source ROI ICC provenance differs")
				}
			}
			visits := 0
			err = psdkit.VisitSourcePrecisionTiles(context.Background(), source, canvas,
				image.Pt(item.VisitorTileSize[0], item.VisitorTileSize[1]), func(region *psdkit.FloatRegion) error {
					visits++
					geometry34Equal(t, region.Image, region.Bounds, item)
					if !bytes.Equal(region.Image.ICCProfile, profile) {
						t.Fatal("visitor ICC provenance differs")
					}
					return nil
				})
			if err != nil || visits != 6 || reader.hits != 0 || sha256.Sum256(data) != before {
				t.Fatalf("native6 visitor/source ownership/footer: visits%d hits%d err%v", visits, reader.hits, err)
			}
			if len(full.ICCProfile) != 0 {
				full.ICCProfile[0] ^= 255
			}
			full.Pix[0] = -123
			again, err := psdkit.RenderSourcePrecision(nil, source)
			if err != nil || !bytes.Equal(again.ICCProfile, profile) {
				t.Fatalf("caller result mutation altered source ICC: %v", err)
			}
			geometry34Equal(t, again, canvas, item)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			canceled, err := psdkit.RenderSourcePrecision(ctx, source)
			if canceled != nil || !errors.Is(err, context.Canceled) {
				t.Fatal("source cancellation returned a partial frame")
			}
			if reader.hits != 0 {
				t.Fatal("source precision read a saved composite pixel payload")
			}
		})
	}
}
