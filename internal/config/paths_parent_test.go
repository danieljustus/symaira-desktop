package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestIngestDataPathRejectsParentNames(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	for _, name := range []string{"..", "  ..  ", "nested" + string(filepath.Separator) + ".." + string(filepath.Separator) + ".."} {
		t.Run(name, func(t *testing.T) {
			path, err := IngestDataPath(name)
			if err == nil {
				t.Fatalf("IngestDataPath(%q) returned %q without rejecting the parent component", name, path)
			}
			if path != "" {
				t.Errorf("invalid artifact returned a path: %q", path)
			}
			want := fmt.Sprintf("ingest data artifact must be a single relative name: %q", name)
			if err.Error() != want {
				t.Errorf("error = %q, want %q", err.Error(), want)
			}
		})
	}
	entries, err := os.ReadDir(dataHome)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid artifact validation wrote %d entries", len(entries))
	}
}
