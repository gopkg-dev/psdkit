package psdscene_test

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/gopkg-dev/psdkit/psdfile"
	"github.com/gopkg-dev/psdkit/psdfilter"
	"github.com/gopkg-dev/psdkit/psdscene"
)

type filterCacheGuard struct {
	data          []byte
	forbidden     []psdfile.Span
	masks         []psdfile.Span
	forbiddenRead int
	maskRead      int
}

func (reader *filterCacheGuard) ReadAt(data []byte, offset int64) (int, error) {
	end := offset + int64(len(data))
	count, err := bytes.NewReader(reader.data).ReadAt(data, offset)
	for _, span := range reader.forbidden {
		if offset < span.Offset+span.Length && end > span.Offset {
			reader.forbiddenRead++
			start, stop := max(0, span.Offset-offset), min(int64(count), span.Offset+span.Length-offset)
			for position := start; position < stop; position++ {
				data[position] = 0
			}
		}
	}
	for _, span := range reader.masks {
		if offset < span.Offset+span.Length && end > span.Offset {
			reader.maskRead++
		}
	}
	return count, err
}

func TestNativeSceneFiltersReadMasksAndIgnorePoisonedRGBFilterArrays(t *testing.T) {
	if os.Getenv("PSDKIT_SMART_FILTER_NATIVE") != "1" {
		t.Skip("optional own native fixtures require PSDKIT_SMART_FILTER_NATIVE=1")
	}
	for _, name := range []string{"opaque-r2", "alpha-r3", "filter-before-scale"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "output", "smart-filter-native", name+".psd"))
			if err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256(data)
			reader := &filterCacheGuard{data: data}
			source, err := psdfile.Read(reader, int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			caches, err := source.FilterCaches(nil)
			if err != nil || len(caches) == 0 {
				t.Fatalf("native cache index missing: %v", err)
			}
			options := psdscene.Options{AssumeUntaggedSRGB: true,
				SmartFilters: &psdfilter.Options{ApproximateGaussian: true}}
			expected, err := psdscene.Render(nil, source, options)
			if err != nil {
				t.Fatal(err)
			}
			for _, cache := range caches {
				for _, array := range cache.Arrays {
					if array.Written != 0 && array.Data.Length != 0 {
						reader.forbidden = append(reader.forbidden, array.Data)
					}
				}
				if cache.Mask != nil {
					reader.masks = append(reader.masks, cache.Mask.Data)
				}
			}
			result, err := psdscene.Render(nil, source, options)
			if err != nil || reader.maskRead == 0 || !bytes.Equal(expected.Pix, result.Pix) {
				t.Fatalf("cache substitution or mask skip: RGB=%d mask=%d error=%v",
					reader.forbiddenRead, reader.maskRead, err)
			}
			if result.Rect.Dx() != int(source.Header.Width) || result.Rect.Dy() != int(source.Header.Height) {
				t.Fatal("filter output geometry differs from parent canvas")
			}
			owned := bytes.Clone(result.Pix)
			if err := source.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(owned, result.Pix) || before != sha256.Sum256(data) {
				t.Fatal("filter output aliases source or Close invalidated owned pixels")
			}
		})
	}
}
