package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/press"
)

// reportMissingTitleDoc is valid frontmatter for the report profile except
// that the required "title" is missing: Validate() yields exactly one
// error-severity issue.
const reportMissingTitleDoc = "---\nprofile: report\n---\n\n# Hello\n"

// TestValidateCmd_MissingRequiredField covers the error branch of the validate
// command in text mode: it exits non-zero with ExitData/KindValidation and
// prints the error-severity issue to stdout.
func TestValidateCmd_MissingRequiredField(t *testing.T) {
	resetGlobalState(t)
	input := renderInput(t, reportMissingTitleDoc)

	cmd := newValidateCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{input})
	var execErr error
	stdout := captureStdout(t, func() {
		execErr = cmd.ExecuteContext(context.Background())
	})
	if execErr == nil {
		t.Fatal("expected a validation error for the missing required title, got nil")
	}
	if code := exitcodes.ExitCodeFromError(execErr); code != exitcodes.ExitData {
		t.Errorf("exit code = %v, want %v", code, exitcodes.ExitData)
	}
	var cliErr *exitcodes.CLIError
	if !errors.As(execErr, &cliErr) {
		t.Fatalf("expected *exitcodes.CLIError, got %T", execErr)
	}
	if cliErr.Kind != exitcodes.KindValidation {
		t.Errorf("kind = %v, want %v", cliErr.Kind, exitcodes.KindValidation)
	}
	if !strings.Contains(stdout, "✗") {
		t.Errorf("stdout = %q, want the error glyph for the error issue", stdout)
	}
	if !strings.Contains(stdout, `requires "title"`) {
		t.Errorf("stdout = %q, want the missing-title issue", stdout)
	}
}

// TestValidateCmd_Text_ValidDocument covers the success side of the exit-code
// boundary: a document with no error issues exits 0 with the success line.
func TestValidateCmd_Text_ValidDocument(t *testing.T) {
	resetGlobalState(t)
	input := renderInput(t, overwriteTestDoc)

	cmd := newValidateCmd()
	cmd.SetArgs([]string{input})
	var execErr error
	stdout := captureStdout(t, func() {
		execErr = cmd.ExecuteContext(context.Background())
	})
	if execErr != nil {
		t.Fatalf("validate failed: %v", execErr)
	}
	if !strings.Contains(stdout, `✓ valid for profile "report"`) {
		t.Errorf("stdout = %q, want the success line", stdout)
	}
}

// assertValidateJSON checks the validate command's --json payload contract:
// valid JSON with profile, ok, issues and warnings fields, exactly nIssues
// entries, each with severity/field/message set. An issue-free document
// serializes issues as JSON null (doc.Validate returns a nil slice that the
// command passes through unchanged), which the helper treats as zero issues.
func assertValidateJSON(t *testing.T, stdout, wantProfile string, wantOK bool, nIssues int) {
	t.Helper()
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not valid JSON: %q", stdout)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("unmarshal stdout: %v", err)
	}
	if got["profile"] != wantProfile {
		t.Errorf("profile = %v, want %q", got["profile"], wantProfile)
	}
	if got["ok"] != wantOK {
		t.Errorf("ok = %v, want %v", got["ok"], wantOK)
	}
	issues, hasIssues := got["issues"].([]any)
	if nIssues == 0 {
		if hasIssues && len(issues) != 0 {
			t.Errorf("issues = %v, want no entries", got["issues"])
		}
	} else {
		if !hasIssues || len(issues) != nIssues {
			t.Fatalf("issues = %v, want %d entries", got["issues"], nIssues)
		}
		for i, raw := range issues {
			is, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("issues[%d] is not an object: %v", i, raw)
			}
			if sev, _ := is["severity"].(string); sev != "error" {
				t.Errorf("issues[%d].severity = %q, want %q", i, sev, "error")
			}
			if is["field"] == "" || is["message"] == "" {
				t.Errorf("issues[%d] = %v, want field and message set", i, is)
			}
		}
	}
	if _, ok := got["warnings"]; !ok {
		t.Errorf("payload missing the warnings field: %v", got)
	}
}

// TestValidateCmd_JSON_ErrorIssues runs the real command tree with the
// persistent --json flag on a document with one error issue. In --json mode
// validate.go deliberately exits 0 (so stdout carries nothing but the clean
// JSON payload) and conveys the failure through "ok": false; the text mode
// counterpart exits ExitData (see TestValidateCmd_MissingRequiredField).
func TestValidateCmd_JSON_ErrorIssues(t *testing.T) {
	resetGlobalState(t)
	input := renderInput(t, reportMissingTitleDoc)

	root := newRootCmd()
	root.SetArgs([]string{"validate", "--json", input})
	var execErr error
	stdout := captureStdout(t, func() {
		execErr = root.ExecuteContext(context.Background())
	})
	if execErr != nil {
		t.Fatalf("validate --json returned an error: %v", execErr)
	}
	if code := exitcodes.ExitCodeFromError(execErr); code != exitcodes.ExitOK {
		t.Errorf("exit code = %v, want %v in --json mode", code, exitcodes.ExitOK)
	}
	assertValidateJSON(t, stdout, "report", false, 1)
}

// TestValidateCmd_JSON_MultipleErrorIssues verifies that several issues are
// serialized in one payload: behoerde requires recipient, title and lang, and
// additionally demands PDF/UA accessibility metadata (lang + title), so a
// document missing all of it yields five error-severity issues.
func TestValidateCmd_JSON_MultipleErrorIssues(t *testing.T) {
	resetGlobalState(t)
	input := renderInput(t, "---\nprofile: behoerde\n---\n\n# Hello\n")

	root := newRootCmd()
	root.SetArgs([]string{"validate", "--json", input})
	var execErr error
	stdout := captureStdout(t, func() {
		execErr = root.ExecuteContext(context.Background())
	})
	if execErr != nil {
		t.Fatalf("validate --json returned an error: %v", execErr)
	}
	assertValidateJSON(t, stdout, "behoerde", false, 5)
}

// TestValidateCmd_JSON_ValidDocument covers the --json success payload: ok
// must be true with no issues and a warnings field present.
func TestValidateCmd_JSON_ValidDocument(t *testing.T) {
	resetGlobalState(t)
	input := renderInput(t, overwriteTestDoc)

	root := newRootCmd()
	root.SetArgs([]string{"validate", "--json", input})
	var execErr error
	stdout := captureStdout(t, func() {
		execErr = root.ExecuteContext(context.Background())
	})
	if execErr != nil {
		t.Fatalf("validate --json failed: %v", execErr)
	}
	assertValidateJSON(t, stdout, "report", true, 0)
}

// TestValidateCmd_UnknownProfile covers the unknown-profile branch of the
// validate command: the error maps to ExitNoInput/KindNotFound (the resource
// does not exist) rather than a validation error.
func TestValidateCmd_UnknownProfile(t *testing.T) {
	resetGlobalState(t)
	input := renderInput(t, overwriteTestDoc)

	cmd := newValidateCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{input, "--profile", "nope"})
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected an error for an unknown profile, got nil")
	}
	if code := exitcodes.ExitCodeFromError(err); code != exitcodes.ExitNoInput {
		t.Errorf("exit code = %v, want %v", code, exitcodes.ExitNoInput)
	}
	var cliErr *exitcodes.CLIError
	if !errors.As(err, &cliErr) {
		t.Fatalf("expected *exitcodes.CLIError, got %T", err)
	}
	if cliErr.Kind != exitcodes.KindNotFound {
		t.Errorf("kind = %v, want %v", cliErr.Kind, exitcodes.KindNotFound)
	}
	if !strings.Contains(err.Error(), `unknown profile "nope"`) {
		t.Errorf("error = %q, want the unknown-profile message", err)
	}
}

// TestValidateCmd_JSONMixedSeverityShape asserts that the JSON payload the
// validate command builds serializes error AND warning severities faithfully.
// press.Validate currently emits error-severity issues only, so no real
// document can produce a warning through the command; the mixed list is
// therefore asserted against the exact payload construction validate.go uses
// (map[string]any{"profile", "issues", "ok"}) — the same shape the MCP
// validate_document tool returns to protocol clients.
func TestValidateCmd_JSONMixedSeverityShape(t *testing.T) {
	resetGlobalState(t)
	issues := []press.Issue{
		{Severity: "warning", Field: "lang", Message: "PDF/UA recommends a document language"},
		{Severity: "error", Field: "title", Message: `profile "report" requires "title"`},
	}
	out := captureStdout(t, func() {
		if err := printJSON(map[string]any{"profile": "report", "issues": issues, "ok": !anyError(issues)}); err != nil {
			t.Fatalf("printJSON() error = %v", err)
		}
	})
	var got struct {
		Profile string        `json:"profile"`
		OK      bool          `json:"ok"`
		Issues  []press.Issue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if got.Profile != "report" {
		t.Errorf("profile = %q, want %q", got.Profile, "report")
	}
	if got.OK {
		t.Error("ok = true, want false (the mixed list contains an error)")
	}
	if len(got.Issues) != 2 {
		t.Fatalf("issues = %v, want 2 entries", got.Issues)
	}
	if got.Issues[0].Severity != "warning" || got.Issues[1].Severity != "error" {
		t.Errorf("severities = [%q %q], want [warning error]", got.Issues[0].Severity, got.Issues[1].Severity)
	}
}
