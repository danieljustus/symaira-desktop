//go:build !windows

package sidecar

import "os"

func replaceMetadataFile(source, target string) error {
	return os.Rename(source, target)
}
