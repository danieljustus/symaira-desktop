package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withFakeSymmemory puts a fake `symmemory` executable script on PATH for
// the duration of the test, mirroring the harness used by
// internal/integration/symmemory's own tests: the fake binary shadows any
// real installation via exec.LookPath while the rest of the real PATH
// stays reachable for the script's own commands. script is a POSIX shell
// script body (the shebang is added here).
func withFakeSymmemory(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shell-script binary not supported on windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "symmemory")
	content := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("failed to write fake symmemory binary: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestMemoryCandidates_HappyPath drives `symrelate memory candidates`
// against a fake symmemory binary and asserts the JSON shape published by
// the CLI contract: snake_case candidate keys under {"available": true,
// "candidates": [...]} on stdout (see docs/CLI_CONTRACT.md).
func TestMemoryCandidates_HappyPath(t *testing.T) {
	withFakeSymmemory(t, `echo '[{"entity_id":"mem-e1","name":"Alice Lovelace","type":"person","aliases":["Ali"],"score":1.0,"match_kind":"exact_name","match_reason":"exact name match"}]'`)

	out, stderr, code := runCLI(t, "memory", "candidates", "--query", "Alice")
	if code != 0 {
		t.Fatalf("memory candidates: code=%d stderr=%s", code, stderr)
	}
	var res struct {
		Available  bool `json:"available"`
		Candidates []struct {
			EntityID    string   `json:"entity_id"`
			Name        string   `json:"name"`
			Type        string   `json:"type"`
			Aliases     []string `json:"aliases"`
			Score       float64  `json:"score"`
			MatchKind   string   `json:"match_kind"`
			MatchReason string   `json:"match_reason"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("memory candidates output not JSON: %v (%s)", err, out)
	}
	if !res.Available {
		t.Errorf("memory candidates available = false, want true: %s", out)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("memory candidates returned %d candidates, want 1: %s", len(res.Candidates), out)
	}
	c := res.Candidates[0]
	if c.EntityID != "mem-e1" || c.Name != "Alice Lovelace" || c.Type != "person" || c.Score != 1.0 || c.MatchKind != "exact_name" || len(c.Aliases) != 1 || c.Aliases[0] != "Ali" {
		t.Errorf("memory candidates[0] = %+v, want exact_name match for Alice Lovelace", c)
	}
}

func TestMemoryCandidates_MissingQuery_Error(t *testing.T) {
	_, stderr, code := runCLI(t, "memory", "candidates")
	if code == 0 {
		t.Fatal("memory candidates without --query succeeded, want error")
	}
	if !strings.Contains(stderr, "--query is required") {
		t.Errorf("memory candidates stderr = %q, want --query is required", stderr)
	}
}

// TestMemoryRelate_HappyPath links a local contact to a SymMemory entity,
// then creates a relation through a fake symmemory binary, asserting the
// snake_case relation JSON shape of the CLI contract on stdout.
func TestMemoryRelate_HappyPath(t *testing.T) {
	setTestProfileDirs(t)

	out, stderr, code := runCLI(t, "contact", "add", "--name", "Ada Lovelace")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}
	var person map[string]any
	if err := json.Unmarshal([]byte(out), &person); err != nil {
		t.Fatalf("contact add: invalid JSON: %v (%s)", err, out)
	}
	personID, _ := person["ID"].(string)
	if personID == "" {
		t.Fatalf("contact add: missing ID in %s", out)
	}

	if _, stderr, code = runCLI(t, "memory", "link", "--person", personID, "--entity", "mem-source"); code != 0 {
		t.Fatalf("memory link: code=%d stderr=%s", code, stderr)
	}

	withFakeSymmemory(t, `echo '{"id":"rel-1","from_entity_id":"mem-source","to_entity_id":"mem-target","relation_type":"attended","source":"symrelate","source_ref":"`+personID+`","verification":"verified","created_at":"2026-01-15T10:00:00Z","updated_at":"2026-01-15T10:00:00Z"}'`)

	out, stderr, code = runCLI(t, "memory", "relate", "--person", personID, "--relation", "attended", "--to-id", "mem-target")
	if code != 0 {
		t.Fatalf("memory relate: code=%d stderr=%s", code, stderr)
	}
	var rel struct {
		ID           string `json:"id"`
		FromEntityID string `json:"from_entity_id"`
		ToEntityID   string `json:"to_entity_id"`
		RelationType string `json:"relation_type"`
		Source       string `json:"source"`
		SourceRef    string `json:"source_ref"`
		Verification string `json:"verification"`
	}
	if err := json.Unmarshal([]byte(out), &rel); err != nil {
		t.Fatalf("memory relate output not JSON: %v (%s)", err, out)
	}
	if rel.ID != "rel-1" || rel.FromEntityID != "mem-source" || rel.ToEntityID != "mem-target" || rel.RelationType != "attended" || rel.Source != "symrelate" || rel.SourceRef != personID || rel.Verification != "verified" {
		t.Errorf("memory relate payload = %+v", rel)
	}
}

func TestMemoryRelate_NoLink_Error(t *testing.T) {
	setTestProfileDirs(t)

	out, stderr, code := runCLI(t, "contact", "add", "--name", "Grace Hopper")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}
	var person map[string]any
	if err := json.Unmarshal([]byte(out), &person); err != nil {
		t.Fatalf("contact add: invalid JSON: %v (%s)", err, out)
	}
	personID, _ := person["ID"].(string)

	_, stderr, code = runCLI(t, "memory", "relate", "--person", personID, "--relation", "attended", "--to-id", "mem-target")
	if code == 0 {
		t.Fatal("memory relate on unlinked contact succeeded, want error")
	}
	if !strings.Contains(stderr, "contact has no SymMemory link") {
		t.Errorf("memory relate stderr = %q, want no-link error", stderr)
	}
}

// TestMemoryUnrelate_HappyPath removes a relation by stable id through a
// fake symmemory binary and asserts the returned relation JSON shape.
func TestMemoryUnrelate_HappyPath(t *testing.T) {
	withFakeSymmemory(t, `echo '{"id":"rel-1","from_entity_id":"mem-source","to_entity_id":"mem-target","relation_type":"attended","source":"symrelate","source_ref":"p-1","verification":"verified","created_at":"2026-01-15T10:00:00Z","updated_at":"2026-01-15T10:00:00Z"}'`)

	out, stderr, code := runCLI(t, "memory", "unrelate", "--relation-id", "rel-1")
	if code != 0 {
		t.Fatalf("memory unrelate: code=%d stderr=%s", code, stderr)
	}
	var rel struct {
		ID           string `json:"id"`
		RelationType string `json:"relation_type"`
	}
	if err := json.Unmarshal([]byte(out), &rel); err != nil {
		t.Fatalf("memory unrelate output not JSON: %v (%s)", err, out)
	}
	if rel.ID != "rel-1" || rel.RelationType != "attended" {
		t.Errorf("memory unrelate payload = %+v", rel)
	}
}

func TestMemoryUnrelate_MissingRelationID_Error(t *testing.T) {
	_, stderr, code := runCLI(t, "memory", "unrelate")
	if code == 0 {
		t.Fatal("memory unrelate without --relation-id succeeded, want error")
	}
	if !strings.Contains(stderr, "--relation-id is required") {
		t.Errorf("memory unrelate stderr = %q, want --relation-id is required", stderr)
	}
}

// TestMemoryShow_HappyPath covers the rawJSON embedding path (the context
// blob is passed through as an unquoted JSON value), exercising
// rawJSON.MarshalJSON.
func TestMemoryShow_HappyPath(t *testing.T) {
	setTestProfileDirs(t)

	out, stderr, code := runCLI(t, "contact", "add", "--name", "Katherine Johnson")
	if code != 0 {
		t.Fatalf("contact add: code=%d stderr=%s", code, stderr)
	}
	var person map[string]any
	if err := json.Unmarshal([]byte(out), &person); err != nil {
		t.Fatalf("contact add: invalid JSON: %v (%s)", err, out)
	}
	personID, _ := person["ID"].(string)
	if personID == "" {
		t.Fatalf("contact add: missing ID in %s", out)
	}
	if _, stderr, code = runCLI(t, "memory", "link", "--person", personID, "--entity", "mem-source"); code != 0 {
		t.Fatalf("memory link: code=%d stderr=%s", code, stderr)
	}

	withFakeSymmemory(t, `echo '{"id":"mem-source","name":"Ada Lovelace","type":"person"}'`)

	out, stderr, code = runCLI(t, "memory", "show", "--person", personID)
	if code != 0 {
		t.Fatalf("memory show: code=%d stderr=%s", code, stderr)
	}
	var res struct {
		Linked       bool            `json:"linked"`
		ContextAvail bool            `json:"context_available"`
		Context      json.RawMessage `json:"context"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("memory show output not JSON: %v (%s)", err, out)
	}
	if !res.Linked || !res.ContextAvail {
		t.Errorf("memory show = %s, want linked with context available", out)
	}
	var ctx map[string]any
	if err := json.Unmarshal(res.Context, &ctx); err != nil {
		t.Fatalf("memory show context is not embedded JSON: %v (%s)", err, out)
	}
	if ctx["id"] != "mem-source" || ctx["name"] != "Ada Lovelace" {
		t.Errorf("memory show context = %v, want mem-source entity", ctx)
	}
}
