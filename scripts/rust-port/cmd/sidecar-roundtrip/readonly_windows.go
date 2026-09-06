//go:build windows

package main

import (
	"os/exec"
)

// Windows has no portable chmod contract. Use the native DOS read-only
// attribute so the permission suite tests the same mechanism users have.
func setReadOnly(path string, readOnly bool) error {
	flag := "-R"
	if readOnly {
		flag = "+R"
	}
	return exec.Command("attrib", flag, path).Run()
}
