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
	"path/filepath"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func smartWarpPresetFixture(
	t *testing.T, version uint16, external bool, modify func(*psdfile.Descriptor), dimensions ...image.Point,
) ([]byte, []byte) {
	t.Helper()
	data, contents := smartWarpRoundtripFixture(t, version, 4, external, false)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	records, err := source.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	descriptor := records[0].Descriptor
	size := image.Pt(2, 2)
	if len(dimensions) != 0 {
		size = dimensions[0]
		pixels := smartDimensionsPixels(size.X, size.Y)
		contents = smartSizedPrototype(version, size, pixels.Rect, pixels)
		for _, entry := range descriptor.Entries {
			if entry.Key == "Sz  " {
				for index := range entry.Value.(*psdfile.Descriptor).Entries {
					edge := &entry.Value.(*psdfile.Descriptor).Entries[index]
					switch edge.Key {
					case "Wdth":
						edge.Value = float64(size.X)
					case "Hght":
						edge.Value = float64(size.Y)
					}
				}
			}
		}
	}
	for _, outer := range descriptor.Entries {
		if outer.Key != "warp" {
			continue
		}
		warp := outer.Value.(*psdfile.Descriptor)
		warp.Entries = slices.DeleteFunc(warp.Entries, func(entry psdfile.DescriptorEntry) bool {
			return entry.Key == "customEnvelopeWarp"
		})
		for index := range warp.Entries {
			entry := &warp.Entries[index]
			switch entry.Key {
			case "warpStyle":
				entry.Value = psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpArcLower"}
			case "vOrder":
				entry.Value = int32(2)
			case "bounds":
				for index := range entry.Value.(*psdfile.Descriptor).Entries {
					edge := &entry.Value.(*psdfile.Descriptor).Entries[index]
					switch edge.Key {
					case "Rght":
						edge.Value = float64(size.X)
					case "Btom":
						edge.Value = float64(size.Y)
					}
				}
			}
		}
		if modify != nil {
			modify(warp)
		}
	}
	encoded, err := source.EncodeDescriptor(t.Context(), descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	payload = append(payload, 0xa1, 0xb2, 0xc3)
	kind := "liFD"
	if external {
		kind = "liFE"
	}
	linked := writerLinkedEntry(kind, "fixture-id", 7, contents)
	lengthWidth := 4
	if version == 2 {
		lengthWidth = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", lengthWidth, linked.data, 4)
	tags := writerLinkedTag("8BIM", "SoLd", 4, payload, 2)
	poison := smartEditImage([4]color.NRGBA{
		{250, 0, 250, 255}, {250, 0, 250, 255}, {250, 0, 250, 255}, {250, 0, 250, 255},
	})
	record, channels := smartEditLayer(version, "Preset target", 11, image.Rect(2, 2, 4, 4), poison, tags, true)
	peerTags := writerLinkedTag("8BIM", "SoLE", 4, smartEditPlacement(t, 18, ""), 2)
	peer, peerChannels := smartEditLayer(version, "Unchanged peer", 12,
		image.Rect(18, 0, 20, 2), smartEditImage(smartEditColors()), peerTags, false)
	layers := binary.BigEndian.AppendUint16(nil, 0xfffe)
	layers = append(layers, record...)
	layers = append(layers, peer...)
	layers = append(layers, channels...)
	layers = append(layers, peerChannels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	preview := append([]byte{0, 0}, bytes.Repeat([]byte{71}, 24*16*4)...)
	return smartEditDocument(version, 24, 16, 4, layers, global, preview), contents
}

func smartWarpPresetExpected(
	t *testing.T, placed *psdmodel.Placed,
) (*image.NRGBA, image.Point, *image.NRGBA) {
	t.Helper()
	geometry, err := psdwarp.CompilePresetGeometry(t.Context(), placed)
	if err != nil {
		t.Fatal(err)
	}
	raw, origin, err := geometry.ProjectMathematicalWithOptions(t.Context(),
		smartEditImage(smartEditColors()), image.Rect(0, 0, 24, 16))
	if err != nil {
		t.Fatal(err)
	}
	// A two-pixel source has one lattice cell. Native arc endpoints lie at the source rectangle corners.
	// Only the positive bend expands its control hull; independently invert that affine endpoint rectangle.
	handleY := (4.0 / 3) * math.Tan(math.Pi*placed.Warp.Value/400)
	hullHeight := 2 + math.Max(handleY, 0)
	colors := smartEditColors()
	compared := 0
	merged := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for row := range raw.Rect.Dy() {
		for column := range raw.Rect.Dx() {
			position := origin.Add(image.Pt(column, row))
			pixel := raw.NRGBAAt(column, row)
			u := (float64(position.X) + .5 - 2) / 10
			v := (float64(position.Y) + .5 - 2) * hullHeight / 20
			x, y := 2*u-.5, 2*v-.5
			if x >= 0 && x <= 1 && y >= 0 && y <= 1 {
				weights := [4]float64{(1 - x) * (1 - y), x * (1 - y), (1 - x) * y, x * y}
				var expected [4]float64
				for index, value := range colors {
					for component, sample := range [4]byte{value.R, value.G, value.B, value.A} {
						expected[component] += float64(sample) * weights[index]
					}
				}
				for component, sample := range [4]byte{pixel.R, pixel.G, pixel.B, pixel.A} {
					if math.Abs(float64(sample)-expected[component]) > .50000001 {
						t.Fatalf("preset sample %v/%d = %d, independent native-endpoint color = %g",
							position, component, sample, expected[component])
					}
				}
				compared++
			}
			mask := byte(128)
			if position.In(image.Rect(2, 2, 4, 4)) {
				mask = []byte{255, 128, 0, 255}[(position.Y-2)*2+position.X-2]
			}
			pixel.A = byte(math.RoundToEven(float64(pixel.A) * float64(mask) * 160 * 128 / (255 * 255 * 255)))
			if pixel.A == 0 {
				pixel = color.NRGBA{}
			}
			merged.SetNRGBA(position.X, position.Y, pixel)
		}
	}
	if compared < 12 {
		t.Fatalf("preset oracle checked too few opaque source-interior pixels: %d", compared)
	}
	for y := range 2 {
		for x := range 2 {
			merged.SetNRGBA(18+x, y, colors[y*2+x])
		}
	}
	return raw, origin, merged
}

func smartWarpPresetPreserved(t *testing.T, before, after *psdfile.Document, value float64) {
	t.Helper()
	oldRecords, err := before.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	newRecords, err := after.SmartObjects(t.Context())
	if err != nil || len(oldRecords) != 2 || len(newRecords) != 2 {
		t.Fatalf("preset save lost original placements: %v", err)
	}
	oldPayload, err := before.ReadSpan(oldRecords[0].Block.Data)
	if err != nil {
		t.Fatal(err)
	}
	expected := bytes.Clone(oldPayload)
	for _, outer := range oldRecords[0].Descriptor.Entries {
		if outer.Key != "warp" {
			continue
		}
		for _, entry := range outer.Value.(*psdfile.Descriptor).Entries {
			if entry.Key == "warpValue" {
				binary.BigEndian.PutUint64(expected[entry.Data.Offset-oldRecords[0].Block.Data.Offset:],
					math.Float64bits(value))
			}
		}
	}
	actual, err := after.ReadSpan(newRecords[0].Block.Data)
	if err != nil || !bytes.Equal(actual, expected) {
		t.Fatalf("preset changed bytes outside the existing bend, including schema, unknown values or suffix: %v", err)
	}
	oldPeer, err := before.ReadSpan(oldRecords[1].Block.Data)
	if err != nil {
		t.Fatal(err)
	}
	newPeer, err := after.ReadSpan(newRecords[1].Block.Data)
	if err != nil || !bytes.Equal(oldPeer, newPeer) {
		t.Fatalf("preset edit changed a shared peer placement: %v", err)
	}
	smartWarpGridLinkedPreserved(t, before, after)
}

type smartWarpPresetCacheGuard struct {
	data      []byte
	forbidden []psdfile.Span
	hits      int
}

func (reader *smartWarpPresetCacheGuard) ReadAt(target []byte, offset int64) (int, error) {
	for _, span := range reader.forbidden {
		if len(target) != 0 && offset < span.Offset+span.Length && offset+int64(len(target)) > span.Offset {
			reader.hits++
			return 0, errors.New("preset live render accessed a saved parent pixel cache")
		}
	}
	return bytes.NewReader(reader.data).ReadAt(target, offset)
}

func smartWarpPresetLiveSources(
	t *testing.T, saved *psdfile.Document, settings psdscene.Options, expected *image.NRGBA, external bool,
) {
	t.Helper()
	data, err := saved.ReadSpan(saved.Source)
	if err != nil {
		t.Fatal(err)
	}
	reader := &smartWarpPresetCacheGuard{data: data}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	reader.forbidden = []psdfile.Span{source.Composite.Data}
	for _, layer := range source.Layers {
		for _, channel := range layer.Channels {
			if channel.ID >= -1 {
				reader.forbidden = append(reader.forbidden, channel.Data)
			}
		}
	}
	compare := func(pixels *image.NRGBA) error {
		if pixels == nil {
			return errors.New("preset source render returned no pixels")
		}
		for y := pixels.Rect.Min.Y; y < pixels.Rect.Max.Y; y++ {
			for x := pixels.Rect.Min.X; x < pixels.Rect.Max.X; x++ {
				actual, wanted := pixels.NRGBAAt(x, y), expected.NRGBAAt(x, y)
				if actual.A != wanted.A || actual.A != 0 && actual != wanted {
					return fmt.Errorf("preset source sample %d,%d = %v, independent edited preview = %v",
						x, y, actual, wanted)
				}
			}
		}
		return nil
	}
	withoutApproximation := settings
	withoutApproximation.ApproximateCustomWarp = false
	if pixels, err := psdscene.Render(t.Context(), saved, withoutApproximation); pixels != nil ||
		!errors.Is(err, psdfile.ErrUnsupported) && !errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("reopened preset source inferred approximation from an earlier edit: %v", err)
	}
	region := image.Rect(1, 1, 16, 14)
	if pixels, err := psdscene.RenderTile(t.Context(), saved, region); pixels != nil ||
		!errors.Is(err, psdfile.ErrUnsupported) && !errors.Is(err, psdwarp.ErrUnsupported) {
		t.Fatalf("reopened regional preset source enabled approximation implicitly: %v", err)
	}
	visited := false
	err = psdscene.VisitTiles(t.Context(), saved, expected.Rect, image.Pt(7, 5), func(*image.NRGBA) error {
		visited = true
		return nil
	})
	if !errors.Is(err, psdfile.ErrUnsupported) && !errors.Is(err, psdwarp.ErrUnsupported) || visited {
		t.Fatalf("reopened preset visitor published implicitly approximate pixels: %v", err)
	}
	if external {
		missingMapping := psdscene.Options{ApproximateCustomWarp: true}
		if pixels, err := psdscene.Render(t.Context(), source, missingMapping); pixels != nil ||
			!errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("fresh preset source render inherited an earlier external mapping: %v", err)
		}
		missingTileMapping := psdscene.TileOptions{ApproximateCustomWarp: true}
		if pixels, err := psdscene.RenderTile(t.Context(), source, region, missingTileMapping); pixels != nil ||
			!errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("fresh regional preset source inherited an earlier external mapping: %v", err)
		}
		visited = false
		err = psdscene.VisitTiles(t.Context(), source, expected.Rect, image.Pt(7, 5), func(*image.NRGBA) error {
			visited = true
			return nil
		}, missingTileMapping)
		if !errors.Is(err, psdfile.ErrUnsupported) || visited {
			t.Fatalf("fresh preset visitor published pixels without an explicit external mapping: %v", err)
		}
	}
	full, err := psdscene.Render(t.Context(), source, settings)
	if err != nil {
		t.Fatalf("reopened explicit preset source render: %v", err)
	}
	if full.Rect != expected.Rect {
		t.Fatalf("full preset source changed the document canvas: %v", full.Rect)
	}
	if err := compare(full); err != nil {
		t.Fatal(err)
	}
	tileSettings := psdscene.TileOptions{ApproximateCustomWarp: true,
		ExternalResolver: settings.ExternalResolver, ExternalRasterResolver: settings.ExternalRasterResolver}
	tile, err := psdscene.RenderTile(t.Context(), source, region, tileSettings)
	if err != nil || tile == nil || tile.Rect != region {
		t.Fatalf("reopened explicit preset regional source render: %v", err)
	}
	if err := compare(tile); err != nil {
		t.Fatal(err)
	}
	visits := 0
	err = psdscene.VisitTiles(t.Context(), source, expected.Rect, image.Pt(7, 5), func(tile *image.NRGBA) error {
		visits++
		return compare(tile)
	}, tileSettings)
	if err != nil || visits != 16 {
		t.Fatalf("reopened preset tile visitor changed regional phase or traversal: %d / %v", visits, err)
	}
	if reader.hits != 0 {
		t.Fatalf("preset source painting read old parent caches: %d", reader.hits)
	}
}

func TestSmartWarpPresetEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, external := range []bool{false, true} {
				name := fmt.Sprintf("v%d/c%d/external%t", version, compression, external)
				t.Run(name, func(t *testing.T) {
					data, contents := smartWarpPresetFixture(t, version, external, nil)
					digest := sha256.Sum256(data)
					editor, original := smartEditOpen(t, data)
					if err := editor.SetOpacity(1, 128); err != nil {
						t.Fatal(err)
					}
					if err := editor.SetFillOpacity(1, 160); err != nil {
						t.Fatal(err)
					}
					mask, err := editor.PixelMask(t.Context(), 1)
					if err != nil {
						t.Fatal(err)
					}
					mask.DefaultColor = 128
					if err := editor.SetPixelMask(t.Context(), 1, mask); err != nil {
						t.Fatal(err)
					}
					settings := &psdscene.Options{ApproximateCustomWarp: true}
					calls := 0
					if external {
						settings.ExternalResolver = func(
							_ context.Context, record psdfile.LinkedFileRecord,
						) (*psdfile.Document, error) {
							calls++
							if record.Kind != "liFE" {
								t.Fatal("preset edit inferred a different linked source kind")
							}
							resolved, err := psdfile.Parse(contents)
							if err == nil {
								t.Cleanup(func() { _ = resolved.Close() })
							}
							return resolved, err
						}
					}
					var prior []byte
					for round, value := range []float64{25, 50} {
						unchanged := smartContentsFilterState(t, editor)
						if err := editor.SetSmartObjectWarpPreset(t.Context(), 1,
							SmartWarpPresetParameters{Value: value}, SmartWarpEditOptions{}); !errors.Is(err, ErrUnsupported) {
							t.Fatalf("preset edit inferred approximation or external mapping after reopening: %v", err)
						}
						unchanged()
						if external {
							err := editor.SetSmartObjectWarpPreset(t.Context(), 1, SmartWarpPresetParameters{Value: value},
								SmartWarpEditOptions{Render: &psdscene.Options{ApproximateCustomWarp: true}})
							if !errors.Is(err, ErrUnsupported) {
								t.Fatalf("preset edit inherited a prior external mapping: %v", err)
							}
							unchanged()
						}
						view, err := editor.source.ReadLayerMetadata(t.Context(), 0)
						if err != nil {
							t.Fatal(err)
						}
						placed := view.Layer.Placed
						placed.Warp.Value = value
						raw, origin, expected := smartWarpPresetExpected(t, placed)
						beforeCalls := calls
						if err := editor.SetSmartObjectWarpPreset(t.Context(), 1, SmartWarpPresetParameters{Value: value},
							SmartWarpEditOptions{Render: settings}); err != nil {
							t.Fatalf("preset edit round %d: %v", round, err)
						}
						if external && calls != beforeCalls+1 {
							t.Fatalf("preset edit did not resolve its shared source once: %d", calls-beforeCalls)
						}
						actual, _ := smartWarpOracleCache(t, editor)
						if !bytes.Equal(actual.Pix, raw.Pix) || prior != nil && bytes.Equal(prior, actual.Pix) {
							t.Fatal("preset parameter edit kept stale pixels or baked parent styling")
						}
						prior = bytes.Clone(actual.Pix)
						filename := filepath.Join(t.TempDir(), "edited-preset.psd")
						beforeCalls = calls
						if err := editor.Save(t.Context(), filename, SaveOptions{Compression: compression}); err != nil {
							t.Fatalf("preset automatic save round %d: %v", round, err)
						}
						if calls != beforeCalls {
							t.Fatal("automatic preset save reused an external mapping from an earlier edit")
						}
						saved, err := psdfile.Open(filename)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = saved.Close() })
						if saved.Header.Version != version {
							t.Fatal("preset save changed PSD/PSB format")
						}
						smartWarpRoundtripRaw(t, saved, raw, origin)
						placedEffectsRoundtripPreview(t, saved, expected)
						smartWarpPresetPreserved(t, original, saved, value)
						metadata, err := saved.ReadLayerMetadata(t.Context(), 0)
						if err != nil || metadata.Layer.Placed.Warp.Value != value ||
							metadata.Layer.Placed.Warp.Style != "warpArcLower" || metadata.Layer.Placed.Warp.HasEnvelope {
							t.Fatalf("preset parameters were reverted or converted to a custom mesh: %v", err)
						}
						smartWarpPresetLiveSources(t, saved, *settings, expected, external)
						editor = lifecycleReopen(t, saved)
					}
					if sha256.Sum256(data) != digest {
						t.Fatal("preset edit changed caller-owned source bytes")
					}
				})
			}
		}
	}
}

func TestSmartWarpPresetExternalCallbackOwnershipAndAtomicity(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "error", "cancel", "stale", "closed", "policy-mutation"} {
		t.Run(scenario, func(t *testing.T) {
			data, contents := smartWarpPresetFixture(t, 1, true, nil)
			editor, source := smartEditOpen(t, data)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("explicit preset source failed")
			settings := &psdscene.Options{ApproximateCustomWarp: true}
			calls := 0
			if scenario != "missing" {
				settings.ExternalResolver = func(
					callbackContext context.Context, _ psdfile.LinkedFileRecord,
				) (*psdfile.Document, error) {
					calls++
					if _, err := editor.Layers(); err != nil {
						return nil, err
					}
					switch scenario {
					case "error":
						return nil, failure
					case "cancel":
						cancel()
					case "stale":
						if err := editor.Rename(1, "preserved concurrent rename"); err != nil {
							return nil, err
						}
					case "closed":
						if err := source.Close(); err != nil {
							return nil, err
						}
					case "policy-mutation":
						settings.ApproximateCustomWarp = false
						settings.MaxWorkingBytes = 1
						settings.CachedSpecialLayers = true
					}
					if err := callbackContext.Err(); err != nil {
						return nil, err
					}
					resolved, err := psdfile.Parse(contents)
					if err == nil {
						t.Cleanup(func() { _ = resolved.Close() })
					}
					return resolved, err
				}
			}
			revision := editor.revision
			err := editor.SetSmartObjectWarpPreset(ctx, 1, SmartWarpPresetParameters{Value: 25},
				SmartWarpEditOptions{Render: settings})
			node, _, _, _ := editor.findNodeLocked(1)
			if scenario == "success" || scenario == "policy-mutation" {
				if err != nil || calls != 1 || editor.revision != revision+1 || node.placed == nil ||
					node.placed.Warp.Value != 25 || node.pixels == nil {
					t.Fatalf("explicit preset resolver lost policy ownership or reentered the editor lock: %v", err)
				}
				return
			}
			wanted := ErrUnsupported
			switch scenario {
			case "error":
				wanted = failure
			case "cancel":
				wanted = context.Canceled
			case "stale":
				wanted = ErrStaleLinkedEdit
			case "closed":
				wanted = psdfile.ErrClosed
			}
			if !errors.Is(err, wanted) {
				t.Fatalf("external preset failure = %v, want %v", err, wanted)
			}
			if node.pixels != nil || node.placed != nil || len(node.additional) != 0 {
				t.Fatal("failed preset edit committed pixels or placement metadata")
			}
			if scenario != "stale" && editor.revision != revision {
				t.Fatal("failed preset edit changed the document revision")
			}
		})
	}
}

func TestSmartWarpPresetInvalidControlsAndPoliciesAreAtomic(t *testing.T) {
	for _, scenario := range []string{
		"nan", "infinite", "positive-boundary", "negative-boundary", "huge", "fold", "cancel",
		"no-policy-zero", "no-policy-curved", "working-budget", "document-budget", "output-profile",
		"cached-special", "cached-text",
	} {
		t.Run(scenario, func(t *testing.T) {
			data, _ := smartWarpPresetFixture(t, 1, false, nil)
			if scenario == "fold" {
				data, _ = smartWarpPresetFixture(t, 1, false, nil, image.Pt(8, 2))
			}
			editor, _ := smartEditOpen(t, data)
			settings := &psdscene.Options{ApproximateCustomWarp: true}
			if err := editor.SetSmartObjectWarpPreset(t.Context(), 1, SmartWarpPresetParameters{Value: 25},
				SmartWarpEditOptions{Render: settings}); err != nil {
				t.Fatal(err)
			}
			var prior bytes.Buffer
			if err := editor.Write(t.Context(), &prior); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			value := 50.0
			wanted := ErrInvalidEdit
			switch scenario {
			case "nan":
				value = math.NaN()
			case "infinite":
				value = math.Inf(1)
			case "positive-boundary":
				value = 100
			case "negative-boundary":
				value = -100
			case "huge":
				value = 1e20
			case "fold":
				value = -99
			case "cancel":
				cancel()
				wanted = context.Canceled
			case "no-policy-zero":
				value, settings = 0, nil
				wanted = ErrUnsupported
			case "no-policy-curved":
				settings = &psdscene.Options{}
				wanted = ErrUnsupported
			case "working-budget":
				settings.MaxWorkingBytes = 1
				wanted = psdfile.ErrLimit
			case "document-budget":
				settings.MaxDocuments = 1
				wanted = psdfile.ErrLimit
			case "output-profile":
				settings.OutputProfile = []byte{}
				wanted = ErrUnsupported
			case "cached-special":
				settings.CachedSpecialLayers = true
				wanted = ErrUnsupported
			case "cached-text":
				settings.CachedTextLayers = true
				wanted = ErrUnsupported
			}
			unchanged := smartContentsFilterState(t, editor)
			err := editor.SetSmartObjectWarpPreset(ctx, 1, SmartWarpPresetParameters{Value: value},
				SmartWarpEditOptions{Render: settings})
			if scenario == "fold" {
				if !errors.Is(err, ErrInvalidEdit) && !errors.Is(err, ErrUnsupported) {
					t.Fatalf("folded preset bend was accepted: %v", err)
				}
			} else if !errors.Is(err, wanted) {
				t.Fatalf("preset policy/value failure = %v, want %v", err, wanted)
			}
			unchanged()
			var after bytes.Buffer
			if err := editor.Write(t.Context(), &after); err != nil || !bytes.Equal(prior.Bytes(), after.Bytes()) {
				t.Fatalf("rejected preset parameters changed earlier artwork or saved bytes: %v", err)
			}
		})
	}
}

func TestSmartWarpPresetUnsupportedSchemasAreAtomic(t *testing.T) {
	for _, scenario := range []string{
		"other-style", "vertical", "u-order", "v-order", "perspective", "perspective-other",
		"missing-value", "duplicate-value", "unit-value", "old-value-nan", "old-value-limit",
		"duplicate-left-same", "duplicate-left-conflict", "duplicate-top", "missing-left", "projective",
	} {
		t.Run(scenario, func(t *testing.T) {
			data, _ := smartWarpPresetFixture(t, 1, false, func(warp *psdfile.Descriptor) {
				if scenario == "missing-value" {
					warp.Entries = slices.DeleteFunc(warp.Entries, func(entry psdfile.DescriptorEntry) bool {
						return entry.Key == "warpValue"
					})
				}
				for index := range warp.Entries {
					entry := &warp.Entries[index]
					switch {
					case scenario == "other-style" && entry.Key == "warpStyle":
						entry.Value = psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpCylinder"}
					case scenario == "vertical" && entry.Key == "warpRotate":
						entry.Value = psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Vrtc"}
					case scenario == "u-order" && entry.Key == "uOrder":
						entry.Value = int32(3)
					case scenario == "v-order" && entry.Key == "vOrder":
						entry.Value = int32(4)
					case scenario == "perspective" && entry.Key == "warpPerspective":
						entry.Value = float64(1)
					case scenario == "perspective-other" && entry.Key == "warpPerspectiveOther":
						entry.Value = float64(1)
					case scenario == "unit-value" && entry.Key == "warpValue":
						entry.Type, entry.Value = "UntF", psdfile.DescriptorUnit{Unit: "#Prc", Number: float64(0)}
					case scenario == "old-value-nan" && entry.Key == "warpValue":
						entry.Value = math.NaN()
					case scenario == "old-value-limit" && entry.Key == "warpValue":
						entry.Value = float64(100)
					case entry.Key == "bounds":
						bounds := entry.Value.(*psdfile.Descriptor)
						switch scenario {
						case "duplicate-left-same":
							bounds.Entries = append(bounds.Entries,
								psdfile.DescriptorEntry{Key: "Left", Type: "doub", Value: float64(0)})
						case "duplicate-left-conflict":
							bounds.Entries = append(bounds.Entries,
								psdfile.DescriptorEntry{Key: "Left", Type: "doub", Value: float64(1)})
						case "duplicate-top":
							bounds.Entries = append(bounds.Entries,
								psdfile.DescriptorEntry{Key: "Top ", Type: "doub", Value: float64(0)})
						case "missing-left":
							bounds.Entries = slices.DeleteFunc(bounds.Entries,
								func(edge psdfile.DescriptorEntry) bool { return edge.Key == "Left" })
						}
					}
				}
				if scenario == "duplicate-value" {
					warp.Entries = append(warp.Entries,
						psdfile.DescriptorEntry{Key: "warpValue", Type: "doub", Value: float64(0)})
				}
			})
			if scenario == "projective" {
				data = smartWarpGridPatchFixture(t, data, func(entry *psdfile.DescriptorEntry, owned []byte) {
					if entry.Key == "nonAffineTransform" {
						values := entry.Value.([]psdfile.DescriptorValue)
						binary.BigEndian.PutUint64(owned[values[6].Data.Offset:], math.Float64bits(2.5))
					}
				})
			}
			editor, _ := smartEditOpen(t, data)
			unchanged := smartContentsFilterState(t, editor)
			err := editor.SetSmartObjectWarpPreset(t.Context(), 1, SmartWarpPresetParameters{Value: 25},
				SmartWarpEditOptions{Render: &psdscene.Options{ApproximateCustomWarp: true}})
			if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrInvalidEdit) {
				t.Fatalf("unsupported preset schema was accepted or misclassified: %v", err)
			}
			unchanged()
			var output bytes.Buffer
			if err := editor.WritePSD(t.Context(), &output); err != nil || !bytes.Equal(output.Bytes(), data) {
				t.Fatalf("unsupported preset changed untouched original bytes: %v", err)
			}
		})
	}
}

func TestSmartWarpPresetContentsReplacementIsAtomicUnsupported(t *testing.T) {
	for _, bends := range [][2]float64{{0, 25}, {25, 0}} {
		t.Run(fmt.Sprintf("original%g/current%g", bends[0], bends[1]), func(t *testing.T) {
			data, _ := smartWarpPresetFixture(t, 1, false, func(warp *psdfile.Descriptor) {
				for index := range warp.Entries {
					if warp.Entries[index].Key == "warpValue" {
						warp.Entries[index].Value = bends[0]
					}
				}
			})
			editor, _ := smartEditOpen(t, data)
			if err := editor.SetSmartObjectWarpPreset(t.Context(), 1, SmartWarpPresetParameters{Value: bends[1]},
				SmartWarpEditOptions{Render: &psdscene.Options{ApproximateCustomWarp: true}}); err != nil {
				t.Fatal(err)
			}
			var prior bytes.Buffer
			if err := editor.Write(t.Context(), &prior); err != nil {
				t.Fatal(err)
			}
			colors := smartEditColors()
			colors[0].R += 15
			contents := smartEditPrototype(1, smartEditImage(colors))
			for _, explicit := range []bool{false, true} {
				unchanged := smartContentsFilterState(t, editor)
				err := editor.ReplaceContentsWithOptions(t.Context(), 1, contents,
					SmartContentsOptions{ApproximateCustomWarp: explicit})
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("unverified preset source replacement did not reject atomically: %v", err)
				}
				unchanged()
				var after bytes.Buffer
				if err := editor.Write(t.Context(), &after); err != nil || !bytes.Equal(prior.Bytes(), after.Bytes()) {
					t.Fatalf("failed source replacement reset the current preset bend, source or artwork: %v", err)
				}
			}
			editor, saved := smartWarpOracleSaved(t, editor, 3)
			view, err := saved.ReadLayerMetadata(t.Context(), 0)
			if err != nil || view.Layer.Placed.Warp.Value != bends[1] {
				t.Fatalf("reopen lost current preset after rejected source replacement: %v", err)
			}
			if err := editor.SetSmartObjectWarpPreset(t.Context(), 1, SmartWarpPresetParameters{Value: 50},
				SmartWarpEditOptions{Render: &psdscene.Options{ApproximateCustomWarp: true}}); err != nil {
				t.Fatalf("rejected source replacement prevented continued preset editing: %v", err)
			}
		})
	}
}

func TestSmartWarpPresetDuplicateSourceSizeIsRejectedBeforePatching(t *testing.T) {
	for _, scenario := range []string{"same-width", "conflicting-height", "missing-width"} {
		t.Run(scenario, func(t *testing.T) {
			data, _ := smartWarpPresetFixture(t, 1, false, nil)
			digest := sha256.Sum256(data)
			source, err := psdfile.Parse(data)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = source.Close() }()
			records, err := source.SmartObjects(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range records[0].Descriptor.Entries {
				if entry.Key != "Sz  " {
					continue
				}
				size := entry.Value.(*psdfile.Descriptor)
				switch scenario {
				case "same-width":
					size.Entries = append(size.Entries,
						psdfile.DescriptorEntry{Key: "Wdth", Type: "doub", Value: float64(2)})
				case "conflicting-height":
					size.Entries = append(size.Entries,
						psdfile.DescriptorEntry{Key: "Hght", Type: "doub", Value: float64(1)})
				case "missing-width":
					size.Entries = slices.DeleteFunc(size.Entries,
						func(edge psdfile.DescriptorEntry) bool { return edge.Key == "Wdth" })
				}
			}
			span := records[0].Block.Data
			payload := data[span.Offset : span.Offset+span.Length]
			err = patchSmartWarpPreset(records[0].Descriptor, payload, span, SmartWarpPresetParameters{Value: 25})
			if !errors.Is(err, ErrUnsupported) || sha256.Sum256(data) != digest {
				t.Fatalf("ambiguous source dimensions reached scalar patching or changed retained bytes: %v", err)
			}
		})
	}
}
