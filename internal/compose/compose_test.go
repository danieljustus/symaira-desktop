package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMockTool(t *testing.T, dir, name, script string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil { //nolint:gosec // G306: test fixture must be executable.
		t.Fatal(err)
	}
}

func withMockPath(t *testing.T, dir string) {
	t.Helper()
	old := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+old)
	// Isolate $HOME and $SYMAIRA_BIN so a real managed-runtime directory on
	// the machine running this test (~/.symaira/bin) can never shadow the
	// PATH-based mock tool these tests set up.
	t.Setenv("HOME", t.TempDir())
	t.Setenv(SymairaBinEnvVar, "")
	ResetCache()
	t.Cleanup(ResetCache)
}

func TestHasToolCachesResult(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	writeMockTool(t, dir, "symseek", `#!/bin/bash
echo "call" >> `+calls+`
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"tool":"symseek","version":"1.2.3","schema_version":1}'
fi
`)
	withMockPath(t, dir)

	ok, ver := HasTool("symseek")
	if !ok || ver != "1.2.3" {
		t.Fatalf("expected available=true version=1.2.3, got %v/%s", ok, ver)
	}

	// Second call must hit the cache, not invoke the binary again.
	ok2, ver2 := HasTool("symseek")
	if !ok2 || ver2 != "1.2.3" {
		t.Fatalf("expected cached result available=true version=1.2.3, got %v/%s", ok2, ver2)
	}

	data, err := os.ReadFile(calls) //nolint:gosec // G304: calls is a test fixture under t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	got := len(strings.Fields(strings.TrimSpace(string(data))))
	if got != 1 {
		t.Errorf("expected exactly 1 invocation due to caching, got %d", got)
	}
}

func TestHasToolNotFound(t *testing.T) {
	dir := t.TempDir()
	withMockPath(t, dir)

	ok, ver := HasTool("symdoesnotexist")
	if ok {
		t.Fatalf("expected available=false for missing binary")
	}
	if ver != "not_found" {
		t.Errorf("expected version 'not_found', got %q", ver)
	}
}

func TestHasToolRunFailureReportsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symseek", `#!/bin/bash
exit 1
`)
	withMockPath(t, dir)

	ok, ver := HasTool("symseek")
	if !ok {
		t.Fatalf("expected available=true even when the version probe exits non-zero")
	}
	if ver != "unknown" {
		t.Errorf("expected version 'unknown' on probe failure, got %q", ver)
	}
}

func TestHasToolMalformedJSONReportsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symseek", `#!/bin/bash
echo 'not json'
`)
	withMockPath(t, dir)

	ok, ver := HasTool("symseek")
	if !ok {
		t.Fatalf("expected available=true")
	}
	if ver != "unknown" {
		t.Errorf("expected version 'unknown' on malformed JSON, got %q", ver)
	}
}

func TestHasSymmemoryShorthand(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
echo '{"tool":"symmemory","version":"8.8.8","schema_version":1}'
`)
	withMockPath(t, dir)

	if ok, ver := HasSymmemory(); !ok || ver != "8.8.8" {
		t.Errorf("HasSymmemory: expected true/8.8.8, got %v/%s", ok, ver)
	}
}

func TestListEntitiesSuccessAndErrors(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  if [ "$SYMMEMORY_MODE" = "fail" ]; then
    echo "entity-list-boom" >&2
    exit 1
  fi
  if [ "$SYMMEMORY_MODE" = "badjson" ]; then
    echo "not json"
    exit 0
  fi
  echo '[{"id":"e1","name":"Mock","type":"project","aliases":[],"description":"d"}]'
fi
`)
	withMockPath(t, dir)

	entities, err := ListEntities()
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(entities) != 1 || entities[0].Name != "Mock" {
		t.Errorf("unexpected entities: %+v", entities)
	}

	t.Setenv("SYMMEMORY_MODE", "fail")
	if _, err := ListEntities(); err == nil || !strings.Contains(err.Error(), "symmemory entity list failed") {
		t.Errorf("expected wrapped command failure, got %v", err)
	}

	t.Setenv("SYMMEMORY_MODE", "badjson")
	if _, err := ListEntities(); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("expected wrapped unmarshal failure, got %v", err)
	}
	t.Setenv("SYMMEMORY_MODE", "")
}

func TestGetNeighborsSuccessAndErrors(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "neighbors" ]; then
  if [ "$SYMMEMORY_MODE" = "fail" ]; then
    echo "neighbors-boom" >&2
    exit 1
  fi
  if [ "$SYMMEMORY_MODE" = "badjson" ]; then
    echo "not json"
    exit 0
  fi
  echo '{"nodes":[{"id":"e1","name":"Mock","type":"project"}],"edges":[{"from_entity_id":"e2","to_entity_id":"e1","relation_type":"knows"}]}'
fi
`)
	withMockPath(t, dir)

	neighbors, err := GetNeighbors("Mock")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(neighbors.Nodes) != 1 || len(neighbors.Edges) != 1 {
		t.Errorf("unexpected neighbors: %+v", neighbors)
	}

	t.Setenv("SYMMEMORY_MODE", "fail")
	if _, err := GetNeighbors("Mock"); err == nil || !strings.Contains(err.Error(), "symmemory neighbors failed") {
		t.Errorf("expected wrapped command failure, got %v", err)
	}

	t.Setenv("SYMMEMORY_MODE", "badjson")
	if _, err := GetNeighbors("Mock"); err == nil || !strings.Contains(err.Error(), "unmarshal") {
		t.Errorf("expected wrapped unmarshal failure, got %v", err)
	}
	t.Setenv("SYMMEMORY_MODE", "")
}
