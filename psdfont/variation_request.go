package psdfont

import (
	"context"
	"strings"
	"unsafe"
)

type fontDecodeConfiguration struct {
	limits       DecodeOptions
	requestBytes int64
	requestWork  int
}

func prepareFontDecodeConfiguration(ctx context.Context, options []DecodeOptions) (fontDecodeConfiguration, error) {
	limits, err := decodeConfiguration(options)
	if err != nil {
		return fontDecodeConfiguration{}, err
	}
	result := fontDecodeConfiguration{limits: limits}
	if len(limits.Variations) == 0 {
		return result, nil
	}
	// Snapshot both the array and axis tags before Open allocates or reads its file buffer.
	perCoordinate := int64(unsafe.Sizeof(VariationCoordinate{})) + 16
	if int64(len(limits.Variations)) > limits.MaxBytes/perCoordinate || len(limits.Variations) > limits.MaxCFFOperations {
		return result, fontError(ErrLimit, "variation.request", 0, "owned axis request exceeds decode limits")
	}
	result.requestBytes = int64(len(limits.Variations)) * perCoordinate
	owned := make([]VariationCoordinate, len(limits.Variations))
	copy(owned, limits.Variations)
	for index, coordinate := range owned {
		if err := ctx.Err(); err != nil {
			return fontDecodeConfiguration{}, err
		}
		if len(coordinate.Tag) != 4 || !finite(coordinate.Value) {
			return result, fontError(ErrMalformed, "variation.request", 0, "invalid axis tag or nonfinite value")
		}
		owned[index] = VariationCoordinate{Tag: strings.Clone(coordinate.Tag), Value: coordinate.Value}
		result.requestWork++
	}
	result.limits.Variations = owned
	if err := ctx.Err(); err != nil {
		return fontDecodeConfiguration{}, err
	}
	return result, nil
}
