package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/config"
	"github.com/danieljustus/symaira-print/internal/press"
)

// overwriteTestDoc is a minimal document that passes the contract for the
// built-in "report" profile (only title is required).
const overwriteTestDoc = "---\nprofile: report\ntitle: Test\n---\n\n# Hello\n"

// fakeTypst installs a typst shim that reports a version and writes a %PDF
// marker to its final argument (the output path), so render tests exercise
// the full pipeline without depending on a real engine on PATH.
func fakeTypst(t *testing.T) {
	t.Helper()
	press.ResetProbeCache()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "typst")
	content := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--version\" ]; then echo \"typst 0.15.0 (fake)\"; exit 0; fi\n" +
		"eval out=\\${$#}\n" +
		"printf '%%PDF-fake' > \"$out\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("WriteFile(fake typst) error = %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// runRender executes the render command with the given args against an
// isolated HOME so no host config leaks into the test.
func runRender(t *testing.T, args ...string) error {
	t.Helper()
	jsonOut = false
	config.ResetForTest()
	t.Setenv("HOME", t.TempDir())
	cmd := newRenderCmd()
	cmd.SetArgs(args)
	return cmd.Execute()
}

// renderFixture writes a valid input document and returns its path plus the
// output path it would render to.
func renderFixture(t *testing.T) (input, output string) {
	t.Helper()
	dir := t.TempDir()
	input = filepath.Join(dir, "brief.md")
	if err := os.WriteFile(input, []byte(overwriteTestDoc), 0o644); err != nil {
		t.Fatalf("WriteFile(input) error = %v", err)
	}
	return input, filepath.Join(dir, "out.pdf")
}

func TestRenderCmdOverwrite_FlagRegistered(t *testing.T) {
	flag := newRenderCmd().Flags().Lookup("overwrite")
	if flag == nil {
		t.Fatal("render command is missing the --overwrite flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--overwrite default = %q, want false", flag.DefValue)
	}
}

func TestRenderCmdOverwrite_MissingOutputPath(t *testing.T) {
	fakeTypst(t)
	in, out := renderFixture(t)
	if err := runRender(t, in, "-o", out); err != nil {
		t.Fatalf("render to a missing output path failed: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("output was not created: %v", err)
	}
	if string(data) != "%PDF-fake" {
		t.Errorf("output content = %q, want %q", data, "%PDF-fake")
	}
}

func TestRenderCmdOverwrite_ExistingPDFReplaced(t *testing.T) {
	fakeTypst(t)
	in, out := renderFixture(t)
	if err := os.WriteFile(out, []byte("%PDF-1.4\nold"), 0o644); err != nil {
		t.Fatalf("WriteFile(existing PDF) error = %v", err)
	}
	// Existing PDF outputs keep the normal replace-without-flag behavior.
	if err := runRender(t, in, "-o", out); err != nil {
		t.Fatalf("render over an existing PDF failed without --overwrite: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "%PDF-fake" {
		t.Errorf("existing PDF was not replaced: got %q, want %q", data, "%PDF-fake")
	}
}

func TestRenderCmdOverwrite_ExistingNonPDFRefused(t *testing.T) {
	in, out := renderFixture(t)
	const original = "precious data"
	if err := os.WriteFile(out, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile(existing non-PDF) error = %v", err)
	}
	// No fake typst installed: the guard must refuse before any engine work.
	err := runRender(t, in, "-o", out)
	if err == nil {
		t.Fatal("expected refusal to overwrite an existing non-PDF file, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite existing non-PDF file") {
		t.Errorf("error = %q, want an overwrite refusal", err)
	}
	var cliErr *exitcodes.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *exitcodes.CLIError, got %T", err)
	}
	if cliErr.Code != exitcodes.ExitConflict {
		t.Errorf("exit code = %v, want %v", cliErr.Code, exitcodes.ExitConflict)
	}
	if cliErr.Kind != exitcodes.KindConflict {
		t.Errorf("kind = %v, want %v", cliErr.Kind, exitcodes.KindConflict)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != original {
		t.Errorf("refused target was modified: got %q, want %q", data, original)
	}
}

func TestRenderCmdOverwrite_ExistingNonPDFAllowed(t *testing.T) {
	fakeTypst(t)
	in, out := renderFixture(t)
	if err := os.WriteFile(out, []byte("precious data"), 0o644); err != nil {
		t.Fatalf("WriteFile(existing non-PDF) error = %v", err)
	}
	if err := runRender(t, in, "-o", out, "--overwrite"); err != nil {
		t.Fatalf("render with --overwrite failed: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(data) != "%PDF-fake" {
		t.Errorf("non-PDF output was not overwritten: got %q, want %q", data, "%PDF-fake")
	}
}

func TestRenderCmdOverwrite_DirectoryRefused(t *testing.T) {
	in, out := renderFixture(t)
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatalf("MkdirAll(output dir) error = %v", err)
	}
	// The directory refusal is unconditional, matching the MCP guard: even
	// --overwrite does not bypass it.
	for _, extra := range [][]string{{}, {"--overwrite"}} {
		args := append([]string{in, "-o", out}, extra...)
		err := runRender(t, args...)
		if err == nil {
			t.Fatalf("args %v: expected directory refusal, got nil", args)
		}
		if !strings.Contains(err.Error(), "is a directory") {
			t.Errorf("args %v: error = %q, want a directory refusal", args, err)
		}
		var cliErr *exitcodes.CLIError
		if !errors.As(err, &cliErr) {
			t.Fatalf("args %v: expected *exitcodes.CLIError, got %T", args, err)
		}
		if cliErr.Code != exitcodes.ExitConflict {
			t.Errorf("args %v: exit code = %v, want %v", args, cliErr.Code, exitcodes.ExitConflict)
		}
	}
}
