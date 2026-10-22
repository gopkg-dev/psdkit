package psdparse

import (
	"math"
	"strings"
)

type descriptor map[string]any
type unitValue struct {
	Unit  string
	Value float64
}
type objectArrayItem struct {
	Kind, Unit, ClassID string
	Type                string
	Prefix              uint32
	Values              []float64
}
type quiltProperty struct {
	Key, Kind string
	Value     any
}

func readVersionDescriptor(r *cursor) descriptor {
	if r.u32() != 16 {
		r.fail("unsupported Photoshop descriptor version")
		return nil
	}
	return readDescriptor(r, 0)
}

func readDescriptor(r *cursor, depth int) descriptor {
	if depth > 32 {
		r.fail("Photoshop descriptor nesting exceeds 32 levels")
		return nil
	}
	r.unicode()
	class := r.classID()
	count := r.u32()
	if count > 10000 {
		r.fail("Photoshop descriptor has too many entries")
		return nil
	}
	value := descriptor{"_classID": class}
	keys := make([]string, 0, int(count))
	kinds := make([]string, 0, int(count))
	var quiltEntries []quiltProperty
	for index := uint32(0); index < count && r.err == nil; index++ {
		key := r.classID()
		kind := r.sig()
		value[key] = readValue(r, kind, depth+1)
		if key == "quiltSliceX" || key == "quiltSliceY" {
			quiltEntries = append(quiltEntries, quiltProperty{Key: key, Kind: kind, Value: value[key]})
		}
		keys = append(keys, key)
		kinds = append(kinds, kind)
	}
	value["_orderedKeys"] = keys
	value["_orderedKinds"] = kinds
	if quiltEntries != nil {
		value["_quiltEntries"] = quiltEntries
	}
	return value
}

func readValue(r *cursor, kind string, depth int) any {
	if depth > 32 {
		r.fail("Photoshop descriptor nesting exceeds 32 levels")
		return nil
	}
	switch kind {
	case "Objc", "GlbO":
		return readDescriptor(r, depth)
	case "VlLs":
		count := r.u32()
		if count > 100000 {
			r.fail("Photoshop descriptor list exceeds limit")
			return nil
		}
		items := make([]any, 0, int(count))
		for index := uint32(0); index < count && r.err == nil; index++ {
			items = append(items, readValue(r, r.sig(), depth+1))
		}
		return items
	case "doub":
		return r.f64()
	case "UntF":
		unit := r.sig()
		return unitValue{Unit: unit, Value: r.f64()}
	case "UnFl":
		unit := r.sig()
		return unitValue{Unit: unit, Value: r.f32()}
	case "TEXT":
		return r.unicode()
	case "enum":
		category := r.classID()
		return category + "." + r.classID()
	case "long":
		return float64(r.i32())
	case "comp":
		return r.u64()
	case "bool":
		return r.u8() != 0
	case "type", "GlbC":
		r.unicode()
		return r.classID()
	case "alis", "tdta", "Pth ":
		r.skip(int64(r.u32()))
		return nil
	case "ObAr":
		prefix := r.u32()
		r.unicode()
		arrayClass := r.classID()
		count := r.u32()
		if count > 10000 {
			r.fail("Photoshop object array exceeds limit")
			return nil
		}
		items := make([]objectArrayItem, 0, count)
		for index := uint32(0); index < count && r.err == nil; index++ {
			key := r.classID()
			typ := r.sig()
			unit := r.sig()
			if typ != "UnFl" && typ != "UntF" {
				r.fail("unsupported object array numeric type %q", typ)
				return nil
			}
			length := r.u32()
			if length > 100000 {
				r.fail("Photoshop mesh exceeds limit")
				return nil
			}
			values := make([]float64, length)
			for offset := range values {
				values[offset] = r.f64()
			}
			items = append(items, objectArrayItem{Kind: key, Unit: unit, ClassID: arrayClass,
				Type: typ, Prefix: prefix, Values: values})
		}
		return items
	case "obj ":
		count := r.u32()
		if count > 10000 {
			r.fail("Photoshop reference exceeds limit")
			return nil
		}
		for index := uint32(0); index < count && r.err == nil; index++ {
			kind := r.sig()
			switch kind {
			case "prop":
				r.unicode()
				r.classID()
				r.classID()
			case "Clss":
				r.unicode()
				r.classID()
			case "Enmr":
				r.unicode()
				r.classID()
				r.classID()
				r.classID()
			case "rele":
				r.unicode()
				r.classID()
				r.u32()
			case "Idnt", "indx":
				r.u32()
			case "name":
				r.unicode()
				r.classID()
				r.unicode()
			default:
				r.fail("unsupported Photoshop reference type %q", kind)
			}
		}
		return nil
	default:
		r.fail("unsupported Photoshop descriptor type %q", kind)
		return nil
	}
}

func desc(value any) descriptor { result, _ := value.(descriptor); return result }
func number(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case unitValue:
		return typed.Value
	}
	return 0
}
func text(value any) string  { result, _ := value.(string); return result }
func boolean(value any) bool { result, _ := value.(bool); return result }
func numbers(value any) []float64 {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]float64, len(items))
	for index, item := range items {
		value, valid := item.(float64)
		if !valid {
			value = math.NaN()
		}
		result[index] = value
	}
	return result
}
func enumValue(value any) string {
	pieces := strings.SplitN(text(value), ".", 2)
	return pieces[len(pieces)-1]
}
