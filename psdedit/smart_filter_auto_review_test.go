package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestSmartFilterAutoReviewCallbackCannotMutateOwnedCandidateControls(t *testing.T) {
	data, contents := smartFilterAutoSourceFixture(t, 1, true, false)
	editor := compositeEditor(t, data)
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	called := false
	options := SmartFilterEditOptions{Render: &psdscene.Options{ExternalResolver: func(
		_ context.Context, _ psdfile.LinkedFileRecord,
	) (*psdfile.Document, error) {
		called = true
		stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = -97
		stack.Filters[0].Radius = 7
		return psdfile.Parse(contents)
	}}}
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, options); err != nil || !called {
		t.Fatalf("callback changed the previously captured candidate: %v", err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	if !bytes.Equal(node.pixels.Pix, exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255}).Pix) {
		t.Fatal("automatic pixels borrowed the caller's mutated nested adjustment")
	}
	saved := compositeSave(t, editor)
	actual := exactSmartFilterStack(t, saved)
	if actual.Filters[0].Radius != 0 || actual.Filters[4].Adjustment.HueSaturation.Master.Lightness != 51 {
		t.Fatal("callback changed committed metadata after candidate encoding")
	}
}

func TestSmartFilterAutoReviewReentrantFilterEditWinsOverStaleOuterCandidate(t *testing.T) {
	data, contents := smartFilterAutoSourceFixture(t, 1, true, false)
	editor := compositeEditor(t, data)
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	expected := exactSmartFilterGrayPixels([4]byte{0, 2, 4, 8})
	options := SmartFilterEditOptions{Render: &psdscene.Options{ExternalResolver: func(
		_ context.Context, _ psdfile.LinkedFileRecord,
	) (*psdfile.Document, error) {
		inner := smartFilterAutoExactStack(t, editor.source)
		inner.Filters[4].Adjustment.HueSaturation.Master.Lightness = -97
		if err := editor.SetSmartFilterParameters(t.Context(), 1, inner, SmartFilterEditOptions{Pixels: expected}); err != nil {
			return nil, err
		}
		return psdfile.Parse(contents)
	}}}
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, options); !errors.Is(err, ErrStaleLinkedEdit) {
		t.Fatalf("outer automatic edit did not reject a committed reentrant edit: %v", err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	if editor.revision != 1 || !bytes.Equal(node.pixels.Pix, expected.Pix) {
		t.Fatal("stale automatic candidate overwrote the inner filter edit")
	}
	saved := compositeSave(t, editor)
	if exactSmartFilterStack(t, saved).Filters[4].Adjustment.HueSaturation.Master.Lightness != -97 {
		t.Fatal("stale automatic candidate overwrote reentrant descriptor bytes")
	}
}

func TestSmartFilterAutoReviewResolverPanicDoesNotLockOrPolluteEditor(t *testing.T) {
	data, contents := smartFilterAutoSourceFixture(t, 1, true, false)
	editor := compositeEditor(t, data)
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	before, _ := editor.Changes()
	owned := editor.ownedBytesLocked(nil)
	failure := errors.New("independent callback panic")
	func() {
		defer func() {
			got, ok := recover().(error)
			if !ok || !errors.Is(got, failure) {
				t.Fatalf("resolver panic was replaced or suppressed: %v", got)
			}
		}()
		_ = editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{
			Render: &psdscene.Options{ExternalResolver: func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
				panic(failure)
			}},
		})
	}()
	available := make(chan error, 1)
	go func() {
		_, err := editor.Layers()
		available <- err
	}()
	select {
	case err := <-available:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("resolver panic retained the editor lock")
	}
	smartFilterAutoAssertStateUnchanged(t, editor, before)
	if editor.ownedBytesLocked(nil) != owned {
		t.Fatal("resolver panic retained a partial automatic candidate")
	}
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{
		Render: &psdscene.Options{ExternalResolver: func(context.Context, psdfile.LinkedFileRecord) (*psdfile.Document, error) {
			return psdfile.Parse(contents)
		}},
	}); err != nil {
		t.Fatalf("editor was unusable after recovering resolver panic: %v", err)
	}
}

func smartFilterReviewShiftedCache(t *testing.T, version uint16) []byte {
	t.Helper()
	data, _ := smartFilterAutoSourceFixture(t, version, false, true)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	position := source.Layers[0].Span.Offset
	_ = source.Close()
	binary.BigEndian.PutUint32(data[position+4:], 1)
	binary.BigEndian.PutUint32(data[position+12:], 3)
	return data
}

func TestSmartFilterAutoReviewUsesCurrentRelativeMaskState(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, scenario := range []string{"added-relative", "changed-relative", "cleared-relative",
			"removed-relative", "source-relative-cleared", "source-relative-removed"} {
			t.Run(scenario, func(t *testing.T) {
				editor := compositeEditor(t, smartFilterReviewShiftedCache(t, version))
				settings := PixelMaskSettings{DefaultColor: 255, Relative: scenario != "changed-relative"}
				plane := psdfile.Plane{Depth: 8, Width: 2, Height: 2, RowBytes: 2, Data: []byte{255, 255, 255, 255}}
				if err := editor.AddPixelMask(t.Context(), 1, image.Rect(1, 0, 3, 2), plane, settings); err != nil {
					t.Fatal(err)
				}
				if scenario == "source-relative-cleared" || scenario == "source-relative-removed" {
					// Poison the preview while materializing original mask metadata; the automatic editor must use source contents.
					saved := compositeSave(t, editor, SaveOptions{CompositeImage: image.NewNRGBA(image.Rect(0, 0, 6, 2))})
					var err error
					editor, err = New(saved)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = editor.Close() })
				}
				switch scenario {
				case "changed-relative":
					settings.Relative = true
					if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
						t.Fatal(err)
					}
				case "cleared-relative", "source-relative-cleared":
					settings.Relative = false
					if err := editor.SetPixelMask(t.Context(), 1, settings); err != nil {
						t.Fatal(err)
					}
				case "removed-relative", "source-relative-removed":
					if err := editor.RemovePixelMask(t.Context(), 1); err != nil {
						t.Fatal(err)
					}
				}
				stack := smartFilterAutoExactStack(t, editor.source)
				stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
				before, _ := editor.Changes()
				node, _, _, _ := editor.findNodeLocked(1)
				maskData := bytes.Clone(node.maskData)
				maskChange := node.pixelMaskChange
				err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{})
				if scenario == "added-relative" || scenario == "changed-relative" {
					if !errors.Is(err, ErrUnsupported) {
						t.Fatalf("current Relative mask moved from (1,0) into full-canvas cache: %v", err)
					}
					if after, _ := editor.Changes(); after != before || node.pixels != nil ||
						node.placed != nil || len(node.additional) != 0 {
						t.Fatal("rejected automatic geometry committed partial artwork or parameters")
					}
				} else if err != nil || node.bounds != image.Rect(0, 0, 6, 2) {
					t.Fatalf("cleared or removed current mask still used stale source Relative flag: %v", err)
				}
				if node.pixelMaskChange != maskChange || !bytes.Equal(maskData, node.maskData) {
					t.Fatal("automatic artwork changed independently owned mask state")
				}
			})
		}
	}
}

type smartFilterReviewClosingContext struct {
	context.Context
	source *psdfile.Document
	closed bool
}

func (ctx *smartFilterReviewClosingContext) Err() error {
	// Close in the final equality window, deterministically modeling a concurrent source owner closing it there.
	var stack [16]uintptr
	frames := runtime.CallersFrames(stack[:runtime.Callers(2, stack[:])])
	for {
		frame, more := frames.Next()
		if !ctx.closed && strings.HasSuffix(frame.Function, ".textEditPixelsEqual") {
			ctx.closed = true
			_ = ctx.source.Close()
		}
		if !more {
			return nil
		}
	}
}

func TestSmartFilterAutoReviewNoOpChecksSourceAfterPixelEquality(t *testing.T) {
	editor := compositeEditor(t, exactSmartFilterFixture(t, 1))
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	pixels, placed := node.pixels, node.placed
	revision, savedRevision := editor.revision, editor.savedRevision
	ctx := &smartFilterReviewClosingContext{Context: t.Context(), source: editor.source}
	err := editor.SetSmartFilterParameters(ctx, 1, stack, SmartFilterEditOptions{})
	if !ctx.closed || !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("source closed during no-op equality was accepted: closed=%t error=%v", ctx.closed, err)
	}
	if editor.revision != revision || editor.savedRevision != savedRevision || node.pixels != pixels || node.placed != placed {
		t.Fatal("failed no-op source check changed previously committed artwork or revisions")
	}
}
