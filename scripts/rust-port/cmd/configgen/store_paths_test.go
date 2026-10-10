package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestNativeStorePathsCaptureIsReadOnlyAndRequiresFreshRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "capture")
	t.Setenv("HOME", filepath.Join(parent, "outer-home"))
	t.Setenv("USERPROFILE", filepath.Join(parent, "outer-profile"))
	t.Setenv("SYMRELATE_DATA_HOME", filepath.Join(parent, "outer-data"))
	oracle := inventory.Oracle{Commit: "unit-fixture-only", Release: "unit-fixture-only"}
	captured, err := buildStorePaths(oracle, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured.Cases) != 15 {
		t.Fatalf("capture has %d layouts, want 15", len(captured.Cases))
	}
	for _, item := range captured.Cases {
		if len(item.Ingest) != 19 || !reflect.DeepEqual(item.Before, item.After) {
			t.Fatalf("incomplete or mutable layout %s", item.ID)
		}
	}
	if os.Getenv("HOME") != filepath.Join(parent, "outer-home") || os.Getenv("USERPROFILE") != filepath.Join(parent, "outer-profile") || os.Getenv("SYMRELATE_DATA_HOME") != filepath.Join(parent, "outer-data") {
		t.Fatal("capture leaked an isolated environment into the caller")
	}
	before, err := storePathSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildStorePaths(oracle, root); err == nil {
		t.Fatal("existing root must be rejected, never overwritten")
	}
	after, err := storePathSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("rejected capture changed the existing root")
	}
}
