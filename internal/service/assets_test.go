package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestServiceStoreAssetAndWithLink(t *testing.T) {
	vaultRoot := t.TempDir()
	dbPath := filepath.Join(vaultRoot, "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	svc := New(vaultRoot, db)

	// 1. Store asset
	data := []byte("binary-test-data")
	relPath, err := svc.StoreAsset(data, "photo.jpg", "jpg")
	if err != nil {
		t.Fatalf("StoreAsset failed: %v", err)
	}
	if relPath != "assets/photo.jpg" {
		t.Errorf("expected assets/photo.jpg, got %q", relPath)
	}

	content, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatalf("failed to read stored asset: %v", err)
	}
	if string(content) != string(data) {
		t.Errorf("stored content mismatch: got %q, want %q", string(content), string(data))
	}

	// 2. Store asset with collision and markdown link
	relPath2, mdLink, err := svc.StoreAssetWithLink(data, "photo.jpg", "jpg")
	if err != nil {
		t.Fatalf("StoreAssetWithLink failed: %v", err)
	}
	if relPath2 != "assets/photo-2.jpg" {
		t.Errorf("expected assets/photo-2.jpg, got %q", relPath2)
	}
	if mdLink != "![photo-2.jpg](assets/photo-2.jpg)" {
		t.Errorf("expected ![photo-2.jpg](assets/photo-2.jpg), got %q", mdLink)
	}

	// 3. Verify spaces are percent-encoded in link
	relPath3, mdLink3, err := svc.StoreAssetWithLink(data, "my image.png", "png")
	if err != nil {
		t.Fatalf("StoreAssetWithLink with spaces failed: %v", err)
	}
	if !strings.Contains(mdLink3, "assets/my%20image.png") {
		t.Errorf("expected percent-encoded link, got %q", mdLink3)
	}
	_ = relPath3
}
