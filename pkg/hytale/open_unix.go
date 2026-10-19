//go:build !windows

package hytale

import (
	"os"
	"syscall"
)

// Opens a file for reading without following a symlink at the last element
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
