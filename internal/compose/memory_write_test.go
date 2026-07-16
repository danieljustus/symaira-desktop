package compose

import (
	"strings"
	"testing"
)

func TestResolveCandidatesExactAndAliasMatches(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"e1","name":"Alice Example","type":"person","aliases":["Ali"],"description":""},{"id":"e2","name":"Bob","type":"person","aliases":["Alice Example"],"description":""}]'
fi
`)
	withMockPath(t, dir)

	candidates, err := ResolveCandidates("Alice Example")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("expected 2 candidates (exact + alias), got %d: %+v", len(candidates), candidates)
	}
	if candidates[0].MatchReason != "exact_name" || candidates[0].Entity.ID != "e1" {
		t.Errorf("expected exact match first, got %+v", candidates[0])
	}
	if candidates[1].MatchReason != "alias" || candidates[1].Entity.ID != "e2" {
		t.Errorf("expected alias match second, got %+v", candidates[1])
	}
}

func TestResolveCandidatesNoMatch(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "list" ]; then
  echo '[{"id":"e1","name":"Someone Else","type":"person","aliases":[],"description":""}]'
fi
`)
	withMockPath(t, dir)

	candidates, err := ResolveCandidates("Alice Example")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(candidates) != 0 {
		t.Errorf("expected no candidates, got %+v", candidates)
	}
}

func TestResolveCandidatesBlankLabel(t *testing.T) {
	candidates, err := ResolveCandidates("   ")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if candidates != nil {
		t.Errorf("expected nil candidates for a blank label, got %+v", candidates)
	}
}

// `entity show --output json` prints human-readable Relations/linked-memory
// sections after the JSON object even with --output json; ShowEntity must
// decode only the leading JSON value.
func TestShowEntityIgnoresTrailingHumanText(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "show" ]; then
  cat <<'JSON'
{"id":"e1","name":"Alice Example","type":"person","aliases":[],"description":""}
JSON
  echo ""
  echo "Relations:"
  echo "  --attended--> e2"
  echo ""
  echo "No linked memories."
fi
`)
	withMockPath(t, dir)

	entity, err := ShowEntity("Alice Example")
	if err != nil {
		t.Fatalf("expected success despite trailing text, got %v", err)
	}
	if entity.ID != "e1" || entity.Name != "Alice Example" {
		t.Errorf("unexpected entity: %+v", entity)
	}
}

func TestShowEntityNotFound(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
echo "Error: entity not found: $3" >&2
exit 1
`)
	withMockPath(t, dir)

	if _, err := ShowEntity("Nobody"); err == nil {
		t.Error("expected an error for an unknown entity")
	}
}

// `entity add` is not idempotent (unique-name constraint), so EnsureEntity
// must look up first and only create when nothing exists.
func TestEnsureEntityReturnsExistingWithoutCreating(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "show" ]; then
  echo '{"id":"e1","name":"Alice Example","type":"person","aliases":[],"description":""}'
elif [ "$1" = "entity" ] && [ "$2" = "add" ]; then
  echo "should not be called" >&2
  exit 1
fi
`)
	withMockPath(t, dir)

	entity, err := EnsureEntity("Alice Example", "person")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if entity.ID != "e1" {
		t.Errorf("unexpected entity: %+v", entity)
	}
}

func TestEnsureEntityCreatesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
STATE_FILE="`+dir+`/created"
if [ "$1" = "entity" ] && [ "$2" = "show" ]; then
  if [ -f "$STATE_FILE" ]; then
    echo '{"id":"e1","name":"New Person","type":"person","aliases":[],"description":""}'
  else
    echo "Error: entity not found: $3" >&2
    exit 1
  fi
elif [ "$1" = "entity" ] && [ "$2" = "add" ]; then
  touch "$STATE_FILE"
  echo "Entity created: $3 (type=person)"
fi
`)
	withMockPath(t, dir)

	entity, err := EnsureEntity("New Person", "person")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if entity.ID != "e1" {
		t.Errorf("unexpected entity: %+v", entity)
	}
}

func TestRelateEntitiesSuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "entity" ] && [ "$2" = "relate" ]; then
  echo "Related: $3 --$4--> $5"
fi
`)
	withMockPath(t, dir)

	if err := RelateEntities("Alice Example", "attended", "Meeting m1"); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestSetMemorySuccess(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
if [ "$1" = "set" ]; then
  echo '{"id":"mem1","content":"Alice proposed the roadmap.","scope":"project","entities":["Alice Example","Meeting m1"]}'
fi
`)
	withMockPath(t, dir)

	record, err := SetMemory("Alice proposed the roadmap.", "project", []string{"Alice Example", "Meeting m1"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if record.ID != "mem1" || len(record.Entities) != 2 {
		t.Errorf("unexpected record: %+v", record)
	}
}

func TestSetMemoryFailure(t *testing.T) {
	dir := t.TempDir()
	writeMockTool(t, dir, "symmemory", `#!/bin/bash
echo "boom" >&2
exit 1
`)
	withMockPath(t, dir)

	if _, err := SetMemory("x", "project", nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected wrapped failure, got %v", err)
	}
}
