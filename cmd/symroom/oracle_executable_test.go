package main

import (
	"path/filepath"
	"runtime"
	"testing"
)

func oracleExecutablePath(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(t.TempDir(), name)
}
