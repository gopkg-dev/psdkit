package psdweb

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"image"
	"reflect"
	"strconv"
	"strings"

	"github.com/gopkg-dev/psdkit/psdedit"
	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfont"
)

func strconvItoa(value int) string { return strconv.Itoa(value) }

func typeSchema(kind reflect.Type, depth int) any {
	if depth > 3 {
		return map[string]any{"goType": kind.String()}
	}
	if kind.Kind() == reflect.Pointer {
		return typeSchema(kind.Elem(), depth+1)
	}
	schema := map[string]any{"goType": kind.String()}
	switch kind.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		for field := range kind.Fields() {
			if !field.IsExported() || field.Tag.Get("json") == "-" || injected(field.Type) {
				continue
			}
			properties[field.Name] = typeSchema(field.Type, depth+1)
		}
		schema["type"], schema["properties"] = "object", properties
	case reflect.Array, reflect.Slice:
		schema["type"], schema["items"] = "array", typeSchema(kind.Elem(), depth+1)
	case reflect.Bool:
		schema["type"] = "boolean"
	case reflect.String:
		schema["type"] = "string"
	default:
		schema["type"] = "number"
	}
	return schema
}

func argumentExample(operation, name string, kind reflect.Type) any {
	if kind == reflect.TypeFor[[]byte]() || kind == reflect.TypeFor[*image.NRGBA]() ||
		kind == reflect.TypeFor[*image.Alpha]() {
		return map[string]any{"$asset": ""}
	}
	if kind.Kind() == reflect.Pointer {
		if resourceKind(kind) != "value" {
			return map[string]any{"$ref": ""}
		}
		return argumentExample(operation, name, kind.Elem())
	}
	switch kind.Kind() {
	case reflect.Bool:
		return true
	case reflect.String:
		if name == "text" {
			return "Hello, psdkit"
		}
		if name == "name" {
			return "新图层"
		}
		return ""
	case reflect.Array:
		values := make([]any, kind.Len())
		for index := range values {
			values[index] = argumentExample(operation, "", kind.Elem())
		}
		return values
	case reflect.Slice:
		return []any{}
	case reflect.Struct:
		if kind == reflect.TypeFor[image.Rectangle]() {
			return map[string]any{"Min": map[string]int{"X": 0, "Y": 0}, "Max": map[string]int{"X": 640, "Y": 512}}
		}
		if kind == reflect.TypeFor[psdfont.Style]() {
			return map[string]any{"FontName": "", "Size": 48, "Kerning": true}
		}
		return map[string]any{}
	default:
		if strings.Contains(strings.ToLower(name), "opacity") {
			return 255
		}
		if name == "id" {
			return 1
		}
		return 0
	}
}

func (e *Engine) argument(ctx context.Context, raw jsontext.Value, kind reflect.Type) (reflect.Value, error) {
	if kind == fontResolverType {
		return reflect.ValueOf(psdfont.ResolverFunc(e.resolveFont)), nil
	}
	if kind.Kind() == reflect.Func {
		return e.resolver(kind)
	}
	if len(raw) == 0 || string(raw) == "null" {
		value := reflect.New(kind).Elem()
		if kind.Kind() == reflect.Struct {
			e.inject(value)
		}
		return value, nil
	}
	var reference struct {
		Ref   string `json:"$ref"`
		Asset string `json:"$asset"`
		Path  []any  `json:"path"`
	}
	if raw[0] == '{' {
		if err := json.Unmarshal(raw, &reference); err != nil {
			return reflect.Value{}, err
		}
	}
	signed := kind.Kind() >= reflect.Int && kind.Kind() <= reflect.Int64
	unsigned := kind.Kind() >= reflect.Uint && kind.Kind() <= reflect.Uint64
	if signed || unsigned {
		var exact struct {
			Integer string `json:"$int64"`
		}
		if raw[0] == '{' {
			if err := json.Unmarshal(raw, &exact); err != nil {
				return reflect.Value{}, err
			}
		}
		if exact.Integer != "" {
			value := reflect.New(kind).Elem()
			if kind.Kind() <= reflect.Int64 {
				parsed, err := strconv.ParseInt(exact.Integer, 10, kind.Bits())
				if err != nil {
					return reflect.Value{}, err
				}
				value.SetInt(parsed)
			} else {
				parsed, err := strconv.ParseUint(exact.Integer, 10, kind.Bits())
				if err != nil {
					return reflect.Value{}, err
				}
				value.SetUint(parsed)
			}
			return value, nil
		}
	}
	if reference.Ref != "" {
		item, exists := e.resources[reference.Ref]
		if !exists {
			return reflect.Value{}, fmt.Errorf("%w: unknown resource %s", ErrProtocol, reference.Ref)
		}
		value := item.value
		for _, component := range reference.Path {
			for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
				value = value.Elem()
			}
			switch index := component.(type) {
			case string:
				if value.Kind() != reflect.Struct {
					return reflect.Value{}, ErrProtocol
				}
				value = value.FieldByName(index)
			case float64:
				if value.Kind() != reflect.Slice || index < 0 || index != float64(int(index)) || int(index) >= value.Len() {
					return reflect.Value{}, ErrProtocol
				}
				value = value.Index(int(index))
			default:
				return reflect.Value{}, ErrProtocol
			}
			if !value.IsValid() || !value.CanInterface() {
				return reflect.Value{}, ErrProtocol
			}
		}
		if value.Type().AssignableTo(kind) {
			return value, nil
		}
		if value.Kind() == reflect.Pointer && value.Elem().Type().AssignableTo(kind) {
			return value.Elem(), nil
		}
		return reflect.Value{}, fmt.Errorf("%w: %s cannot be used as %s", ErrProtocol, value.Type(), kind)
	}
	if reference.Asset != "" {
		item, exists := e.assets[reference.Asset]
		if !exists {
			return reflect.Value{}, fmt.Errorf("%w: asset %s not found", ErrProtocol, reference.Asset)
		}
		if kind == reflect.TypeFor[[]byte]() {
			return reflect.ValueOf(bytes.Clone(item.data)), nil
		}
		if kind == reflect.TypeFor[*image.NRGBA]() {
			pixels, err := e.imageAsset(ctx, reference.Asset)
			return reflect.ValueOf(pixels), err
		}
		if kind == reflect.TypeFor[*image.Alpha]() {
			pixels, err := e.imageAsset(ctx, reference.Asset)
			if err != nil {
				return reflect.Value{}, err
			}
			alpha := image.NewAlpha(pixels.Rect)
			for row := range pixels.Rect.Dy() {
				for column := range pixels.Rect.Dx() {
					alpha.Pix[row*alpha.Stride+column] = pixels.Pix[row*pixels.Stride+column*4+3]
				}
			}
			return reflect.ValueOf(alpha), nil
		}
		return reflect.Value{}, fmt.Errorf("%w: asset is not compatible with %s", ErrProtocol, kind)
	}
	value := reflect.New(kind).Elem()
	switch kind.Kind() {
	case reflect.Pointer:
		child, err := e.argument(ctx, raw, kind.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		value.Set(reflect.New(kind.Elem()))
		value.Elem().Set(child)
	case reflect.Struct:
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(raw, &fields); err != nil {
			return reflect.Value{}, err
		}
		for key, data := range fields {
			found := false
			for index := 0; index < kind.NumField(); index++ {
				field := kind.Field(index)
				if !field.IsExported() || !strings.EqualFold(key, field.Name) {
					continue
				}
				child, err := e.argument(ctx, data, field.Type)
				if err != nil {
					return reflect.Value{}, fmt.Errorf("%s: %w", key, err)
				}
				value.Field(index).Set(child)
				found = true
				break
			}
			if !found {
				return reflect.Value{}, fmt.Errorf("%w: unknown %s field %q", ErrProtocol, kind, key)
			}
		}
		e.inject(value)
	case reflect.Slice, reflect.Array:
		if kind == reflect.TypeFor[[]byte]() {
			if err := json.Unmarshal(raw, value.Addr().Interface()); err != nil {
				return reflect.Value{}, err
			}
			break
		}
		var entries []jsontext.Value
		if err := json.Unmarshal(raw, &entries); err != nil {
			return reflect.Value{}, err
		}
		if kind.Kind() == reflect.Array && len(entries) != kind.Len() {
			return reflect.Value{}, ErrProtocol
		}
		if kind.Kind() == reflect.Slice {
			value.Set(reflect.MakeSlice(kind, len(entries), len(entries)))
		}
		for index, data := range entries {
			child, err := e.argument(ctx, data, kind.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			value.Index(index).Set(child)
		}
	case reflect.Map:
		if kind.Key().Kind() != reflect.String {
			return reflect.Value{}, ErrProtocol
		}
		var fields map[string]jsontext.Value
		if err := json.Unmarshal(raw, &fields); err != nil {
			return reflect.Value{}, err
		}
		value.Set(reflect.MakeMapWithSize(kind, len(fields)))
		for key, data := range fields {
			child, err := e.argument(ctx, data, kind.Elem())
			if err != nil {
				return reflect.Value{}, err
			}
			value.SetMapIndex(reflect.ValueOf(key).Convert(kind.Key()), child)
		}
	default:
		if err := json.Unmarshal(raw, value.Addr().Interface()); err != nil {
			return reflect.Value{}, err
		}
	}
	if kind == reflect.TypeFor[psdfile.Span]() && value.FieldByName("Length").Int() > e.config.MaxOutputBytes {
		return reflect.Value{}, ErrLimit
	}
	return value, nil
}

func (e *Engine) inject(value reflect.Value) {
	for index := 0; index < value.NumField(); index++ {
		field, description := value.Field(index), value.Type().Field(index)
		if !field.CanSet() {
			continue
		}
		if field.Type() == fontResolverType {
			field.Set(reflect.ValueOf(psdfont.ResolverFunc(e.resolveFont)))
			continue
		}
		if field.Kind() == reflect.Func {
			resolver, err := e.resolver(field.Type())
			if err == nil {
				field.Set(resolver)
			}
		}
		if field.Kind() == reflect.Struct {
			e.inject(field)
		}
		if field.Kind() == reflect.Pointer && !field.IsNil() && field.Elem().Kind() == reflect.Struct {
			e.inject(field.Elem())
		}
		if field.Kind() == reflect.Int || field.Kind() == reflect.Int64 {
			limit := int64(0)
			if strings.HasPrefix(description.Name, "Max") && strings.HasSuffix(description.Name, "Bytes") {
				limit = e.config.MaxBytes
			}
			if description.Name == "MaxOutputBytes" {
				limit = e.config.MaxOutputBytes
			}
			if description.Name == "MaxPixels" {
				limit = e.config.MaxPixels
			}
			if limit > 0 && (field.Int() == 0 || field.Int() > limit) {
				field.SetInt(limit)
			}
		}
	}
}

func (e *Engine) linkedAsset(record psdfile.LinkedFileRecord) (*asset, error) {
	for _, key := range []string{record.ID, record.Name, record.ChildDocumentID} {
		if id, exists := e.links[key]; exists {
			if item, exists := e.assets[id]; exists {
				return item, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: linked content requires an explicit asset mapping", ErrProtocol)
}

func (e *Engine) resolveFont(ctx context.Context, name string) (*psdfont.Face, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if face, exists := e.fonts[name]; exists {
		return face, nil
	}
	return nil, psdfont.ErrMissingFont
}

func (e *Engine) resolver(kind reflect.Type) (reflect.Value, error) {
	switch kind {
	case reflect.TypeFor[psdfile.LinkedDocumentResolver]():
		return reflect.ValueOf(psdfile.LinkedDocumentResolver(func(
			ctx context.Context, record psdfile.LinkedFileRecord,
		) (*psdfile.Document, error) {
			item, err := e.linkedAsset(record)
			if err != nil {
				return nil, err
			}
			return psdfile.Parse(item.data, e.loadOptions())
		})), nil
	case reflect.TypeFor[psdedit.LinkedContentsResolver]():
		return reflect.ValueOf(psdedit.LinkedContentsResolver(func(
			ctx context.Context, record psdfile.LinkedFileRecord,
		) ([]byte, error) {
			item, err := e.linkedAsset(record)
			if err != nil {
				return nil, err
			}
			return bytes.Clone(item.data), ctx.Err()
		})), nil
	case reflect.TypeFor[psdedit.LinkedContentsUpdateResolver]():
		return reflect.ValueOf(psdedit.LinkedContentsUpdateResolver(func(
			ctx context.Context, record psdfile.LinkedFileRecord,
		) (psdedit.LinkedContentsUpdate, error) {
			item, err := e.linkedAsset(record)
			if err != nil {
				return psdedit.LinkedContentsUpdate{}, err
			}
			return psdedit.LinkedContentsUpdate{Data: item.data}, ctx.Err()
		})), nil
	}
	return reflect.Value{}, fmt.Errorf("%w: callback %s requires an explicit adapter", ErrProtocol, kind)
}
