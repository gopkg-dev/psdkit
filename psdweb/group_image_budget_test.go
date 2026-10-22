package psdweb

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"
)

func TestCompositeGroupBridgeRetainsAndReleasesFullBuffer(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = engine.reset() })
	opened := bridgeCall(t, engine, "document.create", "", 4, 4, 1).Data.(map[string]any)
	editor := opened["editor"].(string)
	created := bridgeCall(t, engine, "editor.AddGroup", editor, 0, 1, "isolated", true)
	group := created.Data.(map[string]any)["results"].([]any)[0]
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	expected := color.NRGBA{R: 211, G: 33, B: 77, A: 128}
	pixels.SetNRGBA(0, 0, expected)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	asset := bridgeAsset(t, engine, "input.png", encoded.Bytes())
	bridgeCall(t, engine, "editor.AddRaster", editor, group, 0, "pixel", assetRef(asset), image.Pt(2, 1))
	before := engine.valueBytes
	preview := bridgeCall(t, engine, "editor.CompositeGroup", editor, group, map[string]any{})
	if len(preview.Binaries) != 2 || preview.Binaries[0].MIME != "image/png" {
		t.Fatal("group did not produce a PNG and RGBA buffer")
	}
	decoded, err := png.Decode(bytes.NewReader(preview.Binaries[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds() != image.Rect(0, 0, 1, 1) || color.NRGBAModel.Convert(decoded.At(0, 0)) != expected {
		t.Fatal("group binary lost cropping, colors or transparency")
	}
	result := preview.Data.(map[string]any)["results"].([]any)[0].(map[string]any)
	handle := result["handle"].(string)
	if engine.resources[handle].retainedBytes != 64 || engine.valueBytes-before != 64 {
		t.Fatal("group result did not retain the full 4 by 4 buffer")
	}
	bridgeCall(t, engine, "resource.release", handle)
	if engine.valueBytes != before {
		t.Fatal("releasing group result did not restore the retained budget")
	}
}

func TestCroppedImageRetainedBudgetIncludesBackingCapacity(t *testing.T) {
	pixels := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	pixels.Pix = pixels.Pix[:4]
	pixels.Rect = image.Rect(0, 0, 1, 1)
	pixels.Stride = 4
	alpha := image.NewAlpha(image.Rect(0, 0, 8, 8))
	alpha.Pix = alpha.Pix[:1]
	alpha.Rect = image.Rect(0, 0, 1, 1)
	alpha.Stride = 1
	for name, value := range map[string]any{"RGBA": pixels, "Alpha": alpha} {
		t.Run(name, func(t *testing.T) {
			engine, err := New(Config{MaxBytes: 32})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := engine.keep(reflect.ValueOf(value), "", ""); !errors.Is(err, ErrLimit) {
				t.Fatal("cropped backing storage escaped budget", err)
			}
			engine, err = New(Config{MaxBytes: 64})
			if err != nil {
				t.Fatal(err)
			}
			id, err := engine.keep(reflect.ValueOf(value), "", "")
			if err != nil {
				t.Fatal(err)
			}
			if engine.valueBytes != 64 {
				t.Fatal("retained capacity not counted", engine.valueBytes)
			}
			if err := engine.release(id); err != nil || engine.valueBytes != 0 {
				t.Fatal("release did not restore capacity budget", err)
			}
		})
	}
}
