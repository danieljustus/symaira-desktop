package sidecar

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func holdMetadataReplacement(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestMetadataReplacementWaitsForWindowsReader(t *testing.T) {
	dir := t.TempDir()
	if err := recordSidecarMetadata(dir, "before"); err != nil {
		t.Fatal(err)
	}
	handle := holdMetadataReplacement(t, filepath.Join(dir, metadataFileName))
	defer func() {
		if handle != windows.InvalidHandle {
			_ = windows.CloseHandle(handle)
		}
	}()
	completed := make(chan error, 1)
	go func() { completed <- recordSidecarMetadata(dir, "after") }()
	select {
	case err := <-completed:
		t.Fatalf("metadata replacement finished while reader denied deletion: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	handle = windows.InvalidHandle
	if err := <-completed; err != nil {
		t.Fatalf("metadata replacement after reader closed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, metadataFileName))
	if err != nil || !bytes.Contains(data, []byte(`"vault_path":"after"`)) {
		t.Fatalf("replacement record = %s, error %v", data, err)
	}
}

func TestMetadataReplacementPreservesRecordOnWindowsTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := recordSidecarMetadata(dir, "before"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, metadataFileName)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	handle := holdMetadataReplacement(t, path)
	defer func() { _ = windows.CloseHandle(handle) }()
	if err := recordSidecarMetadata(dir, "after"); err == nil {
		t.Fatal("persistent reader lock was hidden")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("old metadata changed on failed replacement: %s, error %v", after, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".metadata-*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files after failed replacement = %v, error %v", leftovers, err)
	}
}
