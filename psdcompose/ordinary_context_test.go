package psdcompose

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
	"time"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func TestOrdinaryRenderingDeadlineReturnsNoPartialImage(t *testing.T) {
	layer := composeTestLayer(image.Rect(0, 0, 1024, 1024), color.NRGBA{R: 213, G: 71, B: 129, A: 128})
	document := &psdmodel.Document{Width: 1024, Height: 1024, Layers: []*psdmodel.Layer{layer}}
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	result, err := Render(ctx, document)
	if result != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ordinary rendering returned partial pixels after its deadline: result=%v error=%v", result, err)
	}
}
