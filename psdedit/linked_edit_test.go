package psdedit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func linkedEditFixture(t *testing.T, parentVersion uint16, recordVersion uint32, key string,
	extraEntries ...[]byte,
) []byte {
	t.Helper()
	pixels := smartEditImage([4]color.NRGBA{{220, 60, 40, 255}, {220, 60, 40, 255},
		{220, 60, 40, 255}, {220, 60, 40, 255}})
	child := smartEditPrototype(1, pixels)
	entry := writerLinkedEntry("liFE", "fixture-id", recordVersion, child)
	for _, extra := range extraEntries {
		entry.data = append(entry.data, extra...)
	}
	lengthWidth := 4
	if parentVersion == 2 && key == "lnk2" {
		lengthWidth = 8
	}
	global := writerLinkedTag("8BIM", key, lengthWidth, entry.data, 4)
	global = append(global, writerLinkedTag("8BIM", "G000", 4, []byte{1, 19, 3}, 4)...)
	var records, payloads []byte
	for index, left := range []int{0, 4} {
		placement := writerLinkedTag("8BIM", "SoLd", 4, smartEditPlacement(t, left, ""), 2)
		record, data := smartEditLayer(parentVersion, "External shared instance", uint32(10+index),
			image.Rect(left, 0, left+2, 2), pixels, placement, index == 1)
		records, payloads = append(records, record...), append(payloads, data...)
	}
	layers := binary.BigEndian.AppendUint16(nil, 2)
	layers = append(layers, records...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	merged := append([]byte{0, 0}, bytes.Repeat([]byte{0}, 6*2*3)...)
	return smartEditDocument(parentVersion, 6, 2, 3, layers, global, merged)
}

func TestEmbedLinkedContentsAllRecordVersionsAndPSDPSB(t *testing.T) {
	for _, parentVersion := range []uint16{1, 2} {
		for recordVersion := uint32(1); recordVersion <= 7; recordVersion++ {
			t.Run(string(rune('0'+parentVersion))+"-"+string(rune('0'+recordVersion)), func(t *testing.T) {
				original := linkedEditFixture(t, parentVersion, recordVersion, "lnkE")
				originalDigest := sha256.Sum256(original)
				editor, source := smartEditOpen(t, original)
				infos, err := editor.SmartObjects(nil)
				if err != nil || len(infos) != 2 || infos[0].Embedded || infos[0].SourceKind != "liFE" {
					t.Fatalf("external source views are incorrect: %v", err)
				}
				before, _ := source.LinkedFiles(nil)
				wanted := smartEditImage(smartEditColors())
				innerVersion := uint16(1 + recordVersion%2)
				encoded := smartEditPrototype(innerVersion, wanted)
				immutable := bytes.Clone(encoded)
				calls := 0
				err = editor.EmbedLinkedContents(nil, infos[0].ID,
					func(ctx context.Context, record psdfile.LinkedFileRecord) ([]byte, error) {
						calls++
						if record.Kind != "liFE" || record.ID != before[0].ID || record.Version != recordVersion {
							t.Fatal("resolver did not receive the explicitly selected source metadata")
						}
						if _, err := editor.Changes(); err != nil {
							t.Fatal(err)
						}
						return encoded, nil
					})
				if err != nil || calls != 1 {
					t.Fatalf("embedding called resolver %d times: %v", calls, err)
				}
				clear(encoded)
				smartEditAssertCaches(t, editor, wanted)
				current, _ := editor.SmartObjects(nil)
				if !current[0].Embedded || !current[1].Embedded || current[0].SourceKind != "liFD" ||
					current[0].SourceID != infos[0].SourceID {
					t.Fatal("embedding did not update all shared source views")
				}
				var output bytes.Buffer
				if err := editor.Write(nil, &output, SaveOptions{Compression: 1}); err != nil {
					t.Fatal(err)
				}
				saved, err := psdfile.Parse(output.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = saved.Close() })
				links, err := saved.LinkedFiles(nil)
				if err != nil || len(links) != 1 {
					t.Fatalf("embedded linked record is unreadable: %v", err)
				}
				after := links[0]
				if after.Kind != "liFD" || after.Block.Key != "lnk2" || after.Version != recordVersion ||
					after.ID != before[0].ID || after.Name != before[0].Name || after.Creator != before[0].Creator ||
					after.ExternalDescriptor.Length != 0 || after.ExternalFileSizeData.Length != 0 || after.Timestamp != nil {
					t.Fatal("external-only fields survived or common link identity was lost")
				}
				if after.FileType != map[uint16]string{1: "8BPS", 2: "8BPB"}[innerVersion] ||
					after.Block.LengthWidth != map[uint16]uint8{1: 4, 2: 8}[parentVersion] {
					t.Fatal("embedded PSD/PSB signature or outer length width is incorrect")
				}
				data, _ := saved.ReadSpan(after.Data)
				if !bytes.Equal(data, immutable) {
					t.Fatal("embedded replacement was truncated or aliases caller bytes")
				}
				for _, spans := range [][2]psdfile.Span{
					{before[0].FileOpenDescriptor, after.FileOpenDescriptor},
					{before[0].ChildDocumentIDData, after.ChildDocumentIDData},
					{before[0].AssetModTimeData, after.AssetModTimeData},
					{before[0].AssetLockedData, after.AssetLockedData},
					{before[0].Tail, after.Tail},
				} {
					old, _ := source.ReadSpan(spans[0])
					value, _ := saved.ReadSpan(spans[1])
					if !bytes.Equal(old, value) {
						t.Fatal("embedding changed common open parameters, versioned fields or unknown tail")
					}
				}
				smartEditAssertPreservedMetadata(t, source, saved)
				if sha256.Sum256(original) != originalDigest {
					t.Fatal("embedding changed immutable external source bytes")
				}
			})
		}
	}
}

func TestEmbedLinkedContentsResolverErrorsCancellationAndConcurrentEdits(t *testing.T) {
	for _, condition := range []string{"error", "cancel", "rename", "close", "source-close", "invalid", "nil"} {
		t.Run(condition, func(t *testing.T) {
			editor, source := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
			infos, _ := editor.SmartObjects(nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callerError := errors.New("resolver unavailable")
			err := editor.EmbedLinkedContents(ctx, infos[0].ID,
				func(ctx context.Context, record psdfile.LinkedFileRecord) ([]byte, error) {
					switch condition {
					case "error":
						return nil, callerError
					case "cancel":
						cancel()
					case "rename":
						if err := editor.Rename(infos[0].ID, "Concurrent name"); err != nil {
							t.Fatal(err)
						}
					case "close":
						_ = editor.Close()
					case "source-close":
						_ = source.Close()
					case "invalid":
						return []byte("bad"), nil
					case "nil":
						return nil, nil
					}
					return smartEditPrototype(1, smartEditImage(smartEditColors())), nil
				})
			if err == nil || len(editor.linkedOverlays) != 0 || editor.linkedRecordsBytes != 0 {
				t.Fatal("failed resolver committed an embedded source")
			}
			if condition == "error" && !errors.Is(err, callerError) ||
				condition == "cancel" && !errors.Is(err, context.Canceled) ||
				condition == "rename" && !errors.Is(err, ErrStaleLinkedEdit) ||
				(condition == "close" || condition == "source-close") && !errors.Is(err, psdfile.ErrClosed) {
				t.Fatalf("resolver failure contract returned %v", err)
			}
			if condition == "rename" {
				actual, _ := editor.Layer(infos[0].ID)
				if actual.Name != "Concurrent name" {
					t.Fatal("failed embedding overwrote the resolver's concurrent edit")
				}
			}
		})
	}
}

func TestEmbeddedExternalOverlaySupportsFurtherReplaceAndIndependentOpenedSource(t *testing.T) {
	editor, source := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
	infos, _ := editor.SmartObjects(nil)
	first := smartEditPrototype(1, smartEditImage(smartEditColors()))
	if err := editor.ReplaceContents(nil, infos[0].ID, first); !errors.Is(err, ErrUnsupported) {
		t.Fatal("legacy ReplaceContents began accepting unembedded external sources")
	}
	if err := editor.EmbedLinkedContents(nil, infos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return first, nil }); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := editor.EmbedLinkedContents(nil, infos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
			called = true
			return first, nil
		}); !errors.Is(err, ErrInvalidEdit) || called {
		t.Fatal("already embedded source invoked an external resolver again")
	}
	pixels := smartDimensionsPixels(3, 2)
	second := smartSizedPrototype(2, image.Pt(3, 2), pixels.Rect, pixels)
	if err := editor.ReplaceContents(nil, infos[1].ID, second); err != nil {
		t.Fatal(err)
	}
	child, err := editor.OpenSmartObjectSource(nil, infos[0].ID, nil)
	if err != nil || child.Header.Width != 3 || child.Header.Height != 2 || child.Header.Version != 2 {
		t.Fatalf("embedded replacement source is not independently readable: %v", err)
	}
	_ = editor.Close()
	_ = source.Close()
	if _, err := child.DecodeComposite(nil); err != nil {
		t.Fatalf("owned embedded source depended on closed editor or parent: %v", err)
	}
	_ = child.Close()
}

func TestExternalEmbedWriterRejectsUntrustedSpansAndKeepsFloatBits(t *testing.T) {
	data := linkedEditFixture(t, 1, 7, "lnkE")
	_, source := smartEditOpen(t, data)
	links, _ := source.LinkedFiles(nil)
	original := links[0]
	for _, mutate := range []func(*psdfile.LinkedFileRecord){
		func(record *psdfile.LinkedFileRecord) { record.ExternalDescriptor.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.DataSizeData.Length = 7 },
		func(record *psdfile.LinkedFileRecord) { record.Body.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.Data.Offset = math.MaxInt64 },
		func(record *psdfile.LinkedFileRecord) { record.ExternalFileSizeData.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.Timestamp = nil },
	} {
		invalid := original
		mutate(&invalid)
		if _, err := writerEmbeddedExternalRecord(invalid, []byte{1}, "8BPS", 1<<20); err == nil {
			t.Fatal("inconsistent external metadata spans were accepted by the writer")
		}
	}
	if original.AssetModTime == nil || math.Float64bits(*original.AssetModTime) != 1<<63 ||
		!slices.Equal(data[original.AssetModTimeData.Offset:original.AssetModTimeData.Offset+8],
			binary.BigEndian.AppendUint64(nil, 1<<63)) {
		t.Fatal("independent external fixture did not retain signed-zero versioned metadata")
	}
}

func linkedInsertGlobal(t *testing.T, data, tags []byte, before bool) []byte {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	position := source.LayerMaskSection.Offset + source.LayerMaskSection.Length
	if before && len(source.AdditionalInfo) != 0 {
		position = source.AdditionalInfo[0].Span.Offset
	}
	output := bytes.Clone(data[:position])
	output = append(output, tags...)
	output = append(output, data[position:]...)
	width := 4
	if source.Header.Version == 2 {
		width = 8
	}
	encoded := writerLinkedLength(nil, source.Header.Version, int(source.LayerMaskSection.Length)-width+len(tags))
	copy(output[source.LayerMaskSection.Offset:], encoded)
	return output
}

func TestEmbedLinkedContentsReusesUniqueEmptyGlobalDestination(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, before := range []bool{false, true} {
			data := linkedEditFixture(t, version, 7, "lnkE")
			width := 4
			if version == 2 {
				width = 8
			}
			data = linkedInsertGlobal(t, data, writerLinkedTag("8BIM", "lnk2", width, nil, 4), before)
			editor, source := smartEditOpen(t, data)
			infos, _ := editor.SmartObjects(nil)
			encoded := smartEditPrototype(1, smartEditImage(smartEditColors()))
			if err := editor.EmbedLinkedContents(nil, infos[0].ID,
				func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return encoded, nil }); err != nil {
				t.Fatal(err)
			}
			var saved bytes.Buffer
			if err := editor.Write(nil, &saved, SaveOptions{Compression: 1}); err != nil {
				t.Fatal(err)
			}
			parsed, err := psdfile.Parse(saved.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer parsed.Close()
			counts := make(map[string]int)
			for _, block := range parsed.AdditionalInfo {
				counts[block.Key]++
				if block.Key == "G000" {
					payload, _ := parsed.ReadSpan(block.Data)
					if !bytes.Equal(payload, []byte{1, 19, 3}) {
						t.Fatal("moving the external source changed an unrelated global block")
					}
				}
			}
			if counts["lnk2"] != 1 || counts["lnkE"] != 0 || counts["G000"] != 1 {
				t.Fatal("conversion did not reuse the unique empty destination and remove the external block")
			}
			links, err := parsed.LinkedFiles(nil)
			if err != nil || len(links) != 1 || links[0].Kind != "liFD" {
				t.Fatalf("moved external source is unreadable: %v", err)
			}
			smartEditAssertPreservedMetadata(t, source, parsed)
		}
	}
}

func TestEmbedLinkedContentsRejectsAmbiguousContainersBeforeResolver(t *testing.T) {
	for _, condition := range []string{"embedded", "empty-duplicates", "source-duplicates", "mixed-external"} {
		t.Run(condition, func(t *testing.T) {
			data := linkedEditFixture(t, 1, 7, "lnkE")
			var payload, tags []byte
			switch condition {
			case "embedded":
				payload = writerLinkedEntry("liFD", "unrelated-source", 7, []byte{1, 2}).data
				tags = writerLinkedTag("8BIM", "lnk2", 4, payload, 4)
			case "empty-duplicates":
				tags = writerLinkedTag("8BIM", "lnk2", 4, nil, 4)
				tags = append(tags, tags...)
			case "source-duplicates":
				payload = writerLinkedEntry("liFE", "fixture-id", 7, nil).data
				tags = writerLinkedTag("8BIM", "lnkE", 4, payload, 4)
			case "mixed-external":
				data = linkedEditFixture(t, 1, 7, "lnkE", writerLinkedEntry("liFE", "unrelated-source", 7, nil).data)
			}
			data = linkedInsertGlobal(t, data, tags, false)
			editor, _ := smartEditOpen(t, data)
			infos, _ := editor.SmartObjects(nil)
			called := false
			err := editor.EmbedLinkedContents(nil, infos[0].ID,
				func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { called = true; return nil, nil })
			if !errors.Is(err, ErrUnsupported) || called || len(editor.linkedOverlays) != 0 {
				t.Fatalf("ambiguous container %s reached its resolver or committed: %v", condition, err)
			}
		})
	}
}

func TestEmbedExternalRecordInStandardContainerPreservesUnrelatedEntry(t *testing.T) {
	for _, key := range []string{"lnk2", "lnkD", "lnk3"} {
		t.Run(key, func(t *testing.T) {
			other := writerLinkedEntry("liFD", "unrelated-source", 7, []byte{1, 13, 9})
			editor, source := smartEditOpen(t, linkedEditFixture(t, 1, 7, key, other.data))
			infos, _ := editor.SmartObjects(nil)
			before, _ := source.LinkedFiles(nil)
			err := editor.EmbedLinkedContents(nil, infos[0].ID,
				func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
					return smartEditPrototype(1, smartEditImage(smartEditColors())), nil
				})
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := editor.Write(nil, &output, SaveOptions{Compression: 1}); err != nil {
				t.Fatal(err)
			}
			saved, err := psdfile.Parse(output.Bytes())
			if err != nil {
				t.Fatal(err)
			}
			defer saved.Close()
			after, err := saved.LinkedFiles(nil)
			if err != nil || len(after) != 2 || after[0].Kind != "liFD" || after[0].Block.Key != key {
				t.Fatalf("external entry in a standard container was not embedded in place: %v", err)
			}
			old, _ := source.ReadSpan(before[1].Span)
			actual, _ := saved.ReadSpan(after[1].Span)
			oldPadding, _ := source.ReadSpan(before[1].Padding)
			actualPadding, _ := saved.ReadSpan(after[1].Padding)
			if !bytes.Equal(old, actual) || !bytes.Equal(oldPadding, actualPadding) {
				t.Fatal("in-place conversion changed the unselected entry or its padding")
			}
		})
	}
}

func linkedProfileFixture(t *testing.T, data, profile []byte) []byte {
	t.Helper()
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	resource := binary.BigEndian.AppendUint16([]byte("8BIM"), 1039)
	resource = append(resource, 0, 0)
	resource = binary.BigEndian.AppendUint32(resource, uint32(len(profile)))
	resource = append(resource, profile...)
	if len(profile)%2 != 0 {
		resource = append(resource, 0)
	}
	output := bytes.Clone(data[:source.ResourceSection.Offset])
	output = binary.BigEndian.AppendUint32(output, uint32(len(resource)))
	output = append(output, resource...)
	return append(output, data[source.ResourceSection.Offset+source.ResourceSection.Length:]...)
}

func TestEmbedLinkedContentsRejectsUntaggedProfileMismatchAtomically(t *testing.T) {
	profile := make([]byte, 132)
	binary.BigEndian.PutUint32(profile, uint32(len(profile)))
	copy(profile[12:24], []byte("mntrRGB XYZ "))
	copy(profile[36:40], []byte("acsp"))
	for _, taggedParent := range []bool{false, true} {
		data := linkedEditFixture(t, 1, 7, "lnkE")
		encoded := smartEditPrototype(1, smartEditImage(smartEditColors()))
		if taggedParent {
			data = linkedProfileFixture(t, data, profile)
		} else {
			encoded = linkedProfileFixture(t, encoded, profile)
		}
		editor, _ := smartEditOpen(t, data)
		infos, _ := editor.SmartObjects(nil)
		before, _ := editor.Changes()
		err := editor.EmbedLinkedContents(nil, infos[0].ID,
			func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return encoded, nil })
		if !errors.Is(err, ErrUnsupported) && !errors.Is(err, psdfile.ErrUnsupported) {
			t.Fatalf("tagged/untagged profile mismatch returned %v", err)
		}
		after, _ := editor.Changes()
		if before != after || len(editor.linkedOverlays) != 0 || editor.roots[0].pixels != nil {
			t.Fatal("profile mismatch committed external source content or projected caches")
		}
	}
}

func TestEmbedLinkedContentsOwnedBudgetAndRepeatedSameTargetSave(t *testing.T) {
	data := linkedEditFixture(t, 1, 7, "lnkE")
	editor, _ := smartEditOpen(t, data, psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 32 << 10}})
	infos, _ := editor.SmartObjects(nil)
	err := editor.EmbedLinkedContents(nil, infos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return make([]byte, (32<<10)+1), nil })
	if !errors.Is(err, psdfile.ErrLimit) || len(editor.linkedOverlays) != 0 {
		t.Fatalf("oversized resolver bytes escaped their owned-content budget: %v", err)
	}
	filename := filepath.Join(t.TempDir(), "external.psd")
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fileEditor, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer fileEditor.Close()
	fileInfos, _ := fileEditor.SmartObjects(nil)
	encoded := smartEditPrototype(1, smartEditImage(smartEditColors()))
	if err := fileEditor.EmbedLinkedContents(nil, fileInfos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return encoded, nil }); err != nil {
		t.Fatal(err)
	}
	var first []byte
	for range 3 {
		if err := fileEditor.Save(nil, filename, SaveOptions{Compression: 1}); err != nil {
			t.Fatal(err)
		}
		actual, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		if first != nil && !bytes.Equal(first, actual) {
			t.Fatal("repeated same-target save changed an embedded source or container layout")
		}
		first = actual
	}
}
