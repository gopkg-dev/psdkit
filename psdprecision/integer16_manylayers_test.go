package psdprecision_test

import (
	"context"
	"encoding/json/v2"
	"errors"
	"image"
	"os"
	"testing"

	"github.com/gopkg-dev/psdkit/psdprecision"
)

func manyLayerScene(count int) *psdprecision.Scene {
	scene := &psdprecision.Scene{Width: 5, Height: 2}
	bounds := []image.Rectangle{image.Rect(-1, 0, 2, 1), image.Rect(4, 1, 6, 2), image.Rect(1, 0, 3, 1)}
	for index := range count {
		rectangle := bounds[index%len(bounds)]
		pixels := &psdprecision.FloatImage{Width: rectangle.Dx(), Height: rectangle.Dy(), SourceDepth: 16}
		for pixel := range pixels.Width * pixels.Height {
			for component := range 4 {
				code := uint16((index*19483 + pixel*7919 + component*13277) & 65535)
				if component == 3 && (index+pixel)%7 == 0 {
					code = 0
				}
				pixels.Pix = append(pixels.Pix, float64(code)/65535)
			}
		}
		mode := "norm"
		if index > 0 && index%3 != 0 {
			mode = "diff"
		}
		opacity := float64(1+(index*73+31)%255) / 255
		scene.Layers = append(scene.Layers, &psdprecision.Layer{
			Bounds: rectangle, Pixels: pixels, BlendMode: mode, Opacity: &opacity,
		})
	}
	return scene
}

func TestRGB16ManyLayersIndependentBigIntAndROI(t *testing.T) {
	checks := 0
	for _, count := range []int{5, 9, 31, 127, 4096} {
		scene := manyLayerScene(count)
		full, err := psdprecision.RenderRGB16(nil, scene)
		if err != nil {
			t.Fatal(err)
		}
		for y := range scene.Height {
			for x := range scene.Width {
				expected := geometryExpected(scene, image.Pt(x, y))
				for component, value := range expected {
					if full.Pix[(y*scene.Width+x)*4+component]*32768 != float64(value) {
						t.Fatalf("layers=%d (%d,%d) component=%d differs", count, x, y, component)
					}
					checks++
				}
			}
		}
		for _, rectangle := range []image.Rectangle{image.Rect(0, 0, 2, 1), image.Rect(2, 0, 4, 2),
			image.Rect(4, 1, 5, 2), image.Rect(0, 0, 0, 2)} {
			region, err := psdprecision.RenderRGB16Region(nil, scene, rectangle)
			if err != nil {
				t.Fatal(err)
			}
			for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
				for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
					for component := range 4 {
						if region.Image.Pix[((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4+component] !=
							full.Pix[(y*scene.Width+x)*4+component] {
							t.Fatal("many-layer ROI changed global first or BBox initialization")
						}
						checks++
					}
				}
			}
		}
	}
	t.Logf("independent big.Int and ROI components=%d; 127/4096 controls are mathematical only", checks)
}

func TestRGB16ManyLayersQuotaWholeInputsAndCancellation(t *testing.T) {
	scene := manyLayerScene(31)
	for _, quota := range []int{30, 31, 32} {
		result, err := psdprecision.RenderRGB16(nil, scene, psdprecision.Options{MaxLayers: quota})
		if quota < 31 {
			if result != nil || !errors.Is(err, psdprecision.ErrLimit) {
				t.Fatalf("layer quota %d exposed output: %v", quota, err)
			}
		} else if err != nil || result == nil {
			t.Fatalf("layer quota %d rejected: %v", quota, err)
		}
	}
	for _, quota := range []int64{31, 32, 33} {
		result, err := psdprecision.RenderRGB16Region(nil, scene, image.Rect(4, 1, 5, 2),
			psdprecision.Options{MaxWorkingBytes: quota})
		if quota < 32 {
			if result != nil || !errors.Is(err, psdprecision.ErrLimit) {
				t.Fatalf("output quota %d exposed result: %v", quota, err)
			}
		} else if result == nil || err != nil {
			t.Fatalf("output quota %d rejected: %v", quota, err)
		}
	}
	deep := manyLayerScene(4096)
	for _, quota := range []int{4095, 4096, 4097} {
		result, err := psdprecision.RenderRGB16Region(nil, deep, image.Rect(4, 1, 5, 2),
			psdprecision.Options{MaxLayers: quota, MaxWorkingBytes: 32})
		if quota < 4096 {
			if result != nil || !errors.Is(err, psdprecision.ErrLimit) {
				t.Fatalf("deep layer quota %d exposed output: %v", quota, err)
			}
		} else if err != nil || result == nil {
			t.Fatalf("deep exact/plus-one layer quota rejected: %v", err)
		}
	}
	deepProbe := &normalFinalCancellation{Context: context.Background()}
	if _, err := psdprecision.RenderRGB16(deepProbe, deep); err != nil {
		t.Fatal(err)
	}
	deepParent, deepCancel := context.WithCancel(context.Background())
	deepContext := &normalFinalCancellation{Context: deepParent, cancel: deepCancel, after: deepProbe.calls}
	if result, err := psdprecision.RenderRGB16(deepContext, deep); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("deep final cancellation exposed output: %v", err)
	}
	hidden := &psdprecision.Layer{Hidden: true, Children: []*psdprecision.Layer{manyLayerScene(1).Layers[0]}}
	withHiddenTree := manyLayerScene(31)
	withHiddenTree.Layers = append(withHiddenTree.Layers, hidden)
	if result, err := psdprecision.RenderRGB16(
		nil, withHiddenTree, psdprecision.Options{MaxLayers: 32},
	); result != nil || !errors.Is(err, psdprecision.ErrLimit) {
		t.Fatalf("hidden descendant escaped whole-tree layer quota: %v", err)
	}
	for _, depth := range []int{1, 2, 3} {
		result, err := psdprecision.RenderRGB16(nil, withHiddenTree,
			psdprecision.Options{MaxLayers: 33, MaxDepth: depth})
		if depth == 1 {
			if result != nil || !errors.Is(err, psdprecision.ErrLimit) {
				t.Fatalf("hidden descendant escaped depth quota: %v", err)
			}
		} else if result == nil || err != nil {
			t.Fatalf("sufficient hidden-tree depth rejected: %v", err)
		}
	}
	probe := &normalFinalCancellation{Context: context.Background()}
	if _, err := psdprecision.RenderRGB16(probe, scene); err != nil {
		t.Fatal(err)
	}
	for checkpoint := 1; checkpoint <= probe.calls; checkpoint++ {
		parent, cancel := context.WithCancelCause(context.Background())
		cause := errors.New("many-layer checkpoint cause")
		ctx := &normalFinalCancellation{Context: parent, cancel: func() { cancel(cause) }, after: checkpoint}
		result, err := psdprecision.RenderRGB16(ctx, scene)
		if result != nil || !errors.Is(err, context.Canceled) || !errors.Is(context.Cause(parent), cause) {
			t.Fatalf("checkpoint %d exposed partial result: %v", checkpoint, err)
		}
	}
	scene.Layers[30].Pixels.Pix[0] = .5
	for _, rectangle := range []image.Rectangle{image.Rect(4, 1, 5, 2), image.Rect(0, 0, 0, 2)} {
		if result, err := psdprecision.RenderRGB16Region(nil, scene, rectangle); result != nil ||
			!errors.Is(err, psdprecision.ErrUnsupported) {
			t.Fatalf("ROI hid last-layer noncanonical off-window sample: %v", err)
		}
	}
	t.Logf("every actual external ctx check canceled=%d", probe.calls)
}

type manyLayerRegistered struct {
	Cases []struct {
		Width, Height int
		Layers        []struct {
			Bounds      [4]int
			Mode        string
			OpacityByte byte        `json:"opacity_byte"`
			RawCodes    [][4]uint16 `json:"raw_codes"`
		}
		ROIs        [][4]int            `json:"rois"`
		Predictions map[string][][4]int `json:"predictions"`
	}
}

func TestRGB16ManyLayersRegisteredProspectiveControls(t *testing.T) {
	encoded, err := os.ReadFile("testdata/manylayers/input-binding.json")
	if err != nil {
		t.Fatal(err)
	}
	var inputs manyLayerRegistered
	if err := json.Unmarshal(encoded, &inputs, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatal(err)
	}
	checks := 0
	for _, item := range inputs.Cases {
		scene := &psdprecision.Scene{Width: item.Width, Height: item.Height}
		for _, layer := range item.Layers {
			pixels := &psdprecision.FloatImage{Width: layer.Bounds[2] - layer.Bounds[0],
				Height: layer.Bounds[3] - layer.Bounds[1], SourceDepth: 16}
			for _, raw := range layer.RawCodes {
				for _, code := range raw {
					pixels.Pix = append(pixels.Pix, float64(code)/65535)
				}
			}
			opacity := float64(layer.OpacityByte) / 255
			scene.Layers = append(scene.Layers, &psdprecision.Layer{Pixels: pixels, BlendMode: layer.Mode,
				Opacity: &opacity, Bounds: image.Rect(layer.Bounds[0], layer.Bounds[1], layer.Bounds[2], layer.Bounds[3])})
		}
		expected := item.Predictions["global-bbox-first-active-adjusted-difference"]
		for _, coordinates := range item.ROIs {
			rectangle := image.Rect(coordinates[0], coordinates[1], coordinates[2], coordinates[3])
			result, err := psdprecision.RenderRGB16Region(nil, scene, rectangle)
			if err != nil {
				t.Fatal(err)
			}
			for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
				for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
					for component := range 4 {
						code := expected[y*item.Width+x][component]
						got := result.Image.Pix[((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4+component]
						if got*32768 != float64((code+1)/2) {
							t.Fatal("registered prospective Fraction/big.Int prediction differs")
						}
						checks++
					}
				}
			}
		}
	}
	t.Logf("prospective mathematical components=%d; native execution remains pending", checks)
}
