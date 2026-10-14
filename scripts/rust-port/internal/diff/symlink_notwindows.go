//go:build !windows

package diff

import "os"

func createSandboxSymlink(root *os.Root, target, path string) error {
	return root.Symlink(target, path)
}
