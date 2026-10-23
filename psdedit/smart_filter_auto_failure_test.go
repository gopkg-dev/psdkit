package psdedit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func TestSmartFilterAutoRejectsConflictingOrCachedPoliciesAtomically(t *testing.T) {
	for _, scenario := range []string{"pixels-and-render", "output-profile", "cached-special", "cached-text", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			editor := compositeEditor(t, exactSmartFilterFixture(t, 1))
			stack := smartFilterAutoExactStack(t, editor.source)
			stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
			options := SmartFilterEditOptions{Render: &psdscene.Options{}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "pixels-and-render":
				options.Pixels = exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255})
			case "output-profile":
				options.Render.OutputProfile = []byte{}
			case "cached-special":
				options.Render.CachedSpecialLayers = true
			case "cached-text":
				options.Render.CachedTextLayers = true
			case "canceled":
				cancel()
			}
			before, _ := editor.Changes()
			err := editor.SetSmartFilterParameters(ctx, 1, stack, options)
			if err == nil || scenario == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("conflicting or non-live policy was accepted: %v", err)
			}
			smartFilterAutoAssertStateUnchanged(t, editor, before)
		})
	}
}

func TestSmartFilterAutoBudgetAndSourceFailuresDoNotCommitArtwork(t *testing.T) {
	for _, scenario := range []string{"working-budget", "document-budget", "source-io"} {
		t.Run(scenario, func(t *testing.T) {
			editor, source, reader := compositeLayerOnlyEditor(t, exactSmartFilterFixture(t, 2))
			stack := smartFilterAutoExactStack(t, source)
			stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
			options := SmartFilterEditOptions{Render: &psdscene.Options{}}
			want := psdfile.ErrLimit
			switch scenario {
			case "working-budget":
				options.Render.MaxWorkingBytes = 1
			case "document-budget":
				options.Render.MaxDocuments = 1
			case "source-io":
				reader.failure, reader.failureCause = source.AdditionalInfo[0].Data, io.ErrClosedPipe
				want = io.ErrClosedPipe
			}
			before, _ := editor.Changes()
			if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, options); !errors.Is(err, want) {
				t.Fatalf("automatic source failure category differs: %v, want %v", err, want)
			}
			smartFilterAutoAssertStateUnchanged(t, editor, before)
			reader.failureCause = nil
			if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
				t.Fatalf("automatic edit did not recover after source or budget failure: %v", err)
			}
		})
	}
}

func TestSmartFilterAutoRejectedRepeatKeepsPreviouslyCommittedParameters(t *testing.T) {
	editor := compositeEditor(t, exactSmartFilterFixture(t, 1))
	stack := smartFilterAutoExactStack(t, editor.source)
	stack.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err != nil {
		t.Fatal(err)
	}
	node, _, _, _ := editor.findNodeLocked(1)
	pixels, placed, owned := node.pixels, node.placed, editor.ownedBytesLocked(nil)
	before, _ := editor.Changes()
	metadata := make(map[int64][]byte, len(node.additional))
	for key, value := range node.additional {
		metadata[key] = bytes.Clone(value)
	}
	stack.Filters[3].Adjustment.BrightnessContrast.Brightness = 15
	if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{}); err == nil {
		t.Fatal("non-exact replacement inherited a previous exact automatic edit's policy")
	}
	if after, _ := editor.Changes(); after != before || node.pixels != pixels || node.placed != placed ||
		editor.ownedBytesLocked(nil) != owned || len(node.additional) != len(metadata) {
		t.Fatal("rejected repeat changed previously committed cache, placement or accounting")
	}
	for key, expected := range metadata {
		if !bytes.Equal(node.additional[key], expected) {
			t.Fatal("rejected repeat left partial new filter controls")
		}
	}
	current, err := editor.Composite(t.Context())
	if err != nil || !bytes.Equal(current.Pix, exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255}).Pix) {
		t.Fatalf("rejected repeat lost the previous successful automatic filter artwork: %v", err)
	}
}
