package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/retention"
)

const (
	retentionStateFixtureRel     = "testdata/port/vault/retention-state.json"
	retentionStateFixtureSchema  = 1
	retentionStateOracleCommit   = "4c0c246bbab80d105987caf21ca16cbbdda59c89"
	retentionStateOracleRelease  = "RUST-007-authoritative-retention-state"
	retentionStateGenerateEnv    = "PORT_GENERATE"
	retentionStateFixturePathEnv = "PORT_FIXTURE_PATH"
)

type retentionStateFixture struct {
	SchemaVersion int                      `json:"schema_version"`
	GeneratedOn   string                   `json:"generated_on"`
	Oracle        retentionStateOracle     `json:"oracle"`
	SourceHashes  map[string]string        `json:"source_hashes"`
	Cases         []retentionStateCase     `json:"cases"`
	Mutations     []retentionStateMutation `json:"mutations"`
	Notes         []string                 `json:"notes"`
}

type retentionStateOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type retentionStateCase struct {
	ID          string                  `json:"id"`
	Description string                  `json:"description"`
	Path        string                  `json:"path"`
	Setup       []retentionStateSetup   `json:"setup"`
	State       *retentionStateObserved `json:"state,omitempty"`
	Error       string                  `json:"error,omitempty"`
	ErrorClass  string                  `json:"error_class,omitempty"`
	Platform    string                  `json:"platform"`
}

type retentionStateMutation struct {
	ID          string                 `json:"id"`
	Description string                 `json:"description"`
	Path        string                 `json:"path"`
	Setup       []retentionStateSetup  `json:"setup"`
	Mutation    []retentionStateSetup  `json:"mutation"`
	Before      retentionStateObserved `json:"before"`
	After       retentionStateObserved `json:"after"`
	Changed     bool                   `json:"changed"`
	Platform    string                 `json:"platform"`
}

type retentionStateSetup struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Content string `json:"content,omitempty"`
	Target  string `json:"target,omitempty"`
}

type retentionStateObserved struct {
	Meta        retention.DocMeta `json:"meta"`
	RuleName    string            `json:"rule_name"`
	Fingerprint string            `json:"fingerprint"`
	Dataset     bool              `json:"dataset"`
}

// TestPortRetentionStateContract records the production Service.RetentionState
// behavior used by retention eval and accept. Generation is explicit; a normal
// run only compares the checked-in fixture with a fresh Go execution.
func TestPortRetentionStateContract(t *testing.T) {
	fixture := buildRetentionStateFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := retentionStateFixturePath(t)
	if os.Getenv(retentionStateGenerateEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // explicit fixture generation path
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", path, len(encoded))
		return
	}

	current, err := os.ReadFile(path) //nolint:gosec // fixed or explicitly supplied fixture path
	if err != nil {
		t.Fatalf("read retention-state fixture: %v (run with %s=1 to create it)", err, retentionStateGenerateEnv)
	}
	want, err := retentionStatePlatformDocument(current, runtime.GOOS)
	if err != nil {
		t.Fatalf("normalise checked-in fixture: %v", err)
	}
	got, err := retentionStatePlatformDocument(encoded, runtime.GOOS)
	if err != nil {
		t.Fatalf("normalise generated fixture: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("retention-state fixture is stale; regenerate deliberately from the pinned Go oracle")
	}
}

func buildRetentionStateFixture(t *testing.T) retentionStateFixture {
	t.Helper()
	return retentionStateFixture{
		SchemaVersion: retentionStateFixtureSchema,
		GeneratedOn:   runtime.GOOS,
		Oracle: retentionStateOracle{
			Commit:  retentionStateOracleCommit,
			Release: retentionStateOracleRelease,
		},
		SourceHashes: map[string]string{
			"internal/dataset/dataset.go":                            retentionStateSourceHash(t, "internal/dataset/dataset.go"),
			"internal/retention/fingerprint.go":                      retentionStateSourceHash(t, "internal/retention/fingerprint.go"),
			"internal/retention/retention.go":                        retentionStateSourceHash(t, "internal/retention/retention.go"),
			"internal/service/dataset_retention.go":                  retentionStateSourceHash(t, "internal/service/dataset_retention.go"),
			"internal/service/port_retention_state_contract_test.go": retentionStateSourceHash(t, "internal/service/port_retention_state_contract_test.go"),
			"internal/service/port_retention_state_unix_test.go":     retentionStateSourceHash(t, "internal/service/port_retention_state_unix_test.go"),
			"internal/service/port_retention_state_windows_test.go":  retentionStateSourceHash(t, "internal/service/port_retention_state_windows_test.go"),
			"internal/vault/root.go":                                 retentionStateSourceHash(t, "internal/vault/root.go"),
			"internal/vault/root_open_unix.go":                       retentionStateSourceHash(t, "internal/vault/root_open_unix.go"),
			"internal/vault/root_open_windows.go":                    retentionStateSourceHash(t, "internal/vault/root_open_windows.go"),
			"internal/vault/vault.go":                                retentionStateSourceHash(t, "internal/vault/vault.go"),
		},
		Cases:     retentionStateCases(t),
		Mutations: retentionStateMutations(t),
		Notes: []string{
			"Generated by internal/service/port_retention_state_contract_test.go through Service.RetentionState; never hand-edit.",
			"Every case uses a fresh disposable vault root; no operator vault, sidecar, credential store, or network service is accessed.",
			"Dataset fingerprints include the handle bytes followed by raw CSV path/content pairs sorted by path.",
			"Mutation vectors re-read production state after changing authoritative bytes and require a different fingerprint.",
			"Unix FIFO cases record the production Go filesystem diagnostics; the Rust replay preserves its relative-path Display difference as an explicit residual.",
		},
	}
}

func retentionStateCases(t *testing.T) []retentionStateCase {
	t.Helper()
	document := `---
title: Invoice 2024
created: "2024-01-02T03:04:05Z"
document_date: "2024-01-01"
due_date: "2024-02-01"
status: paid
person: Daniel
correspondent: Acme Corp
document_type: invoice
tags:
  - finance
  - tax
---
ordinary body
`
	dataset := retentionStateDatasetHandle("orders", "Orders", "seven-years")
	cases := []retentionStateCase{
		{ID: "ordinary-document", Description: "ordinary Markdown bytes and metadata", Path: "notes/invoice.md", Setup: []retentionStateSetup{{Path: "notes/invoice.md", Kind: "file", Content: document}}},
		{ID: "ordinary-nonblocking-positive-control", Description: "ordinary files retain their authoritative fingerprint with nonblocking rooted opens", Path: "probe.md", Setup: []retentionStateSetup{{Path: "probe.md", Kind: "file", Content: "---\ntitle: Probe\n---\nbody\n"}}},
		{ID: "direct-fifo-rejected", Description: "a direct FIFO is rejected without blocking before file-type validation", Path: "pipe.md", Platform: "unix", Setup: []retentionStateSetup{{Path: "pipe.md", Kind: "fifo"}}},
		{ID: "dataset-raw-fifo-rejected", Description: "a dataset raw CSV FIFO is rejected without blocking before fingerprinting", Path: "datasets/orders.md", Platform: "unix", Setup: []retentionStateSetup{
			{Path: "datasets/orders.md", Kind: "file", Content: dataset},
			{Path: "datasets/orders/pipe.csv", Kind: "fifo"},
		}},
		{ID: "dataset-sorted-raw-reverse-creation", Description: "dataset handle plus CSVs created in reverse lexical order", Path: "datasets/orders.md", Setup: []retentionStateSetup{
			{Path: "datasets/orders.md", Kind: "file", Content: dataset},
			{Path: "datasets/orders/z.csv", Kind: "file", Content: "id,total\n2,20\n"},
			{Path: "datasets/orders/a.csv", Kind: "file", Content: "id,total\n1,10\n"},
			{Path: "datasets/orders/readme.txt", Kind: "file", Content: "ignored\n"},
			{Path: "datasets/orders/nested", Kind: "directory"},
		}},
		{ID: "dataset-sorted-raw-forward-creation", Description: "creation order does not change the sorted raw-source fingerprint", Path: "datasets/orders.md", Setup: []retentionStateSetup{
			{Path: "datasets/orders.md", Kind: "file", Content: dataset},
			{Path: "datasets/orders/a.csv", Kind: "file", Content: "id,total\n1,10\n"},
			{Path: "datasets/orders/z.csv", Kind: "file", Content: "id,total\n2,20\n"},
		}},
		{ID: "empty-path", Description: "empty paths fail before filesystem access", Path: ""},
		{ID: "absolute-path", Description: "absolute paths are rejected", Path: "/outside.md", Platform: "unix"},
		{ID: "traversal-path", Description: "parent traversal is denied", Path: "../outside.md"},
		{ID: "corrupt-document", Description: "malformed frontmatter fails closed", Path: "broken.md", Setup: []retentionStateSetup{{Path: "broken.md", Kind: "file", Content: "---\ntitle: [\n---\nbody\n"}}},
		{ID: "corrupt-dataset-handle", Description: "dataset handles missing source fail closed", Path: "datasets/broken.md", Setup: []retentionStateSetup{{Path: "datasets/broken.md", Kind: "file", Content: `---
type: dataset
title: Broken
dataset_id: broken
sensitivity: restricted
retention_rule: seven-years
---
`}}},
		{ID: "dataset-handle-path-mismatch", Description: "dataset_id must match the canonical handle path", Path: "datasets/wrong.md", Setup: []retentionStateSetup{{Path: "datasets/wrong.md", Kind: "file", Content: dataset}}},
		{ID: "dataset-raw-symlink", Description: "raw CSV symlinks are never fingerprinted", Path: "datasets/orders.md", Platform: "unix", Setup: []retentionStateSetup{
			{Path: "datasets/orders.md", Kind: "file", Content: dataset},
			{Path: "datasets/orders/real.csv", Kind: "file", Content: "id,total\n1,10\n"},
			{Path: "datasets/orders/link.csv", Kind: "symlink", Target: "real.csv"},
		}},
		{ID: "document-symlink-escape", Description: "a document symlink cannot escape the vault root", Path: "escape.md", Platform: "unix", Setup: []retentionStateSetup{{Path: "escape.md", Kind: "symlink", Target: "{{OUTSIDE_FILE}}"}}},
	}
	for index := range cases {
		if cases[index].Platform == "" {
			cases[index].Platform = "any"
		}
		if cases[index].Setup == nil {
			cases[index].Setup = []retentionStateSetup{}
		}
		cases[index] = runRetentionStateCase(t, cases[index])
	}
	reverse := retentionStateCaseByID(t, cases, "dataset-sorted-raw-reverse-creation")
	forward := retentionStateCaseByID(t, cases, "dataset-sorted-raw-forward-creation")
	if reverse.State == nil || forward.State == nil || reverse.State.Fingerprint != forward.State.Fingerprint {
		t.Fatal("dataset fingerprint changed with directory creation order")
	}
	return cases
}

func retentionStateCaseByID(t *testing.T, cases []retentionStateCase, id string) retentionStateCase {
	t.Helper()
	for _, item := range cases {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("missing retention-state case %q", id)
	return retentionStateCase{}
}

func retentionStateMutations(t *testing.T) []retentionStateMutation {
	t.Helper()
	mutations := []retentionStateMutation{
		{
			ID:          "ordinary-authoritative-reread",
			Description: "changing the document bytes changes the authoritative fingerprint",
			Path:        "note.md",
			Setup:       []retentionStateSetup{{Path: "note.md", Kind: "file", Content: "---\ntitle: Before\n---\nbefore\n"}},
			Mutation:    []retentionStateSetup{{Path: "note.md", Kind: "file", Content: "---\ntitle: After\n---\nafter\n"}},
			Platform:    "any",
		},
		{
			ID:          "dataset-raw-authoritative-reread",
			Description: "changing one raw CSV changes the dataset fingerprint",
			Path:        "datasets/orders.md",
			Setup: []retentionStateSetup{
				{Path: "datasets/orders.md", Kind: "file", Content: retentionStateDatasetHandle("orders", "Orders", "seven-years")},
				{Path: "datasets/orders/a.csv", Kind: "file", Content: "id,total\n1,10\n"},
			},
			Mutation: []retentionStateSetup{{Path: "datasets/orders/a.csv", Kind: "file", Content: "id,total\n1,11\n"}},
			Platform: "any",
		},
	}
	for index := range mutations {
		mutations[index] = runRetentionStateMutation(t, mutations[index])
	}
	return mutations
}

func runRetentionStateCase(t *testing.T, spec retentionStateCase) retentionStateCase {
	t.Helper()
	if spec.Platform == "unix" && runtime.GOOS == "windows" {
		return spec
	}
	if hasRetentionStateFIFO(spec.Setup) && runtime.GOOS != "windows" && os.Getenv(retentionStateOracleCaseEnv) == "" {
		return runRetentionStateCaseBounded(t, spec)
	}
	return runRetentionStateCaseInline(t, spec)
}

const (
	retentionStateOracleCaseEnv   = "SYMDESK_RETENTION_STATE_ORACLE_CASE"
	retentionStateOracleOutputEnv = "SYMDESK_RETENTION_STATE_ORACLE_OUTPUT"
)

func hasRetentionStateFIFO(setup []retentionStateSetup) bool {
	for _, entry := range setup {
		if entry.Kind == "fifo" {
			return true
		}
	}
	return false
}

func runRetentionStateCaseBounded(t *testing.T, spec retentionStateCase) retentionStateCase {
	t.Helper()
	parent, err := os.MkdirTemp("", "symdesk-port-retention-oracle-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	outputPath := filepath.Join(parent, "result.json")
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRetentionStateOracleCaseHelper$") // #nosec G204,G702 -- current test binary and helper selector are fixed.
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, retentionStateOracleCaseEnv+"=") && !strings.HasPrefix(variable, retentionStateOracleOutputEnv+"=") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, retentionStateOracleCaseEnv+"="+string(encoded), retentionStateOracleOutputEnv+"="+outputPath)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("%s: Go Service.RetentionState exceeded 3s FIFO deadline; child output: %s", spec.ID, output.String())
		}
		t.Fatalf("%s: Go retention oracle child failed: %v\n%s", spec.ID, err, output.String())
	}
	result, err := os.ReadFile(outputPath) // #nosec G304 -- outputPath is inside the private directory from os.MkdirTemp.
	if err != nil {
		t.Fatalf("%s: read bounded Go retention oracle result: %v", spec.ID, err)
	}
	var observed retentionStateCase
	if err := json.Unmarshal(result, &observed); err != nil {
		t.Fatalf("%s: decode bounded Go retention oracle result: %v", spec.ID, err)
	}
	return observed
}

func runRetentionStateCaseInline(t *testing.T, spec retentionStateCase) retentionStateCase {
	t.Helper()
	root, outside := retentionStateSandbox(t)
	applyRetentionStateSetup(t, root, outside, spec.Setup)
	state, err := (&Service{VaultRoot: root}).RetentionState(spec.Path)
	if err != nil {
		spec.Error = sanitiseRetentionStateError(err.Error(), root, outside)
		spec.ErrorClass = retentionStateErrorClass(err)
		return spec
	}
	spec.State = observeRetentionState(state)
	return spec
}

func TestRetentionStateOracleCaseHelper(t *testing.T) {
	encoded := os.Getenv(retentionStateOracleCaseEnv)
	if encoded == "" {
		return
	}
	var spec retentionStateCase
	if err := json.Unmarshal([]byte(encoded), &spec); err != nil {
		t.Fatal(err)
	}
	observed := runRetentionStateCaseInline(t, spec)
	result, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(retentionStateOracleOutputEnv), result, 0o600); err != nil { // #nosec G703,G304 -- parent supplies its private os.MkdirTemp result path.
		t.Fatal(err)
	}
}

func runRetentionStateMutation(t *testing.T, spec retentionStateMutation) retentionStateMutation {
	t.Helper()
	root, outside := retentionStateSandbox(t)
	applyRetentionStateSetup(t, root, outside, spec.Setup)
	service := &Service{VaultRoot: root}
	before, err := service.RetentionState(spec.Path)
	if err != nil {
		t.Fatalf("%s before: %v", spec.ID, err)
	}
	applyRetentionStateSetup(t, root, outside, spec.Mutation)
	after, err := service.RetentionState(spec.Path)
	if err != nil {
		t.Fatalf("%s after: %v", spec.ID, err)
	}
	spec.Before = *observeRetentionState(before)
	spec.After = *observeRetentionState(after)
	spec.Changed = spec.Before.Fingerprint != spec.After.Fingerprint
	if !spec.Changed {
		t.Fatalf("%s did not change its authoritative fingerprint", spec.ID)
	}
	return spec
}

func observeRetentionState(state *RetentionState) *retentionStateObserved {
	if state == nil {
		return nil
	}
	return &retentionStateObserved{Meta: state.Meta, RuleName: state.RuleName, Fingerprint: state.Fingerprint, Dataset: state.Dataset}
}

func retentionStateSandbox(t *testing.T) (string, string) {
	t.Helper()
	parent, err := os.MkdirTemp("", "symdesk-port-retention-state-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	// Match the canonical root used in production filesystem diagnostics, including
	// macOS TMPDIR aliases such as /var and /private/var.
	canonical, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(canonical, "vault")
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(canonical, "outside.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, outside
}

func applyRetentionStateSetup(t *testing.T, root, outside string, entries []retentionStateSetup) {
	t.Helper()
	for _, entry := range entries {
		path := filepath.Join(root, filepath.FromSlash(entry.Path))
		switch entry.Kind {
		case "directory":
			if err := os.MkdirAll(path, 0o750); err != nil {
				t.Fatal(err)
			}
		case "symlink":
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(path)
			target := entry.Target
			if target == "{{OUTSIDE_FILE}}" {
				target = outside
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		case "fifo":
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			retentionStateCreateFIFO(t, path)
		case "file":
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(entry.Content), 0o600); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown setup kind %q", entry.Kind)
		}
	}
}

func retentionStateDatasetHandle(slug, title, rule string) string {
	return fmt.Sprintf(`---
type: dataset
title: %s
created: "2024-01-02T03:04:05Z"
dataset_id: %s
source: orders.csv
coverage:
  from: "2024-01-01"
  to: "2024-12-31"
provenance:
  imported_at: "2025-01-01T00:00:00Z"
  source_name: orders.csv
  source_sha256: abc123
sensitivity: restricted
retention_rule: %s
---

# %s
`, title, slug, rule, title)
}

func retentionStateErrorClass(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "invalid retention path"):
		return "invalid_path"
	case strings.HasPrefix(message, "path traversal denied:") || strings.HasPrefix(message, "symlink escape denied:"):
		return "unsafe_path"
	case strings.Contains(message, "dataset raw source") && strings.Contains(message, "is a symlink"):
		return "symlink"
	case strings.Contains(message, "invalid frontmatter") || strings.Contains(message, "parse dataset handle"):
		return "parse"
	case strings.Contains(message, "dataset handle") || strings.Contains(message, "does not match dataset"):
		return "dataset_contract"
	case errors.Is(err, fs.ErrNotExist):
		return "not_found"
	default:
		return "filesystem"
	}
}

func sanitiseRetentionStateError(message, root, outside string) string {
	message = strings.ReplaceAll(message, root, "{{VAULT}}")
	message = strings.ReplaceAll(message, outside, "{{OUTSIDE_FILE}}")
	return filepath.ToSlash(message)
}

func retentionStateFixturePath(t *testing.T) string {
	t.Helper()
	if override := strings.TrimSpace(os.Getenv(retentionStateFixturePathEnv)); override != "" {
		if filepath.IsAbs(override) {
			return override
		}
		absolute, err := filepath.Abs(override)
		if err != nil {
			t.Fatal(err)
		}
		return absolute
	}
	return filepath.Join(retentionStateRepoRoot(t), filepath.FromSlash(retentionStateFixtureRel))
}

func retentionStateRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve retention-state test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func retentionStateSourceHash(t *testing.T, relative string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(retentionStateRepoRoot(t), filepath.FromSlash(relative))) //nolint:gosec // fixed repository source paths
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func retentionStatePlatformDocument(document []byte, goos string) ([]byte, error) {
	var parsed retentionStateFixture
	if err := json.Unmarshal(document, &parsed); err != nil {
		return nil, err
	}
	parsed.GeneratedOn = ""
	if goos == "windows" {
		kept := make([]retentionStateCase, 0, len(parsed.Cases))
		for _, item := range parsed.Cases {
			if item.Platform != "unix" {
				kept = append(kept, item)
			}
		}
		parsed.Cases = kept
	}
	encoded, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}
