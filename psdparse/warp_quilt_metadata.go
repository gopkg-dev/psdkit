package psdparse

import (
	"math"
	"slices"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func readWarpQuiltProperties(value descriptor, warp *psdmodel.Warp, location string) {
	if location == "warp" {
		warp.DeformRows, warp.HasDeformRows = readWarpDeformCount(value, warp, "deformNumRows")
		warp.DeformColumns, warp.HasDeformColumns = readWarpDeformCount(value, warp, "deformNumCols")
	}
	keys := warpKeys(value)
	kinds, _ := value["_orderedKinds"].([]string)
	seen := make(map[string]bool)
	quiltEntries, _ := value["_quiltEntries"].([]quiltProperty)
	quiltIndex := 0
	for index, key := range keys {
		if location == "warp" && (warp.ClassID == "quiltWarp" || warp.HasDeformRows || warp.HasDeformColumns) &&
			(key == "uOrder" || key == "vOrder") && index < len(kinds) && kinds[index] != "long" {
			warp.Unsupported = append(warp.Unsupported, "quilt cubic order has the wrong descriptor type: "+key)
		}
		if location == "envelope" && seen[key] {
			warp.Unsupported = append(warp.Unsupported, "duplicate warp envelope property: "+key)
		}
		seen[key] = true
		if key != "quiltSliceX" && key != "quiltSliceY" {
			continue
		}
		if index < len(kinds) && kinds[index] != "ObAr" {
			warp.Unsupported = append(warp.Unsupported, "quilt slice has the wrong descriptor type: "+key)
		}
		entry := value[key]
		if quiltIndex < len(quiltEntries) {
			entry = quiltEntries[quiltIndex].Value
		}
		quiltIndex++
		readWarpSlice(entry, warp, location, key)
	}
}

func readWarpDeformCount(value descriptor, warp *psdmodel.Warp, key string) (int, bool) {
	entry, exists := value[key]
	if !exists {
		return 0, false
	}
	count, valid := entry.(float64)
	if !valid || math.IsNaN(count) || math.IsInf(count, 0) || count <= 0 || count > 100_000 ||
		math.Trunc(count) != count {
		warp.Unsupported = append(warp.Unsupported, "quilt control-point count is malformed: "+key)
		return 0, true
	}
	keys := warpKeys(value)
	kinds, _ := value["_orderedKinds"].([]string)
	for index, property := range keys {
		if property == key && index < len(kinds) && kinds[index] != "long" {
			warp.Unsupported = append(warp.Unsupported, "quilt control-point count has the wrong descriptor type: "+key)
		}
	}
	return int(count), true
}

func readWarpSlice(value any, warp *psdmodel.Warp, location, axis string) {
	items, valid := value.([]objectArrayItem)
	if !valid || len(items) == 0 {
		warp.Unsupported = append(warp.Unsupported, "quilt slice object array is absent or malformed: "+axis)
		return
	}
	for index, item := range items {
		warp.QuiltSlices = append(warp.QuiltSlices, psdmodel.WarpSlice{
			Axis: axis, Location: location, ClassID: item.ClassID, Field: item.Kind,
			Kind: item.Type, Unit: item.Unit, Prefix: item.Prefix, Values: slices.Clone(item.Values),
		})
		if len(items) != 1 || index != 0 {
			warp.Unsupported = append(warp.Unsupported, "quilt slice object array has repeated or unknown columns: "+axis)
		}
		if item.ClassID != "UntF" || item.Kind != axis || item.Type != "UnFl" || item.Unit != "#Pxl" ||
			item.Prefix != uint32(len(item.Values)) || len(item.Values) < 2 {
			warp.Unsupported = append(warp.Unsupported, "quilt slice column has an unknown class, type, unit or count: "+axis)
		}
		for _, coordinate := range item.Values {
			if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
				warp.Unsupported = append(warp.Unsupported, "quilt slice column contains a nonfinite coordinate: "+axis)
				break
			}
		}
	}
}
