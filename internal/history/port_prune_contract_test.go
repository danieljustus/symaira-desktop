package history

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const historyPruneFixtureRel = "../../testdata/port/vault/history-prune.json"

type historyPruneFixture struct {
	SchemaVersion int                `json:"schema_version"`
	Oracle        historyOracleBlock `json:"oracle"`
	SourceHashes  map[string]string  `json:"source_hashes"`
	Cases         []historyPruneCase `json:"cases"`
}

type historyPrunePolicy struct {
	MaxPerFile              int   `json:"max_per_file"`
	MaxAgeSeconds           int64 `json:"max_age_seconds"`
	MaxCheckpointAgeSeconds int64 `json:"max_checkpoint_age_seconds"`
}

type historyPruneStep struct {
	Operation  string `json:"operation"`
	Path       string `json:"path,omitempty"`
	TaskID     string `json:"task_id,omitempty"`
	Content    string `json:"content,omitempty"`
	Index      int    `json:"index,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
}

type historyPruneCase struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	Files       []historyFileSpec   `json:"files"`
	Steps       []historyPruneStep  `json:"steps"`
	Policy      historyPrunePolicy  `json:"policy"`
	Removed     int                 `json:"removed"`
	Error       string              `json:"error,omitempty"`
	ErrorClass  string              `json:"error_class,omitempty"`
	After       []historyFileRecord `json:"after"`
	Objects     []purgeObjectRecord `json:"objects"`
}

func TestPortHistoryPruneContract(t *testing.T) {
	document := buildHistoryPruneFixture(t)
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Clean(historyPruneFixtureRel)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatalf("read fixture: %v (run PORT_GENERATE=1 go test ./internal/history -run '^TestPortHistoryPruneContract$')", err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatalf("history prune fixture is stale; regenerate from the Go oracle\ncurrent sha256=%x expected sha256=%x", sha256.Sum256(current), sha256.Sum256(encoded))
	}
}

func buildHistoryPruneFixture(t *testing.T) historyPruneFixture {
	t.Helper()
	hashes := make(map[string]string)
	for _, source := range []string{"internal/history/history.go", "internal/history/checkpoint.go"} {
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(source))) //nolint:gosec // pinned repository source
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[source] = hex.EncodeToString(sum[:])
	}

	cases := []historyPruneCase{
		recordHistoryPruneCase(t, historyPruneCase{
			ID:          "max-per-file-keeps-newest-and-collects-old-blobs",
			Description: "max-per-file keeps the two newest snapshots and GC removes only the dropped snapshot blobs",
			Files:       []historyFileSpec{},
			Steps: []historyPruneStep{
				{Operation: "write", Path: "a.md", Content: "v1"}, {Operation: "snapshot", Path: "a.md"},
				{Operation: "write", Path: "a.md", Content: "v2"}, {Operation: "snapshot", Path: "a.md"},
				{Operation: "write", Path: "a.md", Content: "v3"}, {Operation: "snapshot", Path: "a.md"},
				{Operation: "write", Path: "a.md", Content: "v4"}, {Operation: "snapshot", Path: "a.md"},
			},
			Policy: historyPrunePolicy{MaxPerFile: 2},
		}, func(s *scenario, spec *historyPruneCase) error { return runHistoryPruneSteps(t, s, spec) }),
		recordHistoryPruneCase(t, historyPruneCase{
			ID:          "max-age-drops-old-but-always-keeps-newest",
			Description: "age pruning drops an old non-newest entry but keeps the newest entry even when it is also older than the cutoff",
			Files:       []historyFileSpec{},
			Steps: []historyPruneStep{
				{Operation: "write", Path: "a.md", Content: "old"}, {Operation: "snapshot", Path: "a.md"},
				{Operation: "age_snapshot", Path: "a.md", Index: 0, AgeSeconds: 172800},
				{Operation: "write", Path: "a.md", Content: "new"}, {Operation: "snapshot", Path: "a.md"},
				{Operation: "age_snapshot", Path: "a.md", Index: 0, AgeSeconds: 172800},
			},
			Policy: historyPrunePolicy{MaxAgeSeconds: 3600},
		}, func(s *scenario, spec *historyPruneCase) error { return runHistoryPruneSteps(t, s, spec) }),
		recordHistoryPruneCase(t, historyPruneCase{
			ID:          "checkpoint-age-protection-and-gc",
			Description: "an aged checkpoint loses its old blob protection while a fresh checkpoint retains its blob after max-per-file pruning",
			Files:       []historyFileSpec{},
			Steps: []historyPruneStep{
				{Operation: "write", Path: "old.md", Content: "old-v1"}, {Operation: "snapshot", Path: "old.md"},
				{Operation: "checkpoint", Path: "old.md", TaskID: "aged"},
				{Operation: "age_checkpoint", TaskID: "aged", AgeSeconds: 172800},
				{Operation: "write", Path: "old.md", Content: "old-v2"}, {Operation: "snapshot", Path: "old.md"},
				{Operation: "write", Path: "fresh.md", Content: "fresh-v1"}, {Operation: "snapshot", Path: "fresh.md"},
				{Operation: "checkpoint", Path: "fresh.md", TaskID: "fresh"},
				{Operation: "write", Path: "fresh.md", Content: "fresh-v2"}, {Operation: "snapshot", Path: "fresh.md"},
			},
			Policy: historyPrunePolicy{MaxPerFile: 1, MaxCheckpointAgeSeconds: 86400},
		}, func(s *scenario, spec *historyPruneCase) error { return runHistoryPruneSteps(t, s, spec) }),
		recordHistoryPruneCase(t, historyPruneCase{
			ID:          "later-corrupt-manifest-preserves-prior-prunes-and-skips-gc",
			Description: "a corrupt later manifest returns the earlier removal count, keeps the earlier rewrite, and prevents the final object GC",
			Files:       []historyFileSpec{},
			Steps: []historyPruneStep{
				{Operation: "write", Path: "a.md", Content: "a-v1"}, {Operation: "snapshot", Path: "a.md"},
				{Operation: "write", Path: "a.md", Content: "a-v2"}, {Operation: "snapshot", Path: "a.md"},
				{Operation: "write", Path: "z.md", Content: "z"}, {Operation: "snapshot", Path: "z.md"},
				{Operation: "corrupt_manifest", Path: "z.md", Content: "{}"},
			},
			Policy: historyPrunePolicy{MaxPerFile: 1},
		}, func(s *scenario, spec *historyPruneCase) error { return runHistoryPruneSteps(t, s, spec) }),
	}

	return historyPruneFixture{
		SchemaVersion: 1,
		Oracle:        historyOracleBlock{Commit: historyOracleCommit, Release: historyOracleRelease},
		SourceHashes:  hashes,
		Cases:         cases,
	}
}

func recordHistoryPruneCase(t *testing.T, document historyPruneCase, run func(*scenario, *historyPruneCase) error) historyPruneCase {
	t.Helper()
	s := newScenario(t, document.Files)
	if err := run(s, &document); err != nil {
		document.Error = historyPruneErrorText(err)
		document.ErrorClass = historyPruneErrorClass(err)
	}
	document.After = historyStateOf(t, s.root)
	document.Objects = purgeObjects(t, s.store.objectsDir())
	return document
}

func runHistoryPruneSteps(t *testing.T, s *scenario, document *historyPruneCase) error {
	t.Helper()
	for _, step := range document.Steps {
		switch step.Operation {
		case "write":
			path := filepath.Join(s.root, filepath.FromSlash(step.Path))
			if err := os.WriteFile(path, []byte(step.Content), 0o644); err != nil { //nolint:gosec // fixture models the source file mode under test
				return err
			}
		case "snapshot":
			if _, err := s.store.Snapshot(step.Path); err != nil {
				return err
			}
		case "checkpoint":
			if _, err := s.store.CheckpointFile(step.TaskID, step.Path); err != nil {
				return err
			}
		case "age_snapshot":
			entries, err := s.store.List(step.Path)
			if err != nil {
				return err
			}
			if step.Index < 0 || step.Index >= len(entries) {
				return fmt.Errorf("snapshot index %d is unavailable", step.Index)
			}
			entries[step.Index].Timestamp = time.Now().UTC().Add(-time.Duration(step.AgeSeconds) * time.Second)
			if err := s.store.writeManifest(step.Path, entries); err != nil {
				return err
			}
		case "age_checkpoint":
			checkpoint, err := s.store.loadCheckpoint(step.TaskID)
			if err != nil {
				return err
			}
			checkpoint.Timestamp = time.Now().UTC().Add(-time.Duration(step.AgeSeconds) * time.Second)
			if err := s.store.saveCheckpoint(checkpoint); err != nil {
				return err
			}
		case "corrupt_manifest":
			path, err := manifestRelPath(step.Path)
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(s.root, path), []byte(step.Content), 0o644); err != nil { //nolint:gosec // fixture models the corrupt manifest mode under test
				return err
			}
		default:
			return fmt.Errorf("unknown history prune step %q", step.Operation)
		}
	}

	removed, err := s.store.Prune(RetentionPolicy{
		MaxPerFile:       document.Policy.MaxPerFile,
		MaxAge:           time.Duration(document.Policy.MaxAgeSeconds) * time.Second,
		MaxCheckpointAge: time.Duration(document.Policy.MaxCheckpointAgeSeconds) * time.Second,
	})
	document.Removed = removed
	return err
}

func historyPruneErrorText(err error) string {
	message := err.Error()
	if strings.HasPrefix(message, "corrupt history manifest for ") {
		if index := strings.Index(message, ": "); index >= 0 {
			return message[:index+2]
		}
	}
	return message
}

func historyPruneErrorClass(err error) string {
	if strings.HasPrefix(err.Error(), "corrupt history manifest for ") {
		return "corrupt_manifest"
	}
	return "other"
}
