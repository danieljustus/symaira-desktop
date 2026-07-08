package watcher_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/watcher"
)

func TestInboxWatcher_IngestsAndRemoves(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "symdesk-watcher-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	vaultRoot := filepath.Join(tempDir, "vault")
	if err := os.MkdirAll(filepath.Join(vaultRoot, "inbox"), 0755); err != nil {
		t.Fatalf("failed to create vault inbox: %v", err)
	}

	dbPath := filepath.Join(tempDir, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open sidecar DB: %v", err)
	}
	defer db.Close()

	if err := db.CheckIntegrity(); err != nil {
		t.Fatalf("integrity check failed: %v", err)
	}

	svc := service.New(vaultRoot, db)

	watchDir := filepath.Join(tempDir, "inbox_watch")
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

	// Wait up to 5 seconds for debounce and ingestion
	deadline := time.Now().Add(5 * time.Second)
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
