package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func artboardLivePlacedGroup(t *testing.T, name string, divider uint32, bounds image.Rectangle, kind int32) []byte {
	t.Helper()
	record := make([]byte, 18)
	record = append(record, []byte("8BIMnorm")...)
	record = append(record, 255, 0, 0, 0)
	extra := make([]byte, 8)
	extra = append(extra, byte(len(name)))
	extra = append(extra, name...)
	extra = append(extra, make([]byte, (4-(len(name)+1)%4)%4)...)
	payload := binary.BigEndian.AppendUint32(nil, divider)
	payload = append(payload, []byte("8BIMnorm")...)
	extra = append(extra, compositeFixtureTag("lsct", payload)...)
	if divider == 1 {
		extra = append(extra, compositeFixtureTag("artb", artboardAutoDescriptor(t, bounds, kind))...)
		extra = append(extra, compositeFixtureTag("pRsv", []byte{31, 7, 91})...)
	}
	record = binary.BigEndian.AppendUint32(record, uint32(len(extra)))
	return append(record, extra...)
}

func artboardLivePlacedFixture(t *testing.T, version uint16, external bool) ([]byte, []byte, *image.NRGBA) {
	t.Helper()
	artwork := smartEditImage(smartEditColors())
	contents := smartEditPrototype(version, artwork)
	kind := "liFD"
	if external {
		kind = "liFE"
	}
	linked := writerLinkedEntry(kind, "fixture-id", 7, contents)
	if version == 2 {
		copy(linked.data[linked.sizeOffset:linked.sizeOffset+4], "8BPB")
	}
	width := 4
	if version == 2 {
		width = 8
	}
	global := writerLinkedTag("8BIM", "lnk2", width, linked.data, 4)
	global = append(global, compositeFixtureTag("artd", artboardAutoDocumentDescriptor(t, 2, 9))...)
	cache := smartEditImage([4]color.NRGBA{
		{R: 250, A: 255}, {R: 250, A: 255}, {R: 250, A: 255}, {R: 250, A: 255},
	})
	var records, samples []byte
	for index, name := range []string{
		"end-left", "left-object", "left-board", "end-right", "right-object", "right-board", "outside-overlay",
	} {
		if index == 0 || index == 2 || index == 3 || index == 5 {
			divider, bounds, background := uint32(3), image.Rectangle{}, int32(1)
			if index == 2 {
				divider, bounds = 1, image.Rect(0, 0, 3, 2)
			}
			if index == 5 {
				divider, bounds, background = 1, image.Rect(4, 0, 7, 2), 2
			}
			records = append(records, artboardLivePlacedGroup(t, name, divider, bounds, background)...)
			continue
		}
		left, bounds, pixels := -1, image.Rect(-1, 0, 1, 2), cache
		if index == 4 {
			left, bounds = 4, image.Rect(4, 0, 6, 2)
		}
		var tags []byte
		if index == 6 {
			bounds = image.Rect(0, 0, 7, 2)
			pixels = image.NewNRGBA(bounds)
			for y := range 2 {
				pixels.SetNRGBA(0, y, color.NRGBA{R: 255, B: 255, A: 255})
				pixels.SetNRGBA(3, y, color.NRGBA{R: 255, B: 255, A: 255})
			}
		} else {
			tags = writerLinkedTag("8BIM", "SoLd", 4, smartEditPlacement(t, left, ""), 2)
		}
		record, planes := smartEditLayer(version, name, uint32(index+10), bounds, pixels, tags, false)
		records, samples = append(records, record...), append(samples, planes...)
	}
	layers := binary.BigEndian.AppendUint16(nil, 0xfff9)
	layers = append(layers, records...)
	layers = append(layers, samples...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	footer := append([]byte{0, 0}, bytes.Repeat([]byte{17}, 7*2*4)...)
	return smartEditDocument(version, 7, 2, 4, layers, global, footer), contents, artwork
}

func artboardLivePlacedExpected(artwork *image.NRGBA, opacity byte) (*image.NRGBA, [2]*image.NRGBA) {
	white, black := color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBA{A: 255}
	parent := image.NewNRGBA(image.Rect(0, 0, 7, 2))
	boards := [2]*image.NRGBA{image.NewNRGBA(image.Rect(0, 0, 3, 2)), image.NewNRGBA(image.Rect(0, 0, 3, 2))}
	for y := range 2 {
		for x := range 3 {
			left := white
			if x == 0 {
				left = artwork.NRGBAAt(1, y)
				left.R = byte((int(left.R)*int(opacity) + 255*(255-int(opacity)) + 127) / 255)
				left.G = byte((int(left.G)*int(opacity) + 255*(255-int(opacity)) + 127) / 255)
				left.B = byte((int(left.B)*int(opacity) + 255*(255-int(opacity)) + 127) / 255)
			}
			right := black
			if x < 2 {
				right = artwork.NRGBAAt(x, y)
			}
			boards[0].SetNRGBA(x, y, left)
			boards[1].SetNRGBA(x, y, right)
			parent.SetNRGBA(x, y, left)
			parent.SetNRGBA(x+4, y, right)
		}
		parent.SetNRGBA(0, y, color.NRGBA{R: 255, B: 255, A: 255})
		parent.SetNRGBA(3, y, color.NRGBA{R: 255, B: 255, A: 255})
	}
	return parent, boards
}

func artboardLivePlacedAssert(
	t *testing.T, source *psdfile.Document, expected [2]*image.NRGBA, render *psdscene.Options,
) {
	t.Helper()
	options := psdscene.ArtboardRenderOptions{Render: render}
	boards, err := psdscene.RenderArtboards(t.Context(), source, options)
	if err != nil || len(boards) != 2 {
		t.Fatalf("independent live artboard set failed: %v", err)
	}
	for index, board := range boards {
		wantedBounds := image.Rect(0, 0, 3, 2)
		if index == 1 {
			wantedBounds = wantedBounds.Add(image.Pt(4, 0))
		}
		if board.Bounds != wantedBounds || board.Pixels.Rect != expected[index].Rect ||
			!bytes.Equal(board.Pixels.Pix, expected[index].Pix) {
			t.Fatalf("independent board %d used cached/source/footer/other-board pixels: bounds %v, pixels %v, want %v",
				index, board.Bounds, board.Pixels.Pix, expected[index].Pix)
		}
		options.LayerIndex = &board.LayerIndex
		selected, err := psdscene.RenderArtboard(t.Context(), source, options)
		if err != nil || selected.Rect != expected[index].Rect || !bytes.Equal(selected.Pix, expected[index].Pix) {
			t.Fatalf("single selected board %d differs from independent set: %v", index, err)
		}
	}
}

func TestArtboardLivePlacedEditSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, external := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/external%t", version, compression, external), func(t *testing.T) {
					data, contents, artwork := artboardLivePlacedFixture(t, version, external)
					original := bytes.Clone(data)
					editor := compositeEditor(t, data)
					metadata := artboardAutoMetadata(t, editor.source)
					var resolver psdfile.LinkedDocumentResolver
					var render *psdscene.Options
					if external {
						resolver = func(_ context.Context, record psdfile.LinkedFileRecord) (*psdfile.Document, error) {
							if record.ID != "fixture-id" || record.Kind != "liFE" {
								t.Fatal("independent source resolver received a different link")
							}
							return psdfile.Parse(contents)
						}
						render = &psdscene.Options{ExternalResolver: resolver}
						if _, err := psdscene.RenderArtboards(t.Context(), editor.source); !errors.Is(err, psdfile.ErrUnsupported) {
							t.Fatalf("external artboards silently used the original cached link contents: %v", err)
						}
					}
					_, initial := artboardLivePlacedExpected(artwork, 255)
					artboardLivePlacedAssert(t, editor.source, initial, render)
					if external {
						mappingFailure := errors.New("explicit board source mapping refused")
						failed := psdscene.ArtboardRenderOptions{Render: &psdscene.Options{
							ExternalResolver: func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
								return nil, mappingFailure
							},
						}}
						if images, err := psdscene.RenderArtboards(t.Context(), editor.source, failed); images != nil ||
							!errors.Is(err, mappingFailure) {
							t.Fatalf("failed external mapping fell back to cached source or returned partial boards: %v", err)
						}
					}
					for step := range 2 {
						artwork = smartEditImage([4]color.NRGBA{
							{R: byte(25 + step*20), G: 40, B: 60, A: 255},
							{R: 80, G: byte(100 + step*20), B: 120, A: 255},
							{R: 140, G: 160, B: byte(180 + step*20), A: 255},
							{R: 200, G: 220, B: byte(240 - step*20), A: 255},
						})
						contents = smartEditPrototype(uint16(step+1), artwork)
						left := artboardAutoLayer(t, editor, "left-object")
						if external {
							if err := editor.UpdateLinkedContents(t.Context(), left,
								func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
									return LinkedContentsUpdate{Data: contents}, nil
								}); err != nil {
								t.Fatal(err)
							}
						} else if err := editor.ReplaceContents(t.Context(), left, contents); err != nil {
							t.Fatal(err)
						}
						opacity := byte(255)
						if step == 1 {
							opacity = 128
							if err := editor.SetOpacity(left, opacity); err != nil {
								t.Fatal(err)
							}
						}
						parent, boards := artboardLivePlacedExpected(artwork, opacity)
						pixels, err := editor.Composite(t.Context())
						if err != nil || !bytes.Equal(pixels.Pix, parent.Pix) || pixels.Rect != parent.Rect {
							t.Fatalf("edited parent artboard composite failed: %v", err)
						}
						path := filepath.Join(t.TempDir(), "live-artboard.psd")
						if err := editor.Save(t.Context(), path, SaveOptions{Compression: compression}); err != nil {
							t.Fatalf("automatic live artboard save failed: %v", err)
						}
						saved, err := psdfile.Open(path)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = saved.Close() })
						if saved.Header.Version != version || !reflect.DeepEqual(metadata, artboardAutoMetadata(t, saved)) {
							t.Fatal("save changed source format or retained artboard metadata")
						}
						var expectedPlanes []byte
						for component := range 4 {
							for offset := component; offset < len(parent.Pix); offset += 4 {
								expectedPlanes = append(expectedPlanes, parent.Pix[offset])
							}
						}
						compositeExpect(t, saved, expectedPlanes)
						kind, fileType := "liFD", "8BPS"
						if external {
							kind = "liFE"
							if _, err := psdscene.RenderArtboards(t.Context(), saved); !errors.Is(err, psdfile.ErrUnsupported) {
								t.Fatalf("reopened external artboards silently used cached or retained contents: %v", err)
							}
						}
						if step == 1 {
							fileType = "8BPB"
						}
						smartFormatsAssertRecord(t, saved, kind, fileType, contents)
						artboardLivePlacedAssert(t, saved, boards, render)
						scene, err := psdscene.Render(t.Context(), saved, psdscene.Options{ExternalResolver: resolver})
						if err != nil || scene.Rect != parent.Rect || !bytes.Equal(scene.Pix, parent.Pix) {
							t.Fatalf("live parent render changed board clipping or outside overlay: %v", err)
						}
						reopened, err := New(saved)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = reopened.Close() })
						editor = reopened
					}
					if !bytes.Equal(data, original) {
						t.Fatal("live source editing changed original encoded parent bytes")
					}
				})
			}
		}
	}
}

func TestArtboardLivePlacedRasterSourceSaveReopenContinuePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, external := range []bool{false, true} {
				t.Run(fmt.Sprintf("v%d/c%d/external%t", version, compression, external), func(t *testing.T) {
					data, _, _ := artboardLivePlacedFixture(t, version, external)
					editor := compositeEditor(t, data)
					for step := range 2 {
						artwork := smartEditImage(smartEditColors())
						artwork.Pix[4], artwork.Pix[12] = byte(25+step*20), byte(80+step*30)
						var encoded bytes.Buffer
						if err := png.Encode(&encoded, artwork); err != nil {
							t.Fatal(err)
						}
						contents := encoded.Bytes()
						left := artboardAutoLayer(t, editor, "left-object")
						kind := "liFD"
						var render *psdscene.Options
						if external {
							kind = "liFE"
							if err := editor.UpdateLinkedContents(t.Context(), left,
								func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
									return LinkedContentsUpdate{Data: contents}, nil
								}); err != nil {
								t.Fatal(err)
							}
							render = &psdscene.Options{ExternalRasterResolver: func(
								_ context.Context, record psdfile.LinkedFileRecord,
							) ([]byte, error) {
								if record.Kind != "liFE" || record.FileType != "PNGf" || record.ID != "fixture-id" {
									t.Fatal("explicit raster mapping received a different source record")
								}
								return contents, nil
							}}
						} else if err := editor.ReplaceContents(t.Context(), left, contents); err != nil {
							t.Fatal(err)
						}
						_, boards := artboardLivePlacedExpected(artwork, 255)
						saved := compositeSave(t, editor, SaveOptions{Compression: compression})
						smartFormatsAssertRecord(t, saved, kind, "PNGf", contents)
						artboardLivePlacedAssert(t, saved, boards, render)
						if external {
							if _, err := psdscene.RenderArtboards(t.Context(), saved); !errors.Is(err, psdfile.ErrUnsupported) {
								t.Fatalf("external raster source used a saved cache without explicit mapping: %v", err)
							}
						}
						reopened, err := New(saved)
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = reopened.Close() })
						editor = reopened
					}
				})
			}
		}
	}
}
