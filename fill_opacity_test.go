package psdkit_test

import (
	"bytes"
	"context"
	"image"
	"testing"

	"github.com/gopkg-dev/psdkit"
)

func TestPublicFillOpacityEditorAliasAndEditablePSDPSB(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		editor, err := psdkit.CreateDocument(nil, 2, 1, psdkit.CreateOptions{Version: version})
		if err != nil {
			t.Fatal(err)
		}
		defer editor.Close()
		pixels := image.NewNRGBA(image.Rect(0, 0, 2, 1))
		copy(pixels.Pix, []byte{255, 0, 0, 255, 0, 255, 0, 255})
		if err := editor.ReplacePixels(1, pixels); err != nil {
			t.Fatal(err)
		}
		if err := editor.SetFillOpacityContext(context.Background(), 1, 128); err != nil {
			t.Fatal(err)
		}
		info, err := editor.Layer(1)
		if err != nil || info.FillOpacity != 128 || info.Opacity != 255 {
			t.Fatalf("root Editor alias did not expose effective fill: %+v %v", info, err)
		}
		var encoded bytes.Buffer
		if err := editor.WritePSD(nil, &encoded); err != nil {
			t.Fatal(err)
		}
		source, err := psdkit.ParseDocument(encoded.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		opened, err := psdkit.NewEditor(source)
		if err != nil {
			t.Fatal(err)
		}
		defer opened.Close()
		info, err = opened.Layer(1)
		if err != nil || info.FillOpacity != 128 || source.Header.Version != version {
			t.Fatalf("public save/reopen lost fill parameter: %+v %v", info, err)
		}
		if err := opened.SetFillOpacity(1, 64); err != nil {
			t.Fatal(err)
		}
	}
}
