package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const historyServiceFixture = "../../testdata/port/vault/history-service.json"

type historyServiceContract struct {
	SchemaVersion int               `json:"schema_version"`
	Oracle        string            `json:"oracle_commit"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Restore       historyState      `json:"restore"`
	Malformed     historyState      `json:"malformed_restore"`
	Undo          historyState      `json:"undo"`
}

type historyState struct {
	SnapshotID   string `json:"snapshot_id,omitempty"`
	Content      string `json:"content"`
	OriginalHits int    `json:"original_hits"`
	ChangedHits  int    `json:"changed_hits"`
	NewFileGone  bool   `json:"new_file_gone,omitempty"`
	NewFileHits  int    `json:"new_file_hits,omitempty"`
	Activity     bool   `json:"activity,omitempty"`
	ErrorPrefix  string `json:"error_prefix,omitempty"`
}

func TestPortHistoryServiceContract(t *testing.T) {
	fixture := historyServiceContract{
		SchemaVersion: 1,
		Oracle:        "0dea61627cb9746633c2c7b669f7684d395f29f7",
		SourceHashes: map[string]string{
			"internal/service/history.go":    historySourceHash(t, "history.go"),
			"internal/history/history.go":    historySourceHash(t, "../history/history.go"),
			"internal/history/checkpoint.go": historySourceHash(t, "../history/checkpoint.go"),
		},
		Restore:   historyRestoreState(t),
		Malformed: historyMalformedRestoreState(t),
		Undo:      historyUndoState(t),
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(historyServiceFixture), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(historyServiceFixture, encoded, 0o644); err != nil { //nolint:gosec // explicit fixture generation
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(historyServiceFixture) //nolint:gosec // fixed repository fixture
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("history service fixture is stale; regenerate from the Go oracle")
	}
}

func historyMalformedRestoreState(t *testing.T) historyState {
	t.Helper()
	svc := newTestService(t)
	const rel = "malformed.md"
	const malformed = "---\ntitle: [broken\n---\nmalformedneedle\n"
	if err := os.WriteFile(filepath.Join(svc.VaultRoot, rel), []byte(malformed), 0o644); err != nil { //nolint:gosec // disposable vault
		t.Fatal(err)
	}
	entry, err := svc.History.Snapshot(rel)
	if err != nil || entry == nil {
		t.Fatalf("snapshot malformed: %v", err)
	}
	historyWriteAndIndex(t, svc, rel, "---\ntitle: Valid\n---\nvalidreplacement\n")
	restored, restoreErr := svc.HistoryRestore(rel, "")
	if restored == nil || restoreErr == nil || !strings.HasPrefix(restoreErr.Error(), "restored file but failed to parse for indexing: ") {
		t.Fatalf("partial restore = %#v, %v", restored, restoreErr)
	}
	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, rel)) //nolint:gosec // disposable vault
	if err != nil {
		t.Fatal(err)
	}
	return historyState{
		SnapshotID:  restored.ID,
		Content:     string(content),
		ChangedHits: historySearchCount(t, svc, "validreplacement"),
		ErrorPrefix: "restored file but failed to parse for indexing: ",
	}
}

func historyRestoreState(t *testing.T) historyState {
	t.Helper()
	svc := newTestService(t)
	const rel = "restored.md"
	const original = "---\ntitle: Restored\n---\noriginalneedle\n"
	const changed = "---\ntitle: Restored\n---\nchangedneedle\n"
	historyWriteAndIndex(t, svc, rel, original)
	entry, err := svc.History.Snapshot(rel)
	if err != nil || entry == nil {
		t.Fatalf("snapshot: %v", err)
	}
	historyWriteAndIndex(t, svc, rel, changed)
	if _, err := svc.HistoryRestore(rel, ""); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, rel)) //nolint:gosec // disposable vault
	if err != nil {
		t.Fatal(err)
	}
	journal, err := filepath.Glob(filepath.Join(svc.VaultRoot, ".symdesk", "journal", "*.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	activity := false
	for _, path := range journal {
		data, readErr := os.ReadFile(path) //nolint:gosec // disposable vault journal
		if readErr != nil {
			t.Fatal(readErr)
		}
		activity = activity || strings.Contains(string(data), `"event":"file_changed"`) && strings.Contains(string(data), `"details":"restored snapshot "`)
	}
	return historyState{
		SnapshotID:   entry.ID,
		Content:      string(content),
		OriginalHits: historySearchCount(t, svc, "originalneedle"),
		ChangedHits:  historySearchCount(t, svc, "changedneedle"),
		Activity:     activity,
	}
}

func historyUndoState(t *testing.T) historyState {
	t.Helper()
	svc := newTestService(t)
	const original = "---\ntitle: Checkpoint\n---\ncheckpointoriginal\n"
	historyWriteAndIndex(t, svc, "checkpoint.md", original)
	if _, err := svc.CheckpointFiles("task-history", []string{"checkpoint.md", "new.md"}); err != nil {
		t.Fatal(err)
	}
	historyWriteAndIndex(t, svc, "checkpoint.md", "---\ntitle: Checkpoint\n---\ncheckpointchanged\n")
	historyWriteAndIndex(t, svc, "new.md", "---\ntitle: New\n---\nnewfiletoken\n")
	if _, err := svc.CheckpointUndo("task-history"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(svc.VaultRoot, "checkpoint.md")) //nolint:gosec // disposable vault
	if err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(filepath.Join(svc.VaultRoot, "new.md"))
	return historyState{
		Content:      string(content),
		OriginalHits: historySearchCount(t, svc, "checkpointoriginal"),
		ChangedHits:  historySearchCount(t, svc, "checkpointchanged"),
		NewFileGone:  os.IsNotExist(statErr),
		NewFileHits:  historySearchCount(t, svc, "newfiletoken"),
	}
}

func historyWriteAndIndex(t *testing.T, svc *Service, rel, content string) {
	t.Helper()
	path := filepath.Join(svc.VaultRoot, rel)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // disposable vault
		t.Fatal(err)
	}
	doc, err := vault.ParseFileInRoot(svc.VaultRoot, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
}

func historySearchCount(t *testing.T, svc *Service, query string) int {
	t.Helper()
	hits, err := svc.Search(query)
	if err != nil {
		t.Fatal(err)
	}
	return len(hits)
}

func historySourceHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // fixed source paths
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
