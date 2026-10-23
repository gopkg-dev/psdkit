package psdedit

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfilter"
)

func TestSmartFilterAutoInactiveStacksRestoreOriginalSourceWithoutLegacyBlocker(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for compression := range uint16(4) {
			for _, inactive := range []string{"stack-disabled", "filters-disabled", "zero-opacity"} {
				t.Run(fmt.Sprintf("v%d/c%d/%s", version, compression, inactive), func(t *testing.T) {
					editor := compositeEditor(t, exactSmartFilterFixture(t, version))
					first := smartFilterAutoExactStack(t, editor.source)
					first.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
					if err := editor.SetSmartFilterParameters(t.Context(), 1, first, SmartFilterEditOptions{}); err != nil {
						t.Fatal(err)
					}
					requested := exactSmartFilterStack(t, editor.source)
					switch inactive {
					case "stack-disabled":
						requested.Enabled = false
					case "filters-disabled":
						for index := range requested.Filters {
							requested.Filters[index].Enabled = false
						}
					case "zero-opacity":
						for index := range requested.Filters {
							requested.Filters[index].Opacity = 0
						}
					}
					if err := editor.SetSmartFilterParameters(t.Context(), 1, requested, SmartFilterEditOptions{}); err != nil {
						t.Fatalf("inactive filter metadata retained a legacy blocker: %v", err)
					}
					original := exactSmartFilterGrayPixels([4]byte{0, 64, 128, 255})
					reopened, saved := smartFilterAutoSaved(t, editor, compression, original, psdfilter.Options{}, true)
					if actual := exactSmartFilterStack(t, saved); !reflect.DeepEqual(actual, requested) {
						t.Fatal("inactive saved stack lost retained non-neutral algorithm parameters")
					}
					active := smartFilterAutoExactStack(t, saved)
					active.Enabled = true
					for index := range active.Filters {
						active.Filters[index].Enabled, active.Filters[index].Opacity = true, 1
					}
					active.Filters[4].Adjustment.HueSaturation.Master.Lightness = 51
					if err := reopened.SetSmartFilterParameters(t.Context(), 1, active, SmartFilterEditOptions{}); err != nil {
						t.Fatalf("reopened inactive stack could not resume exact source-live filtering: %v", err)
					}
					smartFilterAutoSaved(t, reopened, compression, exactSmartFilterGrayPixels([4]byte{130, 161, 193, 255}),
						psdfilter.Options{}, false)
				})
			}
		}
	}
}
