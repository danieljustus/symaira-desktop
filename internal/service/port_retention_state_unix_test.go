//go:build !windows

package service

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func retentionStateCreateFIFO(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("create retention-state FIFO %s: %v", path, err)
	}
}

func TestPortRetentionStateContractTempAlias(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent, "real")
	alias := filepath.Join(parent, "alias")
	if err := os.Mkdir(real, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	capture := func(temp string) []byte {
		t.Helper()
		t.Setenv("TMPDIR", temp)
		encoded, err := json.Marshal(buildRetentionStateFixture(t))
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	want := capture(real)
	got := capture(alias)
	if !bytes.Equal(want, got) {
		t.Fatal("retention-state fixture changes when TMPDIR uses a symlink alias")
	}
}
