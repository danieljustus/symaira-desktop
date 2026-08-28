package sidecar

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenForVaultRecordsMetadataInConfiguredDataRoot(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	tmpVault := t.TempDir()

	db, err := OpenForVault(tmpVault)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	root, err := SidecarRoot()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := ListSidecars()
	if err != nil {
		t.Fatal(err)
	}
	canonicalVault, err := filepath.EvalSymlinks(tmpVault)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].VaultPath != canonicalVault || !entries[0].Metadata {
		t.Fatalf("unexpected isolated sidecar inventory under %s: %+v", root, entries)
	}
}

func TestListAndRemoveOrphanSidecars(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	root, err := SidecarRoot()
	if err != nil {
		t.Fatal(err)
	}
	orphanDir := filepath.Join(root, "orphan")
	liveDir := filepath.Join(root, "live")
	for _, dir := range []string{orphanDir, liveDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sidecar.db"), []byte("derived"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordSidecarMetadata(orphanDir, filepath.Join(dataRoot, "missing-vault")); err != nil {
		t.Fatal(err)
	}
	liveVault := filepath.Join(dataRoot, "live-vault")
	if err := os.MkdirAll(liveVault, 0700); err != nil {
		t.Fatal(err)
	}
	if err := recordSidecarMetadata(liveDir, liveVault); err != nil {
		t.Fatal(err)
	}

	entries, err := ListSidecars()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("unexpected inventory: %+v", entries)
	}
	var orphanFound bool
	for _, entry := range entries {
		if entry.Orphan {
			orphanFound = true
		}
	}
	if !orphanFound {
		t.Fatalf("expected orphan inventory entry: %+v", entries)
	}
	removed, err := RemoveOrphanSidecars()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed=%d, want 1", removed)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("orphan sidecar still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(liveDir, "sidecar.db")); err != nil {
		t.Fatalf("live sidecar was removed: %v", err)
	}
}
