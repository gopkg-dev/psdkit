//go:build !windows

package psdfile

import "os"

func openSourceFile(filename string) (*os.File, error) {
	return os.Open(filename)
}
