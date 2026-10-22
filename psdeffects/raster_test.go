package psdeffects

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
	"time"
)

func rectangleAlpha(bounds image.Rectangle, value byte) *image.Alpha {
	alpha := image.NewAlpha(bounds)
	for index := range alpha.Pix {
		alpha.Pix[index] = value
	}
	return alpha
}

func primitiveSet(effects ...Effect) *Set {
	return &Set{Enabled: true, EffectsVisible: true, Scale: 1, GlobalAngle: 90, Items: effects}
}

func primitive(kind Kind) Effect {
	return Effect{Kind: kind, Enabled: true, Color: [3]float64{0, 0, 1}, BlendMode: "norm", Opacity: 1,
		LayerConceals: true, Technique: "SfBL", Range: 0.5, Position: Outside}
}

func TestHardShadowUsesUnfilledAlphaGlobalLightAndOwnedCoverage(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(2, 3, 6, 7), 255)
	before := append([]byte(nil), alpha.Pix...)
	shadow := primitive(DropShadow)
	shadow.Distance, shadow.Opacity = 1, 0.75
	result, err := Rasterize(nil, alpha, primitiveSet(shadow))
	if err != nil {
		t.Fatal(err)
	}
	plane := result.Background[0]
	if plane.Coverage.Rect != image.Rect(1, 3, 5, 7) || plane.Coverage.AlphaAt(1, 4).A != 255 ||
		plane.Coverage.AlphaAt(2, 4).A != 0 || plane.Opacity != 0.75 || plane.Color != shadow.Color {
		t.Fatal("hard shadow displacement, knockout or unbaked opacity differs from the rectangle geometry")
	}
	shadow.UseGlobalLight = true
	global, err := Rasterize(nil, alpha, primitiveSet(shadow))
	if err != nil || global.Background[0].Coverage.Rect != image.Rect(2, 4, 6, 8) ||
		global.Background[0].Coverage.AlphaAt(3, 7).A != 255 {
		t.Fatalf("global lighting angle did not override the local zero angle: %v", err)
	}
	plane.Coverage.Pix[0] = 19
	if !bytes.Equal(alpha.Pix, before) || global.Background[0].Coverage.Pix[0] == 19 {
		t.Fatal("coverage result borrowed source alpha or a previous result")
	}
}

func TestShadowFractionalDisplacementAndTransparentKnockout(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(2, 2, 3, 3), 255)
	shadow := primitive(DropShadow)
	shadow.Distance, shadow.LayerConceals = 0.5, false
	result, err := Rasterize(nil, alpha, primitiveSet(shadow))
	if err != nil || !bytes.Equal(result.Background[0].Coverage.Pix, []byte{128, 128}) {
		t.Fatalf("half-pixel shift did not bilinearly distribute coverage: %v, %v", result, err)
	}
	alpha.Pix[0] = 128
	shadow.Distance, shadow.LayerConceals = 0, true
	result, err = Rasterize(nil, alpha, primitiveSet(shadow))
	if err != nil || result.Background[0].Coverage.Pix[0] != 64 {
		t.Fatalf("transparent shape knockout should be alpha*(1-alpha), rounded to 64: %v", err)
	}
}

func TestStrokePositionsAndGlowHaveIndependentMathematicalCoverage(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(2, 2, 7, 7), 255)
	stroke := primitive(Stroke)
	stroke.Size = 1
	outside, err := Rasterize(nil, alpha, primitiveSet(stroke))
	if err != nil {
		t.Fatal(err)
	}
	coverage := outside.Foreground[0].Coverage
	if coverage.Rect != image.Rect(1, 1, 8, 8) || coverage.AlphaAt(1, 2).A != 255 ||
		coverage.AlphaAt(1, 1).A != byte(math.Round((2-math.Sqrt(2))*255)) || coverage.AlphaAt(4, 4).A != 0 {
		t.Fatal("outside stroke does not match a linearly antialiased distance dilation minus source coverage")
	}
	stroke.Position = Inside
	inside, err := Rasterize(nil, alpha, primitiveSet(stroke))
	if err != nil || inside.Foreground[0].Coverage.AlphaAt(2, 4).A != 255 ||
		inside.Foreground[0].Coverage.AlphaAt(4, 4).A != 0 {
		t.Fatalf("inside stroke does not match source minus unit disk erosion: %v", err)
	}
	stroke.Position, stroke.Size = Center, 2
	center, err := Rasterize(nil, alpha, primitiveSet(stroke))
	if err != nil || center.Foreground[0].Coverage.AlphaAt(1, 3).A != 255 ||
		center.Foreground[0].Coverage.AlphaAt(2, 3).A != 255 ||
		center.Foreground[0].Coverage.AlphaAt(4, 4).A != 0 {
		t.Fatalf("centered stroke did not split width across the boundary: %v", err)
	}
	glow := primitive(OuterGlow)
	glow.Size, glow.Spread = 1, 1
	result, err := Rasterize(nil, rectangleAlpha(image.Rect(3, 3, 4, 4), 255), primitiveSet(glow))
	if err != nil || !bytes.Equal(result.Background[0].Coverage.Pix, []byte{255, 255, 255, 255, 0, 255, 255, 255, 255}) {
		t.Fatalf("full-spread glow should apply the linear distance boundary through its half-range: %v", err)
	}
}

func TestShadowTentCoverageMatchesNativeKernelAndExpandsBounds(t *testing.T) {
	shadow := primitive(DropShadow)
	shadow.Size, shadow.LayerConceals = 3, false
	result, err := Rasterize(nil, rectangleAlpha(image.Rect(0, 0, 1, 1), 255), primitiveSet(shadow))
	if err != nil {
		t.Fatal(err)
	}
	coverage := result.Background[0].Coverage
	// Native size three is two uniform width-three convolutions: [1, 2, 3, 2, 1]/9 per axis.
	centerValue := byte(math.Round(255.0 / 9))
	if coverage.Rect != image.Rect(-3, -3, 4, 4) || coverage.AlphaAt(0, 0).A != centerValue ||
		coverage.AlphaAt(-1, 0).A != coverage.AlphaAt(1, 0).A ||
		coverage.AlphaAt(0, -1).A != coverage.AlphaAt(0, 1).A {
		t.Fatal("native triangular shadow kernel differs from the independently observed convolution")
	}
}

func TestEffectsRejectActiveUnsupportedAndBudgetBeforeReturningPlanes(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(0, 0, 10, 10), 255)
	shadow := primitive(DropShadow)
	shadow.Size = 3
	for _, options := range []Options{{MaxWorkingBytes: 100}, {MaxSampleWork: 10}, {MaxEffects: 1}} {
		result, err := Rasterize(nil, alpha, primitiveSet(shadow, shadow), options)
		if !errors.Is(err, ErrLimit) || result != nil {
			t.Fatalf("effect operation returned partial output after a resource limit: %v", err)
		}
	}
	shadow.Noise = 0.1
	if _, err := Rasterize(nil, alpha, primitiveSet(shadow)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("active noise was silently ignored: %v", err)
	}
	shadow.Enabled = false
	if _, err := Rasterize(nil, alpha, primitiveSet(shadow)); err != nil {
		t.Fatalf("disabled noise should retain metadata without blocking other effects: %v", err)
	}
	shadow.Enabled, shadow.Noise, shadow.Size = true, 0, math.NaN()
	if _, err := Rasterize(nil, alpha, primitiveSet(shadow)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonfinite effect size was accepted: %v", err)
	}
	malformed := &image.Alpha{Pix: []byte{1}, Stride: 1, Rect: image.Rect(0, 0, 2, 2)}
	if _, err := Rasterize(nil, malformed, primitiveSet()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid alpha backing was accepted: %v", err)
	}
}

func TestEffectsObserveCancellationInsideLargeMorphology(t *testing.T) {
	stroke := primitive(Stroke)
	stroke.Position, stroke.Size = Inside, 4000
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
	defer cancel()
	result, err := Rasterize(ctx, rectangleAlpha(image.Rect(0, 0, 1, 1), 255), primitiveSet(stroke))
	if !errors.Is(err, context.DeadlineExceeded) || result != nil {
		t.Fatalf("morphology failed to observe cancellation while visiting its neighborhood: %v", err)
	}
}
