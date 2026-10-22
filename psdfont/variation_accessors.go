package psdfont

import (
	"context"
	"unsafe"
)

func (f *Face) variationCopyBudget(ctx context.Context) (*fontMetadataBudget, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f == nil {
		return nil, fontError(ErrMalformed, "variation.face", 0, "missing font face")
	}
	if f.variation == nil {
		return nil, nil
	}
	limits := DecodeOptions{MaxBytes: 32 << 20, MaxCFFOperations: 1000000}
	if f.variation != nil {
		limits.MaxBytes, limits.MaxCFFOperations = f.variation.copyLimit, f.variation.copyWork
	}
	fixed := int64(unsafe.Sizeof(Face{})) + int64(unsafe.Sizeof(fontMetadataBudget{}))
	if fixed > limits.MaxBytes {
		return nil, fontError(ErrLimit, "variation.copy", 0, "copy workspace exceeds byte limit")
	}
	return &fontMetadataBudget{face: &Face{reservation: fixed}, ctx: ctx, limits: limits}, nil
}

// VariationAxes returns an owned copy of the original fvar axes in source order.
// Fonts without variation metadata return nil; strings are immutable values owned by the decoded face.
func (f *Face) VariationAxes(ctx context.Context) ([]VariationAxis, error) {
	budget, err := f.variationCopyBudget(ctx)
	if err != nil {
		return nil, err
	}
	if f.variation == nil {
		return nil, nil
	}
	if err := budget.array(uint64(len(f.variation.axes)), int64(unsafe.Sizeof(VariationAxis{})),
		"variation.axes.copy", 0); err != nil {
		return nil, err
	}
	result := make([]VariationAxis, len(f.variation.axes))
	for index, axis := range f.variation.axes {
		if err := budget.step("variation.axes.copy", axis.axis.Source.Offset); err != nil {
			return nil, err
		}
		result[index] = axis.axis
	}
	if err := budget.ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// VariationCoordinates returns the owned selected user and normalized coordinates in fvar order.
func (f *Face) VariationCoordinates(ctx context.Context) ([]VariationInstanceCoordinate, error) {
	budget, err := f.variationCopyBudget(ctx)
	if err != nil {
		return nil, err
	}
	if f.variation == nil {
		return nil, nil
	}
	if err := budget.array(uint64(len(f.variation.coordinates)), int64(unsafe.Sizeof(VariationInstanceCoordinate{})),
		"variation.coordinates.copy", 0); err != nil {
		return nil, err
	}
	result := make([]VariationInstanceCoordinate, len(f.variation.coordinates))
	for index, coordinate := range f.variation.coordinates {
		if err := budget.step("variation.coordinates.copy", 0); err != nil {
			return nil, err
		}
		result[index] = coordinate
	}
	if err := budget.ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// NamedVariationInstances returns owned coordinates and source name IDs without resolving or substituting names.
func (f *Face) NamedVariationInstances(ctx context.Context) ([]NamedVariationInstance, error) {
	budget, err := f.variationCopyBudget(ctx)
	if err != nil {
		return nil, err
	}
	if f.variation == nil {
		return nil, nil
	}
	if err := budget.array(uint64(len(f.variation.named)), int64(unsafe.Sizeof(NamedVariationInstance{})),
		"variation.instances.copy", 0); err != nil {
		return nil, err
	}
	result := make([]NamedVariationInstance, len(f.variation.named))
	for index, instance := range f.variation.named {
		if err := budget.step("variation.instances.copy", instance.Source.Offset); err != nil {
			return nil, err
		}
		if err := budget.array(uint64(len(instance.Coordinates)), 8, "variation.instances.coordinates", 0); err != nil {
			return nil, err
		}
		result[index] = instance
		result[index].Coordinates = make([]float64, len(instance.Coordinates))
		for coordinate, value := range instance.Coordinates {
			if err := budget.step("variation.instances.coordinates", 0); err != nil {
				return nil, err
			}
			result[index].Coordinates[coordinate] = value
		}
	}
	if err := budget.ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
