package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

const datasetPurgeFixturePath = "../../testdata/port/dataset/purge.json"

type datasetPurgeFixture struct {
	SchemaVersion int                               `json:"schema_version"`
	Cases         []datasetPurgeFixtureCase         `json:"cases"`
	RecoveryCases []datasetPurgeRecoveryFixtureCase `json:"recovery_cases"`
}

type datasetPurgeFixtureCase struct {
	ID              string   `json:"id"`
	Stale           bool     `json:"stale"`
	Error           string   `json:"error,omitempty"`
	Handle          bool     `json:"handle_exists"`
	Raw             bool     `json:"raw_exists"`
	Rows            int      `json:"rows"`
	Journal         bool     `json:"journal_exists"`
	DatasetTrash    int      `json:"dataset_trash"`
	RawHistory      int      `json:"raw_history_entries"`
	CheckpointPaths []string `json:"checkpoint_paths,omitempty"`
	HistoryObjects  int      `json:"history_objects"`
}

type datasetPurgeRecoveryFixtureCase struct {
	ID           string               `json:"id"`
	InitialError string               `json:"initial_error,omitempty"`
	Before       datasetPurgeSnapshot `json:"before"`
	After        datasetPurgeSnapshot `json:"after"`
	Error        string               `json:"error,omitempty"`
	WindowsError string               `json:"windows_error,omitempty"`
}

type datasetPurgeSnapshot struct {
	Files []datasetPurgeFile `json:"files"`
	Rows  []datasetPurgeRow  `json:"rows"`
}

type datasetPurgeFile struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Target  string `json:"target,omitempty"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Content string `json:"content,omitempty"`
}

type datasetPurgeRow struct {
	RowKey     string `json:"row_key"`
	Identity   string `json:"identity"`
	ValuesJSON string `json:"values_json"`
	SourcePath string `json:"source_path"`
	RowNumber  int    `json:"row_number"`
}

// TestPortDatasetPurgeContract is the Go-owned oracle consumed by the Rust
// dataset purge contract test. Set PORT_GENERATE=1 only when intentionally
// refreshing the checked-in behavioral fixture.
func TestPortDatasetPurgeContract(t *testing.T) {
	fixture := datasetPurgeFixture{SchemaVersion: 1}
	for _, stale := range []bool{false, true} {
		id := "success"
		if stale {
			id = "stale-fingerprint"
		}
		svc := newTestService(t)
		datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
		if !stale {
			rawRel := "datasets/orders/2026-01-04.csv"
			rawAbs := filepath.Join(svc.VaultRoot, filepath.FromSlash(rawRel))
			rawBytes, err := os.ReadFile(rawAbs) //nolint:gosec // test-owned vault path
			if err != nil {
				t.Fatal(err)
			}
			keep := filepath.Join(svc.VaultRoot, "notes/keep.md")
			if err := os.MkdirAll(filepath.Dir(keep), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keep, []byte("unrelated checkpoint content"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{"datasets/orders.md", "notes/keep.md", rawRel} {
				if _, err := svc.History.CheckpointFile("retention-task", path); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := svc.History.Trash(rawRel); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(rawAbs, rawBytes, 0o600); err != nil { //nolint:gosec // rawAbs is a test path rooted in the temporary vault
				t.Fatal(err)
			}
		}
		state, err := svc.RetentionState("datasets/orders.md")
		if err != nil {
			t.Fatal(err)
		}
		if stale {
			if err := os.WriteFile(filepath.Join(svc.VaultRoot, "datasets/orders/2026-01-04.csv"), []byte("id,amount\no1,99\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		purgeErr := svc.DatasetPurgeWithFingerprint("orders", dataset.DefaultRetentionRule, state.Fingerprint)
		observed := datasetPurgeFixtureCase{ID: id}
		if purgeErr != nil {
			observed.Error = purgeErr.Error()
		}
		for path, target := range map[string]*bool{
			"datasets/orders.md":                 &observed.Handle,
			"datasets/orders/2026-01-04.csv":     &observed.Raw,
			".symdesk/dataset-purge/orders.json": &observed.Journal,
		} {
			_, statErr := os.Stat(filepath.Join(svc.VaultRoot, filepath.FromSlash(path)))
			*target = statErr == nil
			if statErr != nil && !os.IsNotExist(statErr) {
				t.Fatal(statErr)
			}
		}
		rows, err := svc.DB.DatasetRows("orders")
		if err != nil {
			t.Fatal(err)
		}
		observed.Rows = len(rows)
		observed.Stale = stale
		trash, err := svc.History.TrashListStrict()
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range trash {
			if entry.OriginalPath == "datasets/orders.md" || strings.HasPrefix(entry.OriginalPath, "datasets/orders/") {
				observed.DatasetTrash++
			}
		}
		if !stale {
			entries, err := svc.History.List("datasets/orders/2026-01-04.csv")
			if err != nil {
				t.Fatal(err)
			}
			observed.RawHistory = len(entries)
			checkpoints, err := svc.History.ListCheckpoints()
			if err != nil {
				t.Fatal(err)
			}
			for _, checkpoint := range checkpoints {
				for _, file := range checkpoint.Files {
					observed.CheckpointPaths = append(observed.CheckpointPaths, file.RelPath)
				}
			}
			objects, err := os.ReadDir(filepath.Join(svc.VaultRoot, ".symdesk/history/objects"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			observed.HistoryObjects = len(objects)
		}
		fixture.Cases = append(fixture.Cases, observed)
	}
	fixture.RecoveryCases = buildDatasetPurgeRecoveryCases(t)

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if os.Getenv("PORT_GENERATE") == "1" {
		path := filepath.Clean(datasetPurgeFixturePath)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // explicit fixture generation
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(datasetPurgeFixturePath) //nolint:gosec // fixed contract fixture
	if err != nil {
		t.Fatalf("read dataset purge fixture: %v (run PORT_GENERATE=1 to create it)", err)
	}
	var expected datasetPurgeFixture
	if err := json.Unmarshal(current, &expected); err != nil {
		t.Fatal(err)
	}
	if expected.SchemaVersion != 1 || len(expected.Cases) != len(fixture.Cases) {
		t.Fatalf("invalid dataset purge fixture header/case count: %#v", expected)
	}
	if len(expected.RecoveryCases) != len(fixture.RecoveryCases) {
		t.Fatalf("invalid dataset purge recovery fixture case count: got %d want %d", len(expected.RecoveryCases), len(fixture.RecoveryCases))
	}
	for i := range fixture.Cases {
		want := expected.Cases[i]
		got := fixture.Cases[i]
		if got.ID != want.ID || got.Stale != want.Stale || got.Handle != want.Handle || got.Raw != want.Raw || got.Rows != want.Rows || got.Journal != want.Journal || got.DatasetTrash != want.DatasetTrash || got.RawHistory != want.RawHistory || got.HistoryObjects != want.HistoryObjects || strings.Join(got.CheckpointPaths, "\n") != strings.Join(want.CheckpointPaths, "\n") || (want.Error != "" && !strings.Contains(got.Error, want.Error)) || (want.Error == "" && got.Error != "") {
			t.Errorf("case %s: Go outcome %#v, fixture %#v", got.ID, got, want)
		}
	}
	for i := range fixture.RecoveryCases {
		got, want := fixture.RecoveryCases[i], expected.RecoveryCases[i]
		if got.ID != want.ID || got.InitialError != want.InitialError || got.Error != want.Error || !equalDatasetPurgeSnapshot(got.Before, want.Before) || !equalDatasetPurgeSnapshot(got.After, want.After) {
			t.Errorf("recovery case %s: Go outcome %#v, fixture %#v", got.ID, got, want)
		}
	}
}

func buildDatasetPurgeRecoveryCases(t *testing.T) []datasetPurgeRecoveryFixtureCase {
	t.Helper()
	return []datasetPurgeRecoveryFixtureCase{
		datasetPurgeCorruptJournalCase(t),
		datasetPurgeReplacementTrashRetryCase(t),
		datasetPurgeJournalSymlinkCase(t),
	}
}

func datasetPurgeJournalSymlinkCase(t *testing.T) datasetPurgeRecoveryFixtureCase {
	t.Helper()
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}
	initialErr := svc.DatasetPurge("orders", dataset.DefaultRetentionRule)
	if initialErr == nil || !strings.Contains(initialErr.Error(), "closed") {
		t.Fatalf("closed-sidecar journal setup error = %v", initialErr)
	}
	journalPath := filepath.Join(svc.VaultRoot, ".symdesk", "dataset-purge", "orders.json")
	targetPath := filepath.Join(filepath.Dir(journalPath), "valid-journal.json")
	if err := os.Rename(journalPath, targetPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(targetPath), journalPath); err != nil {
		t.Fatal(err)
	}
	db, err := sidecar.Open(filepath.Join(svc.VaultRoot, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	resumed := New(svc.VaultRoot, db)
	before := datasetPurgeSnapshotOf(t, svc.VaultRoot, db)
	loadErr := resumed.DatasetPurge("orders", dataset.DefaultRetentionRule)
	after := datasetPurgeSnapshotOf(t, svc.VaultRoot, db)
	const expected = "dataset purge journal is not a regular file"
	if loadErr == nil || loadErr.Error() != expected {
		t.Fatalf("symlink journal error = %v, want %q", loadErr, expected)
	}
	return datasetPurgeRecoveryFixtureCase{
		ID:     "symlink-journal-fails-before-mutation",
		Error:  expected,
		Before: before,
		After:  after,
	}
}

func datasetPurgeCorruptJournalCase(t *testing.T) datasetPurgeRecoveryFixtureCase {
	t.Helper()
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	manifest := filepath.Join(svc.VaultRoot, ".symdesk", "history", "manifest", "datasets", "orders.md.json")
	if err := os.MkdirAll(filepath.Dir(manifest), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("null"), 0o600); err != nil { //nolint:gosec // test-owned vault path
		t.Fatal(err)
	}
	before := datasetPurgeSnapshotOf(t, svc.VaultRoot, svc.DB)
	err := svc.DatasetPurge("orders", dataset.DefaultRetentionRule)
	after := datasetPurgeSnapshotOf(t, svc.VaultRoot, svc.DB)
	if err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("corrupt-journal purge error = %v", err)
	}
	return datasetPurgeRecoveryFixtureCase{ID: "corrupt-history-fails-before-mutation", Before: before, After: after, Error: "preflight"}
}

func datasetPurgeReplacementTrashRetryCase(t *testing.T) datasetPurgeRecoveryFixtureCase {
	t.Helper()
	svc := newTestService(t)
	datasetForPolicyTest(t, svc, dataset.SensitivityRestricted)
	rawRel := "datasets/orders/2026-01-04.csv"
	entry, err := svc.History.Trash(rawRel)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.Close(); err != nil {
		t.Fatal(err)
	}
	initialErr := svc.DatasetPurge("orders", dataset.DefaultRetentionRule)
	if initialErr == nil || !strings.Contains(initialErr.Error(), "closed") {
		t.Fatalf("closed-sidecar purge error = %v", initialErr)
	}
	trashPath := filepath.Join(svc.VaultRoot, ".symdesk", "trash", entry.Name)
	if err := os.WriteFile(trashPath, []byte("replacement payload"), 0o600); err != nil { //nolint:gosec // test-owned vault path
		t.Fatal(err)
	}
	db, err := sidecar.Open(filepath.Join(svc.VaultRoot, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	resumed := New(svc.VaultRoot, db)
	before := datasetPurgeSnapshotOf(t, svc.VaultRoot, db)
	retryErr := resumed.DatasetPurge("orders", dataset.DefaultRetentionRule)
	after := datasetPurgeSnapshotOf(t, svc.VaultRoot, db)
	wantError := "content changed"
	if runtime.GOOS == "windows" {
		wantError = "was replaced"
	}
	if retryErr == nil || !strings.Contains(retryErr.Error(), wantError) {
		t.Fatalf("replacement-trash retry error = %v", retryErr)
	}
	if data, err := os.ReadFile(trashPath); err != nil || string(data) != "replacement payload" { //nolint:gosec // test-owned vault path
		t.Fatalf("replacement trash changed: %q %v", data, err)
	}
	return datasetPurgeRecoveryFixtureCase{
		ID:           "replacement-trash-retry-fails-closed",
		InitialError: "closed",
		Before:       before,
		After:        after,
		Error:        "content changed",
		WindowsError: "was replaced",
	}
}

func datasetPurgeSnapshotOf(t *testing.T, root string, db *sidecar.DB) datasetPurgeSnapshot {
	t.Helper()
	files := make([]datasetPurgeFile, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "sidecar.db" || rel == "sidecar.db-wal" || rel == "sidecar.db-shm" {
			return nil
		}
		if entry.IsDir() {
			files = append(files, datasetPurgeFile{Path: rel, Kind: "directory"})
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			files = append(files, datasetPurgeFile{Path: rel, Kind: "symlink", Target: filepath.ToSlash(target)})
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // test-owned vault tree
		if err != nil {
			return err
		}
		if json.Valid(data) {
			var value interface{}
			if err := json.Unmarshal(data, &value); err != nil {
				return err
			}
			normalizeDatasetPurgeTimes(value)
			data, err = json.Marshal(value)
			if err != nil {
				return err
			}
		} else {
			data = normalizeDatasetPurgeText(data)
		}
		hash := sha256.Sum256(data)
		file := datasetPurgeFile{Path: rel, Kind: "file", Size: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
		if rel == ".symdesk/dataset-purge/orders.json" || rel == "bases/orders.md" {
			file.Content = string(data)
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	rows, err := db.DatasetRows("orders")
	if err != nil {
		t.Fatal(err)
	}
	rowStates := make([]datasetPurgeRow, 0, len(rows))
	for _, row := range rows {
		rowStates = append(rowStates, datasetPurgeRow{RowKey: row.RowKey, Identity: row.Identity, ValuesJSON: row.ValuesJSON, SourcePath: row.SourcePath, RowNumber: row.RowNumber})
	}
	return datasetPurgeSnapshot{Files: files, Rows: rowStates}
}

func normalizeDatasetPurgeTimes(value interface{}) {
	switch current := value.(type) {
	case map[string]interface{}:
		for key, child := range current {
			if key == "timestamp" || key == "deleted_at" || key == "created" || key == "imported_at" || key == "identity" || key == "payload_identity" || key == "metadata_identity" || key == "fingerprint" || key == "metadata_hash" {
				current[key] = "{{timestamp}}"
				continue
			}
			normalizeDatasetPurgeTimes(child)
		}
	case []interface{}:
		for _, child := range current {
			normalizeDatasetPurgeTimes(child)
		}
	}
}

func normalizeDatasetPurgeText(data []byte) []byte {
	lines := strings.SplitAfter(string(data), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		key, _, found := strings.Cut(trimmed, ":")
		if !found || (key != "created" && key != "imported_at") {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		ending := ""
		if strings.HasSuffix(line, "\n") {
			ending = "\n"
		}
		lines[index] = indent + key + `: "{{timestamp}}"` + ending
	}
	return []byte(strings.Join(lines, ""))
}

func equalDatasetPurgeSnapshot(left, right datasetPurgeSnapshot) bool {
	leftBytes, _ := json.Marshal(left)
	rightBytes, _ := json.Marshal(right)
	return string(leftBytes) == string(rightBytes)
}
