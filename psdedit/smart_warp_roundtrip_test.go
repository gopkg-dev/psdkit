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
	"path/filepath"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
	"github.com/gopkg-dev/psdkit/psdscene"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

func smartWarpRoundtripPlacement(t *testing.T, order int, projective bool) []byte {
	t.Helper()
	var horizontal, vertical []float64
	for row := range order {
		for column := range order {
			horizontal = append(horizontal, 2*float64(column)/float64(order-1))
			vertical = append(vertical, 2*float64(row)/float64(order-1))
		}
	}
	mesh := &psdfile.DescriptorObjectArray{ClassID: "rationalPoint", Prefix: uint32(order * order),
		Entries: []psdfile.DescriptorEntry{
			{Key: "Hrzn", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: horizontal}},
			{Key: "Vrtc", Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: vertical}},
		}}
	envelope := &psdfile.Descriptor{ClassID: "customEnvelopeWarp", Entries: []psdfile.DescriptorEntry{
		{Key: "meshPoints", Type: "ObAr", Value: mesh},
	}}
	classID := "warp"
	if order != 4 {
		classID = "quiltWarp"
		count := (order - 1) / 3
		for _, axis := range []string{"quiltSliceX", "quiltSliceY"} {
			values := make([]float64, count+1)
			for index := range values {
				values[index] = 2 * float64(index) / float64(count)
			}
			envelope.Entries = append(envelope.Entries, psdfile.DescriptorEntry{
				Key: axis, Type: "ObAr", Value: &psdfile.DescriptorObjectArray{
					ClassID: "UntF", Prefix: uint32(count + 1), Entries: []psdfile.DescriptorEntry{
						{Key: axis, Type: "UnFl", Value: psdfile.DescriptorUnitArray{Unit: "#Pxl", Numbers: values}},
					},
				},
			})
		}
	}
	warp := &psdfile.Descriptor{ClassID: classID, Entries: []psdfile.DescriptorEntry{
		{Key: "warpStyle", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "warpStyle", ValueID: "warpCustom"}},
		{Key: "warpRotate", Type: "enum", Value: psdfile.DescriptorEnum{TypeID: "Ornt", ValueID: "Hrzn"}},
		{Key: "warpValue", Type: "doub", Value: float64(0)},
		{Key: "warpPerspective", Type: "doub", Value: float64(0)},
		{Key: "warpPerspectiveOther", Type: "doub", Value: float64(0)},
		{Key: "uOrder", Type: "long", Value: int32(4)}, {Key: "vOrder", Type: "long", Value: int32(4)},
		{Key: "bounds", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "classFloatRect",
			Entries: []psdfile.DescriptorEntry{
				{Key: "Left", Type: "doub", Value: float64(0)}, {Key: "Top ", Type: "doub", Value: float64(0)},
				{Key: "Rght", Type: "doub", Value: float64(2)}, {Key: "Btom", Type: "doub", Value: float64(2)},
			}}},
		{Key: "customEnvelopeWarp", Type: "Objc", Value: envelope},
	}}
	if order != 4 {
		warp.Entries = append(warp.Entries,
			psdfile.DescriptorEntry{Key: "deformNumRows", Type: "long", Value: int32(order)},
			psdfile.DescriptorEntry{Key: "deformNumCols", Type: "long", Value: int32(order)})
	}
	quad := []float64{2, 2, 12, 2, 12, 12, 2, 12}
	if projective {
		quad[6] = 2.5
	}
	items := make([]psdfile.DescriptorValue, len(quad))
	for index, value := range quad {
		items[index] = psdfile.DescriptorValue{Type: "doub", Value: value}
	}
	descriptor := &psdfile.Descriptor{ClassID: "null", Entries: []psdfile.DescriptorEntry{
		{Key: "Idnt", Type: "TEXT", Value: "fixture-id"},
		{Key: "Type", Type: "long", Value: int32(2)},
		{Key: "Sz  ", Type: "Objc", Value: &psdfile.Descriptor{ClassID: "size", Entries: []psdfile.DescriptorEntry{
			{Key: "Wdth", Type: "doub", Value: float64(2)}, {Key: "Hght", Type: "doub", Value: float64(2)},
		}}},
		{Key: "Trnf", Type: "VlLs", Value: items},
		{Key: "nonAffineTransform", Type: "VlLs", Value: items},
		{Key: "opaque", Type: "tdta", Value: []byte{255, 7, 91, 0}},
		{Key: classID, Type: "Objc", Value: warp},
		{Key: "opaque", Type: "doub", Value: math.Float64frombits(0x7ff8000000000042)},
		{Key: "negative-zero", Type: "doub", Value: math.Copysign(0, -1)},
	}}
	encoded, err := psdfile.EncodeDescriptor(nil, descriptor, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...)
	return append(payload, 0xa1, 0xb2, 0xc3)
}

func smartWarpRoundtripFixture(t *testing.T, version uint16, order int, external, projective bool) ([]byte, []byte) {
	t.Helper()
	artwork := smartEditImage(smartEditColors())
	contents := smartEditPrototype(version, artwork)
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
	tags := writerLinkedTag("8BIM", "SoLd", 4, smartWarpRoundtripPlacement(t, order, projective), 2)
	poison := smartEditImage([4]color.NRGBA{
		{250, 0, 250, 255}, {250, 0, 250, 255}, {250, 0, 250, 255}, {250, 0, 250, 255},
	})
	records, channels := smartEditLayer(version, "Warp target", 11, image.Rect(2, 2, 4, 4), poison, tags, true)
	otherTags := writerLinkedTag("8BIM", "SoLE", 4, smartEditPlacement(t, 18, ""), 2)
	otherRecord, otherChannels := smartEditLayer(version, "Unchanged peer", 12,
		image.Rect(18, 0, 20, 2), artwork, otherTags, false)
	records, channels = append(records, otherRecord...), append(channels, otherChannels...)
	layers := binary.BigEndian.AppendUint16(nil, 0xfffe)
	layers = append(layers, records...)
	layers = append(layers, channels...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	preview := append([]byte{0, 0}, bytes.Repeat([]byte{71}, 24*16*4)...)
	return smartEditDocument(version, 24, 16, 4, layers, global, preview), contents
}

func smartWarpRoundtripExpected(
	t *testing.T, placed *psdmodel.Placed, mathematical bool,
) (*image.NRGBA, image.Point, *image.NRGBA) {
	t.Helper()
	compile := psdwarp.Compile
	if mathematical {
		compile = psdwarp.CompileMathematical
	}
	patch, err := compile(t.Context(), placed)
	if err != nil {
		t.Fatal(err)
	}
	artwork := smartEditImage(smartEditColors())
	raw, origin, err := patch.Project(t.Context(), artwork, image.Rect(0, 0, 24, 16))
	if err != nil {
		t.Fatal(err)
	}
	merged := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := range raw.Rect.Dy() {
		for x := range raw.Rect.Dx() {
			pixel := raw.NRGBAAt(x, y)
			mask := byte(128)
			position := image.Pt(x+origin.X, y+origin.Y)
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
			merged.SetNRGBA(x+18, y, artwork.NRGBAAt(x, y))
		}
	}
	return raw, origin, merged
}

func smartWarpRoundtripRaw(t *testing.T, saved *psdfile.Document, raw *image.NRGBA, origin image.Point) {
	t.Helper()
	if saved.Layers[0].Bounds != (image.Rectangle{Min: origin, Max: origin.Add(raw.Rect.Size())}) {
		t.Fatalf("saved Warp bounds differ from freshly projected source: %v", saved.Layers[0].Bounds)
	}
	for layerIndex, layer := range saved.Layers {
		wanted := raw
		if layerIndex == 1 {
			wanted = smartEditImage(smartEditColors())
		}
		for channelIndex, channel := range layer.Channels {
			plane, err := saved.DecodeChannel(t.Context(), layerIndex, channelIndex)
			if err != nil {
				t.Fatal(err)
			}
			if channel.ID == -2 {
				if !bytes.Equal(plane.Data, []byte{255, 128, 0, 255}) {
					t.Fatal("Warp editing changed user mask samples")
				}
				continue
			}
			component := int(channel.ID)
			if component == -1 {
				component = 3
			}
			for position, sample := range plane.Data {
				if sample != wanted.Pix[position*4+component] {
					t.Fatalf("saved raw source %d/%d sample %d baked parent styling or retained poison cache",
						layerIndex, channel.ID, position)
				}
			}
		}
	}
	maskData, err := saved.ReadSpan(saved.Layers[0].MaskData)
	if err != nil || len(maskData) != 24 || binary.BigEndian.Uint32(maskData) != 20 || maskData[20] != 128 ||
		maskData[21] != 0 || !bytes.Equal(maskData[22:], []byte{0xa1, 0xb2}) {
		t.Fatalf("Warp save lost default mask coverage or opaque mask metadata: %x: %v", maskData, err)
	}
	metadata, err := saved.ReadLayerMetadata(t.Context(), 0)
	if err != nil || metadata.Layer.Opacity == nil || *metadata.Layer.Opacity != 128.0/255 ||
		metadata.Layer.FillOpacity == nil ||
		*metadata.Layer.FillOpacity != 160.0/255 {
		t.Fatalf("Warp save baked or changed separate opacity and Fill metadata: %v", err)
	}
}

func smartWarpRoundtripPreserved(t *testing.T, before, after *psdfile.Document) {
	t.Helper()
	oldPlacements, err := before.SmartObjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	newPlacements, err := after.SmartObjects(t.Context())
	if err != nil || len(oldPlacements) != 2 || len(newPlacements) != 2 {
		t.Fatalf("Warp save lost placement metadata: %v", err)
	}
	oldPayload, _ := before.ReadSpan(oldPlacements[0].Block.Data)
	newPayload, _ := after.ReadSpan(newPlacements[0].Block.Data)
	if len(oldPayload) != len(newPayload) {
		t.Fatal("Warp edit changed placement descriptor length")
	}
	allowed := make([]bool, len(oldPayload))
	var visit func(*psdfile.Descriptor)
	visit = func(descriptor *psdfile.Descriptor) {
		for _, entry := range descriptor.Entries {
			if nested, ok := entry.Value.(*psdfile.Descriptor); ok {
				visit(nested)
			}
			if mesh, ok := entry.Value.(*psdfile.DescriptorObjectArray); ok && entry.Key == "meshPoints" {
				for _, column := range mesh.Entries {
					array := column.Value.(psdfile.DescriptorUnitArray)
					for offset := array.NumbersData.Offset; offset < array.NumbersData.Offset+array.NumbersData.Length; offset++ {
						allowed[offset-oldPlacements[0].Block.Data.Offset] = true
					}
				}
			}
		}
	}
	visit(oldPlacements[0].Descriptor)
	for offset := range oldPayload {
		if !allowed[offset] && oldPayload[offset] != newPayload[offset] {
			t.Fatalf("Warp save changed non-mesh descriptor byte %d", offset)
		}
	}
	oldPeer, _ := before.ReadSpan(oldPlacements[1].Block.Data)
	newPeer, _ := after.ReadSpan(newPlacements[1].Block.Data)
	if !bytes.Equal(oldPeer, newPeer) {
		t.Fatal("single-instance Warp changed its shared peer placement")
	}
	for layerIndex, layer := range before.Layers {
		var oldUnknown, newUnknown [][]byte
		for _, block := range layer.AdditionalInfo {
			if block.Key != "U000" {
				continue
			}
			oldSpan, _ := before.ReadSpan(block.Span)
			oldUnknown = append(oldUnknown, oldSpan)
		}
		for _, block := range after.Layers[layerIndex].AdditionalInfo {
			if block.Key == "U000" {
				span, _ := after.ReadSpan(block.Span)
				newUnknown = append(newUnknown, span)
			}
		}
		if !slices.EqualFunc(oldUnknown, newUnknown, bytes.Equal) {
			t.Fatal("Warp save changed ordered duplicate unknown layer blocks")
		}
	}
}

func TestSmartWarpRoundtripEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, mathematical := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/mathematical%t", version, compression, mathematical), func(t *testing.T) {
					data, contents := smartWarpRoundtripFixture(t, version, 4, false, mathematical)
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
					options := SmartWarpEditOptions{}
					if mathematical {
						options.Render = &psdscene.Options{ApproximateCustomWarp: true}
					}
					for round := range 2 {
						view, err := editor.source.ReadLayerMetadata(t.Context(), 0)
						if err != nil {
							t.Fatal(err)
						}
						placed := view.Layer.Placed
						points := slices.Clone(placed.Warp.MeshPoints)
						for row := range 4 {
							points[row*4+1].Y -= .12 + float64(round)*.07
							points[row*4+2].Y -= .12 + float64(round)*.07
						}
						placed.Warp.MeshPoints = slices.Clone(points)
						if mathematical {
							unchanged := smartContentsFilterState(t, editor)
							err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{})
							if !errors.Is(err, ErrUnsupported) {
								t.Fatalf("Warp edit inferred a mathematical approximation policy: %v", err)
							}
							unchanged()
						}
						raw, origin, expected := smartWarpRoundtripExpected(t, placed, mathematical)
						if err := editor.SetSmartObjectWarp(t.Context(), 1, points, options); err != nil {
							t.Fatalf("Warp parameter edit failed: %v", err)
						}
						points[0].X = 999
						save := SaveOptions{Compression: compression}
						filename := filepath.Join(t.TempDir(), "edited.psd")
						if err := editor.Save(t.Context(), filename, save); err != nil {
							t.Fatal(err)
						}
						saved, err := psdfile.Open(filename)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = saved.Close() })
						if saved.Header.Version != version {
							t.Fatal("Warp save changed PSD/PSB format")
						}
						smartWarpRoundtripRaw(t, saved, raw, origin)
						placedEffectsRoundtripPreview(t, saved, expected)
						smartWarpRoundtripPreserved(t, original, saved)
						smartContentsFilterAssertSource(t, saved, contents)
						liveOptions := psdscene.Options{}
						if mathematical {
							if pixels, err := psdscene.Render(t.Context(), saved); pixels != nil ||
								!errors.Is(err, psdwarp.ErrUnsupported) {
								t.Fatalf("fresh source rendering inherited the saved Warp approximation: %v", err)
							}
							liveOptions.ApproximateCustomWarp = true
						}
						live, err := psdscene.Render(t.Context(), saved, liveOptions)
						if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
							t.Fatalf("saved current-source Warp render differs from raw projection plus parent styling: %v", err)
						}
						metadata, err := saved.ReadLayerMetadata(t.Context(), 0)
						if err != nil || !slices.Equal(metadata.Layer.Placed.Warp.MeshPoints, placed.Warp.MeshPoints) {
							t.Fatalf("saved Warp parameters were aliased or reverted: %v", err)
						}
						editor = lifecycleReopen(t, saved)
					}
					if sha256.Sum256(data) != digest {
						t.Fatal("Warp edit changed caller-owned original bytes")
					}
				})
			}
		}
	}
}

func TestSmartWarpExternalResolverAtomicAndExplicit(t *testing.T) {
	for _, scenario := range []string{"success", "missing", "error", "cancel", "stale", "closed", "policy-mutation"} {
		t.Run(scenario, func(t *testing.T) {
			data, contents := smartWarpRoundtripFixture(t, 1, 4, true, scenario == "policy-mutation")
			editor, source := smartEditOpen(t, data)
			view, err := source.ReadLayerMetadata(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			points := slices.Clone(view.Layer.Placed.Warp.MeshPoints)
			points[5].Y -= .1
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := errors.New("explicit Warp source failed")
			settings := &psdscene.Options{ApproximateCustomWarp: scenario == "policy-mutation"}
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
						if err := editor.Rename(1, "Concurrent rename"); err != nil {
							return nil, err
						}
					case "closed":
						if err := source.Close(); err != nil {
							return nil, err
						}
					case "policy-mutation":
						settings.ApproximateCustomWarp = false
						points[0].X = 999
					}
					if err := callbackContext.Err(); err != nil {
						return nil, err
					}
					return psdfile.Parse(contents)
				}
			}
			revision := editor.revision
			err = editor.SetSmartObjectWarp(ctx, 1, points, SmartWarpEditOptions{Render: settings})
			if scenario == "success" || scenario == "policy-mutation" {
				if err != nil || calls != 1 || editor.revision != revision+1 {
					t.Fatalf("explicit external Warp source did not render outside the editor lock: %v", err)
				}
				node, _, _, _ := editor.findNodeLocked(1)
				if node.placed == nil || node.placed.Warp.MeshPoints[0].X != 0 ||
					node.placed.Warp.MeshPoints[5].Y != 2.0/3-.1 {
					t.Fatal("Warp render retained caller-owned controls across the external resolver")
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
				t.Fatalf("external Warp failure = %v, want %v", err, wanted)
			}
			node, _, _, _ := editor.findNodeLocked(1)
			if node.pixels != nil || node.placed != nil || len(node.additional) != 0 {
				t.Fatal("failed Warp edit committed placement metadata or pixels")
			}
			if scenario != "stale" && editor.revision != revision {
				t.Fatal("failed Warp edit changed revision")
			}
		})
	}
}

func TestSmartWarpUniformGridMissingSlicesUnsupportedAndOriginalIsPreserved(t *testing.T) {
	for _, order := range []int{10, 13} {
		data, _ := smartWarpRoundtripFixture(t, 1, order, false, false)
		data = bytes.ReplaceAll(data, []byte("quiltSliceX"), []byte("wrongSliceX"))
		editor, source := smartEditOpen(t, data)
		view, err := source.ReadLayerMetadata(t.Context(), 0)
		if err != nil || len(view.Layer.Placed.Warp.MeshPoints) != order*order {
			t.Fatalf("grid metadata fixture was not parsed: %v", err)
		}
		unchanged := smartContentsFilterState(t, editor)
		points := slices.Clone(view.Layer.Placed.Warp.MeshPoints)
		points[order+1].Y -= .01
		err = editor.SetSmartObjectWarp(t.Context(), 1, points,
			SmartWarpEditOptions{Render: &psdscene.Options{ApproximateCustomWarp: true}})
		if !errors.Is(err, ErrUnsupported) && !errors.Is(err, ErrInvalidEdit) {
			t.Fatalf("uniform grid edit bypassed its independent source-render scope: %v", err)
		}
		unchanged()
		var output bytes.Buffer
		if err := editor.WritePSD(t.Context(), &output); err != nil || !bytes.Equal(output.Bytes(), data) {
			t.Fatalf("rejected grid Warp changed untouched source bytes: %v", err)
		}
	}
}

func TestSmartWarpBudgetSourceAndPolicyFailuresAreAtomic(t *testing.T) {
	for _, scenario := range []string{
		"working-budget", "document-budget", "source-io", "output-profile", "cached-special", "cached-text",
	} {
		t.Run(scenario, func(t *testing.T) {
			data, _ := smartWarpRoundtripFixture(t, 1, 4, false, false)
			editor, source, reader := compositeLayerOnlyEditor(t, data)
			view, err := source.ReadLayerMetadata(t.Context(), 0)
			if err != nil {
				t.Fatal(err)
			}
			points := slices.Clone(view.Layer.Placed.Warp.MeshPoints)
			points[5].Y -= .1
			options := SmartWarpEditOptions{Render: &psdscene.Options{}}
			wanted := psdfile.ErrLimit
			switch scenario {
			case "working-budget":
				options.Render.MaxWorkingBytes = 1
			case "document-budget":
				options.Render.MaxDocuments = 1
			case "source-io":
				reader.failure, reader.failureCause = source.AdditionalInfo[0].Data, io.ErrClosedPipe
				wanted = io.ErrClosedPipe
			case "output-profile":
				options.Render.OutputProfile = []byte{}
				wanted = ErrUnsupported
			case "cached-special":
				options.Render.CachedSpecialLayers = true
				wanted = ErrUnsupported
			case "cached-text":
				options.Render.CachedTextLayers = true
				wanted = ErrUnsupported
			}
			unchanged := smartContentsFilterState(t, editor)
			if err := editor.SetSmartObjectWarp(t.Context(), 1, points, options); !errors.Is(err, wanted) {
				t.Fatalf("failed Warp edit = %v, want %v", err, wanted)
			}
			unchanged()
			reader.failureCause = nil
			if err := editor.SetSmartObjectWarp(t.Context(), 1, points, SmartWarpEditOptions{}); err != nil {
				t.Fatalf("Warp edit could not recover after policy or source failure: %v", err)
			}
		})
	}
}
