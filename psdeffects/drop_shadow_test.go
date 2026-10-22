package psdeffects

import (
	"bytes"
	"context"
	"errors"
	"image"
	"math"
	"testing"
)

func templateDropShadow() Effect {
	shadow := primitive(DropShadow)
	shadow.Size, shadow.Spread = 2, 0.04
	shadow.Distance, shadow.Angle, shadow.Opacity = 4, 54, 0.1
	return shadow
}

func patternedShadowAlpha(bounds image.Rectangle) *image.Alpha {
	alpha := image.NewAlpha(bounds)
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			value := byte((x-bounds.Min.X)*31 + (y-bounds.Min.Y)*47 + 13)
			if (x+y)%5 == 0 {
				value = 0
			}
			alpha.Pix[alpha.PixOffset(x, y)] = value
		}
	}
	return alpha
}

func TestApproximateDropShadowRealTemplateCoverage(t *testing.T) {
	for _, mode := range []string{"norm", "mul "} {
		for _, conceal := range []bool{false, true} {
			for _, scale := range []float64{1, 25.0 / 12} {
				shadow := templateDropShadow()
				shadow.BlendMode, shadow.LayerConceals = mode, conceal
				set := primitiveSet(shadow)
				set.Scale = scale
				alpha := patternedShadowAlpha(image.Rect(-4, 3, 4, 10))
				result, err := Rasterize(nil, alpha, set, Options{ApproximateDropShadow: true})
				if err != nil {
					t.Fatal(err)
				}
				plane := result.Background[0]
				for y := plane.Coverage.Rect.Min.Y; y < plane.Coverage.Rect.Max.Y; y++ {
					for x := plane.Coverage.Rect.Min.X; x < plane.Coverage.Rect.Max.X; x++ {
						want := referenceApproximateShadow(alpha, shadow, scale, 90, x, y)
						if got := plane.Coverage.AlphaAt(x, y).A; got != want {
							t.Fatalf("mode %q conceal %v scale %v at %d,%d: got %d, want %d",
								mode, conceal, scale, x, y, got, want)
						}
					}
				}
				if plane.Opacity != 0.1 || plane.BlendMode != mode || plane.Color != shadow.Color {
					t.Fatal("coverage baked in or discarded effect paint parameters")
				}
			}
		}
	}
}

// referenceApproximateShadow directly sums the declared 2D convolution and sampling equations.
// It does not call the primitive's planning, morphology, interpolation or filter helpers.
func referenceApproximateShadow(
	alpha *image.Alpha, shadow Effect, scale, globalAngle float64, x, y int,
) byte {
	if scale == 0 {
		scale = 1
	}
	angle := shadow.Angle
	if shadow.UseGlobalLight {
		angle = globalAngle
	}
	angle *= math.Pi / 180
	offsetX := -shadow.Distance * scale * math.Cos(angle)
	offsetY := shadow.Distance * scale * math.Sin(angle)
	if math.Abs(offsetX-math.Round(offsetX)) < 1e-9 {
		offsetX = math.Round(offsetX)
	}
	if math.Abs(offsetY-math.Round(offsetY)) < 1e-9 {
		offsetY = math.Round(offsetY)
	}
	radius := shadow.Size * scale * shadow.Spread
	blur := shadow.Size * scale * (1 - shadow.Spread)
	extent := max(0, int(math.Ceil(blur))-1)
	kernel := make([]float64, 2*extent+1)
	total := float64(0)
	for index := range kernel {
		kernel[index] = math.Max(0, blur-math.Abs(float64(index-extent)))
		total += kernel[index]
	}
	if total == 0 {
		kernel[0], total = 1, 1
	}
	value := float64(0)
	for ky, weightY := range kernel {
		for kx, weightX := range kernel {
			positionX := float64(x+kx-extent) - offsetX
			positionY := float64(y+ky-extent) - offsetY
			left, top := int(math.Floor(positionX)), int(math.Floor(positionY))
			fractionX, fractionY := positionX-float64(left), positionY-float64(top)
			sample := float64(0)
			for iy := range 2 {
				for ix := range 2 {
					factorX, factorY := 1-fractionX, 1-fractionY
					if ix == 1 {
						factorX = fractionX
					}
					if iy == 1 {
						factorY = fractionY
					}
					matte := float64(0)
					brushExtent := int(math.Ceil(radius))
					for dy := -brushExtent; dy <= brushExtent; dy++ {
						for dx := -brushExtent; dx <= brushExtent; dx++ {
							weight := math.Max(0, math.Min(1, radius+1-math.Hypot(float64(dx), float64(dy))))
							point := image.Pt(left+ix+dx, top+iy+dy)
							if point.In(alpha.Rect) {
								matte = math.Max(matte, float64(alpha.AlphaAt(point.X, point.Y).A)/255*weight)
							}
						}
					}
					sample += matte * factorX * factorY
				}
			}
			value += sample * weightX * weightY / (total * total)
		}
	}
	if shadow.LayerConceals {
		value *= 1 - float64(alpha.AlphaAt(x, y).A)/255
	}
	return byte(math.Round(math.Max(0, math.Min(1, value)) * 255))
}

func TestDropShadowSourceBoundsRealGeometryAndLegacyControls(t *testing.T) {
	shadow := templateDropShadow()
	shadow.LayerConceals = false
	output := image.Rect(-2, 1, 5, 7)
	needed, err := DropShadowSourceBounds(output, shadow, 1, 90, Options{ApproximateDropShadow: true})
	if err != nil || needed != image.Rect(-2, -5, 10, 6) {
		t.Fatalf("actual template dependencies differ: %v %v", needed, err)
	}
	shadow.LayerConceals = true
	needed, err = DropShadowSourceBounds(output, shadow, 1, 90, Options{ApproximateDropShadow: true})
	if err != nil || needed != image.Rect(-2, -5, 10, 7) {
		t.Fatalf("local conceal alpha is missing: %v %v", needed, err)
	}
	shadow.UseGlobalLight = true
	needed, err = DropShadowSourceBounds(output, shadow, 0, 90)
	if err != nil || needed != image.Rect(-4, -5, 7, 7) {
		t.Fatalf("global-light or default-scale source support differs: %v %v", needed, err)
	}
	empty := image.Rect(3, 4, 3, 7)
	if got, err := DropShadowSourceBounds(empty, shadow, 1, 90); err != nil || got != empty {
		t.Fatalf("empty output changed or allocated support: %v %v", got, err)
	}
	shadow.Enabled = false
	if got, err := DropShadowSourceBounds(output, shadow, 1, 90); err != nil || !got.Empty() {
		t.Fatalf("disabled effect requested source samples: %v %v", got, err)
	}
}

func TestDropShadowSourceWindowPreservesFullAndROICoverage(t *testing.T) {
	alpha := patternedShadowAlpha(image.Rect(-12, -10, 12, 15))
	for _, approximate := range []bool{false, true} {
		for _, geometry := range []Effect{templateDropShadow(), primitive(DropShadow)} {
			if geometry.Size == 0 {
				geometry.Size, geometry.Spread, geometry.Angle, geometry.Distance = 3, 1, 31, 2.75
			}
			options := Options{ApproximateDropShadow: approximate}
			full, err := Rasterize(nil, alpha, primitiveSet(geometry), options)
			if err != nil {
				t.Fatal(err)
			}
			for _, output := range []image.Rectangle{
				image.Rect(-11, -9, -4, -2), image.Rect(-1, 1, 5, 6),
				image.Rect(7, 10, 15, 18), image.Rect(-20, -20, -15, -15),
			} {
				needed, err := DropShadowSourceBounds(output, geometry, 1, 90, options)
				if err != nil {
					t.Fatal(err)
				}
				cropped := image.NewAlpha(needed.Intersect(alpha.Rect))
				for y := cropped.Rect.Min.Y; y < cropped.Rect.Max.Y; y++ {
					for x := cropped.Rect.Min.X; x < cropped.Rect.Max.X; x++ {
						cropped.SetAlpha(x, y, alpha.AlphaAt(x, y))
					}
				}
				partial, err := Rasterize(nil, cropped, primitiveSet(geometry), options)
				if err != nil {
					t.Fatal(err)
				}
				for y := output.Min.Y; y < output.Max.Y; y++ {
					for x := output.Min.X; x < output.Max.X; x++ {
						var found byte
						if len(partial.Background) != 0 {
							found = partial.Background[0].Coverage.AlphaAt(x, y).A
						}
						want := full.Background[0].Coverage.AlphaAt(x, y).A
						if found != want {
							t.Fatalf("approximate %v ROI %v at %d,%d: got %d, want %d",
								approximate, output, x, y, found, want)
						}
					}
				}
			}
		}
	}
}

func TestApproximateDropShadowHasOwnedCoverageAndNoNativeBrushOverride(t *testing.T) {
	alpha := rectangleAlpha(image.Rect(2, 3, 3, 4), 255)
	before := bytes.Clone(alpha.Pix)
	shadow := primitive(DropShadow)
	shadow.Size, shadow.Spread, shadow.LayerConceals = 3, 1, false
	legacy, err := Rasterize(nil, alpha, primitiveSet(shadow))
	if err != nil {
		t.Fatal(err)
	}
	approximate, err := Rasterize(nil, alpha, primitiveSet(shadow), Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	want := byte(math.Round((4 - math.Sqrt(10)) * 255))
	if approximate.Background[0].Coverage.AlphaAt(5, 4).A != want ||
		legacy.Background[0].Coverage.AlphaAt(5, 4).A != 195 {
		t.Fatal("explicit generic spread was confused with the finite legacy native brush calibration")
	}
	approximate.Background[0].Coverage.Pix[0] = 99
	if !bytes.Equal(alpha.Pix, before) || legacy.Background[0].Coverage.Pix[0] == 99 {
		t.Fatal("an approximate output borrowed input or another result storage")
	}
}

func TestApproximateDropShadowBudgetsAndUnsupportedSemantics(t *testing.T) {
	alpha := patternedShadowAlpha(image.Rect(-4, 3, 4, 10))
	shadow := templateDropShadow()
	set := primitiveSet(shadow, shadow)
	options := Options{ApproximateDropShadow: true}
	estimate, err := EstimateWorking(alpha.Rect, set, options)
	if err != nil {
		t.Fatal(err)
	}
	// Each of two 13x12 planes retains 512 metadata bytes; scratch is 16*156 plus three float weights.
	// Bilinear sampling visits 4*9 morphology cells and two three-tap passes, plus three final visits.
	if estimate.OutputBytes != 1336 || estimate.ScratchBytes != 2520 || estimate.SampleWork != 14040 ||
		estimate.Planes != 2 || estimate.Bounds != image.Rect(-9, 3, 4, 16) {
		t.Fatalf("actual template finite support has an incorrect independent peak/work count: %+v", estimate)
	}
	options.MaxWorkingBytes = estimate.OutputBytes + estimate.ScratchBytes
	options.MaxSampleWork = estimate.SampleWork
	if _, err := Rasterize(nil, alpha, set, options); err != nil {
		t.Fatalf("estimated exact peak and work should suffice: %v", err)
	}
	for _, lower := range []Options{
		{ApproximateDropShadow: true, MaxWorkingBytes: options.MaxWorkingBytes - 1},
		{ApproximateDropShadow: true, MaxSampleWork: options.MaxSampleWork - 1},
		{ApproximateDropShadow: true, MaxEffects: 1},
	} {
		if result, err := Rasterize(nil, alpha, set, lower); result != nil || !errors.Is(err, ErrLimit) {
			t.Fatalf("approximation returned partial coverage after a preflight limit: %v", err)
		}
	}
	for _, mutate := range []func(*Effect){
		func(effect *Effect) { effect.Noise = 0.05 },
		func(effect *Effect) { effect.Unsupported = []string{"nonlinear contour"} },
		func(effect *Effect) { effect.Jitter = 0.1 },
	} {
		changed := shadow
		mutate(&changed)
		result, err := Rasterize(nil, alpha, primitiveSet(shadow, changed), options)
		if result != nil || !errors.Is(err, ErrUnsupported) {
			t.Fatalf("approximation silently admitted unsupported later metadata: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result, err := Rasterize(ctx, alpha, set, options); result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled approximation returned coverage: %v", err)
	}
	shadow.Size = math.NaN()
	if _, err := DropShadowSourceBounds(alpha.Rect, shadow, 1, 90, options); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source support accepted invalid geometry: %v", err)
	}
}

type cancelShadowContext struct {
	context.Context
	cancel        context.CancelFunc
	checks, after int
}

func (ctx *cancelShadowContext) Err() error {
	ctx.checks++
	if ctx.checks == ctx.after {
		ctx.cancel()
	}
	return ctx.Context.Err()
}

func TestApproximateDropShadowCancellationDuringCoverage(t *testing.T) {
	alpha := patternedShadowAlpha(image.Rect(-4, 3, 4, 10))
	before := bytes.Clone(alpha.Pix)
	for _, after := range []int{3, 5, 10, 50} {
		base, cancel := context.WithCancel(context.Background())
		ctx := &cancelShadowContext{Context: base, cancel: cancel, after: after}
		result, err := Rasterize(ctx, alpha, primitiveSet(templateDropShadow()),
			Options{ApproximateDropShadow: true})
		cancel()
		if result != nil || !errors.Is(err, context.Canceled) || ctx.checks != after {
			t.Fatalf("coverage cancellation at check %d: result %v, error %v, checks %d",
				after, result, err, ctx.checks)
		}
		if !bytes.Equal(alpha.Pix, before) {
			t.Fatal("canceled coverage changed the source alpha")
		}
	}
}

func TestDropShadowSourceBoundsRejectsInvalidAndOverflowingDependencies(t *testing.T) {
	shadow := templateDropShadow()
	for _, output := range []image.Rectangle{
		{Min: image.Pt(4, 1), Max: image.Pt(3, 2)},
		image.Rect(0, 0, 2, 2),
	} {
		options := Options{ApproximateDropShadow: true}
		if !output.Empty() {
			options.MaxWorkingBytes = -1
		}
		if _, err := DropShadowSourceBounds(output, shadow, 1, 90, options); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid rectangle/options accepted: %v", err)
		}
	}
	shadow.Angle, shadow.LayerConceals = 270, false
	limit64 := int64(1 << 40)
	limit := int(limit64)
	if int64(limit) != limit64 {
		t.Skip("the precise-coordinate limit exceeds this platform's integer domain")
	}
	output := image.Rect(0, limit-1, 2, limit)
	if _, err := DropShadowSourceBounds(output, shadow, 1, 90,
		Options{ApproximateDropShadow: true}); !errors.Is(err, ErrLimit) {
		t.Fatalf("dependency coordinate escaped the supported precise domain: %v", err)
	}
}

func TestDeferredDropShadowConcealPreservesRawCoverageAndMetadata(t *testing.T) {
	alpha := patternedShadowAlpha(image.Rect(-4, 3, 4, 10))
	shadow := templateDropShadow()
	set := primitiveSet(shadow)
	result, err := Rasterize(nil, alpha, set,
		Options{ApproximateDropShadow: true, DeferDropShadowConceal: true})
	if err != nil {
		t.Fatal(err)
	}
	plane := result.Background[0]
	if !plane.LayerConceals || !set.Items[0].LayerConceals || plane.Opacity != 0.1 {
		t.Fatal("deferred coverage discarded or mutated the original paint/knockout parameters")
	}
	shadow.LayerConceals = false
	for y := plane.Coverage.Rect.Min.Y; y < plane.Coverage.Rect.Max.Y; y++ {
		for x := plane.Coverage.Rect.Min.X; x < plane.Coverage.Rect.Max.X; x++ {
			want := referenceApproximateShadow(alpha, shadow, 1, 90, x, y)
			if got := plane.Coverage.AlphaAt(x, y).A; got != want {
				t.Fatalf("deferred coverage has a baked source-shape knockout at %d,%d: %d/%d", x, y, got, want)
			}
		}
	}
	half := rectangleAlpha(image.Rect(2, 3, 3, 4), 128)
	hard := primitive(DropShadow)
	concealed, err := Rasterize(nil, half, primitiveSet(hard), Options{ApproximateDropShadow: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Rasterize(nil, half, primitiveSet(hard),
		Options{ApproximateDropShadow: true, DeferDropShadowConceal: true})
	if err != nil || concealed.Background[0].Coverage.Pix[0] != 64 || raw.Background[0].Coverage.Pix[0] != 128 {
		t.Fatalf("half-alpha concealment was not exclusively deferred: %v", err)
	}
}

func TestDeferredDropShadowConcealRequiresExplicitPolicy(t *testing.T) {
	options := Options{DeferDropShadowConceal: true}
	alpha := rectangleAlpha(image.Rect(0, 0, 1, 1), 128)
	shadow := templateDropShadow()
	if result, err := Rasterize(nil, alpha, primitiveSet(shadow), options); result != nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("concealment changed legacy primitive semantics implicitly: %v", err)
	}
	if _, err := EstimateWorking(alpha.Rect, primitiveSet(shadow), options); !errors.Is(err, ErrInvalid) {
		t.Fatalf("estimate accepted a policy that rasterization rejects: %v", err)
	}
	if _, err := DropShadowSourceBounds(alpha.Rect, shadow, 1, 90, options); !errors.Is(err, ErrInvalid) {
		t.Fatalf("support accepted a policy that rasterization rejects: %v", err)
	}
}
