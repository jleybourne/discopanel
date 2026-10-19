//go:build windows

package hytale

import "os"

// Windows has no O_NOFOLLOW, the Lstat check before the open stands alone
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
