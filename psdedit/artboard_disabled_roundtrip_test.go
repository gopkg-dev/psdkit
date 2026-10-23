package psdedit

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func editableDisabledArtboardFixture(t *testing.T, version uint16) []byte {
	t.Helper()
	rect := &psdfile.Descriptor{ClassID: "classFloatRect", Entries: []psdfile.DescriptorEntry{
		{Key: "Left", Type: "doub", Value: float64(0)}, {Key: "Top ", Type: "doub", Value: float64(0)},
		{Key: "Rght", Type: "doub", Value: float64(1)}, {Key: "Btom", Type: "doub", Value: float64(1)},
	}}
	board, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "artboard",
		Entries: []psdfile.DescriptorEntry{{Key: "artboardRect", Type: "Objc", Value: rect},
			{Key: "artboardBackgroundType", Type: "long", Value: int32(1)}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	vector := editVectorPayload()
	binary.BigEndian.PutUint32(vector[4:], 4)
	tags := compositeFixtureTag("artb", board)
	tags = append(tags, compositeFixtureTag("vmsk", vector)...)
	tags = append(tags, compositeFixtureTag("lfx2", effectEditPayload(t, false))...)
	data := compositeFixture(version, 8, 4, []compositeFixtureLayer{
		{divider: 3}, {color: []byte{80, 120, 200, 255}},
		{color: []byte{0, 0, 0, 0}, mask: []byte{0}, divider: 1, blend: "norm", tags: tags},
	})
	passRangeEffectMaster(t, data, false)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	data[source.Layers[2].Mask.Span.Offset+17] |= 2
	point := &psdfile.Descriptor{ClassID: "Pnt ", Entries: []psdfile.DescriptorEntry{
		{Key: "Hrzn", Type: "doub", Value: float64(0)}, {Key: "Vrtc", Type: "doub", Value: float64(0)},
	}}
	document, err := psdfile.EncodeDescriptor(t.Context(), &psdfile.Descriptor{ClassID: "artboard",
		Entries: []psdfile.DescriptorEntry{{Key: "Cnt ", Type: "long", Value: int32(1)},
			{Key: "origin", Type: "Objc", Value: point}, {Key: "autoExpandOffset", Type: "Objc", Value: point}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	global := compositeFixtureTag("artd", document)
	position := source.LayerMaskSection.Offset
	if version == 2 {
		binary.BigEndian.PutUint64(data[position:], binary.BigEndian.Uint64(data[position:])+uint64(len(global)))
	} else {
		binary.BigEndian.PutUint32(data[position:], binary.BigEndian.Uint32(data[position:])+uint32(len(global)))
	}
	result := append(bytes.Clone(data[:source.Composite.Span.Offset]), global...)
	return append(result, data[source.Composite.Span.Offset:]...)
}

func disabledArtboardSavedMetadata(t *testing.T, source *psdfile.Document) [][]byte {
	t.Helper()
	group := source.Layers[2]
	spans := []psdfile.Span{group.MaskData}
	for _, channel := range group.Channels {
		if channel.ID == -2 {
			spans = append(spans, channel.Span)
		}
	}
	blocks := append(append([]psdfile.AdditionalBlock(nil), group.AdditionalInfo...), source.AdditionalInfo...)
	for _, block := range blocks {
		if block.Key == "artb" || block.Key == "artd" || block.Key == "vmsk" || block.Key == "lfx2" {
			spans = append(spans, block.Span)
		}
	}
	result := make([][]byte, len(spans))
	for index, span := range spans {
		data, err := source.ReadSpan(span)
		if err != nil {
			t.Fatal(err)
		}
		result[index] = data
	}
	return result
}

func TestDisabledArtboardEditsSaveAndReopenWithAccurateComposite(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("v%d_c%d", version, compression), func(t *testing.T) {
				data := editableDisabledArtboardFixture(t, version)
				original := bytes.Clone(data)
				editor := compositeEditor(t, data)
				metadata := disabledArtboardSavedMetadata(t, editor.source)
				if _, err := psdscene.RenderArtboard(t.Context(), editor.source); err != nil {
					t.Fatal(err)
				}
				pixels := image.NewNRGBA(image.Rect(0, 0, 1, 1))
				pixels.SetNRGBA(0, 0, color.NRGBA{B: 255, A: 128})
				if err := editor.ReplacePixels(2, pixels); err != nil {
					t.Fatal(err)
				}
				for _, expected := range []color.NRGBA{{R: 127, G: 127, B: 255, A: 255},
					{R: 191, G: 191, B: 255, A: 255}} {
					var automatic bytes.Buffer
					if err := editor.Write(t.Context(), &automatic); err != nil {
						t.Fatalf("disabled artboard could not automatically rebuild its saved preview: %v", err)
					}
					automaticSaved, err := psdfile.Parse(automatic.Bytes())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = automaticSaved.Close() })
					compositeExpect(t, automaticSaved, []byte{expected.R, expected.G, expected.B, expected.A})
					if rendered, err := psdscene.Render(t.Context(), automaticSaved); err != nil ||
						rendered.NRGBAAt(0, 0) != expected {
						t.Fatalf("automatically saved artboard did not reopen with matching live pixels: %v", err)
					}
					preview := image.NewNRGBA(image.Rect(0, 0, 1, 1))
					preview.SetNRGBA(0, 0, expected)
					saved := compositeSave(t, editor, SaveOptions{Compression: compression, CompositeImage: preview})
					if saved.Header.Version != version {
						t.Fatal("save changed PSD/PSB version")
					}
					retained := disabledArtboardSavedMetadata(t, saved)
					if len(retained) != len(metadata) {
						t.Fatal("save removed disabled artboard semantics")
					}
					for index := range metadata {
						if !bytes.Equal(retained[index], metadata[index]) {
							t.Fatal("child edits changed artboard metadata, disabled mask samples or effect payload")
						}
					}
					board, err := psdscene.RenderArtboard(t.Context(), saved)
					if err != nil || board.NRGBAAt(0, 0) != expected {
						t.Fatalf("reopened artboard did not redraw edited child pixels: %v", err)
					}
					sourcePixels, err := psdscene.RenderTile(t.Context(), saved, image.Rect(0, 0, 1, 1))
					if err != nil || !bytes.Equal(sourcePixels.Pix, board.Pix) {
						t.Fatalf("reopened source tile disagrees with independent artboard: %v", err)
					}
					compositeExpect(t, saved, []byte{expected.R, expected.G, expected.B, expected.A})
					reopened, err := New(saved)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = reopened.Close() })
					editor = reopened
					if err := editor.SetOpacity(2, 128); err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Equal(data, original) {
					t.Fatal("artboard edit or save mutated source bytes")
				}
			})
		}
	}
}
