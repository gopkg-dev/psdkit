package psdeffects

import (
	"context"
	"fmt"
	"math"
)

type descriptorBudget struct {
	ctx        context.Context
	remaining  int64
	entries    int
	maxEntries int
	maxDepth   int
}

// Decode interprets an owned snapshot of a modern effect descriptor while retaining unknown entries.
// Disabled unimplemented effects remain readable and do not prevent rendering supported enabled effects.
func Decode(ctx context.Context, descriptor *Descriptor, options ...DecodeOptions) (*Set, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	configuration := DecodeOptions{MaxBytes: 32 << 20, MaxEntries: 100_000, MaxDepth: 32}
	if len(options) > 1 {
		return nil, effectError(ErrInvalid, "options", "at most one decode option is accepted")
	}
	if len(options) == 1 {
		provided := options[0]
		if provided.MaxBytes < 0 || provided.MaxEntries < 0 || provided.MaxDepth < 0 {
			return nil, effectError(ErrInvalid, "options", "decode budgets must not be negative")
		}
		if provided.MaxBytes != 0 {
			configuration.MaxBytes = provided.MaxBytes
		}
		if provided.MaxEntries != 0 {
			configuration.MaxEntries = provided.MaxEntries
		}
		if provided.MaxDepth != 0 {
			configuration.MaxDepth = provided.MaxDepth
		}
		configuration.GlobalAngle, configuration.EffectsVisible = provided.GlobalAngle, provided.EffectsVisible
	}
	budget := descriptorBudget{ctx: ctx, remaining: configuration.MaxBytes, maxEntries: configuration.MaxEntries,
		maxDepth: configuration.MaxDepth}
	owned, err := budget.clone(descriptor, 1)
	if err != nil {
		return nil, err
	}
	set := &Set{Enabled: true, EffectsVisible: true, Scale: 1, GlobalAngle: 30,
		DescriptorVersion: owned.Version, OrderedEntries: owned.Entries, Descriptor: owned, Span: owned.Span}
	if configuration.GlobalAngle != nil {
		set.GlobalAngle = *configuration.GlobalAngle
	}
	if configuration.EffectsVisible != nil {
		set.EffectsVisible = *configuration.EffectsVisible
	}
	seen := map[string]bool{}
	for _, entry := range owned.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch entry.Key {
		case "masterFXSwitch":
			value, valid := entry.Value.(bool)
			if !valid || seen[entry.Key] {
				set.Unsupported = append(set.Unsupported, "ambiguous master effects switch")
			} else {
				set.Enabled = value
			}
		case "Scl ", "scale":
			value, valid := scalar(entry.Value)
			if !valid || seen[entry.Key] {
				set.Unsupported = append(set.Unsupported, "ambiguous effects scale")
			} else {
				set.Scale = value / 100
			}
		case "numModifyingFX":
			value, valid := scalar(entry.Value)
			if !valid || value < 0 || value > float64(configuration.MaxEntries) || value != math.Trunc(value) ||
				seen[entry.Key] {
				set.Unsupported = append(set.Unsupported, "unrecognized modifying effects count")
			} else {
				count := int(value)
				set.ModifyingCount = &count
			}
		default:
			if nested, valid := entry.Value.(*Descriptor); valid {
				if err := appendEffect(set, &budget, entry.Key, nested); err != nil {
					return nil, err
				}
			} else if list, valid := entry.Value.([]Value); valid {
				for _, item := range list {
					nested, valid := item.Value.(*Descriptor)
					if !valid {
						set.Unsupported = append(set.Unsupported, "unrecognized effects list entry")
						continue
					}
					if err := appendEffect(set, &budget, entry.Key, nested); err != nil {
						return nil, err
					}
				}
			} else {
				set.Unsupported = append(set.Unsupported, "unrecognized effects descriptor entry "+entry.Key)
			}
		}
		seen[entry.Key] = true
	}
	if set.ModifyingCount != nil && *set.ModifyingCount > len(set.Items) {
		set.Unsupported = append(set.Unsupported, "modifying effects count exceeds retained effect entries")
	}
	return set, nil
}

func (b *descriptorBudget) reserve(bytes int64, entries int) error {
	if err := b.ctx.Err(); err != nil {
		return err
	}
	if bytes < 0 || bytes > b.remaining || entries > b.maxEntries-b.entries {
		return effectError(ErrLimit, "effects.descriptor", "owned descriptor exceeds decode budgets")
	}
	b.remaining -= bytes
	b.entries += entries
	return nil
}

func (b *descriptorBudget) clone(source *Descriptor, depth int) (*Descriptor, error) {
	if source == nil {
		return nil, effectError(ErrInvalid, "effects.descriptor", "descriptor is required")
	}
	if depth > b.maxDepth {
		return nil, effectError(ErrLimit, "effects.descriptor", "descriptor nesting exceeds decode budget")
	}
	if err := b.reserve(512+int64(len(source.Entries))*256, len(source.Entries)); err != nil {
		return nil, err
	}
	if err := b.reserve(int64(len(source.Name))+int64(len(source.ClassID)), 0); err != nil {
		return nil, err
	}
	result := *source
	result.Entries = make([]Entry, len(source.Entries))
	for index, entry := range source.Entries {
		// Include retained names and potential unsupported-parameter diagnostics before constructing them.
		if err := b.reserve(int64(len(entry.Key))*2+int64(len(entry.Type)), 0); err != nil {
			return nil, err
		}
		value, err := b.cloneValue(entry.Value, depth)
		if err != nil {
			return nil, err
		}
		result.Entries[index], result.Entries[index].Value = entry, value
	}
	return &result, nil
}

func (b *descriptorBudget) cloneValue(value any, depth int) (any, error) {
	switch typed := value.(type) {
	case *Descriptor:
		return b.clone(typed, depth+1)
	case []Value:
		if depth >= b.maxDepth {
			return nil, effectError(ErrLimit, "effects.list", "list nesting exceeds decode budget")
		}
		if err := b.reserve(int64(len(typed))*256, len(typed)); err != nil {
			return nil, err
		}
		values := make([]Value, len(typed))
		for index, item := range typed {
			cloned, err := b.cloneValue(item.Value, depth+1)
			if err != nil {
				return nil, err
			}
			values[index], values[index].Value = item, cloned
		}
		return values, nil
	case string:
		if err := b.reserve(int64(len(typed)), 0); err != nil {
			return nil, err
		}
		return value, nil
	case Enum:
		if err := b.reserve(int64(len(typed.TypeID))+int64(len(typed.ValueID)), 0); err != nil {
			return nil, err
		}
		return value, nil
	case Unit:
		if err := b.reserve(int64(len(typed.Unit)), 0); err != nil {
			return nil, err
		}
		return value, nil
	case nil, bool, int32, int64, float32, float64, Raw:
		return value, nil
	default:
		return nil, effectError(ErrInvalid, "effects.value", fmt.Sprintf("unknown descriptor value type %T", value))
	}
}

func appendEffect(set *Set, budget *descriptorBudget, key string, descriptor *Descriptor) error {
	if err := budget.reserve(1024, 1); err != nil {
		return err
	}
	effect := Effect{Enabled: true, Present: true, Shown: true, BlendMode: "norm", Opacity: 1,
		LayerConceals: true, Position: Outside, GlowSource: Edge, Technique: "SfBL", Range: 0.5, Descriptor: descriptor}
	switch key {
	case "DrSh", "dropShadow", "DrShMulti", "dropShadowMulti":
		effect.Kind = DropShadow
	case "OrGl", "outerGlow", "OrGlMulti", "outerGlowMulti":
		effect.Kind = OuterGlow
	case "IrSh", "innerShadow", "IrShMulti", "innerShadowMulti":
		effect.Kind = InnerShadow
	case "IrGl", "innerGlow", "IrGlMulti", "innerGlowMulti":
		effect.Kind = InnerGlow
	case "FrFX", "frameFX", "FrFXMulti", "frameFXMulti":
		effect.Kind = Stroke
	case "SoFi", "solidFill", "SoFiMulti", "solidFillMulti":
		effect.Kind = ColorOverlay
	default:
		effect.Kind = Kind(key)
		effect.Unsupported = append(effect.Unsupported, "unsupported effect family "+key)
	}
	seen := map[string]bool{}
	for _, entry := range descriptor.Entries {
		if seen[entry.Key] {
			effect.Unsupported = append(effect.Unsupported, "duplicate effect parameter "+entry.Key)
		}
		seen[entry.Key] = true
		if err := decodeParameter(&effect, entry); err != nil {
			return err
		}
	}
	if !seen["Clr "] && (effect.Kind == DropShadow || effect.Kind == OuterGlow ||
		effect.Kind == InnerShadow || effect.Kind == InnerGlow || effect.Kind == Stroke ||
		effect.Kind == ColorOverlay) {
		effect.Unsupported = append(effect.Unsupported, "effect color is missing")
	}
	set.Items = append(set.Items, effect)
	return nil
}

func scalar(value any) (float64, bool) {
	var result float64
	switch typed := value.(type) {
	case float64:
		result = typed
	case float32:
		result = float64(typed)
	case int32:
		result = float64(typed)
	case int64:
		result = float64(typed)
	case Unit:
		result = typed.Number
	default:
		return 0, false
	}
	return result, !math.IsNaN(result) && !math.IsInf(result, 0)
}
