//nolint:gosec // test fixtures write PDF fixtures into t.TempDir() and read injected tool paths; the whole test file is suppressed per repo convention
package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/pdfops"
)

// writeFakeTool creates a fake external PDF tool so the real MergePDFs and
// RotatePDF implementations run end-to-end without poppler/qpdf installed
// (issue #643; seam from issue #644).
func writeFakeTool(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMergePDFsRealPath(t *testing.T) {
	dir := t.TempDir()
	input1 := filepath.Join(dir, "a.pdf")
	input2 := filepath.Join(dir, "b.pdf")
	output := filepath.Join(dir, "nested", "merged.pdf")
	for _, p := range []string{input1, input2} {
		if err := os.WriteFile(p, []byte("%PDF-1.4\n"+filepath.Base(p)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The fake pdfunite concatenates its PDF inputs into the last argument
	// (the output path), mirroring the real tool's contract.
	fakeUnite := writeFakeTool(t, dir, "pdfunite", `
last=""
for arg in "$@"; do last="$arg"; done
cat "$1" "$2" > "$last"
`)
	pdfops.WithToolsFactory(func() pdfops.Tools {
		return pdfops.Tools{PDFUnite: fakeUnite}
	}, func() {
		if err := MergePDFs(context.Background(), []string{input1, input2}, output); err != nil {
			t.Fatalf("MergePDFs: %v", err)
		}
		data, err := os.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "a.pdf") || !strings.Contains(string(data), "b.pdf") {
			t.Errorf("merged output missing input contents: %q", data)
		}
	})

	// Failure path: unresolvable merge tool surfaces a clear error.
	pdfops.WithToolsFactory(func() pdfops.Tools {
		return pdfops.Tools{PDFUnite: filepath.Join(dir, "missing-pdfunite")}
	}, func() {
		err := MergePDFs(context.Background(), []string{input1, input2}, filepath.Join(dir, "out.pdf"))
		if err == nil {
			t.Fatal("expected error for missing pdfunite")
		}
		if !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("error = %q, want 'not found in PATH'", err.Error())
		}
	})
}

func TestRotatePDFRealPath(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.pdf")
	output := filepath.Join(dir, "rotated.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\npage\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The fake qpdf records its arguments and copies input to output; the
	// --rotate argument must carry the degrees and optional page selector.
	argsFile := filepath.Join(dir, "qpdf-args.txt")
	fakeQpdf := writeFakeTool(t, dir, "qpdf", `printf '%s\n' "$@" > `+argsFile+`
cp "$2" "$3"
`)
	pdfops.WithToolsFactory(func() pdfops.Tools {
		return pdfops.Tools{QPDF: fakeQpdf}
	}, func() {
		if err := RotatePDF(context.Background(), input, output, 90, "1-3"); err != nil {
			t.Fatalf("RotatePDF: %v", err)
		}
		if _, err := os.Stat(output); err != nil {
			t.Fatalf("rotated output missing: %v", err)
		}
		args, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(args), "--rotate=+90:1-3") {
			t.Errorf("qpdf args = %q, want --rotate=+90:1-3", string(args))
		}
	})

	// Invalid degrees fail before any tool invocation.
	pdfops.WithToolsFactory(func() pdfops.Tools {
		return pdfops.Tools{QPDF: fakeQpdf}
	}, func() {
		err := RotatePDF(context.Background(), input, output, 45, "")
		if err == nil || !strings.Contains(err.Error(), "rotation must be one of") {
			t.Fatalf("error = %v, want invalid-rotation error", err)
		}
	})

	// Unresolvable qpdf surfaces a clear error.
	pdfops.WithToolsFactory(func() pdfops.Tools {
		return pdfops.Tools{QPDF: filepath.Join(dir, "missing-qpdf")}
	}, func() {
		err := RotatePDF(context.Background(), input, output, 90, "")
		if err == nil {
			t.Fatal("expected error for missing qpdf")
		}
		if !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("error = %q, want 'not found in PATH'", err.Error())
		}
	})
}
