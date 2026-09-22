//go:build !windows

package service

import (
	"syscall"
	"testing"
)

func retentionStateCreateFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create retention-state FIFO %s: %v", path, err)
	}
}
