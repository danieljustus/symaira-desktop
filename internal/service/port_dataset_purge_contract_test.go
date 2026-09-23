package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
)

const datasetPurgeFixturePath = "../../testdata/port/dataset/purge.json"

type datasetPurgeFixture struct {
	SchemaVersion int                       `json:"schema_version"`
	Cases         []datasetPurgeFixtureCase `json:"cases"`
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
			if err := os.WriteFile(rawAbs, rawBytes, 0o600); err != nil {
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
	for i := range fixture.Cases {
		want := expected.Cases[i]
		got := fixture.Cases[i]
		if got.ID != want.ID || got.Stale != want.Stale || got.Handle != want.Handle || got.Raw != want.Raw || got.Rows != want.Rows || got.Journal != want.Journal || got.DatasetTrash != want.DatasetTrash || got.RawHistory != want.RawHistory || got.HistoryObjects != want.HistoryObjects || strings.Join(got.CheckpointPaths, "\n") != strings.Join(want.CheckpointPaths, "\n") || (want.Error != "" && !strings.Contains(got.Error, want.Error)) || (want.Error == "" && got.Error != "") {
			t.Errorf("case %s: Go outcome %#v, fixture %#v", got.ID, got, want)
		}
	}
}
