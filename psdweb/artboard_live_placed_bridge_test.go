package psdweb

import (
	"bytes"
	"encoding/binary"
	"encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

// bridgeArtboardPlacedFixture joins independent wire fixtures, retaining a poisoned parent raster.
func bridgeArtboardPlacedFixture(t *testing.T, version uint16, external bool, filtered ...bool) ([]byte, []byte) {
	t.Helper()
	smartData, contents := bridgeSmartFixture(t, version, external)
	smart, err := psdfile.Parse(smartData)
	if err != nil {
		t.Fatal(err)
	}
	defer smart.Close()
	var tags, linked []byte
	for _, block := range smart.Layers[0].AdditionalInfo {
		if block.Key != "SoLd" {
			continue
		}
		tags, err = smart.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
		if len(filtered) != 0 && filtered[0] {
			descriptor, err := smart.DecodeDescriptorContext(t.Context(),
				psdfile.Span{Offset: block.Data.Offset + 8, Length: block.Data.Length - 8}, true)
			if err != nil {
				t.Fatal(err)
			}
			stack := bridgeFilterStackDescriptor()
			items := stack.Entries[len(stack.Entries)-1].Value.([]psdfile.DescriptorValue)
			for _, entry := range items[0].Value.(*psdfile.Descriptor).Entries {
				if entry.Key == "Fltr" {
					entry.Value.(*psdfile.Descriptor).Entries[0].Value =
						psdfile.DescriptorUnit{Unit: "#Pxl", Number: float64(.7)}
				}
			}
			descriptor.Entries = append(descriptor.Entries,
				psdfile.DescriptorEntry{Key: "filterFX", Type: "Objc", Value: stack})
			encoded, err := psdfile.EncodeDescriptor(t.Context(), descriptor, true)
			if err != nil {
				t.Fatal(err)
			}
			tags = bridgeSmartTag("SoLd",
				append(binary.BigEndian.AppendUint32([]byte("soLD"), 4), encoded...), false, 2)
		}
	}
	for _, block := range smart.AdditionalInfo {
		if block.Key != "lnk2" && block.Key != "lnkE" {
			continue
		}
		linked, err = smart.ReadSpan(block.Span)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(tags) == 0 || len(linked) == 0 {
		t.Fatal("smart-object wire fixture is missing placement or source records")
	}
	data := bridgeArtboardFixture(t, version)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	addLength := func(offset int64, delta int) {
		if version == 2 {
			binary.BigEndian.PutUint64(data[offset:], binary.BigEndian.Uint64(data[offset:])+uint64(delta))
		} else {
			binary.BigEndian.PutUint32(data[offset:], binary.BigEndian.Uint32(data[offset:])+uint32(delta))
		}
	}
	child := source.Layers[1]
	for _, offset := range []int64{source.LayerMaskSection.Offset, source.LayerInfo.Offset} {
		addLength(offset, len(tags))
	}
	binary.BigEndian.PutUint32(data[child.Extra.Offset-4:], uint32(child.Extra.Length)+uint32(len(tags)))
	position := child.Extra.Offset + child.Extra.Length
	joined := append(bytes.Clone(data[:position]), tags...)
	data = append(joined, data[position:]...)
	addLength(source.LayerMaskSection.Offset, len(linked))
	position = source.Composite.Span.Offset + int64(len(tags))
	joined = append(bytes.Clone(data[:position]), linked...)
	return append(joined, data[position:]...), contents
}

func TestArtboardLivePlacedBridgeApproximationAndCachePoliciesAreExplicit(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
	data, _ := bridgeArtboardPlacedFixture(t, 2, false, true)
	asset := bridgeAsset(t, engine, "filtered-board.psb", data)
	opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
	document := opened["document"].(string)
	for _, options := range []map[string]any{
		{}, {"Render": map[string]any{}}, {"Render": map[string]any{"SmartFilters": map[string]any{}}},
		{"Render": map[string]any{"SmartFilters": map[string]any{"ApproximateGaussian": false}}},
	} {
		response := artboardLiveBridgeRequest(t, engine, "render.artboard", nil, ref(document), options)
		if response.OK || response.Error == nil || response.Error.Code != "unsupported" || len(response.Binaries) != 0 {
			t.Fatalf("independent artboard enabled approximation without permission: %+v", response.Error)
		}
	}
	policy := map[string]any{"Render": map[string]any{
		"SmartFilters": map[string]any{"ApproximateGaussian": true}}}
	pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	pixels.SetNRGBA(0, 0, color.NRGBA{R: 120, G: 120, B: 120, A: 255})
	stack := psdfilter.Stack{Enabled: true, Filters: []psdfilter.Filter{
		{Kind: psdfilter.GaussianBlur, Enabled: true, Radius: .7, Opacity: 1, BlendMode: "norm"},
	}}
	filtered, err := psdfilter.Apply(t.Context(), pixels, stack, psdfilter.Options{ApproximateGaussian: true})
	if err != nil {
		t.Fatal(err)
	}
	pixel := filtered.NRGBAAt(0, 0)
	expected := color.NRGBA{A: 255}
	for index, component := range []byte{pixel.R, pixel.G, pixel.B} {
		value := byte(math.RoundToEven(float64(component)*float64(pixel.A)/255 + 255 - float64(pixel.A)))
		switch index {
		case 0:
			expected.R = value
		case 1:
			expected.G = value
		case 2:
			expected.B = value
		}
	}
	verifyArtboardLiveBridge(t, artboardLiveBridgeRequest(t, engine, "render.artboard", nil,
		ref(document), policy), expected, false)
	for _, render := range []map[string]any{
		{"CachedSpecialLayers": true}, {"CachedTextLayers": true}, {"OutputProfile": []byte{}},
	} {
		response := artboardLiveBridgeRequest(t, engine, "render.artboards", nil, ref(document),
			map[string]any{"Render": render})
		if response.OK || response.Error == nil || len(response.Binaries) != 0 {
			t.Fatal("independent live artboard accepted cached source or unrequested output conversion")
		}
	}
	response := artboardLiveBridgeRequest(t, engine, "render.artboard", nil, ref(document), map[string]any{})
	if response.OK || response.Error == nil || response.Error.Code != "unsupported" {
		t.Fatal("independent artboard retained approximation permission from an earlier request")
	}
}

func artboardLiveBridgeRequest(
	t *testing.T, engine *Engine, operation string, links map[string]string, arguments ...any,
) Response {
	t.Helper()
	request := Request{Operation: operation, Links: links}
	for _, argument := range arguments {
		encoded, err := json.Marshal(argument)
		if err != nil {
			t.Fatal(err)
		}
		request.Args = append(request.Args, encoded)
	}
	return engine.Execute(t.Context(), request, nil)
}

func verifyArtboardLiveBridge(t *testing.T, response Response, expected color.NRGBA, all bool) {
	t.Helper()
	if !response.OK {
		t.Fatalf("independent live artboard failed: %+v", response.Error)
	}
	bridgeVectorOverlayPreview(t, response, expected)
	if all {
		boards := blendingBridgeResult[[]struct {
			LayerIndex int
			Bounds     image.Rectangle
		}](t, response)
		if len(boards) != 1 || boards[0].LayerIndex != 2 || boards[0].Bounds != image.Rect(0, 0, 1, 1) {
			t.Fatalf("all-board output lost physical selection or saved coordinates: %+v", boards)
		}
	}
}

func TestArtboardLivePlacedBridgeEditSaveReopenAndContinue(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, external := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/external%t", version, compression, external), func(t *testing.T) {
					engine, err := New(Config{})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { bridgeCall(t, engine, "system.reset", "") })
					original, contents := bridgeArtboardPlacedFixture(t, version, external)
					asset := bridgeAsset(t, engine, "placed-artboard.psd", original)
					opened := bridgeCall(t, engine, "document.open", "", assetRef(asset)).Data.(map[string]any)
					editor, document := opened["editor"].(string), opened["document"].(string)
					infos := blendingBridgeResult[[]psdedit.SmartObjectInfo](t,
						bridgeCall(t, engine, "editor.SmartObjects", editor))
					if len(infos) != 1 {
						t.Fatal("wire artboard does not contain one live smart object")
					}
					links := map[string]string{"shared-fixture": bridgeAsset(t, engine, "initial-source.psd", contents)}
					options := map[string]any{}
					if external {
						options["Render"] = map[string]any{}
						missing := artboardLiveBridgeRequest(t, engine, "render.artboard", nil, ref(document), options)
						if missing.OK || missing.Error == nil || len(missing.Binaries) != 0 {
							t.Fatal("external artboard used retained source or instance pixels without an explicit mapping")
						}
					}
					verifyArtboardLiveBridge(t, artboardLiveBridgeRequest(t, engine, "render.artboard", links,
						ref(document), options), color.NRGBA{R: 120, G: 120, B: 120, A: 255}, false)
					for _, pixel := range []color.NRGBA{{R: 27, G: 81, B: 219, A: 255}, {R: 231, G: 71, B: 43, A: 255}} {
						pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
						pixels.SetNRGBA(0, 0, pixel)
						var encoded bytes.Buffer
						if err := png.Encode(&encoded, pixels); err != nil {
							t.Fatal(err)
						}
						input := bytes.Clone(encoded.Bytes())
						replacement := bridgeAsset(t, engine, "new-board-source.png", input)
						links["shared-fixture"] = replacement
						if external {
							bridgeSmartCall(t, engine, "UpdateLinkedContents", editor, links, infos[0].ID)
						} else {
							bridgeCall(t, engine, "editor.ReplaceContents", editor, infos[0].ID, assetRef(replacement))
						}
						saved := bridgeCall(t, engine, "editor.WritePSD", editor,
							map[string]any{"Compression": compression})
						if len(saved.Binaries) != 1 {
							t.Fatal("placed artboard did not save without caller composite pixels")
						}
						output := bridgeAsset(t, engine, "placed-artboard-saved.psd", saved.Binaries[0].Data)
						opened = bridgeCall(t, engine, "document.open", "", assetRef(output)).Data.(map[string]any)
						editor, document = opened["editor"].(string), opened["document"].(string)
						if opened["header"].(psdfile.Header).Version != version {
							t.Fatal("saving a live artboard changed the PSD/PSB format")
						}
						for _, operation := range []string{"render.artboard", "render.artboards"} {
							verifyArtboardLiveBridge(t, artboardLiveBridgeRequest(t, engine, operation, links,
								ref(document), options), pixel, operation == "render.artboards")
						}
						blendingBridgePixel(t, bridgeCall(t, engine, "preview.saved", "", ref(document)), pixel)
						bridgeSmartBytes(t, bridgeSmartCall(t, engine, "ReadSmartObjectContents", editor, links,
							infos[0].ID), input)
						if external {
							missing := artboardLiveBridgeRequest(t, engine, "render.artboard", nil, ref(document), options)
							if missing.OK || missing.Error == nil || len(missing.Binaries) != 0 {
								t.Fatal("reopened artboard reused an earlier request's external mapping")
							}
						}
						if !bytes.Equal(input, engine.assets[replacement].data) {
							t.Fatal("live artboard editing mutated imported source bytes")
						}
					}
					if !bytes.Equal(original, engine.assets[asset].data) {
						t.Fatal("live artboard rendering mutated its imported parent document")
					}
				})
			}
		}
	}
}
