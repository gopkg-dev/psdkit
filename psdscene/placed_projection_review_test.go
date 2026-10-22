package psdscene

import (
	"context"
	"errors"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func TestIndependentPlacedProjectionPolicyLimitsCancelAndStrictDefault(t *testing.T) {
	data := projectiveTransparentSource(t)
	reader := &tilePlacedSparseReader{prefix: data, length: int64(len(data))}
	source, err := psdfile.Read(reader, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := Render(nil, source); !errors.Is(err, psdrender.ErrUnsupportedCrop) {
		t.Fatalf("full default lost strict projection refusal: %v", err)
	}
	forbidPlacedSnapshots(t, source, 0, reader)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, operation := range []string{"full", "tile", "placed"} {
		t.Run(operation, func(t *testing.T) {
			var err error
			switch operation {
			case "full":
				_, err = Render(ctx, source, Options{ApproximatePlacedProjection: true})
			case "tile":
				_, err = RenderTile(ctx, source, image.Rect(4, 5, 12, 14), TileOptions{ApproximatePlacedProjection: true})
			case "placed":
				_, err = RenderPlacedLayer(ctx, source, 1, Options{ApproximatePlacedProjection: true})
			}
			if !errors.Is(err, context.Canceled) || reader.reads != 0 {
				t.Fatalf("canceled policy read saved pixels or lost cause: %d / %v", reader.reads, err)
			}
		})
	}
	if _, err := Render(nil, source, Options{ApproximatePlacedProjection: true, MaxWorkingBytes: 1024}); !errors.Is(err, psdfile.ErrLimit) || reader.reads != 0 {
		t.Fatalf("full projection budget ignored: %v", err)
	}
	if _, err := RenderTile(nil, source, image.Rect(4, 5, 12, 14), TileOptions{
		ApproximatePlacedProjection: true, MaxPixels: 1}); !errors.Is(err, psdfile.ErrLimit) || reader.reads != 0 {
		t.Fatalf("regional output budget ignored: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Render(nil, source, Options{ApproximatePlacedProjection: true}); !errors.Is(err, psdfile.ErrClosed) {
		t.Fatalf("closed source lost lifecycle error: %v", err)
	}
}
