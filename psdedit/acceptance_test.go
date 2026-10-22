package psdedit_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
)

func editAcceptanceSource(t *testing.T, data []byte) *psdfile.Document {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := source.Close(); err != nil {
			t.Error(err)
		}
	})
	return source
}

func editAcceptanceEditor(t *testing.T, source *psdfile.Document) *psdedit.Editor {
	t.Helper()
	editor, err := psdedit.New(source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := editor.Close(); err != nil {
			t.Error(err)
		}
	})
	return editor
}

func editAcceptanceFirst(t *testing.T, editor *psdedit.Editor) psdedit.LayerInfo {
	t.Helper()
	layers, err := editor.Layers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) == 0 {
		t.Fatal("fixture contains no editable layer")
	}
	return layers[0]
}

func editAcceptanceWrite(t *testing.T, editor *psdedit.Editor, options ...psdedit.SaveOptions) []byte {
	t.Helper()
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output, options...); err != nil {
		t.Fatal(err)
	}
	return bytes.Clone(output.Bytes())
}

func editAcceptanceBytes(t *testing.T, source *psdfile.Document, span psdfile.Span) []byte {
	t.Helper()
	var result bytes.Buffer
	if _, err := source.CopySpan(context.Background(), &result, span); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func editAcceptanceEqualSpan(
	t *testing.T, original, reopened *psdfile.Document, before, after psdfile.Span, label string,
) {
	t.Helper()
	if !bytes.Equal(editAcceptanceBytes(t, original, before), editAcceptanceBytes(t, reopened, after)) {
		t.Fatalf("unchanged %s bytes differ", label)
	}
}

func editAcceptancePreserved(
	t *testing.T, original, reopened *psdfile.Document, beforeIndex, afterIndex int,
) {
	t.Helper()
	before, after := original.Layers[beforeIndex], reopened.Layers[afterIndex]
	if before.Bounds != after.Bounds || before.BlendMode != after.BlendMode ||
		before.Clipping != after.Clipping || before.Opacity != after.Opacity || before.Flags != after.Flags {
		t.Fatal("rename changed visual source metadata")
	}
	editAcceptanceEqualSpan(t, original, reopened, before.MaskData, after.MaskData, "layer mask")
	editAcceptanceEqualSpan(t, original, reopened, before.BlendRanges, after.BlendRanges, "blend ranges")
	if len(before.Channels) != len(after.Channels) {
		t.Fatal("rename changed channel count")
	}
	for index, channel := range before.Channels {
		if channel.ID != after.Channels[index].ID || channel.Compression != after.Channels[index].Compression {
			t.Fatal("rename changed channel identifiers or encoding")
		}
		editAcceptanceEqualSpan(t, original, reopened, channel.Span, after.Channels[index].Span, "layer channel")
	}
	if len(before.AdditionalInfo) != len(after.AdditionalInfo) {
		t.Fatal("rename lost or added original layer metadata")
	}
	for index, block := range before.AdditionalInfo {
		other := after.AdditionalInfo[index]
		if block.Key != other.Key || block.Signature != other.Signature || block.LengthWidth != other.LengthWidth {
			t.Fatal("rename changed ordered additional-block identities")
		}
		if block.Key != "luni" {
			editAcceptanceEqualSpan(t, original, reopened, block.Span, other.Span, "layer additional block")
		}
	}
}

func TestP2AcceptanceNoEditIsByteIdentical(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, precision := range []struct {
			depth     uint16
			alternate string
		}{{1, ""}, {8, ""}, {16, "Lr16"}, {32, "Lr32"}} {
			t.Run(fmt.Sprintf("version%d_depth%d", version, precision.depth), func(t *testing.T) {
				data := editAcceptanceDocument(editAcceptanceFixtureOptions{
					version: version, depth: precision.depth, advanced: true, alternate: precision.alternate,
				})
				source := editAcceptanceSource(t, data)
				editor := editAcceptanceEditor(t, source)
				if !bytes.Equal(editAcceptanceWrite(t, editor), data) {
					t.Fatal("unedited output must preserve every source byte")
				}
				changes, err := editor.Changes()
				if err != nil || changes.Revision != 0 || changes.Dirty || changes.CompositeChanged {
					t.Fatalf("no-edit write changed tracking: %+v, %v", changes, err)
				}
			})
		}
	}
}

func TestP2AcceptanceRenamePreservesRawBlocksAndRepeatedSave(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{
				version: version, advanced: true, count: 2,
			}))
			editor := editAcceptanceEditor(t, source)
			layer := editAcceptanceFirst(t, editor)
			for _, name := range []string{"新", "中文名称😀长度变化足以重排后续数据", ""} {
				if err := editor.Rename(layer.ID, name); err != nil {
					t.Fatal(err)
				}
				output := editAcceptanceWrite(t, editor)
				reopened := editAcceptanceSource(t, output)
				if reopened.Layers[layer.SourceIndex].Name != name {
					t.Fatal("saved Unicode name did not survive independent source reindexing")
				}
				editAcceptanceEqualSpan(t, source, reopened, source.HeaderData, reopened.HeaderData, "header")
				editAcceptanceEqualSpan(t, source, reopened, source.ResourceSection, reopened.ResourceSection, "resources")
				editAcceptanceEqualSpan(t, source, reopened, source.GlobalMask, reopened.GlobalMask, "global mask")
				editAcceptanceEqualSpan(t, source, reopened, source.Composite.Span, reopened.Composite.Span, "preview")
				if len(source.AdditionalInfo) != len(reopened.AdditionalInfo) {
					t.Fatal("rename changed global additional-block count")
				}
				for index, block := range source.AdditionalInfo {
					editAcceptanceEqualSpan(t, source, reopened, block.Span,
						reopened.AdditionalInfo[index].Span, "global additional/linked payload")
				}
				for index := range source.Layers {
					editAcceptancePreserved(t, source, reopened, index, index)
				}
				if !bytes.Equal(editAcceptanceWrite(t, editor), output) {
					t.Fatal("repeat save reverted edits after Dirty became false")
				}
				changes, err := editor.Changes()
				if err != nil || changes.Dirty || changes.Revision != changes.SavedRevision || changes.CompositeChanged {
					t.Fatalf("rename tracking is inconsistent: %+v, %v", changes, err)
				}
				if source.Layers[layer.SourceIndex].Name == name {
					t.Fatal("edit mutated the immutable source metadata")
				}
			}
		})
	}
}

func editAcceptanceComposite(t *testing.T, source *psdfile.Document) *psdfile.CompositePixels {
	t.Helper()
	pixels, err := source.DecodeComposite(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return pixels
}

func editAcceptanceMatte(value, alpha byte) byte {
	return byte((int(value)*int(alpha) + 255*(255-int(alpha)) + 127) / 255)
}

func TestP2AcceptanceRGBAEditsUpdatePlanesAndWhiteMattePreview(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			t.Run(fmt.Sprintf("version%d_compression%d", version, compression), func(t *testing.T) {
				source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{version: version}))
				editor := editAcceptanceEditor(t, source)
				layer := editAcceptanceFirst(t, editor)
				input := editAcceptanceImage()
				expected := bytes.Clone(input.Pix)
				if err := editor.ReplacePixels(layer.ID, input); err != nil {
					t.Fatal(err)
				}
				input.Pix[0] ^= 255
				reopened := editAcceptanceSource(t, editAcceptanceWrite(t, editor, psdedit.SaveOptions{
					Compression: compression,
				}))
				decoded, err := reopened.DecodeLayer(context.Background(), 0)
				if err != nil {
					t.Fatal(err)
				}
				if len(decoded.Channels) != 4 {
					t.Fatal("RGBA edit must retain four independently decoded channels")
				}
				for _, plane := range decoded.Channels {
					channel := int(plane.ID)
					if plane.ID == -1 {
						channel = 3
					}
					for pixel, value := range plane.Data {
						if value != expected[pixel*4+channel] {
							t.Fatalf("edited source sample differs at pixel %d channel %d", pixel, channel)
						}
					}
				}
				composite := editAcceptanceComposite(t, reopened)
				if !reopened.GlobalAlpha || len(composite.Channels) != 4 {
					t.Fatal("rebuilt preview lost merged transparency metadata")
				}
				for pixel := range 4 {
					alpha := expected[pixel*4+3]
					for channel := range 3 {
						want := editAcceptanceMatte(expected[pixel*4+channel], alpha)
						if composite.Channels[channel].Data[pixel] != want {
							t.Fatalf("preview white matte differs at pixel %d channel %d", pixel, channel)
						}
					}
					if composite.Channels[3].Data[pixel] != alpha {
						t.Fatal("preview alpha differs from independently calculated source alpha")
					}
				}
				if !bytes.Equal(editAcceptanceWrite(t, editor, psdedit.SaveOptions{Compression: compression}),
					editAcceptanceWrite(t, editor, psdedit.SaveOptions{Compression: compression})) {
					t.Fatal("saved pixel overlays were lost on subsequent writes")
				}
				changes, err := editor.Changes()
				if err != nil || changes.Dirty || !changes.CompositeChanged {
					t.Fatalf("pixel edit tracking is inconsistent: %+v, %v", changes, err)
				}
			})
		}
	}
}

func TestP2AcceptanceVisibleAndOpacityUpdatePreview(t *testing.T) {
	for _, hide := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidden_%t", hide), func(t *testing.T) {
			source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{}))
			editor := editAcceptanceEditor(t, source)
			layer := editAcceptanceFirst(t, editor)
			alpha := byte(128)
			if hide {
				alpha = 0
				if err := editor.SetVisible(layer.ID, false); err != nil {
					t.Fatal(err)
				}
			} else if err := editor.SetOpacity(layer.ID, alpha); err != nil {
				t.Fatal(err)
			}
			reopened := editAcceptanceSource(t, editAcceptanceWrite(t, editor))
			if hide && reopened.Layers[0].Flags&2 == 0 {
				t.Fatal("saved layer visibility flag was not changed")
			}
			if !hide && reopened.Layers[0].Opacity != alpha {
				t.Fatal("saved layer opacity was not changed")
			}
			for index, channel := range source.Layers[0].Channels {
				editAcceptanceEqualSpan(t, source, reopened, channel.Span,
					reopened.Layers[0].Channels[index].Span, "visibility/opacity channel")
			}
			composite := editAcceptanceComposite(t, reopened)
			for pixel := range 4 {
				for channel := range 3 {
					want := editAcceptanceMatte(byte(40+channel*40), alpha)
					if composite.Channels[channel].Data[pixel] != want {
						t.Fatal("visibility/opacity edit saved an incorrect or stale preview")
					}
				}
				if composite.Channels[3].Data[pixel] != alpha {
					t.Fatal("visibility/opacity preview alpha differs")
				}
			}
		})
	}
}

func TestP2AcceptanceTreeEditsStableHandlesUniquePSDIDsAndSeed(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{version: version}))
			editor := editAcceptanceEditor(t, source)
			original := editAcceptanceFirst(t, editor)
			group, err := editor.AddGroup(0, 0, "外组", true)
			if err != nil {
				t.Fatal(err)
			}
			raster, err := editor.AddRaster(group, 0, "新像素", editAcceptanceImage(), image.Pt(-1, 1))
			if err != nil {
				t.Fatal(err)
			}
			inner, err := editor.AddGroup(group, 1, "内组", false)
			if err != nil {
				t.Fatal(err)
			}
			if err := editor.Move(original.ID, inner, 0); err != nil {
				t.Fatal(err)
			}
			tree, err := editor.Tree()
			if err != nil || len(tree) != 1 || tree[0].ID != group || len(tree[0].Children) != 2 ||
				tree[0].Children[0].ID != raster || tree[0].Children[1].ID != inner ||
				len(tree[0].Children[1].Children) != 1 || tree[0].Children[1].Children[0].ID != original.ID {
				t.Fatalf("nested bottom-to-top tree is incorrect: %+v, %v", tree, err)
			}
			tree[0].Children[0].Name = "caller mutation"
			view, err := editor.Layer(raster)
			if err != nil || view.Name != "新像素" || view.Parent != group || view.Bounds != image.Rect(-1, 1, 1, 3) {
				t.Fatalf("metadata view is borrowed or has incorrect geometry: %+v, %v", view, err)
			}
			before, err := editor.Changes()
			if err != nil {
				t.Fatal(err)
			}
			if err := editor.Move(group, inner, 0); !errors.Is(err, psdedit.ErrInvalidEdit) {
				t.Fatalf("moving an ancestor into its descendant must fail: %v", err)
			}
			after, err := editor.Changes()
			if err != nil || after != before {
				t.Fatal("rejected cyclic move changed the revision")
			}
			if err := editor.Move(original.ID, 0, 1); err != nil {
				t.Fatal(err)
			}
			descendant, err := editor.AddRaster(inner, 0, "随组删除", editAcceptanceImage(), image.Point{})
			if err != nil {
				t.Fatal(err)
			}
			descendantInfo, err := editor.Layer(descendant)
			if err != nil {
				t.Fatal(err)
			}
			if err := editor.Remove(inner); err != nil {
				t.Fatal(err)
			}
			if _, err := editor.Layer(inner); !errors.Is(err, psdedit.ErrInvalidEdit) {
				t.Fatalf("removed group handle remains usable: %v", err)
			}
			if _, err := editor.Layer(descendant); !errors.Is(err, psdedit.ErrInvalidEdit) {
				t.Fatalf("removed group's descendant handle remains usable: %v", err)
			}
			replacement, err := editor.AddRaster(0, 2, "不复用旧标识", editAcceptanceImage(), image.Point{})
			if err != nil || replacement <= descendant {
				t.Fatalf("new node reused a removed stable handle: %v", err)
			}
			replacementInfo, err := editor.Layer(replacement)
			if err != nil || replacementInfo.PSDID <= descendantInfo.PSDID {
				t.Fatalf("new node reused a removed PSD identifier: %v", err)
			}
			if err := editor.Remove(replacement); err != nil {
				t.Fatal(err)
			}
			if err := editor.Rename(original.ID, "原层稳定"); err != nil {
				t.Fatal(err)
			}
			output := editAcceptanceWrite(t, editor)
			reopened := editAcceptanceSource(t, output)
			openedEditor := editAcceptanceEditor(t, reopened)
			result, err := openedEditor.Tree()
			if err != nil || len(result) != 2 || result[0].Name != "外组" || !result[0].Group ||
				!result[0].Expanded || len(result[0].Children) != 1 || result[0].Children[0].Name != "新像素" ||
				result[1].Name != "原层稳定" || result[1].Group {
				t.Fatalf("saved editable hierarchy does not match the edit: %+v, %v", result, err)
			}
			seen := make(map[uint32]bool)
			var highest uint32
			for _, layer := range reopened.Layers {
				if !layer.HasID || layer.ID == 0 || seen[layer.ID] {
					t.Fatal("saved group markers and raster layers need unique nonzero PSD IDs")
				}
				seen[layer.ID] = true
				highest = max(highest, layer.ID)
				if layer.Name != "原层稳定" && layer.ID < 100 {
					t.Fatal("new PSD ID ignored the original Layer ID Seed resource")
				}
			}
			seedSeen := false
			for _, resource := range reopened.Resources {
				if resource.ID == 1024 || resource.ID == 1026 {
					t.Fatal("saved tree retained stale UI layer-index resources")
				}
				if resource.ID == 1044 {
					seedSeen = true
					seed := binary.BigEndian.Uint32(editAcceptanceBytes(t, reopened, resource.Data))
					if seed <= highest || seed <= replacementInfo.PSDID {
						t.Fatal("saved Layer ID Seed does not exceed allocated PSD IDs")
					}
				}
			}
			if !seedSeen {
				t.Fatal("saved structure lost its Layer ID Seed")
			}
			if !bytes.Equal(output, editAcceptanceWrite(t, editor)) {
				t.Fatal("saved hierarchy reverted on repeat write")
			}
		})
	}
}

func TestP2AcceptanceHighPrecisionMetadataAndExplicitComposite(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, precision := range []struct {
			depth     uint16
			alternate string
		}{{1, ""}, {16, "Lr16"}, {32, "Lr32"}} {
			t.Run(fmt.Sprintf("version%d_depth%d", version, precision.depth), func(t *testing.T) {
				source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{
					version: version, depth: precision.depth, alternate: precision.alternate,
				}))
				editor := editAcceptanceEditor(t, source)
				layer := editAcceptanceFirst(t, editor)
				if err := editor.Rename(layer.ID, "高位深保持原精度"); err != nil {
					t.Fatal(err)
				}
				renamed := editAcceptanceSource(t, editAcceptanceWrite(t, editor))
				if renamed.Header != source.Header || renamed.Layers[0].Name != "高位深保持原精度" {
					t.Fatal("metadata save normalized original header or name")
				}
				for index, channel := range source.Layers[0].Channels {
					editAcceptanceEqualSpan(t, source, renamed, channel.Span,
						renamed.Layers[0].Channels[index].Span, "high-precision source channel")
				}
				plane, err := source.DecodeChannel(context.Background(), 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				plane.Data[0] ^= 0x40
				expected := bytes.Clone(plane.Data)
				if err := editor.ReplaceChannel(layer.ID, plane.ID, *plane); err != nil {
					t.Fatal(err)
				}
				plane.Data[0] ^= 0xff
				var stale bytes.Buffer
				if err := editor.Write(context.Background(), &stale); !errors.Is(err, psdedit.ErrUnsupported) {
					t.Fatalf("source-precision visual edit must not silently reuse a stale preview: %v", err)
				}
				if stale.Len() != 0 {
					t.Fatal("unsupported automatic preview wrote partial output before validation")
				}
				preview := editAcceptanceComposite(t, source)
				preview.Channels[0].Data = bytes.Clone(expected)
				output := editAcceptanceWrite(t, editor, psdedit.SaveOptions{Composite: preview, Compression: 2})
				reopened := editAcceptanceSource(t, output)
				decoded, err := reopened.DecodeChannel(context.Background(), 0, 0)
				if err != nil || decoded.Depth != precision.depth || !bytes.Equal(decoded.Data, expected) {
					t.Fatalf("source-precision channel edit was normalized: %v", err)
				}
				merged := editAcceptanceComposite(t, reopened)
				if merged.Depth != precision.depth || !bytes.Equal(merged.Channels[0].Data, expected) {
					t.Fatal("explicit preview did not retain original sample precision")
				}
			})
		}
	}
}

func TestP2AcceptanceAdvancedRasterAndUnsupportedPreviewAreExplicit(t *testing.T) {
	source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{
		advanced: true, textCache: true,
	}))
	editor := editAcceptanceEditor(t, source)
	layer := editAcceptanceFirst(t, editor)
	if err := editor.ReplacePixels(layer.ID, editAcceptanceImage()); !errors.Is(err, psdedit.ErrUnsupported) {
		t.Fatalf("text cached pixels must not masquerade as editable raster content: %v", err)
	}
	plane, err := source.DecodeChannel(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.ReplaceChannel(layer.ID, plane.ID, *plane); !errors.Is(err, psdedit.ErrUnsupported) {
		t.Fatalf("text channel replacement must be explicit unsupported: %v", err)
	}
	if err := editor.SetOpacity(layer.ID, 128); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(context.Background(), &output); !errors.Is(err, psdedit.ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("advanced visual edit emitted stale or partial preview: output=%d err=%v", output.Len(), err)
	}
	reopened := editAcceptanceSource(t, editAcceptanceWrite(t, editor,
		psdedit.SaveOptions{CompositeImage: image.NewNRGBA(image.Rect(0, 0, 2, 2))}))
	if reopened.Layers[0].Opacity != 128 {
		t.Fatal("explicit preview fallback did not save the metadata edit")
	}
	for index, channel := range source.Layers[0].Channels {
		editAcceptanceEqualSpan(t, source, reopened, channel.Span, reopened.Layers[0].Channels[index].Span,
			"advanced cached/source channels")
	}
	for index, block := range source.AdditionalInfo {
		editAcceptanceEqualSpan(t, source, reopened, block.Span, reopened.AdditionalInfo[index].Span, "linked source")
	}
}

func TestP2AcceptanceOrdinaryRasterReplacementPreservesMasksAndExtraChannels(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprintf("version%d", version), func(t *testing.T) {
			source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{
				version: version, advanced: true,
			}))
			editor := editAcceptanceEditor(t, source)
			layer := editAcceptanceFirst(t, editor)
			pixels := editAcceptanceImage()
			if err := editor.ReplacePixels(layer.ID, pixels); err != nil {
				t.Fatal(err)
			}
			var automatic bytes.Buffer
			if err := editor.Write(context.Background(), &automatic); !errors.Is(err, psdedit.ErrUnsupported) {
				t.Fatalf("spot channels need an explicit preview capability decision: %v", err)
			}
			reopened := editAcceptanceSource(t, editAcceptanceWrite(t, editor, psdedit.SaveOptions{
				Compression: 2, CompositeImage: pixels,
			}))
			before, after := source.Layers[0], reopened.Layers[0]
			editAcceptanceEqualSpan(t, source, reopened, before.MaskData, after.MaskData, "mask geometry/padding")
			editAcceptanceEqualSpan(t, source, reopened, before.BlendRanges, after.BlendRanges, "blend ranges")
			if len(before.Channels) != len(after.Channels) {
				t.Fatal("RGBA edit discarded original mask/spot channels")
			}
			for index, channel := range before.Channels {
				if channel.ID != after.Channels[index].ID {
					t.Fatal("RGBA edit reordered source channels")
				}
				if channel.ID == -2 || channel.ID > 2 {
					editAcceptanceEqualSpan(t, source, reopened, channel.Span, after.Channels[index].Span,
						"unchanged mask/spot channel")
				}
			}
		})
	}
}

func TestP2AcceptanceAmbiguousLayerSetsRejectMutation(t *testing.T) {
	source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{
		depth: 16, alternate: "Lr16", parallel: true,
	}))
	editor := editAcceptanceEditor(t, source)
	layer := editAcceptanceFirst(t, editor)
	if err := editor.Rename(layer.ID, "ambiguous"); !errors.Is(err, psdedit.ErrUnsupported) {
		t.Fatalf("parallel populated layer sets require explicit unsupported result: %v", err)
	}
	changes, err := editor.Changes()
	if err != nil || changes.Revision != 0 {
		t.Fatal("rejected alternate-set edit changed tracking")
	}
}

func TestP2AcceptanceUnknownHierarchyMetadataRejectsStructuralEdit(t *testing.T) {
	data := editAcceptanceDocument(editAcceptanceFixtureOptions{unknownDivider: true})
	source := editAcceptanceSource(t, data)
	editor, err := psdedit.New(source)
	if errors.Is(err, psdedit.ErrUnsupported) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = editor.Close() })
	if !bytes.Equal(editAcceptanceWrite(t, editor), data) {
		t.Fatal("unknown hierarchy metadata lost in an unedited document")
	}
	group, err := editor.AddGroup(0, 0, "unknown hierarchy must not be interpreted", true)
	if errors.Is(err, psdedit.ErrUnsupported) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if group == 0 {
		t.Fatal("structural edit returned an invalid handle")
	}
	var output bytes.Buffer
	err = editor.Write(context.Background(), &output, psdedit.SaveOptions{
		CompositeImage: image.NewNRGBA(image.Rect(0, 0, 2, 2)),
	})
	if !errors.Is(err, psdedit.ErrUnsupported) || output.Len() != 0 {
		t.Fatalf("unknown divider was silently treated as an ordinary layer: output=%d err=%v", output.Len(), err)
	}
}

type editAcceptanceFailWriter struct{ err error }

func (writer editAcceptanceFailWriter) Write([]byte) (int, error) { return 0, writer.err }

type editAcceptanceShortWriter struct{}

func (editAcceptanceShortWriter) Write(data []byte) (int, error) { return len(data) / 2, nil }

type editAcceptanceCancelWriter struct {
	bytes.Buffer
	cancel context.CancelFunc
}

func (writer *editAcceptanceCancelWriter) Write(data []byte) (int, error) {
	count, err := writer.Buffer.Write(data)
	writer.cancel()
	return count, err
}

func TestP2AcceptanceFailedSavesDoNotCommitOrDamageDestination(t *testing.T) {
	source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{}))
	editor := editAcceptanceEditor(t, source)
	layer := editAcceptanceFirst(t, editor)
	if err := editor.Rename(layer.ID, "only successful output marks saved"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("deliberate acceptance writer failure")
	if err := editor.Write(context.Background(), editAcceptanceFailWriter{err: failure}); !errors.Is(err, failure) {
		t.Fatalf("write failure lost original cause: %v", err)
	}
	if err := editor.Write(context.Background(), editAcceptanceShortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short writer was accepted as a complete save: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer := &editAcceptanceCancelWriter{cancel: cancel}
	if err := editor.Write(ctx, writer); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation did not abort save: %v", err)
	}
	cancel()
	directory := t.TempDir()
	filename := filepath.Join(directory, "existing.psd")
	original := []byte("existing destination must remain intact")
	if err := os.WriteFile(filename, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := editor.Save(ctx, filename); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled save was accepted: %v", err)
	}
	limitErr := editor.Save(context.Background(), filename, psdedit.SaveOptions{MaxOutputBytes: 10})
	if !errors.Is(limitErr, psdfile.ErrLimit) {
		t.Fatalf("output budget violation was accepted: %v", limitErr)
	}
	if err := editor.Save(context.Background(), directory); !errors.Is(err, psdedit.ErrInvalidEdit) {
		t.Fatalf("directory destination was accepted: %v", err)
	}
	if err := editor.Save(context.Background(), filepath.Join(directory, "missing", "out.psd")); err == nil {
		t.Fatal("missing destination parent was accepted")
	}
	remaining, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(remaining, original) {
		t.Fatal("failed atomic save damaged the existing destination")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "existing.psd" {
		t.Fatalf("failed atomic save left temporary files: count=%d, err=%v", len(entries), err)
	}
	changes, err := editor.Changes()
	if err != nil || !changes.Dirty || changes.Revision != 1 || changes.SavedRevision != 0 {
		t.Fatalf("failed output committed revision: %+v, %v", changes, err)
	}
	if err := editor.Save(context.Background(), filename); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filename)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("atomic replacement lost existing file permissions")
	}
	saved, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	reopened := editAcceptanceSource(t, saved)
	if reopened.Layers[0].Name != "only successful output marks saved" {
		t.Fatal("successful Save did not persist the current overlay")
	}
	changes, err = editor.Changes()
	if err != nil || changes.Dirty || changes.SavedRevision != changes.Revision {
		t.Fatalf("successful atomic output did not commit revision: %+v, %v", changes, err)
	}
}

func TestP2AcceptanceSourceAndEditorLifecycle(t *testing.T) {
	data := editAcceptanceDocument(editAcceptanceFixtureOptions{})
	source := editAcceptanceSource(t, data)
	editor := editAcceptanceEditor(t, source)
	if err := editor.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := editor.Layers(); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed editor returned mutable metadata: %v", err)
	}
	if _, err := source.ReadSpan(source.HeaderData); err != nil {
		t.Fatalf("closing a borrowing editor closed its source: %v", err)
	}
	other := editAcceptanceEditor(t, source)
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Write(context.Background(), io.Discard); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("saving from a closed source was accepted: %v", err)
	}
	if _, err := other.Changes(); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source still returned editable metadata: %v", err)
	}
	filename := filepath.Join(t.TempDir(), "owned.psd")
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	owned, err := psdedit.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := owned.Close(); err != nil {
		t.Fatal(err)
	}
	if err := owned.Write(context.Background(), io.Discard); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("owned editor remained writable after Close: %v", err)
	}
}

func TestP2AcceptanceConcurrentEditAndSave(t *testing.T) {
	source := editAcceptanceSource(t, editAcceptanceDocument(editAcceptanceFixtureOptions{}))
	editor := editAcceptanceEditor(t, source)
	layer := editAcceptanceFirst(t, editor)
	var workers sync.WaitGroup
	workerErrors := make(chan error, 2)
	workers.Add(2)
	go func() {
		defer workers.Done()
		for iteration := range 12 {
			if err := editor.Rename(layer.ID, fmt.Sprintf("revision %d", iteration)); err != nil {
				workerErrors <- err
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for range 12 {
			var output bytes.Buffer
			if err := editor.Write(context.Background(), &output); err != nil {
				workerErrors <- err
				return
			}
			reopened, err := psdfile.Parse(output.Bytes())
			if err != nil {
				workerErrors <- err
				return
			}
			if err := reopened.Close(); err != nil {
				workerErrors <- err
				return
			}
		}
	}()
	workers.Wait()
	close(workerErrors)
	for err := range workerErrors {
		t.Error(err)
	}
	reopened := editAcceptanceSource(t, editAcceptanceWrite(t, editor))
	if reopened.Layers[0].Name != "revision 11" {
		t.Fatal("concurrent saves lost the final edit")
	}
}

func editAcceptanceDigest(t *testing.T, source *psdfile.Document, span psdfile.Span) []byte {
	t.Helper()
	digest := sha256.New()
	if _, err := source.CopySpan(context.Background(), digest, span); err != nil {
		t.Fatal(err)
	}
	return digest.Sum(nil)
}

func TestP2AcceptanceRealSampleRenamePreservesChannelAndLinkedDigests(t *testing.T) {
	filename := testfixture.Sample(t)
	if _, err := os.Stat(filename); errors.Is(err, os.ErrNotExist) {
		t.Skip("repository sample is unavailable")
	}
	source, err := psdfile.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	editor := editAcceptanceEditor(t, source)
	layers, err := editor.Layers()
	if err != nil || len(layers) == 0 {
		t.Fatalf("real sample has no layer metadata: %v", err)
	}
	selected := slices.IndexFunc(layers, func(layer psdedit.LayerInfo) bool { return !layer.Group })
	if selected < 0 {
		t.Fatal("real sample contains no ordinary node to rename")
	}
	name := "P2 验收名称修改"
	if err := editor.Rename(layers[selected].ID, name); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "renamed.psd")
	if err := editor.Save(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	reopened, err := psdfile.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if len(reopened.Layers) != len(source.Layers) ||
		reopened.Layers[layers[selected].SourceIndex].Name != name {
		t.Fatal("real sample saved layer count/name differs")
	}
	for index, layer := range source.Layers {
		if len(layer.Channels) != len(reopened.Layers[index].Channels) {
			t.Fatal("real sample channel count changed")
		}
		for channelIndex, channel := range layer.Channels {
			if !bytes.Equal(editAcceptanceDigest(t, source, channel.Span),
				editAcceptanceDigest(t, reopened, reopened.Layers[index].Channels[channelIndex].Span)) {
				t.Fatalf("real sample encoded channel digest differs: layer=%d channel=%d", index, channelIndex)
			}
		}
	}
	if len(source.AdditionalInfo) != len(reopened.AdditionalInfo) {
		t.Fatal("real sample global block count changed")
	}
	linked := 0
	for index, block := range source.AdditionalInfo {
		if block.Key == "lnk2" || block.Key == "lnkD" || block.Key == "lnk3" || block.Key == "lnkE" {
			linked++
		}
		if !bytes.Equal(editAcceptanceDigest(t, source, block.Span),
			editAcceptanceDigest(t, reopened, reopened.AdditionalInfo[index].Span)) {
			t.Fatal("real sample global/linked block digest differs")
		}
	}
	if linked == 0 {
		t.Fatal("real sample test did not exercise an embedded linked-object payload")
	}
	if !bytes.Equal(editAcceptanceDigest(t, source, source.Composite.Span),
		editAcceptanceDigest(t, reopened, reopened.Composite.Span)) {
		t.Fatal("real sample metadata-only edit changed encoded composite")
	}
}
