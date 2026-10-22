package psdtext

import (
	"bytes"
	"testing"
)

func FuzzEngineDataBoundedRoundTrip(f *testing.F) {
	for _, data := range [][]byte{
		[]byte("<< /EngineDict << /Editor << /Text (\xfe\xff\x00A\x00\r) >> >> >>"),
		[]byte("[1 -0.5 true /N (hwid) <00f>]"),
		[]byte("<< /x << /x [1 2 3] >> /x false >>\x00"),
		[]byte("(\xfe\xff\xd8\x3d\xde\x00)"),
		[]byte("<< /x exec >>"),
	} {
		f.Add(data)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		engine, err := Decode(nil, data, DecodeOptions{MaxBytes: 64 << 10, MaxNodes: 1000, MaxDepth: 16})
		if err != nil {
			return
		}
		encoded, err := Encode(nil, engine, EncodeOptions{MaxBytes: 1 << 20, MaxNodes: 1000, MaxDepth: 16})
		if err != nil || !bytes.Equal(encoded, data) {
			t.Fatalf("accepted bounded syntax did not preserve exact input bytes: %v", err)
		}
	})
}
