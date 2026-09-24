//go:build windows

package service

import "testing"

func retentionStateCreateFIFO(t *testing.T, _ string) {
	t.Helper()
	t.Fatal("Unix-only FIFO setup was not platform-filtered")
}
