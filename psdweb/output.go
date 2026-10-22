package psdweb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/png"
	"math"
	"reflect"
	"strconv"
	"strings"
)

func (e *Engine) binary(data []byte, name, mime string) (any, error) {
	used := int64(0)
	for _, binary := range e.binaries {
		used += int64(binary.Length)
	}
	if int64(len(data)) > e.config.MaxOutputBytes-used {
		return nil, ErrLimit
	}
	id := "b" + strconvItoa(len(e.binaries)+1)
	e.binaries = append(e.binaries, Binary{ID: id, Name: name, MIME: mime, Length: len(data), Data: bytes.Clone(data)})
	return map[string]any{"binary": id, "name": name, "mime": mime, "bytes": len(data)}, nil
}

func (e *Engine) exportImage(pixels image.Image) (any, error) {
	bounds := pixels.Bounds()
	width, height := int64(bounds.Dx()), int64(bounds.Dy())
	if width <= 0 || height <= 0 || width > e.config.MaxPixels/height || width > e.config.MaxBytes/4/height {
		return nil, ErrLimit
	}
	encoded := &boundedBuffer{limit: e.config.MaxOutputBytes}
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(encoded, pixels); err != nil {
		return nil, err
	}
	output, err := e.binary(encoded.Bytes(), "preview.png", "image/png")
	if err != nil {
		return nil, err
	}
	result := map[string]any{"image": output, "width": width, "height": height, "bounds": bounds,
		"goType": reflect.TypeOf(pixels).String()}
	if straight, valid := pixels.(*image.NRGBA); valid && e.imageOutput != "png" {
		packed := make([]byte, int(width*height*4))
		for row := range int(height) {
			copy(packed[row*int(width)*4:], straight.Pix[row*straight.Stride:row*straight.Stride+int(width)*4])
		}
		raw, err := e.binary(packed, "pixels.rgba", "application/x-rgba8")
		if err != nil {
			return nil, err
		}
		result["rgba"] = raw
	}
	return result, nil
}

func (e *Engine) exportValue(value reflect.Value, depth int) (any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("%w: result nesting", ErrLimit)
	}
	if !value.IsValid() {
		return nil, nil
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, nil
		}
	}
	if value.CanInterface() {
		if file, valid := reflect.TypeAssert[FileOutput](value); valid {
			return e.binary(file.Data, file.Name, file.MIME)
		}
		if pixels, valid := reflect.TypeAssert[image.Image](value); valid && value.Type() != reflect.TypeFor[image.Rectangle]() {
			return e.exportImage(pixels)
		}
		if data, valid := reflect.TypeAssert[[]byte](value); valid {
			return e.binary(data, "data.bin", "application/octet-stream")
		}
		if data, valid := reflect.TypeAssert[[]float64](value); valid && len(data) > 512 {
			if int64(len(data)) > e.config.MaxOutputBytes/8 {
				return nil, ErrLimit
			}
			encoded := make([]byte, len(data)*8)
			for index, sample := range data {
				binary.LittleEndian.PutUint64(encoded[index*8:], math.Float64bits(sample))
			}
			return e.binary(encoded, "samples.f64le", "application/x-float64-le")
		}
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		integer := value.Int()
		if integer > 9_007_199_254_740_991 || integer < -9_007_199_254_740_991 {
			return map[string]string{"$int64": strconv.FormatInt(integer, 10)}, nil
		}
		return integer, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		integer := value.Uint()
		if integer > 9_007_199_254_740_991 {
			return map[string]string{"$int64": strconv.FormatUint(integer, 10)}, nil
		}
		return integer, nil
	case reflect.Pointer, reflect.Interface:
		return e.exportValue(value.Elem(), depth+1)
	case reflect.Struct:
		result := map[string]any{}
		for index := 0; index < value.NumField(); index++ {
			field := value.Type().Field(index)
			if !field.IsExported() || field.Tag.Get("json") == "-" || field.Type.Kind() == reflect.Func {
				continue
			}
			name := field.Name
			if tag, _, _ := strings.Cut(field.Tag.Get("json"), ","); tag != "" {
				name = tag
			}
			view, err := e.exportValue(value.Field(index), depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = view
		}
		return result, nil
	case reflect.Array, reflect.Slice:
		if value.Len() > e.config.MaxJSONBytes/2 {
			return nil, ErrLimit
		}
		result := make([]any, value.Len())
		for index := range result {
			view, err := e.exportValue(value.Index(index), depth+1)
			if err != nil {
				return nil, err
			}
			result[index] = view
		}
		return result, nil
	case reflect.Map:
		result := map[string]any{}
		iterator := value.MapRange()
		for iterator.Next() {
			view, err := e.exportValue(iterator.Value(), depth+1)
			if err != nil {
				return nil, err
			}
			result[fmt.Sprint(iterator.Key().Interface())] = view
		}
		return result, nil
	case reflect.Func:
		return "injected resolver", nil
	default:
		if value.CanInterface() {
			return value.Interface(), nil
		}
	}
	return nil, nil
}
