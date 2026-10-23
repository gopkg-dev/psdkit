package psdeffects

import (
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func TestZeroOpacityKnownNoiseDoesNotRequireSampling(t *testing.T) {
	for _, family := range []Kind{DropShadow, OuterGlow, InnerShadow, InnerGlow, Stroke, ColorOverlay} {
		t.Run(string(family), func(t *testing.T) {
			effect := primitive(family)
			effect.Opacity, effect.Noise, effect.Jitter = 0, .25, .5
			set := primitiveSet(effect)
			bounds := image.Rect(-2, 3, 4, 8)
			settings := Options{MaxWorkingBytes: 1, MaxSampleWork: 1}
			estimate, err := EstimateWorking(bounds, set, settings)
			if err != nil || estimate.Planes != 0 || estimate.OutputBytes != 0 || estimate.SampleWork != 0 {
				t.Fatalf("invisible noise requested coverage sampling: %+v, %v", estimate, err)
			}
			output, err := Rasterize(t.Context(), rectangleAlpha(bounds, 255), set, settings)
			if err != nil || len(output.Background)+len(output.Interior)+len(output.Foreground) != 0 {
				t.Fatalf("invisible noise produced effect coverage: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if output, err := Rasterize(ctx, rectangleAlpha(bounds, 255), set); output != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("zero-opacity effect ignored cancellation: %v", err)
			}
			set.Items[0].Opacity = .01
			if err := Validate(set); !errors.Is(err, ErrUnsupported) {
				t.Fatalf("active noise escaped the algorithm restriction: %v", err)
			}
		})
	}
}

func TestZeroOpacityNoiseKeepsUnknownAndInvalidParametersRejected(t *testing.T) {
	for _, change := range []func(*Effect){
		func(effect *Effect) { effect.Unsupported = []string{"ambiguous opacity"} },
		func(effect *Effect) { effect.Kind = "future" },
		func(effect *Effect) { effect.BlendMode = "invalid" },
	} {
		effect := primitive(DropShadow)
		effect.Opacity, effect.Noise = 0, .25
		change(&effect)
		if err := Validate(primitiveSet(effect)); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("zero-opacity noise hid unsupported fields: %v", err)
		}
	}
	for _, change := range []func(*Effect){
		func(effect *Effect) { effect.Noise = math.NaN() },
		func(effect *Effect) { effect.Jitter = math.Inf(1) },
		func(effect *Effect) { effect.Noise = -.1 },
		func(effect *Effect) { effect.Jitter = 1.1 },
		func(effect *Effect) { effect.Size = -1 },
	} {
		effect := primitive(DropShadow)
		effect.Opacity, effect.Noise = 0, .25
		change(&effect)
		if err := Validate(primitiveSet(effect)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("zero-opacity noise hid invalid fields: %v", err)
		}
	}
	for _, duplicate := range []bool{false, true} {
		descriptor := shadowDescriptor(true)
		descriptor.Entries[2].Value = Unit{Unit: "#Prc", Number: 0}
		if duplicate {
			descriptor.Entries = append(descriptor.Entries, descriptor.Entries[2])
		} else {
			descriptor.Entries[2].Value = Unit{Unit: "#Pxl", Number: 0}
		}
		descriptor.Entries = append(descriptor.Entries, Entry{Key: "Nose", Type: "UntF",
			Value: Unit{Unit: "#Prc", Number: 25}})
		set, err := Decode(t.Context(), &Descriptor{Entries: []Entry{{Key: "DrSh", Type: "Objc", Value: descriptor}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(set); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("ambiguous zero-opacity descriptor became a no-op: %v", err)
		}
	}
}
