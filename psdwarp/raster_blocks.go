package psdwarp

// blockIndices keeps the original first-covering-cell order while pruning remote cells once per block.
// Stack-backed scratch has a fixed capacity; dense overlaps retain the original complete row scan.
func (plan *rasterPlan) blockIndices(indices []uint32, left, right int, scratch []uint32) []uint32 {
	if len(indices) <= 16 {
		return indices
	}
	for _, index := range indices {
		box := plan.cells[index].box
		if box.Min.X >= right || box.Max.X <= left {
			continue
		}
		if len(scratch) == cap(scratch) {
			return indices
		}
		scratch = append(scratch, index)
	}
	return scratch
}
