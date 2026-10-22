// Package testfixture locates optional large test assets outside the source repository.
package testfixture

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Path returns a corpus path relative to the psdkit repository root.
// Missing optional assets skip the caller; an explicit corpus must be complete.
func Path(t testing.TB, relative string) string {
	t.Helper()
	directory := os.Getenv("PSDKIT_TESTDATA_DIR")
	explicit := directory != ""
	if directory == "" {
		_, filename, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal("cannot locate test fixture helper")
		}
		directory = filepath.Join(filepath.Dir(filename), "..", "..", "..", "psdkit-testdata")
	}
	return require(t, filepath.Join(directory, relative), explicit)
}

// Sample returns the private sample PSD selected by PSD_SAMPLE_PATH or the corpus.
func Sample(t testing.TB) string {
	t.Helper()
	if filename := os.Getenv("PSD_SAMPLE_PATH"); filename != "" {
		return require(t, filename, true)
	}
	return Path(t, filepath.Join("testdata", "sample.psd"))
}

func require(t testing.TB, filename string, explicit bool) string {
	t.Helper()
	info, err := os.Stat(filename)
	if os.IsNotExist(err) && !explicit && os.Getenv("PSDKIT_REQUIRE_CORPUS") != "1" {
		t.Skipf("optional corpus missing: %s; set PSDKIT_TESTDATA_DIR to enable", filename)
	}
	if err != nil {
		t.Fatalf("test corpus: %v", err)
	}
	if info.IsDir() {
		t.Fatalf("test corpus path is a directory: %s", filename)
	}
	return filename
}
