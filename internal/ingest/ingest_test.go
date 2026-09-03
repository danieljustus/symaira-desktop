package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestFile(t *testing.T) {
	// Exercise the built-in fallback: the pipeline refuses, so IngestFile must
	// still copy the file into the inbox and leave a placeholder note rather
	// than losing it.
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	vaultRoot := t.TempDir()
	srcDir := t.TempDir()

	src := filepath.Join(srcDir, "scan.pdf")
	if err := os.WriteFile(src, []byte("%PDF-1.4 fake"), 0o600); err != nil {
		t.Fatal(err)
	}

	relNote, err := IngestFile(vaultRoot, src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(relNote, "inbox/") || !strings.HasSuffix(relNote, ".md") {
		t.Errorf("unexpected note path: %s", relNote)
	}

	noteBytes, err := os.ReadFile(filepath.Join(vaultRoot, relNote)) //nolint:gosec // G304: test path is confined to the test fixture directory
	if err != nil {
		t.Fatal(err)
	}
	note := string(noteBytes)
	if !strings.Contains(note, `status: "inbox"`) {
		t.Errorf("frontmatter missing status: %s", note)
	}
	if !strings.Contains(note, "scan_") || !strings.Contains(note, ".pdf") {
		t.Errorf("note does not embed the copied asset: %s", note)
	}

	// The original file must be copied into the inbox.
	entries, err := os.ReadDir(filepath.Join(vaultRoot, "inbox"))
	if err != nil {
		t.Fatal(err)
	}
	var foundAsset bool
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "scan_") && strings.HasSuffix(e.Name(), ".pdf") {
			foundAsset = true
		}
	}
	if !foundAsset {
		t.Error("copied asset not found in inbox")
	}
}

func TestIngestFileMissingSource(t *testing.T) {
	if _, err := IngestFile(t.TempDir(), "/nonexistent/file.pdf"); err == nil {
		t.Error("expected error for missing source")
	}
}

// stubIngest points the document-pipeline seam at a scripted double.
func stubIngest(t *testing.T, fn func(source string, opts Options) (*Result, error)) {
	t.Helper()
	original := IngestFunc
	t.Cleanup(func() { IngestFunc = original })
	IngestFunc = func(_ context.Context, source string, opts Options) (*Result, error) {
		return fn(source, opts)
	}
}

func TestIngestDelegatesToPipeline(t *testing.T) {
	vaultRoot := t.TempDir()

	var seen Options
	stubIngest(t, func(_ string, opts Options) (*Result, error) {
		seen = opts
		return &Result{VaultPath: "mock_output.md"}, nil
	})

	srcFile := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(srcFile, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	relPath, err := IngestFile(vaultRoot, srcFile)
	if err != nil {
		t.Fatalf("IngestFile failed: %v", err)
	}
	if relPath != "mock_output.md" {
		t.Errorf("expected mock_output.md, got %s", relPath)
	}
	if seen.Vault != vaultRoot {
		t.Errorf("pipeline vault = %q, want %q", seen.Vault, vaultRoot)
	}
}

// The pipeline reports notes as absolute paths; callers expect a path relative
// to the vault they asked for.
func TestIngestFileRelativizesVaultPath(t *testing.T) {
	vaultRoot := t.TempDir()
	stubIngest(t, func(string, Options) (*Result, error) {
		return &Result{VaultPath: filepath.Join(vaultRoot, "notes", "scan.md")}, nil
	})

	srcFile := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(srcFile, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	relPath, err := IngestFile(vaultRoot, srcFile)
	if err != nil {
		t.Fatal(err)
	}
	if relPath != filepath.Join("notes", "scan.md") {
		t.Errorf("expected a vault-relative note path, got %q", relPath)
	}
}

// A duplicate is reported as such rather than silently rewritten through the
// built-in fallback, which would write a second note for the same content.
func TestIngestFileSurfacesDuplicate(t *testing.T) {
	stubIngest(t, func(string, Options) (*Result, error) {
		return nil, ErrDuplicate
	})

	srcFile := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(srcFile, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := IngestFile(t.TempDir(), srcFile)
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate, got %v", err)
	}
}

// When the pipeline cannot run at all, the file must still land in the inbox.
func TestIngestFileFallsBackWhenPipelineUnavailable(t *testing.T) {
	stubIngest(t, func(string, Options) (*Result, error) {
		return nil, ErrNoVault
	})

	vaultRoot := t.TempDir()
	srcFile := filepath.Join(t.TempDir(), "test.txt")
	if err := os.WriteFile(srcFile, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	relPath, err := IngestFile(vaultRoot, srcFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(relPath, "inbox/") {
		t.Errorf("expected the built-in inbox fallback, got %q", relPath)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, relPath)); err != nil {
		t.Errorf("fallback note was not written: %v", err)
	}
}
