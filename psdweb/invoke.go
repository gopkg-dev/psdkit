package psdweb

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"reflect"

	"github.com/gopkg-dev/psdkit/psdfont"
)

type boundedBuffer struct {
	bytes.Buffer
	limit int64
}

func (buffer *boundedBuffer) Write(data []byte) (int, error) {
	if int64(len(data)) > buffer.limit-int64(buffer.Len()) {
		return 0, ErrLimit
	}
	return buffer.Buffer.Write(data)
}

func (e *Engine) invoke(ctx context.Context, request Request) (any, error) {
	operation, exists := e.bindings[request.Operation]
	if !exists || !operation.function.IsValid() && operation.method == "" {
		return nil, fmt.Errorf("%w: unknown operation %q", ErrProtocol, request.Operation)
	}
	function, owner, inputAsset := operation.function, "", ""
	if operation.method != "" {
		target, exists := e.resources[request.Target]
		if !exists || target.kind != operation.capability.TargetKind {
			return nil, fmt.Errorf("%w: select a %s handle", ErrProtocol, operation.capability.TargetKind)
		}
		function, owner, inputAsset = target.value.MethodByName(operation.method), target.owner, target.assetID
	}
	writer := &boundedBuffer{limit: e.config.MaxOutputBytes}
	arguments, position := []reflect.Value{}, 0
	for index, declared := range operation.argumentTypes {
		kind := declared
		variadic := operation.variadic && index == len(operation.argumentTypes)-1
		if variadic {
			kind = declared.Elem()
			if !injected(kind) && position >= len(request.Args) {
				arguments = append(arguments, reflect.MakeSlice(declared, 0, 0))
				continue
			}
		}
		var value reflect.Value
		var err error
		switch kind {
		case contextType:
			value = reflect.ValueOf(ctx)
		case writerType:
			value = reflect.ValueOf(io.Writer(writer))
		default:
			var raw jsontext.Value
			if !injected(kind) {
				if position < len(request.Args) {
					raw = request.Args[position]
				} else if !variadic {
					return nil, fmt.Errorf("%w: missing parameter %s", ErrProtocol, operation.argumentNames[index])
				}
				position++
				var reference struct {
					Ref   string `json:"$ref"`
					Asset string `json:"$asset"`
				}
				if len(raw) > 0 && raw[0] == '{' {
					if err := json.Unmarshal(raw, &reference); err != nil {
						return nil, fmt.Errorf("%w: invalid reference: %w", ErrProtocol, err)
					}
				}
				if item := e.resources[reference.Ref]; item != nil && owner == "" {
					owner = item.owner
				}
				if reference.Asset != "" && inputAsset == "" {
					inputAsset = reference.Asset
				}
			}
			value, err = e.argument(ctx, raw, kind)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrProtocol, operation.argumentNames[index], err)
		}
		if variadic {
			values := reflect.MakeSlice(declared, 1, 1)
			values.Index(0).Set(value)
			value = values
		}
		arguments = append(arguments, value)
	}
	if position < len(request.Args) {
		return nil, fmt.Errorf("%w: too many arguments", ErrProtocol)
	}
	var returned []reflect.Value
	if operation.variadic {
		returned = function.CallSlice(arguments)
	} else {
		returned = function.Call(arguments)
	}
	if len(returned) != 0 && returned[len(returned)-1].Type().Implements(reflect.TypeFor[error]()) {
		failure := returned[len(returned)-1]
		returned = returned[:len(returned)-1]
		if !failure.IsNil() {
			return nil, failure.Interface().(error)
		}
	}
	result := make([]any, 0, len(returned))
	for _, value := range returned {
		if value.Kind() == reflect.Pointer && value.IsNil() {
			result = append(result, nil)
			continue
		}
		face, isFace := reflect.TypeAssert[*psdfont.Face](value)
		if isFace {
			alias := request.Alias
			if alias == "" {
				alias = face.PostScriptName
			}
			if _, exists := e.fonts[alias]; exists {
				return nil, fmt.Errorf("%w: font alias already loaded", ErrProtocol)
			}
		}
		view, err := e.exportValue(value, 0)
		if err != nil {
			return nil, err
		}
		if value.Kind() == reflect.Pointer || value.Kind() == reflect.Struct || value.Kind() == reflect.Slice {
			id, err := e.keep(value, owner, inputAsset)
			if err != nil {
				return nil, err
			}
			view = map[string]any{"handle": id, "kind": resourceKind(value.Type()),
				"type": value.Type().String(), "value": view}
			if isFace {
				alias := request.Alias
				if alias == "" {
					alias = face.PostScriptName
				}
				e.fonts[alias] = face
			}
		}
		result = append(result, view)
	}
	if writer.Len() != 0 {
		name, mime := "data.bin", "application/octet-stream"
		if request.Operation == "editor.Write" || request.Operation == "editor.WritePSD" {
			name, mime = "export.psd", "image/vnd.adobe.photoshop"
		}
		if request.Operation == "image.encode" {
			name, mime = "export.png", "image/png"
			format := string(request.Args[len(request.Args)-1])
			if format == `"jpg"` || format == `"jpeg"` {
				name, mime = "export.jpg", "image/jpeg"
			}
		}
		blob, err := e.binary(writer.Bytes(), name, mime)
		if err != nil {
			return nil, err
		}
		result = append(result, blob)
	}
	return map[string]any{"results": result}, nil
}
