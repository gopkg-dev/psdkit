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
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdrender"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func smartTransformCorners(left, top, width, height float64) [4]psdmodel.Point {
	return [4]psdmodel.Point{{X: left, Y: top}, {X: left + width, Y: top},
		{X: left + width, Y: top + height}, {X: left, Y: top + height}}
}

func smartTransformValues(corners [4]psdmodel.Point) []float64 {
	var values []float64
	for _, point := range corners {
		values = append(values, point.X, point.Y)
	}
	return values
}

func smartTransformRoundTrip(t *testing.T, editor *Editor, compression uint16) *psdfile.Document {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transform.psd")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := editor.Write(t.Context(), file, SaveOptions{Compression: compression})
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("save transformed document: %v / %v", writeErr, closeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	return source
}

func TestSmartTransformSingleInstancePSDPSBCompressionAndPreservedContents(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("%d-%d", version, compression), func(t *testing.T) {
				original := smartEditFixture(t, version, "legacy")
				digest := sha256.Sum256(original)
				editor, source := smartEditOpen(t, original)
				infos, _ := editor.SmartObjects(nil)
				before, _ := source.SmartObjects(nil)
				contents, err := editor.ReadSmartObjectContents(nil, infos[0].ID, nil)
				if err != nil {
					t.Fatal(err)
				}
				corners := smartTransformCorners(1, 1, 2, 2)
				if err := editor.SetSmartObjectTransform(nil, infos[0].ID, corners); err != nil {
					t.Fatal(err)
				}
				node, _, _, _ := editor.findNodeLocked(infos[0].ID)
				if node.bounds != image.Rect(1, 1, 3, 2) || node.pixels.NRGBAAt(0, 0) != (color.NRGBA{220, 60, 40, 255}) {
					t.Fatalf("translated source artwork has wrong bounds or pixels: %v", node.bounds)
				}
				revision, owned := editor.revision, editor.ownedBytesLocked(nil)
				if err := editor.SetSmartObjectTransform(nil, infos[0].ID, corners); err != nil ||
					editor.revision != revision || editor.ownedBytesLocked(nil) != owned {
					t.Fatalf("identical transform mutated overlays: %v", err)
				}
				saved := smartTransformRoundTrip(t, editor, compression)
				after, err := saved.SmartObjects(nil)
				if err != nil || len(after) != 2 || !slices.Equal(after[0].Transform, smartTransformValues(corners)) ||
					!slices.Equal(after[0].NonAffine, smartTransformValues(corners)) ||
					!slices.Equal(after[1].Transform, before[1].Transform) {
					t.Fatalf("saved single-instance transform changed shared peer: %v", err)
				}
				linked, _ := saved.LinkedFiles(nil)
				encoded, _ := saved.ReadSpan(linked[0].Data)
				if !bytes.Equal(encoded, contents) || len(editor.linkedOverlays) != 0 || sha256.Sum256(original) != digest {
					t.Fatal("transform changed shared contents or source input")
				}
				for index, record := range before {
					oldPayload, _ := source.ReadSpan(record.Block.Data)
					newPayload, _ := saved.ReadSpan(after[index].Block.Data)
					allowed := make([]bool, len(oldPayload))
					if index == 0 {
						for _, entry := range record.Descriptor.Entries {
							if entry.Key != "Trnf" && entry.Key != "nonAffineTransform" {
								continue
							}
							for _, item := range entry.Value.([]psdfile.DescriptorValue) {
								for offset := item.Data.Offset; offset < item.Data.Offset+item.Data.Length; offset++ {
									allowed[offset-record.Block.Data.Offset] = true
								}
							}
						}
					}
					if len(oldPayload) != len(newPayload) {
						t.Fatal("transform changed descriptor encoding length")
					}
					for offset := range oldPayload {
						if !allowed[offset] && oldPayload[offset] != newPayload[offset] {
							t.Fatal("transform changed unrelated placement bytes")
						}
					}
				}
				preview, err := editor.Composite(nil)
				if err != nil {
					t.Fatal(err)
				}
				live, err := psdscene.Render(nil, saved)
				if err != nil || !bytes.Equal(preview.Pix, live.Pix) {
					t.Fatalf("saved source render differs from edited preview: %v", err)
				}
			})
		}
	}
}

func TestSmartTransformReplaceDimensionAndTransformChain(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor, _ := smartEditOpen(t, smartEditFixture(t, version, "legacy"))
		infos, _ := editor.SmartObjects(nil)
		corners := smartTransformCorners(1, 0, 3, 3)
		if err := editor.SetSmartObjectTransform(nil, infos[0].ID, corners); err != nil {
			t.Fatal(err)
		}
		for _, dimensions := range []image.Point{{3, 2}, {2, 2}, {4, 3}} {
			pixels := smartDimensionsPixels(dimensions.X, dimensions.Y)
			contents := smartSizedPrototype(version, dimensions, pixels.Rect, pixels)
			if err := editor.ReplaceContents(nil, infos[1].ID, contents); err != nil {
				t.Fatal(err)
			}
			node, _, _, _ := editor.findNodeLocked(infos[0].ID)
			if node.placed == nil || !slices.Equal(node.placed.Transform, smartTransformValues(corners)) ||
				node.placed.Width != dimensions.X || node.placed.Height != dimensions.Y {
				t.Fatal("content replacement reverted the edited transform or current source size")
			}
			corners = smartTransformCorners(2, 1, 3, 2)
			if err := editor.SetSmartObjectTransform(nil, infos[0].ID, corners); err != nil {
				t.Fatal(err)
			}
			saved := smartTransformRoundTrip(t, editor, 1)
			placements, err := saved.SmartObjects(nil)
			if err != nil || placements[0].Width != float64(dimensions.X) ||
				placements[0].Height != float64(dimensions.Y) ||
				!slices.Equal(placements[0].Transform, smartTransformValues(corners)) ||
				!slices.Equal(placements[1].Transform, []float64{4, 0, 6, 0, 6, 2, 4, 2}) {
				t.Fatalf("chain saved stale geometry: %v", err)
			}
		}
	}
}

func TestSmartTransformMasksUseCurrentEditedState(t *testing.T) {
	for _, scenario := range []string{"relative", "relative-cleared", "removed", "added-relative"} {
		t.Run(scenario, func(t *testing.T) {
			editor, _ := smartEditOpen(t, smartEditFixture(t, 1, ""))
			infos, _ := editor.SmartObjects(nil)
			id := infos[1].ID
			settings, err := editor.PixelMask(nil, id)
			if err != nil {
				t.Fatal(err)
			}
			settings.Relative = true
			if err := editor.SetPixelMask(nil, id, settings); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "relative-cleared":
				settings.Relative = false
				if err := editor.SetPixelMask(nil, id, settings); err != nil {
					t.Fatal(err)
				}
			case "removed", "added-relative":
				if err := editor.RemovePixelMask(nil, id); err != nil {
					t.Fatal(err)
				}
				if scenario == "added-relative" {
					plane := psdfile.Plane{Depth: 8, Width: 2, Height: 2, RowBytes: 2, Data: []byte{255, 0, 128, 255}}
					if err := editor.AddPixelMask(nil, id, image.Rect(4, 0, 6, 2), plane, settings); err != nil {
						t.Fatal(err)
					}
				}
			}
			revision := editor.revision
			err = editor.SetSmartObjectTransform(nil, id, smartTransformCorners(3, 1, 2, 2))
			blocked := scenario == "relative" || scenario == "added-relative"
			if blocked && (!errors.Is(err, ErrUnsupported) || editor.revision != revision) || !blocked && err != nil {
				t.Fatalf("current mask state was ignored: %v", err)
			}
		})
	}
}

func TestSmartTransformInvalidCanceledAndUnsupportedLeaveNoOverlays(t *testing.T) {
	for _, scenario := range []string{"nan", "cross", "degenerate", "warp", "filter", "cancel", "external"} {
		t.Run(scenario, func(t *testing.T) {
			editor, _ := smartEditOpen(t, smartEditFixture(t, 1, scenario))
			infos, _ := editor.SmartObjects(nil)
			corners := smartTransformCorners(1, 1, 2, 2)
			switch scenario {
			case "nan":
				corners[0].X = math.NaN()
			case "cross":
				corners[1], corners[2] = corners[2], corners[1]
			case "degenerate":
				corners[0] = corners[1]
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "cancel" {
				cancel()
			}
			revision, owned := editor.revision, editor.ownedBytesLocked(nil)
			err := editor.SetSmartObjectTransform(ctx, infos[1].ID, corners)
			if err == nil || editor.revision != revision || editor.ownedBytesLocked(nil) != owned {
				t.Fatalf("invalid transform partially committed: %v", err)
			}
		})
	}
}

func TestSmartTransformExternalResolverAndStaleEdits(t *testing.T) {
	for _, scenario := range []string{"resolved", "stale", "error", "cancel", "embed", "update"} {
		t.Run(scenario, func(t *testing.T) {
			editor, _ := smartEditOpen(t, smartEditFixture(t, 1, "external"))
			infos, _ := editor.SmartObjects(nil)
			contents := smartEditPrototype(1, smartEditImage(smartEditColors()))
			calls := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("resolver offline")
			resolver := func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
				calls++
				if scenario == "stale" {
					if err := editor.Rename(infos[0].ID, "renamed while resolving"); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "error" {
					return nil, failure
				}
				if scenario == "cancel" {
					cancel()
				}
				return contents, nil
			}
			switch scenario {
			case "embed":
				if err := editor.EmbedLinkedContents(nil, infos[0].ID, resolver); err != nil {
					t.Fatal(err)
				}
			case "update":
				if err := editor.UpdateLinkedContents(nil, infos[0].ID,
					func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
						return LinkedContentsUpdate{Data: contents}, nil
					}); err != nil {
					t.Fatal(err)
				}
			}
			calls = 0
			corners := smartTransformCorners(1, 1, 2, 2)
			err := editor.SetSmartObjectTransformWithOptions(ctx, infos[0].ID, corners, resolver, SmartTransformOptions{})
			want := map[string]error{"stale": ErrStaleLinkedEdit, "error": failure, "cancel": context.Canceled}[scenario]
			if want != nil {
				if !errors.Is(err, want) {
					t.Fatalf("resolver failure = %v, want %v", err, want)
				}
				node, _, _, _ := editor.findNodeLocked(infos[0].ID)
				if node.placed != nil || node.pixels != nil || len(node.additional) != 0 {
					t.Fatal("failed resolver committed transform overlays")
				}
				return
			}
			if err != nil || scenario == "resolved" && calls != 1 || scenario != "resolved" && calls != 0 {
				t.Fatalf("source resolution or existing overlay priority failed: %v, calls %d", err, calls)
			}
			switch scenario {
			case "embed":
				if err := editor.ReplaceContents(nil, infos[1].ID, contents); err != nil {
					t.Fatal(err)
				}
			case "update":
				if err := editor.UpdateLinkedContents(nil, infos[1].ID,
					func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
						return LinkedContentsUpdate{Data: contents}, nil
					}); err != nil {
					t.Fatal(err)
				}
			}
			saved := smartTransformRoundTrip(t, editor, 2)
			placements, _ := saved.SmartObjects(nil)
			if !slices.Equal(placements[0].Transform, smartTransformValues(corners)) {
				t.Fatal("later source update reverted transformed corners")
			}
		})
	}
}

func TestSmartTransformPerspectiveUsesSourcePixelsAndStrictTransparentPolicy(t *testing.T) {
	for _, transparent := range []bool{false, true} {
		editor, _ := smartEditOpen(t, smartEditFixture(t, 1, ""))
		infos, _ := editor.SmartObjects(nil)
		pixels := smartDimensionsPixels(4, 4)
		if transparent {
			for y := range 4 {
				pixels.SetNRGBA(0, y, color.NRGBA{})
			}
		}
		contents := smartSizedPrototype(1, image.Pt(4, 4), pixels.Rect, pixels)
		if err := editor.ReplaceContents(nil, infos[0].ID, contents); err != nil {
			t.Fatal(err)
		}
		corners := [4]psdmodel.Point{{X: .125, Y: 0}, {X: 5.125, Y: 0}, {X: 4.125, Y: 3}, {X: 1.125, Y: 3}}
		err := editor.SetSmartObjectTransform(nil, infos[0].ID, corners)
		if transparent {
			if !errors.Is(err, psdrender.ErrUnsupportedCrop) {
				t.Fatalf("strict transparent projection = %v", err)
			}
			err = editor.SetSmartObjectTransformWithOptions(nil, infos[0].ID, corners, nil,
				SmartTransformOptions{ApproximatePlacedProjection: true})
		}
		if err != nil {
			t.Fatal(err)
		}
		node, _, _, _ := editor.findNodeLocked(infos[0].ID)
		// The trapezoid's inverse is derived directly, independently of the production homography solve.
		for y := range node.pixels.Rect.Dy() {
			for x := range node.pixels.Rect.Dx() {
				dx, dy := float64(x+node.bounds.Min.X)+.375, float64(y+node.bounds.Min.Y)+.5
				u, v := (3*dx-dy)/(15-2*dy), 3*dy/(15-2*dy)
				want := smartTransformReferenceSample(pixels, u, v)
				actual := node.pixels.NRGBAAt(x, y)
				// Equivalent homography solvers can straddle a final half-byte rounding boundary.
				if math.Abs(float64(actual.R)-float64(want.R)) > 1 ||
					math.Abs(float64(actual.G)-float64(want.G)) > 1 ||
					math.Abs(float64(actual.B)-float64(want.B)) > 1 || actual.A != want.A {
					t.Fatalf("independent perspective sample %d,%d = %v, want %v", x, y, actual, want)
				}
			}
		}
	}
}

func smartTransformReferenceSample(pixels *image.NRGBA, u, v float64) color.NRGBA {
	if u < 0 || u > 1 || v < 0 || v > 1 {
		return color.NRGBA{}
	}
	x, y := u*float64(pixels.Rect.Dx())-.5, v*float64(pixels.Rect.Dy())-.5
	left, top := math.Floor(x), math.Floor(y)
	var alpha float64
	var rgb [3]float64
	for row := range 2 {
		for column := range 2 {
			wx, wy := 1-(x-left), 1-(y-top)
			if column == 1 {
				wx = x - left
			}
			if row == 1 {
				wy = y - top
			}
			pixel := pixels.NRGBAAt(max(0, min(pixels.Rect.Dx()-1, int(left)+column)),
				max(0, min(pixels.Rect.Dy()-1, int(top)+row)))
			weight := wx * wy * float64(pixel.A) / 255
			alpha += weight
			for index, value := range []byte{pixel.R, pixel.G, pixel.B} {
				rgb[index] += float64(value) * weight
			}
		}
	}
	if alpha == 0 {
		return color.NRGBA{}
	}
	return color.NRGBA{R: byte(math.RoundToEven(rgb[0] / alpha)), G: byte(math.RoundToEven(rgb[1] / alpha)),
		B: byte(math.RoundToEven(rgb[2] / alpha)), A: byte(math.RoundToEven(alpha * 255))}
}

func TestSmartTransformDuplicateBlocksAndCoordinates(t *testing.T) {
	for _, scenario := range []string{"same-block", "different-block", "same-field", "different-field", "bad-legacy"} {
		t.Run(scenario, func(t *testing.T) {
			pixels := smartEditImage(smartEditColors())
			linked := writerLinkedEntry("liFD", "fixture-id", 7, smartEditPrototype(1, pixels))
			global := writerLinkedTag("8BIM", "lnk2", 4, linked.data, 4)
			payload := smartEditPlacement(t, 0, "")
			if scenario == "different-block" {
				payload = smartEditPlacement(t, 1, "")
			}
			if scenario == "same-field" || scenario == "different-field" {
				bridge, _, err := smartFilterEditSource(t.Context(), payload, "SoLd", psdfile.DefaultLimits(), 4<<20)
				if err != nil {
					t.Fatal(err)
				}
				records, err := bridge.SmartObjects(nil)
				if err != nil {
					t.Fatal(err)
				}
				record := records[0]
				for _, entry := range record.Descriptor.Entries {
					if entry.Key != "Trnf" {
						continue
					}
					duplicate, _ := bridge.ReadSpan(entry.Span)
					if scenario == "different-field" {
						item := entry.Value.([]psdfile.DescriptorValue)[0]
						binary.BigEndian.PutUint64(duplicate[item.Data.Offset-entry.Span.Offset:], math.Float64bits(1))
					}
					end := record.Descriptor.Span.Offset + record.Descriptor.Span.Length - record.Block.Data.Offset
					data := append(bytes.Clone(payload[:end]), duplicate...)
					payload = append(data, payload[end:]...)
					count := record.Descriptor.CountData.Offset - record.Block.Data.Offset
					binary.BigEndian.PutUint32(payload[count:], uint32(len(record.Descriptor.Entries)+1))
				}
				_ = bridge.Close()
			}
			key := "SoLE"
			if scenario == "bad-legacy" {
				key, payload = "PlLd", smartDimensionLegacy(t, 0)
				binary.BigEndian.PutUint32(payload[4:], 99)
			}
			local := map[int][]byte{0: writerLinkedTag("8BIM", key, 4, payload, 2)}
			data := linkedContainerScopesFixture(t, 1, global, local, "fixture-id", "fixture-id")
			editor, _ := smartEditOpen(t, data)
			infos, err := editor.SmartObjects(nil)
			if err != nil {
				t.Fatal(err)
			}
			revision := editor.revision
			err = editor.SetSmartObjectTransform(nil, infos[0].ID, smartTransformCorners(1, 0, 2, 2))
			blocked := scenario == "different-block" || scenario == "different-field" || scenario == "bad-legacy"
			if blocked {
				if err == nil || editor.revision != revision {
					t.Fatalf("ambiguous metadata was changed: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			saved := smartTransformRoundTrip(t, editor, 1)
			records, _ := saved.SmartObjects(nil)
			if len(records) != 3 || !slices.Equal(records[0].Transform, records[1].Transform) ||
				records[2].Transform[0] != 4 {
				t.Fatal("duplicate placements or peer were not preserved correctly")
			}
		})
	}
}

type smartTransformFailReader struct {
	data    []byte
	trigger psdfile.Span
	failure error
	cancel  context.CancelFunc
	matches int
}

func (reader *smartTransformFailReader) ReadAt(data []byte, offset int64) (int, error) {
	if len(data) != 0 && reader.trigger.Length != 0 && offset < reader.trigger.Offset+reader.trigger.Length &&
		reader.trigger.Offset < offset+int64(len(data)) {
		reader.matches++
		if reader.cancel != nil {
			reader.cancel()
		}
		if reader.failure != nil {
			return 0, reader.failure
		}
	}
	return bytes.NewReader(reader.data).ReadAt(data, offset)
}

func TestSmartTransformIOAndCancelPreservePreviousOverlayAndNeverReadParentCaches(t *testing.T) {
	for _, scenario := range []string{"io", "cancel", "cache"} {
		t.Run(scenario, func(t *testing.T) {
			reader := &smartTransformFailReader{data: smartEditFixture(t, 1, "")}
			source, err := psdfile.Read(reader, int64(len(reader.data)))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			editor, err := New(source)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			infos, _ := editor.SmartObjects(nil)
			if err := editor.SetSmartObjectTransform(nil, infos[0].ID, smartTransformCorners(1, 0, 2, 2)); err != nil {
				t.Fatal(err)
			}
			node, _, _, _ := editor.findNodeLocked(infos[0].ID)
			oldPixels, oldPlaced, revision := node.pixels, node.placed, editor.revision
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			reader.trigger = source.Layers[0].AdditionalInfo[2].Data
			switch scenario {
			case "io":
				reader.failure = io.ErrUnexpectedEOF
			case "cancel":
				reader.cancel = cancel
			case "cache":
				reader.trigger = source.Layers[0].Channels[0].Data
				reader.failure = errors.New("parent raster cache must not be read")
			}
			err = editor.SetSmartObjectTransform(ctx, infos[0].ID, smartTransformCorners(2, 0, 2, 2))
			if scenario == "cache" {
				if err != nil || reader.matches != 0 {
					t.Fatalf("transform used original parent caches: %v", err)
				}
				return
			}
			if err == nil || reader.matches == 0 || node.pixels != oldPixels || node.placed != oldPlaced ||
				editor.revision != revision {
				t.Fatalf("failed read partially committed the transform: %v", err)
			}
		})
	}
}

func TestSmartTransformCombinedMetadataBudgetAndNoopExternal(t *testing.T) {
	data := smartEditFixture(t, 1, "")
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	placements, _ := source.ReadSmartObjectView(nil)
	metadata, _ := source.ReadLayerMetadata(nil, 0)
	_ = source.Close()
	budget := placements.MetadataBytes + metadata.WorkingBytes - 1
	editor, _ := smartEditOpen(t, data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: budget}})
	infos, _ := editor.SmartObjects(nil)
	revision, owned := editor.revision, editor.ownedBytesLocked(nil)
	err = editor.SetSmartObjectTransform(nil, infos[0].ID, smartTransformCorners(1, 0, 2, 2))
	if !errors.Is(err, psdfile.ErrLimit) || editor.revision != revision || editor.ownedBytesLocked(nil) != owned {
		t.Fatalf("combined metadata budget was not transactional: %v", err)
	}
	external, _ := smartEditOpen(t, smartEditFixture(t, 1, "external"))
	externalInfos, _ := external.SmartObjects(nil)
	err = external.SetSmartObjectTransform(nil, externalInfos[0].ID, smartTransformCorners(0, 0, 2, 2))
	if err != nil || external.revision != 0 || len(external.linkedOverlays) != 0 {
		t.Fatalf("unchanged external transform required resolution or changed state: %v", err)
	}
}

func TestSmartTransformWarpNoneRetainsSourceBoundsAcrossSizeRestoration(t *testing.T) {
	base := gatePlacement(t, 0, false, false)
	bridge, _, err := smartFilterEditSource(t.Context(), base, "SoLd", psdfile.DefaultLimits(), 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	records, err := bridge.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := records[0].Descriptor
	descriptor.Entries = append(descriptor.Entries, psdfile.DescriptorEntry{Key: "warp", Type: "Objc",
		Value: &psdfile.Descriptor{ClassID: "warp", Entries: []psdfile.DescriptorEntry{
			{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpNone"}},
			{Key: "bounds", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "classFloatRect",
				Entries: []psdfile.DescriptorEntry{
					{Key: "Left", Type: "doub", Value: float64(7)}, {Key: "Top ", Type: "doub", Value: float64(9)},
					{Key: "Rght", Type: "doub", Value: float64(9)}, {Key: "Btom", Type: "doub", Value: float64(11)},
				}}},
		}}})
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	pixels := smartEditImage(smartEditColors())
	linked := writerLinkedEntry("liFD", "fixture-id", 7, smartEditPrototype(1, pixels))
	global := writerLinkedTag("8BIM", "lnk2", 4, linked.data, 4)
	record, channels := smartEditLayer(1, "Warp None", 11, pixels.Rect, pixels,
		writerLinkedTag("8BIM", "SoLd", 4, payload, 2), false)
	layers := append(binary.BigEndian.AppendUint16(nil, 1), record...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	merged := append([]byte{0, 0}, make([]byte, 6*4*3)...)
	editor, _ := smartEditOpen(t, smartEditDocument(1, 6, 4, 3, layers, global, merged))
	infos, _ := editor.SmartObjects(nil)
	if err := editor.SetSmartObjectTransform(nil, infos[0].ID, smartTransformCorners(1, 1, 2, 2)); err != nil {
		t.Fatal(err)
	}
	for _, size := range []image.Point{{3, 2}, {2, 2}} {
		pixels := smartDimensionsPixels(size.X, size.Y)
		if err := editor.ReplaceContents(nil, infos[0].ID, smartSizedPrototype(1, size, pixels.Rect, pixels)); err != nil {
			t.Fatal(err)
		}
	}
	saved := smartTransformRoundTrip(t, editor, 3)
	metadata, err := saved.ReadLayerMetadata(nil, 0)
	if err != nil || metadata.Layer.Placed.Warp == nil || metadata.Layer.Placed.Warp.Bounds == nil ||
		metadata.Layer.Placed.Warp.Bounds.Left != 7 || metadata.Layer.Placed.Warp.Bounds.Top != 9 {
		t.Fatalf("restoring source size rewrote unchanged Warp None source bounds: %v", err)
	}
	node, _, _, _ := editor.findNodeLocked(infos[0].ID)
	if node.placed.Warp.Bounds.Left != 7 || node.placed.Warp.Bounds.Top != 9 {
		t.Fatal("live Warp None bounds disagree with the saved placement")
	}
}
