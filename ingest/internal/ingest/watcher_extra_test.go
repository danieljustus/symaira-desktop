package ingest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

func TestWatcherOptions_Defaults(t *testing.T) {
	opts := WatcherOptions{}
	if opts.StableFor != 0 {
		t.Errorf("StableFor = %v, want 0", opts.StableFor)
	}
	if opts.ProcessingDir != "" {
		t.Errorf("ProcessingDir = %q, want empty", opts.ProcessingDir)
	}
	if opts.ProcessedDir != "" {
		t.Errorf("ProcessedDir = %q, want empty", opts.ProcessedDir)
	}
	if opts.FailedDir != "" {
		t.Errorf("FailedDir = %q, want empty", opts.FailedDir)
	}
}

func TestWatcherOptions_WithValues(t *testing.T) {
	opts := WatcherOptions{
		StableFor:     2 * time.Second,
		ProcessingDir: "/tmp/processing",
		ProcessedDir:  "/tmp/processed",
		FailedDir:     "/tmp/failed",
	}
	if opts.StableFor != 2*time.Second {
		t.Errorf("StableFor = %v, want 2s", opts.StableFor)
	}
	if opts.ProcessingDir != "/tmp/processing" {
		t.Errorf("ProcessingDir = %q, want /tmp/processing", opts.ProcessingDir)
	}
}

func TestIsIgnored(t *testing.T) {
	tests := []struct {
		name string
		path string
		want bool
	}{
		{"hidden_file", ".hidden", true},
		{"hidden_file_in_dir", "dir/.hidden", true},
		{"normal_file", "document.txt", false},
		{"normal_dir", "inbox/file.txt", false},
		{"dot_in_middle", "file.name.txt", false},
		{"swap_file", "file.swp", true},
		{"temp_file", "file.tmp", true},
		{"backup_file", "file~", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isIgnored(tt.path)
			if got != tt.want {
				t.Errorf("isIgnored(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsPathWithin(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		parent string
		want   bool
	}{
		{"within", "/a/b/c", "/a/b", true},
		{"same", "/a/b", "/a/b", true},
		{"outside", "/a/c", "/a/b", false},
		{"partial_match", "/a/bc", "/a/b", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPathWithin(tt.path, tt.parent)
			if got != tt.want {
				t.Errorf("isPathWithin(%q, %q) = %v, want %v", tt.path, tt.parent, got, tt.want)
			}
		})
	}
}

func TestMoveFileToDir(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	src := filepath.Join(srcDir, "test.txt")
	if err := os.WriteFile(src, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst, err := moveFileToDir(src, dstDir)
	if err != nil {
		t.Fatalf("moveFileToDir: %v", err)
	}

	if _, err := os.Stat(dst); err != nil {
		t.Errorf("file not moved: %v", err)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source file still exists")
	}
}

func TestMoveFileToDir_SourceNotFound(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	src := filepath.Join(srcDir, "nonexistent.txt")
	_, err := moveFileToDir(src, dstDir)
	if err == nil {
		t.Fatal("expected error for missing source")
	}
}

func TestWriteFailureSidecar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if err := writeFailureSidecar(path, "test", os.ErrNotExist); err != nil {
		t.Fatalf("writeFailureSidecar: %v", err)
	}

	sidecar := path + ".error.json"
	if _, err := os.Stat(sidecar); err != nil {
		t.Errorf("sidecar not written: %v", err)
	}

	data, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Error("sidecar is empty")
	}
}

func TestCleanOptionalDir(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")

	if err := os.MkdirAll(subdir, 0o700); err != nil {
		t.Fatal(err)
	}

	cleaned, err := cleanOptionalDir(subdir)
	if err != nil {
		t.Fatalf("cleanOptionalDir: %v", err)
	}
	if cleaned == "" {
		t.Error("cleaned path is empty")
	}
	if !filepath.IsAbs(cleaned) {
		t.Errorf("cleaned path %q is not absolute", cleaned)
	}
}

func TestCleanOptionalDir_Empty(t *testing.T) {
	cleaned, err := cleanOptionalDir("")
	if err != nil {
		t.Fatalf("cleanOptionalDir: %v", err)
	}
	if cleaned != "" {
		t.Errorf("cleaned = %q, want empty", cleaned)
	}
}

func TestNewWatcherWithOptions(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "test.db")

	st, err := store.Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	w, err := NewWatcherWithOptions(st, dir, WatcherOptions{
		StableFor: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewWatcherWithOptions: %v", err)
	}
	if w == nil {
		t.Fatal("watcher is nil")
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestStartWorker_ContextCancel(t *testing.T) {
	db := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(db)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	pipeline := &Pipeline{
		Store: st,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		StartWorker(ctx, pipeline)
		close(done)
	}()

	select {
	case <-done:
		// Worker exited as expected.
	case <-time.After(1 * time.Second):
		t.Fatal("worker did not exit after context cancel")
	}
}

func TestFileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	if fileExists(path) {
		t.Error("fileExists returned true for non-existent file")
	}

	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !fileExists(path) {
		t.Error("fileExists returned false for existing file")
	}
}

func TestWatcherOptions_AllFields(t *testing.T) {
	opts := WatcherOptions{
		StableFor:     5 * time.Second,
		ProcessingDir: "/tmp/proc",
		ProcessedDir:  "/tmp/done",
		FailedDir:     "/tmp/fail",
	}

	if opts.StableFor != 5*time.Second {
		t.Errorf("StableFor = %v, want 5s", opts.StableFor)
	}
	if opts.ProcessingDir != "/tmp/proc" {
		t.Errorf("ProcessingDir = %q, want /tmp/proc", opts.ProcessingDir)
	}
	if opts.ProcessedDir != "/tmp/done" {
		t.Errorf("ProcessedDir = %q, want /tmp/done", opts.ProcessedDir)
	}
	if opts.FailedDir != "/tmp/fail" {
		t.Errorf("FailedDir = %q, want /tmp/fail", opts.FailedDir)
	}
}
