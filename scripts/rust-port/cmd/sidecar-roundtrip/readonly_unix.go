//go:build !windows

package main

import "os"

func setReadOnly(path string, readOnly bool) error {
	mode := os.FileMode(0o600)
	if readOnly {
		mode = 0o400
	}
	return os.Chmod(path, mode)
}
