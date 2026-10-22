package psdcompose

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

// TestVectorFeatherNativePrototype is opt-in research using the existing pixel-mask Gaussian helper.
// No compositor capability or production vector-cache behavior is changed by this test.
func TestVectorFeatherNativePrototype(t *testing.T) {
	if os.Getenv("PSDKIT_VECTOR_FEATHER_PROTOTYPE") != "1" {
		t.Skip("optional native vector-feather fixtures are required")
	}
	data, err := os.ReadFile(filepath.Join("..", "verification", "photoshop-vector-feather-fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Cases []struct{ ID, PSD, PNG string }
	}
	if err := json.Unmarshal(data, &manifest, json.MatchCaseInsensitiveNames(true)); err != nil || len(manifest.Cases) == 0 {
		t.Fatalf("native vector feather manifest: %v", err)
	}
	var records []map[string]any
	reportName := "vector-feather-prototype-inputs.json"
	if variant := os.Getenv("PSDKIT_VECTOR_FEATHER_VARIANT"); variant != "" {
		reportName = "vector-feather-prototype-" + variant + "-inputs.json"
	}
	for _, item := range manifest.Cases {
		t.Run(item.ID, func(t *testing.T) {
			record := vectorFeatherPrototypeCase(t, item.ID, item.PSD, item.PNG)
			records = append(records, record)
		})
	}
	encoded, err := json.Marshal(map[string]any{"cases": records,
		"production_changed":       os.Getenv("PSDKIT_VECTOR_FEATHER_VARIANT") == "core",
		"primitive":                "original vmsk raster plus existing pixel-mask Gaussian helper",
		"derived_mask_pixels_used": false, "coverage_expanded_outside_canvas": true}, jsontext.WithIndent("  "), json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join("..", "verification", reportName),
		append(encoded, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}

func vectorFeatherPrototypeCase(t *testing.T, id, path, native string) map[string]any {
	t.Helper()
	source, err := psdfile.Open(filepath.Join("..", path))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	var mask *psdvector.Mask
	requested, density := float64(0), byte(255)
	var layerIndex int
	for index, layer := range source.Layers {
		for _, block := range layer.AdditionalInfo {
			if block.Key != "vmsk" && block.Key != "vsms" {
				continue
			}
			if mask != nil {
				t.Fatal("controlled source has ambiguous vector masks")
			}
			mask, err = source.DecodeVectorMaskBlock(context.Background(), block)
			if err != nil {
				t.Fatal(err)
			}
			layerIndex = index
			if layer.Mask != nil {
				if layer.Mask.VectorFeather != nil {
					requested = *layer.Mask.VectorFeather
				}
				if layer.Mask.VectorDensity != nil {
					density = *layer.Mask.VectorDensity
				}
			}
		}
	}
	if mask == nil || requested == 0 && !mask.Disabled {
		t.Fatal("controlled source is missing actual vector feather metadata")
	}
	sigma := featherSigma(requested)
	radius := int(math.Ceil(sigma * 3))
	variant := os.Getenv("PSDKIT_VECTOR_FEATHER_VARIANT")
	if variant == "support-two" {
		radius = int(math.Ceil(requested * 2))
	} else if variant == "support-two-quarter" || variant == "core" {
		radius = int(math.Ceil(requested * 2.25))
	} else if variant != "" {
		t.Fatal("unknown controlled research variant")
	}
	canvas := image.Rect(0, 0, int(source.Header.Width), int(source.Header.Height))
	expanded := canvas.Inset(-radius)
	raw, err := psdvector.Rasterize(context.Background(), mask, expanded)
	if err != nil {
		t.Fatal(err)
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, expanded.Dx(), expanded.Dy()))
	for row := range expanded.Dy() {
		for column := range expanded.Dx() {
			offset := row*pixels.Stride + column*4
			pixels.Pix[offset] = raw.Pix[row*raw.Stride+column]
			pixels.Pix[offset+3] = 255
		}
	}
	defaultColor := byte(0)
	outside := mask.Invert
	if len(mask.Subpaths) == 0 {
		outside = mask.InitialFill != mask.Invert
	}
	if outside {
		defaultColor = 255
	}
	borrowed := &psdmodel.Mask{Bounds: expanded, Pixels: pixels, DefaultColor: &defaultColor}
	blur := &featherMask{mask: borrowed, bounds: canvas, canvas: canvas, radius: radius, sigma: sigma}
	if err := blur.prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := image.NewNRGBA(canvas)
	for row := range canvas.Dy() {
		for column := range canvas.Dx() {
			value := blur.values[row*canvas.Dx()+column]
			if mask.Disabled || density == 0 {
				value = 1
			} else {
				value = 1 - (1-value)*float64(density)/255
			}
			offset := row*result.Stride + column*4
			result.Pix[offset], result.Pix[offset+1], result.Pix[offset+2] = 255, 255, 255
			result.Pix[offset+3] = composeByte(value)
		}
	}
	if variant == "core" {
		snapshot, err := source.Decode(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result, err = Render(context.Background(), snapshot)
		if err != nil {
			t.Fatal(err)
		}
	}
	destination := "output/vector-feather-native/" + id + "-prototype.png"
	if variant != "" {
		destination = "output/vector-feather-native/" + id + "-" + variant + ".png"
	}
	file, err := os.Create(filepath.Join("..", destination))
	if err != nil {
		t.Fatal(err)
	}
	encodeErr, closeErr := png.Encode(file, result), file.Close()
	if encodeErr != nil || closeErr != nil {
		t.Fatalf("prototype PNG: %v, %v", encodeErr, closeErr)
	}
	sourceBytes, err := os.ReadFile(filepath.Join("..", path))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(sourceBytes)
	var sourceMaskFlags *uint8
	if source.Layers[layerIndex].Mask != nil {
		flags := source.Layers[layerIndex].Mask.Flags
		sourceMaskFlags = &flags
	}
	return map[string]any{"id": id, "source": path, "native_png": native, "prototype_png": destination,
		"source_sha256": hex.EncodeToString(hash[:]), "feather": requested, "density_raw_byte": density,
		"flags": mask.Flags, "initial_fill": mask.InitialFill, "sigma": sigma, "kernel_radius": radius,
		"research_variant": variant,
		"canvas":           canvas, "expanded_geometry_bounds": expanded, "physical_layer": layerIndex,
		"source_mask_flags": sourceMaskFlags, "derived_mask_pixels_used": false}
}
