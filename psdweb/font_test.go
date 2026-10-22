package psdweb

import (
	"context"
	"testing"

	"github.com/gopkg-dev/psdkit/internal/webfixtures"
)

func TestExplicitFontBytesLayoutRasterAndLifetime(t *testing.T) {
	engine, _ := New(Config{})
	fontAsset := bridgeAsset(t, engine, "fixture.ttf", webfixtures.Font())
	decoded := bridgeCall(t, engine, "font.decode", "", assetRef(fontAsset), map[string]any{})
	face := decoded.Data.(map[string]any)["results"].([]any)[0].(map[string]any)
	if face["value"].(map[string]any)["PostScriptName"] != "BridgeFixture-Regular" {
		t.Fatal("font metadata changed")
	}
	layout := bridgeCall(t, engine, "font.layout", "", "AV",
		map[string]any{"FontName": "BridgeFixture-Regular", "Size": 48}, map[string]any{"OriginY": 60})
	run := layout.Data.(map[string]any)["results"].([]any)[0].(map[string]any)
	raster := bridgeCall(t, engine, "font.raster", "", ref(run["handle"].(string)),
		map[string]any{"Min": map[string]int{"X": 0, "Y": 0}, "Max": map[string]int{"X": 140, "Y": 90}}, map[string]any{})
	if len(raster.Binaries) == 0 || raster.Binaries[0].MIME != "image/png" {
		t.Fatal("no real glyph raster PNG")
	}
	bridgeCall(t, engine, "resource.release", face["handle"].(string))
	if _, err := engine.resolveFont(context.Background(), "BridgeFixture-Regular"); err == nil {
		t.Fatal("released font substituted")
	}
}
