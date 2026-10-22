package psdscene_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func precisionCoordinatePlanes(depth uint16, values []float64) []byte {
	var result []byte
	for _, value := range values {
		if depth == 16 {
			result = binary.BigEndian.AppendUint16(result, uint16(value))
		} else {
			result = binary.BigEndian.AppendUint32(result, math.Float32bits(float32(value)))
		}
	}
	return result
}

func TestPrecisionMaskStoredCoordinatesFullROIAndVisitor(t *testing.T) {
	canvas := image.Rect(0, 0, 12, 10)
	maskBounds := image.Rect(1, 2, 5, 5)
	for _, depth := range []uint16{16, 32} {
		for _, flags := range []byte{0, 1} {
			for _, origin := range []image.Point{{}, {3, 4}, {-2, -1}} {
				for _, real := range []bool{false, true} {
					name := fmt.Sprintf("depth%d/flags%d/origin%d_%d/real%t", depth, flags, origin.X, origin.Y, real)
					t.Run(name, func(t *testing.T) {
						layerBounds := image.Rectangle{Min: origin, Max: origin.Add(image.Pt(7, 6))}
						layer := precisionLayerFixture{bounds: layerBounds, opacity: 255}
						color, opaque := 0.25, 1.0
						if depth == 16 {
							color, opaque = 16384, 65535
						}
						for _, id := range []int16{0, 1, 2, -1} {
							values := make([]float64, 42)
							for index := range values {
								values[index] = color
								if id == -1 {
									values[index] = opaque
								}
							}
							layer.channels = append(layer.channels, precisionChannel{id, precisionCoordinatePlanes(depth, values)})
						}
						values := make([]float64, 12)
						for index := range values {
							values[index] = float64(index+1) / 16
							if depth == 16 {
								values[index] = float64((index + 1) * 4096)
							}
						}
						maskFlags := flags
						if real {
							maskFlags |= 8
						}
						layer.mask = precisionMask(maskBounds, 0, maskFlags, nil, false)
						layer.channels = append(layer.channels, precisionChannel{-2, precisionCoordinatePlanes(depth, values)})
						if real {
							layer.mask = layer.mask[:18]
							layer.mask = append(layer.mask, flags, 0)
							for _, edge := range []int{2, 1, 5, 5} {
								layer.mask = binary.BigEndian.AppendUint32(layer.mask, uint32(edge))
							}
							layer.channels = append(layer.channels, precisionChannel{-3, precisionCoordinatePlanes(depth, values)})
						}
						data := precisionSourceFixture(depth, 3, canvas.Dx(), canvas.Dy(), []precisionLayerFixture{layer})
						original := bytes.Clone(data)
						source, _ := precisionSource(t, data)
						check := func(pixels *psdprecision.FloatImage, rectangle image.Rectangle) {
							t.Helper()
							for y := rectangle.Min.Y; y < rectangle.Max.Y; y++ {
								for x := rectangle.Min.X; x < rectangle.Max.X; x++ {
									want := 0.0
									point := image.Pt(x, y)
									if point.In(layerBounds) && point.In(maskBounds) {
										want = values[(y-2)*4+x-1]
										if depth == 16 {
											want /= 65535
										}
									}
									offset := ((y-rectangle.Min.Y)*rectangle.Dx()+x-rectangle.Min.X)*4 + 3
									if math.Abs(pixels.Pix[offset]-want) > 1e-12 {
										t.Fatalf("stored mask (%d,%d) coverage shifted: %g != %g", x, y, pixels.Pix[offset], want)
									}
								}
							}
						}
						full, err := psdscene.RenderPrecision(nil, source)
						if err != nil {
							t.Fatal(err)
						}
						check(full, canvas)
						region, err := psdscene.RenderPrecisionTile(nil, source, image.Rect(2, 1, 6, 6))
						if err != nil {
							t.Fatal(err)
						}
						check(region.Image, region.Bounds)
						visits := 0
						err = psdscene.VisitPrecisionTiles(nil, source, canvas, image.Pt(5, 3),
							func(tile *psdprecision.RegionImage) error {
								check(tile.Image, tile.Bounds)
								visits++
								return nil
							})
						if err != nil || visits != 12 || !bytes.Equal(data, original) {
							t.Fatalf("visitor or original source changed: %d / %v", visits, err)
						}
					})
				}
			}
		}
	}
}
