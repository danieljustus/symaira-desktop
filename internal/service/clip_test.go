package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

func writeFakeClipper(t *testing.T, dir, name, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".bat"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0700); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
}

// withFakeClipperOnPath restricts PATH to the fake clipper plus minimal
// system directories, deliberately excluding any real sibling tools so tests
// exercise NoteClip and the built-in FTS5 search fallback in isolation, not
// whatever happens to be installed on the developer's machine. $HOME is
// isolated for the same reason: the managed runtime directory
// (~/.symaira/bin) is searched ahead of PATH.
func withFakeClipperOnPath(t *testing.T, dir string) {
	t.Helper()
	old := os.Getenv("PATH")
	_ = os.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	compose.ResetCache()
	t.Cleanup(func() {
		_ = os.Setenv("PATH", old)
		compose.ResetCache()
	})
}

func TestNoteClipWithoutAnyClipper(t *testing.T) {
	svc := newTestService(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	// Isolate $HOME too: a real ~/.symaira/bin clipper on the machine
	// running this test must not make Resolve find a binary that PATH
	// alone deliberately excludes here.
	t.Setenv("HOME", t.TempDir())
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	_, err := svc.NoteClip("https://example.com")
	if err == nil {
		t.Fatal("expected an error when no web clipper is installed")
	}
	if !strings.Contains(err.Error(), "symbrowse") {
		t.Errorf("expected error to point at symbrowse, got %q", err)
	}
	if !strings.Contains(err.Error(), "brew install") {
		t.Errorf("expected error to include install instructions, got %q", err)
	}
}

func TestNoteClipCreatesNoteFromSymbrowseOutput(t *testing.T) {
	svc := newTestService(t)
	dir := t.TempDir()
	writeFakeClipper(t, dir, "symbrowse", "#!/bin/sh\ncat <<'EOF'\n---\ntitle: Example Domain\nurl: https://example.com\nfetched_at: 2026-08-23T10:00:00Z\n---\n# Example Domain\n\nThis domain is for illustrative examples.\nEOF\n")
	withFakeClipperOnPath(t, dir)

	fileName, err := svc.NoteClip("https://example.com")
	if err != nil {
		t.Fatalf("NoteClip: %v", err)
	}
	if fileName != "Clipped:_Example_Domain.md" {
		t.Errorf("unexpected file name: %q", fileName)
	}

	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, fileName)) //nolint:gosec // fileName is a test fixture path inside t.TempDir()
	if err != nil {
		t.Fatalf("reading clipped note: %v", err)
	}
	body := string(content)
	if !strings.Contains(body, `title: "Clipped: Example Domain"`) {
		t.Errorf("note missing expected title frontmatter, got:\n%s", body)
	}
	if !strings.Contains(body, `source_uri: "https://example.com"`) {
		t.Errorf("note missing expected source_uri frontmatter, got:\n%s", body)
	}
	if !strings.Contains(body, "This domain is for illustrative examples.") {
		t.Errorf("note missing fetched body content, got:\n%s", body)
	}

	results, err := svc.Search("illustrative examples")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected the clipped note to be indexed and searchable, got %d hits", len(results))
	}
}

func TestNoteClipReadsSymbrowseFrontmatterTitle(t *testing.T) {
	svc := newTestService(t)
	dir := t.TempDir()
	// symbrowse answers with the shared schema's YAML frontmatter; the note
	// name must come from its title key.
	writeFakeClipper(t, dir, "symbrowse", "#!/bin/sh\ncat <<'EOF'\n---\ntitle: Browsed Page\nurl: https://example.net/\nfetched_at: 2026-08-23T10:00:00Z\n---\n# Browsed Page\n\nRendered by the browser engine.\nEOF\n")
	withFakeClipperOnPath(t, dir)

	fileName, err := svc.NoteClip("https://example.net")
	if err != nil {
		t.Fatalf("NoteClip: %v", err)
	}
	if fileName != "Clipped:_Browsed_Page.md" {
		t.Errorf("expected the note name to come from the symbrowse frontmatter title, got %q", fileName)
	}

	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, fileName)) //nolint:gosec // test reads its own temp vault fixture
	if err != nil {
		t.Fatalf("reading clipped note: %v", err)
	}
	if !strings.Contains(string(content), "Rendered by the browser engine.") {
		t.Errorf("note missing the browsed body, got:\n%s", content)
	}
}

func TestNoteClipFallsBackToHeadingWhenNoFrontmatterTitle(t *testing.T) {
	svc := newTestService(t)
	dir := t.TempDir()
	writeFakeClipper(t, dir, "symbrowse", "#!/bin/sh\ncat <<'EOF'\n# Fallback Heading\n\nBody text.\nEOF\n")
	withFakeClipperOnPath(t, dir)

	fileName, err := svc.NoteClip("https://example.org")
	if err != nil {
		t.Fatalf("NoteClip: %v", err)
	}
	if fileName != "Clipped:_Fallback_Heading.md" {
		t.Errorf("unexpected file name: %q", fileName)
	}
}

func TestNoteClipFallsBackToURLWhenNoTitleFound(t *testing.T) {
	svc := newTestService(t)
	dir := t.TempDir()
	writeFakeClipper(t, dir, "symbrowse", "#!/bin/sh\necho 'just some plain text with no title markers'\n")
	withFakeClipperOnPath(t, dir)

	// The URL includes a path and scheme separators ("://", "/") to confirm
	// the fallback-to-URL title is sanitized into a valid file name instead
	// of being rejected by vault.SecurePath's path-traversal protection.
	fileName, err := svc.NoteClip("https://example.net/some/page")
	if err != nil {
		t.Fatalf("NoteClip: %v", err)
	}
	if !strings.Contains(fileName, "example.net") || strings.ContainsAny(fileName, "/\\") {
		t.Errorf("expected a sanitized file name derived from the URL, got %q", fileName)
	}
	if _, err := os.Stat(filepath.Join(svc.VaultRoot, fileName)); err != nil {
		t.Errorf("expected the note to be written at the sanitized path: %v", err)
	}
}

func TestNoteClipPropagatesSymbrowseFailure(t *testing.T) {
	svc := newTestService(t)
	dir := t.TempDir()
	writeFakeClipper(t, dir, "symbrowse", "#!/bin/sh\necho 'boom' >&2\nexit 1\n")
	withFakeClipperOnPath(t, dir)

	_, err := svc.NoteClip("https://example.com")
	if err == nil {
		t.Fatal("expected an error when symbrowse exits non-zero")
	}
	if !strings.Contains(err.Error(), "symbrowse failed") {
		t.Errorf("expected error to mention symbrowse failure, got %q", err)
	}
}
