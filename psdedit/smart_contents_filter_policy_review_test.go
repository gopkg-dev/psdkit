package psdedit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
)

func TestSmartContentsFilterResolverCannotChangeCapturedApproximationPolicy(t *testing.T) {
	for _, operation := range []string{"embed", "update"} {
		for _, allowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allowed=%t", operation, allowed), func(t *testing.T) {
				data, _ := smartFilterAutoSourceFixture(t, 1, true, false)
				editor := compositeEditor(t, data)
				stack := smartFilterAutoExactStack(t, editor.source)
				stack.Filters[0].Radius = .7
				if err := editor.SetSmartFilterParameters(t.Context(), 1, stack, SmartFilterEditOptions{
					Pixels: exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255}),
				}); err != nil {
					t.Fatal(err)
				}
				pixels := exactSmartFilterGrayPixels([4]byte{255, 128, 64, 0})
				contents := smartEditPrototype(2, pixels)
				policy := psdfilter.Options{ApproximateGaussian: allowed}
				options := SmartContentsOptions{SmartFilters: &policy}
				unchanged := smartContentsFilterState(t, editor)
				calls := 0
				mutate := func() {
					calls++
					policy.ApproximateGaussian = !allowed
					if _, err := editor.Layers(); err != nil {
						t.Fatal("content resolver held the editor lock", err)
					}
				}
				var err error
				if operation == "embed" {
					err = editor.EmbedLinkedContentsWithOptions(t.Context(), 1,
						func(context.Context, psdfile.LinkedFileRecord) ([]byte, error) {
							mutate()
							return contents, nil
						}, options)
				} else {
					err = editor.UpdateLinkedContentsWithOptions(t.Context(), 1,
						func(context.Context, psdfile.LinkedFileRecord) (LinkedContentsUpdate, error) {
							mutate()
							return LinkedContentsUpdate{Data: contents}, nil
						}, options)
				}
				if calls != 1 {
					t.Fatalf("external resolver calls = %d, want 1", calls)
				}
				if !allowed {
					if !errors.Is(err, ErrUnsupported) {
						t.Fatalf("resolver enabled an approximation absent from the captured policy: %v", err)
					}
					unchanged()
					return
				}
				if err != nil {
					t.Fatalf("resolver disabled an explicitly captured approximation policy: %v", err)
				}
				expected, err := psdfilter.Apply(t.Context(), pixels, stack,
					psdfilter.Options{ApproximateGaussian: true})
				if err != nil {
					t.Fatal(err)
				}
				actual, err := editor.Composite(t.Context())
				if err != nil || !bytes.Equal(actual.Pix, expected.Pix) {
					t.Fatalf("captured approximate artwork differs after the resolver returned: %v", err)
				}
				saved := compositeSave(t, editor, SaveOptions{Compression: 1})
				links, err := saved.LinkedFiles(t.Context())
				if err != nil || len(links) != 1 || (links[0].Kind == "liFE") != (operation == "update") {
					t.Fatalf("captured policy changed external link ownership: %v", err)
				}
				retained, err := saved.ReadSpan(links[0].Data)
				if err != nil || !bytes.Equal(retained, contents) {
					t.Fatalf("captured policy changed original replacement source bytes: %v", err)
				}
			})
		}
	}
}
