package psdscene

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
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

// sceneUniformArray emits the typed native ObAr shape independently of the scene/model encoders.
func sceneUniformArray(class string, prefix int, keys []string, columns ...[]float64) []byte {
	result := artboardTestU32(uint32(prefix))
	result = append(result, artboardTestU32(0)...)
	result = append(result, cachedTestID(class)...)
	result = append(result, artboardTestU32(uint32(len(keys)))...)
	for index, key := range keys {
		result = append(result, cachedTestID(key)...)
		result = append(result, "UnFl#Pxl"...)
		result = append(result, artboardTestU32(uint32(len(columns[index])))...)
		for _, value := range columns[index] {
			result = append(result, artboardTestDouble(value)...)
		}
	}
	return result
}

func sceneUniformWarpWire(order, width, height int, guarded, nonuniform bool) []byte {
	count := (order - 1) / 3
	xs, ys := make([]float64, order*order), make([]float64, order*order)
	for row := range order {
		for column := range order {
			xs[row*order+column] = float64(column*width) / float64(order-1)
			ys[row*order+column] = float64(row*height) / float64(order-1)
		}
	}
	xSlices, ySlices := make([]float64, count+1), make([]float64, count+1)
	for index := range count + 1 {
		xSlices[index] = float64(index*width) / float64(count)
		ySlices[index] = float64(index*height) / float64(count)
	}
	if guarded {
		xSlices[0], ySlices[0] = -.6, -.6
		xSlices[count], ySlices[count] = float64(width)+.6, float64(height)+.6
	}
	if nonuniform {
		xSlices[1] += .1
	}
	envelope := artboardTestDescriptor("customEnvelopeWarp",
		artboardTestEntry{"quiltSliceX", "ObAr", sceneUniformArray("UntF", count+1,
			[]string{"quiltSliceX"}, xSlices)},
		artboardTestEntry{"quiltSliceY", "ObAr", sceneUniformArray("UntF", count+1,
			[]string{"quiltSliceY"}, ySlices)},
		artboardTestEntry{"meshPoints", "ObAr", sceneUniformArray("rationalPoint", order*order,
			[]string{"Hrzn", "Vrtc"}, xs, ys)})
	bounds := artboardTestDescriptor("classFloatRect",
		artboardTestEntry{"Left", "doub", artboardTestDouble(0)},
		artboardTestEntry{"Top ", "doub", artboardTestDouble(0)},
		artboardTestEntry{"Rght", "doub", artboardTestDouble(float64(width))},
		artboardTestEntry{"Btom", "doub", artboardTestDouble(float64(height))})
	return artboardTestDescriptor("quiltWarp",
		artboardTestEntry{"warpStyle", "enum", cachedTestEnum("warpStyle", "warpCustom")},
		artboardTestEntry{"warpRotate", "enum", cachedTestEnum("Ornt", "Hrzn")},
		artboardTestEntry{"uOrder", "long", artboardTestU32(4)},
		artboardTestEntry{"vOrder", "long", artboardTestU32(4)},
		artboardTestEntry{"deformNumRows", "long", artboardTestU32(uint32(order))},
		artboardTestEntry{"deformNumCols", "long", artboardTestU32(uint32(order))},
		artboardTestEntry{"bounds", "Objc", bounds},
		artboardTestEntry{"customEnvelopeWarp", "Objc", envelope})
}

func sceneUniformPlacementWire(
	id string, order, width, height int, quad []float64, guarded, nonuniform bool,
) []byte {
	points := artboardTestU32(uint32(len(quad)))
	for _, value := range quad {
		points = append(points, "doub"...)
		points = append(points, artboardTestDouble(value)...)
	}
	size := artboardTestDescriptor("size",
		artboardTestEntry{"Wdth", "doub", artboardTestDouble(float64(width))},
		artboardTestEntry{"Hght", "doub", artboardTestDouble(float64(height))})
	descriptor := artboardTestDescriptor("null",
		artboardTestEntry{"Idnt", "TEXT", cachedTestUnicode(id)},
		artboardTestEntry{"Type", "long", artboardTestU32(2)},
		artboardTestEntry{"Sz  ", "Objc", size},
		artboardTestEntry{"Trnf", "VlLs", points},
		artboardTestEntry{"quiltWarp", "Objc", sceneUniformWarpWire(order, width, height, guarded, nonuniform)})
	return append([]byte("soLD\x00\x00\x00\x04\x00\x00\x00\x10"), descriptor...)
}

func sceneUniformOriginalPixels() *image.NRGBA {
	result := image.NewNRGBA(image.Rect(0, 0, 18, 12))
	for y := range result.Rect.Dy() {
		for x := range result.Rect.Dx() {
			result.SetNRGBA(x, y, color.NRGBA{R: byte(20 + x*12), G: byte(30 + y*17),
				B: byte(10 + x*3 + y*4), A: 255})
		}
	}
	return result
}

func sceneUniformOriginalPSD() []byte {
	pixels := sceneUniformOriginalPixels()
	var record, samples bytes.Buffer
	_ = binary.Write(&record, binary.BigEndian, int16(-1))
	for _, value := range []int32{0, 0, 12, 18} {
		_ = binary.Write(&record, binary.BigEndian, value)
	}
	_ = binary.Write(&record, binary.BigEndian, uint16(4))
	for index, id := range []int16{0, 1, 2, -1} {
		_ = binary.Write(&record, binary.BigEndian, id)
		_ = binary.Write(&record, binary.BigEndian, uint32(2+18*12))
		samples.Write([]byte{0, 0})
		for offset := index; offset < len(pixels.Pix); offset += 4 {
			samples.WriteByte(pixels.Pix[offset])
		}
	}
	record.WriteString("8BIMnorm")
	record.Write([]byte{255, 0, 0, 0})
	extra := append(make([]byte, 8), 1, 'L', 0, 0)
	_ = binary.Write(&record, binary.BigEndian, uint32(len(extra)))
	record.Write(extra)
	record.Write(samples.Bytes())
	section := append(artboardTestU32(uint32(record.Len())), record.Bytes()...)
	section = append(section, 0, 0, 0, 0)
	result := binary.BigEndian.AppendUint16([]byte("8BPS"), 1)
	result = append(result, make([]byte, 6)...)
	result = binary.BigEndian.AppendUint16(result, 4)
	result = append(result, artboardTestU32(12)...)
	result = append(result, artboardTestU32(18)...)
	result = binary.BigEndian.AppendUint16(result, 8)
	result = binary.BigEndian.AppendUint16(result, 3)
	result = append(result, make([]byte, 8)...)
	result = append(result, artboardTestU32(uint32(len(section)))...)
	result = append(result, section...)
	return append(append(result, 0, 0), bytes.Repeat([]byte{7}, 18*12*4)...)
}

func sceneUniformFixture(
	t *testing.T, order int, origin image.Point, guarded, nonuniform, projective bool,
) []byte {
	t.Helper()
	x, y := float64(origin.X), float64(origin.Y)
	quad := []float64{x, y, x + 54, y, x + 54, y + 36, x, y + 36}
	if projective {
		quad[4] += 2
	}
	data := recursiveTextParentFixture(t, sceneUniformOriginalPSD(), false, image.Pt(64, 48), quad)
	return placedFXReplaceBlock(t, data, "SoLd",
		sceneUniformPlacementWire("recursive-text", order, 18, 12, quad, guarded, nonuniform), 0)
}

// sceneUniformTypedCoordinate independently maps geometric piece position through the complete typed source window.
func sceneUniformTypedCoordinate(position, extent float64, count int, guarded bool) float64 {
	piece := min(count-1, int(math.Floor(position*float64(count))))
	local := position*float64(count) - float64(piece)
	guard := 0.0
	if guarded {
		guard = .6
	}
	left, right := float64(piece)*extent/float64(count), float64(piece+1)*extent/float64(count)
	if piece == 0 {
		left -= guard
	}
	if piece == count-1 {
		right += guard
	}
	return (left + local*(right-left) + guard) / (extent + guard*2)
}

func TestUniformGridSceneLiveSourcesKeepTypedGuardsAndRegionalPhase(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, order := range []int{10, 13} {
			for _, origin := range []image.Point{{X: 4, Y: 3}, {X: -9, Y: -4}} {
				t.Run(fmt.Sprintf("v%d_order%d_origin%v", version, order, origin), func(t *testing.T) {
					data := sceneUniformFixture(t, order, origin, true, false, false)
					if version == 2 {
						data = recursiveTextPSB(t, data)
					}
					before := sha256.Sum256(data)
					defaultSource := cachedOpen(t, data)
					if result, operationErr := Render(t.Context(), defaultSource); result != nil || !cachedUnsupported(operationErr) {
						t.Fatalf("uniform quilts became implicitly approximate: %v", operationErr)
					}
					defaultRegion := image.Rect(3, 5, 53, 39)
					defaultTile, tileErr := RenderTile(t.Context(), defaultSource, defaultRegion)
					if !cachedUnsupported(tileErr) || defaultTile != nil {
						t.Fatalf("regional uniform source enabled approximation implicitly: %v", tileErr)
					}
					visited := false
					operationErr := VisitTiles(t.Context(), defaultSource, image.Rect(0, 0, 64, 48), image.Pt(7, 5),
						func(*image.NRGBA) error {
							visited = true
							return nil
						})
					if !cachedUnsupported(operationErr) || visited {
						t.Fatalf("uniform tile visitor published implicitly approximate pixels: %v", operationErr)
					}
					defaultPlaced, placedErr := RenderPlacedLayer(t.Context(), defaultSource, 0)
					if !cachedUnsupported(placedErr) || defaultPlaced != nil {
						t.Fatalf("placed helper enabled uniform approximation implicitly: %v", placedErr)
					}
					source, reader := sourceArtboardOpen(t, data)
					for _, channel := range source.Layers[0].Channels {
						reader.forbidden = append(reader.forbidden, channel.Data)
					}
					links, err := source.LinkedFiles(t.Context())
					if err != nil || len(links) != 1 {
						t.Fatalf("original source metadata: %v", err)
					}
					child, err := source.OpenLinkedDocument(t.Context(), links[0])
					if err != nil {
						t.Fatal(err)
					}
					reader.forbidden = append(reader.forbidden, psdfile.Span{
						Offset: links[0].Data.Offset + child.Composite.Data.Offset, Length: child.Composite.Data.Length})
					_ = child.Close()
					settings := Options{ApproximateCustomWarp: true, MaxWorkingBytes: 8 << 20}
					full, err := Render(t.Context(), source, settings)
					if err != nil {
						t.Fatal(err)
					}
					count, checked, distinguished := (order-1)/3, 0, false
					for y := max(0, origin.Y+5); y < min(48, origin.Y+31); y++ {
						for x := max(0, origin.X+5); x < min(64, origin.X+49); x++ {
							u := sceneUniformTypedCoordinate((float64(x-origin.X)+.5)/54, 18, count, true)
							v := sceneUniformTypedCoordinate((float64(y-origin.Y)+.5)/36, 12, count, true)
							sx, sy := u*18-.5, v*12-.5
							expected := color.NRGBA{R: byte(math.RoundToEven(20 + sx*12)),
								G: byte(math.RoundToEven(30 + sy*17)), B: byte(math.RoundToEven(10 + sx*3 + sy*4)), A: 255}
							actual := full.NRGBAAt(x, y)
							for component, want := range []byte{expected.R, expected.G, expected.B, expected.A} {
								got := []byte{actual.R, actual.G, actual.B, actual.A}[component]
								if math.Abs(float64(got)-float64(want)) > 1 {
									t.Fatalf("typed source coordinates differ at %d,%d: got %v want %v", x, y, actual, expected)
								}
							}
							plain := 20 + (((float64(x-origin.X)+.5)/54)*18-.5)*12
							plainGreen := 30 + (((float64(y-origin.Y)+.5)/36)*12-.5)*17
							distinguished = distinguished || math.Abs(plain-float64(expected.R)) > 2 ||
								math.Abs(plainGreen-float64(expected.G)) > 2
							checked++
						}
					}
					if checked < 500 || !distinguished {
						t.Fatal("independent coordinate check did not distinguish padded guards from discarded guards")
					}
					tileSettings := TileOptions{ApproximateCustomWarp: true, MaxWorkingBytes: 8 << 20}
					region := image.Rect(3, 5, 53, 39)
					tile, err := RenderTile(t.Context(), source, region, tileSettings)
					if err != nil {
						t.Fatal(err)
					}
					compare := func(pixels *image.NRGBA) error {
						for y := pixels.Rect.Min.Y; y < pixels.Rect.Max.Y; y++ {
							for x := pixels.Rect.Min.X; x < pixels.Rect.Max.X; x++ {
								if pixels.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
									return fmt.Errorf("regional phase changed at %d,%d", x, y)
								}
							}
						}
						return nil
					}
					if err := compare(tile); err != nil {
						t.Fatal(err)
					}
					if err := VisitTiles(t.Context(), source, full.Rect, image.Pt(7, 5), compare, tileSettings); err != nil {
						t.Fatal(err)
					}
					materialized, err := RenderPlacedLayer(t.Context(), source, 0, settings)
					if err != nil || materialized.Placed == nil || materialized.Placed.Warp == nil ||
						materialized.Placed.Warp.DeformRows != order ||
						!materialized.RenderedEffects.Valid(materialized.Pixels, materialized.Bounds, materialized.Placed) {
						t.Fatalf("placed helper lost original artwork or current uniform geometry: %v", err)
					}
					for y := range full.Rect.Dy() {
						for x := range full.Rect.Dx() {
							expected := materialized.Pixels.NRGBAAt(x-materialized.Bounds.Min.X,
								y-materialized.Bounds.Min.Y)
							if full.NRGBAAt(x, y) != expected {
								t.Fatalf("single placed source changed scene projection at %d,%d", x, y)
							}
						}
					}
					if reader.reads != 0 || sha256.Sum256(data) != before {
						t.Fatal("uniform projection used parent/merged cache samples or changed input bytes")
					}
				})
			}
		}
	}
}

func TestUniformGridSceneExternalSharedSourcesRemainExplicit(t *testing.T) {
	for _, order := range []int{10, 13} {
		data := externalRasterFixture(t, []externalRasterFixtureLink{{id: "shared", name: "chosen.png",
			fileType: "PNGf", cached: bytes.Repeat([]byte{99}, 64)}}, 0, 0)
		for index := range 2 {
			x := float64(index * 2)
			quad := []float64{x, 0, x + 2, 0, x + 2, 1, x, 1}
			data = placedFXReplaceBlock(t, data, "SoLd",
				sceneUniformPlacementWire("shared", order, 2, 1, quad, false, false), index)
		}
		source, reader := sourceArtboardOpen(t, data)
		for _, layer := range source.Layers {
			for _, channel := range layer.Channels {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
		links, err := source.LinkedFiles(t.Context())
		if err != nil || len(links) != 1 || !links[0].HasData {
			t.Fatalf("cached external payload fixture absent: %v", err)
		}
		reader.forbidden = append(reader.forbidden, links[0].Data)
		settings := Options{ApproximateCustomWarp: true}
		if pixels, err := Render(t.Context(), source, settings); !cachedUnsupported(err) || pixels != nil {
			t.Fatalf("explicit warp selected an implicit external source: %v", err)
		}
		calls := 0
		encoded := externalRasterPNG(t, externalRasterInput(), nil)
		resolver := func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
			calls++
			return encoded, nil
		}
		settings.ExternalRasterResolver = resolver
		full, err := Render(t.Context(), source, settings)
		if err != nil || calls != 1 {
			t.Fatalf("shared uniform instances resolved more than one original source: calls=%d err=%v", calls, err)
		}
		calls = 0
		tile, err := RenderTile(t.Context(), source, full.Rect,
			TileOptions{ApproximateCustomWarp: true, ExternalRasterResolver: resolver})
		if err != nil || calls != 1 || !bytes.Equal(tile.Pix, full.Pix) || reader.reads != 0 {
			t.Fatalf("regional uniform instances changed shared-source ownership: calls=%d err=%v", calls, err)
		}
	}
}

func TestUniformGridSceneOpacityWindowFiltersLimitsAndRejections(t *testing.T) {
	for _, order := range []int{10, 13} {
		source, reader := sourceArtboardOpen(t, sceneUniformFixture(t, order, image.Pt(-9, -4), true, false, false))
		metadata, err := source.ReadLayerMetadata(t.Context(), 0)
		if err != nil {
			t.Fatal(err)
		}
		prototype := sceneUniformOriginalPixels()
		filterPolicy := psdfilter.Options{ApproximateGaussian: true}
		engine := rgbTileRenderer{settings: TileOptions{MaxWorkingBytes: 8 << 20,
			ApproximateCustomWarp: true, SmartFilters: &filterPolicy}}
		whole := image.Rect(-12, -8, 51, 36)
		projected, err := engine.opacityWindow(t.Context(), metadata.Layer, prototype, whole, psdfilter.Stack{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		stack := psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{{Kind: psdfilter.GaussianBlur,
			Enabled: true, Radius: 1, Opacity: 1, BlendMode: "norm"}}}
		expected, err := psdfilter.Apply(t.Context(), projected, stack, filterPolicy)
		if err != nil {
			t.Fatal(err)
		}
		window := image.Rect(-2, 1, 27, 24)
		actual, err := engine.opacityWindow(t.Context(), metadata.Layer, prototype, window.Inset(-6), stack, []bool{false})
		if err != nil {
			t.Fatal(err)
		}
		for y := window.Min.Y; y < window.Max.Y; y++ {
			for x := window.Min.X; x < window.Max.X; x++ {
				if actual.NRGBAAt(x, y) != expected.NRGBAAt(x, y) {
					t.Fatalf("uniform opacity/filter source window changed full-image pixels at %d,%d", x, y)
				}
			}
		}
		for _, channel := range source.Layers[0].Channels {
			reader.forbidden = append(reader.forbidden, channel.Data)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		settings := Options{ApproximateCustomWarp: true}
		if result, err := Render(ctx, source, settings); !errors.Is(err, context.Canceled) || result != nil {
			t.Fatalf("uniform scene ignored cancellation: %v", err)
		}
		settings.MaxWorkingBytes = 1
		if result, err := Render(t.Context(), source, settings); !errors.Is(err, psdfile.ErrLimit) || result != nil {
			t.Fatalf("uniform geometry/output bypassed scene working budget: %v", err)
		}
		for _, malformed := range []struct{ nonuniform, projective bool }{{true, false}, {false, true}} {
			unsupported, guardedReader := sourceArtboardOpen(t,
				sceneUniformFixture(t, order, image.Point{}, true, malformed.nonuniform, malformed.projective))
			for _, channel := range unsupported.Layers[0].Channels {
				guardedReader.forbidden = append(guardedReader.forbidden, channel.Data)
			}
			settings.MaxWorkingBytes = 0
			result, renderErr := Render(t.Context(), unsupported, settings)
			if !cachedUnsupported(renderErr) || result != nil || guardedReader.reads != 0 {
				t.Fatalf("nonuniform/projective grid was normalized or read a cached replacement: %v", renderErr)
			}
		}
		if reader.reads != 0 {
			t.Fatal("cancellation or low-budget planning read a parent/merged cache")
		}
	}
}
