package psdfilter

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdadjust"
)

func brightnessFilter(brightness, contrast int16) Filter {
	return Filter{Kind: BrightnessContrast, Enabled: true, Opacity: 1, BlendMode: "norm",
		Adjustment: &psdadjust.Adjustment{Kind: psdadjust.BrightnessContrast,
			BrightnessContrast: &psdadjust.BrightnessContrastParams{
				Brightness: brightness, Contrast: contrast, Mean: 128,
			}}}
}

func hueFilter(parameters *psdadjust.HueSaturationParams) Filter {
	return Filter{Kind: HueSaturation, Enabled: true, Opacity: 1, BlendMode: "norm",
		Adjustment: &psdadjust.Adjustment{Kind: psdadjust.HueSaturation, HueSaturation: parameters}}
}

func TestColorFilterModernDeclaredCurveSignedControlsAndEndpoints(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 5, 1))
	for pixel, value := range []uint8{0, 64, 128, 192, 255} {
		copy(source.Pix[pixel*4:pixel*4+4], []uint8{value, value, value, 255})
	}
	fixtures := []struct {
		brightness, contrast int16
		expected             [5]uint8
	}{
		{75, 0, [5]uint8{0, 102, 170, 219, 255}},
		{-75, 0, [5]uint8{0, 37, 85, 154, 255}},
		{0, 50, [5]uint8{0, 32, 128, 224, 255}},
	}
	for _, fixture := range fixtures {
		stack := Stack{Enabled: true, Filters: []Filter{brightnessFilter(fixture.brightness, fixture.contrast)}}
		result, err := Apply(nil, source, stack, Options{ApproximateBrightnessContrast: true})
		if err != nil {
			t.Fatal(err)
		}
		for pixel, expected := range fixture.expected {
			if got := result.Pix[pixel*4 : pixel*4+4]; !bytes.Equal(got, []uint8{expected, expected, expected, 255}) {
				t.Fatalf("brightness %d contrast %d sample %d: %v", fixture.brightness, fixture.contrast, pixel, got)
			}
		}
	}
	for _, brightness := range []int16{-150, -35, 0, 2, 24, 150} {
		for _, contrast := range []int16{-50, 0, 11, 34, 100} {
			previous := -1.0
			for value := range 256 {
				actual := approximateBrightnessContrast(float64(value)/255, psdadjust.BrightnessContrastParams{
					Brightness: brightness, Contrast: contrast,
				})
				if actual < previous || actual < 0 || actual > 1 {
					t.Fatalf("non-monotone or unbounded curve: brightness %d contrast %d value %d", brightness, contrast, value)
				}
				previous = actual
			}
		}
	}
}

func TestColorFilterOpacityAlphaStridedOwnershipAndHiddenColor(t *testing.T) {
	container := image.NewNRGBA(image.Rect(-9, -4, 5, 6))
	source := container.SubImage(image.Rect(-3, -1, 1, 0)).(*image.NRGBA)
	for index, alpha := range []uint8{1, 128, 255, 0} {
		source.SetNRGBA(source.Rect.Min.X+index, source.Rect.Min.Y, color.NRGBA{64, 128, 192, alpha})
	}
	before := bytes.Clone(container.Pix)
	filter := brightnessFilter(75, 0)
	filter.Opacity = .25
	stack := Stack{Enabled: true, Filters: []Filter{filter}}
	options := Options{ApproximateBrightnessContrast: true}
	result, err := Apply(nil, source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rect != source.Rect || !bytes.Equal(before, container.Pix) {
		t.Fatal("color filter changed the input pixels or rectangle")
	}
	for index, alpha := range []uint8{1, 128, 255, 0} {
		expected := color.NRGBA{74, 139, 199, alpha}
		if alpha == 0 {
			expected = color.NRGBA{64, 128, 192, 0}
		}
		if actual := result.NRGBAAt(source.Rect.Min.X+index, source.Rect.Min.Y); actual != expected {
			t.Fatalf("opacity sample %d: %v != %v", index, actual, expected)
		}
	}
	result.Pix[0] ^= 255
	if !bytes.Equal(before, container.Pix) {
		t.Fatal("color result aliases caller pixels")
	}
}

func TestColorFilterHueNativeSamplesAndRealTemplateRanges(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	copy(source.Pix, []byte{166, 122, 89, 128, 255, 85, 0, 255})
	parameters := &psdadjust.HueSaturationParams{}
	parameters.Ranges[0] = psdadjust.HueSaturationRange{Bounds: [4]int16{315, 345, 15, 45},
		Values: psdadjust.HueSaturationValues{Saturation: 60}}
	stack := Stack{Enabled: true, Filters: []Filter{hueFilter(parameters)}}
	result, err := Apply(nil, source, stack, Options{ApproximateHueSaturation: true})
	if err != nil {
		t.Fatal(err)
	}
	// This independent Photoshop RGB8 witness is shared with the adjustment engine's native controls.
	for component, value := range []byte{202, 116, 52, 128} {
		actual := int(result.Pix[component])
		if actual < int(value)-1 || actual > int(value)+1 {
			t.Fatalf("native range sample component %d: %d != %d", component, actual, value)
		}
	}
	parameters = &psdadjust.HueSaturationParams{}
	parameters.Ranges[4] = psdadjust.HueSaturationRange{Bounds: [4]int16{195, 225, 255, 285},
		Values: psdadjust.HueSaturationValues{Saturation: -100}}
	parameters.Ranges[1] = psdadjust.HueSaturationRange{Bounds: [4]int16{15, 45, 75, 105},
		Values: psdadjust.HueSaturationValues{Saturation: -54}}
	copy(source.Pix, []byte{0, 0, 255, 128, 255, 85, 0, 255})
	stack.Filters[0] = hueFilter(parameters)
	result, err = Apply(nil, source, stack, Options{ApproximateHueSaturation: true})
	if err != nil {
		t.Fatal(err)
	}
	blue := result.NRGBAAt(0, 0)
	if blue.R != blue.G || blue.G != blue.B || blue.A != 128 {
		t.Fatalf("real blue range did not desaturate or changed alpha: %v", blue)
	}
	orange := result.NRGBAAt(1, 0)
	if orange.R == 255 || orange.B == 0 || orange.A != 255 {
		t.Fatalf("real yellow range was ignored: %v", orange)
	}
}

func TestColorFilterLegacyPivotNeutralPolicyAndStrictValidation(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(source.Pix, []byte{64, 128, 192, 255})
	neutral := brightnessFilter(0, 0)
	neutralResult, neutralErr := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{neutral}})
	if neutralErr != nil || !bytes.Equal(neutralResult.Pix, source.Pix) {
		t.Fatalf("neutral modern filter requires approximation or changed pixels: %v", neutralErr)
	}
	legacy := brightnessFilter(0, 50)
	legacy.Adjustment.BrightnessContrast.Legacy = true
	result, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{legacy}},
		Options{ApproximateBrightnessContrast: true})
	if err != nil || !bytes.Equal(result.Pix, []byte{0, 128, 255, 255}) {
		t.Fatalf("legacy explicit pivot was ignored: %v, %v", result, err)
	}
	invalid := []Filter{
		brightnessFilter(151, 0), brightnessFilter(-151, 0), brightnessFilter(0, -51), brightnessFilter(0, 101),
		{Kind: BrightnessContrast},
		{Kind: HueSaturation, Adjustment: &psdadjust.Adjustment{Kind: psdadjust.BrightnessContrast}},
		hueFilter(&psdadjust.HueSaturationParams{Master: psdadjust.HueSaturationValues{Hue: 181}}),
	}
	for _, filter := range invalid {
		if err := colorFilterValidate(filter, Options{ApproximateBrightnessContrast: true,
			ApproximateHueSaturation: true}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid controls accepted: %v", err)
		}
	}
	for _, filter := range []Filter{brightnessFilter(1, 0),
		hueFilter(&psdadjust.HueSaturationParams{Master: psdadjust.HueSaturationValues{Saturation: -100}})} {
		if err := colorFilterValidate(filter, Options{}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("non-neutral policy was implicit: %v", err)
		}
	}
	legacy.Adjustment.BrightnessContrast.LabOnly = true
	if err := colorFilterValidate(legacy, Options{ApproximateBrightnessContrast: true}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Lab-only semantics ignored: %v", err)
	}
	neutral.Adjustment.Unsupported = []string{"source unknown field"}
	if err := colorFilterValidate(neutral, Options{ApproximateBrightnessContrast: true}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported source semantics ignored: %v", err)
	}
}

func TestColorFilterSequentialOrderBudgetsAndCancellation(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for offset := 0; offset < len(source.Pix); offset += 4 {
		copy(source.Pix[offset:offset+4], []byte{166, 122, 89, 128})
	}
	hue := hueFilter(&psdadjust.HueSaturationParams{Master: psdadjust.HueSaturationValues{Saturation: 40}})
	brightness := brightnessFilter(-35, 34)
	stack := Stack{Enabled: true, Filters: []Filter{hue, brightness}}
	options := Options{ApproximateBrightnessContrast: true, ApproximateHueSaturation: true}
	chain, err := Apply(nil, source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{hue}}, options)
	if err != nil {
		t.Fatal(err)
	}
	sequential, err := Apply(nil, first, Stack{Enabled: true, Filters: []Filter{brightness}}, options)
	if err != nil || !bytes.Equal(chain.Pix, sequential.Pix) || bytes.Equal(chain.Pix, source.Pix) {
		t.Fatalf("zero-radius color stack was skipped or reordered: %v", err)
	}
	reversed, err := Apply(nil, source, Stack{Enabled: true, Filters: []Filter{brightness, hue}}, options)
	if err != nil || bytes.Equal(chain.Pix, reversed.Pix) {
		t.Fatalf("noncommuting controls did not preserve stored order: %v", err)
	}
	estimate, err := WorkingEstimate(source, stack, options)
	if err != nil {
		t.Fatal(err)
	}
	options.MaxWorkingBytes = estimate.WorkingBytes
	if _, err := Apply(nil, source, stack, options); err != nil {
		t.Fatal(err)
	}
	options.MaxWorkingBytes--
	if pixels, err := Apply(nil, source, stack, options); pixels != nil || !errors.Is(err, ErrLimit) {
		t.Fatalf("minus-one color budget: %v, %v", pixels, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if pixels, err := Apply(ctx, source, stack, options); pixels != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("color filter cancellation: %v, %v", pixels, err)
	}
}
