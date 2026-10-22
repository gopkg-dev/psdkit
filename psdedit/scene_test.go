package psdedit

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdscene"
)

func sceneNestedFixture(t *testing.T, inner []byte, key string) []byte {
	t.Helper()
	linked := writerLinkedEntry("liFD", "fixture-id", 7, inner)
	global := writerLinkedTag("8BIM", "lnk2", 4, linked.data, 4)
	placement := writerLinkedTag("8BIM", "SoLd", 4, smartEditPlacement(t, 0, ""), 2)
	if key != "" {
		placement = append(placement, writerLinkedTag("8BIM", key, 4, []byte{0, 0}, 2)...)
	}
	stale := smartEditImage([4]color.NRGBA{{30, 30, 30, 255}, {30, 30, 30, 255},
		{30, 30, 30, 255}, {30, 30, 30, 255}})
	record, payloads := smartEditLayer(1, "Stale cache", 40, image.Rect(0, 0, 2, 2), stale, placement, false)
	layers := binary.BigEndian.AppendUint16(nil, 1)
	layers = append(layers, record...)
	layers = append(layers, payloads...)
	if len(layers)%2 != 0 {
		layers = append(layers, 0)
	}
	return smartEditDocument(1, 2, 2, 3, layers, global, append([]byte{0, 0}, make([]byte, 12)...))
}

func TestSceneUsesNestedContentsInsteadOfSavedCaches(t *testing.T) {
	wanted := smartEditImage(smartEditColors())
	data := sceneNestedFixture(t, sceneNestedFixture(t, smartEditPrototype(2, wanted), ""), "")
	before := bytes.Clone(data)
	source, err := psdfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	result, err := psdscene.Render(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Pix, wanted.Pix) || !bytes.Equal(before, data) {
		t.Fatal("nested result used stale smart-object or merged caches, or modified source bytes")
	}
	result.Pix[0] = 0
	again, err := psdscene.Render(context.Background(), source)
	if err != nil || again.Pix[0] != wanted.Pix[0] {
		t.Fatalf("returned scene pixels alias cached/source state: %v", err)
	}
}

func TestSceneContentDepthAndWorkingBudgets(t *testing.T) {
	wanted := smartEditImage(smartEditColors())
	source, err := psdfile.Parse(sceneNestedFixture(t, smartEditPrototype(1, wanted), ""))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	for _, options := range []psdscene.Options{{MaxDepth: 1}, {MaxDocuments: 1}, {MaxWorkingBytes: 32}} {
		if _, err := psdscene.Render(context.Background(), source, options); !errors.Is(err, psdfile.ErrLimit) {
			t.Fatalf("scene limits=%+v error=%v", options, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := psdscene.Render(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestSceneRequiresExplicitCachedSpecialLayerPolicy(t *testing.T) {
	wanted := smartEditImage(smartEditColors())
	source, err := psdfile.Parse(sceneNestedFixture(t, smartEditPrototype(1, wanted), "TySh"))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err := psdscene.Render(context.Background(), source); !errors.Is(err, psdfile.ErrUnsupported) {
		t.Fatalf("unsupported text semantics silently use cache: %v", err)
	}
	if _, err := psdscene.Render(context.Background(), source, psdscene.Options{CachedSpecialLayers: true}); err != nil {
		t.Fatalf("explicit cached policy failed: %v", err)
	}
}
