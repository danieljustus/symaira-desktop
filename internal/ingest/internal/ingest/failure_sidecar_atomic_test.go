package ingest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWriteFailureSidecarAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "document.txt")
	sidecar := path + ".error.json"
	if err := writeFailureSidecar(path, "before", errors.New("original")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFailureSidecar(path, "after", errors.New("replacement")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("replacement modified the published file in place instead of atomically replacing it")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode = %o, want 600", after.Mode().Perm())
	}
	data, err := readTestFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	var record failureSidecar
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.SourcePath != path || record.Stage != "after" || record.Error != "replacement" || record.FailedAt.IsZero() || !bytes.HasSuffix(data, []byte{'\n'}) {
		t.Fatal("sidecar schema, values, timestamp, or trailing newline changed")
	}

	stop := make(chan struct{})
	ready := make(chan struct{})
	readerDone := make(chan error, 1)
	var reads atomic.Uint64
	go func() {
		close(ready)
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			observed, err := readTestFile(sidecar)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				readerDone <- err
				return
			}
			var value failureSidecar
			if err := json.Unmarshal(observed, &value); err != nil {
				readerDone <- fmt.Errorf("reader observed incomplete JSON: %w", err)
				return
			}
			if value.SourcePath != path || value.FailedAt.IsZero() {
				readerDone <- errors.New("reader observed an incomplete record")
				return
			}
			reads.Add(1)
		}
	}()
	defer func() {
		close(stop)
		if err := <-readerDone; err != nil {
			t.Error(err)
		}
		if reads.Load() == 0 {
			t.Error("concurrent reader did not execute a valid-record assertion")
		}
	}()
	<-ready
	for i := 0; i < 24; i++ {
		if err := writeFailureSidecar(path, "replace", errors.New(strings.Repeat("message", 8192))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriteFailureSidecarFailedReplacementPreservesRecord(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires enforced Unix directory permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "document.txt")
	if err := writeFailureSidecar(path, "original", errors.New("original")); err != nil {
		t.Fatal(err)
	}
	before, err := readTestFile(path + ".error.json")
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G302: this private directory needs owner traversal to test failed publication.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() {
		//nolint:gosec // G302: restore owner-only directory access so t.TempDir can remove it.
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Error(err)
		}
	}()
	if err := writeFailureSidecar(path, "replacement", errors.New("replacement")); err == nil {
		t.Fatal("replacement in a non-writable directory must return its publication error")
	}
	after, err := readTestFile(path + ".error.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("failed replacement changed the previous complete record")
	}
}
