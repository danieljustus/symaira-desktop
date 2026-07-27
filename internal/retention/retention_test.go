package retention

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSelectorMatches(t *testing.T) {
	doc := DocMeta{
		Title:         "Test Doc",
		DocumentType:  "invoice",
		Status:        "paid",
		Person:        "Alice",
		Correspondent: "Acme Corp",
		Tags:          []string{"finance", "2026"},
	}

	tests := []struct {
		name string
		sel  Selector
		want bool
	}{
		{"empty matches all", Selector{}, true},
		{"type match", Selector{DocumentType: "invoice"}, true},
		{"type mismatch", Selector{DocumentType: "receipt"}, false},
		{"status match", Selector{Status: "paid"}, true},
		{"status mismatch", Selector{Status: "open"}, false},
		{"person match", Selector{Person: "Alice"}, true},
		{"person case insensitive", Selector{Person: "alice"}, true},
		{"correspondent match", Selector{Correspondent: "Acme Corp"}, true},
		{"tag match", Selector{Tags: []string{"finance"}}, true},
		{"all tags required", Selector{Tags: []string{"finance", "2026"}}, true},
		{"missing tag", Selector{Tags: []string{"nonexistent"}}, false},
		{"combined match", Selector{DocumentType: "invoice", Status: "paid", Person: "Alice"}, true},
		{"combined mismatch", Selector{DocumentType: "invoice", Status: "open"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.sel.Matches(doc)
			if got != tt.want {
				t.Errorf("Selector.Matches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvaluate(t *testing.T) {
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rule := Rule{
		Name:           "old invoices",
		Period:         30 * 24 * time.Hour,
		ReferenceField: "document_date",
		Action:         ActionTrash,
		Selector:       Selector{DocumentType: "invoice"},
	}

	docs := []DocMeta{
		{
			Path:         "invoice-june.md",
			Title:        "June Invoice",
			DocumentDate: "2026-06-15",
			DocumentType: "invoice",
		},
		{
			Path:         "invoice-july.md",
			Title:        "July Invoice",
			DocumentDate: "2026-07-15",
			DocumentType: "invoice",
		},
		{
			Path:         "receipt-july.md",
			Title:        "July Receipt",
			DocumentDate: "2026-06-15",
			DocumentType: "receipt",
		},
	}

	items := Evaluate(rule, docs, now)
	// only invoice-june.md should match: June 15 + 30 days = July 15, which is before Aug 1
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Path != "invoice-june.md" {
		t.Errorf("expected invoice-june.md, got %s", items[0].Path)
	}
	if items[0].Action != ActionTrash {
		t.Errorf("expected trash action, got %s", items[0].Action)
	}
}

func TestValidate(t *testing.T) {
	valid := Rule{
		Name:           "test rule",
		PeriodDays:     30,
		ReferenceField: "document_date",
		Action:         ActionTrash,
	}
	if err := Validate(valid); err != nil {
		t.Errorf("expected valid rule, got error: %v", err)
	}

	noName := Rule{PeriodDays: 30, ReferenceField: "document_date", Action: ActionTrash}
	if err := Validate(noName); err == nil {
		t.Error("expected error for missing name")
	}

	noPeriod := Rule{Name: "test", PeriodDays: 0, ReferenceField: "document_date", Action: ActionTrash}
	if err := Validate(noPeriod); err == nil {
		t.Error("expected error for zero period")
	}

	badAction := Rule{Name: "test", PeriodDays: 30, ReferenceField: "document_date", Action: "delete"}
	if err := Validate(badAction); err == nil {
		t.Error("expected error for invalid action")
	}
}

func TestProposalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := Proposal{
		RunID:    "test-123",
		RuleName: "test rule",
		Created:  time.Now().UTC(),
		Status:   "pending",
		Items: []ProposalItem{
			{Path: "doc1.md", Title: "Doc 1", ReferenceDate: "2026-01-01", ExpiresAt: "2026-02-01", Action: ActionTrash},
		},
	}

	if err := WriteProposal(dir, p); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadProposal(dir, "test-123")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RunID != p.RunID {
		t.Errorf("run ID mismatch")
	}
	if len(loaded.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(loaded.Items))
	}
}

func TestHistoryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	entry := HistoryEntry{
		Timestamp: time.Now().UTC(),
		RuleName:  "test rule",
		Action:    ActionTrash,
		Path:      "doc1.md",
		Title:     "Doc 1",
	}

	if err := AppendHistory(dir, entry); err != nil {
		t.Fatal(err)
	}

	entries, err := LoadHistory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Path != "doc1.md" {
		t.Errorf("expected doc1.md, got %s", entries[0].Path)
	}
}

func TestLoadRulesFromFile(t *testing.T) {
	dir := t.TempDir()
	yamlContent := `name: "rule1"
period_days: 30
reference_field: document_date
action: trash
selector:
  document_type: invoice
  status: paid
`
	path := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}
	rules, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].Name != "rule1" {
		t.Errorf("expected rule1, got %s", rules[0].Name)
	}
	if rules[0].PeriodDays != 30 {
		t.Errorf("expected 30 days, got %d", rules[0].PeriodDays)
	}
}

func TestDocMetaReferenceDate(t *testing.T) {
	d := DocMeta{DocumentDate: "2026-07-27"}
	tm, ok := d.ReferenceDate("document_date")
	if !ok {
		t.Fatal("expected reference date to parse")
	}
	if tm.Year() != 2026 || tm.Month() != 7 || tm.Day() != 27 {
		t.Errorf("unexpected date: %s", tm.Format("2006-01-02"))
	}

	_, ok = d.ReferenceDate("nonexistent")
	if ok {
		t.Error("expected missing reference date")
	}

	d2 := DocMeta{Created: "2026-01-15T12:00:00Z"}
	tm2, ok := d2.ReferenceDate("created")
	if !ok {
		t.Fatal("expected created date to parse RFC3339")
	}
	if tm2.Year() != 2026 {
		t.Errorf("unexpected year: %d", tm2.Year())
	}
}

// Verify JSON serialization
func TestProposalJSON(t *testing.T) {
	p := Proposal{
		RunID:    "test-123",
		RuleName: "test",
		Created:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Status:   "pending",
	}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var p2 Proposal
	if err := json.Unmarshal(data, &p2); err != nil {
		t.Fatal(err)
	}
	if p2.RunID != p.RunID {
		t.Errorf("RunID mismatch after round-trip")
	}
}
