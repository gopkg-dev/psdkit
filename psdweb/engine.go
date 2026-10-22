package psdweb

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/gopkg-dev/psdkit/psdadjust"
	"github.com/gopkg-dev/psdkit/psdcolor"
	"github.com/gopkg-dev/psdkit/psdcompose"
	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdeffects"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdfont"
	"github.com/gopkg-dev/psdkit/psdgradient"
	"github.com/gopkg-dev/psdkit/psdpattern"
	"github.com/gopkg-dev/psdkit/psdprecision"
	"github.com/gopkg-dev/psdkit/psdtext"
	"github.com/gopkg-dev/psdkit/psdvector"
	"github.com/gopkg-dev/psdkit/psdwarp"
)

type asset struct {
	id, name, kind string
	data           []byte
	pixels         *image.NRGBA
}

type resource struct {
	id, kind, owner string
	value           reflect.Value
	assetID         string
	retainedBytes   int64
}

type binding struct {
	capability    Capability
	function      reflect.Value
	method        string
	argumentTypes []reflect.Type
	argumentNames []string
	variadic      bool
}

// Engine owns all bytes and handles inside one worker. Execute calls are serialized.
// MaxBytes bounds asset storage and individual library work, not the browser's total process heap.
type Engine struct {
	mu          sync.Mutex
	config      Config
	assets      map[string]*asset
	resources   map[string]*resource
	bindings    map[string]binding
	fonts       map[string]*psdfont.Face
	sequence    uint64
	assetBytes  int64
	valueBytes  int64
	links       map[string]string
	binaries    []Binary
	imageOutput string
}

// New returns an independent in-memory workbench with no filesystem or network resolver.
func New(config Config) (*Engine, error) {
	if config.MaxBytes < 0 || config.MaxPixels < 0 || config.MaxAssets < 0 || config.MaxResources < 0 ||
		config.MaxJSONBytes < 0 || config.MaxOutputBytes < 0 {
		return nil, ErrLimit
	}
	if config.MaxBytes == 0 {
		config.MaxBytes = 256 << 20
	}
	if config.MaxPixels == 0 {
		config.MaxPixels = 16_000_000
	}
	if config.MaxAssets == 0 {
		config.MaxAssets = 128
	}
	if config.MaxResources == 0 {
		config.MaxResources = 512
	}
	if config.MaxJSONBytes == 0 {
		config.MaxJSONBytes = 16 << 20
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = 256 << 20
	}
	engine := &Engine{config: config, assets: map[string]*asset{}, resources: map[string]*resource{},
		bindings: map[string]binding{}, fonts: map[string]*psdfont.Face{}}
	engine.registerOperations()
	return engine, nil
}

func (e *Engine) next(prefix string) string {
	e.sequence++
	return prefix + strconv.FormatUint(e.sequence, 36)
}

// Catalog returns owned operation descriptions and complete parameter schemas.
func (e *Engine) Catalog() []Capability {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.catalog()
}

func (e *Engine) catalog() []Capability {
	result := make([]Capability, 0, len(e.bindings))
	for _, operation := range e.bindings {
		result = append(result, operation.capability)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	data, err := json.Marshal(result)
	if err != nil {
		panic(fmt.Errorf("psdweb: invalid capability catalog: %w", err))
	}
	var owned []Capability
	if err := json.Unmarshal(data, &owned); err != nil {
		panic(fmt.Errorf("psdweb: invalid capability copy: %w", err))
	}
	return owned
}

// Execute processes one bounded request. Input bytes are used only by asset.put and are copied there.
// A context cancellation never returns a successful partial result.
func (e *Engine) Execute(ctx context.Context, request Request, input []byte) (response Response) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if request.TimeoutMS < 0 || request.TimeoutMS > 120_000 {
		return failed(fmt.Errorf("%w: timeoutMS must be in 0..120000", ErrProtocol))
	}
	if request.ImageOutput != "" && request.ImageOutput != "png" {
		return failed(fmt.Errorf("%w: imageOutput must be empty or png", ErrProtocol))
	}
	if request.TimeoutMS != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(request.TimeoutMS)*time.Millisecond)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > e.config.MaxJSONBytes {
		return failed(ErrLimit)
	}
	e.links, e.binaries = request.Links, nil
	e.imageOutput = request.ImageOutput
	previousResources, previousAssets := map[string]bool{}, map[string]bool{}
	for id := range e.resources {
		previousResources[id] = true
	}
	for id := range e.assets {
		previousAssets[id] = true
	}
	defer func() {
		if problem := recover(); problem != nil {
			response = failed(fmt.Errorf("psdweb internal operation failed: %v", problem))
		}
		if !response.OK {
			for id := range e.resources {
				if !previousResources[id] {
					_ = e.release(id)
				}
			}
			for id := range e.assets {
				if !previousAssets[id] {
					_ = e.removeAsset(id)
				}
			}
		}
		e.links, e.binaries = nil, nil
		e.imageOutput = ""
	}()
	var result any
	switch request.Operation {
	case "system.capabilities":
		result = e.catalog()
	case "system.state":
		result = e.state()
	case "system.reset":
		err = e.reset()
		result = e.state()
	case "asset.put":
		result, err = e.putAsset(request, input)
	case "asset.remove":
		err = e.removeAsset(request.Target)
		result = e.state()
	case "resource.release":
		err = e.release(request.Target)
		result = e.state()
	case "document.open":
		result, err = e.openDocument(ctx, request)
	case "document.create":
		result, err = e.createDocument(ctx, request, false)
	case "demo.create":
		result, err = e.createDocument(ctx, request, true)
	default:
		result, err = e.invoke(ctx, request)
	}
	if err != nil {
		return failed(err)
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		return failed(err)
	}
	if len(data) > e.config.MaxJSONBytes {
		return failed(ErrLimit)
	}
	return Response{OK: true, Data: result, Binaries: e.binaries}
}

func failed(err error) Response {
	fault := &Fault{Code: "operation_failed", Message: err.Error()}
	switch {
	case errors.Is(err, ErrLimit), errors.Is(err, psdfile.ErrLimit), errors.Is(err, psdfont.ErrLimit),
		errors.Is(err, psdcolor.ErrLimit), errors.Is(err, psdcompose.ErrLimit), errors.Is(err, psdeffects.ErrLimit),
		errors.Is(err, psdadjust.ErrLimit), errors.Is(err, psdgradient.ErrLimit), errors.Is(err, psdpattern.ErrLimit),
		errors.Is(err, psdprecision.ErrLimit), errors.Is(err, psdtext.ErrLimit), errors.Is(err, psdvector.ErrLimit),
		errors.Is(err, psdfilter.ErrLimit):
		fault.Code = "resource_limit"
	case errors.Is(err, psdwarp.ErrUnsupported), errors.Is(err, psdedit.ErrUnsupported),
		errors.Is(err, psdfont.ErrUnsupported), errors.Is(err, psdcolor.ErrUnsupported),
		errors.Is(err, psdfilter.ErrUnsupported), errors.Is(err, psdfile.ErrUnsupported),
		errors.Is(err, psdcompose.ErrUnsupported), errors.Is(err, psdeffects.ErrUnsupported),
		errors.Is(err, psdadjust.ErrUnsupported), errors.Is(err, psdgradient.ErrUnsupported),
		errors.Is(err, psdpattern.ErrUnsupported), errors.Is(err, psdprecision.ErrUnsupported),
		errors.Is(err, psdtext.ErrUnsupported), errors.Is(err, psdvector.ErrUnsupported):
		fault.Code = "unsupported"
	case errors.Is(err, psdfont.ErrMissingFont):
		fault.Code = "missing_font"
	case errors.Is(err, psdfont.ErrMissingGlyph):
		fault.Code = "missing_glyph"
	case errors.Is(err, ErrProtocol):
		fault.Code = "invalid_request"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		fault.Code = "canceled"
	}
	if fileError, ok := errors.AsType[*psdfile.Error](err); ok {
		fault.Path, fault.Feature, fault.Offset = fileError.Path, fileError.Feature, fileError.Offset
		if errors.Is(err, psdfile.ErrUnsupported) {
			fault.Code = "unsupported"
		}
	}
	if fontError, ok := errors.AsType[*psdfont.Error](err); ok {
		fault.Path, fault.Offset = fontError.Path, fontError.Offset
	}
	if projectionError, ok := errors.AsType[*projectionFailure](err); ok {
		fault.Audit = &projectionError.audit
	}
	return Response{Error: fault}
}

func (e *Engine) state() any {
	assets := make([]any, 0, len(e.assets))
	for _, item := range e.assets {
		assets = append(assets, map[string]any{"id": item.id, "name": item.name, "kind": item.kind,
			"bytes": len(item.data)})
	}
	sort.Slice(assets, func(i, j int) bool {
		return assets[i].(map[string]any)["id"].(string) < assets[j].(map[string]any)["id"].(string)
	})
	resources := make([]any, 0, len(e.resources))
	for _, item := range e.resources {
		resources = append(resources, map[string]any{"id": item.id, "kind": item.kind,
			"type": item.value.Type().String(), "owner": item.owner})
	}
	return map[string]any{"assets": assets, "resources": resources, "assetBytes": e.assetBytes,
		"retainedPixelAndBinaryBytes": e.valueBytes, "limits": e.config}
}

func (e *Engine) putAsset(request Request, input []byte) (any, error) {
	if len(input) == 0 || int64(len(input)) > e.config.MaxBytes-e.assetBytes || len(e.assets) >= e.config.MaxAssets {
		return nil, ErrLimit
	}
	item := &asset{id: e.next("a"), name: request.Name, kind: request.Kind, data: bytes.Clone(input)}
	e.assets[item.id], e.assetBytes = item, e.assetBytes+int64(len(item.data))
	return map[string]any{"asset": item.id, "name": item.name, "kind": item.kind, "bytes": len(item.data)}, nil
}

func (e *Engine) removeAsset(id string) error {
	item, exists := e.assets[id]
	if !exists {
		return fmt.Errorf("%w: asset %q not found", ErrProtocol, id)
	}
	for _, handle := range e.resources {
		if handle.assetID == id {
			return fmt.Errorf("%w: release dependent resources first", ErrProtocol)
		}
	}
	e.assetBytes -= int64(len(item.data))
	if item.pixels != nil {
		e.valueBytes -= int64(len(item.pixels.Pix))
	}
	delete(e.assets, id)
	return nil
}

func (e *Engine) keep(value reflect.Value, owner, assetID string) (string, error) {
	if len(e.resources) >= e.config.MaxResources {
		return "", ErrLimit
	}
	retained := retainedValueBytes(value)
	if retained > e.config.MaxBytes-e.valueBytes {
		return "", ErrLimit
	}
	id := e.next("r")
	if owner == "" {
		owner = id
	}
	e.resources[id] = &resource{id: id, owner: owner, assetID: assetID,
		kind: resourceKind(value.Type()), value: value, retainedBytes: retained}
	e.valueBytes += retained
	return id, nil
}

func retainedValueBytes(value reflect.Value) int64 {
	switch item := value.Interface().(type) {
	case *image.NRGBA:
		return int64(cap(item.Pix))
	case *image.Alpha:
		return int64(cap(item.Pix))
	case []byte:
		return int64(len(item))
	case []float64:
		return int64(len(item)) * 8
	}
	return 0
}

func (e *Engine) release(id string) error {
	selected, exists := e.resources[id]
	if !exists {
		return fmt.Errorf("%w: handle %q not found", ErrProtocol, id)
	}
	var failures []error
	for key, item := range e.resources {
		if key != id && (selected.owner != id || item.owner != id) {
			continue
		}
		if closer, valid := reflect.TypeAssert[io.Closer](item.value); valid {
			failures = append(failures, closer.Close())
		}
		if face, valid := reflect.TypeAssert[*psdfont.Face](item.value); valid {
			for name, registered := range e.fonts {
				if registered == face {
					delete(e.fonts, name)
				}
			}
		}
		e.valueBytes -= item.retainedBytes
		delete(e.resources, key)
	}
	return errors.Join(failures...)
}

func (e *Engine) reset() error {
	var failures []error
	for id := range e.resources {
		if _, exists := e.resources[id]; exists {
			failures = append(failures, e.release(id))
		}
	}
	e.assets, e.fonts, e.assetBytes, e.valueBytes = map[string]*asset{}, map[string]*psdfont.Face{}, 0, 0
	return errors.Join(failures...)
}

func (e *Engine) imageAsset(ctx context.Context, id string) (*image.NRGBA, error) {
	item, exists := e.assets[id]
	if !exists {
		return nil, fmt.Errorf("%w: image asset %q not found", ErrProtocol, id)
	}
	if item.pixels != nil {
		return item.pixels, nil
	}
	configuration, _, err := image.DecodeConfig(bytes.NewReader(item.data))
	if err != nil {
		return nil, err
	}
	switch configuration.ColorModel.Convert(color.NRGBA64{}).(type) {
	case color.RGBA64, color.NRGBA64, color.Gray16:
		return nil, fmt.Errorf("%w: image operation requires RGB8; original high-depth bytes are retained",
			psdfile.ErrUnsupported)
	}
	width, height := int64(configuration.Width), int64(configuration.Height)
	if width <= 0 || height <= 0 || width > e.config.MaxPixels/height || width > e.config.MaxBytes/12/height {
		return nil, ErrLimit
	}
	if width*height*4 > e.config.MaxBytes-e.valueBytes {
		return nil, ErrLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoded, _, err := image.Decode(bytes.NewReader(item.data))
	if err != nil {
		return nil, err
	}
	pixels := image.NewNRGBA(image.Rect(0, 0, configuration.Width, configuration.Height))
	for row := range configuration.Height {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for column := range configuration.Width {
			pixels.Set(column, row, decoded.At(column, row))
		}
	}
	item.pixels = pixels
	e.valueBytes += int64(len(pixels.Pix))
	return pixels, nil
}
