//go:build !windows

package psdedit

import "os"

func replaceSavedFile(temporary, filename string) error {
	return os.Rename(temporary, filename)
}
