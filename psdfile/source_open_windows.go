package psdfile

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func openSourceFile(filename string) (*os.File, error) {
	if filename == "" {
		return nil, &os.PathError{Op: "open", Path: filename, Err: syscall.ERROR_FILE_NOT_FOUND}
	}
	path, err := filepath.Abs(filename)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filename, Err: err}
	}
	if len(path) >= 248 && !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\\.\`) {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + path[2:]
		} else {
			path = `\\?\` + path
		}
	}
	wide, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filename, Err: err}
	}
	// Keep the original file readable after Save replaces its path, matching Unix descriptor ownership.
	sharing := uint32(syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE)
	handle, err := syscall.CreateFile(wide, syscall.GENERIC_READ, sharing, nil, syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filename, Err: err}
	}
	return os.NewFile(uintptr(handle), filename), nil
}
