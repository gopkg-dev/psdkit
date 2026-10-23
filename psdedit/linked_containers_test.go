package psdedit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
)

func linkedContainerFixture(t *testing.T, version uint16, local bool, tags []byte, identities ...string) []byte {
	global := tags
	var localTags map[int][]byte
	if local {
		global = nil
		localTags = map[int][]byte{0: tags}
	}
	return linkedContainerScopesFixture(t, version, global, localTags, identities...)
}

func linkedContainerScopesFixture(
	t *testing.T, version uint16, global []byte, localTags map[int][]byte, identities ...string,
) []byte {
	t.Helper()
	pixels := smartEditImage(smartEditColors())
	var records, payloads []byte
	for index, identity := range identities {
		left := index * 4
		placement := bytes.Replace(smartEditPlacement(t, left, ""),
			writerLinkedUnicode("fixture-id\x00"), writerLinkedUnicode(identity+"\x00"), 1)
		layerTags := writerLinkedTag("8BIM", "SoLd", 4, placement, 2)
		layerTags = append(layerTags, localTags[index]...)
		record, data := smartEditLayer(version, fmt.Sprintf("Instance %d", index), uint32(index+20),
			image.Rect(left, 0, left+2, 2), pixels, layerTags, false)
		records, payloads = append(records, record...), append(payloads, data...)
	}
	layers := binary.BigEndian.AppendUint16(nil, uint16(len(identities)))
	layers = append(layers, records...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	width := len(identities)*4 - 2
	merged := append([]byte{0, 0}, bytes.Repeat([]byte{0}, width*2*3)...)
	return smartEditDocument(version, width, 2, 3, layers, global, merged)
}

func linkedContainerTag(version uint16, local bool, key string, payload []byte) []byte {
	width, alignment := 4, 4
	if local {
		alignment = 2
	}
	if version == 2 && key == "lnk2" {
		width = 8
	}
	return writerLinkedTag("8BIM", key, width, payload, alignment)
}

func linkedContainerRecords(t *testing.T, source *psdfile.Document) map[string]psdfile.LinkedFileRecord {
	t.Helper()
	records, err := source.LinkedFiles(nil)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string]psdfile.LinkedFileRecord, len(records))
	for _, record := range records {
		if _, exists := result[record.ID]; exists {
			t.Fatal("saving generated a duplicate source record")
		}
		result[record.ID] = record
	}
	return result
}

func linkedContainerAssertRaw(t *testing.T, before, after *psdfile.Document, identity string) {
	t.Helper()
	first, firstOK := linkedContainerRecords(t, before)[identity]
	second, secondOK := linkedContainerRecords(t, after)[identity]
	if !firstOK || !secondOK {
		t.Fatal("an unrelated linked source disappeared")
	}
	old, oldErr := before.ReadSpan(psdfile.Span{Offset: first.Span.Offset,
		Length: first.Span.Length + first.Padding.Length})
	actual, actualErr := after.ReadSpan(psdfile.Span{Offset: second.Span.Offset,
		Length: second.Span.Length + second.Padding.Length})
	if oldErr != nil || actualErr != nil || !bytes.Equal(old, actual) {
		t.Fatal("unselected record bytes or record padding changed")
	}
}

func linkedContainerAssertCaches(t *testing.T, source *psdfile.Document, identity string, wanted *image.NRGBA) {
	t.Helper()
	placements, err := source.SmartObjects(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, placement := range placements {
		if placement.LayerSetIndex != source.ActiveLayerSet || placement.SourceID != identity {
			continue
		}
		decoded, err := source.DecodeLayer(nil, placement.LayerIndex)
		if err != nil || len(decoded.Channels) < 4 {
			t.Fatalf("saved shared cache is unreadable: %v", err)
		}
		for _, plane := range decoded.Channels {
			component := int(plane.ID)
			if component == -1 {
				component = 3
			}
			if component < 0 || component > 3 {
				continue
			}
			for sample, value := range plane.Data {
				if value != wanted.Pix[sample*4+component] {
					t.Fatal("saved shared cache does not contain the selected source pixels")
				}
			}
		}
	}
}

func TestLinkedContainerMigrationMixedSourcesAndDeterministicTargets(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, local := range []bool{false, true} {
			for _, destination := range []string{"none", "empty", "occupied", "multiple-empty", "multiple-occupied"} {
				t.Run(fmt.Sprintf("v%d-local%t-%s", version, local, destination), func(t *testing.T) {
					alpha := writerLinkedEntry("liFE", "alpha", 7, nil).data
					alpha = append(alpha, writerLinkedEntry("liFE", "keep-external", 7, []byte{3, 1, 4}).data...)
					beta := writerLinkedEntry("liFE", "beta", 3, nil).data
					beta = append(beta, writerLinkedEntry("liFA", "keep-alias", 7, nil).data...)
					tags := linkedContainerTag(version, local, "lnkE", alpha)
					kept := []string{"keep-external", "keep-alias"}
					if destination != "none" {
						var contents []byte
						if destination == "occupied" || destination == "multiple-occupied" {
							contents = writerLinkedEntry("liFD", "keep-embedded", 6, []byte{2, 7, 1}).data
							kept = append(kept, "keep-embedded")
						}
						tags = append(tags, linkedContainerTag(version, local, "lnk2", contents)...)
					}
					tags = append(tags, linkedContainerTag(version, local, "lnkE", beta)...)
					if destination == "multiple-empty" || destination == "multiple-occupied" {
						var contents []byte
						if destination == "multiple-occupied" {
							contents = writerLinkedEntry("liFD", "keep-second", 5, []byte{9, 2}).data
							kept = append(kept, "keep-second")
						}
						tags = append(tags, linkedContainerTag(version, local, "lnk2", contents)...)
					}
					original := linkedContainerFixture(t, version, local, tags, "alpha", "beta", "alpha", "beta")
					digest := sha256.Sum256(original)
					editor, source := smartEditOpen(t, original)
					infos, err := editor.SmartObjects(nil)
					if err != nil || len(infos) != 4 {
						t.Fatalf("source placements: %v", err)
					}
					wanted := smartEditImage(smartEditColors())
					encoded := smartEditPrototype(2, wanted)
					for phase, identity := range []string{"alpha", "beta"} {
						if err := editor.EmbedLinkedContents(nil, infos[phase].ID,
							func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return encoded, nil }); err != nil {
							t.Fatal(err)
						}
						for compression := range uint16(4) {
							var output bytes.Buffer
							if err := editor.Write(nil, &output, SaveOptions{Compression: compression}); err != nil {
								t.Fatal(err)
							}
							saved, err := psdfile.Parse(output.Bytes())
							if err != nil {
								t.Fatal(err)
							}
							after := linkedContainerRecords(t, saved)
							if len(after) != len(kept)+2 {
								t.Fatal("a source was lost during container migration")
							}
							for _, preserved := range kept {
								linkedContainerAssertRaw(t, source, saved, preserved)
							}
							if phase == 0 {
								linkedContainerAssertRaw(t, source, saved, "beta")
							} else if !sameLinkedBlock(after["alpha"].Block, after["beta"].Block) {
								t.Fatal("successive embeddings did not reuse their scope's first destination")
							}
							record := after[identity]
							actual, err := saved.ReadSpan(record.Data)
							if err != nil || record.Kind != "liFD" || record.Block.Key != "lnk2" ||
								!bytes.Equal(actual, encoded) || record.Block.LengthWidth != uint8(versionLengthWidth(version)) {
								t.Fatalf("embedded record or container length is incorrect: %v", err)
							}
							scope, ok := linkedContainerScope(saved, record.Block)
							if !ok || (scope.layerSet >= 0) != local {
								t.Fatal("embedding changed the source container scope")
							}
							count, first := 0, psdfile.AdditionalBlock{}
							for _, block := range scope.blocks {
								if block.Key == "lnk2" {
									if count == 0 {
										first = block
									}
									count++
								}
							}
							wantedCount := 1
							if destination == "multiple-empty" || destination == "multiple-occupied" {
								wantedCount = 2
							}
							if count != wantedCount || !sameLinkedBlock(first, record.Block) {
								t.Fatal("migration created an extra destination or ignored the first existing container")
							}
							linkedContainerAssertCaches(t, saved, identity, wanted)
							_ = saved.Close()
						}
					}
					if sha256.Sum256(original) != digest {
						t.Fatal("migration changed immutable input bytes")
					}
				})
			}
		}
	}
}

func TestLinkedContainerUpdateEmbedReplaceDiskRoundTrip(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			original := linkedEditFixture(t, version, 7, "lnkE",
				writerLinkedEntry("liFE", "untouched", 5, []byte{8, 7, 6}).data)
			width := versionLengthWidth(version)
			original = linkedInsertGlobal(t, original, writerLinkedTag("8BIM", "lnk2", int(width),
				writerLinkedEntry("liFD", "existing", 7, []byte{5, 4, 3}).data, 4), true)
			directory := t.TempDir()
			input, output := filepath.Join(directory, "original.psd"), filepath.Join(directory, "saved.psd")
			if err := os.WriteFile(input, original, 0o600); err != nil {
				t.Fatal(err)
			}
			editor, err := Open(input)
			if err != nil {
				t.Fatal(err)
			}
			defer editor.Close()
			infos, _ := editor.SmartObjects(nil)
			first := smartEditPrototype(1, smartEditImage(smartEditColors()))
			if err := editor.UpdateLinkedContents(nil, infos[0].ID,
				func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
					return LinkedContentsUpdate{Data: first,
						Timestamp: &psdfile.LinkedFileTimestamp{Year: 2024, Month: 2, Day: 29}}, nil
				}); err != nil {
				t.Fatal(err)
			}
			if err := editor.EmbedLinkedContents(nil, infos[1].ID,
				func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return first, nil }); err != nil {
				t.Fatal(err)
			}
			pixels := smartEditImage(smartEditColors())
			pixels.Pix[0], pixels.Pix[5] = 231, 17
			last := smartEditPrototype(2, pixels)
			if err := editor.ReplaceContents(nil, infos[0].ID, last); err != nil {
				t.Fatal(err)
			}
			var previous []byte
			for range 2 {
				if err := editor.Save(nil, output, SaveOptions{Compression: 3}); err != nil {
					t.Fatal(err)
				}
				actual, err := os.ReadFile(output)
				if err != nil || previous != nil && !bytes.Equal(previous, actual) {
					t.Fatalf("repeat save changed migrated containers: %v", err)
				}
				previous = actual
			}
			reopened, err := Open(output)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			after := linkedContainerRecords(t, reopened.source)
			record := after["fixture-id"]
			data, err := reopened.source.ReadSpan(record.Data)
			if err != nil || record.Kind != "liFD" || record.Timestamp != nil || record.ExternalDescriptor.Length != 0 ||
				!bytes.Equal(data, last) {
				t.Fatalf("final source did not survive disk reopen: %v", err)
			}
			linkedContainerAssertCaches(t, reopened.source, "fixture-id", pixels)
			for _, identity := range []string{"untouched", "existing"} {
				linkedContainerAssertRaw(t, editor.source, reopened.source, identity)
			}
			unaltered, err := os.ReadFile(input)
			if err != nil || !bytes.Equal(unaltered, original) {
				t.Fatal("saving modified the independent original file")
			}
			freshInfos, _ := reopened.SmartObjects(nil)
			if err := reopened.ReplaceContents(nil, freshInfos[1].ID, first); err != nil {
				t.Fatal("reopened embedded source could not be edited again", err)
			}
			if err := reopened.Save(nil, output); err != nil {
				t.Fatal("reopened embedded edit could not replace its own saved file", err)
			}
		})
	}
}

func TestLinkedContainerMigrationFailuresAreAtomic(t *testing.T) {
	for _, failure := range []string{"cancel", "budget"} {
		t.Run(failure, func(t *testing.T) {
			original := linkedEditFixture(t, 1, 7, "lnkE",
				writerLinkedEntry("liFE", "untouched", 7, nil).data)
			editor, _ := smartEditOpen(t, original,
				psdfile.LoadOptions{Limits: psdfile.Limits{MaxDecodeBytes: 128 << 10}})
			infos, _ := editor.SmartObjects(nil)
			before, _ := editor.Changes()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := editor.EmbedLinkedContents(ctx, infos[0].ID,
				func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
					if failure == "cancel" {
						cancel()
						return smartEditPrototype(1, smartEditImage(smartEditColors())), nil
					}
					return make([]byte, 129<<10), nil
				})
			after, _ := editor.Changes()
			if err == nil || before != after || len(editor.linkedOverlays) != 0 || len(editor.linkedRecords) != 0 {
				t.Fatal("failed migration committed editor state")
			}
			var output bytes.Buffer
			if err := editor.Write(nil, &output); err != nil || !bytes.Equal(output.Bytes(), original) {
				t.Fatal("failed migration changed later lossless output")
			}
		})
	}
	editor, _ := smartEditOpen(t, linkedEditFixture(t, 1, 7, "lnkE"))
	infos, _ := editor.SmartObjects(nil)
	if err := editor.EmbedLinkedContents(nil, infos[0].ID,
		func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
			return smartEditPrototype(1, smartEditImage(smartEditColors())), nil
		}); err != nil {
		t.Fatal(err)
	}
	before, _ := editor.Changes()
	var output bytes.Buffer
	if err := editor.Write(nil, &output, SaveOptions{MaxOutputBytes: 32}); !errors.Is(err, psdfile.ErrLimit) ||
		output.Len() != 0 {
		t.Fatalf("failed write leaked partial output: %v", err)
	}
	after, _ := editor.Changes()
	if before != after {
		t.Fatal("failed write changed revisions")
	}
}

func TestLinkedContainerOwnerRemovalPreservesSharedSource(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, operation := range []string{"preserve", "preserve-external", "replace", "update", "embed"} {
			t.Run(fmt.Sprintf("v%d-%s", version, operation), func(t *testing.T) {
				kind, key := "liFD", "lnk2"
				if operation == "preserve-external" || operation == "update" || operation == "embed" {
					kind, key = "liFE", "lnkE"
				}
				wanted := smartEditImage(smartEditColors())
				encoded := smartEditPrototype(1, wanted)
				tags := linkedContainerTag(version, true, key, writerLinkedEntry(kind, "shared", 7, encoded).data)
				original := linkedContainerFixture(t, version, true, tags, "shared", "shared")
				editor, source := smartEditOpen(t, original)
				infos, _ := editor.SmartObjects(nil)
				if err := editor.Remove(infos[0].ID); err != nil {
					t.Fatal(err)
				}
				wanted.Pix[0] = 247
				replacement := smartEditPrototype(2, wanted)
				var editErr error
				switch operation {
				case "replace":
					editErr = editor.ReplaceContents(nil, infos[1].ID, replacement)
				case "update":
					editErr = editor.UpdateLinkedContents(nil, infos[1].ID,
						func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
							return LinkedContentsUpdate{Data: replacement}, nil
						})
				case "embed":
					editErr = editor.EmbedLinkedContents(nil, infos[1].ID,
						func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) { return replacement, nil })
				}
				if editErr != nil {
					t.Fatal(editErr)
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
				records := linkedContainerRecords(t, saved)
				record, exists := records["shared"]
				if !exists || len(records) != 1 || len(saved.Layers) != 1 {
					t.Fatal("deleting the source owner left a dangling shared placement")
				}
				scope, ok := linkedContainerScope(saved, record.Block)
				if !ok || scope.layerSet >= 0 {
					t.Fatal("orphaned source was not retained in a document-level container")
				}
				if operation == "preserve" || operation == "preserve-external" {
					linkedContainerAssertRaw(t, source, saved, "shared")
					return
				}
				data, err := saved.ReadSpan(record.Data)
				if err != nil || !bytes.Equal(data, replacement) || (record.Kind == "liFE") != (operation == "update") {
					t.Fatalf("relocation lost source update or external kind: %v", err)
				}
				linkedContainerAssertCaches(t, saved, "shared", wanted)
			})
		}
	}
}

func TestLinkedContainerMigrationKeepsLayerScopesIndependent(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			global := linkedContainerTag(version, false, "lnk2",
				writerLinkedEntry("liFD", "keep-global", 7, []byte{3, 1}).data)
			localTags := make(map[int][]byte)
			for index, identity := range []string{"alpha", "beta"} {
				tags := linkedContainerTag(version, true, "lnkE", writerLinkedEntry("liFE", identity, 7, nil).data)
				tags = append(tags, linkedContainerTag(version, true, "lnk2",
					writerLinkedEntry("liFD", "keep-"+identity, 7, []byte{8, 2, 4}).data)...)
				localTags[index] = tags
			}
			editor, source := smartEditOpen(t, linkedContainerScopesFixture(t, version,
				global, localTags, "alpha", "beta", "alpha", "beta"))
			infos, _ := editor.SmartObjects(nil)
			for _, info := range infos[:2] {
				if err := editor.EmbedLinkedContents(nil, info.ID,
					func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
						return smartEditPrototype(1, smartEditImage(smartEditColors())), nil
					}); err != nil {
					t.Fatal(err)
				}
			}
			for _, removeOwner := range []bool{false, true} {
				if removeOwner {
					if err := editor.Remove(infos[1].ID); err != nil {
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
				records := linkedContainerRecords(t, saved)
				alpha, _ := linkedContainerScope(saved, records["alpha"].Block)
				beta, _ := linkedContainerScope(saved, records["beta"].Block)
				if alpha.layer != 0 || alpha.layerSet < 0 ||
					!removeOwner && (beta.layer != 1 || beta.layerSet < 0) || removeOwner && beta.layerSet >= 0 {
					t.Fatal("independent layer scopes merged or deleted owner contents disappeared")
				}
				for _, identity := range []string{"keep-global", "keep-alpha", "keep-beta"} {
					linkedContainerAssertRaw(t, source, saved, identity)
				}
				_ = saved.Close()
			}
		})
	}
}
