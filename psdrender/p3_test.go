package psdrender

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func TestP3TreeSmartReplacementRetainsGroupAndSource(t *testing.T) {
	groupOpacity := .5
	original := solid(1, 1, 0, 0, 200, 255)
	placed := &Placed{
		ID: "test-object", Type: "raster", Width: 1, Height: 1,
		Transform: []float64{0, 0, 1, 0, 1, 1, 0, 1},
	}
	leaf := &Layer{Name: "Replacement", Bounds: image.Rect(0, 0, 1, 1), Pixels: original,
		Placed: placed, BlendMode: "normal"}
	document := &Document{Width: 1, Height: 1, Layers: []*Layer{
		{Bounds: image.Rect(0, 0, 1, 1), Pixels: solid(1, 1, 100, 100, 100, 255)},
		{Name: "Group", Children: []*Layer{leaf}, Opacity: &groupOpacity, BlendMode: "normal"},
	}}
	engine, err := New(document)
	if err != nil {
		t.Fatal(err)
	}
	metadata := engine.Metadata()
	if len(metadata.Objects) != 1 || len(metadata.Objects[0].Instances) != 1 ||
		len(metadata.BackgroundLayers) != 0 || len(metadata.BlockingIssues) != 0 {
		t.Fatalf("unexpected tree capabilities: %+v", metadata)
	}
	config := DefaultConfig(metadata)
	result, err := engine.Render(context.Background(), config,
		map[string]*image.NRGBA{config.Objects[0].Tag: solid(1, 1, 0, 200, 0, 255)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Pix, []byte{50, 150, 50, 255}) {
		t.Fatalf("tree replacement = %v", result.Pix)
	}
	if leaf.Pixels != original || !bytes.Equal(original.Pix, []byte{0, 0, 200, 255}) || leaf.Placed != placed {
		t.Fatal("tree replacement modified the source")
	}
}

func TestP3RendererRejectsCyclesBeforeMetadataTraversal(t *testing.T) {
	group := &Layer{Group: true, BlendMode: "pass"}
	group.Children = []*Layer{group}
	_, err := Inspect(&Document{Width: 1, Height: 1, Layers: []*Layer{group}})
	if err == nil {
		t.Fatal("cycle accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	document := &Document{Width: 1, Height: 1, Layers: []*Layer{
		{Group: true, BlendMode: "pass", Children: []*Layer{
			{Bounds: image.Rect(0, 0, 1, 1), Pixels: solid(1, 1, 1, 2, 3, 255)},
		}},
	}}
	engine, err := New(document)
	if err != nil {
		t.Fatal(err)
	}
	_, err = engine.Render(ctx, Config{Fit: "fill", Align: "center"}, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tree render: %v", err)
	}
}

func TestP3ProjectionRetainsRevealedMaskPixels(t *testing.T) {
	for _, invert := range []bool{true, false} {
		density := uint8(128)
		mask := &Mask{Bounds: image.Rect(0, 0, 1, 1), Pixels: solid(1, 1, 0, 0, 0, 255), Invert: invert}
		if !invert {
			mask.Density = &density
		}
		leaf := &Layer{Name: "Object", Bounds: image.Rect(0, 0, 1, 1),
			Pixels: solid(1, 1, 100, 0, 0, 255), Mask: mask,
			Placed: &Placed{ID: "shared", Type: "raster", Width: 1, Height: 1,
				Transform: []float64{0, 0, 1, 0, 1, 1, 0, 1}}}
		document := &Document{Width: 1, Height: 1, Layers: []*Layer{{Children: []*Layer{leaf}}}}
		engine, err := New(document)
		if err != nil {
			t.Fatal(err)
		}
		config := DefaultConfig(engine.Metadata())
		result, err := engine.Render(context.Background(), config,
			map[string]*image.NRGBA{config.Objects[0].Tag: solid(1, 1, 0, 200, 0, 255)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		expectedAlpha := byte(255)
		if !invert {
			expectedAlpha = 127
		}
		if !bytes.Equal(result.Pix, []byte{0, 200, 0, expectedAlpha}) {
			t.Fatalf("revealed projected pixel = %v", result.Pix)
		}
	}
}

func TestP3HiddenSharedObjectDoesNotProjectInvalidGeometry(t *testing.T) {
	visible := &Layer{Name: "Visible", Bounds: image.Rect(0, 0, 1, 1), Pixels: solid(1, 1, 1, 2, 3, 255),
		Placed: &Placed{ID: "shared", Type: "raster", Width: 1, Height: 1,
			Transform: []float64{0, 0, 1, 0, 1, 1, 0, 1}}}
	hidden := *visible
	hidden.Placed = &Placed{ID: "shared", Type: "raster", Transform: []float64{math.NaN()}}
	document := &Document{Width: 1, Height: 1, Layers: []*Layer{
		{Hidden: true, Children: []*Layer{&hidden}}, visible,
	}}
	engine, err := New(document)
	if err != nil {
		t.Fatal(err)
	}
	config := DefaultConfig(engine.Metadata())
	result, err := engine.Render(context.Background(), config,
		map[string]*image.NRGBA{config.Objects[0].Tag: solid(1, 1, 20, 30, 40, 255)}, nil)
	if err != nil || !bytes.Equal(result.Pix, []byte{20, 30, 40, 255}) {
		t.Fatalf("hidden shared instance affected replacement: %v, %v", result, err)
	}
}

func TestP3MalformedStrideRejectsBeforeSampling(t *testing.T) {
	pixels := &image.NRGBA{Rect: image.Rect(0, 0, 1, 2), Stride: math.MaxInt, Pix: make([]byte, 8)}
	if _, err := FitImage(pixels, 2, 3, "fill", "center"); err == nil {
		t.Fatal("overflowing stride accepted")
	}
}
