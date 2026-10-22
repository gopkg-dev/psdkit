package psdfile

import (
	"context"
	"encoding/binary"
	"errors"
	"io"

	"github.com/gopkg-dev/psdkit/psdeffects"
)

// DecodeEffects reads modern lfx2/lmfx layer effects without decoding pixels or changing source blocks.
// Ordered descriptor entries and unknown raw spans survive source closure as owned metadata.
// Active rendering support is validated separately by psdeffects.Rasterize.
func (d *Document) DecodeEffects(ctx context.Context, block AdditionalBlock, options ...MetadataOptions) (*psdeffects.Set, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := d.ReadAt(nil, 0); err != nil {
		return nil, err
	}
	limits, err := d.metadataLimits(options)
	if err != nil {
		return nil, err
	}
	if block.Key != "lfx2" && block.Key != "lmfx" {
		return nil, fileError(ErrUnsupported, "effects", "effects.key", block.Data.Offset,
			"additional block is not a modern effects descriptor")
	}
	if block.Data.Length < 8 {
		return nil, fileError(ErrMalformed, "effects", "effects.header", block.Data.Offset, "effects header is truncated")
	}
	var header [4]byte
	if err := d.readEffectsField(ctx, Span{Offset: block.Data.Offset, Length: 4}, header[:]); err != nil {
		return nil, err
	}
	version := binary.BigEndian.Uint32(header[:])
	if version != 0 {
		return nil, fileError(ErrUnsupported, "effects", "effects.version", block.Data.Offset,
			"modern object effects version is not zero")
	}
	limits.MaxMetadataBytes = min(limits.MaxMetadataBytes, limits.MaxDecodeBytes)
	span := Span{Offset: block.Data.Offset + 4, Length: block.Data.Length - 4}
	descriptor, used, _, err := d.decodeDescriptorUsage(ctx, span, true, limits)
	if err != nil {
		return nil, err
	}
	// Source descriptors, conversion metadata and the independent semantic snapshot coexist during decoding.
	remaining := (limits.MaxMetadataBytes - used) / 2
	if remaining <= 0 {
		return nil, fileError(ErrLimit, "effects", "effects.descriptor", span.Offset,
			"effects metadata snapshot exceeds the working budget")
	}
	converter := effectDescriptorConverter{ctx: ctx, remaining: remaining}
	converted, err := converter.descriptor(descriptor)
	if err != nil {
		return nil, err
	}
	angle, visible := float64(30), true
	for _, resource := range d.Resources {
		switch resource.ID {
		case 1037:
			if resource.Data.Length != 4 {
				return nil, fileError(ErrMalformed, "effects", "effects.globalAngle", resource.Data.Offset,
					"global lighting angle must contain four bytes")
			}
			if err := d.readEffectsField(ctx, resource.Data, header[:]); err != nil {
				return nil, err
			}
			angle = float64(binary.BigEndian.Uint32(header[:]))
		case 1042:
			if resource.Data.Length != 1 {
				return nil, fileError(ErrMalformed, "effects", "effects.visible", resource.Data.Offset,
					"effects visibility resource must contain one byte")
			}
			if err := d.readEffectsField(ctx, resource.Data, header[:1]); err != nil {
				return nil, err
			}
			visible = header[0] != 0
		}
	}
	set, err := psdeffects.Decode(ctx, converted, psdeffects.DecodeOptions{
		MaxBytes: remaining, MaxEntries: limits.MaxDescriptorItems, MaxDepth: limits.MaxDescriptorDepth,
		GlobalAngle: &angle, EffectsVisible: &visible,
	})
	if err != nil {
		return nil, effectsDecodeError(err, block.Data.Offset)
	}
	set.ObjectVersion, set.Span = version, effectsSpan(block.Data)
	set.Tail = psdeffects.Span{Offset: descriptor.Span.Offset + descriptor.Span.Length,
		Length: block.Data.Length - 4 - descriptor.Span.Length}
	if set.Tail.Length != 0 {
		padding := set.Tail.Length <= 3 && set.Tail.Length == (4-(block.Data.Length-set.Tail.Length)%4)%4
		if padding {
			var bytes [3]byte
			if err := d.readEffectsField(ctx, Span{Offset: set.Tail.Offset, Length: set.Tail.Length},
				bytes[:set.Tail.Length]); err != nil {
				return nil, err
			}
			for _, value := range bytes[:set.Tail.Length] {
				padding = padding && value == 0
			}
		}
		if !padding {
			set.Unsupported = append(set.Unsupported, "unrecognized bytes after effects descriptor")
		}
	}
	return set, nil
}

func (d *Document) readEffectsField(ctx context.Context, span Span, output []byte) error {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !d.validSpan(span) || span.Length != int64(len(output)) {
		return fileError(ErrMalformed, "effects", "effects.field", span.Offset, "effects field is outside the source")
	}
	if span.Length > d.limits.MaxReadBytes {
		return fileError(ErrLimit, "effects", "effects.field", span.Offset, "effects field exceeds the read budget")
	}
	count, err := (contextReaderAt{ctx: ctx, reader: d.reader}).ReadAt(output, span.Offset)
	if count != len(output) || err != nil && !errors.Is(err, io.EOF) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return &Error{Kind: ErrMalformed, Feature: "effects", Path: "effects.field", Offset: span.Offset,
			Detail: "retained effects field could not be read", Cause: err}
	}
	return nil
}

type effectDescriptorConverter struct {
	ctx       context.Context
	remaining int64
}

func (c *effectDescriptorConverter) reserve(bytes int64) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	if bytes < 0 || bytes > c.remaining {
		return fileError(ErrLimit, "effects", "effects.descriptor", 0, "owned effects conversion exceeds metadata budget")
	}
	c.remaining -= bytes
	return nil
}

func (c *effectDescriptorConverter) descriptor(source *Descriptor) (*psdeffects.Descriptor, error) {
	if err := c.reserve(512 + int64(len(source.Entries))*256); err != nil {
		return nil, err
	}
	result := &psdeffects.Descriptor{Name: source.Name, ClassID: source.ClassID,
		Version: source.Version, Span: effectsSpan(source.Span), Entries: make([]psdeffects.Entry, len(source.Entries))}
	for index, entry := range source.Entries {
		value, err := c.value(entry.Value, entry.Data)
		if err != nil {
			return nil, err
		}
		result.Entries[index] = psdeffects.Entry{Key: entry.Key, Type: entry.Type, Value: value,
			Span: effectsSpan(entry.Span), KeyData: effectsSpan(entry.KeyData),
			TypeData: effectsSpan(entry.TypeData), Data: effectsSpan(entry.Data)}
	}
	return result, nil
}

func (c *effectDescriptorConverter) value(value any, span Span) (any, error) {
	switch typed := value.(type) {
	case *Descriptor:
		return c.descriptor(typed)
	case []DescriptorValue:
		if err := c.reserve(int64(len(typed)) * 256); err != nil {
			return nil, err
		}
		values := make([]psdeffects.Value, len(typed))
		for index, item := range typed {
			converted, err := c.value(item.Value, item.Data)
			if err != nil {
				return nil, err
			}
			values[index] = psdeffects.Value{Type: item.Type, Value: converted, Span: effectsSpan(item.Span),
				TypeData: effectsSpan(item.TypeData), Data: effectsSpan(item.Data)}
		}
		return values, nil
	case DescriptorEnum:
		return psdeffects.Enum{TypeID: typed.TypeID, ValueID: typed.ValueID}, nil
	case DescriptorUnit:
		var number float64
		switch scalar := typed.Number.(type) {
		case float64:
			number = scalar
		case float32:
			number = float64(scalar)
		}
		return psdeffects.Unit{Unit: typed.Unit, Number: number, Width: typed.Width, Bits: typed.Bits}, nil
	case DescriptorBlob:
		return psdeffects.Raw{Span: effectsSpan(typed.Span), Data: effectsSpan(typed.Data)}, nil
	case nil, bool, string, int32, int64, float32, float64:
		return value, nil
	default:
		return psdeffects.Raw{Span: effectsSpan(span), Data: effectsSpan(span)}, nil
	}
}

func effectsSpan(span Span) psdeffects.Span {
	return psdeffects.Span{Offset: span.Offset, Length: span.Length}
}

func effectsDecodeError(err error, offset int64) error {
	kind := ErrMalformed
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, psdeffects.ErrUnsupported):
		kind = ErrUnsupported
	case errors.Is(err, psdeffects.ErrLimit):
		kind = ErrLimit
	}
	return &Error{Kind: kind, Feature: "effects", Path: "effects.descriptor", Offset: offset,
		Detail: err.Error(), Cause: err}
}
