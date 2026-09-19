package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestPortRetentionContract records the Go retention decision logic and its
// state files — rule and run-id validation, selector matching, reference dates,
// expiry evaluation, proposals and the idempotent history log — as a
// differential fixture (contract row VAULT-006). crates/symdesk-vault replays
// it in `retention.rs`.
//
// Set PORT_GENERATE=1 to rewrite the fixture; a normal run verifies the
// checked-in fixture against the Go implementation.
const (
	retentionFixturePath   = "testdata/port/vault/retention.json"
	retentionFixtureSchema = 1

	// The oracle is pinned in docs/rust-port/architecture.md.
	retentionOracleCommit  = "745c08e8b9d1b1a9cbb2e0ba1c1d0d0d5c0f0f7a"
	retentionOracleRelease = "v0.1.0"
)

type retentionRuleVector struct {
	ID     string `json:"id"`
	Rule   Rule   `json:"rule"`
	Error  string `json:"error"`
	Period int    `json:"period_days_resolved"`
}

type retentionRunIDVector struct {
	ID    string `json:"id"`
	RunID string `json:"run_id"`
	Error string `json:"error"`
}

type retentionMatchVector struct {
	ID       string   `json:"id"`
	Selector Selector `json:"selector"`
	Doc      DocMeta  `json:"doc"`
	Matched  bool     `json:"matched"`
}

type retentionReferenceVector struct {
	ID    string  `json:"id"`
	Doc   DocMeta `json:"doc"`
	Field string  `json:"field"`
	Date  string  `json:"date"`
	OK    bool    `json:"ok"`
}

type retentionEvaluationVector struct {
	ID    string         `json:"id"`
	Rule  Rule           `json:"rule"`
	Docs  []DocMeta      `json:"docs"`
	Now   string         `json:"now"`
	Items []ProposalItem `json:"items"`
}

type retentionFileVector struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Paths       []string `json:"paths"`
	Content     string   `json:"content"`
	Size        int      `json:"size"`
	SHA256      string   `json:"sha256"`
	Mode        *int     `json:"mode"`
	Error       string   `json:"error,omitempty"`
	ErrorClass  string   `json:"error_class,omitempty"`
	Loaded      string   `json:"loaded"`
}

type retentionActionIDVector struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	ItemIndex int    `json:"item_index"`
	ActionID  string `json:"action_id"`
}

type retentionFixture struct {
	SchemaVersion int                         `json:"schema_version"`
	GeneratedOn   string                      `json:"generated_on"`
	Oracle        retentionOracle             `json:"oracle"`
	SourceHashes  map[string]string           `json:"source_hashes"`
	ProposalDir   string                      `json:"proposal_dir"`
	HistoryPath   string                      `json:"history_path"`
	Rules         []retentionRuleVector       `json:"rules"`
	RunIDs        []retentionRunIDVector      `json:"run_ids"`
	Matches       []retentionMatchVector      `json:"matches"`
	References    []retentionReferenceVector  `json:"references"`
	Evaluations   []retentionEvaluationVector `json:"evaluations"`
	Proposals     []retentionFileVector       `json:"proposals"`
	History       []retentionFileVector       `json:"history"`
	ActionIDs     []retentionActionIDVector   `json:"action_ids"`
	Notes         []string                    `json:"notes"`
}

type retentionOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

func TestPortRetentionContract(t *testing.T) {
	fixture := buildRetentionFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Join(portRepoRoot(t), retentionFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // fixture path is derived from the repository root
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", retentionFixturePath, len(encoded))
		return
	}

	current, err := os.ReadFile(path) //nolint:gosec // fixture path is derived from the repository root
	if err != nil {
		t.Fatalf("read %s: %v (run with PORT_GENERATE=1 to create it)", retentionFixturePath, err)
	}
	want, wantErr := retentionPlatformDocument(current)
	got, gotErr := retentionPlatformDocument(encoded)
	if wantErr != nil || gotErr != nil {
		t.Fatalf("normalise fixture for this platform: %v %v", wantErr, gotErr)
	}
	if string(want) != string(got) {
		t.Fatalf("retention fixture is stale; regenerate deliberately from the pinned Go oracle\n%s",
			retentionVectorDifference(want, got))
	}
}

func buildRetentionFixture(t *testing.T) retentionFixture {
	t.Helper()
	return retentionFixture{
		SchemaVersion: retentionFixtureSchema,
		GeneratedOn:   runtime.GOOS,
		Oracle:        retentionOracle{Commit: retentionOracleCommit, Release: retentionOracleRelease},
		SourceHashes: map[string]string{
			"internal/retention/retention.go": retentionFileSHA256(t, "internal/retention/retention.go"),
		},
		ProposalDir: filepath.ToSlash(ProposalDir("")),
		HistoryPath: filepath.ToSlash(HistoryPath("")),
		Rules:       retentionRuleVectors(t),
		RunIDs:      retentionRunIDVectors(),
		Matches:     retentionMatchVectors(),
		References:  retentionReferenceVectors(),
		Evaluations: retentionEvaluationVectors(),
		Proposals:   retentionProposalVectors(t),
		History:     retentionHistoryVectors(t),
		ActionIDs:   retentionActionIDVectors(),
		Notes: []string{
			"Generated by internal/retention/port_retention_contract_test.go; never hand-edit.",
			"Proposal and history files are the exact bytes Go writes: two-space indent, no trailing newline.",
			"Evaluate keeps an item whose expiry equals 'now' and skips documents without a parsable reference date.",
			"LoadRules is not covered yet: the YAML multi-document reader needs a Rust YAML parser and belongs to the CLI slice.",
			"DocMetaFromDocument is not covered yet: it needs the vault document model and belongs to the document slice.",
		},
	}
}

func retentionRuleVectors(t *testing.T) []retentionRuleVector {
	t.Helper()
	cases := []struct {
		id   string
		rule Rule
	}{
		{"valid-trash", Rule{Name: "old-receipts", PeriodDays: 365, ReferenceField: "document_date", Action: ActionTrash}},
		{"valid-flag-review", Rule{Name: "review", PeriodDays: 30, ReferenceField: "created", Action: ActionFlagReview}},
		{"valid-due-date", Rule{Name: "overdue", PeriodDays: 1, ReferenceField: "due_date", Action: ActionTrash}},
		{"default-reference-field", Rule{Name: "defaults", PeriodDays: 7, Action: ActionFlagReview}},
		{"missing-name", Rule{Name: "   ", PeriodDays: 30, Action: ActionTrash}},
		{"zero-period", Rule{Name: "zero", PeriodDays: 0, ReferenceField: "document_date", Action: ActionTrash}},
		{"negative-period", Rule{Name: "negative", PeriodDays: -5, ReferenceField: "document_date", Action: ActionTrash}},
		{"unknown-reference-field", Rule{Name: "odd", PeriodDays: 30, ReferenceField: "invoice_date", Action: ActionTrash}},
		{"unknown-action", Rule{Name: "move", PeriodDays: 30, ReferenceField: "document_date", Action: Action("move")}},
		{"empty-action", Rule{Name: "noop", PeriodDays: 30, ReferenceField: "document_date"}},
	}

	out := make([]retentionRuleVector, 0, len(cases))
	for _, item := range cases {
		err := Validate(item.rule)
		period := 0
		if err == nil {
			period = int(time.Duration(item.rule.PeriodDays) * 24 * time.Hour / time.Hour / 24)
		}
		message, _ := retentionError(err)
		out = append(out, retentionRuleVector{
			ID:     item.id,
			Rule:   item.rule,
			Error:  message,
			Period: period,
		})
	}
	return out
}

func retentionRunIDVectors() []retentionRunIDVector {
	runIDs := []struct {
		id    string
		runID string
	}{
		{"valid", "run-20260918"},
		{"valid-dots", "run.2026.09.18"},
		{"empty", ""},
		{"blank", "   "},
		{"dot", "."},
		{"dotdot", ".."},
		{"slash", "run/child"},
		{"backslash", `run\child`},
		{"control-character", "run\u0001id"},
		{"windows-forbidden-lt", "run<id"},
		{"windows-forbidden-gt", "run>id"},
		{"windows-forbidden-colon", "run:id"},
		{"windows-forbidden-quote", `run"id`},
		{"windows-forbidden-pipe", "run|id"},
		{"windows-forbidden-question", "run?id"},
		{"windows-forbidden-star", "run*id"},
		{"trailing-dot", "run."},
		{"trailing-space", "run "},
		{"reserved-con", "CON"},
		{"reserved-con-extension", "con.json"},
		{"reserved-com1", "COM1"},
		{"reserved-lpt9", "LPT9"},
		{"not-reserved-com0", "COM0"},
		{"not-reserved-lpt10", "LPT10"},
		{"not-reserved-nullish", "NULX"},
	}
	out := make([]retentionRunIDVector, 0, len(runIDs))
	for _, item := range runIDs {
		message, _ := retentionError(ValidateRunID(item.runID))
		out = append(out, retentionRunIDVector{
			ID:    item.id,
			RunID: item.runID,
			Error: message,
		})
	}
	return out
}

func retentionMatchVectors() []retentionMatchVector {
	type matchCase struct {
		id       string
		selector Selector
		doc      DocMeta
	}
	base := DocMeta{
		Path:          "notes/receipt.md",
		Title:         "Receipt",
		DocumentDate:  "2024-01-15",
		Created:       "2024-01-15T10:00:00Z",
		DueDate:       "2024-02-15",
		Status:        "final",
		Correspondent: "Acme",
		DocumentType:  "invoice",
		Person:        "Daniel",
		Tags:          []string{"Finance", "2024"},
	}
	cases := []matchCase{
		{"empty-selector", Selector{}, base},
		{"document-type-exact", Selector{DocumentType: "invoice"}, base},
		{"document-type-case-insensitive", Selector{DocumentType: "INVOICE"}, base},
		{"document-type-mismatch", Selector{DocumentType: "receipt"}, base},
		{"status-mismatch", Selector{Status: "draft"}, base},
		{"person-match", Selector{Person: "daniel"}, base},
		{"correspondent-match", Selector{Correspondent: "ACME"}, base},
		{"tags-subset", Selector{Tags: []string{"finance"}}, base},
		{"tags-all-required", Selector{Tags: []string{"finance", "2024"}}, base},
		{"tags-one-missing", Selector{Tags: []string{"finance", "2025"}}, base},
		{"empty-document", Selector{}, DocMeta{Path: "notes/empty.md"}},
		{"empty-document-with-selector", Selector{Status: "final"}, DocMeta{Path: "notes/empty.md"}},
		{"category-is-ignored", Selector{Category: "anything"}, base},
	}
	out := make([]retentionMatchVector, 0, len(cases))
	for _, item := range cases {
		out = append(out, retentionMatchVector{
			ID:       item.id,
			Selector: item.selector,
			Doc:      item.doc,
			Matched:  item.selector.Matches(item.doc),
		})
	}
	return out
}

func retentionReferenceVectors() []retentionReferenceVector {
	cases := []struct {
		id    string
		doc   DocMeta
		field string
	}{
		{"document-date", DocMeta{DocumentDate: "2024-03-04"}, "document_date"},
		{"created-rfc3339", DocMeta{Created: "2024-03-04T05:06:07Z"}, "created"},
		{"created-offset", DocMeta{Created: "2024-03-04T05:06:07+02:00"}, "created"},
		{"due-date-date-only", DocMeta{DueDate: "2024-03-04"}, "due_date"},
		{"missing-value", DocMeta{}, "document_date"},
		{"unparsable-value", DocMeta{DocumentDate: "04.03.2024"}, "document_date"},
		{"unknown-field", DocMeta{DocumentDate: "2024-03-04"}, "invoice_date"},
		{"date-with-time-not-rfc3339", DocMeta{DocumentDate: "2024-03-04T05:06:07"}, "document_date"},
	}
	out := make([]retentionReferenceVector, 0, len(cases))
	for _, item := range cases {
		parsed, ok := item.doc.ReferenceDate(item.field)
		date := ""
		if ok {
			date = parsed.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, retentionReferenceVector{
			ID:    item.id,
			Doc:   item.doc,
			Field: item.field,
			Date:  date,
			OK:    ok,
		})
	}
	return out
}

func retentionEvaluationVectors() []retentionEvaluationVector {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	docs := []DocMeta{
		{Path: "notes/old.md", Title: "Old", DocumentDate: "2024-01-01", Status: "final", DocumentType: "invoice", Tags: []string{"finance"}},
		{Path: "notes/fresh.md", Title: "Fresh", DocumentDate: "2026-09-01", Status: "final", DocumentType: "invoice", Tags: []string{"finance"}},
		{Path: "notes/undated.md", Title: "Undated", Status: "final", DocumentType: "invoice"},
		{Path: "notes/broken.md", Title: "Broken", DocumentDate: "not-a-date", Status: "final", DocumentType: "invoice"},
		{Path: "notes/other.md", Title: "Other", DocumentDate: "2024-01-01", Status: "final", DocumentType: "receipt"},
		{Path: "notes/exact.md", Title: "Exact", DocumentDate: "2025-09-18", Status: "final", DocumentType: "invoice"},
	}
	cases := []struct {
		id   string
		rule Rule
		docs []DocMeta
	}{
		{
			"expired-invoices",
			Rule{Name: "old-invoices", PeriodDays: 365, ReferenceField: "document_date", Action: ActionTrash},
			docs,
		},
		{
			"selector-narrows",
			Rule{Name: "old-invoices", PeriodDays: 365, ReferenceField: "document_date", Action: ActionTrash,
				Selector: Selector{DocumentType: "receipt"}},
			docs,
		},
		{
			"expiry-equals-now",
			Rule{Name: "exact", PeriodDays: 365, ReferenceField: "document_date", Action: ActionFlagReview},
			[]DocMeta{{Path: "notes/exact.md", Title: "Exact", DocumentDate: "2025-09-18"}},
		},
		{
			"default-reference-field",
			Rule{Name: "defaults", PeriodDays: 30, Action: ActionFlagReview},
			[]DocMeta{{Path: "notes/created.md", Title: "Created", Created: "2026-01-01T00:00:00Z"}},
		},
		{
			"no-matches",
			Rule{Name: "none", PeriodDays: 3650, ReferenceField: "document_date", Action: ActionTrash},
			docs,
		},
	}
	out := make([]retentionEvaluationVector, 0, len(cases))
	for _, item := range cases {
		rule := item.rule
		rule.Period = time.Duration(rule.PeriodDays) * 24 * time.Hour
		items := Evaluate(rule, item.docs, now)
		if items == nil {
			items = []ProposalItem{}
		}
		out = append(out, retentionEvaluationVector{
			ID:    item.id,
			Rule:  rule,
			Docs:  item.docs,
			Now:   now.Format(time.RFC3339),
			Items: items,
		})
	}
	return out
}

// retentionProposalVectors writes real proposals through the atomic writer and
// records the resulting file, the round trip and the failure cases.
func retentionProposalVectors(t *testing.T) []retentionFileVector {
	t.Helper()
	root := newRetentionTempDir(t, "symdesk-port-retention-")
	created := time.Date(2026, 9, 18, 12, 30, 45, 0, time.UTC)

	proposal := Proposal{
		RunID:    "run-20260918",
		RuleName: "old-receipts",
		Created:  created,
		Status:   ProposalStatusPending,
		Items: []ProposalItem{
			{
				Path:          "notes/old.md",
				Title:         "Old",
				ReferenceDate: "2024-01-01",
				ExpiresAt:     "2025-01-01",
				Action:        ActionTrash,
				RuleName:      "old-receipts",
				Fingerprint:   "sha256:abc",
			},
			{
				Path:          "notes/old2.md",
				Title:         "Old 2",
				ReferenceDate: "2024-02-01",
				ExpiresAt:     "2025-02-01",
				Action:        ActionTrash,
				RuleName:      "old-receipts",
				Fingerprint:   "sha256:def",
				Status:        ProposalItemStatusAccepted,
				Failure:       "none",
			},
		},
	}
	if err := WriteProposal(root, proposal); err != nil {
		t.Fatal(err)
	}

	out := []retentionFileVector{
		retentionFileVectorFor(t, root, "write-proposal", "a proposal is written as indented JSON through the atomic writer",
			[]string{filepath.ToSlash(filepath.Join(ProposalDir(root), "run-20260918.json"))}, "", nil),
	}

	loaded, err := LoadProposal(root, "run-20260918")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, retentionFileVector{
		ID:          "load-proposal",
		Description: "loading returns the written proposal",
		Paths:       []string{},
		Content:     "",
		Size:        0,
		SHA256:      "",
		Mode:        nil,
		Loaded:      string(encoded),
	})

	_, err = LoadProposal(root, "missing-run")
	message, class := retentionError(err)
	out = append(out, retentionFileVector{
		ID:          "load-missing-proposal",
		Description: "loading an unknown run fails",
		Paths:       []string{},
		Error:       message,
		ErrorClass:  class,
	})

	err = WriteProposal(root, Proposal{RunID: "../escape", Status: ProposalStatusPending})
	message, class = retentionError(err)
	out = append(out, retentionFileVector{
		ID:          "write-invalid-run-id",
		Description: "a run id that is not a single safe filename is rejected before writing",
		Paths:       []string{},
		Error:       message,
		ErrorClass:  class,
	})
	return out
}

// retentionHistoryVectors exercises the append log: order, idempotency by
// action id, the older entry format and the reader's error cases.
func retentionHistoryVectors(t *testing.T) []retentionFileVector {
	t.Helper()
	root := newRetentionTempDir(t, "symdesk-port-retention-history-")
	stamp := time.Date(2026, 9, 18, 12, 30, 45, 0, time.UTC)
	out := []retentionFileVector{}

	missing, err := LoadHistory(root)
	message, class := retentionError(err)
	out = append(out, retentionFileVector{
		ID:          "load-missing-history",
		Description: "a vault without a history file reports no entries and no error",
		Paths:       []string{},
		Loaded:      retentionJSON(missing),
		Error:       message,
		ErrorClass:  class,
	})

	legacy := HistoryEntry{
		Timestamp: stamp,
		RuleName:  "legacy",
		Action:    ActionTrash,
		Path:      "notes/legacy.md",
		Title:     "Legacy",
	}
	modern := HistoryEntry{
		ActionID:  StableActionID("run-20260918", 0),
		Timestamp: stamp.Add(time.Minute),
		RuleName:  "old-receipts",
		Action:    ActionTrash,
		Path:      "notes/old.md",
		Title:     "Old",
	}
	retry := modern
	retry.Timestamp = stamp.Add(2 * time.Minute)

	if err := AppendHistory(root, legacy); err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(root, modern); err != nil {
		t.Fatal(err)
	}
	if err := AppendHistory(root, retry); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadHistory(root)
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, retentionFileVectorFor(t, root, "append-and-deduplicate",
		"a retried action id is not appended twice, the older entry format still appends",
		[]string{HistoryPath(root)}, retentionJSON(entries), nil))

	nullRoot := newRetentionTempDir(t, "symdesk-port-retention-null-")
	//nolint:gosec // the state directory mirrors what the Go writer creates
	if err := os.MkdirAll(ProposalDir(nullRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // the corrupt state file is written on purpose
	if err := os.WriteFile(HistoryPath(nullRoot), []byte("null"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadHistory(nullRoot)
	message, class = retentionError(err)
	out = append(out, retentionFileVector{
		ID:          "load-null-history",
		Description: "a null history document is rejected instead of silently treated as empty",
		Paths:       []string{},
		Error:       message,
		ErrorClass:  class,
	})

	objectRoot := newRetentionTempDir(t, "symdesk-port-retention-object-")
	//nolint:gosec // the state directory mirrors what the Go writer creates
	if err := os.MkdirAll(ProposalDir(objectRoot), 0o755); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // the corrupt state file is written on purpose
	if err := os.WriteFile(HistoryPath(objectRoot), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = LoadHistory(objectRoot)
	message, class = retentionError(err)
	out = append(out, retentionFileVector{
		ID:          "load-object-history",
		Description: "an object where the history array belongs is rejected",
		Paths:       []string{},
		Error:       message,
		ErrorClass:  class,
	})
	return out
}

func retentionActionIDVectors() []retentionActionIDVector {
	cases := []struct {
		id    string
		runID string
		index int
	}{
		{"first", "run-20260918", 0},
		{"second", "run-20260918", 1},
		{"large-index", "run-20260918", 42},
		{"other-run", "run-20260919", 0},
	}
	out := make([]retentionActionIDVector, 0, len(cases))
	for _, item := range cases {
		out = append(out, retentionActionIDVector{
			ID:        item.id,
			RunID:     item.runID,
			ItemIndex: item.index,
			ActionID:  StableActionID(item.runID, item.index),
		})
	}
	return out
}

// retentionFileVectorFor records the byte-level state of the named files.
func retentionFileVectorFor(t *testing.T, root, id, description string, absPaths []string, loaded string, err error) retentionFileVector {
	t.Helper()
	message, class := retentionError(err)
	vector := retentionFileVector{
		ID:          id,
		Description: description,
		Paths:       []string{},
		Loaded:      loaded,
		Error:       message,
		ErrorClass:  class,
	}
	for _, abs := range absPaths {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			t.Fatal(err)
		}
		vector.Paths = append(vector.Paths, filepath.ToSlash(rel))
	}
	if len(absPaths) == 1 {
		data, err := os.ReadFile(absPaths[0]) //nolint:gosec // path is built from the harness root
		if err == nil {
			vector.Content = string(data)
			vector.Size = len(data)
			vector.SHA256 = retentionSHA256(data)
		}
		vector.Mode = retentionMode(t, absPaths[0])
	}
	return vector
}

func retentionJSON(value any) string {
	if value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "error: " + err.Error()
	}
	return string(encoded)
}

// retentionError splits a Go error into the part the port must reproduce
// byte-for-byte and a class for failures whose wording is language-specific
// (JSON decoder text, OS errno text): there the contract is that the operation
// fails, not how the stdlib spells it.
func retentionError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "json:"):
		return "", "decode_failed"
	case strings.HasPrefix(message, "open "):
		return "", "read_failed"
	default:
		return message, "validation"
	}
}

func retentionMode(t *testing.T, path string) *int {
	t.Helper()
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := int(info.Mode().Perm())
	return &mode
}

func retentionSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func retentionFileSHA256(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(portRepoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return retentionSHA256(data)
}

func portRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root (go.mod) not found")
		}
		dir = parent
	}
}

// newRetentionTempDir avoids t.TempDir: the vault store caches an os.Root that
// Go never closes, so on Windows the directory handle can still be open when
// t.TempDir's cleanup runs (#964).
func newRetentionTempDir(t *testing.T, prefix string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// retentionPlatformDocument prepares both sides of the drift check: the
// generating platform is metadata and the Unix-only file modes cannot be
// observed elsewhere.
func retentionPlatformDocument(document []byte) ([]byte, error) {
	var parsed retentionFixture
	if err := json.Unmarshal(document, &parsed); err != nil {
		return nil, err
	}
	parsed.GeneratedOn = ""
	if runtime.GOOS == "windows" {
		for _, section := range [][]retentionFileVector{parsed.Proposals, parsed.History} {
			for i := range section {
				section[i].Mode = nil
			}
		}
	}
	encoded, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// retentionVectorDifference attributes a drift to one vector so a stale fixture
// is actionable.
func retentionVectorDifference(want, got []byte) string {
	var wantDoc, gotDoc map[string]json.RawMessage
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		return err.Error()
	}
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		return err.Error()
	}
	for _, section := range []string{"rules", "run_ids", "matches", "references", "evaluations", "proposals", "history", "action_ids"} {
		var wantItems, gotItems []map[string]any
		if err := json.Unmarshal(wantDoc[section], &wantItems); err != nil {
			return fmt.Sprintf("%s: %v", section, err)
		}
		if err := json.Unmarshal(gotDoc[section], &gotItems); err != nil {
			return fmt.Sprintf("%s: %v", section, err)
		}
		gotByID := make(map[string]any, len(gotItems))
		for _, item := range gotItems {
			gotByID[fmt.Sprint(item["id"])] = item
		}
		for _, item := range wantItems {
			id := fmt.Sprint(item["id"])
			counterpart, ok := gotByID[id]
			if !ok {
				return fmt.Sprintf("%s: fixture vector %q is missing from the generated document", section, id)
			}
			left, _ := json.Marshal(item)
			right, _ := json.Marshal(counterpart)
			if string(left) != string(right) {
				return fmt.Sprintf("%s: vector %q differs\n  fixture:   %s\n  generated: %s", section, id, left, right)
			}
		}
	}
	for _, section := range []string{"oracle", "source_hashes", "proposal_dir", "history_path", "notes"} {
		if string(wantDoc[section]) != string(gotDoc[section]) {
			return fmt.Sprintf("%s differs\n  fixture:   %s\n  generated: %s", section, wantDoc[section], gotDoc[section])
		}
	}
	return "vectors are equal; the surrounding document differs"
}
