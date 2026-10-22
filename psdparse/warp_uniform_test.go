package psdparse

import (
	"bytes"
	"math"
	"slices"
	"testing"

	"github.com/gopkg-dev/psdkit/psdmodel"
)

func uniformDescriptorWire(class string, entries ...semanticsDescriptorEntry) []byte {
	writer := &psbWriter{}
	writer.u32(0)
	semanticsClassID(writer, class)
	writer.u32(uint32(len(entries)))
	for _, entry := range entries {
		semanticsClassID(writer, entry.key)
		writer.WriteString(entry.kind)
		writer.Write(entry.data)
	}
	return writer.Bytes()
}

func uniformLongWire(value int) []byte {
	writer := &psbWriter{}
	writer.u32(uint32(value))
	return writer.Bytes()
}

func uniformDoubleWire(value float64) []byte {
	writer := &psbWriter{}
	writer.u64(math.Float64bits(value))
	return writer.Bytes()
}

func uniformObjectArrayWire(class string, prefix uint32, columns ...objectArrayItem) []byte {
	writer := &psbWriter{}
	writer.u32(prefix)
	writer.u32(0)
	semanticsClassID(writer, class)
	writer.u32(uint32(len(columns)))
	for _, column := range columns {
		semanticsClassID(writer, column.Kind)
		writer.WriteString(column.Type)
		writer.WriteString(column.Unit)
		writer.u32(uint32(len(column.Values)))
		for _, coordinate := range column.Values {
			writer.u64(math.Float64bits(coordinate))
		}
	}
	return writer.Bytes()
}

func uniformWarpWire(order int, width, height float64) []byte {
	segments := (order - 1) / 3
	xs, ys := make([]float64, order*order), make([]float64, order*order)
	for row := range order {
		for column := range order {
			xs[row*order+column] = width * float64(column) / float64(order-1)
			ys[row*order+column] = height * float64(row) / float64(order-1)
		}
	}
	xSlices, ySlices := make([]float64, segments+1), make([]float64, segments+1)
	for index := range xSlices {
		xSlices[index] = width * float64(index) / float64(segments)
		ySlices[index] = height * float64(index) / float64(segments)
	}
	xSlices[0], ySlices[0] = -0.6, -0.6
	xSlices[segments], ySlices[segments] = width+0.6, height+0.6
	envelope := uniformDescriptorWire("customEnvelopeWarp",
		semanticsDescriptorEntry{key: "quiltSliceX", kind: "ObAr", data: uniformObjectArrayWire("UntF",
			uint32(len(xSlices)), objectArrayItem{Kind: "quiltSliceX", Type: "UnFl", Unit: "#Pxl", Values: xSlices})},
		semanticsDescriptorEntry{key: "quiltSliceY", kind: "ObAr", data: uniformObjectArrayWire("UntF",
			uint32(len(ySlices)), objectArrayItem{Kind: "quiltSliceY", Type: "UnFl", Unit: "#Pxl", Values: ySlices})},
		semanticsDescriptorEntry{key: "meshPoints", kind: "ObAr", data: uniformObjectArrayWire("rationalPoint",
			uint32(len(xs)), objectArrayItem{Kind: "Hrzn", Type: "UnFl", Unit: "#Pxl", Values: xs},
			objectArrayItem{Kind: "Vrtc", Type: "UnFl", Unit: "#Pxl", Values: ys})})
	bounds := uniformDescriptorWire("classFloatRect",
		semanticsDescriptorEntry{key: "Left", kind: "doub", data: uniformDoubleWire(0)},
		semanticsDescriptorEntry{key: "Top ", kind: "doub", data: uniformDoubleWire(0)},
		semanticsDescriptorEntry{key: "Rght", kind: "doub", data: uniformDoubleWire(width)},
		semanticsDescriptorEntry{key: "Btom", kind: "doub", data: uniformDoubleWire(height)})
	return uniformDescriptorWire("quiltWarp",
		semanticsDescriptorEntry{key: "warpStyle", kind: "enum", data: warpEnumWire("warpStyle", "warpCustom")},
		semanticsDescriptorEntry{key: "warpRotate", kind: "enum", data: warpEnumWire("Ornt", "Hrzn")},
		semanticsDescriptorEntry{key: "uOrder", kind: "long", data: uniformLongWire(4)},
		semanticsDescriptorEntry{key: "vOrder", kind: "long", data: uniformLongWire(4)},
		semanticsDescriptorEntry{key: "deformNumRows", kind: "long", data: uniformLongWire(order)},
		semanticsDescriptorEntry{key: "deformNumCols", kind: "long", data: uniformLongWire(order)},
		semanticsDescriptorEntry{key: "bounds", kind: "Objc", data: bounds},
		semanticsDescriptorEntry{key: "customEnvelopeWarp", kind: "Objc", data: envelope})
}

func decodeUniformWarpWire(t *testing.T, data []byte) *psdmodel.Warp {
	t.Helper()
	original := bytes.Clone(data)
	r := &cursor{source: &source{reader: bytes.NewReader(data), size: int64(len(data))}, end: int64(len(data))}
	warp := readWarp(readDescriptor(r, 0))
	if r.err != nil || r.remaining() != 0 || !bytes.Equal(data, original) {
		t.Fatalf("wire parse changed source or failed: %v, remaining=%d", r.err, r.remaining())
	}
	return warp
}

func TestUniformQuiltWireRetains10And13SourceDomains(t *testing.T) {
	for _, test := range []struct {
		name          string
		order         int
		width, height float64
	}{
		{name: "real10", order: 10, width: 413, height: 413},
		{name: "real13", order: 13, width: 736, height: 1308},
	} {
		t.Run(test.name, func(t *testing.T) {
			warp := decodeUniformWarpWire(t, uniformWarpWire(test.order, test.width, test.height))
			if warp.ClassID != "quiltWarp" || !warp.Split || !warp.HasEnvelope || warp.UOrder != 4 || warp.VOrder != 4 ||
				!warp.HasDeformRows || !warp.HasDeformColumns || warp.DeformRows != test.order ||
				warp.DeformColumns != test.order || len(warp.MeshPoints) != test.order*test.order ||
				len(warp.Unsupported) != 0 || len(warp.QuiltSlices) != 2 {
				t.Fatalf("native cubic order and grid counts were conflated: %#v", warp)
			}
			if warp.Bounds.Left != 0 || warp.Bounds.Top != 0 || warp.Bounds.Right != test.width ||
				warp.Bounds.Bottom != test.height || !warp.Bounds.Pixels {
				t.Fatal("slice guards overwrote the original source domain")
			}
			for index, axis := range []string{"quiltSliceX", "quiltSliceY"} {
				slice := warp.QuiltSlices[index]
				if slice.Axis != axis || slice.Field != axis || slice.Location != "envelope" || slice.ClassID != "UntF" ||
					slice.Kind != "UnFl" || slice.Unit != "#Pxl" || slice.Prefix != uint32((test.order-1)/3+1) ||
					slice.Values[0] != -0.6 {
					t.Fatalf("native slice structure or outer guard lost: %#v", slice)
				}
				extent := []float64{test.width, test.height}[index]
				if slice.Values[len(slice.Values)-1] != extent+0.6 {
					t.Fatal("native padded endpoint was reduced to a uniform knot")
				}
			}
		})
	}
}

func TestUniformQuiltMetadataDoesNotInferAbsentCountsOrNormalizeKnots(t *testing.T) {
	data := uniformWarpWire(13, 736, 1308)
	r := &cursor{source: &source{reader: bytes.NewReader(data), size: int64(len(data))}, end: int64(len(data))}
	value := readDescriptor(r, 0)
	delete(value, "deformNumRows")
	delete(value, "deformNumCols")
	value["_orderedKeys"] = []string{"warpStyle", "warpRotate", "uOrder", "vOrder", "bounds", "customEnvelopeWarp"}
	delete(value, "_orderedKinds")
	envelope := desc(value["customEnvelopeWarp"])
	columns := envelope["quiltSliceX"].([]objectArrayItem)
	columns[0].Values[1] = 190
	warp := readWarp(value)
	if warp.HasDeformRows || warp.HasDeformColumns || warp.DeformRows != 0 || warp.DeformColumns != 0 ||
		len(warp.MeshPoints) != 169 || warp.QuiltSlices[0].Values[1] != 190 {
		t.Fatal("point count inferred missing grid metadata or nonuniform source knots were silently normalized")
	}
	columns[0].Values[1] = 191
	if warp.QuiltSlices[0].Values[1] != 190 {
		t.Fatal("quilt source coordinates were retained by alias rather than owned metadata")
	}
	warp.Bounds.Right, warp.Bounds.Bottom = 1346, 2400
	if !slices.Equal(warp.QuiltSlices[0].Values, []float64{-0.6, 190, 368, 552, 736.6}) {
		t.Fatal("changing source bounds rescaled the stale native slice domain")
	}
}

func TestUniformQuiltPSDAndPSBReadRetainRawSource(t *testing.T) {
	for _, version := range []uint16{1, 2} {
		placement := uniformDescriptorWire("null", semanticsDescriptorEntry{
			key: "quiltWarp", kind: "Objc", data: uniformWarpWire(10, 413, 413)})
		block := &psbWriter{}
		block.WriteString("soLD")
		block.u32(4)
		block.u32(16)
		block.Write(placement)
		data := semanticsDocument(version, nil, nil, semanticsBlock{key: "SoLd", data: block.Bytes()})
		original := bytes.Clone(data)
		document, err := ParseBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		warp := document.Layers[0].Placed.Warp
		if !bytes.Equal(data, original) || warp.DeformRows != 10 || len(warp.QuiltSlices) != 2 ||
			len(warp.Unsupported) != 0 || warp.QuiltSlices[1].Values[3] != 413.6 {
			t.Fatalf("PSD/PSB %d did not preserve original quilt metadata: %#v", version, warp)
		}
	}
}
