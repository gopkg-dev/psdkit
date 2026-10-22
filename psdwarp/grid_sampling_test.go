package psdwarp

import (
	"image"
	"image/color"
	"math"
	"testing"
)

func TestGridKeysWeightsMatchDistancePolynomial(t *testing.T) {
	for _, tension := range []float64{0, -0.75} {
		for step := range 257 {
			phase := float64(step) / 256
			weights := gridKeysWeights(phase, tension)
			var sum float64
			for index, weight := range weights {
				expected := gridOracleKeysDistance(phase-float64(index-1), tension)
				if math.Abs(weight-expected) > 2e-14 {
					t.Fatalf("tension=%v phase=%v tap=%d: %v != %v", tension, phase, index, weight, expected)
				}
				sum += weight
			}
			if math.Abs(sum-1) > 2e-14 {
				t.Fatalf("tension=%v phase=%v weights sum to %v", tension, phase, sum)
			}
		}
	}
	if got := gridKeysWeights(0.5, -0.75); got != [4]float64{-0.09375, 0.59375, 0.59375, -0.09375} {
		t.Fatalf("half-phase alpha weights = %v", got)
	}
	if got := gridKeysWeights(0.5, 0); got != [4]float64{0, 0.5, 0.5, 0} {
		t.Fatalf("half-phase color weights = %v", got)
	}
}

func TestGridRGBKeys0MatchesIndependentWholeSourceOracle(t *testing.T) {
	backing := image.NewNRGBA(image.Rect(-19, -13, 12, 16))
	for y := backing.Rect.Min.Y; y < backing.Rect.Max.Y; y++ {
		for x := backing.Rect.Min.X; x < backing.Rect.Max.X; x++ {
			backing.SetNRGBA(x, y, color.NRGBA{R: byte(x*31 + y*13), G: byte(x*7 - y*19),
				B: byte(x*17 + y*29), A: byte((x + y) * 37)})
		}
	}
	source := backing.SubImage(image.Rect(-11, -6, -4, -1)).(*image.NRGBA)
	before := append([]byte(nil), backing.Pix...)
	for _, premultiplied := range []bool{false, true} {
		for row := -8; row <= 28; row++ {
			for column := -8; column <= 36; column++ {
				u, v := float64(column)/28, float64(row)/20
				got := gridRGBKeys0(source, u, v, premultiplied)
				expected := gridOracleSample(source, u, v, premultiplied)
				for component := range got {
					if math.Abs(got[component]-expected[component]) > 2e-10 {
						t.Fatalf("premult=%v UV=(%v,%v) component=%d: %v != %v",
							premultiplied, u, v, component, got, expected)
					}
				}
			}
		}
	}
	for index, value := range backing.Pix {
		if value != before[index] {
			t.Fatalf("source byte %d changed", index)
		}
	}
}

func TestGridSamplingPreservesSignedAlphaUntilAverage(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 3, 1))
	for column, alpha := range []uint8{255, 255, 0} {
		source.SetNRGBA(column, 0, color.NRGBA{R: 80, G: 140, B: 210, A: alpha})
	}
	over := gridRGBKeys0(source, 1.0/3, 0.5, false)
	if over[3] != 302.8125 {
		t.Fatalf("alpha overshoot was clipped: %v", over)
	}
	source.SetNRGBA(1, 0, color.NRGBA{R: 80, G: 140, B: 210})
	under := gridRGBKeys0(source, 2.0/3, 0.5, false)
	if under[3] != -23.90625 {
		t.Fatalf("negative alpha lobe was clipped: %v", under)
	}
	var accumulator gridSampleAccumulator
	accumulator.add(over)
	accumulator.add(under)
	expected := [4]float64{80, 140, 210, 139.453125}
	if got := accumulator.pixel(false); got != expected {
		t.Fatalf("signed alpha average = %v, want %v", got, expected)
	}
	var emptyAccumulator gridSampleAccumulator
	if got := emptyAccumulator.pixel(false); got != [4]float64{} {
		t.Fatalf("empty accumulator = %v", got)
	}
}

func TestGridSamplingSeparatesTransparentColorsAndPremultipliedControl(t *testing.T) {
	source := image.NewNRGBA(image.Rect(17, -8, 18, -7))
	source.SetNRGBA(17, -8, color.NRGBA{R: 220, G: 120, B: 40})
	for _, u := range []float64{-10, -0.25, 0.5, 1, 12} {
		straight := gridRGBKeys0(source, u, 0.5, false)
		if straight != [4]float64{220, 120, 40, 0} {
			t.Fatalf("transparent straight color at u=%v: %v", u, straight)
		}
		premultiplied := gridRGBKeys0(source, u, 0.5, true)
		if premultiplied != [4]float64{} {
			t.Fatalf("transparent premultiplied control at u=%v: %v", u, premultiplied)
		}
	}
	source.SetNRGBA(17, -8, color.NRGBA{R: 220, G: 120, B: 40, A: 255})
	straight := gridRGBKeys0(source, 0, 0.5, false)
	premultiplied := gridRGBKeys0(source, 0, 0.5, true)
	if straight != [4]float64{220, 120, 40, 151.40625} ||
		premultiplied != [4]float64{110, 60, 20, 151.40625} {
		t.Fatalf("source border: straight=%v premultiplied=%v", straight, premultiplied)
	}
	var accumulator gridSampleAccumulator
	accumulator.add(premultiplied)
	expected := [4]float64{110.0 / 151.40625 * 255, 60.0 / 151.40625 * 255,
		20.0 / 151.40625 * 255, 151.40625}
	if got := accumulator.pixel(true); got != expected {
		t.Fatalf("negative control did not unpremultiply after averaging: %v != %v", got, expected)
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if got := gridRGBKeys0(source, bad, 0.5, false); got != [4]float64{} {
			t.Fatalf("nonfinite coordinate result = %v", got)
		}
	}
}

func TestGridSamplingUsesNoHeapScratch(t *testing.T) {
	source := image.NewNRGBA(image.Rect(2, 3, 10, 11))
	source.SetNRGBA(5, 6, color.NRGBA{R: 120, G: 90, B: 40, A: 180})
	allocations := testing.AllocsPerRun(100, func() {
		var accumulator gridSampleAccumulator
		for row := range 4 {
			for column := range 4 {
				accumulator.add(gridRGBKeys0(source, (float64(column)+0.5)/4, (float64(row)+0.5)/4, false))
			}
		}
		_ = accumulator.pixel(false)
	})
	if allocations != 0 {
		t.Fatalf("sampling allocated %v times per pixel", allocations)
	}
}

func gridOracleKeysDistance(distance, tension float64) float64 {
	distance = math.Abs(distance)
	if distance < 1 {
		return (tension+2)*distance*distance*distance - (tension+3)*distance*distance + 1
	}
	if distance < 2 {
		return tension*distance*distance*distance - 5*tension*distance*distance +
			8*tension*distance - 4*tension
	}
	return 0
}

func gridOracleSample(source *image.NRGBA, u, v float64, premultiplied bool) [4]float64 {
	x, y := u*float64(source.Rect.Dx())-0.5, v*float64(source.Rect.Dy())-0.5
	var result [4]float64
	for sy := int(math.Floor(y)) - 1; sy <= int(math.Floor(y))+2; sy++ {
		for sx := int(math.Floor(x)) - 1; sx <= int(math.Floor(x))+2; sx++ {
			inside := sx >= 0 && sy >= 0 && sx < source.Rect.Dx() && sy < source.Rect.Dy()
			pixel := source.NRGBAAt(source.Rect.Min.X+min(max(sx, 0), source.Rect.Dx()-1),
				source.Rect.Min.Y+min(max(sy, 0), source.Rect.Dy()-1))
			alpha := 0.0
			if inside {
				alpha = float64(pixel.A)
			}
			result[3] += alpha * gridOracleKeysDistance(x-float64(sx), -0.75) *
				gridOracleKeysDistance(y-float64(sy), -0.75)
			weight := gridOracleKeysDistance(x-float64(sx), 0) * gridOracleKeysDistance(y-float64(sy), 0)
			if premultiplied {
				weight *= alpha / 255
			}
			for component, value := range [3]uint8{pixel.R, pixel.G, pixel.B} {
				result[component] += float64(value) * weight
			}
		}
	}
	return result
}
