//go:build windows

package vault

import "os"

func openRootReadFile(root *os.Root, relative string) (*os.File, error) {
	return root.Open(relative)
}
