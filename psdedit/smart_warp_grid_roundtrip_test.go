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

func smartWarpGridControls(order, round int) ([]psdmodel.Point, float64) {
	shear := .08 + float64(round)*.1
	points := make([]psdmodel.Point, order*order)
	for row := range order {
		for column := range order {
			u, v := float64(column)/float64(order-1), float64(row)/float64(order-1)
			points[row*order+column] = psdmodel.Point{
				X: 100*float64(round) + 2*(u+shear*v), Y: -31*float64(round) + 2*v,
			}
		}
	}
	return points, shear
}

func smartWarpGridAnalyticInterior(t *testing.T, raw *image.NRGBA, origin image.Point, shear float64) {
	t.Helper()
	colors := smartEditColors()
	compared := 0
	for row := range raw.Rect.Dy() {
		for column := range raw.Rect.Dx() {
			// Directly invert the independently authored affine control net and placement.
			v := (float64(origin.Y+row) + .5 - 2) / 10
			u := (float64(origin.X+column)+.5-2)/10*(1+shear) - shear*v
			x, y := 2*u-.5, 2*v-.5
			if x < 0 || x > 1 || y < 0 || y > 1 {
				continue
			}
			weights := [4]float64{(1 - x) * (1 - y), x * (1 - y), (1 - x) * y, x * y}
			var expected [4]float64
			for index, value := range colors {
				for component, sample := range [4]byte{value.R, value.G, value.B, value.A} {
					expected[component] += float64(sample) * weights[index]
				}
			}
			actual := raw.NRGBAAt(column, row)
			for component, sample := range [4]byte{actual.R, actual.G, actual.B, actual.A} {
				if math.Abs(float64(sample)-expected[component]) > .50000001 {
					t.Fatalf("grid source pixel %d,%d/%d = %d, independent affine color = %g",
						origin.X+column, origin.Y+row, component, sample, expected[component])
				}
			}
			compared++
		}
	}
	if compared < 16 {
		t.Fatalf("independent coordinate oracle checked too few opaque interior pixels: %d", compared)
	}
}

func smartWarpGridExpected(
	t *testing.T, placed *psdmodel.Placed, shear float64,
) (*image.NRGBA, image.Point, *image.NRGBA) {
	t.Helper()
	grid, err := psdwarp.CompileUniformGrid(t.Context(), placed)
	if err != nil {
		t.Fatal(err)
	}
	artwork := smartEditImage(smartEditColors())
	raw, origin, err := grid.Project(t.Context(), artwork, image.Rect(0, 0, 24, 16))
	if err != nil {
		t.Fatal(err)
	}
	smartWarpGridAnalyticInterior(t, raw, origin, shear)
	merged := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := range raw.Rect.Dy() {
		for x := range raw.Rect.Dx() {
			pixel := raw.NRGBAAt(x, y)
			position := origin.Add(image.Pt(x, y))
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
	for y := range 2 {
		for x := range 2 {
			merged.SetNRGBA(18+x, y, artwork.NRGBAAt(x, y))
		}
	}
	return raw, origin, merged
}

func smartWarpGridLinkedPreserved(t *testing.T, before, after *psdfile.Document) {
	t.Helper()
	oldRecords, err := before.LinkedFiles(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	newRecords, err := after.LinkedFiles(t.Context())
	if err != nil || len(oldRecords) != 1 || len(newRecords) != 1 {
		t.Fatalf("grid save lost the original linked entry: %v", err)
	}
	oldRecord, newRecord := oldRecords[0], newRecords[0]
	oldData, err := before.ReadSpan(oldRecord.Span)
	if err != nil {
		t.Fatal(err)
	}
	newData, err := after.ReadSpan(newRecord.Span)
	if err != nil || oldRecord.Kind != newRecord.Kind || !bytes.Equal(oldData, newData) {
		t.Fatalf("grid edit changed linked kind, source bytes or opaque link metadata: %v", err)
	}
}

func TestSmartWarpGridEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, version := range []uint16{1, 2} {
			for compression := range uint16(4) {
				for _, external := range []bool{false, true} {
					name := fmt.Sprintf("order%d/v%d/c%d/external%t", order, version, compression, external)
					t.Run(name, func(t *testing.T) {
						data, contents := smartWarpRoundtripFixture(t, version, order, external, false)
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
									t.Fatal("external grid inferred a different link kind")
								}
								return psdfile.Parse(contents)
							}
						}
						for round := range 2 {
							points, shear := smartWarpGridControls(order, round)
							before := smartContentsFilterState(t, editor)
							if err := editor.SetSmartObjectWarp(
								t.Context(), 1, points, SmartWarpEditOptions{},
							); !errors.Is(err, ErrUnsupported) {
								t.Fatalf("grid edit inferred a mathematical approximation or external mapping: %v", err)
							}
							before()
							view, err := editor.source.ReadLayerMetadata(t.Context(), 0)
							if err != nil {
								t.Fatal(err)
							}
							placed := view.Layer.Placed
							placed.Warp.MeshPoints = slices.Clone(points)
							raw, origin, expected := smartWarpGridExpected(t, placed, shear)
							if err := editor.SetSmartObjectWarp(t.Context(), 1, points,
								SmartWarpEditOptions{Render: settings}); err != nil {
								t.Fatalf("grid edit round %d: %v", round, err)
							}
							points[0].X = 999
							filename := filepath.Join(t.TempDir(), "uniform-grid.psd")
							if err := editor.Save(t.Context(), filename,
								SaveOptions{Compression: compression}); err != nil {
								t.Fatalf("grid save round %d: %v", round, err)
							}
							saved, err := psdfile.Open(filename)
							if err != nil {
								t.Fatal(err)
							}
							t.Cleanup(func() { _ = saved.Close() })
							if saved.Header.Version != version {
								t.Fatal("grid save changed PSD/PSB format")
							}
							smartWarpRoundtripRaw(t, saved, raw, origin)
							placedEffectsRoundtripPreview(t, saved, expected)
							smartWarpRoundtripPreserved(t, original, saved)
							smartWarpGridLinkedPreserved(t, original, saved)
							if live, err := psdscene.Render(t.Context(), saved); live != nil ||
								!errors.Is(err, psdwarp.ErrUnsupported) && !errors.Is(err, psdfile.ErrUnsupported) {
								t.Fatalf("reopened grid inferred its approximation or source mapping: %v", err)
							}
							live, err := psdscene.Render(t.Context(), saved, *settings)
							if err != nil {
								t.Fatal(err)
							}
							for offset := 0; offset < len(expected.Pix); offset += 4 {
								if live.Pix[offset+3] != expected.Pix[offset+3] || expected.Pix[offset+3] != 0 &&
									!bytes.Equal(live.Pix[offset:offset+3], expected.Pix[offset:offset+3]) {
									point := image.Pt(offset/4%24, offset/4/24)
									t.Fatalf("grid live round%d at%v = %v, expected %v",
										round, point, live.NRGBAAt(point.X, point.Y), expected.NRGBAAt(point.X, point.Y))
								}
							}
							region := image.Rect(3, 3, 11, 10)
							tile, err := psdscene.RenderTile(t.Context(), saved, region, psdscene.TileOptions{
								ApproximateCustomWarp: true, ExternalResolver: settings.ExternalResolver,
							})
							if err != nil {
								t.Fatal(err)
							}
							for y := region.Min.Y; y < region.Max.Y; y++ {
								for x := region.Min.X; x < region.Max.X; x++ {
									actual, wanted := tile.NRGBAAt(x, y), expected.NRGBAAt(x, y)
									if actual.A != wanted.A || wanted.A != 0 && actual != wanted {
										t.Fatalf("grid regional seam at %d,%d", x, y)
									}
								}
							}
							metadata, err := saved.ReadLayerMetadata(t.Context(), 0)
							if err != nil || !slices.Equal(metadata.Layer.Placed.Warp.MeshPoints, placed.Warp.MeshPoints) {
								t.Fatalf("saved grid lost or aliased current controls: %v", err)
							}
							editor = lifecycleReopen(t, saved)
						}
						if external && calls < 6 {
							t.Fatalf("external grid was rendered without explicit mappings: %d", calls)
						}
						if sha256.Sum256(data) != digest {
							t.Fatal("grid edit changed caller-owned original bytes")
						}
					})
				}
			}
		}
	}
}

func smartWarpGridPatchFixture(
	t *testing.T, data []byte, patch func(*psdfile.DescriptorEntry, []byte),
) []byte {
	t.Helper()
	owned := bytes.Clone(data)
	source, err := psdfile.Parse(owned)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	records, err := source.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var visit func(*psdfile.Descriptor)
	visit = func(descriptor *psdfile.Descriptor) {
		for index := range descriptor.Entries {
			entry := &descriptor.Entries[index]
			patch(entry, owned)
			if nested, ok := entry.Value.(*psdfile.Descriptor); ok {
				visit(nested)
			}
		}
	}
	visit(records[0].Descriptor)
	return owned
}

func smartWarpGridSourceDomain(t *testing.T, data []byte, origin psdmodel.Point, guard float64) []byte {
	t.Helper()
	return smartWarpGridPatchFixture(t, data, func(entry *psdfile.DescriptorEntry, owned []byte) {
		if entry.Key == "bounds" {
			for _, edge := range entry.Value.(*psdfile.Descriptor).Entries {
				delta := origin.X
				if edge.Key == "Top " || edge.Key == "Btom" {
					delta = origin.Y
				}
				binary.BigEndian.PutUint64(owned[edge.Data.Offset:], math.Float64bits(edge.Value.(float64)+delta))
			}
		}
		if entry.Key != "meshPoints" && entry.Key != "quiltSliceX" && entry.Key != "quiltSliceY" {
			return
		}
		object := entry.Value.(*psdfile.DescriptorObjectArray)
		for _, axis := range object.Entries {
			array := axis.Value.(psdfile.DescriptorUnitArray)
			delta := origin.X
			if axis.Key == "Vrtc" || axis.Key == "quiltSliceY" {
				delta = origin.Y
			}
			for index, number := range array.Numbers {
				value := number + delta
				if entry.Key != "meshPoints" {
					if index == 0 {
						value -= guard
					}
					if index == len(array.Numbers)-1 {
						value += guard
					}
				}
				binary.BigEndian.PutUint64(owned[array.NumbersData.Offset+int64(index*8):], math.Float64bits(value))
			}
		}
	})
}

func smartWarpGridSameMesh(t *testing.T, document *psdfile.Document, expected *psdmodel.Placed) {
	t.Helper()
	view, err := document.ReadLayerMetadata(t.Context(), 0)
	if err != nil {
		t.Fatal(err)
	}
	actual := view.Layer.Placed
	if !slices.Equal(actual.Warp.MeshPoints, expected.Warp.MeshPoints) ||
		*actual.Warp.Bounds != *expected.Warp.Bounds || len(actual.Warp.QuiltSlices) != 2 {
		t.Fatal("source replacement reset current grid controls, bounds or source slices")
	}
	for index, axis := range actual.Warp.QuiltSlices {
		if !slices.Equal(axis.Values, expected.Warp.QuiltSlices[index].Values) ||
			axis.Axis != expected.Warp.QuiltSlices[index].Axis {
			t.Fatal("source replacement moved guarded source boundaries")
		}
	}
}

func TestSmartWarpGridCurrentMeshSourceReplacementAndResizeAtomic(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, version := range []uint16{1, 2} {
			for _, guard := range []float64{0, .6} {
				name := fmt.Sprintf("order%d/v%d/guard%g", order, version, guard)
				t.Run(name, func(t *testing.T) {
					data, _ := smartWarpRoundtripFixture(t, version, order, false, false)
					origin := psdmodel.Point{X: 10.25, Y: -4.75}
					data = smartWarpGridSourceDomain(t, data, origin, guard)
					editor, source := smartEditOpen(t, data)
					view, err := source.ReadLayerMetadata(t.Context(), 0)
					if err != nil {
						t.Fatal(err)
					}
					placed := view.Layer.Placed
					for round := range 2 {
						points := slices.Clone(placed.Warp.MeshPoints)
						points[order+1].Y -= .012 + float64(round)*.01
						if err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{
							Render: &psdscene.Options{ApproximateCustomWarp: true},
						}); err != nil {
							t.Fatalf("offset guarded grid edit: %v", err)
						}
						placed.Warp.MeshPoints = slices.Clone(points)
						colors := smartEditColors()
						colors[0].R += byte(17 * (round + 1))
						artwork := smartEditImage(colors)
						contents := smartEditPrototype(3-version, artwork)
						unchanged := smartContentsFilterState(t, editor)
						if err := editor.ReplaceContents(t.Context(), 1, contents); !errors.Is(err, ErrUnsupported) {
							t.Fatalf("source replacement inferred grid approximation: %v", err)
						}
						unchanged()
						if err := editor.ReplaceContentsWithOptions(t.Context(), 1, contents,
							SmartContentsOptions{ApproximateCustomWarp: true}); err != nil {
							t.Fatalf("same-size source replacement: %v", err)
						}
						grid, err := psdwarp.CompileUniformGrid(t.Context(), placed)
						if err != nil {
							t.Fatal(err)
						}
						expected, position, err := grid.Project(t.Context(), artwork, image.Rect(0, 0, 24, 16))
						if err != nil {
							t.Fatal(err)
						}
						actual, bounds := smartWarpOracleCache(t, editor)
						if bounds.Min != position || bounds.Size() != expected.Rect.Size() ||
							!bytes.Equal(actual.Pix, expected.Pix) {
							t.Fatal("same-size replacement used original grid or baked parent user mask")
						}
						var prior bytes.Buffer
						if err := editor.Write(t.Context(), &prior); err != nil {
							t.Fatal(err)
						}
						unchanged = smartContentsFilterState(t, editor)
						resized := smartDimensionsPixels(3, 4)
						if err := editor.ReplaceContentsWithOptions(t.Context(), 1,
							smartSizedPrototype(version, image.Pt(3, 4), resized.Rect, resized),
							SmartContentsOptions{ApproximateCustomWarp: true}); !errors.Is(err, ErrUnsupported) {
							t.Fatalf("different-size grid source migration was silently inferred: %v", err)
						}
						unchanged()
						var after bytes.Buffer
						if err := editor.Write(t.Context(), &after); err != nil || !bytes.Equal(prior.Bytes(), after.Bytes()) {
							t.Fatalf("failed grid resize changed retained source or saved bytes: %v", err)
						}
						var saved *psdfile.Document
						editor, saved = smartWarpOracleSaved(t, editor, uint16(round+2))
						smartWarpGridSameMesh(t, saved, placed)
						if !bytes.Equal(smartWarpOracleLayer(t, saved).Pix, expected.Pix) {
							t.Fatal("reopened current grid/source raw pixels differ")
						}
						smartContentsFilterAssertSource(t, saved, contents)
					}
				})
			}
		}
	}
}

func TestSmartWarpGridInvalidControlsAndPoliciesAreAtomic(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, scenario := range []string{
			"count", "wrong-supported-count", "nan", "infinite", "huge", "fold", "collapsed", "cancel",
			"working-budget", "document-budget", "output-profile", "cached-special", "cached-text",
		} {
			t.Run(fmt.Sprintf("order%d/%s", order, scenario), func(t *testing.T) {
				data, _ := smartWarpRoundtripFixture(t, 1, order, false, false)
				editor, _ := smartEditOpen(t, data)
				points, _ := smartWarpGridControls(order, 0)
				policy := &psdscene.Options{ApproximateCustomWarp: true}
				if err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{Render: policy}); err != nil {
					t.Fatal(err)
				}
				var prior bytes.Buffer
				if err := editor.Write(t.Context(), &prior); err != nil {
					t.Fatal(err)
				}
				points, _ = smartWarpGridControls(order, 1)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				wanted := ErrInvalidEdit
				switch scenario {
				case "count":
					points = points[:len(points)-1]
				case "wrong-supported-count":
					points, _ = smartWarpGridControls(23-order, 1)
				case "nan":
					points[order+1].X = math.NaN()
				case "infinite":
					points[order+1].Y = math.Inf(1)
				case "huge":
					points[order+1].X = 1e20
				case "fold":
					for row := range order {
						points[row*order+1].X, points[row*order+2].X = 35, -17
					}
				case "collapsed":
					for index := range points {
						points[index].X = 5
					}
				case "cancel":
					cancel()
					wanted = context.Canceled
				case "working-budget":
					policy.MaxWorkingBytes = 1
					wanted = psdfile.ErrLimit
				case "document-budget":
					policy.MaxDocuments = 1
					wanted = psdfile.ErrLimit
				case "output-profile":
					policy.OutputProfile = []byte{}
					wanted = ErrUnsupported
				case "cached-special":
					policy.CachedSpecialLayers = true
					wanted = ErrUnsupported
				case "cached-text":
					policy.CachedTextLayers = true
					wanted = ErrUnsupported
				}
				unchanged := smartContentsFilterState(t, editor)
				err := editor.SetSmartObjectWarp(ctx, 1, points, SmartWarpEditOptions{Render: policy})
				if scenario == "fold" || scenario == "collapsed" || scenario == "huge" ||
					scenario == "wrong-supported-count" {
					if !errors.Is(err, ErrInvalidEdit) && !errors.Is(err, ErrUnsupported) {
						t.Fatalf("invalid grid geometry = %v", err)
					}
				} else if !errors.Is(err, wanted) {
					t.Fatalf("failed grid edit = %v, want %v", err, wanted)
				}
				unchanged()
				var after bytes.Buffer
				if err := editor.Write(t.Context(), &after); err != nil || !bytes.Equal(prior.Bytes(), after.Bytes()) {
					t.Fatalf("failed grid edit changed earlier committed source/mesh/pixels: %v", err)
				}
			})
		}
	}
}

func TestSmartWarpGridExternalCallbackOwnershipAndAtomicity(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, scenario := range []string{"success", "missing", "error", "cancel", "stale", "closed", "policy-mutation"} {
			t.Run(fmt.Sprintf("order%d/%s", order, scenario), func(t *testing.T) {
				data, contents := smartWarpRoundtripFixture(t, 1, order, true, false)
				editor, source := smartEditOpen(t, data)
				points, _ := smartWarpGridControls(order, 0)
				originalPoints := slices.Clone(points)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				failure := errors.New("explicit uniform source failed")
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
							points[0].X = 999
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
				err := editor.SetSmartObjectWarp(ctx, 1, points, SmartWarpEditOptions{Render: settings})
				node, _, _, _ := editor.findNodeLocked(1)
				if scenario == "success" || scenario == "policy-mutation" {
					if err != nil || calls != 1 || editor.revision != revision+1 || node.placed == nil ||
						!slices.Equal(node.placed.Warp.MeshPoints, originalPoints) {
						t.Fatalf("explicit grid resolver lost policy/control ownership or reentered a held lock: %v", err)
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
					t.Fatalf("failed external grid edit = %v, want %v", err, wanted)
				}
				if node.pixels != nil || node.placed != nil || len(node.additional) != 0 ||
					scenario != "stale" && editor.revision != revision {
					t.Fatal("failed external grid committed pixels, current placement or revision")
				}
			})
		}
	}
}

func TestSmartWarpGridUnsupportedSourceSchemaIsAtomic(t *testing.T) {
	for _, order := range []int{10, 13} {
		for _, scenario := range []string{
			"projective", "missing-slice", "nonuniform", "unpaired-guard", "nan-slice", "rows", "order", "class",
		} {
			t.Run(fmt.Sprintf("order%d/%s", order, scenario), func(t *testing.T) {
				data, _ := smartWarpRoundtripFixture(t, 1, order, false, scenario == "projective")
				switch scenario {
				case "missing-slice":
					data = bytes.ReplaceAll(data, []byte("quiltSliceX"), []byte("wrongSliceX"))
				case "class":
					data = bytes.ReplaceAll(data, []byte("quiltWarp"), []byte("wrongWarp"))
				default:
					data = smartWarpGridPatchFixture(t, data, func(entry *psdfile.DescriptorEntry, owned []byte) {
						if entry.Key == "deformNumRows" && scenario == "rows" {
							binary.BigEndian.PutUint32(owned[entry.Data.Offset:], uint32(23-order))
						}
						if entry.Key == "uOrder" && scenario == "order" {
							binary.BigEndian.PutUint32(owned[entry.Data.Offset:], 3)
						}
						if entry.Key != "quiltSliceX" {
							return
						}
						array := entry.Value.(*psdfile.DescriptorObjectArray).Entries[0].Value.(psdfile.DescriptorUnitArray)
						switch scenario {
						case "nonuniform":
							binary.BigEndian.PutUint64(owned[array.NumbersData.Offset+8:], math.Float64bits(array.Numbers[1]+.05))
						case "unpaired-guard":
							binary.BigEndian.PutUint64(owned[array.NumbersData.Offset:], math.Float64bits(-.6))
						case "nan-slice":
							binary.BigEndian.PutUint64(owned[array.NumbersData.Offset:], math.Float64bits(math.NaN()))
						}
					})
				}
				editor, _ := smartEditOpen(t, data)
				points, _ := smartWarpGridControls(order, 0)
				unchanged := smartContentsFilterState(t, editor)
				err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{
					Render: &psdscene.Options{ApproximateCustomWarp: true},
				})
				if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrInvalidEdit) {
					t.Fatalf("unqualified grid schema edited successfully or lost classified cause: %v", err)
				}
				unchanged()
				var output bytes.Buffer
				if err := editor.Write(t.Context(), &output); err != nil || !bytes.Equal(output.Bytes(), data) {
					t.Fatalf("unsupported grid schema changed untouched saved source: %v", err)
				}
			})
		}
	}
}
