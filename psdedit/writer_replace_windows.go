package psdedit

import (
	"os"
	"path/filepath"
)

func replaceSavedFile(temporary, filename string) error {
	root, err := os.OpenRoot(filepath.Dir(filename))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// Root.Rename selects Windows handle-relative replacement while preserving existing source readers.
	return root.Rename(filepath.Base(temporary), filepath.Base(filename))
}
