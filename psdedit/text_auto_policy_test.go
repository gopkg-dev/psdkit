package psdedit

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func textAutoExpectUnchanged(t *testing.T, editor *Editor, before Changes, pixels *image.NRGBA, retained int64) {
	t.Helper()
	if after, _ := editor.Changes(); after != before || editor.roots[0].pixels != pixels ||
		editor.ownedBytesLocked(nil) != retained {
		t.Fatal("automatic text failure committed metadata, artwork, revision or retained allocations")
	}
}

func TestTextAutoRequiresExplicitFontAndApproximationAndRejectsUnsafeScopesAtomically(t *testing.T) {
	face, resolver := textAutoFont(t)
	for _, test := range []struct {
		name       string
		resolution uint32
		text       string
		global     []byte
		options    TextEditOptions
		kind       error
	}{
		{name: "default", resolution: 72, text: "V", kind: ErrUnsupported},
		{name: "font without policy", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver}}},
		{name: "cache cannot replace explicit policy", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver, CachedTextLayers: true}}},
		{name: "cache plus live policy", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver, CachedSpecialLayers: true,
				ApproximateText: &psdscene.TextRenderOptions{}}}},
		{name: "output color conversion cannot bake raw text", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver, OutputProfile: []byte{},
				ApproximateText: &psdscene.TextRenderOptions{}}}},
		{name: "no font", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{ApproximateText: &psdscene.TextRenderOptions{}}}},
		{name: "both policies", resolution: 72, text: "V", kind: ErrInvalidEdit,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver,
				ApproximateText:         &psdscene.TextRenderOptions{},
				ApproximateOpenTypeText: &psdscene.OpenTypeTextRenderOptions{Kerning: psdscene.OpenTypeTextNone}}}},
		{name: "pixels plus renderer", resolution: 72, text: "V", kind: ErrInvalidEdit,
			options: TextEditOptions{Pixels: image.NewNRGBA(image.Rect(0, 0, 1, 1)),
				Render: textAutoOptions(resolver, false).Render}},
		{name: "missing resolution", text: "V", kind: ErrUnsupported, options: textAutoOptions(resolver, false)},
		{name: "non72 resolution", resolution: 144, text: "V", kind: ErrUnsupported,
			options: textAutoOptions(resolver, false)},
		{name: "OpenType preserves non72 refusal", resolution: 144, text: "V", kind: ErrUnsupported,
			options: textAutoOptions(resolver, true)},
		{name: "modern private state", resolution: 72, text: "V", kind: ErrUnsupported,
			global: nativeTextGlobalTag(nativeTextDocumentFixture("AV")), options: textAutoOptions(resolver, false)},
		{name: "modern private OpenType state", resolution: 72, text: "V", kind: ErrUnsupported,
			global: nativeTextGlobalTag(nativeTextDocumentFixture("AV")), options: textAutoOptions(resolver, true)},
		{name: "Unicode shaping", resolution: 72, text: "中文", kind: psdfont.ErrUnsupported,
			options: textAutoOptions(resolver, false)},
		{name: "missing Latin glyph", resolution: 72, text: "B", kind: psdfont.ErrMissingGlyph,
			options: textAutoOptions(resolver, false)},
		{name: "OpenType multiline", resolution: 72, text: "A\nV", kind: ErrUnsupported,
			options: textAutoOptions(resolver, true)},
		{name: "unselected OpenType kerning", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver,
				ApproximateOpenTypeText: &psdscene.OpenTypeTextRenderOptions{}}}},
		{name: "partial legacy bounds", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver,
				ApproximateText: &psdscene.TextRenderOptions{Bounds: new(image.Rect(0, 0, 48, 64))}}}},
		{name: "partial OpenType bounds", resolution: 72, text: "V", kind: ErrUnsupported,
			options: TextEditOptions{Render: &psdscene.Options{FontResolver: resolver,
				ApproximateOpenTypeText: &psdscene.OpenTypeTextRenderOptions{
					Kerning: psdscene.OpenTypeTextNone, Bounds: new(image.Rect(0, 0, 48, 64))}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			editor := compositeEditor(t, textAutoFixture(t, 1, "AV", face.PostScriptName, test.resolution, test.global, nil))
			before, _ := editor.Changes()
			retained := editor.ownedBytesLocked(nil)
			err := editor.SetTextContent(t.Context(), 1, test.text, test.options)
			if !errors.Is(err, test.kind) {
				t.Fatalf("automatic text policy returned %v, want %v", err, test.kind)
			}
			textAutoExpectUnchanged(t, editor, before, nil, retained)
			if len(editor.roots[0].additional) != 0 {
				t.Fatal("policy refusal retained candidate TySh metadata")
			}
		})
	}
}

func TestTextAutoFontResolverRunsOutsideLockAndCannotCommitStaleOrFailedCandidates(t *testing.T) {
	face, _ := textAutoFont(t)
	for _, openType := range []bool{false, true} {
		scenarios := []string{"success", "missing", "error", "cancel", "stale", "source closed", "editor closed"}
		for _, scenario := range scenarios {
			t.Run(scenario+map[bool]string{false: "/legacy", true: "/OpenType"}[openType], func(t *testing.T) {
				editor := compositeEditor(t, textAutoFixture(t, 1, "AV", face.PostScriptName, 72, nil, nil))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				failure := errors.New("explicit font source unavailable")
				calls := 0
				resolver := psdfont.ResolverFunc(func(ctx context.Context, name string) (*psdfont.Face, error) {
					calls++
					if _, err := editor.Layers(); err != nil {
						return nil, err
					}
					if name != face.PostScriptName {
						t.Fatal("renderer inferred a substitute font")
					}
					switch scenario {
					case "missing":
						return nil, psdfont.ErrMissingFont
					case "error":
						return nil, failure
					case "cancel":
						cancel()
					case "stale":
						if err := editor.Rename(1, "callback edit survives"); err != nil {
							return nil, err
						}
					case "source closed":
						if err := editor.source.Close(); err != nil {
							return nil, err
						}
					case "editor closed":
						if err := editor.Close(); err != nil {
							return nil, err
						}
					}
					return face, nil
				})
				before, _ := editor.Changes()
				retained := editor.ownedBytesLocked(nil)
				err := editor.SetTextContent(ctx, 1, "ffi", textAutoOptions(resolver, openType))
				if calls != 1 {
					t.Fatalf("explicit font callback calls = %d, want 1", calls)
				}
				if scenario == "success" {
					if err != nil || editor.roots[0].pixels == nil {
						t.Fatalf("font callback could not reenter editor reads outside lock: %v", err)
					}
					return
				}
				kind := map[string]error{"missing": psdfont.ErrMissingFont, "error": failure,
					"cancel": context.Canceled, "stale": ErrStaleLinkedEdit,
					"source closed": psdfile.ErrClosed, "editor closed": psdfile.ErrClosed}[scenario]
				if !errors.Is(err, kind) {
					t.Fatalf("font callback failure cause = %v, want %v", err, kind)
				}
				if scenario == "editor closed" {
					if !editor.closed || len(editor.roots) != 0 {
						t.Fatal("closed editor received a candidate commit")
					}
					return
				}
				if scenario == "stale" {
					before.Revision++
					before.Dirty = true
					if editor.roots[0].name != "callback edit survives" {
						t.Fatal("stale text candidate replaced the independent callback edit")
					}
				}
				textAutoExpectUnchanged(t, editor, before, nil, retained)
				if len(editor.roots[0].additional) != 0 {
					t.Fatal("font callback failure committed candidate metadata")
				}
			})
		}
	}
}

func TestTextAutoSnapshotsBoundsKerningFeaturesAndResolverBeforeCallback(t *testing.T) {
	face, resolver := textAutoFont(t)
	for _, openType := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "OpenType"}[openType], func(t *testing.T) {
			editor := compositeEditor(t, textAutoFixture(t, 1, "AV", face.PostScriptName, 72, nil, nil))
			options := textAutoOptions(resolver, openType)
			bounds := image.Rect(0, 0, 96, 64)
			if openType {
				options.Render.ApproximateOpenTypeText.Bounds = &bounds
			} else {
				options.Render.ApproximateText.Bounds = &bounds
			}
			expected := textAutoExpected(t, "ffiAV", face.PostScriptName, options)
			options.Render.FontResolver = psdfont.ResolverFunc(func(_ context.Context, _ string) (*psdfont.Face, error) {
				bounds = image.Rect(0, 0, 1, 1)
				if openType {
					options.Render.ApproximateOpenTypeText.Shape.Features[0].Enabled = false
					options.Render.ApproximateOpenTypeText.Shape.Features[1].Enabled = false
					options.Render.ApproximateOpenTypeText.Kerning = psdscene.OpenTypeTextNone
					options.Render.ApproximateOpenTypeText.MaxWorkingBytes = 1
				} else {
					*options.Render.ApproximateText.Kerning = true
					options.Render.ApproximateText.Samples = 1
					options.Render.ApproximateText.MaxWorkingBytes = 1
				}
				options.Render.FontResolver = nil
				options.Render.ApproximateText = nil
				options.Render.ApproximateOpenTypeText = nil
				options.Render.MaxWorkingBytes = 1
				return face, nil
			})
			if err := editor.SetTextContent(t.Context(), 1, "ffiAV", options); err != nil {
				t.Fatalf("callback mutated the captured render policy: %v", err)
			}
			textAutoExpectRaw(t, editor, expected)
		})
	}
}

func TestTextAutoNeverReadsOldRasterAndPreservesUnbakedArtwork(t *testing.T) {
	face, resolver := textAutoFont(t)
	data := textAutoFixture(t, 1, "AV", face.PostScriptName, 72, nil, compositeFixtureTag("lfx2", colorOverlayPayload(t)))
	editor, source, reader := compositeLayerOnlyEditor(t, data)
	reader.blockFooter = true
	first := source.Layers[0].Channels[0].Data
	last := source.Layers[0].Channels[len(source.Layers[0].Channels)-1].Data
	reader.failure, reader.failureCause = psdfile.Span{Offset: first.Offset,
		Length: last.Offset + last.Length - first.Offset}, errCompositeLayerOnlyPixels
	if err := editor.SetOpacity(1, 61); err != nil {
		t.Fatal(err)
	}
	plane := psdfile.Plane{Width: 2, Height: 2, Depth: 8, RowBytes: 2, Data: []byte{128, 128, 128, 128}}
	if err := editor.AddPixelMask(t.Context(), 1, image.Rect(0, 0, 2, 2), plane,
		PixelMaskSettings{DefaultColor: 128}); err != nil {
		t.Fatal(err)
	}
	vector := lifecycleVectorMask(t)
	vector.Width, vector.Height = 96, 64
	if err := editor.AddVectorMask(t.Context(), 1, vector); err != nil {
		t.Fatal(err)
	}
	options := textAutoOptions(resolver, false)
	expected := textAutoExpected(t, "ffiAV", face.PostScriptName, options)
	if err := editor.SetTextContent(t.Context(), 1, "ffiAV", options); err != nil {
		t.Fatalf("automatic source rendering depended on old raster or composed semantics: %v", err)
	}
	textAutoExpectRaw(t, editor, expected)
	if reader.footerReads != 0 || editor.roots[0].opacity != 61 || editor.roots[0].pixelMask == nil ||
		editor.roots[0].vectorMask == nil {
		t.Fatal("automatic text update consumed saved preview or removed independent masks/opacity")
	}
}

func TestTextAutoBudgetReadFailureAndCancellationKeepExistingEdit(t *testing.T) {
	face, resolver := textAutoFont(t)
	for _, scenario := range []string{"render budget", "retained budget", "source read", "metadata cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			data := textAutoFixture(t, 1, "AV", face.PostScriptName, 72, nil, nil)
			editor, source, reader := compositeLayerOnlyEditor(t, data)
			block := textAutoSourceRecord(t, editor).Block
			options := textAutoOptions(resolver, false)
			if scenario != "source read" && scenario != "metadata cancellation" {
				if err := editor.SetTextContent(t.Context(), 1, "V", options); err != nil {
					t.Fatal(err)
				}
			}
			kind := error(psdfile.ErrLimit)
			switch scenario {
			case "render budget":
				options.Render.MaxWorkingBytes = 1024
			case "retained budget":
				editor.roots[0].setSemanticCost("other", source.Limits().MaxDecodeBytes-editor.ownedBytesLocked(nil)-1024)
			case "source read":
				reader.failure = block.Data
				reader.failureCause, kind = io.ErrClosedPipe, io.ErrClosedPipe
			case "metadata cancellation":
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				cancelReader := &smartEditCancelReader{data: data}
				cancelSource, err := psdfile.Read(cancelReader, int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = cancelSource.Close() })
				editor = lifecycleReopen(t, cancelSource)
				cancelReader.trigger, cancelReader.cancel = textAutoSourceRecord(t, editor).Block.Data, cancel
				before, _ := editor.Changes()
				retained := editor.ownedBytesLocked(nil)
				err = editor.SetTextContent(ctx, 1, "ffi", options)
				if !errors.Is(err, context.Canceled) || cancelReader.matches == 0 {
					t.Fatalf("metadata-read cancellation cause = %v", err)
				}
				textAutoExpectUnchanged(t, editor, before, nil, retained)
				return
			}
			before, _ := editor.Changes()
			pixels, retained := editor.roots[0].pixels, editor.ownedBytesLocked(nil)
			metadata := bytes.Clone(editor.roots[0].additional[block.Span.Offset])
			err := editor.SetTextContent(t.Context(), 1, "ffi", options)
			if !errors.Is(err, kind) {
				t.Fatalf("automatic text failed with %v, want %v", err, kind)
			}
			textAutoExpectUnchanged(t, editor, before, pixels, retained)
			for _, current := range editor.roots[0].additional {
				if !bytes.Equal(metadata, current) {
					t.Fatal("failed automatic replacement changed prior TySh bytes")
				}
			}
		})
	}
}

func TestTextAutoEmptyTextClearsPixelsWithBothExplicitPolicies(t *testing.T) {
	face, resolver := textAutoFont(t)
	for _, openType := range []bool{false, true} {
		editor := compositeEditor(t, textAutoFixture(t, 1, "AV", face.PostScriptName, 72, nil, nil))
		options := textAutoOptions(resolver, openType)
		if err := editor.SetTextContent(t.Context(), 1, "", options); err != nil {
			t.Fatal(err)
		}
		pixels := editor.roots[0].pixels
		if pixels == nil || editor.roots[0].bounds != image.Rect(0, 0, 96, 64) {
			t.Fatal("empty text did not materialize a full-canvas owned transparent source")
		}
		for index := 3; index < len(pixels.Pix); index += 4 {
			if pixels.Pix[index] != 0 {
				t.Fatal("empty text retained old glyph coverage")
			}
		}
		saved := compositeSave(t, editor)
		record, _, metadata := textEditRead(t, saved)
		if record.Text != "" || metadata.Text != "\r" || metadata.UTF16Units != 1 {
			t.Fatal("empty text lost its terminal paragraph range")
		}
		reopened := lifecycleReopen(t, saved)
		if err := reopened.SetTextContent(t.Context(), 1, "AV", options); err != nil {
			t.Fatal(err)
		}
		textAutoExpectRaw(t, reopened, textAutoExpected(t, "AV", face.PostScriptName, options))
	}
}

func TestTextAutoMultilineUsesOnlyExplicitlySupportedTypographyAndContinuesAfterSave(t *testing.T) {
	face, resolver := textAutoFont(t)
	for _, openType := range []bool{false, true} {
		editor := compositeEditor(t, textAutoFixture(t, 1, "AV", face.PostScriptName, 72, nil, nil))
		options := textAutoOptions(resolver, openType)
		if openType {
			options.Render.ApproximateOpenTypeText.ApproximateTypography = true
		}
		if err := editor.SetTextContent(t.Context(), 1, "A\r\nV\né", options); err != nil {
			t.Fatal(err)
		}
		saved := compositeSave(t, editor, SaveOptions{Compression: 3})
		record, _, metadata := textEditRead(t, saved)
		if record.Text != "A\rV\ré" || metadata.Text != "A\rV\ré\r" || len(metadata.Paragraphs) != 3 ||
			metadata.UTF16Units != 6 {
			t.Fatal("automatic multiline source lost normalization, paragraph coverage or terminal CR")
		}
		for _, paragraph := range metadata.Paragraphs {
			if paragraph.Length != 2 {
				t.Fatal("automatic multiline paragraph boundaries do not match UTF-16 source ranges")
			}
		}
		reopened := lifecycleReopen(t, saved)
		if err := reopened.SetTextContent(t.Context(), 1, "V\nA", options); err != nil {
			t.Fatal(err)
		}
		again := compositeSave(t, reopened, SaveOptions{Compression: 2})
		last, _, lastMetadata := textEditRead(t, again)
		if last.Text != "V\rA" || len(lastMetadata.Paragraphs) != 2 || lastMetadata.UTF16Units != 4 {
			t.Fatal("saved multiline source cannot be edited and rasterized again")
		}
	}
}
