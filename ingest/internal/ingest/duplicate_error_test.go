package ingest

import (
	"errors"
	"strings"
	"testing"
)

func TestDuplicateError_Error(t *testing.T) {
	err := &DuplicateError{
		SourcePath:  "/source/doc.txt",
		VaultPath:   "/vault/doc.md",
		ArchivePath: "/archive/doc.txt",
	}
	got := err.Error()
	for _, want := range []string{"/source/doc.txt", "/vault/doc.md", "/archive/doc.txt", "already ingested"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, missing %q", got, want)
		}
	}
}

func TestDuplicateError_Is(t *testing.T) {
	err := &DuplicateError{SourcePath: "/source/doc.txt"}
	if !errors.Is(err, ErrDuplicate) {
		t.Error("errors.Is(err, ErrDuplicate) = false, want true")
	}
	if errors.Is(err, errors.New("other")) {
		t.Error("errors.Is(err, other) = true, want false")
	}
}
