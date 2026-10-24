package psdscene

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

type scenePresetWire struct {
	style, rotation       string
	uOrder, vOrder        uint32
	width, height, bend   float64
	perspective, opposite float64
	sizeFields            []artboardTestEntry
	boundsFields          []artboardTestEntry
}

// scenePresetPlacementWire encodes native preset records independently of model and geometry encoders.
func scenePresetPlacementWire(
	id string, width, height int, bend float64, quad []float64, change func(*scenePresetWire),
) []byte {
	settings := scenePresetWire{style: "warpArcLower", rotation: "Hrzn", uOrder: 4, vOrder: 2,
		width: float64(width), height: float64(height), bend: bend}
	if change != nil {
		change(&settings)
	}
	points := artboardTestU32(uint32(len(quad)))
	for _, value := range quad {
		points = append(points, "doub"...)
		points = append(points, artboardTestDouble(value)...)
	}
	sizeFields := []artboardTestEntry{
		{"Wdth", "doub", artboardTestDouble(settings.width)},
		{"Hght", "doub", artboardTestDouble(settings.height)}}
	if settings.sizeFields != nil {
		sizeFields = settings.sizeFields
	}
	size := artboardTestDescriptor("size", sizeFields...)
	boundsFields := []artboardTestEntry{
		{"Left", "doub", artboardTestDouble(0)},
		{"Top ", "doub", artboardTestDouble(0)},
		{"Rght", "doub", artboardTestDouble(settings.width)},
		{"Btom", "doub", artboardTestDouble(settings.height)}}
	if settings.boundsFields != nil {
		boundsFields = settings.boundsFields
	}
	bounds := artboardTestDescriptor("classFloatRect", boundsFields...)
	warp := artboardTestDescriptor("warp",
		artboardTestEntry{"warpStyle", "enum", cachedTestEnum("warpStyle", settings.style)},
		artboardTestEntry{"warpRotate", "enum", cachedTestEnum("Ornt", settings.rotation)},
		artboardTestEntry{"warpValue", "doub", artboardTestDouble(settings.bend)},
		artboardTestEntry{"warpPerspective", "doub", artboardTestDouble(settings.perspective)},
		artboardTestEntry{"warpPerspectiveOther", "doub", artboardTestDouble(settings.opposite)},
		artboardTestEntry{"uOrder", "long", artboardTestU32(settings.uOrder)},
		artboardTestEntry{"vOrder", "long", artboardTestU32(settings.vOrder)},
		artboardTestEntry{"bounds", "Objc", bounds})
	descriptor := artboardTestDescriptor("null",
		artboardTestEntry{"Idnt", "TEXT", cachedTestUnicode(id)},
		artboardTestEntry{"Type", "long", artboardTestU32(2)},
		artboardTestEntry{"Sz  ", "Objc", size},
		artboardTestEntry{"Trnf", "VlLs", points},
		artboardTestEntry{"warp", "Objc", warp})
	return append([]byte("soLD\x00\x00\x00\x04\x00\x00\x00\x10"), descriptor...)
}

func scenePresetFixture(t *testing.T, origin image.Point, bend float64, change func(*scenePresetWire)) []byte {
	t.Helper()
	x, y := float64(origin.X), float64(origin.Y)
	quad := []float64{x, y, x + 54, y, x + 54, y + 36, x, y + 36}
	data := recursiveTextParentFixture(t, sceneUniformOriginalPSD(), false, image.Pt(64, 48), quad)
	return placedFXReplaceBlock(t, data, "SoLd",
		scenePresetPlacementWire("recursive-text", 18, 12, bend, quad, change), 0)
}

// scenePresetNativeUV inverts the explicit mathematical lattice of recorded native Arc Lower controls.
// The records are width96, bend25: handleX30.73388390139061 and handleY12.73039122030451.
// No production geometry, trigonometric control generator or inverse sampler supplies the oracle.
func scenePresetNativeUV(x, y float64, bend float64) (float64, float64, bool) {
	width, height := 18.0, 12.0
	handleX, handleY := 30.73388390139061*width/96, 12.73039122030451*width/96
	if bend < 0 {
		handleY = -handleY
	}
	if bend == 0 {
		handleX, handleY = width/3, 0
	}
	px, py := x*width, y*(height+math.Max(0, handleY))
	bottom := func(u float64) (float64, float64) {
		// Source width18 selects five complete-source lattice columns regardless of the requested region.
		column := min(4, int(math.Floor(u*5)))
		fraction := u*5 - float64(column)
		first, last := float64(column)/5, float64(column+1)/5
		cubicX := func(value float64) float64 {
			return 3*(1-value)*(1-value)*value*handleX +
				3*(1-value)*value*value*(width-handleX) + value*value*value*width
		}
		return cubicX(first)*(1-fraction) + cubicX(last)*fraction,
			height + 3*(first*(1-first)*(1-fraction)+last*(1-last)*fraction)*handleY
	}
	left, right := 0.0, 1.0
	for range 70 {
		u := (left + right) / 2
		bottomX, bottomY := bottom(u)
		v := py / bottomY
		projectedX := width*u*(1-v) + bottomX*v
		if projectedX < px {
			left = u
		} else {
			right = u
		}
	}
	u := (left + right) / 2
	_, bottomY := bottom(u)
	v := py / bottomY
	return u, v, x >= 0 && x <= 1 && v >= 0 && v <= 1
}

func TestPresetSceneLiveSourcesKeepNativeControlsAndRegionalPhase(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, bend := range []float64{25, -25, 0} {
			for _, origin := range []image.Point{{X: 4, Y: 3}, {X: -9, Y: -4}} {
				t.Run(fmt.Sprintf("v%d_bend%g_origin%v", version, bend, origin), func(t *testing.T) {
					data := scenePresetFixture(t, origin, bend, nil)
					if version == 2 {
						data = recursiveTextPSB(t, data)
					}
					before := sha256.Sum256(data)
					defaultSource := cachedOpen(t, data)
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
					if pixels, err := Render(t.Context(), defaultSource); pixels != nil || !cachedUnsupported(err) {
						t.Fatalf("preset pixels became implicitly approximate: %v", err)
					}
					region := image.Rect(3, 5, 53, 39)
					if pixels, err := RenderTile(t.Context(), defaultSource, region); pixels != nil || !cachedUnsupported(err) {
						t.Fatalf("regional preset pixels became implicitly approximate: %v", err)
					}
					visited := false
					err = VisitTiles(t.Context(), defaultSource, region, image.Pt(7, 5), func(*image.NRGBA) error {
						visited = true
						return nil
					})
					if !cachedUnsupported(err) || visited {
						t.Fatalf("preset visitor published implicitly approximate pixels: %v", err)
					}
					if layer, err := RenderPlacedLayer(t.Context(), defaultSource, 0); layer != nil || !cachedUnsupported(err) {
						t.Fatalf("placed helper enabled preset approximation implicitly: %v", err)
					}
					settings := Options{ApproximateCustomWarp: true, MaxWorkingBytes: 8 << 20}
					full, err := Render(t.Context(), source, settings)
					if err != nil {
						t.Fatal(err)
					}
					checked, distinguished := 0, bend == 0
					for y := range full.Rect.Dy() {
						for x := range full.Rect.Dx() {
							u, v, inside := scenePresetNativeUV((float64(x-origin.X)+.5)/54,
								(float64(y-origin.Y)+.5)/36, bend)
							if !inside || u < .08 || u > .92 || v < .08 || v > .92 {
								continue
							}
							sx, sy := u*18-.5, v*12-.5
							expected := color.NRGBA{R: byte(math.RoundToEven(20 + sx*12)),
								G: byte(math.RoundToEven(30 + sy*17)), B: byte(math.RoundToEven(10 + sx*3 + sy*4)), A: 255}
							actual := full.NRGBAAt(x, y)
							for component, want := range []byte{expected.R, expected.G, expected.B, expected.A} {
								got := []byte{actual.R, actual.G, actual.B, actual.A}[component]
								if math.Abs(float64(got)-float64(want)) > 1 {
									t.Fatalf("native preset source UV differs at %d,%d: got %v want %v", x, y, actual, expected)
								}
							}
							plainGreen := 30 + (((float64(y-origin.Y)+.5)/36)*12-.5)*17
							distinguished = distinguished || math.Abs(plainGreen-float64(expected.G)) > 3
							checked++
						}
					}
					if checked < 600 || !distinguished {
						t.Fatal("independent native control oracle did not distinguish preset from a flat quad")
					}
					compare := func(pixels *image.NRGBA) error {
						for y := pixels.Rect.Min.Y; y < pixels.Rect.Max.Y; y++ {
							for x := pixels.Rect.Min.X; x < pixels.Rect.Max.X; x++ {
								if pixels.NRGBAAt(x, y) != full.NRGBAAt(x, y) {
									return fmt.Errorf("preset regional phase changed at %d,%d", x, y)
								}
							}
						}
						return nil
					}
					tileSettings := TileOptions{ApproximateCustomWarp: true, MaxWorkingBytes: 8 << 20}
					tile, err := RenderTile(t.Context(), source, region, tileSettings)
					if err != nil {
						t.Fatal(err)
					}
					if err := compare(tile); err != nil {
						t.Fatal(err)
					}
					if err := VisitTiles(t.Context(), source, full.Rect, image.Pt(7, 5), compare, tileSettings); err != nil {
						t.Fatal(err)
					}
					materialized, err := RenderPlacedLayer(t.Context(), source, 0, settings)
					if err != nil || materialized.Placed == nil || materialized.Placed.Warp == nil ||
						materialized.Placed.Warp.Style != "warpArcLower" || materialized.Placed.Warp.Value != bend ||
						!materialized.RenderedEffects.Valid(materialized.Pixels, materialized.Bounds, materialized.Placed) {
						t.Fatalf("placed helper lost original preset metadata: %v", err)
					}
					for y := range full.Rect.Dy() {
						for x := range full.Rect.Dx() {
							expected := materialized.Pixels.NRGBAAt(x-materialized.Bounds.Min.X,
								y-materialized.Bounds.Min.Y)
							actual := full.NRGBAAt(x, y)
							if actual.A != expected.A || actual.A != 0 && actual != expected {
								t.Fatalf("placed helper differs from preset scene projection at %d,%d: %v / %v",
									x, y, full.NRGBAAt(x, y), expected)
							}
						}
					}
					if reader.reads != 0 || sha256.Sum256(data) != before {
						t.Fatal("preset source projection read saved caches or mutated source bytes")
					}
				})
			}
		}
	}
}

func TestPresetSceneExternalSharedSourcesRemainExplicit(t *testing.T) {
	for _, bend := range []float64{0, 25} {
		t.Run(fmt.Sprintf("bend%g", bend), func(t *testing.T) {
			data := externalRasterFixture(t, []externalRasterFixtureLink{{id: "shared", name: "chosen.png",
				fileType: "PNGf", cached: bytes.Repeat([]byte{99}, 64)}}, 0, 0)
			for index := range 2 {
				x := float64(index * 2)
				quad := []float64{x, 0, x + 2, 0, x + 2, 1, x, 1}
				data = placedFXReplaceBlock(t, data, "SoLd",
					scenePresetPlacementWire("shared", 2, 1, bend, quad, nil), index)
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
			if pixels, err := Render(t.Context(), source, settings); pixels != nil || !cachedUnsupported(err) {
				t.Fatalf("preset approximation selected implicit external source: %v", err)
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
				t.Fatalf("shared preset instances resolved more than one source: calls=%d err=%v", calls, err)
			}
			calls = 0
			tile, err := RenderTile(t.Context(), source, full.Rect,
				TileOptions{ApproximateCustomWarp: true, ExternalRasterResolver: resolver})
			if err != nil || calls != 1 || !bytes.Equal(tile.Pix, full.Pix) || reader.reads != 0 {
				t.Fatalf("regional preset instances changed source ownership: calls=%d err=%v", calls, err)
			}
			calls = 0
			err = VisitTiles(t.Context(), source, full.Rect, full.Rect.Size(), func(pixels *image.NRGBA) error {
				for x := pixels.Rect.Min.X; x < pixels.Rect.Max.X; x++ {
					if pixels.NRGBAAt(x, 0) != full.NRGBAAt(x, 0) {
						return fmt.Errorf("external preset visitor differs at %d", x)
					}
				}
				return nil
			}, TileOptions{ApproximateCustomWarp: true, ExternalRasterResolver: resolver})
			if err != nil || calls != 1 || reader.reads != 0 {
				t.Fatalf("preset visitor lost shared source reuse: calls=%d err=%v", calls, err)
			}
			if pixels, err := Render(t.Context(), source,
				Options{ApproximateCustomWarp: true}); pixels != nil || !cachedUnsupported(err) {
				t.Fatalf("external mapping survived its render call: %v", err)
			}
			if pixels, err := Render(t.Context(), source,
				Options{ExternalRasterResolver: resolver}); pixels != nil || !cachedUnsupported(err) {
				t.Fatalf("preset approximation survived its render call: %v", err)
			}
			if pixels, err := RenderTile(t.Context(), source, full.Rect,
				TileOptions{ApproximateCustomWarp: true}); pixels != nil || !cachedUnsupported(err) {
				t.Fatalf("external mapping survived its regional render call: %v", err)
			}
		})
	}
}

func TestPresetSceneOpacityWindowFiltersLimitsAndRejections(t *testing.T) {
	source, reader := sourceArtboardOpen(t, scenePresetFixture(t, image.Pt(-9, -4), 25, nil))
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
				t.Fatalf("preset opacity/filter window changed full-image phase at %d,%d", x, y)
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
		t.Fatalf("preset scene ignored cancellation: %v", err)
	}
	settings.MaxWorkingBytes = 1
	if result, err := Render(t.Context(), source, settings); !errors.Is(err, psdfile.ErrLimit) || result != nil {
		t.Fatalf("preset geometry/output bypassed scene budget: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*scenePresetWire)
	}{
		{"unknown", func(w *scenePresetWire) { w.style = "warpCylinder" }},
		{"vertical", func(w *scenePresetWire) { w.rotation = "Vrtc" }},
		{"perspective", func(w *scenePresetWire) { w.perspective = 1 }},
		{"oppositePerspective", func(w *scenePresetWire) { w.opposite = 1 }},
		{"wrongOrder", func(w *scenePresetWire) { w.vOrder = 4 }},
		{"unbounded", func(w *scenePresetWire) { w.bend = 100 }},
		{"fold", func(w *scenePresetWire) { w.width, w.height, w.bend = 96, 10, -68.8 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			unsupported, guarded := sourceArtboardOpen(t, scenePresetFixture(t, image.Point{}, 25, test.change))
			for _, channel := range unsupported.Layers[0].Channels {
				guarded.forbidden = append(guarded.forbidden, channel.Data)
			}
			result, err := Render(t.Context(), unsupported, Options{ApproximateCustomWarp: true})
			if !cachedUnsupported(err) || result != nil || guarded.reads != 0 {
				t.Fatalf("unsupported preset schema used replacement pixels: %v", err)
			}
		})
	}
	data := scenePresetFixture(t, image.Point{}, 25, nil)
	quad := []float64{0, 0, 54, 0, 56, 36, 0, 36}
	data = placedFXReplaceBlock(t, data, "SoLd", scenePresetPlacementWire("recursive-text", 18, 12, 25, quad, nil), 0)
	projective, guarded := sourceArtboardOpen(t, data)
	for _, channel := range projective.Layers[0].Channels {
		guarded.forbidden = append(guarded.forbidden, channel.Data)
	}
	if result, err := Render(t.Context(), projective,
		Options{ApproximateCustomWarp: true}); !cachedUnsupported(err) || result != nil || guarded.reads != 0 {
		t.Fatalf("unverified projective preset entered the source renderer: %v", err)
	}
	if reader.reads != 0 {
		t.Fatal("preset canceled/limited operation read saved caches")
	}
}

func TestPresetSceneRejectsAmbiguousNativeDescriptorsBeforeSourceIO(t *testing.T) {
	field := func(key string, value float64) artboardTestEntry {
		return artboardTestEntry{key, "doub", artboardTestDouble(value)}
	}
	var cases []struct {
		name   string
		change func(*scenePresetWire)
	}
	for index, key := range []string{"Wdth", "Hght"} {
		for _, conflicting := range []bool{false, true} {
			cases = append(cases, struct {
				name   string
				change func(*scenePresetWire)
			}{fmt.Sprintf("size_%s_conflicting%t", key, conflicting), func(w *scenePresetWire) {
				w.sizeFields = []artboardTestEntry{field("Wdth", 18), field("Hght", 12)}
				correct := []float64{18, 12}[index]
				if conflicting {
					w.sizeFields[index] = field(key, correct+1)
				}
				// Last-wins typed dimensions remain valid, so raw schema validation must reject the ambiguity.
				w.sizeFields = append(w.sizeFields, field(key, correct))
			}})
		}
	}
	for index, key := range []string{"Left", "Top ", "Rght", "Btom"} {
		for _, conflicting := range []bool{false, true} {
			cases = append(cases, struct {
				name   string
				change func(*scenePresetWire)
			}{fmt.Sprintf("bounds_%s_conflicting%t", key, conflicting), func(w *scenePresetWire) {
				w.boundsFields = []artboardTestEntry{field("Left", 0), field("Top ", 0),
					field("Rght", 18), field("Btom", 12)}
				correct := []float64{0, 0, 18, 12}[index]
				if conflicting {
					w.boundsFields[index] = field(key, correct+1)
				}
				w.boundsFields = append(w.boundsFields, field(key, correct))
			}})
		}
		cases = append(cases, struct {
			name   string
			change func(*scenePresetWire)
		}{"bounds_missing_" + key, func(w *scenePresetWire) {
			all := []artboardTestEntry{field("Left", 0), field("Top ", 0), field("Rght", 18), field("Btom", 12)}
			w.boundsFields = append(all[:index:index], all[index+1:]...)
		}})
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data := scenePresetFixture(t, image.Point{}, 25, test.change)
			before := sha256.Sum256(data)
			source, reader := sourceArtboardOpen(t, data)
			for _, channel := range source.Layers[0].Channels {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
			links, err := source.LinkedFiles(t.Context())
			if err != nil || len(links) != 1 {
				t.Fatalf("raw schema source fixture is absent: %v", err)
			}
			reader.forbidden = append(reader.forbidden, links[0].Data)
			settings := Options{ApproximateCustomWarp: true}
			if pixels, err := Render(t.Context(), source, settings); pixels != nil || !cachedUnsupported(err) {
				t.Fatalf("ambiguous raw preset descriptor reached full source pixels: %v", err)
			}
			if pixels, err := RenderTile(t.Context(), source, image.Rect(3, 5, 50, 39),
				TileOptions{ApproximateCustomWarp: true}); pixels != nil || !cachedUnsupported(err) {
				t.Fatalf("ambiguous raw preset descriptor reached regional source pixels: %v", err)
			}
			if layer, err := RenderPlacedLayer(t.Context(), source, 0, settings); layer != nil || !cachedUnsupported(err) {
				t.Fatalf("ambiguous raw preset descriptor reached placed artwork: %v", err)
			}
			if reader.reads != 0 || sha256.Sum256(data) != before {
				t.Fatal("ambiguous raw descriptor read original artwork/caches or mutated source before rejection")
			}
		})
	}
}

func TestPresetSceneEmptyRegionKeepsEmptyProjectionWithoutSourceIO(t *testing.T) {
	quad := []float64{0, 0, 10_000, 0, 10_000, 10_000, 0, 10_000}
	data := scenePresetFixture(t, image.Point{}, 25, nil)
	data = placedFXReplaceBlock(t, data, "SoLd", scenePresetPlacementWire("recursive-text", 18, 12, 25, quad, nil), 0)
	source, reader := sourceArtboardOpen(t, data)
	for _, channel := range source.Layers[0].Channels {
		reader.forbidden = append(reader.forbidden, channel.Data)
	}
	links, err := source.LinkedFiles(t.Context())
	if err != nil || len(links) != 1 {
		t.Fatalf("empty-region original source fixture is absent: %v", err)
	}
	reader.forbidden = append(reader.forbidden, links[0].Data)
	metadata, err := source.ReadLayerMetadata(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Metadata and certified geometry fit; the unrequested 10000x10000 projected raster cannot fit this budget.
	const budget = 256 << 10
	for _, window := range []image.Rectangle{image.Rect(2, 3, 2, 7), image.Rect(2, 3, 7, 3), image.Rect(2, 3, 2, 3)} {
		t.Run(fmt.Sprint(window), func(t *testing.T) {
			settings := TileOptions{ApproximateCustomWarp: true, MaxWorkingBytes: budget}
			engine := rgbTileRenderer{source: source, settings: settings, window: window,
				working: metadata.MetadataBytes, nodes: map[*psdmodel.Layer]int{metadata.Layer: 0},
				legacy: renderer{options: Options{MaxWorkingBytes: budget}}}
			plan, err := engine.compilePlacedPlan(t.Context(), source.Layers[0], metadata.Layer)
			if err != nil || plan.patch == nil || !plan.window.Empty() || !plan.bounds.Empty() {
				t.Fatalf("empty preset source request became an unbounded complete patch: %v, %+v", err, plan.bounds)
			}
			pixels, err := RenderTile(t.Context(), source, window, settings)
			if err != nil || pixels == nil || pixels.Rect != window || len(pixels.Pix) != 0 {
				t.Fatalf("empty preset region allocated artwork or changed requested coordinates: %v", err)
			}
			if reader.reads != 0 {
				t.Fatal("empty preset projection read unused original contents or saved caches")
			}
		})
	}
}
