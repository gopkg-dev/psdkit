package psdparse

import (
	"testing"

	"github.com/gopkg-dev/psdkit/internal/testfixture"
	"github.com/gopkg-dev/psdkit/psdrender"
)

func TestSampleMetadata(t *testing.T) {
	filename := testfixture.Sample(t)
	document, err := ParseFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := psdrender.Inspect(document)
	if err != nil {
		t.Fatal(err)
	}
	instances := 0
	for _, object := range metadata.Objects {
		instances += len(object.Instances)
	}
	t.Logf("canvas=%dx%d layers=%d objects=%d instances=%d slices=%d warnings=%v blockers=%v",
		document.Width, document.Height, len(document.Layers), len(metadata.Objects), instances, len(metadata.Slices),
		metadata.Warnings, metadata.BlockingIssues)
	if instances != 14 || len(metadata.Slices) != 9 {
		t.Fatalf("real PSD sample metadata differs from frontend: %d instances, %d slices",
			instances, len(metadata.Slices))
	}
	if len(metadata.BlockingIssues) != 0 {
		t.Fatalf("supported sample was rejected: %v", metadata.BlockingIssues)
	}
}
