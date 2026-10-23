package psdfile

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"hash/adler32"
	"io"
	"math/bits"
	"reflect"
	"testing"
)

func TestLinkedTIFFDeflateDynamicHuffmanPeakBudget(t *testing.T) {
	var stream []byte
	var accumulator uint64
	var pending uint
	put := func(value uint64, width uint) {
		accumulator |= value << pending
		pending += width
		for pending >= 8 {
			stream = append(stream, byte(accumulator))
			accumulator >>= 8
			pending -= 8
		}
	}
	lengths := make([]int, 263)
	lengths[0], lengths[1] = 1, 2
	for index := 2; index < 257; index++ {
		lengths[index] = 10
	}
	copy(lengths[257:], []int{11, 12, 13, 14, 15, 15})
	put(5, 3)  // final, dynamic Huffman.
	put(6, 5)  // 263 literal/length codes.
	put(0, 5)  // one distance code.
	put(15, 4) // all 19 code-length entries.
	codeSymbols := []int{1, 2, 10, 11, 12, 13, 14, 15}
	codeLengths := make(map[int]int)
	for index, symbol := range codeSymbols {
		codeLengths[symbol] = index
	}
	for _, symbol := range []int{16, 17, 18, 0, 8, 7, 9, 6, 10, 5, 11, 4, 12, 3, 13, 2, 14, 1, 15} {
		_, exists := codeLengths[symbol]
		if exists {
			put(3, 3)
		} else {
			put(0, 3)
		}
	}
	for _, length := range append(append([]int(nil), lengths...), 1) {
		put(uint64(bits.Reverse8(uint8(codeLengths[length]))>>5), 3)
	}
	var count, next [16]int
	for _, length := range lengths {
		count[length]++
	}
	code := 0
	for width := 1; width < 16; width++ {
		code = (code + count[width-1]) << 1
		next[width] = code
	}
	codes := make([]int, len(lengths))
	for symbol, width := range lengths {
		codes[symbol] = next[width]
		next[width]++
	}
	for _, symbol := range []int{12, 34, 56, 256} {
		width := uint(lengths[symbol])
		put(uint64(bits.Reverse16(uint16(codes[symbol]))>>(16-width)), width)
	}
	if pending > 0 {
		stream = append(stream, byte(accumulator))
	}
	stream = append([]byte{0x78, 0x01}, stream...)
	stream = binary.BigEndian.AppendUint32(stream, adler32.Checksum([]byte{12, 34, 56}))
	inflated, err := zlib.NewReader(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	defer inflated.Close()
	decoded, err := io.ReadAll(inflated)
	if err != nil || !bytes.Equal(decoded, []byte{12, 34, 56}) {
		t.Fatalf("independent valid stream did not decode: %x, %v", decoded, err)
	}
	zlibState := reflect.ValueOf(inflated).Elem()
	flateState := zlibState.FieldByName("decompressor").Elem().Elem()
	stateBytes := int64(zlibState.Type().Size() + flateState.Type().Size())
	for _, name := range []string{"bits", "codebits"} {
		stateBytes += int64(flateState.FieldByName(name).Type().Elem().Size())
	}
	stateBytes += int64(flateState.FieldByName("dict").FieldByName("hist").Cap())
	for _, name := range []string{"h1", "h2"} {
		links := flateState.FieldByName(name).FieldByName("links")
		stateBytes += int64(links.Cap()) * int64(links.Type().Elem().Size())
		for index := 0; index < links.Len(); index++ {
			stateBytes += int64(links.Index(index).Cap()) * 4
		}
	}
	source, record := rasterSource(deflateTIFFFixture(binary.LittleEndian, 1, 1, 1, 3, 0, 8, 1,
		[][]byte{stream}, nil))
	defer source.Close()
	result, err := source.DecodeLinkedRaster(t.Context(), record, LinkedRasterOptions{MaxWorkingBytes: 72 << 10})
	if !errors.Is(err, ErrLimit) || result != nil {
		t.Fatalf("insufficient Huffman working budget accepted: %v", err)
	}
	result, err = source.DecodeLinkedRaster(t.Context(), record)
	if err != nil || !bytes.Equal(result.Pixels.Pix, []byte{12, 34, 56, 255}) {
		t.Fatalf("valid dynamic Huffman strip failed with sufficient budget: %v", err)
	}
	if stateBytes > result.WorkingBytes {
		t.Fatalf("legal Deflate state alone exceeds the entire reported work budget by %d bytes", stateBytes-result.WorkingBytes)
	}
}
