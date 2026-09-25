package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
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
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

const (
	portDatasetSyncServiceFixtureRel     = "testdata/port/dataset/service-sync.json"
	portDatasetSyncServiceFixtureSchema  = 1
	portDatasetSyncServiceOracleCommit   = "38891d35eb8ceb6c348eca9a78b3fb2873677e3d"
	portDatasetSyncServiceOracleRelease  = "DATA-001-service-dataset-sync-prerequisite"
	portDatasetSyncServiceGoVersion      = "go1.26.6"
	portDatasetSyncServiceModuleGo       = "1.26.6"
	portDatasetSyncServiceGenerateEnv    = "PORT_GENERATE"
	portDatasetSyncServiceFixturePathEnv = "PORT_FIXTURE_PATH"
)

type portDatasetSyncServiceFixture struct {
	SchemaVersion   int                          `json:"schema_version"`
	GeneratedOn     string                       `json:"generated_on"`
	Oracle          portDatasetSyncServiceOracle `json:"oracle"`
	GeneratorSHA256 string                       `json:"generator_sha256"`
	SourceSHA256    map[string]string            `json:"source_sha256"`
	Cases           []portDatasetSyncServiceCase `json:"cases"`
	Residuals       []string                     `json:"residuals"`
}

type portDatasetSyncServiceOracle struct {
	Commit    string `json:"commit"`
	Release   string `json:"release"`
	ModuleGo  string `json:"module_go"`
	Toolchain string `json:"toolchain"`
	GOOS      string `json:"goos"`
	GOARCH    string `json:"goarch"`
}

type portDatasetSyncServiceCase struct {
	ID          string                        `json:"id"`
	Description string                        `json:"description"`
	Calls       []portDatasetSyncServiceCall  `json:"calls"`
	States      []portDatasetSyncServiceState `json:"states"`
}

type portDatasetSyncServiceCall struct {
	Label  string             `json:"label"`
	Result *DatasetSyncResult `json:"result,omitempty"`
	Error  string             `json:"error,omitempty"`
}

type portDatasetSyncServiceState struct {
	Label       string                             `json:"label"`
	Vault       []portDatasetSyncServiceVaultEntry `json:"vault"`
	Rows        []portDatasetSyncServiceRow        `json:"rows"`
	RowsError   string                             `json:"rows_error,omitempty"`
	Handle      *dataset.Handle                    `json:"handle,omitempty"`
	HandleError string                             `json:"handle_error,omitempty"`
}

type portDatasetSyncServiceVaultEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Mode       string `json:"mode"`
	Perm       string `json:"perm"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Content    string `json:"content,omitempty"`
	ModifiedAt string `json:"modified_at,omitempty"`
}

type portDatasetSyncServiceRow struct {
	DatasetSlug string `json:"dataset_slug"`
	RowKey      string `json:"row_key"`
	Identity    string `json:"identity"`
	ValuesJSON  string `json:"values_json"`
	SourcePath  string `json:"source_path"`
	RowNumber   int    `json:"row_number"`
}

type portDatasetSyncServiceSandbox struct {
	Root string
	DB   *sidecar.DB
	Svc  *Service
}

// TestPortDatasetSyncServiceContract records the shipped Service.DatasetSync
// persistence contract. PORT_GENERATE=1 is the only mode that writes a fixture;
// a normal run recomputes the real Go behavior and compares without writing.
func TestPortDatasetSyncServiceContract(t *testing.T) {
	// Pin the fixture's Unix creation mask in a child, never process-globally:
	// sibling tests must keep the caller's original mask.
	const child = "PORT_DATASET_SYNC_SERVICE_UMASK_CHILD"
	if runtime.GOOS != "windows" && os.Getenv(child) != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", `umask 022; exec "$@"`, "dataset-sync-oracle", executable, "-test.run=^TestPortDatasetSyncServiceContract$", "-test.v", "-test.timeout=30s") //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Env = append(os.Environ(), child+"=1")
		cmd.WaitDelay = 5 * time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dataset sync oracle child: %v\n%s", err, output)
		}
		t.Logf("dataset sync oracle child:\n%s", output)
		return
	}
	fixture := portDatasetSyncServiceBuildFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := portDatasetSyncServiceFixturePath(t)
	if os.Getenv(portDatasetSyncServiceGenerateEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // explicit fixture generation path
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes, %d cases)", path, len(encoded), len(fixture.Cases))
		return
	}

	current, err := os.ReadFile(path) //nolint:gosec // fixed or explicitly supplied fixture path
	if err != nil {
		t.Fatalf("read dataset sync fixture: %v (run with %s=1 to create it)", err, portDatasetSyncServiceGenerateEnv)
	}
	if err := portDatasetSyncServiceCompare(current, encoded, runtime.GOOS); err != nil {
		t.Fatal(err)
	}
}

func TestPortDatasetSyncServiceContractCaseInventory(t *testing.T) {
	data, err := os.ReadFile(portDatasetSyncServiceFixturePath(t)) //nolint:gosec // fixed or explicitly supplied fixture path
	if err != nil {
		t.Fatal(err)
	}
	var fixture portDatasetSyncServiceFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"first-sync-typed-quoted-unicode",
		"repeated-provenance-idempotent-no-rewrite",
		"matching-handle-empty-sidecar-rebuild",
		"same-day-later-refresh-duplicate-ordering",
		"blank-title-preserves-existing-metadata",
		"representative-validation-order-before-write",
		"nonfinite-number-projection-partial-write",
		"closed-sidecar-partial-write",
		"json-unmarshal-large-integer-float64-rounding",
	}
	if len(fixture.Cases) != len(want) {
		t.Fatalf("fixture case count = %d, want %d", len(fixture.Cases), len(want))
	}
	for index, id := range want {
		if fixture.Cases[index].ID != id || len(fixture.Cases[index].Calls) == 0 {
			t.Fatalf("fixture case %d = %#v, want id %q with calls", index, fixture.Cases[index], id)
		}
	}
	if fixture.Oracle.Commit != portDatasetSyncServiceOracleCommit || fixture.Oracle.Toolchain != portDatasetSyncServiceGoVersion {
		t.Fatalf("fixture oracle identity = %#v", fixture.Oracle)
	}
	if fixture.GeneratorSHA256 != portDatasetSyncServiceSourceHash(t, "internal/service/port_dataset_sync_service_contract_test.go") {
		t.Fatal("fixture generator hash does not match the executing test source")
	}
}

func TestPortDatasetSyncServiceContractRejectsBehavioralMutation(t *testing.T) {
	data, err := os.ReadFile(portDatasetSyncServiceFixturePath(t)) //nolint:gosec // fixed or explicitly supplied fixture path
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		t.Fatal(err)
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		t.Run(goos, func(t *testing.T) {
			baseline := compact.Bytes()
			if err := portDatasetSyncServiceCompare(data, baseline, goos); err != nil {
				t.Fatalf("unmodified positive control: %v", err)
			}
			for _, mutation := range []struct{ name, old, replacement string }{
				{"row-count", `"rows":2`, `"rows":3`},
				{"missing-false", `,"idempotent":false`, ``},
				{"null-false", `"idempotent":false`, `"idempotent":null`},
				{"numeric-false", `"idempotent":false`, `"idempotent":0`},
				{"added-null", `"idempotent":false`, `"idempotent":false,"unexpected":null`},
				{"missing-platform", `"generated_on":`, `"missing_generated_on":`},
				{"inconsistent-platform", `"goos":"`, `"goos":"wrong-`},
			} {
				t.Run(mutation.name, func(t *testing.T) {
					if !bytes.Contains(baseline, []byte(mutation.old)) {
						t.Fatal("mutation did not reach its intended field")
					}
					mutated := bytes.Replace(baseline, []byte(mutation.old), []byte(mutation.replacement), 1)
					if err := portDatasetSyncServiceCompare(mutated, baseline, goos); err == nil {
						t.Fatal("real comparator accepted corrupted fixture")
					}
				})
			}
			t.Run("adjacent-large-integers", func(t *testing.T) {
				large := bytes.Replace(baseline, []byte(`"rows":2`), []byte(`"rows":9007199254740992`), 1)
				if bytes.Equal(large, baseline) {
					t.Fatal("large-integer control did not modify its intended field")
				}
				if err := portDatasetSyncServiceCompare(large, large, goos); err != nil {
					t.Fatalf("large-integer positive control: %v", err)
				}
				mutated := bytes.Replace(large, []byte(`9007199254740992`), []byte(`9007199254740993`), 1)
				if err := portDatasetSyncServiceCompare(mutated, large, goos); err == nil {
					t.Fatal("adjacent large integers collapsed to equality")
				}
			})
		})
	}
	t.Run("literal-diagnostic-backslashes", func(t *testing.T) {
		message := `duplicate dataset row identity "a\\b	"`
		for _, root := range []string{"", "unmatched-root"} {
			if got := portDatasetSyncServiceSanitise(message, root); got != message {
				t.Fatalf("diagnostic bytes changed: %q", got)
			}
		}
	})
	for _, goos := range []string{"darwin", "linux", "windows"} {
		var fixture portDatasetSyncServiceFixture
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatal(err)
		}
		fixture.GeneratedOn = "linux/amd64"
		fixture.Oracle.GOOS, fixture.Oracle.GOARCH = "linux", "amd64"
		portable, err := json.Marshal(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := portDatasetSyncServiceCompare(data, portable, goos); err != nil {
			t.Fatalf("declared platform-only normalization: %v", err)
		}
	}
}

func portDatasetSyncServiceBuildFixture(t *testing.T) portDatasetSyncServiceFixture {
	t.Helper()
	if runtime.Version() != portDatasetSyncServiceGoVersion {
		t.Fatalf("dataset sync oracle requires %s, got %s", portDatasetSyncServiceGoVersion, runtime.Version())
	}
	base, err := os.MkdirTemp("", "symdesk-port-dataset-sync-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })

	cases := []portDatasetSyncServiceCase{
		portDatasetSyncServiceTypedUnicodeCase(t, base),
		portDatasetSyncServiceIdempotentCase(t, base),
		portDatasetSyncServiceRebuildCase(t, base),
		portDatasetSyncServiceRefreshOrderingCase(t, base),
		portDatasetSyncServicePreserveMetadataCase(t, base),
		portDatasetSyncServiceValidationOrderCase(t, base),
		portDatasetSyncServiceNonfiniteCase(t, base),
		portDatasetSyncServiceClosedSidecarCase(t, base),
		portDatasetSyncServiceLargeJSONFloatCase(t, base),
	}
	wantIDs := []string{
		"first-sync-typed-quoted-unicode",
		"repeated-provenance-idempotent-no-rewrite",
		"matching-handle-empty-sidecar-rebuild",
		"same-day-later-refresh-duplicate-ordering",
		"blank-title-preserves-existing-metadata",
		"representative-validation-order-before-write",
		"nonfinite-number-projection-partial-write",
		"closed-sidecar-partial-write",
		"json-unmarshal-large-integer-float64-rounding",
	}
	if len(cases) != len(wantIDs) {
		t.Fatalf("executed %d dataset sync cases, want %d", len(cases), len(wantIDs))
	}
	for i := range cases {
		if cases[i].ID != wantIDs[i] {
			t.Fatalf("dataset sync case %d = %q, want %q", i, cases[i].ID, wantIDs[i])
		}
		if len(cases[i].Calls) == 0 {
			t.Fatalf("dataset sync case %q executed no calls", cases[i].ID)
		}
	}

	return portDatasetSyncServiceFixture{
		SchemaVersion: portDatasetSyncServiceFixtureSchema,
		GeneratedOn:   runtime.GOOS + "/" + runtime.GOARCH,
		Oracle: portDatasetSyncServiceOracle{
			Commit:    portDatasetSyncServiceOracleCommit,
			Release:   portDatasetSyncServiceOracleRelease,
			ModuleGo:  portDatasetSyncServiceModuleGo,
			Toolchain: runtime.Version(),
			GOOS:      runtime.GOOS,
			GOARCH:    runtime.GOARCH,
		},
		GeneratorSHA256: portDatasetSyncServiceSourceHash(t, "internal/service/port_dataset_sync_service_contract_test.go"),
		SourceSHA256: map[string]string{
			"cmd/symdesk/dataset.go":                      portDatasetSyncServiceSourceHash(t, "cmd/symdesk/dataset.go"),
			"go.mod":                                      portDatasetSyncServiceSourceHash(t, "go.mod"),
			"go.sum":                                      portDatasetSyncServiceSourceHash(t, "go.sum"),
			"internal/dataset/dataset.go":                 portDatasetSyncServiceSourceHash(t, "internal/dataset/dataset.go"),
			"internal/service/dataset.go":                 portDatasetSyncServiceSourceHash(t, "internal/service/dataset.go"),
			"internal/service/dataset_policy.go":          portDatasetSyncServiceSourceHash(t, "internal/service/dataset_policy.go"),
			"internal/service/dataset_additional_test.go": portDatasetSyncServiceSourceHash(t, "internal/service/dataset_additional_test.go"),
			"internal/service/datasets.go":                portDatasetSyncServiceSourceHash(t, "internal/service/datasets.go"),
			"internal/service/datasets_test.go":           portDatasetSyncServiceSourceHash(t, "internal/service/datasets_test.go"),
			"internal/sidecar/db.go":                      portDatasetSyncServiceSourceHash(t, "internal/sidecar/db.go"),
			"internal/tools/dataset_tools.go":             portDatasetSyncServiceSourceHash(t, "internal/tools/dataset_tools.go"),
			"internal/vault/assets.go":                    portDatasetSyncServiceSourceHash(t, "internal/vault/assets.go"),
			"internal/vault/root.go":                      portDatasetSyncServiceSourceHash(t, "internal/vault/root.go"),
			"internal/vault/vault.go":                     portDatasetSyncServiceSourceHash(t, "internal/vault/vault.go"),
		},
		Cases: cases,
		Residuals: []string{
			"Resource-bound, concurrent DatasetSync calls are not covered by this bounded sequential oracle.",
			"Unix captures execute with umask 0022 in an isolated child; they do not certify other creation masks or concurrent filesystem writers.",
			"Native Windows and Linux execution remains required; this fixture was generated on the recorded host and normalises only platform identity plus unavailable Windows permission bits.",
			"Whole-service regression coverage and CLI/MCP adapter parity remain separate gates; this oracle calls production Service.DatasetSync directly.",
			"This prerequisite records Go behavior only and does not approve a Rust implementation, production cutover, or Go removal.",
		},
	}
}

func portDatasetSyncServiceLargeJSONFloatCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "large-json-float64")
	var request struct {
		Rows []DatasetSyncRow `json:"rows"`
	}
	if err := json.Unmarshal([]byte(`{"rows":[{"identity":"large","values":{"id":"large","value":9007199254740993,"nested":{"value":9007199254740993}}}]}`), &request); err != nil {
		t.Fatal(err)
	}
	if number, ok := request.Rows[0].Values["value"].(float64); !ok || number != 9007199254740992 {
		t.Fatalf("Go JSON interface number = %#v, want float64 9007199254740992", request.Rows[0].Values["value"])
	}
	opts := portDatasetSyncServiceBaseOptions("large-json-float", "Large JSON Float", "2026-08-02T00:00:00Z", "large-json", "large-json-sha")
	opts.Schema = map[string]dbviews.PropertyConfig{"id": {Type: "text"}, "value": {Type: "text"}, "nested": {Type: "text"}}
	opts.Rows = request.Rows
	call := portDatasetSyncServiceInvoke("json-unmarshal-float64", sandbox.Svc, opts, sandbox.Root)
	state := portDatasetSyncServiceCaptureState(t, sandbox, "after-json-number-sync", opts.Slug, false)
	raw := portDatasetSyncServiceEntry(t, state.Vault, "datasets/large-json-float/2026-08-02.csv")
	if !strings.Contains(raw.Content, "9007199254740992") || strings.Contains(raw.Content, "9007199254740993") || !strings.Contains(raw.Content, "\"\"value\"\":9007199254740992") {
		t.Fatalf("CSV did not preserve Go float64 rounding: %q", raw.Content)
	}
	return portDatasetSyncServiceCase{
		ID:          "json-unmarshal-large-integer-float64-rounding",
		Description: "JSON decoded into map[string]interface{} rounds 9007199254740993 to float64 9007199254740992 before DatasetSync formats CSV",
		Calls:       []portDatasetSyncServiceCall{call},
		States:      []portDatasetSyncServiceState{state},
	}
}

func portDatasetSyncServiceTypedUnicodeCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "typed-unicode")
	opts := DatasetSyncOptions{
		Title:         "Unicode Ledger",
		Slug:          "unicode-ledger",
		IdentityField: "id",
		Schema: map[string]dbviews.PropertyConfig{
			"active":  {Type: "checkbox", Label: "Active"},
			"amount":  {Type: "number", Label: "Amount"},
			"id":      {Type: "text", Label: "Identifier"},
			"note":    {Type: "text", Label: "Note"},
			"payload": {Type: "text", Label: "Payload"},
			"when":    {Type: "date", Label: "When"},
		},
		Provenance:    dataset.Provenance{ImportedAt: "2026-03-01T10:11:12+02:00", SourceName: "typed-feed.csv", SourceSHA256: "typed-sha-001"},
		Sensitivity:   " confidential ",
		RetentionRule: " finance-7y ",
		Rows: []DatasetSyncRow{
			{Identity: "u-α", Values: map[string]interface{}{"id": "ignored", "active": true, "amount": 12.5, "note": "Café, \"quoted\"\nline二", "payload": []string{"x", "雪"}, "when": "2026-02-28"}},
			{Identity: "u-β", Values: map[string]interface{}{"active": false, "amount": int64(7), "note": "emoji 🚀", "payload": map[string]interface{}{"k": "値"}, "when": "2026-03-01"}},
		},
	}
	call := portDatasetSyncServiceInvoke("first", sandbox.Svc, opts, sandbox.Root)
	if call.Error != "" || call.Result == nil || call.Result.Idempotent || call.Result.Rows != 2 || call.Result.ImportedRows != 2 {
		t.Fatalf("typed unicode sync result: %#v", call)
	}
	state := portDatasetSyncServiceCaptureState(t, sandbox, "after-first", opts.Slug, false)
	raw := portDatasetSyncServiceEntry(t, state.Vault, "datasets/unicode-ledger/2026-03-01.csv")
	if !strings.Contains(raw.Content, `"Café, ""quoted""`+"\n"+`line二"`) || !strings.Contains(raw.Content, "emoji 🚀") {
		t.Fatalf("typed unicode CSV lost quoted or Unicode bytes: %q", raw.Content)
	}
	if len(state.Rows) != 2 || state.Handle == nil || state.Handle.Sensitivity != dataset.SensitivityConfidential || state.Handle.RetentionRule != "finance-7y" {
		t.Fatalf("typed unicode state: %#v", state)
	}
	return portDatasetSyncServiceCase{ID: "first-sync-typed-quoted-unicode", Description: "first successful sync persists sorted CSV columns, typed rows, quoting, Unicode, policy metadata, raw bytes and Markdown handle bytes", Calls: []portDatasetSyncServiceCall{call}, States: []portDatasetSyncServiceState{state}}
}

func portDatasetSyncServiceIdempotentCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "idempotent")
	firstOpts := portDatasetSyncServiceBaseOptions("idempotent", "Idempotent", "2026-03-02T01:02:03Z", "stable-feed", "stable-sha")
	firstOpts.Rows = []DatasetSyncRow{{Identity: "original", Values: map[string]interface{}{"id": "original", "value": "authoritative"}}}
	first := portDatasetSyncServiceInvoke("first", sandbox.Svc, firstOpts, sandbox.Root)
	if first.Error != "" || first.Result == nil {
		t.Fatalf("idempotent setup: %#v", first)
	}
	fixed := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, rel := range []string{first.Result.RawPath, first.Result.HandlePath} {
		if err := os.Chtimes(filepath.Join(sandbox.Root, filepath.FromSlash(rel)), fixed, fixed); err != nil {
			t.Fatal(err)
		}
	}
	before := portDatasetSyncServiceCaptureState(t, sandbox, "before-repeat", firstOpts.Slug, true)

	repeatOpts := firstOpts
	repeatOpts.Title = "Changed but ignored"
	repeatOpts.Provenance.ImportedAt = "2026-03-09T09:09:09Z"
	repeatOpts.Rows = []DatasetSyncRow{{Identity: "replacement", Values: map[string]interface{}{"id": "replacement", "value": "must-not-write"}}}
	repeat := portDatasetSyncServiceInvoke("same-source-and-sha-with-changed-valid-input", sandbox.Svc, repeatOpts, sandbox.Root)
	after := portDatasetSyncServiceCaptureState(t, sandbox, "after-repeat", firstOpts.Slug, true)
	if repeat.Error != "" || repeat.Result == nil || !repeat.Result.Idempotent || repeat.Result.ImportedRows != 1 {
		t.Fatalf("idempotent repeat: %#v", repeat)
	}
	if !reflect.DeepEqual(before.Vault, after.Vault) || !reflect.DeepEqual(before.Rows, after.Rows) || !reflect.DeepEqual(before.Handle, after.Handle) {
		t.Fatalf("matching provenance rewrote authoritative or derived state\nbefore=%#v\nafter=%#v", before, after)
	}
	if len(after.Rows) != 1 || after.Rows[0].Identity != "original" {
		t.Fatalf("matching provenance materialized incoming replacement: %#v", after.Rows)
	}
	return portDatasetSyncServiceCase{ID: "repeated-provenance-idempotent-no-rewrite", Description: "same source_name and source_sha256 ignores otherwise-valid changed rows and preserves authoritative file bytes, fixed mtimes, handle metadata and sidecar rows", Calls: []portDatasetSyncServiceCall{first, repeat}, States: []portDatasetSyncServiceState{before, after}}
}

func portDatasetSyncServiceRebuildCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "rebuild")
	opts := portDatasetSyncServiceBaseOptions("rebuild", "Rebuild", "2026-03-03T03:03:03Z", "rebuild-feed", "rebuild-sha")
	opts.Rows = []DatasetSyncRow{
		{Identity: "one", Values: map[string]interface{}{"id": "one", "value": "raw-one"}},
		{Identity: "two", Values: map[string]interface{}{"id": "two", "value": "raw-two"}},
	}
	first := portDatasetSyncServiceInvoke("first", sandbox.Svc, opts, sandbox.Root)
	if first.Error != "" {
		t.Fatalf("rebuild setup: %#v", first)
	}
	if err := sandbox.DB.DeleteDataset(opts.Slug); err != nil {
		t.Fatal(err)
	}
	empty := portDatasetSyncServiceCaptureState(t, sandbox, "matching-handle-empty-sidecar", opts.Slug, false)
	if len(empty.Rows) != 0 || len(empty.Vault) == 0 {
		t.Fatalf("rebuild setup did not preserve raw files with empty sidecar: %#v", empty)
	}
	repeatOpts := opts
	repeatOpts.Rows = []DatasetSyncRow{{Identity: "incoming-ignored", Values: map[string]interface{}{"id": "incoming-ignored", "value": "not-authoritative"}}}
	rebuilt := portDatasetSyncServiceInvoke("matching-handle-rebuild", sandbox.Svc, repeatOpts, sandbox.Root)
	after := portDatasetSyncServiceCaptureState(t, sandbox, "after-rebuild", opts.Slug, false)
	if rebuilt.Error != "" || rebuilt.Result == nil || !rebuilt.Result.Idempotent || rebuilt.Result.Rows != 2 || rebuilt.Result.ImportedRows != 1 {
		t.Fatalf("rebuild result: %#v", rebuilt)
	}
	if len(after.Rows) != 2 || after.Rows[0].Identity != "one" || after.Rows[1].Identity != "two" {
		t.Fatalf("rebuild did not rematerialize authoritative raw rows: %#v", after.Rows)
	}
	return portDatasetSyncServiceCase{ID: "matching-handle-empty-sidecar-rebuild", Description: "matching provenance with an empty derived sidecar rebuilds rows from existing raw CSV files and still reports idempotent", Calls: []portDatasetSyncServiceCall{first, rebuilt}, States: []portDatasetSyncServiceState{empty, after}}
}

func portDatasetSyncServiceRefreshOrderingCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "refresh-ordering")
	firstOpts := portDatasetSyncServiceBaseOptions("refresh", "Refresh", "2026-04-01T08:00:00Z", "refresh-feed", "refresh-sha-1")
	firstOpts.Rows = []DatasetSyncRow{
		{Identity: "shared", Values: map[string]interface{}{"id": "shared", "value": "first"}},
		{Identity: "b", Values: map[string]interface{}{"id": "b", "value": "first-b"}},
	}
	first := portDatasetSyncServiceInvoke("first", sandbox.Svc, firstOpts, sandbox.Root)
	firstState := portDatasetSyncServiceCaptureState(t, sandbox, "after-first", firstOpts.Slug, false)

	sameDayOpts := firstOpts
	sameDayOpts.Provenance.SourceSHA256 = "refresh-sha-2"
	sameDayOpts.Provenance.ImportedAt = "2026-04-01T17:00:00-04:00"
	sameDayOpts.Rows = []DatasetSyncRow{
		{Identity: "a", Values: map[string]interface{}{"id": "a", "value": "same-day-a"}},
		{Identity: "shared", Values: map[string]interface{}{"id": "shared", "value": "same-day-wins"}},
	}
	sameDay := portDatasetSyncServiceInvoke("same-day-refresh", sandbox.Svc, sameDayOpts, sandbox.Root)
	sameDayState := portDatasetSyncServiceCaptureState(t, sandbox, "after-same-day", firstOpts.Slug, false)

	laterOpts := firstOpts
	laterOpts.Provenance.SourceSHA256 = "refresh-sha-3"
	laterOpts.Provenance.ImportedAt = "2026-04-02T00:00:01Z"
	laterOpts.Rows = []DatasetSyncRow{
		{Identity: "shared", Values: map[string]interface{}{"id": "shared", "value": "later-wins"}},
		{Identity: "c", Values: map[string]interface{}{"id": "c", "value": "later-c"}},
	}
	later := portDatasetSyncServiceInvoke("later-date-refresh", sandbox.Svc, laterOpts, sandbox.Root)
	laterState := portDatasetSyncServiceCaptureState(t, sandbox, "after-later-date", firstOpts.Slug, false)

	for _, call := range []portDatasetSyncServiceCall{first, sameDay, later} {
		if call.Error != "" || call.Result == nil || call.Result.Idempotent {
			t.Fatalf("refresh ordering call: %#v", call)
		}
	}
	if sameDay.Result.RawPath != "datasets/refresh/2026-04-01-2.csv" || later.Result.RawPath != "datasets/refresh/2026-04-02.csv" {
		t.Fatalf("refresh raw paths: same-day=%#v later=%#v", sameDay.Result, later.Result)
	}
	wantOrder := []string{"a", "b", "c", "shared"}
	if len(laterState.Rows) != len(wantOrder) {
		t.Fatalf("later row count = %d, want %d", len(laterState.Rows), len(wantOrder))
	}
	for i, identity := range wantOrder {
		if laterState.Rows[i].Identity != identity {
			t.Fatalf("later row %d identity = %q, want %q", i, laterState.Rows[i].Identity, identity)
		}
	}
	shared := laterState.Rows[len(laterState.Rows)-1]
	if shared.SourcePath != "datasets/refresh/2026-04-02.csv" || !strings.Contains(shared.ValuesJSON, "later-wins") {
		t.Fatalf("duplicate identity did not materialize from latest sorted raw file: %#v", shared)
	}
	return portDatasetSyncServiceCase{ID: "same-day-later-refresh-duplicate-ordering", Description: "same-day refresh uses a collision suffix; later-date refresh adds another raw file; duplicate identities materialize from the last lexically read raw file and sidecar rows remain key-ordered", Calls: []portDatasetSyncServiceCall{first, sameDay, later}, States: []portDatasetSyncServiceState{firstState, sameDayState, laterState}}
}

func portDatasetSyncServicePreserveMetadataCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "preserve-metadata")
	firstOpts := portDatasetSyncServiceBaseOptions("metadata", "Original Title", "2026-05-01T00:00:00Z", "metadata-feed", "metadata-sha-1")
	firstOpts.Rows = []DatasetSyncRow{{Identity: "first", Values: map[string]interface{}{"id": "first", "value": "first"}}}
	first := portDatasetSyncServiceInvoke("first", sandbox.Svc, firstOpts, sandbox.Root)
	if first.Error != "" || first.Result == nil {
		t.Fatalf("metadata setup: %#v", first)
	}
	handle, err := readDatasetHandle(sandbox.Root, first.Result.HandlePath)
	if err != nil {
		t.Fatal(err)
	}
	handle.Created = "2024-12-31T23:59:58Z"
	handle.Coverage = dataset.Coverage{From: "2024-01-01", To: "2024-12-31"}
	handle.RefreshCommand = "symdesk dataset refresh metadata"
	encoded, err := handle.Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeDatasetFileAtomic(filepath.Join(sandbox.Root, filepath.FromSlash(first.Result.HandlePath)), encoded); err != nil {
		t.Fatal(err)
	}
	seeded := portDatasetSyncServiceCaptureState(t, sandbox, "seeded-existing-metadata", firstOpts.Slug, false)

	refreshOpts := firstOpts
	refreshOpts.Title = ""
	refreshOpts.Provenance.SourceSHA256 = "metadata-sha-2"
	refreshOpts.Provenance.ImportedAt = "2026-05-02T00:00:00Z"
	refreshOpts.Rows = []DatasetSyncRow{{Identity: "second", Values: map[string]interface{}{"id": "second", "value": "second"}}}
	refresh := portDatasetSyncServiceInvoke("blank-title-refresh", sandbox.Svc, refreshOpts, sandbox.Root)
	after := portDatasetSyncServiceCaptureState(t, sandbox, "after-blank-title-refresh", firstOpts.Slug, false)
	if refresh.Error != "" || refresh.Result == nil || after.Handle == nil {
		t.Fatalf("metadata refresh: call=%#v state=%#v", refresh, after)
	}
	if after.Handle.Title != handle.Title || after.Handle.Created != handle.Created || after.Handle.Coverage != handle.Coverage || after.Handle.RefreshCommand != handle.RefreshCommand {
		t.Fatalf("metadata was not preserved: before=%#v after=%#v", handle, after.Handle)
	}
	return portDatasetSyncServiceCase{ID: "blank-title-preserves-existing-metadata", Description: "a refresh with a blank title preserves existing title, created timestamp, coverage and refresh command while updating source and provenance", Calls: []portDatasetSyncServiceCall{first, refresh}, States: []portDatasetSyncServiceState{seeded, after}}
}

func portDatasetSyncServiceValidationOrderCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "validation-order")
	baseOpts := portDatasetSyncServiceBaseOptions("validation", "Validation", "2026-06-01T00:00:00Z", "validation-feed", "validation-sha")
	baseOpts.Rows = []DatasetSyncRow{{Identity: "one", Values: map[string]interface{}{"id": "one", "value": "valid"}}}

	invalidPolicy := baseOpts
	invalidPolicy.Sensitivity = "secret"
	invalidPolicy.IdentityField = ""
	invalidPolicy.Provenance = dataset.Provenance{}

	missingIdentity := baseOpts
	missingIdentity.IdentityField = " "
	missingIdentity.Provenance = dataset.Provenance{}

	missingProvenance := baseOpts
	missingProvenance.Provenance.SourceName = ""
	missingProvenance.Provenance.ImportedAt = "not-a-time"
	missingProvenance.Slug = "Bad Slug"

	invalidTimestamp := baseOpts
	invalidTimestamp.Provenance.ImportedAt = "not-a-time"
	invalidTimestamp.Slug = "Bad Slug"
	invalidTimestamp.Rows = nil

	unsafeSlug := baseOpts
	unsafeSlug.Slug = "Bad Slug"
	unsafeSlug.Rows = nil

	noRows := baseOpts
	noRows.Rows = nil

	blankBeforeDuplicate := baseOpts
	blankBeforeDuplicate.Rows = []DatasetSyncRow{{Identity: " ", Values: map[string]interface{}{}}, {Identity: "dup", Values: map[string]interface{}{}}, {Identity: "dup", Values: map[string]interface{}{}}}

	duplicate := baseOpts
	duplicate.Rows = []DatasetSyncRow{{Identity: "dup", Values: map[string]interface{}{}}, {Identity: "dup", Values: map[string]interface{}{}}}

	calls := []portDatasetSyncServiceCall{
		portDatasetSyncServiceInvoke("nil-service-dependencies-first", nil, baseOpts, sandbox.Root),
		portDatasetSyncServiceInvoke("invalid-policy-before-identity-and-provenance", sandbox.Svc, invalidPolicy, sandbox.Root),
		portDatasetSyncServiceInvoke("identity-before-provenance", sandbox.Svc, missingIdentity, sandbox.Root),
		portDatasetSyncServiceInvoke("provenance-before-timestamp-and-slug", sandbox.Svc, missingProvenance, sandbox.Root),
		portDatasetSyncServiceInvoke("timestamp-before-slug-and-rows", sandbox.Svc, invalidTimestamp, sandbox.Root),
		portDatasetSyncServiceInvoke("slug-before-rows", sandbox.Svc, unsafeSlug, sandbox.Root),
		portDatasetSyncServiceInvoke("rows-before-row-identity", sandbox.Svc, noRows, sandbox.Root),
		portDatasetSyncServiceInvoke("blank-identity-before-later-duplicate", sandbox.Svc, blankBeforeDuplicate, sandbox.Root),
		portDatasetSyncServiceInvoke("duplicate-identity-after-valid-identities", sandbox.Svc, duplicate, sandbox.Root),
	}
	for _, call := range calls {
		if call.Error == "" || call.Result != nil {
			t.Fatalf("validation call unexpectedly succeeded: %#v", call)
		}
	}
	state := portDatasetSyncServiceCaptureState(t, sandbox, "after-invalid-calls", baseOpts.Slug, false)
	if len(state.Vault) != 0 || len(state.Rows) != 0 || state.Handle != nil {
		t.Fatalf("pre-write validation produced side effects: %#v", state)
	}
	return portDatasetSyncServiceCase{ID: "representative-validation-order-before-write", Description: "representative invalid requests pin dependency, policy, identity, provenance, timestamp, slug, row-count and per-row identity ordering before any vault write", Calls: calls, States: []portDatasetSyncServiceState{state}}
}

func portDatasetSyncServiceNonfiniteCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "nonfinite")
	opts := portDatasetSyncServiceBaseOptions("nonfinite", "Nonfinite", "2026-07-01T00:00:00Z", "nonfinite-feed", "nonfinite-sha")
	opts.Schema = map[string]dbviews.PropertyConfig{"id": {Type: "text"}, "amount": {Type: "number"}}
	opts.Rows = []DatasetSyncRow{{Identity: "nan-row", Values: map[string]interface{}{"id": "nan-row", "amount": "NaN"}}}
	call := portDatasetSyncServiceInvoke("declared-number-string-nan", sandbox.Svc, opts, sandbox.Root)
	state := portDatasetSyncServiceCaptureState(t, sandbox, "after-projection-failure", opts.Slug, false)
	if call.Result != nil || !strings.Contains(call.Error, "json: unsupported value: NaN") {
		t.Fatalf("nonfinite projection error: %#v", call)
	}
	if len(state.Vault) != 4 || state.Handle == nil || len(state.Rows) != 0 {
		t.Fatalf("nonfinite projection did not retain raw/handle partial writes: %#v", state)
	}
	return portDatasetSyncServiceCase{ID: "nonfinite-number-projection-partial-write", Description: "JSON-representable string NaN passes CSV number parsing, then sidecar JSON projection fails after the real raw CSV and Markdown handle have been written", Calls: []portDatasetSyncServiceCall{call}, States: []portDatasetSyncServiceState{state}}
}

func portDatasetSyncServiceClosedSidecarCase(t *testing.T, base string) portDatasetSyncServiceCase {
	t.Helper()
	sandbox := portDatasetSyncServiceNewSandbox(t, base, "closed-sidecar")
	if err := sandbox.DB.Close(); err != nil {
		t.Fatal(err)
	}
	opts := portDatasetSyncServiceBaseOptions("closed-sidecar", "Closed Sidecar", "2026-08-01T00:00:00Z", "closed-feed", "closed-sha")
	opts.Rows = []DatasetSyncRow{{Identity: "one", Values: map[string]interface{}{"id": "one", "value": "written-before-db-failure"}}}
	call := portDatasetSyncServiceInvoke("closed-sidecar", sandbox.Svc, opts, sandbox.Root)
	state := portDatasetSyncServiceCaptureState(t, sandbox, "after-closed-sidecar-failure", opts.Slug, false)
	if call.Result != nil || call.Error == "" || len(state.Vault) != 4 || state.Handle == nil || state.RowsError == "" {
		t.Fatalf("closed sidecar partial-write result: call=%#v state=%#v", call, state)
	}
	return portDatasetSyncServiceCase{ID: "closed-sidecar-partial-write", Description: "a deterministically closed real sidecar fails row replacement after the raw CSV and Markdown handle have been persisted", Calls: []portDatasetSyncServiceCall{call}, States: []portDatasetSyncServiceState{state}}
}

func portDatasetSyncServiceBaseOptions(slug, title, importedAt, sourceName, sourceSHA string) DatasetSyncOptions {
	return DatasetSyncOptions{
		Title:         title,
		Slug:          slug,
		IdentityField: "id",
		Schema: map[string]dbviews.PropertyConfig{
			"id":    {Type: "text"},
			"value": {Type: "text"},
		},
		Provenance: dataset.Provenance{ImportedAt: importedAt, SourceName: sourceName, SourceSHA256: sourceSHA},
		Rows:       []DatasetSyncRow{},
	}
}

func portDatasetSyncServiceNewSandbox(t *testing.T, base, id string) portDatasetSyncServiceSandbox {
	t.Helper()
	parent := filepath.Join(base, id)
	root := filepath.Join(parent, "vault")
	state := filepath.Join(parent, "state")
	home := filepath.Join(parent, "home")
	tmp := filepath.Join(parent, "tmp")
	for _, dir := range []string{root, state, home, tmp, filepath.Join(parent, "xdg-config"), filepath.Join(parent, "xdg-data"), filepath.Join(parent, "xdg-cache")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(parent, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(parent, "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(parent, "xdg-cache"))
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	db, err := sidecar.Open(filepath.Join(state, "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return portDatasetSyncServiceSandbox{Root: canonical, DB: db, Svc: &Service{VaultRoot: canonical, DB: db}}
}

func portDatasetSyncServiceInvoke(label string, svc *Service, opts DatasetSyncOptions, root string) portDatasetSyncServiceCall {
	result, err := svc.DatasetSync(opts)
	call := portDatasetSyncServiceCall{Label: label, Result: result}
	if err != nil {
		call.Error = portDatasetSyncServiceSanitise(err.Error(), root)
	}
	return call
}

func portDatasetSyncServiceCaptureState(t *testing.T, sandbox portDatasetSyncServiceSandbox, label, slug string, includeTimes bool) portDatasetSyncServiceState {
	t.Helper()
	state := portDatasetSyncServiceState{Label: label, Vault: portDatasetSyncServiceVaultManifest(t, sandbox.Root, includeTimes), Rows: []portDatasetSyncServiceRow{}}
	rows, err := sandbox.DB.DatasetRows(slug)
	if err != nil {
		state.RowsError = portDatasetSyncServiceSanitise(err.Error(), sandbox.Root)
	} else {
		for _, row := range rows {
			state.Rows = append(state.Rows, portDatasetSyncServiceRow{DatasetSlug: row.DatasetSlug, RowKey: row.RowKey, Identity: row.Identity, ValuesJSON: row.ValuesJSON, SourcePath: filepath.ToSlash(row.SourcePath), RowNumber: row.RowNumber})
		}
	}
	handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
	handle, err := readDatasetHandle(sandbox.Root, handleRel)
	if err != nil {
		if !os.IsNotExist(err) {
			state.HandleError = portDatasetSyncServiceSanitise(err.Error(), sandbox.Root)
		}
	} else {
		state.Handle = handle
	}
	return state
}

func portDatasetSyncServiceVaultManifest(t *testing.T, root string, includeTimes bool) []portDatasetSyncServiceVaultEntry {
	t.Helper()
	entries := make([]portDatasetSyncServiceVaultEntry, 0)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
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
		info, err := entry.Info()
		if err != nil {
			return err
		}
		observed := portDatasetSyncServiceVaultEntry{
			Path: filepath.ToSlash(rel),
			Kind: "file",
			Mode: info.Mode().String(),
			Perm: fmt.Sprintf("%#o", info.Mode().Perm()),
			Size: info.Size(),
		}
		if entry.IsDir() {
			observed.Kind = "directory"
			observed.Size = 0
		} else {
			data, err := os.ReadFile(path) //nolint:gosec // walk is confined to test-owned vault
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			observed.SHA256 = hex.EncodeToString(sum[:])
			observed.Content = string(data)
			if includeTimes {
				observed.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339Nano)
			}
		}
		entries = append(entries, observed)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func portDatasetSyncServiceEntry(t *testing.T, entries []portDatasetSyncServiceVaultEntry, path string) portDatasetSyncServiceVaultEntry {
	t.Helper()
	for _, entry := range entries {
		if entry.Path == path {
			return entry
		}
	}
	t.Fatalf("vault entry %q not found in %#v", path, entries)
	return portDatasetSyncServiceVaultEntry{}
}

func portDatasetSyncServiceSanitise(message, root string) string {
	if strings.TrimSpace(root) == "" {
		return message
	}
	aliases := []string{root, filepath.Clean(root)}
	if evaluated, err := filepath.EvalSymlinks(root); err == nil {
		aliases = append(aliases, evaluated)
	}
	if strings.HasPrefix(root, "/private/var/") {
		aliases = append(aliases, strings.TrimPrefix(root, "/private"))
	} else if strings.HasPrefix(root, "/var/") {
		aliases = append(aliases, "/private"+root)
	}
	sort.Slice(aliases, func(i, j int) bool { return len(aliases[i]) > len(aliases[j]) })
	for _, alias := range aliases {
		if alias != "" {
			message = strings.ReplaceAll(message, alias, "{{VAULT}}")
		}
	}
	return message
}

func portDatasetSyncServiceFixturePath(t *testing.T) string {
	t.Helper()
	if override := strings.TrimSpace(os.Getenv(portDatasetSyncServiceFixturePathEnv)); override != "" {
		if filepath.IsAbs(override) {
			return override
		}
		absolute, err := filepath.Abs(override)
		if err != nil {
			t.Fatal(err)
		}
		return absolute
	}
	return filepath.Join(portDatasetSyncServiceRepoRoot(t), filepath.FromSlash(portDatasetSyncServiceFixtureRel))
}

func portDatasetSyncServiceRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve dataset sync oracle source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func portDatasetSyncServiceSourceHash(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(portDatasetSyncServiceRepoRoot(t), filepath.FromSlash(relative))) //nolint:gosec // fixed repository source paths
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	// The separately hashed generator is new; every recorded oracle input
	// must still match the immutable production-source pin before capture.
	if relative != "internal/service/port_dataset_sync_service_contract_test.go" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", "-C", portDatasetSyncServiceRepoRoot(t), "show", portDatasetSyncServiceOracleCommit+":"+relative) //nolint:gosec // fixed git command reads the pinned oracle source
		cmd.WaitDelay = time.Second
		pinned, err := cmd.Output()
		if err != nil {
			t.Fatalf("read pinned oracle source %s: %v", relative, err)
		}
		if !bytes.Equal(data, pinned) {
			t.Fatalf("oracle source %s differs from pinned commit %s", relative, portDatasetSyncServiceOracleCommit)
		}
	}
	return hex.EncodeToString(sum[:])
}

func portDatasetSyncServiceComparisonDocument(document []byte, goos string) ([]byte, error) {
	var shape portDatasetSyncServiceFixture
	if err := json.Unmarshal(document, &shape); err != nil {
		return nil, err
	}
	if shape.Oracle.GOOS == "" || shape.Oracle.GOARCH == "" || shape.GeneratedOn != shape.Oracle.GOOS+"/"+shape.Oracle.GOARCH {
		return nil, fmt.Errorf("missing or inconsistent dataset oracle platform")
	}
	// Keep presence, null, unknown fields and exact integer tokens. Decoding
	// back into the capture struct silently turns a missing/null bool into false.
	var parsed map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	oracle, ok := parsed["oracle"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("missing dataset oracle object")
	}
	parsed["generated_on"], oracle["goos"], oracle["goarch"] = "", "", ""
	if goos == "windows" {
		cases, ok := parsed["cases"].([]interface{})
		if !ok {
			return nil, fmt.Errorf("missing dataset cases array")
		}
		for _, value := range cases {
			item, ok := value.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid dataset case object")
			}
			states, ok := item["states"].([]interface{})
			if !ok {
				return nil, fmt.Errorf("missing dataset states array")
			}
			for _, value := range states {
				state, ok := value.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("invalid dataset state object")
				}
				vault, ok := state["vault"].([]interface{})
				if !ok {
					return nil, fmt.Errorf("missing dataset vault array")
				}
				for _, value := range vault {
					entry, ok := value.(map[string]interface{})
					if !ok {
						return nil, fmt.Errorf("invalid dataset vault entry")
					}
					for _, field := range []string{"mode", "perm"} {
						if _, ok := entry[field].(string); !ok {
							return nil, fmt.Errorf("missing dataset vault %s string", field)
						}
						entry[field] = ""
					}
				}
			}
		}
	}
	encoded, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func portDatasetSyncServiceCompare(recorded, generated []byte, goos string) error {
	want, err := portDatasetSyncServiceComparisonDocument(recorded, goos)
	if err != nil {
		return fmt.Errorf("normalise checked-in fixture: %w", err)
	}
	got, err := portDatasetSyncServiceComparisonDocument(generated, goos)
	if err != nil {
		return fmt.Errorf("normalise generated fixture: %w", err)
	}
	if !bytes.Equal(want, got) {
		index := 0
		for index < len(want) && index < len(got) && want[index] == got[index] {
			index++
		}
		start := max(0, index-60)
		return fmt.Errorf("dataset service sync fixture is stale at byte %d; recorded %q, generated %q", index, want[start:min(len(want), index+120)], got[start:min(len(got), index+120)])
	}
	return nil
}
