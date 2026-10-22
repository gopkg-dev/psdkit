package psdprecision_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"math"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/psdprecision"
)

type minMaxLiteralLayer struct {
	Name    string
	Bounds  [4]int
	Mode    string
	Opacity uint8
	RGBA    []uint16
}

type minMaxLiteralCase struct {
	ID            string `json:"id"`
	Width, Height int
	Layers        []minMaxLiteralLayer
	Q15           []uint16
	StoredRGBA    []uint16
}

func minMaxLiteralCases(t *testing.T) []minMaxLiteralCase {
	t.Helper()
	encoded, err := os.ReadFile("testdata/minmax16/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var dataset struct{ Cases []minMaxLiteralCase }
	if err := json.Unmarshal(encoded, &dataset, json.MatchCaseInsensitiveNames(true)); err != nil || len(dataset.Cases) != 8 {
		t.Fatalf("immutable Fraction fixture family differs: %v", err)
	}
	return dataset.Cases
}

func minMaxLiteralScene(item minMaxLiteralCase) *psdprecision.Scene {
	scene := &psdprecision.Scene{Width: item.Width, Height: item.Height, Encoding: psdprecision.EncodedRGB}
	for _, original := range item.Layers {
		bounds := image.Rect(original.Bounds[0], original.Bounds[1], original.Bounds[2], original.Bounds[3])
		pixels := &psdprecision.FloatImage{Width: bounds.Dx(), Height: bounds.Dy(), SourceDepth: 16}
		for _, raw := range original.RGBA {
			pixels.Pix = append(pixels.Pix, float64(raw)/65535)
		}
		opacity := float64(original.Opacity) / 255
		scene.Layers = append(scene.Layers, &psdprecision.Layer{
			Name: original.Name, Bounds: bounds, BlendMode: original.Mode, Pixels: pixels, Opacity: &opacity,
		})
	}
	return scene
}

func minMaxCompare(t *testing.T, result *psdprecision.FloatImage, rectangle image.Rectangle, item minMaxLiteralCase) {
	t.Helper()
	if result == nil || result.Width != rectangle.Dx() || result.Height != rectangle.Dy() || result.SourceDepth != 16 {
		t.Fatal("integer result geometry or depth differs")
	}
	for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
		for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
			for component := range 4 {
				original := (y*item.Width+x)*4 + component
				offset := ((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4 + component
				if result.Pix[offset]*32768 != float64(item.Q15[original]) ||
					math.Round(result.Pix[offset]*65535) != float64(item.StoredRGBA[original]) {
					t.Fatalf("%s (%d,%d) component%d differs from independent Fraction", item.ID, x, y, component)
				}
			}
		}
	}
}

func TestRGB16MinMaxIndependentFractionTwoThreeFiveLayersAndROI(t *testing.T) {
	for _, item := range minMaxLiteralCases(t) {
		t.Run(item.ID, func(t *testing.T) {
			scene := minMaxLiteralScene(item)
			canvas := image.Rect(0, 0, item.Width, item.Height)
			result, err := psdprecision.RenderRGB16OverOpaque(nil, scene, psdprecision.Options{
				MaxPixels:       int64(item.Width * item.Height),
				MaxWorkingBytes: int64(item.Width * item.Height * 32),
			})
			if err != nil {
				t.Fatal(err)
			}
			minMaxCompare(t, result, canvas, item)
			for _, rectangle := range []image.Rectangle{image.Rect(0, 0, 3, 2), image.Rect(3, 1, 11, 4),
				image.Rect(13, 0, 16, 4), image.Rect(0, 0, 0, 4)} {
				region, err := psdprecision.RenderRGB16OverOpaqueRegion(nil, scene, rectangle)
				if err != nil || region.Bounds != rectangle {
					t.Fatalf("absolute ROI differs: %v", err)
				}
				minMaxCompare(t, region.Image, rectangle, item)
			}
			if item.ID == "dark-opaque" || item.ID == "lite-opaque" {
				strict, err := psdprecision.RenderRGB16Opaque(nil, scene)
				if err != nil {
					t.Fatal(err)
				}
				minMaxCompare(t, strict, canvas, item)
			} else if _, err := psdprecision.RenderRGB16Opaque(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
				t.Fatalf("strict opaque API accepted partial source alpha: %v", err)
			}
			before := scene.Layers[0].Pixels.Pix[0]
			result.Pix[0] = 2
			if scene.Layers[0].Pixels.Pix[0] != before {
				t.Fatal("output aliases borrowed raw source")
			}
		})
	}
}

func TestRGB16MinMaxKeepsTransparentAndGenericContractsSeparate(t *testing.T) {
	type renderer func(
		context.Context, *psdprecision.Scene, ...psdprecision.Options,
	) (*psdprecision.FloatImage, error)
	for _, item := range minMaxLiteralCases(t)[:2] {
		scene := minMaxLiteralScene(item)
		for _, render := range []renderer{
			psdprecision.RenderRGB16, psdprecision.RenderRGB16Normal,
		} {
			if result, err := render(nil, scene); result != nil || !errors.Is(err, psdprecision.ErrUnsupported) {
				t.Fatalf("general transparent-bottom integer path accepted min/max: %v", err)
			}
		}
		scene.Layers[0].Pixels.Pix[3] = float64(32768) / 65535
		if _, err := psdprecision.RenderRGB16OverOpaque(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
			t.Fatalf("partial backdrop entered opaque integer path: %v", err)
		}
		if _, err := psdprecision.Render(nil, scene); err != nil {
			t.Fatalf("generic float rendering was narrowed: %v", err)
		}
	}
}

func TestRGB16MinMaxWholeSamplesQuotaCanonicalAndCancellation(t *testing.T) {
	item := minMaxLiteralCases(t)[1]
	scene := minMaxLiteralScene(item)
	if result, err := psdprecision.RenderRGB16OverOpaque(nil, scene,
		psdprecision.Options{MaxWorkingBytes: 16*4*32 - 1}); result != nil || !errors.Is(err, psdprecision.ErrLimit) {
		t.Fatalf("exact output budget minus one escaped: %v", err)
	}
	scene.Layers[0].Pixels.Pix[len(scene.Layers[0].Pixels.Pix)-4] = math.Nextafter(float64(32767)/65535, 1)
	if result, err := psdprecision.RenderRGB16OverOpaqueRegion(nil, scene, image.Rect(0, 0, 1, 1)); result != nil ||
		!errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("ROI hid an invalid original backdrop sample: %v", err)
	}
	scene = minMaxLiteralScene(item)
	scene.Layers[1].Opacity = new(math.Nextafter(float64(128)/255, 1))
	if _, err := psdprecision.RenderRGB16OverOpaque(nil, scene); !errors.Is(err, psdprecision.ErrUnsupported) {
		t.Fatalf("noncanonical byte opacity accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := psdprecision.RenderRGB16OverOpaque(ctx, minMaxLiteralScene(item)); result != nil ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled min/max returned pixels: %v", err)
	}
}
