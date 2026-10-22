package psdedit

import (
	"bytes"
	"errors"
	"testing"

	"github.com/gopkg-dev/psdkit/psdpattern"
)

func TestSetPatternFillDoesNotEnableUnverifiedSamplingModes(t *testing.T) {
	editor := patternEditSource(t, 1, patternEditDescriptor(), nil, nil)
	original := patternEditRead(t, editor.source)
	for _, sampling := range [][2]float64{{80, 0}, {150, 0}, {100, 90}, {100, 180}, {100, 270}, {50, 90}, {200, 270}} {
		desired := *original.Fill
		desired.Scale, desired.Angle = sampling[0], sampling[1]
		if err := editor.SetPatternFill(nil, 1, desired); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("sampling without a uniform verified formula was enabled: %v", err)
		}
	}
	state, _ := editor.Changes()
	node, _, _, _ := editor.findNodeLocked(1)
	if state.Dirty || node.pattern != nil || node.patternTile != nil || len(node.additional) != 0 {
		t.Fatal("rejected sampling request committed partial editable parameters")
	}
}

func TestSetPatternFillScaledParametersSavePSDPSBWithoutChangingTile(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		for _, scale := range []float64{50, 200} {
			editor := patternEditSource(t, version, patternEditDescriptor(), nil, nil)
			fill := *patternEditRead(t, editor.source).Fill
			fill.Scale, fill.Phase = scale, psdpattern.Phase{X: 1, Y: -1}
			if err := editor.SetPatternFill(nil, 1, fill); err != nil {
				t.Fatal(err)
			}
			node, _, _, _ := editor.findNodeLocked(1)
			expected, err := psdpattern.Render(nil, node.patternTile, fill, node.bounds)
			if err != nil {
				t.Fatal(err)
			}
			live, err := editor.Composite(nil)
			if err != nil || !bytes.Equal(live.Pix, expected.Pix) {
				t.Fatalf("scaled editor source differs from the independent primitive: %v", err)
			}
			saved := compositeSave(t, editor)
			view := patternEditRead(t, saved)
			if view.Fill.Scale != scale || view.Fill.Phase != fill.Phase || view.Fill.Angle != 0 ||
				view.Block.Data.Length%4 != 0 || saved.Header.Version != version {
				t.Fatal("scaled saved parameters, internal descriptor alignment or version differs")
			}
			original, _ := editor.source.ReadSpan(editor.source.AdditionalInfo[0].Span)
			unchanged, _ := saved.ReadSpan(saved.AdditionalInfo[0].Span)
			if !bytes.Equal(original, unchanged) {
				t.Fatal("scaling edited the original pattern or compressed tile bytes")
			}
			reopened, err := New(saved)
			if err != nil {
				t.Fatal(err)
			}
			pixels, err := reopened.Composite(nil)
			if err != nil || !bytes.Equal(live.Pix, pixels.Pix) {
				t.Fatalf("reopened scaled source differs from the editor: %v", err)
			}
			_ = reopened.Close()
		}
	}
}
