package psdweb

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"image"
	"image/color"
	"image/png"
	"reflect"
	"testing"

	"github.com/gopkg-dev/psdkit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdmodel"
)

func bridgeCall(t *testing.T, engine *Engine, operation, target string, arguments ...any) Response {
	t.Helper()
	request := Request{Operation: operation, Target: target}
	for _, argument := range arguments {
		encoded, err := json.Marshal(argument, json.Deterministic(true))
		if err != nil {
			t.Fatal(err)
		}
		request.Args = append(request.Args, encoded)
	}
	response := engine.Execute(context.Background(), request, nil)
	if !response.OK {
		t.Fatalf("%s: %+v", operation, response.Error)
	}
	return response
}

func bridgeAsset(t *testing.T, engine *Engine, name string, data []byte) string {
	t.Helper()
	response := engine.Execute(nil, Request{Operation: "asset.put", Name: name}, data)
	if !response.OK {
		t.Fatal(response.Error)
	}
	return response.Data.(map[string]any)["asset"].(string)
}

func ref(id string) map[string]string      { return map[string]string{"$ref": id} }
func assetRef(id string) map[string]string { return map[string]string{"$asset": id} }

func TestDocumentEditPNGAndPSDMemoryRoundTrip(t *testing.T) {
	engine, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	opened := bridgeCall(t, engine, "demo.create", "", 96, 80, 2).Data.(map[string]any)
	document, editor := opened["document"].(string), opened["editor"].(string)
	if opened["header"].(psdfile.Header).Version != 2 {
		t.Fatal("PSB version was lost")
	}
	before := bytes.Clone(engine.assets[opened["asset"].(string)].data)
	tree := bridgeCall(t, engine, "editor.Tree", editor)
	if len(tree.Data.(map[string]any)["results"].([]any)) != 1 {
		t.Fatal("tree result missing")
	}
	bridgeCall(t, engine, "editor.Rename", editor, 1, "浏览器改名")
	preview := bridgeCall(t, engine, "editor.Composite", editor, map[string]any{})
	if len(preview.Binaries) != 2 || preview.Binaries[0].MIME != "image/png" ||
		preview.Binaries[1].MIME != "application/x-rgba8" {
		t.Fatal("PNG/raw buffers are not separate")
	}
	pixels, err := png.Decode(bytes.NewReader(preview.Binaries[0].Data))
	if err != nil || pixels.Bounds().Size() != image.Pt(96, 80) {
		t.Fatal("real composite not encoded", err)
	}
	saved := bridgeCall(t, engine, "editor.WritePSD", editor, map[string]any{})
	if len(saved.Binaries) != 1 {
		t.Fatal("PSD writer did not return bytes")
	}
	reopened, err := psdkit.ParseDocument(saved.Binaries[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Header.Version != 2 || reopened.Layers[0].Name != "浏览器改名" {
		t.Fatal("edited name or PSB version did not survive save")
	}
	if !bytes.Equal(before, engine.assets[opened["asset"].(string)].data) {
		t.Fatal("immutable source was edited")
	}
	blocked := engine.Execute(nil, Request{Operation: "asset.remove", Target: opened["asset"].(string)}, nil)
	if blocked.OK {
		t.Fatal("source asset dropped while document borrows it")
	}
	bridgeCall(t, engine, "resource.release", document)
	if len(engine.resources) != 0 {
		t.Fatal("closing document leaked derived resources")
	}
	bridgeCall(t, engine, "asset.remove", opened["asset"].(string))
}

func TestAssetOwnershipHiddenRGBAndBridgeLimits(t *testing.T) {
	engine, _ := New(Config{MaxBytes: 1 << 20, MaxPixels: 1024})
	pixels := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	pixels.SetNRGBA(0, 0, color.NRGBA{R: 17, G: 231, B: 53})
	pixels.SetNRGBA(1, 0, color.NRGBA{R: 231, G: 12, B: 119, A: 173})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	input := bytes.Clone(encoded.Bytes())
	id := bridgeAsset(t, engine, "hidden.png", input)
	clear(input)
	decoded, err := engine.imageAsset(context.Background(), id)
	if err != nil || !bytes.Equal(decoded.Pix, pixels.Pix) {
		t.Fatal("image import changed original RGBA", err)
	}
	response := engine.Execute(nil, Request{Operation: "demo.create"}, nil)
	if response.OK || response.Error.Code != "resource_limit" {
		t.Fatal("image budget not enforced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response = engine.Execute(ctx, Request{Operation: "asset.put"}, []byte{1})
	if response.OK || response.Error.Code != "canceled" || len(engine.assets) != 1 {
		t.Fatal("canceled input committed")
	}
	response = engine.Execute(nil, Request{Operation: "editor.Save", Target: "missing"}, nil)
	if response.OK || response.Error.Code != "invalid_request" {
		t.Fatal("filesystem method became callable")
	}
}

func TestCatalogCoversPublicEditingAndSourceMethods(t *testing.T) {
	engine, _ := New(Config{})
	if len(engine.Catalog()) < 100 {
		t.Fatal("capability catalog is incomplete")
	}
	for _, prototype := range []struct {
		prefix string
		value  any
	}{
		{"editor", (*psdkit.Editor)(nil)}, {"document", (*psdfile.Document)(nil)},
	} {
		kind := reflect.TypeOf(prototype.value)
		for method := range kind.Methods() {
			name := method.Name
			if name == "Close" || name == "Save" || name == "SavePSD" || name == "ReadAt" {
				continue
			}
			operation, exists := engine.bindings[prototype.prefix+"."+name]
			if !exists || operation.capability.Description == "" {
				t.Fatalf("missing Go contract for %s", name)
			}
		}
	}
	catalog := engine.Catalog()
	catalog[0].Label = "mutated"
	if engine.Catalog()[0].Label == "mutated" {
		t.Fatal("catalog aliases internal descriptions")
	}
}

func TestUniformWarpGateIsNotBypassedByBridge(t *testing.T) {
	engine, _ := New(Config{})
	placed := &psdmodel.Placed{Width: 64, Height: 64, Warp: &psdmodel.Warp{Style: "custom"},
		Transform: []float64{0, 0, 64, 0, 64, 64, 0, 64}}
	encoded, _ := json.Marshal(placed, json.Deterministic(true))
	response := engine.Execute(nil, Request{Operation: "warp.uniform.compile", Args: []jsontext.Value{encoded}}, nil)
	if response.OK || response.Error.Code != "unsupported" {
		t.Fatalf("missing quilt metadata accepted: %+v", response)
	}
	if len(engine.resources) != 0 || len(response.Binaries) != 0 {
		t.Fatal("blocked geometry returned output")
	}
}
