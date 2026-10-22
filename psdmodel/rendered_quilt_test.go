package psdmodel

import "testing"

func TestRasterFingerprintIncludesEveryQuiltField(t *testing.T) {
	fixture := func() *Placed {
		return &Placed{Width: 413, Height: 413, Warp: &Warp{
			DeformRows: 10, DeformColumns: 10, HasDeformRows: true, HasDeformColumns: true,
			QuiltSlices: []WarpSlice{{Axis: "quiltSliceX", Location: "envelope", ClassID: "UntF", Prefix: 4,
				Field: "quiltSliceX", Kind: "UnFl", Unit: "#Pxl", Values: []float64{-0.6, 137, 275, 413.6}}},
		}}
	}
	baseline := fixture().RasterFingerprint()
	for _, test := range []struct {
		name   string
		change func(*Warp)
	}{
		{name: "rows", change: func(w *Warp) { w.DeformRows++ }},
		{name: "columns", change: func(w *Warp) { w.DeformColumns++ }},
		{name: "rowsPresence", change: func(w *Warp) { w.HasDeformRows = false }},
		{name: "columnsPresence", change: func(w *Warp) { w.HasDeformColumns = false }},
		{name: "axis", change: func(w *Warp) { w.QuiltSlices[0].Axis = "quiltSliceY" }},
		{name: "location", change: func(w *Warp) { w.QuiltSlices[0].Location = "warp" }},
		{name: "class", change: func(w *Warp) { w.QuiltSlices[0].ClassID = "future" }},
		{name: "prefix", change: func(w *Warp) { w.QuiltSlices[0].Prefix++ }},
		{name: "field", change: func(w *Warp) { w.QuiltSlices[0].Field = "quiltSliceY" }},
		{name: "kind", change: func(w *Warp) { w.QuiltSlices[0].Kind = "UntF" }},
		{name: "unit", change: func(w *Warp) { w.QuiltSlices[0].Unit = "#Prc" }},
		{name: "coordinate", change: func(w *Warp) { w.QuiltSlices[0].Values[1]++ }},
		{name: "columnOrder", change: func(w *Warp) {
			w.QuiltSlices = append([]WarpSlice{{Axis: "quiltSliceY"}}, w.QuiltSlices...)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			placed := fixture()
			test.change(placed.Warp)
			if placed.RasterFingerprint() == baseline {
				t.Fatal("changed quilt metadata could reuse an unrelated previously rendered raster")
			}
		})
	}
	placed := fixture()
	placed.Warp.QuiltSlices = nil
	nilFingerprint := placed.RasterFingerprint()
	placed.Warp.QuiltSlices = []WarpSlice{}
	if placed.RasterFingerprint() == nilFingerprint {
		t.Fatal("nil and explicitly empty quilt columns share a raster fingerprint")
	}
	placed = fixture()
	placed.Warp.QuiltSlices[0].Values = nil
	nilFingerprint = placed.RasterFingerprint()
	placed.Warp.QuiltSlices[0].Values = []float64{}
	if placed.RasterFingerprint() == nilFingerprint {
		t.Fatal("nil and explicitly empty quilt coordinates share a raster fingerprint")
	}
}
