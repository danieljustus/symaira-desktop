//go:build !windows

package diff

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBuildManifestNormalizesInternalAbsoluteSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	entries, err := buildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Path == "link" && entry.LinkTarget == "<SANDBOX>/target" {
			return
		}
	}
	t.Fatalf("normalized symlink missing: %#v", entries)
}

func TestBuildManifestCapturesUnixModesAndTypes(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dir")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "entry"), []byte("fixture"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("entry", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	entries, err := buildManifest(root)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]ManifestEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	if got := byPath["dir"]; got.Type != "directory" || got.Mode != 0o750 {
		t.Fatalf("directory manifest = %#v", got)
	}
	if got := byPath["dir/entry"]; got.Type != "file" || got.Mode != 0o640 || got.SHA256 == "" {
		t.Fatalf("file manifest = %#v", got)
	}
	if got := byPath["dir/link"]; got.Type != "symlink" || got.LinkTarget != "entry" {
		t.Fatalf("symlink manifest = %#v", got)
	}
}

func TestRunTimeoutKillsDescendantProcessGroup(t *testing.T) {
	caseSpec := Case{
		ID:        "timeout-child",
		Args:      []string{"-test.run=TestRunAndCompareIdenticalHelper"},
		Env:       map[string]string{"SYMDESK_PORT_HELPER": "1", "PORT_HELPER_MODE": "child"},
		TimeoutMS: 100,
	}
	result, err := Run(os.Args[0], caseSpec)
	if err != nil {
		t.Fatal(err)
	}
	if !result.TimedOut {
		t.Fatal("expected process timeout")
	}
	if result.Signal == "" {
		t.Fatal("expected terminating signal to be captured")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(result.Stdout)))
	if err != nil {
		t.Fatalf("parse helper child PID: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		err = syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descendant process %d survived group termination: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
