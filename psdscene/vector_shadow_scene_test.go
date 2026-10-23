package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdvector"
)

type vectorShadowSceneCase struct {
	name                string
	global              *bool
	notLinked, inverted bool
	density             byte
}

func vectorShadowSceneSource(
	t *testing.T, test vectorShadowSceneCase, pixelStage string, mixed bool, shift image.Point,
) []byte {
	t.Helper()
	data := shadowOverlaySceneSource(t, pixelStage == "global", 0, shift)
	index := cachedOpen(t, data)
	record := index.Layers[1]
	if pixelStage == "disabled" {
		data[record.Mask.Span.Offset+17] |= 2
	}
	// This fixture uses the valid eighteen-byte mask header, so the density parameter needs new encoded storage.
	data[record.Mask.Span.Offset+17] |= 16
	if record.Mask.Span.Length != 18 {
		t.Fatal("expected the source fixture's unparameterized short mask header")
	}
	for _, offset := range []int64{index.LayerMaskSection.Offset, index.LayerInfo.Offset} {
		binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+2)
	}
	binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length)+2)
	binary.BigEndian.PutUint32(data[record.MaskData.Offset:], 20)
	parameterPosition := record.Mask.Span.Offset + 18
	parameterized := append(bytes.Clone(data[:parameterPosition]), 4, test.density)
	data = append(parameterized, data[parameterPosition:]...)
	index = cachedOpen(t, data)
	record = index.Layers[1]
	if !mixed {
		for _, block := range record.AdditionalInfo {
			if block.Key != "lfx2" {
				continue
			}
			set, err := index.DecodeEffects(t.Context(), block)
			if err != nil {
				t.Fatal(err)
			}
			for _, effect := range set.Items {
				if effect.Kind != psdeffects.ColorOverlay {
					continue
				}
				for _, entry := range effect.Descriptor.Entries {
					if entry.Key == "enab" {
						data[entry.Data.Offset] = 0
					}
				}
			}
		}
	}
	bounds := record.Bounds
	mask := vectorStrokeTestMask(int(index.Header.Width), int(index.Header.Height),
		float64(bounds.Min.X)+.25, float64(bounds.Min.Y)+.5,
		float64(bounds.Min.X)+10.75, float64(bounds.Max.Y)-.75)
	mask.NotLinked, mask.Invert = test.notLinked, test.inverted
	encoded, err := psdvector.EncodeMask(t.Context(), mask)
	if err != nil {
		t.Fatal(err)
	}
	tags := artboardTestBlock("vmsk", encoded)
	if test.global != nil {
		value := byte(0)
		if *test.global {
			value = 1
		}
		tags = append(tags, artboardTestBlock("vmgm", []byte{value, 0, 0, 0})...)
	}
	for _, offset := range []int64{index.LayerMaskSection.Offset, index.LayerInfo.Offset} {
		binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(len(tags)))
	}
	binary.BigEndian.PutUint32(data[record.Extra.Offset-4:], uint32(record.Extra.Length)+uint32(len(tags)))
	insertion := record.Extra.Offset + record.Extra.Length
	result := append(bytes.Clone(data[:insertion]), tags...)
	return append(result, data[insertion:]...)
}

func translateVectorShadowReference(document *psdmodel.Document, translation image.Point) {
	document.Width += translation.X * 2
	document.Height += translation.Y * 2
	for _, layer := range flatten(document.Layers) {
		if layer == nil {
			continue
		}
		layer.Bounds = layer.Bounds.Add(translation)
		if layer.Mask != nil {
			layer.Mask.Bounds = layer.Mask.Bounds.Add(translation)
			if layer.Mask.SampleBounds != nil {
				bounds := layer.Mask.SampleBounds.Add(translation)
				layer.Mask.SampleBounds = &bounds
			}
		}
		if layer.VectorMask == nil {
			continue
		}
		mask := layer.VectorMask
		mask.Width, mask.Height = document.Width, document.Height
		for path := range mask.Subpaths {
			for index := range mask.Subpaths[path].Knots {
				knot := &mask.Subpaths[path].Knots[index]
				for _, point := range []*psdvector.Point{&knot.Anchor, &knot.Preceding, &knot.Leaving} {
					point.X += float64(translation.X)
					point.Y += float64(translation.Y)
				}
			}
		}
	}
}

func compareVectorShadowReference(t *testing.T, pixels, reference *image.NRGBA, translation image.Point) {
	t.Helper()
	for y := pixels.Rect.Min.Y; y < pixels.Rect.Max.Y; y++ {
		for x := pixels.Rect.Min.X; x < pixels.Rect.Max.X; x++ {
			actual := pixels.NRGBAAt(x, y)
			expected := reference.NRGBAAt(x+translation.X, y+translation.Y)
			if actual != expected {
				t.Fatalf("vector source coverage differs at %d,%d: got %v, complete-domain reference %v",
					x, y, actual, expected)
			}
		}
	}
}

func TestVectorShadowScenePreservesShapeOutsideOutputWindow(t *testing.T) {
	cases := []vectorShadowSceneCase{
		{name: "linked-default-shape", density: 255},
		{name: "unlinked-default-final", notLinked: true, inverted: true, density: 93},
		{name: "explicit-shape", global: new(false), notLinked: true, inverted: true, density: 255},
		{name: "explicit-final", global: new(true), density: 93},
		{name: "zero-density", global: new(false), density: 0},
	}
	for _, test := range cases {
		for _, pixelStage := range []string{"disabled", "shape", "global"} {
			for _, mixed := range []bool{false, true} {
				for _, shift := range []image.Point{{}, {X: -24, Y: 10}} {
					name := fmt.Sprintf("%s_pixel-%s_mixed%t_shift%d", test.name, pixelStage, mixed, shift.X)
					t.Run(name, func(t *testing.T) {
						data := vectorShadowSceneSource(t, test, pixelStage, mixed, shift)
						before := sha256.Sum256(data)
						source, reader := sourceArtboardOpen(t, data)
						complete, err := source.DecodeLayers(t.Context())
						if err != nil {
							t.Fatal(err)
						}
						translation := image.Pt(32, 32)
						// A larger complete domain independently covers source geometry outside the requested canvas.
						translateVectorShadowReference(complete, translation)
						options := psdcompose.Options{ApproximateDropShadow: true}
						reference, err := psdcompose.Render(t.Context(), complete, options)
						if err != nil {
							t.Fatal(err)
						}
						if pixelStage == "disabled" {
							for _, channel := range source.Layers[1].Channels {
								if channel.ID == -2 {
									reader.forbidden = append(reader.forbidden, channel.Data)
								}
							}
						}
						full, err := Render(t.Context(), source, Options{Compose: options})
						if err != nil {
							t.Fatal(err)
						}
						compareVectorShadowReference(t, full, reference, translation)
						if err := VisitTiles(t.Context(), source, full.Rect, image.Pt(7, 5), func(tile *image.NRGBA) error {
							compareVectorShadowReference(t, tile, reference, translation)
							return nil
						}, TileOptions{Compose: options}); err != nil {
							t.Fatal(err)
						}
						for _, bounds := range []image.Rectangle{image.Rect(0, 0, 1, 1), image.Rect(0, 15, 5, 24),
							image.Rect(18, 0, 32, 2)} {
							pixels, err := RenderTile(t.Context(), source, bounds, TileOptions{Compose: options})
							if err != nil {
								t.Fatal(err)
							}
							compareVectorShadowReference(t, pixels, reference, translation)
						}
						if reader.reads != 0 || sha256.Sum256(data) != before {
							t.Fatal("source rendering read saved preview/disabled mask samples or changed original bytes")
						}
					})
				}
			}
		}
	}
}

func TestVectorShadowSceneKeepsExplicitPolicyBudgetAndCancellation(t *testing.T) {
	data := vectorShadowSceneSource(t, vectorShadowSceneCase{density: 255}, "shape", true, image.Pt(-24, 10))
	source := cachedOpen(t, data)
	if _, err := Render(t.Context(), source); !cachedUnsupported(err) {
		t.Fatalf("active vector shadow approximation was silently enabled: %v", err)
	}
	options := TileOptions{Compose: psdcompose.Options{ApproximateDropShadow: true}, MaxWorkingBytes: 1}
	result, err := RenderTile(t.Context(), source, image.Rect(0, 15, 1, 16), options)
	if result != nil || !errors.Is(err, psdfile.ErrLimit) {
		t.Fatalf("vector-shadow source escaped a tiny regional budget: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	options.MaxWorkingBytes = 4 << 20
	result, err = RenderTile(ctx, source, image.Rect(0, 15, 1, 16), options)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("vector-shadow source ignored cancellation: %v", err)
	}
}
