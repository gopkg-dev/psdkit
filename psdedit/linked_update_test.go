package psdedit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"image"
	"math"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func TestUpdateLinkedContentsVersionsPreserveExternalRecordsAndSharedCaches(t *testing.T) {
	for _, parentVersion := range []uint16{1, 2} {
		for version := uint32(1); version <= 7; version++ {
			t.Run(fmt.Sprintf("parent%d-record%d", parentVersion, version), func(t *testing.T) {
				unrelated := writerLinkedEntry("liFE", "other-reference", 7, []byte{7, 9, 1})
				original := linkedEditFixture(t, parentVersion, version, "lnkE", unrelated.data)
				beforeDigest := sha256.Sum256(original)
				editor, source := smartEditOpen(t, original)
				infos, err := editor.SmartObjects(nil)
				if err != nil {
					t.Fatal(err)
				}
				before, _ := source.LinkedFiles(nil)
				wanted := smartEditImage(smartEditColors())
				encoded := smartEditPrototype(2, wanted)
				immutable := bytes.Clone(encoded)
				timestamp := &psdfile.LinkedFileTimestamp{Year: 2024, Month: 2, Day: 29,
					Hour: 23, Minute: 59, Seconds: 58.125}
				if version < 4 {
					timestamp = nil
				}
				calls := 0
				err = editor.UpdateLinkedContents(nil, infos[0].ID,
					func(ctx context.Context, selected psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
						calls++
						if selected.Kind != "liFE" || selected.ID != before[0].ID {
							t.Fatal("resolver did not receive the selected external record")
						}
						if _, err := editor.Changes(); err != nil {
							t.Fatal("resolver ran while holding the editor lock")
						}
						return LinkedContentsUpdate{Data: encoded, Timestamp: timestamp}, nil
					})
				if err != nil || calls != 1 {
					t.Fatalf("external update failed: calls=%d error=%v", calls, err)
				}
				clear(encoded)
				if timestamp != nil {
					timestamp.Year = 1999
				}
				smartEditAssertCaches(t, editor, wanted)
				current, err := editor.SmartObjects(nil)
				if err != nil || current[0].Embedded || current[1].Embedded || current[0].SourceKind != "liFE" {
					t.Fatal("external update changed shared source type")
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
				if err != nil || len(after) != 2 || after[0].Kind != "liFE" || after[0].Block.Key != "lnkE" ||
					after[0].Version != version || after[0].ID != before[0].ID || after[0].Name != before[0].Name ||
					after[0].FileType != "8BPB" || after[0].ExternalFileSize != uint64(len(immutable)) {
					t.Fatalf("external type, identity or file size was lost: %v", err)
				}
				if version == 1 {
					if after[0].HasData || after[0].DataSize != 0 {
						t.Fatal("version one began storing a data cache")
					}
				} else {
					data, err := saved.ReadSpan(after[0].Data)
					if err != nil || !bytes.Equal(data, immutable) {
						t.Fatalf("external cache was lost or aliases caller input: %v", err)
					}
				}
				if version >= 4 && (after[0].Timestamp.Year != 2024 || after[0].Timestamp.Seconds != 58.125) {
					t.Fatal("timestamp was not copied into the external record")
				}
				for _, pair := range [][2]psdfile.Span{
					{before[0].ExternalDescriptor, after[0].ExternalDescriptor},
					{before[0].FileOpenDescriptor, after[0].FileOpenDescriptor},
					{before[0].ChildDocumentIDData, after[0].ChildDocumentIDData},
					{before[0].AssetModTimeData, after[0].AssetModTimeData},
					{before[0].AssetLockedData, after[0].AssetLockedData},
					{before[0].Tail, after[0].Tail}, {before[1].Span, after[1].Span},
				} {
					old, err := source.ReadSpan(pair[0])
					data, nextErr := saved.ReadSpan(pair[1])
					if err != nil || nextErr != nil || !bytes.Equal(old, data) {
						t.Fatal("path, open settings, versioned fields or unrelated record changed")
					}
				}
				smartEditAssertPreservedMetadata(t, source, saved)
				if sha256.Sum256(original) != beforeDigest {
					t.Fatal("external refresh modified the immutable source")
				}
				child, err := editor.OpenSmartObjectSource(nil, infos[0].ID, nil)
				if err != nil || child.Header.Version != 2 {
					t.Fatalf("updated source could not be opened independently: %v", err)
				}
				_ = editor.Close()
				_ = source.Close()
				if _, err := child.DecodeComposite(nil); err != nil {
					t.Fatal("updated child aliases the parent lifetime")
				}
				_ = child.Close()
			})
		}
	}
}

func TestUpdateLinkedContentsFailuresCommitNothing(t *testing.T) {
	for _, condition := range []string{"resolver", "cancel", "rename", "close", "source-close", "bad-data", "timestamp"} {
		t.Run(condition, func(t *testing.T) {
			editor, source := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
			infos, _ := editor.SmartObjects(nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			callerError := errors.New("controlled resolver failure")
			err := editor.UpdateLinkedContents(ctx, infos[0].ID,
				func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
					update := LinkedContentsUpdate{Data: smartEditPrototype(1, smartEditImage(smartEditColors()))}
					switch condition {
					case "resolver":
						return LinkedContentsUpdate{}, callerError
					case "cancel":
						cancel()
					case "rename":
						_ = editor.Rename(infos[0].ID, "concurrent rename")
					case "close":
						_ = editor.Close()
					case "source-close":
						_ = source.Close()
					case "bad-data":
						update.Data = []byte("not PSD")
					case "timestamp":
						update.Timestamp = &psdfile.LinkedFileTimestamp{Year: 2023, Month: 2, Day: 29}
					}
					return update, nil
				})
			if err == nil || len(editor.linkedOverlays) != 0 || editor.linkedRecordsBytes != 0 {
				t.Fatal("failed update committed some linked state")
			}
			if condition == "resolver" && !errors.Is(err, callerError) ||
				condition == "cancel" && !errors.Is(err, context.Canceled) ||
				condition == "rename" && !errors.Is(err, ErrStaleLinkedEdit) ||
				(condition == "close" || condition == "source-close") && !errors.Is(err, psdfile.ErrClosed) ||
				condition == "timestamp" && !errors.Is(err, ErrInvalidEdit) {
				t.Fatalf("failure cause was lost: %v", err)
			}
		})
	}
}

func TestUpdateLinkedContentsTimestampValidationAndExplicitOperation(t *testing.T) {
	for _, timestamp := range []psdfile.LinkedFileTimestamp{
		{}, {Year: 2024, Month: 13, Day: 1}, {Year: 2024, Month: 4, Day: 31},
		{Year: 2024, Month: 1, Day: 1, Seconds: math.NaN()},
		{Year: 2024, Month: 1, Day: 1, Hour: 24}, {Year: 2024, Month: 1, Day: 1, Seconds: 60},
	} {
		if validLinkedTimestamp(timestamp) {
			t.Fatal("invalid calendar fields were accepted")
		}
	}
	editor, _ := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
	infos, _ := editor.SmartObjects(nil)
	if err := editor.UpdateLinkedContents(nil, infos[0].ID, nil); !errors.Is(err, ErrInvalidEdit) {
		t.Fatal("nil resolver did not fail")
	}
	for range 2 {
		if err := editor.UpdateLinkedContents(nil, infos[0].ID,
			func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
				return LinkedContentsUpdate{Data: smartEditPrototype(1, smartEditImage(smartEditColors()))}, nil
			}); err != nil {
			t.Fatal(err)
		}
	}
	if err := editor.ReplaceContents(nil, infos[0].ID,
		smartEditPrototype(1, smartEditImage(smartEditColors()))); !errors.Is(err, ErrUnsupported) {
		t.Fatal("embedded-only ReplaceContents silently accepted an external record")
	}
	if err := editor.EmbedLinkedContents(nil, infos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
			return smartEditPrototype(1, smartEditImage(smartEditColors())), nil
		}); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := editor.UpdateLinkedContents(nil, infos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
			called = true
			return LinkedContentsUpdate{}, nil
		}); !errors.Is(err, ErrInvalidEdit) || called {
		t.Fatal("converted embedded source invoked the external resolver")
	}
}

func TestUpdateLinkedContentsDimensionsBudgetAndDuplicateIdentity(t *testing.T) {
	original := linkedEditFixture(t, 2, 7, "lnkE")
	editor, source := smartEditOpen(t, original)
	infos, _ := editor.SmartObjects(nil)
	pixels := smartDimensionsPixels(3, 2)
	replacement := smartSizedPrototype(1, image.Pt(3, 2), pixels.Rect, pixels)
	if err := editor.UpdateLinkedContents(nil, infos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
			return LinkedContentsUpdate{Data: replacement}, nil
		}); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := editor.Write(nil, &output, SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	before, _ := source.SmartObjects(nil)
	after, err := saved.SmartObjects(nil)
	if err != nil || len(after) != 2 {
		t.Fatalf("updated placements were unreadable: %v", err)
	}
	for index, placement := range after {
		if placement.Width != 3 || placement.Height != 2 || placement.SourceID != before[index].SourceID ||
			fmt.Sprint(placement.Transform) != fmt.Sprint(before[index].Transform) {
			t.Fatal("dimension refresh did not preserve corners and shared identities")
		}
	}
	links, _ := saved.LinkedFiles(nil)
	if links[0].Kind != "liFE" || links[0].ExternalFileSize != uint64(len(replacement)) {
		t.Fatal("dimension refresh changed external type or failed file-size update")
	}
	limited, _ := smartEditOpen(t, original,
		psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 32 << 10}})
	limitedInfos, _ := limited.SmartObjects(nil)
	beforeChange, _ := limited.Changes()
	err = limited.UpdateLinkedContents(nil, limitedInfos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
			return LinkedContentsUpdate{Data: make([]byte, (32<<10)+1)}, nil
		})
	afterChange, _ := limited.Changes()
	if !errors.Is(err, psdfile.ErrLimit) || beforeChange != afterChange || len(limited.linkedOverlays) != 0 {
		t.Fatalf("oversized external update escaped its budget or partially committed: %v", err)
	}
	duplicate := writerLinkedEntry("liFE", "fixture-id", 7, []byte{1})
	ambiguous, _ := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE", duplicate.data))
	ambiguousInfos, _ := ambiguous.SmartObjects(nil)
	called := false
	err = ambiguous.UpdateLinkedContents(nil, ambiguousInfos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
			called = true
			return LinkedContentsUpdate{}, nil
		})
	if !errors.Is(err, ErrUnsupported) || called {
		t.Fatal("ambiguous source invoked the external resolver")
	}
}

func TestExternalLinkedUpdateWriterRejectsInconsistentSourceSpans(t *testing.T) {
	_, source := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
	links, _ := source.LinkedFiles(nil)
	for _, mutate := range []func(*psdfile.LinkedFileRecord){
		func(record *psdfile.LinkedFileRecord) { record.ExternalDescriptor.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.ExternalFileSizeData.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.DataSizeData.Length = 7 },
		func(record *psdfile.LinkedFileRecord) { record.Data.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.Data.Length++ },
		func(record *psdfile.LinkedFileRecord) { record.FileOpenFlagData.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.Body.Offset++ },
		func(record *psdfile.LinkedFileRecord) { record.Timestamp = nil },
	} {
		invalid := links[0]
		mutate(&invalid)
		if _, err := writerExternalLinkedRecord(invalid,
			&linkedReplacement{data: []byte{1, 2}, fileType: "8BPS", retainExternal: true}, 1<<20); err == nil {
			t.Fatal("inconsistent external spans were accepted")
		}
	}
}

func TestExternalLinkedUpdatePreservesNativeZeroLengthCachePolicy(t *testing.T) {
	for version := uint32(1); version <= 7; version++ {
		entry := writerLinkedEntry("liFE", "fixture-id", version, nil)
		editor, block := writerLinkedEditor(t, writerLinkedDocument(1, true, entry.data))
		editor.linkedOverlays = map[string]*linkedReplacement{"fixture-id": {
			data: []byte{1, 2, 3}, fileType: "8BPS", retainExternal: true,
		}}
		plan, err := editor.writeLinkedBlockLocked(block, true, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		output := writerLinkedEmit(t, editor.source, plan)
		original, err := editor.source.ReadSpan(block.Span)
		if err != nil || len(output) != len(original) {
			t.Fatal("native empty external cache gained encoded contents")
		}
		links, _ := editor.source.LinkedFiles(nil)
		allowed := make(map[int]bool)
		for offset := links[0].ExternalFileSizeData.Offset; offset < links[0].ExternalFileSizeData.Offset+8; offset++ {
			allowed[int(offset-block.Span.Offset)] = true
		}
		for index := range output {
			if !allowed[index] && output[index] != original[index] {
				t.Fatal("empty-cache update changed a field other than explicit external file size")
			}
		}
	}
}

func TestExternalLinkedUpdateNilTimestampRetainsEarlierEditorValue(t *testing.T) {
	editor, _ := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
	infos, _ := editor.SmartObjects(nil)
	timestamp := &psdfile.LinkedFileTimestamp{Year: 2024, Month: 3, Day: 5, Seconds: 7.25}
	for _, supplied := range []*psdfile.LinkedFileTimestamp{timestamp, nil} {
		if err := editor.UpdateLinkedContents(nil, infos[0].ID,
			func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
				return LinkedContentsUpdate{
					Data: smartEditPrototype(1, smartEditImage(smartEditColors())), Timestamp: supplied,
				}, nil
			}); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	if err := editor.Write(nil, &output); err != nil {
		t.Fatal(err)
	}
	saved, err := psdfile.Parse(output.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	links, _ := saved.LinkedFiles(nil)
	if links[0].Timestamp.Year != 2024 || links[0].Timestamp.Seconds != 7.25 {
		t.Fatal("nil timestamp discarded the earlier controlled update")
	}
}
