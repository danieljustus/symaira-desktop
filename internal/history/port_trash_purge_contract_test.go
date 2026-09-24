package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const selectedTrashPurgeFixtureRel = "../../testdata/port/vault/history-trash-purge.json"

type selectedTrashPurgeFixture struct {
	SchemaVersion int                      `json:"schema_version"`
	Oracle        historyOracleBlock       `json:"oracle"`
	SourceHashes  map[string]string        `json:"source_hashes"`
	Cases         []selectedTrashPurgeCase `json:"cases"`
}

type selectedTrashPurgeCase struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	Files       []historyFileSpec   `json:"files"`
	Result      string              `json:"result,omitempty"`
	Error       string              `json:"error,omitempty"`
	ErrorClass  string              `json:"error_class,omitempty"`
	After       []historyFileRecord `json:"after"`
}

func TestPortHistorySelectedTrashPurgeContract(t *testing.T) {
	document := buildSelectedTrashPurgeFixture(t)
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Clean(selectedTrashPurgeFixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixed repository fixture path
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v (run PORT_GENERATE=1 go test ./internal/history -run '^TestPortHistorySelectedTrashPurgeContract$')", err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatalf("selected trash purge fixture is stale; regenerate from the pinned Go oracle\ncurrent sha256=%x expected sha256=%x", sha256.Sum256(current), sha256.Sum256(encoded))
	}
}

// Go's production loop can remove an earlier selected entry before it sees a
// later stale selector. The Rust migration deliberately prevalidates all
// selectors to avoid that partial destructive outcome.
func TestPortHistorySelectedTrashMixedSelectorSafetyDelta(t *testing.T) {
	s := newScenario(t, []historyFileSpec{{Path: "a.md", Content: "alpha"}, {Path: "b.md", Content: "bravo"}})
	first, err := s.store.Trash("a.md")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.store.Trash("b.md")
	if err != nil {
		t.Fatal(err)
	}
	stale := *second
	stale.OriginalPath = "elsewhere.md"
	removed, err := s.store.PurgeTrashEntries([]TrashEntry{*first, stale})
	if removed != 1 || err == nil || !strings.Contains(err.Error(), "original path changed") {
		t.Fatalf("Go mixed-selector behavior: removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(s.root, trashRelDir(), first.Name)); !os.IsNotExist(err) {
		t.Fatalf("first entry should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.root, trashRelDir(), second.Name)); err != nil {
		t.Fatalf("second entry should remain: %v", err)
	}
}

func buildSelectedTrashPurgeFixture(t *testing.T) selectedTrashPurgeFixture {
	t.Helper()
	hashes := make(map[string]string)
	for _, source := range []string{"internal/history/trash.go", "internal/history/history.go"} {
		//nolint:gosec // repository-relative Go oracle source
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(source)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[source] = hex.EncodeToString(sum[:])
	}
	cases := []selectedTrashPurgeCase{
		recordSelectedTrashPurgeCase(t, selectedTrashPurgeCase{
			ID:          "selected-purge-keeps-unselected-and-retries-idempotently",
			Description: "selected entry removal leaves unrelated trash intact and repeating the request after success is a no-op",
			Files:       []historyFileSpec{{Path: "a.md", Content: "alpha"}, {Path: "b.md", Content: "bravo"}},
		}, func(s *scenario) (string, error) {
			selected, err := s.store.Trash("a.md")
			if err != nil {
				return "", err
			}
			if _, err := s.store.Trash("b.md"); err != nil {
				return "", err
			}
			first, err := s.store.PurgeTrashEntries([]TrashEntry{*selected})
			if err != nil {
				return "", err
			}
			retry, err := s.store.PurgeTrashEntries([]TrashEntry{*selected})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("first=%d retry=%d remaining=%s", first, retry, selectedTrashNames(t, s)), nil
		}),
		recordSelectedTrashPurgeCase(t, selectedTrashPurgeCase{
			ID:          "selected-purge-corrupt-unselected-metadata-preserves-all",
			Description: "a null unselected metadata file fails strict inventory validation before selected entry removal",
			Files:       []historyFileSpec{{Path: "a.md", Content: "alpha"}, {Path: "b.md", Content: "bravo"}},
		}, func(s *scenario) (string, error) {
			selected, err := s.store.Trash("a.md")
			if err != nil {
				return "", err
			}
			other, err := s.store.Trash("b.md")
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(s.root, trashRelDir(), other.Name+trashMetaSuffix), []byte("null"), 0o600); err != nil {
				return "", err
			}
			_, err = s.store.PurgeTrashEntries([]TrashEntry{*selected})
			return "", err
		}),
		recordSelectedTrashPurgeCase(t, selectedTrashPurgeCase{
			ID:          "selected-purge-replaced-metadata-path-preserves-all",
			Description: "valid replacement metadata with a changed original path refuses selected deletion before mutation",
			Files:       []historyFileSpec{{Path: "a.md", Content: "alpha"}},
		}, func(s *scenario) (string, error) {
			selected, err := s.store.Trash("a.md")
			if err != nil {
				return "", err
			}
			replaced := *selected
			replaced.OriginalPath = "elsewhere.md"
			data, err := json.MarshalIndent(replaced, "", "  ")
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(s.root, trashRelDir(), selected.Name+trashMetaSuffix), data, 0o600); err != nil {
				return "", err
			}
			_, err = s.store.PurgeTrashEntries([]TrashEntry{*selected})
			return "", err
		}),
		recordSelectedTrashPurgeCase(t, selectedTrashPurgeCase{
			ID:          "selected-purge-replaced-payload-preserves-all",
			Description: "a payload whose size no longer matches its metadata fails strict inventory validation before mutation",
			Files:       []historyFileSpec{{Path: "a.md", Content: "alpha"}},
		}, func(s *scenario) (string, error) {
			selected, err := s.store.Trash("a.md")
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(s.root, trashRelDir(), selected.Name), []byte("replacement payload"), 0o600); err != nil {
				return "", err
			}
			_, err = s.store.PurgeTrashEntries([]TrashEntry{*selected})
			return "", err
		}),
		recordSelectedTrashPurgeCase(t, selectedTrashPurgeCase{
			ID:          "selected-purge-missing-metadata-preserves-all",
			Description: "a payload missing its metadata makes the strict inventory invalid before selected deletion",
			Files:       []historyFileSpec{{Path: "a.md", Content: "alpha"}},
		}, func(s *scenario) (string, error) {
			selected, err := s.store.Trash("a.md")
			if err != nil {
				return "", err
			}
			if err := os.Remove(filepath.Join(s.root, trashRelDir(), selected.Name+trashMetaSuffix)); err != nil {
				return "", err
			}
			_, err = s.store.PurgeTrashEntries([]TrashEntry{*selected})
			return "", err
		}),
		recordSelectedTrashPurgeCase(t, selectedTrashPurgeCase{
			ID:          "selected-purge-missing-payload-preserves-all",
			Description: "metadata missing its payload makes the strict inventory invalid before selected deletion",
			Files:       []historyFileSpec{{Path: "a.md", Content: "alpha"}},
		}, func(s *scenario) (string, error) {
			selected, err := s.store.Trash("a.md")
			if err != nil {
				return "", err
			}
			if err := os.Remove(filepath.Join(s.root, trashRelDir(), selected.Name)); err != nil {
				return "", err
			}
			_, err = s.store.PurgeTrashEntries([]TrashEntry{*selected})
			return "", err
		}),
	}
	return selectedTrashPurgeFixture{
		SchemaVersion: 1,
		Oracle:        historyOracleBlock{Commit: "e0364e835c03672178db936a2263fba1fb1ec2ab", Release: "post-v0.12.2-security-880"},
		SourceHashes:  hashes,
		Cases:         cases,
	}
}

func recordSelectedTrashPurgeCase(t *testing.T, document selectedTrashPurgeCase, run func(*scenario) (string, error)) selectedTrashPurgeCase {
	t.Helper()
	s := newScenario(t, document.Files)
	result, err := run(s)
	if err != nil {
		document.Error = historyErrorText(err)
		document.ErrorClass = historyErrorClass(err)
	} else {
		document.Result = result
	}
	document.After = historyStateOf(t, s.root)
	return document
}

func selectedTrashNames(t *testing.T, s *scenario) string {
	t.Helper()
	entries, err := s.store.TrashListStrict()
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.Name
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}
