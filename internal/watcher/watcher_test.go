package watcher_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/watcher"
	"github.com/fsnotify/fsnotify"
)

// fakeEventSource is a test double for watcher.EventSource that lets tests
// inject synthetic fsnotify events without touching the filesystem or
// fsnotify directly.
type fakeEventSource struct {
	events chan fsnotify.Event
	errors chan error
	once   sync.Once
}

func newFakeEventSource() *fakeEventSource {
	return &fakeEventSource{
		events: make(chan fsnotify.Event),
		errors: make(chan error),
	}
}

func (f *fakeEventSource) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeEventSource) Errors() <-chan error          { return f.errors }

func (f *fakeEventSource) Close() error {
	f.once.Do(func() {
		close(f.events)
		close(f.errors)
	})
	return nil
}

func (f *fakeEventSource) send(event fsnotify.Event) {
	f.events <- event
}

func setupWatcherTest(t *testing.T) (watchDir, vaultRoot string, svc *service.Service, cleanup func()) {
	t.Helper()
	tempDir, err := os.MkdirTemp("", "symdesk-watcher-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	cleanup = func() { _ = os.RemoveAll(tempDir) }

	vaultRoot = filepath.Join(tempDir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "inbox"), 0755); err != nil {
		t.Fatalf("failed to create vault inbox: %v", err)
	}

	dbPath := filepath.Join(tempDir, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open sidecar DB: %v", err)
	}

	if err := db.CheckIntegrity(); err != nil {
		t.Fatalf("integrity check failed: %v", err)
	}

	svc = service.New(vaultRoot, db)
	watchDir = filepath.Join(tempDir, "inbox_watch")
	if err := os.MkdirAll(watchDir, 0755); err != nil {
		t.Fatalf("failed to create watch dir: %v", err)
	}

	cleanup = func() {
		_ = db.Close()
		_ = os.RemoveAll(tempDir)
	}
	return watchDir, vaultRoot, svc, cleanup
}

func TestInboxWatcher_IngestsAndRemoves(t *testing.T) {
	watchDir, vaultRoot, svc, cleanup := setupWatcherTest(t)
	defer cleanup()

	fake := newFakeEventSource()
	w, err := watcher.NewInboxWatcherWithTiming(watchDir, svc, fake, 1*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := w.Start(ctx); err != nil {
			// ignore start error when context cancelled
		}
	}()

	testFilePath := filepath.Join(watchDir, "test-document.txt")
	testContent := fmt.Sprintf("Test content for watched inbox: %d", time.Now().UnixNano())
	if err := os.WriteFile(testFilePath, []byte(testContent), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	fake.send(fsnotify.Event{Name: testFilePath, Op: fsnotify.Create})

	deadline := time.Now().Add(5 * time.Second)
	success := false
	for time.Now().Before(deadline) {
		if _, err := os.Stat(testFilePath); os.IsNotExist(err) {
			hasMd := false
			_ = filepath.Walk(vaultRoot, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && filepath.Ext(p) == ".md" {
					hasMd = true
				}
				return nil
			})
			if hasMd {
				success = true
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !success {
		t.Log("Watch directory contents:")
		_ = filepath.Walk(watchDir, func(p string, info os.FileInfo, err error) error {
			t.Logf("  - %s (err: %v)", p, err)
			return nil
		})
		t.Log("Vault directory contents:")
		_ = filepath.Walk(vaultRoot, func(p string, info os.FileInfo, err error) error {
			t.Logf("  - %s (err: %v)", p, err)
			return nil
		})
		t.Error("expected file to be ingested and removed from watch directory, but it was not")
	}
}

func TestInboxWatcher_IngestsAndRemoves_Integration(t *testing.T) {
	watchDir, vaultRoot, svc, cleanup := setupWatcherTest(t)
	defer cleanup()

	w, err := watcher.NewInboxWatcher(watchDir, svc)
	if err != nil {
		t.Fatalf("failed to create watcher: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := w.Start(ctx); err != nil {
			// ignore start error when context cancelled
		}
	}()
	time.Sleep(200 * time.Millisecond)

	testFilePath := filepath.Join(watchDir, "test-document.txt")
	testContent := fmt.Sprintf("Test content for watched inbox: %d", time.Now().UnixNano())
	err = os.WriteFile(testFilePath, []byte(testContent), 0644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Production timing includes a two-second stability window and a one-second
	// ticker. Leave enough headroom for service ingestion under the race detector.
	deadline := time.Now().Add(10 * time.Second)
	success := false
	for time.Now().Before(deadline) {
		// Check if file is removed from watchDir
		if _, err := os.Stat(testFilePath); os.IsNotExist(err) {
			hasMd := false
			_ = filepath.Walk(vaultRoot, func(p string, info os.FileInfo, err error) error {
				if err == nil && !info.IsDir() && filepath.Ext(p) == ".md" {
					hasMd = true
				}
				return nil
			})
			if hasMd {
				success = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !success {
		t.Log("Watch directory contents:")
		_ = filepath.Walk(watchDir, func(p string, info os.FileInfo, err error) error {
			t.Logf("  - %s (err: %v)", p, err)
			return nil
		})
		t.Log("Vault directory contents:")
		_ = filepath.Walk(vaultRoot, func(p string, info os.FileInfo, err error) error {
			t.Logf("  - %s (err: %v)", p, err)
			return nil
		})
		t.Error("expected file to be ingested and removed from watch directory, but it was not")
	}
}
