package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

const (
	portDatasetImportFixtureRel   = "testdata/port/dataset/import.json"
	portDatasetImportOracleCommit = "f7a6a9d375e24f5a1aa47f9d01f43b852c41aa3e"
	portDatasetImportGoVersion    = "go1.26.6"
)

type portDatasetImportFixture struct {
	SchemaVersion   int                     `json:"schema_version"`
	GeneratedOn     string                  `json:"generated_on"`
	Oracle          portDatasetImportOracle `json:"oracle"`
	GeneratorSHA256 string                  `json:"generator_sha256"`
	SourceSHA256    map[string]string       `json:"source_sha256"`
	Cases           []portDatasetImportCase `json:"cases"`
}

type portDatasetImportOracle struct {
	Commit    string `json:"commit"`
	Toolchain string `json:"toolchain"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

type portDatasetImportCase struct {
	ID     string                   `json:"id"`
	Calls  []portDatasetImportCall  `json:"calls"`
	States []portDatasetImportState `json:"states"`
}

type portDatasetImportCall struct {
	Label  string               `json:"label"`
	Result *DatasetImportResult `json:"result,omitempty"`
	Error  string               `json:"error,omitempty"`
}

type portDatasetImportState struct {
	Label  string                      `json:"label"`
	Vault  []portDatasetImportEntry    `json:"vault"`
	Rows   []portDatasetSyncServiceRow `json:"rows"`
	Handle *dataset.Handle             `json:"handle,omitempty"`
}

type portDatasetImportEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Size    int64  `json:"size,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Content string `json:"content,omitempty"`
}

func TestPortDatasetImportContract(t *testing.T) {
	if runtime.Version() != portDatasetImportGoVersion {
		t.Fatalf("dataset import oracle requires %s, got %s", portDatasetImportGoVersion, runtime.Version())
	}
	fixture := portDatasetImportBuildFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := portDatasetImportFixturePath(t)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dataset import fixture: %v", err)
	}
	var got, want interface{}
	if err := json.Unmarshal(encoded, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(current, &got); err != nil {
		t.Fatal(err)
	}
	for _, document := range []*interface{}{&got, &want} {
		root, ok := (*document).(map[string]interface{})
		if !ok {
			t.Fatal("dataset import fixture is not an object")
		}
		oracle, ok := root["oracle"].(map[string]interface{})
		if !ok {
			t.Fatal("dataset import fixture has no oracle object")
		}
		goos, osOK := oracle["goos"].(string)
		goarch, archOK := oracle["goarch"].(string)
		if !osOK || !archOK || goos == "" || goarch == "" || root["generated_on"] != goos+"/"+goarch {
			t.Fatal("dataset import fixture has inconsistent platform metadata")
		}
		root["generated_on"], oracle["goos"], oracle["goarch"] = "", "", ""
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatal("dataset import fixture differs from the production Go oracle")
	}
}

func TestPortDatasetImportContractCrossPlatformMetadata(t *testing.T) {
	if os.Getenv("PORT_GENERATE") == "1" {
		return
	}
	var fixture portDatasetImportFixture
	data, err := os.ReadFile(portDatasetImportFixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	fixture.GeneratedOn = "linux/amd64"
	fixture.Oracle.GOOS, fixture.Oracle.GOARCH = "linux", "amd64"
	data, err = json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "import.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PORT_DATASET_IMPORT_FIXTURE", path)
	TestPortDatasetImportContract(t)
}

func portDatasetImportBuildFixture(t *testing.T) portDatasetImportFixture {
	t.Helper()
	base, err := os.MkdirTemp("", "symdesk-port-dataset-import-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	caseRoot := filepath.Join(base, "same-day-collision")
	sandbox := portDatasetSyncServiceNewSandbox(t, caseRoot, "import")
	sourceDir := filepath.Join(caseRoot, "sources")
	if err := os.MkdirAll(sourceDir, 0o750); err != nil {
		t.Fatal(err)
	}
	firstBytes := []byte("id,amount,when\nalpha,1.25,2026-01-04\n")
	secondBytes := []byte("id,amount,when\nbeta,2.50,2026-02-06\n")
	firstPath := filepath.Join(sourceDir, "first-feed.CSV")
	secondPath := filepath.Join(sourceDir, "second-feed.csv")
	if err := os.WriteFile(firstPath, firstBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, secondBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	options := func() DatasetImportOptions {
		return DatasetImportOptions{
			Title: "Imported Ledger", Slug: "ledger", IdentityField: "id",
			Schema: map[string]dbviews.PropertyConfig{
				"amount": {Type: "number", Label: "Amount", Description: "Ledger amount", Default: "0"},
				"id":     {Type: "text", Label: "Identifier"},
				"when":   {Type: "date", Label: "When"},
			},
			Sensitivity: "confidential", RetentionRule: "finance-7y", Now: now,
		}
	}
	first := portDatasetImportInvoke("first-import", sandbox.Svc, firstPath, options(), sandbox.Root)
	firstState := portDatasetImportCapture(t, sandbox, "after-first-import", "ledger")
	second := portDatasetImportInvoke("same-day-collision", sandbox.Svc, secondPath, options(), sandbox.Root)
	secondState := portDatasetImportCapture(t, sandbox, "after-collision-import", "ledger")
	if first.Result == nil || second.Result == nil || first.Result.RawPath != "datasets/ledger/2026-02-03.csv" || second.Result.RawPath != "datasets/ledger/2026-02-03-2.csv" {
		t.Fatalf("unexpected StoreRaw collision paths: %#v %#v", first, second)
	}
	return portDatasetImportFixture{
		SchemaVersion:   1,
		GeneratedOn:     runtime.GOOS + "/" + runtime.GOARCH,
		Oracle:          portDatasetImportOracle{Commit: portDatasetImportOracleCommit, Toolchain: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		GeneratorSHA256: portDatasetImportHash(t, "internal/service/port_dataset_import_contract_test.go"),
		SourceSHA256: map[string]string{
			"internal/dataset/dataset.go":        portDatasetImportHash(t, "internal/dataset/dataset.go"),
			"internal/dbviews/views.go":          portDatasetImportHash(t, "internal/dbviews/views.go"),
			"internal/service/dataset.go":        portDatasetImportHash(t, "internal/service/dataset.go"),
			"internal/service/dataset_policy.go": portDatasetImportHash(t, "internal/service/dataset_policy.go"),
			"internal/service/datasets.go":       portDatasetImportHash(t, "internal/service/datasets.go"),
			"internal/sidecar/db.go":             portDatasetImportHash(t, "internal/sidecar/db.go"),
			"internal/vault/assets.go":           portDatasetImportHash(t, "internal/vault/assets.go"),
			"internal/vault/root.go":             portDatasetImportHash(t, "internal/vault/root.go"),
			"internal/vault/vault.go":            portDatasetImportHash(t, "internal/vault/vault.go"),
		},
		Cases: []portDatasetImportCase{{
			ID:     "same-day-source-import-collision-and-manifest-projection",
			Calls:  []portDatasetImportCall{first, second},
			States: []portDatasetImportState{firstState, secondState},
		}},
	}
}

func portDatasetImportInvoke(label string, svc *Service, source string, options DatasetImportOptions, root string) portDatasetImportCall {
	result, err := svc.DatasetImport(source, options)
	call := portDatasetImportCall{Label: label, Result: result}
	if err != nil {
		call.Error = portDatasetSyncServiceSanitise(err.Error(), root)
	}
	return call
}

func portDatasetImportCapture(t *testing.T, sandbox portDatasetSyncServiceSandbox, label, slug string) portDatasetImportState {
	t.Helper()
	state := portDatasetImportState{Label: label, Vault: []portDatasetImportEntry{}, Rows: []portDatasetSyncServiceRow{}}
	err := filepath.WalkDir(sandbox.Root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sandbox.Root {
			return nil
		}
		rel, err := filepath.Rel(sandbox.Root, path)
		if err != nil {
			return err
		}
		item := portDatasetImportEntry{Path: filepath.ToSlash(rel), Kind: "file"}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			item.Kind = "directory"
		} else {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			item.Size = int64(len(data))
			sum := sha256.Sum256(data)
			item.SHA256 = hex.EncodeToString(sum[:])
			item.Content = string(data)
		}
		_ = info
		state.Vault = append(state.Vault, item)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(state.Vault, func(i, j int) bool { return state.Vault[i].Path < state.Vault[j].Path })
	rows, err := sandbox.DB.DatasetRows(slug)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		state.Rows = append(state.Rows, portDatasetSyncServiceRow{DatasetSlug: row.DatasetSlug, RowKey: row.RowKey, Identity: row.Identity, ValuesJSON: row.ValuesJSON, SourcePath: filepath.ToSlash(row.SourcePath), RowNumber: row.RowNumber})
	}
	handle, err := readDatasetHandle(sandbox.Root, filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md")))
	if err != nil {
		t.Fatal(err)
	}
	state.Handle = handle
	return state
}

func portDatasetImportFixturePath(t *testing.T) string {
	t.Helper()
	if override := strings.TrimSpace(os.Getenv("PORT_DATASET_IMPORT_FIXTURE")); override != "" && os.Getenv("PORT_GENERATE") != "1" {
		return override
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve dataset import fixture path")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", filepath.FromSlash(portDatasetImportFixtureRel))
}

func TestPortDatasetImportGenerationUsesCanonicalFixturePath(t *testing.T) {
	t.Setenv("PORT_GENERATE", "1")
	t.Setenv("PORT_DATASET_IMPORT_FIXTURE", filepath.Join(t.TempDir(), "poison.json"))
	if got := portDatasetImportFixturePath(t); !strings.HasSuffix(got, filepath.FromSlash(portDatasetImportFixtureRel)) {
		t.Fatalf("generation path escaped canonical fixture: %s", got)
	}
}

func portDatasetImportHash(t *testing.T, relative string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve dataset import oracle root")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if relative != "internal/service/port_dataset_import_contract_test.go" {
		pinned, err := exec.Command("git", "-C", root, "show", portDatasetImportOracleCommit+":"+relative).Output()
		if err != nil {
			t.Fatalf("read pinned oracle source %s: %v", relative, err)
		}
		if !bytes.Equal(data, pinned) {
			t.Fatalf("oracle source %s differs from pinned commit %s", relative, portDatasetImportOracleCommit)
		}
	}
	return hex.EncodeToString(sum[:])
}
