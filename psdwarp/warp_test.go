package psdwarp_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func warpFixture(width, height int, left, top float64) *psdmodel.Placed {
	warp := &psdmodel.Warp{Style: "custom", Rotate: "horizontal", UOrder: 4, VOrder: 4, HasEnvelope: true,
		Bounds: &psdmodel.WarpBounds{Left: 5, Top: 7, Right: 5 + float64(width), Bottom: 7 + float64(height), Pixels: true}}
	for row := range 4 {
		for column := range 4 {
			warp.MeshPoints = append(warp.MeshPoints, psdmodel.Point{
				X: 5 + float64(column)*float64(width)/3, Y: 7 + float64(row)*float64(height)/3})
		}
	}
	return &psdmodel.Placed{Width: width, Height: height, Warp: warp,
		Transform: []float64{left, top, left + float64(width), top, left + float64(width), top + float64(height),
			left, top + float64(height)}}
}

func nativeWarpSource() *image.NRGBA {
	source := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for row := range 8 {
		for column := range 8 {
			pixel := color.NRGBA{R: 20, G: 220, B: 80, A: 255}
			if column < 3 && row < 2 {
				pixel = color.NRGBA{R: 220, G: 30, B: 60, A: 255}
			}
			if column >= 5 && row >= 2 && row < 6 {
				pixel = color.NRGBA{R: 20, G: 40, B: 220, A: 255}
			}
			if column >= 2 && column < 5 && row >= 6 {
				pixel = color.NRGBA{R: 240, G: 230, B: 50, A: 255}
			}
			source.SetNRGBA(column, row, pixel)
		}
	}
	return source
}

func nativeWarpFixture() *psdmodel.Placed {
	placed := warpFixture(8, 8, 20, 4)
	for index := range placed.Warp.MeshPoints {
		if index%4 == 1 || index%4 == 2 {
			placed.Warp.MeshPoints[index].Y -= 2
		}
	}
	placed.Transform = []float64{20, 2, 28, 2, 28, 12, 20, 12}
	return placed
}

func TestWarpBicubicForwardInverseAnalyticCurve(t *testing.T) {
	placed := warpFixture(100, 100, 10, 20)
	for row := range 4 {
		placed.Warp.MeshPoints[row*4+1].Y -= 20
		placed.Warp.MeshPoints[row*4+2].Y -= 20
	}
	// A warped PSD placement quad is the control hull, expanded above the original rectangle.
	placed.Transform = []float64{10, 0, 110, 0, 110, 120, 10, 120}
	patch, err := psdwarp.Compile(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	for column := range 11 {
		for row := range 11 {
			u, v := float64(column)/10, float64(row)/10
			point, err := patch.Forward(u, v)
			if err != nil || math.Abs(point.X-(10+100*u)) > 1e-8 ||
				math.Abs(point.Y-(20+100*v-60*u*(1-u))) > 1e-8 {
				t.Fatalf("analytic cubic forward differs at (%g,%g): %v, %v", u, v, point, err)
			}
			gotU, gotV, inside, err := patch.Inverse(nil, point)
			if err != nil || !inside || math.Abs(gotU-u) > 1e-7 || math.Abs(gotV-v) > 1e-7 {
				t.Fatalf("inverse differs at (%g,%g): %g,%g,%t,%v", u, v, gotU, gotV, inside, err)
			}
		}
	}
	for _, point := range []psdmodel.Point{{X: 0, Y: 0}, {X: 60, Y: -10}, {X: 120, Y: 70}} {
		if _, _, inside, err := patch.Inverse(nil, point); err != nil || inside {
			t.Fatalf("exterior point was not transparent: %v", err)
		}
	}
}

func TestWarpCurveBoundsExtendBeyondOriginalQuad(t *testing.T) {
	placed := warpFixture(100, 100, 10, 20)
	for row := range 4 {
		placed.Warp.MeshPoints[row*4+1].Y -= 20
		placed.Warp.MeshPoints[row*4+2].Y -= 20
	}
	placed.Transform = []float64{10, 0, 110, 0, 110, 120, 10, 120}
	patch, err := psdwarp.Compile(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	bounds, err := patch.Bounds(image.Rectangle{})
	if err != nil || bounds.Min.Y >= 5 || bounds.Max.Y < 120 {
		t.Fatalf("curved surface was cropped to its original quad: %v, %v", bounds, err)
	}
	source := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	for index := 0; index < len(source.Pix); index += 4 {
		copy(source.Pix[index:], []byte{255, 0, 0, 255})
	}
	projected, origin, err := patch.Project(nil, source, image.Rect(0, 0, 200, 200))
	if err != nil {
		t.Fatal(err)
	}
	if projected.NRGBAAt(60-origin.X, 10-origin.Y).A != 255 {
		t.Fatal("valid warped pixels extending above the original quad were lost")
	}
	clipped, clippedOrigin, err := patch.Project(nil, source, image.Rect(40, 8, 80, 100))
	if err != nil || clippedOrigin != image.Pt(40, 8) || clipped.Rect.Size() != image.Pt(40, 92) {
		t.Fatalf("document-space canvas clipping differs: %v, %v", clippedOrigin, err)
	}
	for row := range clipped.Rect.Dy() {
		for column := range clipped.Rect.Dx() {
			if clipped.NRGBAAt(column, row) != projected.NRGBAAt(column+clippedOrigin.X-origin.X,
				row+clippedOrigin.Y-origin.Y) {
				t.Fatal("canvas clipping changed the warp lattice or texture samples")
			}
		}
	}
}

func TestWarpNativeLatticeEdgeAndInteriorSamples(t *testing.T) {
	// Stored control coordinates and sample bytes are independent Photoshop fixture observations.
	placed, source := nativeWarpFixture(), nativeWarpSource()
	result, origin, err := psdwarp.Project(nil, source, placed, image.Rectangle{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		point image.Point
		pixel color.NRGBA
	}{
		{image.Pt(20, 3), color.NRGBA{R: 220, G: 30, B: 60, A: 48}},
		{image.Pt(21, 3), color.NRGBA{R: 220, G: 30, B: 60, A: 143}},
		{image.Pt(22, 3), color.NRGBA{R: 220, G: 30, B: 60, A: 239}},
		{image.Pt(23, 2), color.NRGBA{R: 20, G: 220, B: 80, A: 80}},
		{image.Pt(22, 4), color.NRGBA{R: 220, G: 30, B: 60, A: 255}},
		{image.Pt(20, 11), color.NRGBA{R: 20, G: 220, B: 80, A: 207}},
	} {
		actual := result.NRGBAAt(test.point.X-origin.X, test.point.Y-origin.Y)
		if actual != test.pixel {
			t.Fatalf("native lattice sample %v differs: %v, expected %v", test.point, actual, test.pixel)
		}
	}
}

func TestWarpNativeScaledLatticeAndDocumentEdgeCoverage(t *testing.T) {
	for _, test := range []struct {
		name  string
		quad  []float64
		point image.Point
		pixel color.NRGBA
	}{
		{"scale2-interior", []float64{16, -4, 32, -4, 32, 16, 16, 16}, image.Pt(20, 2),
			color.NRGBA{R: 101, G: 143, B: 72, A: 255}},
		{"scale2-edge", []float64{16, -4, 32, -4, 32, 16, 16, 16}, image.Pt(23, 13),
			color.NRGBA{R: 69, G: 75, B: 50, A: 255}},
		{"scale2-exterior", []float64{16, -4, 32, -4, 32, 16, 16, 16}, image.Pt(21, 14),
			color.NRGBA{R: 30, G: 40, B: 50, A: 255}},
		{"nonuniform-edge", []float64{16, -1, 32, -1, 32, 14, 16, 14}, image.Pt(16, 1),
			color.NRGBA{R: 50, G: 39, B: 51, A: 255}},
		{"nonuniform-interior", []float64{16, -1, 32, -1, 32, 14, 16, 14}, image.Pt(22, 2),
			color.NRGBA{R: 67, G: 175, B: 75, A: 255}},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := nativeWarpFixture()
			placed.Transform = test.quad
			result, origin, err := psdwarp.Project(nil, nativeWarpSource(), placed, image.Rectangle{})
			if err != nil {
				t.Fatal(err)
			}
			foreground := result.NRGBAAt(test.point.X-origin.X, test.point.Y-origin.Y)
			alpha := float64(foreground.A) / 255
			for index, pair := range [3][2]byte{{foreground.R, 30}, {foreground.G, 40}, {foreground.B, 50}} {
				actual := math.RoundToEven(float64(pair[0])*alpha + float64(pair[1])*(1-alpha))
				expected := [3]byte{test.pixel.R, test.pixel.G, test.pixel.B}[index]
				if math.Abs(actual-float64(expected)) > 2 {
					t.Fatalf("native scaled sample %v differs: channel %d = %g, expected %d",
						test.point, index, actual, expected)
				}
			}
		})
	}
}

func TestWarpIdentityPreservesPixelsAndOwnsGeometryAndOutput(t *testing.T) {
	placed := warpFixture(4, 4, 10, 12)
	source := image.NewNRGBA(image.Rect(5, 6, 9, 10))
	for row := range 4 {
		for column := range 4 {
			source.SetNRGBA(column+5, row+6, color.NRGBA{R: byte(column * 60), G: byte(row * 60), B: 30, A: 255})
		}
	}
	before := bytes.Clone(source.Pix)
	patch, err := psdwarp.Compile(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	placed.Warp.MeshPoints[0].X = 999
	projected, origin, err := patch.Project(nil, source, image.Rectangle{})
	if err != nil || origin != image.Pt(10, 12) || projected.Rect.Size() != image.Pt(4, 4) ||
		!bytes.Equal(projected.Pix, source.Pix) {
		t.Fatalf("identity projection or owned geometry differs: %v, %v", origin, err)
	}
	projected.Pix[0] = 250
	if !bytes.Equal(source.Pix, before) {
		t.Fatal("output aliases caller-owned pixels")
	}
}

func TestMathematicalWarpNonAffineQuadMatchesIndependentHomographyProjection(t *testing.T) {
	placed := warpFixture(4, 4, 0, 0)
	placed.NonAffineTransform = []float64{2, 3, 13, 4, 12, 19, 1, 16}
	layer := &psdmodel.Layer{Placed: placed}
	source := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for index := range 16 {
		copy(source.Pix[index*4:], []byte{byte(index * 10), byte(index * 7), 150, 255})
	}
	plan, err := psdrender.CompileProjection(layer, 24, 24, false)
	if err != nil {
		t.Fatal(err)
	}
	expected, expectedOrigin, err := psdrender.ProjectImageContext(nil, layer, source, plan)
	if err != nil {
		t.Fatal(err)
	}
	// This explicit source-cubic plus homography calculation is not a native Warp validation.
	patch, err := psdwarp.CompileMathematical(nil, placed)
	if err != nil {
		t.Fatal(err)
	}
	actual, origin, err := patch.Project(nil, source, image.Rect(0, 0, 24, 24))
	if err != nil || origin != expectedOrigin || actual.Rect != expected.Rect {
		t.Fatalf("identity warp quad bounds differ: %v", err)
	}
	// This lattice approximates the homography between its exact forward vertices.
	// Interior texture samples remain close, while its transparent border differs from clamped projection.
	point := image.Pt(7-origin.X, 10-origin.Y)
	actualPixel, expectedPixel := actual.NRGBAAt(point.X, point.Y), expected.NRGBAAt(point.X, point.Y)
	if actualPixel.A != 255 || math.Abs(float64(actualPixel.R)-float64(expectedPixel.R)) > 3 {
		t.Fatalf("perspective lattice interior differs: %v, %v", actualPixel, expectedPixel)
	}
}

func TestWarpRejectsFoldDegenerateAndUnsupportedSchemas(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*psdmodel.Placed)
	}{
		{"preset", func(p *psdmodel.Placed) { p.Warp.Style = "arc" }},
		{"split", func(p *psdmodel.Placed) { p.Warp.Split = true }},
		{"wrong-order", func(p *psdmodel.Placed) { p.Warp.UOrder = 3 }},
		{"short-grid", func(p *psdmodel.Placed) { p.Warp.MeshPoints = p.Warp.MeshPoints[:15] }},
		{"unknown-units", func(p *psdmodel.Placed) { p.Warp.Bounds.Pixels = false }},
		{"different-source-scale", func(p *psdmodel.Placed) { p.Warp.Bounds.Right += 1 }},
		{"nonfinite", func(p *psdmodel.Placed) { p.Warp.MeshPoints[5].Y = math.NaN() }},
		{"folded-grid", func(p *psdmodel.Placed) { p.Warp.MeshPoints[5].X -= 100 }},
		{"collapsed-grid", func(p *psdmodel.Placed) {
			for index := range p.Warp.MeshPoints {
				p.Warp.MeshPoints[index] = psdmodel.Point{}
			}
		}},
		{"bow-tie-quad", func(p *psdmodel.Placed) { p.Transform = []float64{0, 0, 4, 4, 4, 0, 0, 4} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := warpFixture(4, 4, 0, 0)
			test.edit(placed)
			if patch, err := psdwarp.Compile(nil, placed); err == nil || patch != nil {
				t.Fatal("invalid or uncertified surface was accepted")
			}
		})
	}
}

func TestWarpBudgetsCancellationAndConvergence(t *testing.T) {
	placed := warpFixture(4, 4, 0, 0)
	source := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	for _, options := range []psdwarp.Options{{MaxPixels: 1}, {MaxBytes: 4}, {MaxIterations: 129}} {
		result, _, err := psdwarp.Project(nil, source, placed, image.Rectangle{}, options)
		if !errors.Is(err, psdwarp.ErrLimit) || result != nil {
			t.Fatalf("bad budget returned an image and %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if patch, err := psdwarp.Compile(ctx, placed); !errors.Is(err, context.Canceled) || patch != nil {
		t.Fatalf("canceled compile returned geometry and %v", err)
	}
	placed.Warp.MeshPoints[5].X += 0.3
	placed.Warp.MeshPoints[6].X += 0.3
	patch, err := psdwarp.Compile(nil, placed, psdwarp.Options{MaxIterations: 1})
	if err != nil {
		t.Fatal(err)
	}
	point, err := patch.Forward(0.3, 0.6)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := patch.Inverse(nil, point); !errors.Is(err, psdwarp.ErrConvergence) {
		t.Fatalf("bounded inverse silently omitted an interior point: %v", err)
	}
}
