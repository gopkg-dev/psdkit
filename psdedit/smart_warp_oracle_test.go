package psdedit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func smartWarpOraclePixels(alternate bool) *image.NRGBA {
	pixels := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			value := color.NRGBA{R: 20, G: 220, B: 80, A: 255}
			if x < 3 && y < 2 {
				value = color.NRGBA{R: 220, G: 30, B: 60, A: 255}
			}
			if x >= 5 && y >= 2 && y < 6 {
				value = color.NRGBA{R: 20, G: 40, B: 220, A: 255}
			}
			if x >= 2 && x < 5 && y >= 6 {
				value = color.NRGBA{R: 240, G: 230, B: 50, A: 255}
			}
			if alternate {
				value.R, value.G, value.B = byte(21+x*19), byte(31+y*21), byte(71+x*7+y*9)
			}
			pixels.SetNRGBA(x, y, value)
		}
	}
	return pixels
}

func smartWarpOracleControls(curved bool) []psdmodel.Point {
	points := make([]psdmodel.Point, 16)
	for row := range 4 {
		for column := range 4 {
			point := psdmodel.Point{X: 5 + float64(column)*8/3, Y: 7 + float64(row)*8/3}
			if curved && (column == 1 || column == 2) {
				point.Y -= 2
			}
			points[row*4+column] = point
		}
	}
	return points
}

func smartWarpOracleFixture(t *testing.T, version uint16, native bool) []byte {
	t.Helper()
	quad := []float64{4, 5, 12, 5, 12, 13, 4, 13}
	if native {
		quad = []float64{20, 2, 28, 2, 28, 12, 20, 12}
	}
	var xs, ys []float64
	for _, point := range smartWarpOracleControls(false) {
		xs, ys = append(xs, point.X), append(ys, point.Y)
	}
	mesh := &psdfile.DescriptorObjectArray{Name: "untouched mesh", ClassID: "rationalPoint", Prefix: 16,
		Entries: []psdfile.DescriptorEntry{
			{Key: "Hrzn", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: xs}},
			{Key: "Vrtc", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: ys}},
		}}
	warp := &psdfile.Descriptor{Name: "original warp", ClassID: "warp", Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpCustom"}},
		{Key: "warpValue", Type: "doub", Value: math.Copysign(0, -1)},
		{Key: "warpPerspective", Type: "doub", Value: float64(0)},
		{Key: "warpPerspectiveOther", Type: "doub", Value: float64(0)},
		{Key: "warpRotate", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "uOrder", Type: "long", Value: int32(4)}, {Key: "vOrder", Type: "long", Value: int32(4)},
		{Key: "bounds", Type: "Objc", Value: &psdfile.Descriptor{Name: "source domain", ClassID: "Rctn",
			Entries: []psdfile.DescriptorEntry{
				{Key: "Left", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(5)}},
				{Key: "Top ", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(7)}},
				{Key: "Rght", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(13)}},
				{Key: "Btom", Type: "UntF", Value: psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(15)}},
			}}},
		{Key: "customEnvelopeWarp", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "customEnvelopeWarp",
			Entries: []psdfile.DescriptorEntry{{Key: "meshPoints", Type: "ObAr", Value: mesh}}}},
	}}
	items := make([]psdfile.DescriptorValue, len(quad))
	for index, value := range quad {
		items[index] = psdfile.DescriptorValue{Type: "doub", Value: value}
	}
	descriptor := &psdfile.Descriptor{Name: "kept placement", ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: "fixture-id"},
		{Key: "Type", Type: "long", Value: int32(2)},
		{Key: "futureObject", Type: "tdta", Value: []byte{0xff, 0, 19, 71}},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(8)}, {Key: "Hght", Type: "doub", Value: float64(8)},
		}}},
		{Key: "Trnf", Type: "VlLs", Value: items},
		{Key: "nonAffineTransform", Type: "VlLs", Value: items},
		{Key: "warp", Type: "Objc", Value: warp},
	}}
	encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	payload = append(payload, 0x17, 0x98, 0xf1)
	bounds := image.Rect(int(quad[0]), int(quad[1]), int(quad[4]), int(quad[5]))
	poison := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	for index := 0; index < len(poison.Pix); index += 4 {
		copy(poison.Pix[index:], []byte{255, 0, 255, 255})
	}
	record, channels := smartEditLayer(version, "warp oracle", 10, bounds, poison,
		writerLinkedTag("8BIM", "SoLd", 4, payload, 2), false)
	layers := binary.BigEndian.AppendUint16(nil, 0xffff)
	layers = append(layers, record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	pixels := smartWarpOraclePixels(false)
	contents := smartSizedPrototype(version, image.Pt(8, 8), pixels.Rect, pixels)
	linked := writerLinkedEntry("liFD", "fixture-id", 7, contents)
	lengthWidth := 4
	if version == 2 {
		lengthWidth = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", lengthWidth, linked.data, 4)
	composite := append([]byte{0, 0}, bytes.Repeat([]byte{0x71}, 48*24*4)...)
	return smartEditDocument(version, 48, 24, 4, layers, global, composite)
}

func smartWarpOracleSaved(t *testing.T, editor *Editor, compression uint16) (*Editor, *psdfile.Document) {
	t.Helper()
	var output bytes.Buffer
	if err := editor.Write(t.Context(), &output, SaveOptions{Compression: compression}); err != nil {
		t.Fatal(err)
	}
	return smartEditOpen(t, output.Bytes())
}

func smartWarpOracleCache(t *testing.T, editor *Editor) (*image.NRGBA, image.Rectangle) {
	t.Helper()
	node, _, _, err := editor.findNodeLocked(1)
	if err != nil || node.pixels == nil {
		t.Fatalf("edited cache absent: %v", err)
	}
	return node.pixels, node.bounds
}

func smartWarpOracleLayer(t *testing.T, document *psdfile.Document) *image.NRGBA {
	t.Helper()
	layer, err := document.DecodeLayer(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	width, height := document.Layers[0].Bounds.Dx(), document.Layers[0].Bounds.Dy()
	pixels := image.NewNRGBA(image.Rect(0, 0, width, height))
	for component, channel := range []int16{0, 1, 2, -1} {
		var plane *psdfile.Plane
		for index := range layer.Channels {
			if layer.Channels[index].ID == channel {
				plane = &layer.Channels[index]
			}
		}
		if plane == nil {
			t.Fatalf("saved raw channel %d absent", channel)
		}
		for index, value := range plane.Data {
			pixels.Pix[index*4+component] = value
		}
	}
	return pixels
}

func TestSmartWarpOracleSameSessionUsesLatestControls(t *testing.T) {
	editor, _ := smartEditOpen(t, smartWarpOracleFixture(t, 1, false))
	for _, curved := range []bool{true, false, true, false} {
		if err := editor.SetSmartObjectWarp(t.Context(), 1, smartWarpOracleControls(curved),
			SmartWarpEditOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	actual, bounds := smartWarpOracleCache(t, editor)
	if bounds != image.Rect(4, 5, 12, 13) || !bytes.Equal(actual.Pix, smartWarpOraclePixels(false).Pix) {
		t.Fatal("multiple same-session warp edits used stale geometry or accumulated projection")
	}
	_, saved := smartWarpOracleSaved(t, editor, 2)
	view, err := saved.ReadLayerMetadata(t.Context(), 0)
	if err != nil || !slices.Equal(view.Layer.Placed.Warp.MeshPoints, smartWarpOracleControls(false)) ||
		!bytes.Equal(smartWarpOracleLayer(t, saved).Pix, smartWarpOraclePixels(false).Pix) {
		t.Fatalf("save selected an earlier current placement: %v", err)
	}
}

func TestSmartWarpOracleIdentityAndTranslatedControlDomain(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("version%d/compression%d", version, compression), func(t *testing.T) {
				original := smartWarpOracleFixture(t, version, false)
				digest := sha256.Sum256(original)
				editor, _ := smartEditOpen(t, original)
				points := smartWarpOracleControls(false)
				// The source coordinate origin is metadata, not a document-space translation.
				for round := range 2 {
					if round == 1 {
						for index := range points {
							points[index].X += 100
							points[index].Y -= 31
						}
					}
					if err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{}); err != nil {
						t.Fatal(err)
					}
					actual, bounds := smartWarpOracleCache(t, editor)
					if bounds != image.Rect(4, 5, 12, 13) || !bytes.Equal(actual.Pix, smartWarpOraclePixels(false).Pix) {
						t.Fatalf("identity lost current source pixels or control-hull origin: %v", bounds)
					}
					var saved *psdfile.Document
					editor, saved = smartWarpOracleSaved(t, editor, compression)
					if !bytes.Equal(smartWarpOracleLayer(t, saved).Pix, actual.Pix) {
						t.Fatal("saved identity cache differs from the independently known source bytes")
					}
					view, err := saved.ReadLayerMetadata(t.Context(), 0)
					if err != nil || !slices.Equal(view.Layer.Placed.Warp.MeshPoints, points) ||
						view.Layer.Placed.Warp.Bounds.Left != 5 || view.Layer.Placed.Warp.Bounds.Top != 7 {
						t.Fatalf("coordinate-domain values were rescaled or source bounds moved: %v", err)
					}
				}
				if sha256.Sum256(original) != digest {
					t.Fatal("warp edit changed the original input")
				}
			})
		}
	}
}

// smartWarpOracleInterior derives the two-segment raster inverse from the known cubic's lattice knots.
func smartWarpOracleInterior(source *image.NRGBA, documentX, documentY int) color.NRGBA {
	u := (float64(documentX) + 0.5 - 20) / 8
	boundary := 4 - 3*u
	if u > .5 {
		boundary = 1 + 3*u
	}
	v := (float64(documentY) + .5 - boundary) / 8
	x, y := 8*u-.5, 8*v-.5
	left, top := int(math.Floor(x)), int(math.Floor(y))
	xWeight, yWeight := x-float64(left), y-float64(top)
	var components [4]float64
	for row := range 2 {
		for column := range 2 {
			weightX, weightY := 1-xWeight, 1-yWeight
			if column != 0 {
				weightX = xWeight
			}
			if row != 0 {
				weightY = yWeight
			}
			value := source.NRGBAAt(left+column, top+row)
			for component, number := range []byte{value.R, value.G, value.B, value.A} {
				components[component] += float64(number) * weightX * weightY
			}
		}
	}
	return color.NRGBA{R: byte(math.RoundToEven(components[0])), G: byte(math.RoundToEven(components[1])),
		B: byte(math.RoundToEven(components[2])), A: byte(math.RoundToEven(components[3]))}
}

func TestSmartWarpOracleNativeCurveAndCurrentSource(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, replacement := range []bool{false, true} {
			t.Run(fmt.Sprintf("version%d/replacement%t", version, replacement), func(t *testing.T) {
				editor, _ := smartEditOpen(t, smartWarpOracleFixture(t, version, true))
				source := smartWarpOraclePixels(replacement)
				if replacement {
					contents := smartSizedPrototype(version, source.Rect.Size(), source.Rect, source)
					if err := editor.ReplaceContents(t.Context(), 1, contents); err != nil {
						t.Fatal(err)
					}
				}
				points := smartWarpOracleControls(true)
				if err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{}); err != nil {
					t.Fatal(err)
				}
				actual, bounds := smartWarpOracleCache(t, editor)
				if bounds != image.Rect(20, 2, 28, 12) {
					t.Fatalf("native control hull changed document bounds: %v", bounds)
				}
				for y := 5; y < 9; y++ {
					for x := 20; x < 28; x++ {
						expected := smartWarpOracleInterior(source, x, y)
						pixel := actual.NRGBAAt(x-bounds.Min.X, y-bounds.Min.Y)
						// Equivalent bilinear solves can straddle a half-byte rounding boundary.
						if math.Abs(float64(pixel.R)-float64(expected.R)) > 1 ||
							math.Abs(float64(pixel.G)-float64(expected.G)) > 1 ||
							math.Abs(float64(pixel.B)-float64(expected.B)) > 1 || pixel.A != expected.A {
							t.Fatalf("independent raster inverse at %d,%d: %v, want %v", x, y, pixel, expected)
						}
					}
				}
				if !replacement {
					// These edge bytes were measured independently in the native Photoshop fixture.
					for _, sample := range []struct {
						point image.Point
						pixel color.NRGBA
					}{
						{image.Pt(20, 3), color.NRGBA{R: 220, G: 30, B: 60, A: 48}},
						{image.Pt(21, 3), color.NRGBA{R: 220, G: 30, B: 60, A: 143}},
						{image.Pt(22, 3), color.NRGBA{R: 220, G: 30, B: 60, A: 239}},
						{image.Pt(23, 2), color.NRGBA{R: 20, G: 220, B: 80, A: 80}},
						{image.Pt(20, 11), color.NRGBA{R: 20, G: 220, B: 80, A: 207}},
					} {
						if pixel := actual.NRGBAAt(sample.point.X-20, sample.point.Y-2); pixel != sample.pixel {
							t.Fatalf("native edge sample %v: %v, want %v", sample.point, pixel, sample.pixel)
						}
					}
				}
				reopened, saved := smartWarpOracleSaved(t, editor, 3)
				if !bytes.Equal(smartWarpOracleLayer(t, saved).Pix, actual.Pix) {
					t.Fatal("saved curve changed independently checked source artwork")
				}
				if err := reopened.SetSmartObjectWarp(t.Context(), 1, smartWarpOracleControls(false),
					SmartWarpEditOptions{}); err != nil {
					t.Fatalf("reopened current source cannot continue editing: %v", err)
				}
			})
		}
	}
}

func TestSmartWarpOracleInvalidGeometryIsAtomic(t *testing.T) {
	for _, scenario := range []string{"count", "nan", "infinite", "huge", "fold", "collapsed", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			editor, _ := smartEditOpen(t, smartWarpOracleFixture(t, 1, true))
			if err := editor.SetSmartObjectWarp(t.Context(), 1, smartWarpOracleControls(true),
				SmartWarpEditOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := editor.Rename(1, "keep prior transaction"); err != nil {
				t.Fatal(err)
			}
			var before bytes.Buffer
			if err := editor.Write(t.Context(), &before); err != nil {
				t.Fatal(err)
			}
			points := smartWarpOracleControls(false)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "count":
				points = points[:15]
			case "nan":
				points[5].X = math.NaN()
			case "infinite":
				points[5].Y = math.Inf(1)
			case "huge":
				points[5].X = 1e20
			case "fold":
				for row := range 4 {
					points[row*4+1].X, points[row*4+2].X = 35, -17
				}
			case "collapsed":
				for index := range points {
					points[index].X = 5
				}
			case "cancel":
				cancel()
			}
			revision, owned := editor.revision, editor.ownedBytesLocked(nil)
			err := editor.SetSmartObjectWarp(ctx, 1, points, SmartWarpEditOptions{})
			if err == nil || editor.revision != revision || editor.ownedBytesLocked(nil) != owned {
				t.Fatalf("invalid geometry changed earlier edits or ownership: %v", err)
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
			var after bytes.Buffer
			if err := editor.Write(t.Context(), &after); err != nil || !bytes.Equal(before.Bytes(), after.Bytes()) {
				t.Fatalf("failed warp edit changed the next saved document: %v", err)
			}
		})
	}
}

func TestSmartWarpOracleIndependentCubicForwardAfterMetadataCommit(t *testing.T) {
	editor, _ := smartEditOpen(t, smartWarpOracleFixture(t, 1, true))
	if err := editor.SetSmartObjectWarp(t.Context(), 1, smartWarpOracleControls(true), SmartWarpEditOptions{}); err != nil {
		t.Fatal(err)
	}
	_, saved := smartWarpOracleSaved(t, editor, 0)
	view, err := saved.ReadLayerMetadata(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	patch, err := psdwarp.Compile(t.Context(), view.Layer.Placed)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []float64{0, .07, .25, .5, .71, .93, 1} {
		for _, v := range []float64{0, .11, .5, .88, 1} {
			actual, forwardErr := patch.Forward(u, v)
			expected := psdmodel.Point{X: 20 + 8*u, Y: 4 + 8*v - 6*u*(1-u)}
			if forwardErr != nil || math.Hypot(actual.X-expected.X, actual.Y-expected.Y) > 1e-11 {
				t.Fatalf("saved cubic at %g,%g: %v, want %v, %v", u, v, actual, expected, forwardErr)
			}
		}
	}
	// Rendering consumes the saved source and geometry, never the original poisoned cache.
	live, err := psdscene.Render(t.Context(), saved)
	if err != nil || live.NRGBAAt(22, 4) != (color.NRGBA{R: 220, G: 30, B: 60, A: 255}) {
		t.Fatalf("saved live source did not use the edited cubic: %v", err)
	}
}
