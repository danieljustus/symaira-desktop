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

const historyPurgeFixtureRel = "../../testdata/port/vault/history-purge.json"

type historyPurgeFixture struct {
	SchemaVersion int                `json:"schema_version"`
	Oracle        historyOracleBlock `json:"oracle"`
	SourceHashes  map[string]string  `json:"source_hashes"`
	Cases         []historyPurgeCase `json:"cases"`
}

type historyPurgeCase struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	Operation   string              `json:"operation"`
	Files       []historyFileSpec   `json:"files"`
	Paths       []string            `json:"paths"`
	Result      string              `json:"result,omitempty"`
	Error       string              `json:"error,omitempty"`
	ErrorClass  string              `json:"error_class,omitempty"`
	After       []historyFileRecord `json:"after"`
	Objects     []purgeObjectRecord `json:"objects"`
}

type purgeObjectRecord struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func TestPortHistoryPurgeContract(t *testing.T) {
	document := buildHistoryPurgeFixture(t)
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Clean(historyPurgeFixtureRel)
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
		t.Fatalf("read fixture: %v (run PORT_GENERATE=1 go test ./internal/history -run '^TestPortHistoryPurgeContract$')", err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatalf("history purge fixture is stale; regenerate from the pinned Go oracle\ncurrent sha256=%x expected sha256=%x", sha256.Sum256(current), sha256.Sum256(encoded))
	}
}

func buildHistoryPurgeFixture(t *testing.T) historyPurgeFixture {
	t.Helper()
	hashes := make(map[string]string)
	for _, source := range []string{"internal/history/history.go", "internal/history/checkpoint.go"} {
		//nolint:gosec // repository-relative Go oracle source
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(source)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[source] = hex.EncodeToString(sum[:])
	}
	cases := []historyPurgeCase{
		recordPurgeCase(t, historyPurgeCase{
			ID: "purge-success-filters-checkpoints-and-gc", Description: "purge removes selected manifests and checkpoint references, then collects target-only and orphan blobs while preserving shared history",
			Operation: "purge_paths", Paths: []string{"target.md", "target-new.md", "target-skip/child.md", "target-only-new.md"},
			Files: []historyFileSpec{{Path: "target.md", Content: "target-only"}, {Path: "keep.md", Content: "shared"}, {Path: "shared.md", Content: "shared"}, {Path: "target-skip", Content: "blocks child"}},
		}, func(s *scenario) (string, error) {
			if _, err := s.store.Snapshot("target.md"); err != nil {
				return "", err
			}
			if _, err := s.store.Snapshot("keep.md"); err != nil {
				return "", err
			}
			if _, err := s.store.Snapshot("shared.md"); err != nil {
				return "", err
			}
			for _, path := range []string{"target.md", "keep.md", "target-new.md", "target-skip/child.md"} {
				if _, err := s.store.CheckpointFile("mixed", path); err != nil {
					return "", err
				}
			}
			if _, err := s.store.CheckpointFile("empty", "target-only-new.md"); err != nil {
				return "", err
			}
			orphanID := strings.Repeat("f", 64)
			if err := os.WriteFile(filepath.Join(s.store.objectsDir(), orphanID), []byte("orphan"), 0o600); err != nil {
				return "", err
			}
			if err := s.store.PurgePaths("target.md", "target-new.md", "target-skip/child.md", "target-only-new.md"); err != nil {
				return "", err
			}
			return purgeResult(t, s), nil
		}),
		recordPurgeCase(t, historyPurgeCase{
			ID: "preflight-valid-inventory-is-read-only", Description: "preflight validates references without removing manifests, checkpoints or objects",
			Operation: "preflight_purge_paths", Paths: []string{"target.md"},
			Files: []historyFileSpec{{Path: "target.md", Content: "target"}, {Path: "keep.md", Content: "keep"}},
		}, func(s *scenario) (string, error) {
			for _, path := range []string{"target.md", "keep.md"} {
				if _, err := s.store.Snapshot(path); err != nil {
					return "", err
				}
			}
			if _, err := s.store.CheckpointFile("task", "target.md"); err != nil {
				return "", err
			}
			if err := s.store.PreflightPurgePaths("target.md"); err != nil {
				return "", err
			}
			return purgeResult(t, s), nil
		}),
		recordPurgeCase(t, historyPurgeCase{
			ID: "purge-corrupt-survivor-preserves-target", Description: "a null survivor manifest fails closed before the requested target manifest or blobs are changed",
			Operation: "purge_paths", Paths: []string{"target.md"},
			Files: []historyFileSpec{{Path: "target.md", Content: "target"}, {Path: "keep.md", Content: "keep"}},
		}, func(s *scenario) (string, error) {
			if _, err := s.store.Snapshot("target.md"); err != nil {
				return "", err
			}
			if _, err := s.store.Snapshot("keep.md"); err != nil {
				return "", err
			}
			if _, err := s.store.CheckpointFile("task", "target.md"); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(s.root, ".symdesk/history/manifest/keep.md.json"), []byte("null"), 0o644); err != nil {
				return "", err
			}
			return "", s.store.PurgePaths("target.md")
		}),
		recordPurgeCase(t, historyPurgeCase{
			ID: "purge-corrupt-checkpoint-preserves-target", Description: "a null survivor checkpoint fails closed before the requested target manifest or blobs are changed",
			Operation: "purge_paths", Paths: []string{"target.md"},
			Files: []historyFileSpec{{Path: "target.md", Content: "target"}, {Path: "keep.md", Content: "keep"}},
		}, func(s *scenario) (string, error) {
			if _, err := s.store.Snapshot("target.md"); err != nil {
				return "", err
			}
			if _, err := s.store.CheckpointFile("task", "target.md"); err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(s.root, ".symdesk/history/checkpoints/task.json"), []byte("null"), 0o644); err != nil {
				return "", err
			}
			return "", s.store.PurgePaths("target.md")
		}),
		recordPurgeCase(t, historyPurgeCase{
			ID: "purge-replaced-object-fails-before-mutation", Description: "a referenced content-addressed blob replaced with different bytes fails validation before target mutation",
			Operation: "purge_paths", Paths: []string{"target.md"},
			Files: []historyFileSpec{{Path: "target.md", Content: "target"}, {Path: "keep.md", Content: "keep"}},
		}, func(s *scenario) (string, error) {
			if _, err := s.store.Snapshot("target.md"); err != nil {
				return "", err
			}
			keep, err := s.store.Snapshot("keep.md")
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(filepath.Join(s.store.objectsDir(), keep.ID), []byte("replaced"), 0o644); err != nil {
				return "", err
			}
			return "", s.store.PurgePaths("target.md")
		}),
		recordPurgeCase(t, historyPurgeCase{
			ID: "preflight-traversal-target-is-rejected", Description: "preflight rejects traversal paths before opening or changing recovery state",
			Operation: "preflight_purge_paths", Paths: []string{"../outside.md"},
		}, func(s *scenario) (string, error) {
			if err := s.store.PreflightPurgePaths("../outside.md"); err != nil {
				return "", err
			}
			return "preflight accepted traversal", nil
		}),
	}
	return historyPurgeFixture{
		SchemaVersion: 1,
		Oracle:        historyOracleBlock{Commit: "d78e40d4083eefbda54aee53b771d5da6136c905", Release: "post-v0.12.2-security-880"},
		SourceHashes:  hashes,
		Cases:         cases,
	}
}

func recordPurgeCase(t *testing.T, document historyPurgeCase, run func(*scenario) (string, error)) historyPurgeCase {
	t.Helper()
	if document.Files == nil {
		document.Files = []historyFileSpec{}
	}
	if document.Paths == nil {
		document.Paths = []string{}
	}
	s := newScenario(t, document.Files)
	result, err := run(s)
	if err != nil {
		document.Error = historyErrorText(err)
		document.ErrorClass = historyErrorClass(err)
	} else {
		document.Result = result
	}
	document.After = historyStateOf(t, s.root)
	document.Objects = purgeObjects(t, s.store.objectsDir())
	return document
}

func purgeResult(t *testing.T, s *scenario) string {
	t.Helper()
	target, err := s.store.List("target.md")
	if err != nil {
		t.Fatal(err)
	}
	keep, err := s.store.List("keep.md")
	if err != nil {
		t.Fatal(err)
	}
	checkpoints, err := s.store.ListCheckpoints()
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("target=%d keep=%d checkpoints=%d objects=%s", len(target), len(keep), len(checkpoints), purgeObjectNames(purgeObjects(t, s.store.objectsDir())))
}

func purgeObjects(t *testing.T, directory string) []purgeObjectRecord {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return []purgeObjectRecord{}
	}
	if err != nil {
		t.Fatal(err)
	}
	objects := make([]purgeObjectRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		objects = append(objects, purgeObjectRecord{Name: entry.Name(), Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	return objects
}

func purgeObjectNames(objects []purgeObjectRecord) string {
	names := make([]string, len(objects))
	for i, object := range objects {
		names[i] = object.Name
	}
	return strings.Join(names, ",")
}
