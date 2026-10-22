package psdmodel

import (
	"crypto/sha256"
	"encoding/binary"
	"hash"
	"math"
)

// RasterFingerprint binds a projected raster to all placed-layer metadata without reading pixel data.
// The field order and IEEE encodings deliberately preserve nil slices, signed zero and source slice order.
// Keep this representation synchronized with Placed and Warp when adding fields to those types.
func (placed *Placed) RasterFingerprint() [32]byte {
	if placed == nil {
		return [32]byte{}
	}
	writer := placementFingerprint{digest: sha256.New()}
	writer.text(placed.ID)
	writer.text(placed.Type)
	writer.word(uint64(placed.Width))
	writer.word(uint64(placed.Height))
	writer.numbers(placed.Transform)
	writer.numbers(placed.NonAffineTransform)
	writer.strings(placed.Unsupported)
	writer.flag(placed.Warp != nil)
	if warp := placed.Warp; warp != nil {
		writer.text(warp.ClassID)
		writer.text(warp.Style)
		writer.text(warp.Rotate)
		writer.text(warp.StyleEnum)
		writer.text(warp.RotateEnum)
		writer.number(warp.Value)
		writer.number(warp.Perspective)
		writer.number(warp.PerspectiveOther)
		writer.numbers(warp.Values)
		writer.flag(warp.HasValue)
		writer.flag(warp.HasValues)
		writer.word(uint64(warp.UOrder))
		writer.word(uint64(warp.VOrder))
		writer.word(uint64(warp.DeformRows))
		writer.word(uint64(warp.DeformColumns))
		writer.flag(warp.HasDeformRows)
		writer.flag(warp.HasDeformColumns)
		writer.flag(warp.QuiltSlices != nil)
		writer.word(uint64(len(warp.QuiltSlices)))
		for _, slice := range warp.QuiltSlices {
			writer.text(slice.Axis)
			writer.text(slice.Location)
			writer.text(slice.ClassID)
			writer.text(slice.Field)
			writer.text(slice.Kind)
			writer.text(slice.Unit)
			writer.word(uint64(slice.Prefix))
			writer.numbers(slice.Values)
		}
		writer.flag(warp.HasEnvelope)
		writer.flag(warp.Split)
		writer.flag(warp.Bounds != nil)
		if bounds := warp.Bounds; bounds != nil {
			writer.number(bounds.Left)
			writer.number(bounds.Top)
			writer.number(bounds.Right)
			writer.number(bounds.Bottom)
			writer.flag(bounds.Pixels)
			for _, unit := range bounds.Units {
				writer.text(unit)
			}
		}
		writer.flag(warp.MeshPoints != nil)
		writer.word(uint64(len(warp.MeshPoints)))
		for _, point := range warp.MeshPoints {
			writer.number(point.X)
			writer.number(point.Y)
		}
		for _, unit := range warp.MeshUnits {
			writer.text(unit)
		}
		writer.text(warp.MeshClass)
		writer.strings(warp.Fields)
		writer.strings(warp.Kinds)
		writer.strings(warp.Unsupported)
	}
	var result [32]byte
	writer.digest.Sum(result[:0])
	return result
}

type placementFingerprint struct {
	digest hash.Hash
	buffer [8]byte
}

func (writer *placementFingerprint) word(value uint64) {
	binary.BigEndian.PutUint64(writer.buffer[:], value)
	_, _ = writer.digest.Write(writer.buffer[:])
}

func (writer *placementFingerprint) flag(value bool) {
	word := uint64(0)
	if value {
		word = 1
	}
	writer.word(word)
}

func (writer *placementFingerprint) number(value float64) { writer.word(math.Float64bits(value)) }

func (writer *placementFingerprint) text(value string) {
	writer.word(uint64(len(value)))
	for len(value) != 0 {
		count := copy(writer.buffer[:], value)
		_, _ = writer.digest.Write(writer.buffer[:count])
		value = value[count:]
	}
}

func (writer *placementFingerprint) numbers(values []float64) {
	writer.flag(values != nil)
	writer.word(uint64(len(values)))
	for _, value := range values {
		writer.number(value)
	}
}

func (writer *placementFingerprint) strings(values []string) {
	writer.flag(values != nil)
	writer.word(uint64(len(values)))
	for _, value := range values {
		writer.text(value)
	}
}
