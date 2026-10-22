package psdweb

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func TestPreviewPNGPreservesCroppedRGBAAndTransparentColors(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	frame := image.NewNRGBA(image.Rect(0, 0, 23, 19))
	for index := range frame.Pix {
		frame.Pix[index] = byte(index*37 + index/17)
	}
	source := frame.SubImage(image.Rect(3, 4, 21, 17)).(*image.NRGBA)
	for y := source.Rect.Min.Y; y < source.Rect.Max.Y; y++ {
		source.Pix[(y-source.Rect.Min.Y)*source.Stride+3] = 0
	}
	before := bytes.Clone(frame.Pix)
	if _, err := engine.exportImage(source); err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(engine.binaries[0].Data))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Bounds().Size() != source.Bounds().Size() {
		t.Fatal("preview PNG dimensions changed")
	}
	for y := range source.Rect.Dy() {
		for x := range source.Rect.Dx() {
			expected := source.NRGBAAt(source.Rect.Min.X+x, source.Rect.Min.Y+y)
			if color.NRGBAModel.Convert(decoded.At(x, y)) != expected {
				t.Fatalf("preview PNG changed RGBA at %d,%d", x, y)
			}
		}
	}
	if !bytes.Equal(frame.Pix, before) {
		t.Fatal("preview encoding mutated borrowed pixels")
	}
}

func TestPNGOnlyRequestPreservesPixelsAndDefaultTransfers(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	opened := bridgeCall(t, engine, "demo.create", "", 32, 24).Data.(map[string]any)
	editor := opened["editor"].(string)
	standard := bridgeCall(t, engine, "editor.Composite", editor)
	only := engine.Execute(t.Context(), Request{
		Operation: "editor.Composite", Target: editor, ImageOutput: "png",
	}, nil)
	if !only.OK || len(only.Binaries) != 1 || !bytes.Equal(only.Binaries[0].Data, standard.Binaries[0].Data) {
		t.Fatal("PNG-only output changed pixels or included duplicate RGBA:", only.Error)
	}
	results := only.Data.(map[string]any)["results"].([]any)
	value := results[0].(map[string]any)["value"].(map[string]any)
	if _, exists := value["rgba"]; exists {
		t.Fatal("PNG-only metadata advertised an absent raw buffer")
	}
	if len(bridgeCall(t, engine, "editor.Composite", editor).Binaries) != 2 {
		t.Fatal("per-request transfer selection leaked into later calls")
	}
	invalid := engine.Execute(t.Context(), Request{Operation: "system.state", ImageOutput: "jpeg"}, nil)
	if invalid.OK || invalid.Error.Code != "invalid_request" {
		t.Fatal("unknown transfer selection was accepted")
	}
	bridgeCall(t, engine, "resource.release", opened["document"].(string))
	if len(engine.resources) != 0 {
		t.Fatal("PNG-only request leaked resources")
	}
}
