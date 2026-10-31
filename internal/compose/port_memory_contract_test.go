package compose

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Records the symmemory adapter surface (ListEntities, GetNeighbors,
// ResolveCandidates, ShowEntity, EnsureEntity, RelateEntities, SetMemory)
// through the single ResolveFunc seam against a loopback shell double. No
// real sibling binary, network or operator state is involved.

type portMemoryCall struct {
	Fn      string   `json:"fn"`
	Args    []string `json:"args,omitempty"`
	Mode    string   `json:"mode"`
	Pre     []string `json:"pre_entities,omitempty"` // entities that already exist
	Missing bool     `json:"missing,omitempty"`      // ResolveFunc fails
	Value   string   `json:"value,omitempty"`        // JSON of the result
	Error   string   `json:"error,omitempty"`
	Log     string   `json:"log,omitempty"`
	State   []string `json:"state,omitempty"` // entities after the call
}

const portMemoryScript = `#!/bin/sh
state="$FAKE_SYMMEMORY_STATE"
{
  printf 'argv='
  for a in "$@"; do printf '[%s]' "$a"; done
  printf '\n'
} >> "$FAKE_SYMMEMORY_LOG"
case "$1 $2" in
  "entity list")
    case "$FAKE_SYMMEMORY_MODE" in
      garbage) printf 'not json' ;;
      fail) printf 'list broke\n' >&2; exit 2 ;;
      slow) exec sleep 30 ;;
      empty) printf '[]' ;;
      *) printf '[{"id":"e1","name":"Ada Lovelace","type":"person","aliases":["Countess","ada"],"description":"d1"},{"id":"e2","name":"ADA","type":"other","aliases":null,"description":""},{"id":"e3","name":"Babbage","type":"person","aliases":["Ada Lovelace"],"description":"d3"}]' ;;
    esac ;;
  "entity neighbors")
    case "$FAKE_SYMMEMORY_MODE" in
      garbage) printf '{' ;;
      fail) printf 'no such entity\n' >&2; exit 1 ;;
      *) printf '{"nodes":[{"id":"e2","name":"Babbage","type":"person","aliases":[],"description":""}],"edges":[{"from_entity_id":"e1","to_entity_id":"e2","relation_type":"worked_with"}]}' ;;
    esac ;;
  "entity show")
    if [ -e "$state/$3" ]; then
      printf '{"id":"s1","name":"%s","type":"person","aliases":["x"],"description":"shown"}\n' "$3"
      printf 'Relations: 0\nMemories: 0\n'
    else
      printf 'entity not found\n' >&2; exit 1
    fi ;;
  "entity add")
    case "$FAKE_SYMMEMORY_MODE" in
      fail) printf 'add broke\n' >&2; exit 1 ;;
      race) : > "$state/$3"; printf 'unique constraint\n' >&2; exit 1 ;;
      *) : > "$state/$3" ;;
    esac ;;
  "entity relate")
    case "$FAKE_SYMMEMORY_MODE" in
      fail) printf 'relate broke\n' >&2; exit 4 ;;
      *) : ;;
    esac ;;
  "set --value")
    case "$FAKE_SYMMEMORY_MODE" in
      garbage) printf 'saved!' ;;
      fail) printf 'set broke\n' >&2; exit 5 ;;
      *) printf '{"id":"m1","content":"%s","scope":"%s","entities":["e1"]}' "$3" "$5" ;;
    esac ;;
  *) printf 'unknown command\n' >&2; exit 64 ;;
esac
`

func portMemoryCalls() []portMemoryCall {
	return []portMemoryCall{
		{Fn: "ListEntities", Mode: "ok"},
		{Fn: "ListEntities", Mode: "empty"},
		{Fn: "ListEntities", Mode: "garbage"},
		{Fn: "ListEntities", Mode: "fail"},
		{Fn: "ListEntities", Mode: "ok", Missing: true},
		{Fn: "ListEntities", Mode: "slow"},
		{Fn: "GetNeighbors", Args: []string{"Ada Lovelace"}, Mode: "ok"},
		{Fn: "GetNeighbors", Args: []string{"--odd name"}, Mode: "ok"},
		{Fn: "GetNeighbors", Args: []string{"x"}, Mode: "garbage"},
		{Fn: "GetNeighbors", Args: []string{"x"}, Mode: "fail"},
		{Fn: "GetNeighbors", Args: []string{"x"}, Mode: "ok", Missing: true},
		{Fn: "ResolveCandidates", Args: []string{"  ada  "}, Mode: "ok"},
		{Fn: "ResolveCandidates", Args: []string{"countess"}, Mode: "ok"},
		{Fn: "ResolveCandidates", Args: []string{"ADA LOVELACE"}, Mode: "ok"},
		{Fn: "ResolveCandidates", Args: []string{"nobody"}, Mode: "ok"},
		{Fn: "ResolveCandidates", Args: []string{"   "}, Mode: "ok"},
		{Fn: "ResolveCandidates", Args: []string{"ada"}, Mode: "fail"},
		{Fn: "ShowEntity", Args: []string{"Ann"}, Mode: "ok", Pre: []string{"Ann"}},
		{Fn: "ShowEntity", Args: []string{"Ann"}, Mode: "ok"},
		{Fn: "EnsureEntity", Args: []string{"Ann", "person"}, Mode: "ok", Pre: []string{"Ann"}},
		{Fn: "EnsureEntity", Args: []string{"Ann", "person"}, Mode: "ok"},
		{Fn: "EnsureEntity", Args: []string{"Ann", "person"}, Mode: "race"},
		{Fn: "EnsureEntity", Args: []string{"Ann", "person"}, Mode: "fail"},
		{Fn: "RelateEntities", Args: []string{"Ann", "attended", "Standup"}, Mode: "ok"},
		{Fn: "RelateEntities", Args: []string{"Ann", "attended", "Standup"}, Mode: "fail"},
		{Fn: "RelateEntities", Args: []string{"Ann", "attended", "Standup"}, Mode: "ok", Missing: true},
		{Fn: "SetMemory", Args: []string{"fact one", "project"}, Mode: "ok"},
		{Fn: "SetMemory", Args: []string{"fact two", "project", "Ann", "Bob"}, Mode: "ok"},
		{Fn: "SetMemory", Args: []string{"fact", "project"}, Mode: "garbage"},
		{Fn: "SetMemory", Args: []string{"fact", "project"}, Mode: "fail"},
	}
}

func runPortMemoryCall(t *testing.T, call portMemoryCall) portMemoryCall {
	t.Helper()
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "symmemory")
	if err := os.WriteFile(script, []byte(portMemoryScript), 0o700); err != nil { //nolint:gosec // executable test double in a test-owned directory
		t.Fatal(err)
	}
	for _, name := range call.Pre {
		if err := os.WriteFile(filepath.Join(state, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	logPath := filepath.Join(root, "symmemory.log")
	t.Setenv("FAKE_SYMMEMORY_STATE", state)
	t.Setenv("FAKE_SYMMEMORY_LOG", logPath)
	t.Setenv("FAKE_SYMMEMORY_MODE", call.Mode)
	original := ResolveFunc
	ResolveFunc = func(name string) (string, error) {
		if call.Missing {
			return "", errors.New("synthetic: " + name + " not installed")
		}
		return script, nil
	}
	t.Cleanup(func() { ResolveFunc = original })

	var value any
	var err error
	switch call.Fn {
	case "ListEntities":
		value, err = ListEntities()
	case "GetNeighbors":
		value, err = GetNeighbors(call.Args[0])
	case "ResolveCandidates":
		value, err = ResolveCandidates(call.Args[0])
	case "ShowEntity":
		value, err = ShowEntity(call.Args[0])
	case "EnsureEntity":
		value, err = EnsureEntity(call.Args[0], call.Args[1])
	case "RelateEntities":
		err = RelateEntities(call.Args[0], call.Args[1], call.Args[2])
	case "SetMemory":
		value, err = SetMemory(call.Args[0], call.Args[1], call.Args[2:])
	default:
		t.Fatalf("unknown function %s", call.Fn)
	}
	if err != nil {
		call.Error = strings.ReplaceAll(err.Error(), root, "<ROOT>")
	} else {
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		call.Value = string(encoded)
	}
	if b, readErr := os.ReadFile(logPath); readErr == nil { //nolint:gosec // test-owned log
		call.Log = string(b)
	}
	entries, _ := os.ReadDir(state)
	for _, entry := range entries {
		call.State = append(call.State, entry.Name())
	}
	return call
}

func TestPortComposeMemoryContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell double is Unix-only")
	}
	var calls []portMemoryCall
	for _, call := range portMemoryCalls() {
		calls = append(calls, runPortMemoryCall(t, call))
	}
	document := struct {
		SchemaVersion int              `json:"schema_version"`
		Script        string           `json:"symmemory_script"`
		Calls         []portMemoryCall `json:"calls"`
	}{1, portMemoryScript, calls}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(repo, "testdata/port/ai/compose-memory.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	expected, err := os.ReadFile(path) //nolint:gosec // fixed repository fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, expected) {
		t.Fatal("compose memory fixture drift; regenerate deliberately with make compose-memory-fixtures-generate")
	}
}
