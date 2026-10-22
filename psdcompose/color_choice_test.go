package psdcompose

import "testing"

func TestWholeColorChoiceNativeHalfIntegerBoundaries(t *testing.T) {
	values := []struct {
		mode             string
		backdrop, source [3]uint8
		chooseSource     bool
	}{
		{"dkCl", [3]uint8{230, 194, 106}, [3]uint8{153, 227, 135}, false},
		{"dkCl", [3]uint8{240, 214, 28}, [3]uint8{222, 202, 138}, false},
		{"lgCl", [3]uint8{92, 48, 58}, [3]uint8{132, 28, 58}, false},
		{"lgCl", [3]uint8{184, 35, 215}, [3]uint8{224, 15, 215}, true},
		{"lgCl", [3]uint8{201, 107, 137}, [3]uint8{241, 87, 137}, true},
	}
	for _, fixture := range values {
		var backdrop, source [3]float64
		for component := range backdrop {
			backdrop[component] = float64(fixture.backdrop[component]) / 255
			source[component] = float64(fixture.source[component]) / 255
		}
		actual, err := blendRGB(fixture.mode, backdrop, source, 1)
		if err != nil {
			t.Fatal(err)
		}
		wanted := backdrop
		if fixture.chooseSource {
			wanted = source
		}
		if actual != wanted {
			t.Fatalf("native %s decision for %v/%v: actual %v, want %v",
				fixture.mode, fixture.backdrop, fixture.source, actual, wanted)
		}
	}
}
