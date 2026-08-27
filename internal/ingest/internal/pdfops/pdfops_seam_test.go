//nolint:gosec // test fixtures write PDF fixtures into t.TempDir() and read injected tool paths; the whole test file is suppressed per repo convention
package pdfops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewToolsOverride verifies the package-level tools factory seam: the
// default resolves the standard tool names, and WithToolsFactory injects fake
// tool paths that the operations then execute (issue #644).
func TestNewToolsOverride(t *testing.T) {
	if got := NewTools().PDFUnite; got != "pdfunite" {
		t.Fatalf("NewTools().PDFUnite = %q, want pdfunite", got)
	}

	dir := t.TempDir()
	input1 := filepath.Join(dir, "a.pdf")
	input2 := filepath.Join(dir, "b.pdf")
	output := filepath.Join(dir, "merged.pdf")
	for _, p := range []string{input1, input2} {
		if err := os.WriteFile(p, []byte("%PDF-1.4\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	fake := writeExecutable(t, dir, "pdfunite", `
last=""
for arg in "$@"; do last="$arg"; done
cat "$1" "$2" > "$last"
`)
	WithToolsFactory(func() Tools {
		return Tools{PDFUnite: fake}
	}, func() {
		if got := NewTools().PDFUnite; got != fake {
			t.Fatalf("NewTools().PDFUnite = %q, want injected %q", got, fake)
		}
		if err := NewTools().Merge(context.Background(), []string{input1, input2}, output); err != nil {
			t.Fatalf("Merge with injected tools: %v", err)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "%PDF-1.4") {
			t.Errorf("output is not PDF-like: %q", data)
		}
	})

	// The factory is restored after WithToolsFactory returns.
	if got := NewTools().PDFUnite; got != "pdfunite" {
		t.Fatalf("factory not restored: PDFUnite = %q, want pdfunite", got)
	}
}

// TestNewToolsMissingTool pins the error message when a configured tool name
// cannot be resolved through PATH.
func TestNewToolsMissingTool(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "a.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	WithToolsFactory(func() Tools {
		return Tools{PDFUnite: filepath.Join(dir, "definitely-not-on-path-pdfunite")}
	}, func() {
		err := NewTools().Merge(context.Background(), []string{input, input}, filepath.Join(dir, "out.pdf"))
		if err == nil {
			t.Fatal("expected error for unresolvable tool")
		}
		if !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("error = %q, want 'not found in PATH'", err.Error())
		}
	})
}
