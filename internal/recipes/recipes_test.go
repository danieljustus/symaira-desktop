package recipes

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidateRecipe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daily.yml")
	if err := os.WriteFile(path, []byte("version: 1\nname: daily\ntriggers: [manual, save]\ntools: [desk_search]\nwrite_cap: 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "daily" || r.WriteCap != 2 {
		t.Fatalf("unexpected recipe: %#v", r)
	}
}

func TestValidateRejectsUnsafeRecipe(t *testing.T) {
	if err := Validate(Recipe{Version: 1, Name: "x", Triggers: []string{"magic"}}); err == nil {
		t.Fatal("expected unsupported trigger error")
	}
	if err := Validate(Recipe{Version: 1, Name: "x", Triggers: []string{"manual"}, WriteCap: -1}); err == nil {
		t.Fatal("expected write cap error")
	}
	if err := Validate(Recipe{Version: 1, Name: "x", Triggers: []string{"manual"}}); err == nil {
		t.Fatal("expected allow-list error")
	}
}

func TestAcceptAndRejectKeepChangesPendingUntilApproved(t *testing.T) {
	root := t.TempDir()
	m := Manifest{Request: Request{RunID: "run-1", Recipe: Recipe{Version: 1, Name: "x", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 1}}, Response: Response{ContractVersion: 1, Changes: []Change{{Path: "notes/a.md", Content: "approved"}}}, Status: "pending"}
	dir := runDir(root, "run-1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	if err := Accept(root, "run-1"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "notes", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "approved" {
		t.Fatalf("got %q", b)
	}
	if err := Accept(root, "run-1"); err == nil {
		t.Fatal("accepted run must not be applied twice")
	}

	m.Request.RunID, m.Status = "run-2", "pending"
	dir = runDir(root, "run-2")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	if err := Reject(root, "run-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "notes", "a.md")); err != nil {
		t.Fatal(err)
	}
}

func TestChangeValidationEnforcesCapAndVaultBoundary(t *testing.T) {
	root := t.TempDir()
	if err := validateChanges(root, 1, []Change{{Path: "a.md"}, {Path: "b.md"}}); err == nil {
		t.Fatal("expected cap error")
	}
	if err := validateChanges(root, 1, []Change{{Path: "../outside.md"}}); err == nil {
		t.Fatal("expected path error")
	}
}

func writeFakeSymvibe(t *testing.T, responseJSON string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "symvibe")
	content := `#!/bin/sh
REQUEST="" ; RESPONSE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --request) REQUEST="$2" ; shift 2 ;;
    --response) RESPONSE="$2" ; shift 2 ;;
    *) shift ;;
  esac
done
printf '%s' '` + responseJSON + `' > "$RESPONSE"
`
	if err := os.WriteFile(script, []byte(content), 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestStartRunnerMissing(t *testing.T) {
	root := t.TempDir()
	// Ensure symvibe is NOT on PATH
	t.Setenv("PATH", t.TempDir())
	recipe := Recipe{Version: 1, Name: "test", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 1}
	_, err := Start(context.Background(), root, recipe, "manual")
	if err == nil {
		t.Fatal("expected error when symvibe is missing")
	}
}

func TestStartTriggerNotInAllowList(t *testing.T) {
	root := t.TempDir()
	recipe := Recipe{Version: 1, Name: "test", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 1}
	_, err := Start(context.Background(), root, recipe, "schedule")
	if err == nil {
		t.Fatal("expected error for trigger not in recipe allow-list")
	}
}

func TestStartHappyPath(t *testing.T) {
	root := t.TempDir()
	binDir := writeFakeSymvibe(t, `{"contract_version":1,"trace":["running"],"changes":[{"path":"notes/hello.md","content":"hello"}]}`)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	recipe := Recipe{Version: 1, Name: "test-recipe", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 2}
	m, err := Start(context.Background(), root, recipe, "manual")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Status != "pending" {
		t.Fatalf("expected status pending, got %q", m.Status)
	}
	if m.Request.Recipe.Name != "test-recipe" {
		t.Fatalf("expected recipe name test-recipe, got %q", m.Request.Recipe.Name)
	}
	if len(m.Response.Changes) != 1 || m.Response.Changes[0].Path != "notes/hello.md" {
		t.Fatalf("unexpected changes: %+v", m.Response.Changes)
	}
	// Verify manifest.json was written
	runID := m.Request.RunID
	manifestPath := filepath.Join(runDir(root, runID), "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("manifest.json not created: %v", err)
	}
	// Verify trace.md was written
	tracePath := filepath.Join(runDir(root, runID), "trace.md")
	b, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatalf("trace.md not created: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("trace.md is empty")
	}
}

func TestStartContractVersionMismatch(t *testing.T) {
	root := t.TempDir()
	binDir := writeFakeSymvibe(t, `{"contract_version":999,"trace":[],"changes":[]}`)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	recipe := Recipe{Version: 1, Name: "test", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 1}
	_, err := Start(context.Background(), root, recipe, "manual")
	if err == nil {
		t.Fatal("expected error for contract version mismatch")
	}
}

func TestStartExceedsWriteCap(t *testing.T) {
	root := t.TempDir()
	binDir := writeFakeSymvibe(t, `{"contract_version":1,"trace":[],"changes":[{"path":"a.md","content":"a"},{"path":"b.md","content":"b"}]}`)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	recipe := Recipe{Version: 1, Name: "test", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 1}
	_, err := Start(context.Background(), root, recipe, "manual")
	if err == nil {
		t.Fatal("expected error when changes exceed write_cap")
	}
}

func TestPendingDiff(t *testing.T) {
	root := t.TempDir()
	runID := "test-run"
	dir := runDir(root, runID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	m := Manifest{
		Request:  Request{RunID: runID, Recipe: Recipe{Version: 1, Name: "x", Triggers: []string{"manual"}, Tools: []string{"desk_search"}, WriteCap: 1}},
		Response: Response{ContractVersion: 1, Changes: []Change{{Path: "a.md", Content: "content-a"}, {Path: "b.md", Content: "content-b"}}},
		Status:   "pending",
	}
	if err := writeJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
	changes, err := PendingDiff(root, runID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(changes))
	}
	if changes[0].Path != "a.md" || changes[1].Path != "b.md" {
		t.Fatalf("unexpected paths: %+v", changes)
	}
}

func TestSafeName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"hello", "hello"},
		{"Hello World", "Hello-World"},
		{"recipe-v2.1", "recipe-v2-1"},
		{"__special__", "--special--"},
		{"", ""},
	}
	for _, tt := range tests {
		got := safeName(tt.input)
		if got != tt.want {
			t.Errorf("safeName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWriteTrace(t *testing.T) {
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "trace.md")
	m := Manifest{
		Status:   "pending",
		Response: Response{Trace: []string{"line 1", "line 2"}, Changes: []Change{{Path: "b.md"}, {Path: "a.md"}}},
	}
	if err := writeTrace(tracePath, m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(b)
	// Verify key sections exist
	for _, substr := range []string{"# Recipe run", "Status: pending", "## Trace", "line 1", "line 2", "## Proposed changes", "`a.md`", "`b.md`"} {
		if !containsStr(content, substr) {
			t.Errorf("trace.md missing %q", substr)
		}
	}
	// Verify changes are sorted
	idxA := indexOf(content, "`a.md`")
	idxB := indexOf(content, "`b.md`")
	if idxA >= idxB {
		t.Error("expected a.md before b.md in sorted trace output")
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsSubstr(s, sub))
}

func containsSubstr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
