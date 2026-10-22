package psdweb

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"image"
	"image/color"
	"image/png"
	"math"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/webfixtures"
)

func TestExactLargeIntegersAcrossJSON(t *testing.T) {
	engine, _ := New(Config{})
	for _, original := range []any{int64(math.MinInt64), int64(math.MaxInt64), uint64(math.MaxUint64)} {
		view, err := engine.exportValue(reflect.ValueOf(original), 0)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(view, json.Deterministic(true))
		if err != nil || !bytes.Contains(encoded, []byte(`"$int64"`)) {
			t.Fatal("unsafe integer was sent as a JavaScript number", err)
		}
		decoded, err := engine.argument(context.Background(), encoded, reflect.TypeOf(original))
		if err != nil || decoded.Interface() != original {
			t.Fatalf("lost integer precision: %s %v", encoded, err)
		}
	}
	_, err := engine.argument(context.Background(), jsontext.Value(`{"$int64":"256"}`), reflect.TypeFor[uint8]())
	if err == nil {
		t.Fatal("integer overflow accepted")
	}
}

func TestHighDepthImageIsNotSilentlyQuantized(t *testing.T) {
	engine, _ := New(Config{})
	pixels := image.NewNRGBA64(image.Rect(0, 0, 2, 1))
	pixels.SetNRGBA64(0, 0, color.NRGBA64{R: 257, G: 1023, B: 40001, A: 65535})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	id := bridgeAsset(t, engine, "high-depth.png", encoded.Bytes())
	if _, err := engine.imageAsset(context.Background(), id); err == nil {
		t.Fatal("RGB16 implicitly converted to RGB8")
	}
	if !bytes.Equal(engine.assets[id].data, encoded.Bytes()) || engine.assets[id].pixels != nil {
		t.Fatal("original source was changed")
	}
}

func TestFailedOutputDoesNotRetainNewResourcesOrFontAliases(t *testing.T) {
	engine, _ := New(Config{MaxResources: 1})
	response := engine.Execute(nil, Request{Operation: "demo.create"}, nil)
	if response.OK || len(engine.resources) != 0 || len(engine.assets) != 0 {
		t.Fatal("failed document creation retained partial state")
	}
	fontAsset := bridgeAsset(t, engine, "fixture.ttf", webfixtures.Font())
	engine.config.MaxJSONBytes = 256
	encoded, _ := json.Marshal(assetRef(fontAsset), json.Deterministic(true))
	response = engine.Execute(nil, Request{Operation: "font.decode", Alias: "test-alias",
		Args: []jsontext.Value{encoded, jsontext.Value(`{}`)}}, nil)
	if response.OK || len(engine.resources) != 0 || len(engine.fonts) != 0 {
		t.Fatal("failed font result leaked a resolver alias")
	}
	engine.config.MaxJSONBytes = 16 << 20
	response = engine.Execute(nil, Request{Operation: "font.decode", Alias: "test-alias",
		Args: []jsontext.Value{encoded, jsontext.Value(`{}`)}}, nil)
	if !response.OK {
		t.Fatal(response.Error)
	}
	face := response.Data.(map[string]any)["results"].([]any)[0].(map[string]any)
	bridgeCall(t, engine, "resource.release", face["handle"].(string))
	if _, err := engine.resolveFont(context.Background(), "test-alias"); err == nil {
		t.Fatal("released alias still resolved")
	}
}

func TestRetainedPixelBudgetIsReleased(t *testing.T) {
	engine, _ := New(Config{MaxBytes: 64})
	first, err := engine.keep(reflect.ValueOf(image.NewNRGBA(image.Rect(0, 0, 4, 4))), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.keep(reflect.ValueOf([]byte{1}), "", ""); err == nil {
		t.Fatal("retained buffers exceeded budget")
	}
	if err := engine.release(first); err != nil || engine.valueBytes != 0 {
		t.Fatal("release did not reclaim buffer budget", err)
	}
	if _, err := engine.keep(reflect.ValueOf([]byte{1}), "", ""); err != nil {
		t.Fatal("released capacity unavailable", err)
	}
}
