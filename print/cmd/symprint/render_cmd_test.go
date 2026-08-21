package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/press"
)

// renderInput writes a Markdown document into a temp dir and returns its path.
func renderInput(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "doc.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(input) error = %v", err)
	}
	return path
}

// TestRenderCmd_MissingInputFile covers the os.ReadFile error branch of the
// render command: the error must map to ExitNoInput/KindNotFound and the
// stderr output must stay a plain, formatted CLI hint — never a raw panic —
// with nothing written to stdout in non-JSON mode.
func TestRenderCmd_MissingInputFile(t *testing.T) {
	resetGlobalState(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.md")

	cmd := newRenderCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{missing})

	var execErr error
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			execErr = cmd.ExecuteContext(context.Background())
		})
		// Mirror main(): the process prints the formatted CLI error to stderr.
		if execErr != nil {
			fmt.Fprintln(os.Stderr, "Error:", exitcodes.FormatCLIError(execErr))
		}
	})
	if execErr == nil {
		t.Fatal("expected an error for a missing input file, got nil")
	}
	if code := exitcodes.ExitCodeFromError(execErr); code != exitcodes.ExitNoInput {
		t.Errorf("exit code = %v, want %v", code, exitcodes.ExitNoInput)
	}
	var cliErr *exitcodes.CLIError
	if !errors.As(execErr, &cliErr) {
		t.Fatalf("expected *exitcodes.CLIError, got %T", execErr)
	}
	if cliErr.Kind != exitcodes.KindNotFound {
		t.Errorf("kind = %v, want %v", cliErr.Kind, exitcodes.KindNotFound)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no output in non-JSON mode", stdout)
	}
	if !strings.Contains(stderr, "read input") {
		t.Errorf("stderr = %q, want a plain 'read input' hint", stderr)
	}
	if strings.Contains(stderr, "panic") {
		t.Errorf("stderr = %q, must not contain a panic", stderr)
	}
}

// TestRenderCmd_UnknownProfile covers the contract-stage unknown-profile
// error of press.Render, both via frontmatter and via the --profile flag. The
// error must map to ExitData/KindValidation with the profiles hint, and the
// failure must happen before any engine work.
func TestRenderCmd_UnknownProfile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		args    []string
	}{
		{"frontmatter profile unknown", "---\nprofile: nope\ntitle: Test\n---\n\n# Hello\n", nil},
		{"--profile flag unknown", overwriteTestDoc, []string{"--profile", "nope"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetGlobalState(t)
			input := renderInput(t, tt.content)

			cmd := newRenderCmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs(append([]string{input}, tt.args...))
			err := cmd.ExecuteContext(context.Background())
			if err == nil {
				t.Fatal("expected an error for an unknown profile, got nil")
			}
			if code := exitcodes.ExitCodeFromError(err); code != exitcodes.ExitData {
				t.Errorf("exit code = %v, want %v", code, exitcodes.ExitData)
			}
			var cliErr *exitcodes.CLIError
			if !errors.As(err, &cliErr) {
				t.Fatalf("expected *exitcodes.CLIError, got %T", err)
			}
			if cliErr.Kind != exitcodes.KindValidation {
				t.Errorf("kind = %v, want %v", cliErr.Kind, exitcodes.KindValidation)
			}
			formatted := exitcodes.FormatCLIError(err)
			if !strings.Contains(formatted, "unknown profile: nope") {
				t.Errorf("error = %q, want it to name the unknown profile", formatted)
			}
			if !strings.Contains(formatted, "Hint: run `symprint profiles`") {
				t.Errorf("error = %q, want the profiles hint", formatted)
			}
		})
	}
}

// TestRenderCmd_MissingProfile covers a document whose frontmatter selects no
// profile. render.go falls back to the config default profile ("report");
// because this document also lacks the title the report contract requires,
// the render fails deterministically at the contract stage — before any
// engine work — with a *press.ContractError mapped to ExitData/KindValidation.
func TestRenderCmd_MissingProfileFallsBackToConfigDefault(t *testing.T) {
	resetGlobalState(t)
	input := renderInput(t, "# Hello\n")

	cmd := newRenderCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{input})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected a contract error for the missing required title, got nil")
	}
	if code := exitcodes.ExitCodeFromError(err); code != exitcodes.ExitData {
		t.Errorf("exit code = %v, want %v", code, exitcodes.ExitData)
	}
	var cliErr *exitcodes.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *exitcodes.CLIError, got %T", err)
	}
	if cliErr.Kind != exitcodes.KindValidation {
		t.Errorf("kind = %v, want %v", cliErr.Kind, exitcodes.KindValidation)
	}
	// The cause must be the typed contract error, not a RenderError: this
	// proves the failure happened before typst was invoked.
	var ce *press.ContractError
	if !errors.As(err, &ce) {
		t.Fatalf("expected the cause chain to contain *press.ContractError, got %T", err)
	}
	formatted := exitcodes.FormatCLIError(err)
	if !strings.Contains(formatted, "frontmatter issue(s)") {
		t.Errorf("error = %q, want the contract issue summary", formatted)
	}
	if !strings.Contains(formatted, `profile "report" requires "title"`) {
		t.Errorf("error = %q, want the missing-title contract issue", formatted)
	}
}

// TestRenderCmd_JSONErrorCase_StdoutClean executes the real command tree with
// the persistent --json flag on an error path: the flag must bind to jsonOut,
// the error must map to ExitNoInput, and stdout must stay completely clean —
// no partial JSON, no text pollution — because the error is returned before
// printJSON is reached.
func TestRenderCmd_JSONErrorCase_StdoutClean(t *testing.T) {
	resetGlobalState(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist.md")

	root := newRootCmd()
	root.SetArgs([]string{"render", "--json", missing})
	var execErr error
	stdout := captureStdout(t, func() {
		execErr = root.ExecuteContext(context.Background())
	})
	if execErr == nil {
		t.Fatal("expected an error for a missing input file, got nil")
	}
	if !jsonOut {
		t.Error("--json flag did not bind to the jsonOut package state")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want no output (JSON or otherwise) on an error path", stdout)
	}
	if code := exitcodes.ExitCodeFromError(execErr); code != exitcodes.ExitNoInput {
		t.Errorf("exit code = %v, want %v", code, exitcodes.ExitNoInput)
	}
}

// TestRenderCmd_JSONSuccess_WarningsShape runs a full successful --json render
// (fake typst shim, broken host config): printJSON must emit valid JSON that
// carries the render result AND the config-ignored warning inside the
// warnings array, with the warning kept off stderr.
func TestRenderCmd_JSONSuccess_WarningsShape(t *testing.T) {
	home := resetGlobalState(t)
	configDir := filepath.Join(home, ".config", "symprint")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(config dir) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("not = [valid toml"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	fakeTypst(t)
	input := renderInput(t, overwriteTestDoc)
	out := filepath.Join(t.TempDir(), "out.pdf")

	root := newRootCmd()
	root.SetArgs([]string{"render", "--json", input, "-o", out})
	var execErr error
	var stdout, stderr string
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			execErr = root.ExecuteContext(context.Background())
		})
	})
	if execErr != nil {
		t.Fatalf("render --json failed: %v", execErr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not valid JSON: %q", stdout)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v", err)
	}
	if got["output_path"] != out {
		t.Errorf("output_path = %v, want %q", got["output_path"], out)
	}
	if got["profile"] != "report" {
		t.Errorf("profile = %v, want %q", got["profile"], "report")
	}
	warnings, ok := got["warnings"].([]any)
	if !ok || len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one config warning in the JSON payload", got["warnings"])
	}
	if !strings.Contains(warnings[0].(string), "warning: config ignored") {
		t.Errorf("warnings[0] = %v, want the config-ignored warning", warnings[0])
	}
	if strings.Contains(stderr, "config ignored") {
		t.Errorf("config warning leaked onto stderr in --json mode: %q", stderr)
	}
}
