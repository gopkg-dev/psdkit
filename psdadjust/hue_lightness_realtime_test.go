package psdadjust

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"testing"
)

func TestPlainMasterLightnessDefaultMatchesEveryNativeGrayControl(t *testing.T) {
	data, err := os.ReadFile("testdata/native_master_lightness.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Cases []struct {
			Control int16  `json:"lightness"`
			Pixels  string `json:"nrgba_sha256"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) != 201 {
		t.Fatal("native sweep does not cover every integer lightness control")
	}
	source := image.NewNRGBA(image.Rect(0, 0, 256, 1))
	for value := range 256 {
		copy(source.Pix[value*4:], []byte{byte(value), byte(value), byte(value), 255})
	}
	original := bytes.Clone(source.Pix)
	for index, entry := range fixture.Cases {
		if entry.Control != int16(index-100) {
			t.Fatal("native sweep omitted or duplicated a control")
		}
		parameters := &HueSaturationParams{Master: HueSaturationValues{Lightness: entry.Control}}
		adjustment := Adjustment{Kind: HueSaturation, HueSaturation: parameters}
		output, err := Apply(nil, source, adjustment)
		if err != nil {
			t.Fatalf("default control %d: %v", entry.Control, err)
		}
		if actual := fmt.Sprintf("%x", sha256.Sum256(output.Pix)); actual != entry.Pixels {
			t.Fatalf("control %d does not match independent native grayscale bytes", entry.Control)
		}
		if !bytes.Equal(source.Pix, original) || output == source {
			t.Fatal("default lightness borrowed output or changed source bytes")
		}
	}
}

func TestPlainMasterLightnessDefaultRemainsBoundedAndOwnsParameters(t *testing.T) {
	parameters := &HueSaturationParams{Master: HueSaturationValues{Lightness: 51}}
	transform, err := Compile(Adjustment{Kind: HueSaturation, HueSaturation: parameters})
	if err != nil {
		t.Fatal(err)
	}
	parameters.Master.Lightness = -97
	if actual := transform.RGB([3]float64{1, 128.0 / 255, 0}); actual != ([3]float64{1, 193.0 / 255, 130.0 / 255}) {
		t.Fatalf("compiled default lightness borrowed parameters: %v", actual)
	}
	source := adjustmentPixels(255, 128, 0, 128)
	if _, err := Apply(nil, source, Adjustment{Kind: HueSaturation, HueSaturation: parameters},
		Options{MaxWorkingBytes: 16 << 10}); !errors.Is(err, ErrLimit) {
		t.Fatalf("default lightness bypassed the transform memory budget: %v", err)
	}
	for _, modify := range []func(*HueSaturationParams){
		func(value *HueSaturationParams) { value.Master.Hue = 1 },
		func(value *HueSaturationParams) { value.Master.Saturation = 1 },
		func(value *HueSaturationParams) { value.Ranges[0].Values.Lightness = 1 },
		func(value *HueSaturationParams) { value.Ranges[1].Values.Hue = 1 },
		func(value *HueSaturationParams) { value.Ranges[5].Values.Saturation = 1 },
		func(value *HueSaturationParams) { value.Colorize = true },
	} {
		variant := HueSaturationParams{Master: HueSaturationValues{Lightness: 51}}
		modify(&variant)
		if _, err := Compile(Adjustment{Kind: HueSaturation, HueSaturation: &variant}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("mixed, local or colorized parameters escaped the approximation policy: %v", err)
		}
	}
}
