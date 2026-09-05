// Package retention defines a rule model for scheduled document lifecycle
// actions. Rules select documents by contract metadata fields and act after a
// defined period. Actions are always staged for review before any file is
// moved, reusing the recipes manifest pattern so nothing is destroyed
// unattended.
package retention

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Rule defines a retention policy.
type Rule struct {
	// Name is a short, unique identifier for the rule.
	Name string `yaml:"name" json:"name"`
	// Description explains what the rule does in human-readable form.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Selector narrows which documents the rule applies to.
	Selector Selector `yaml:"selector" json:"selector"`
	// Period is how long after the document's reference date the action fires.
	Period time.Duration `yaml:"-" json:"period"`
	// PeriodDays is the YAML/JSON wire format for Period.
	PeriodDays int `yaml:"period_days" json:"period_days"`
	// ReferenceField is the date field used to compute expiry.
	ReferenceField string `yaml:"reference_field" json:"reference_field"`
	// Action is what happens when the rule fires.
	Action Action `yaml:"action" json:"action"`
}

// Selector matches documents by their contract metadata. Every non-empty field
// is ANDed together (an empty field means "match all" for that dimension).
type Selector struct {
	DocumentType  string   `yaml:"document_type,omitempty" json:"document_type,omitempty"`
	Status        string   `yaml:"status,omitempty" json:"status,omitempty"`
	Category      string   `yaml:"category,omitempty" json:"category,omitempty"`
	Person        string   `yaml:"person,omitempty" json:"person,omitempty"`
	Correspondent string   `yaml:"correspondent,omitempty" json:"correspondent,omitempty"`
	Tags          []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// Action is what the rule does when it fires.
type Action string

const (
	ActionTrash      Action = "trash"
	ActionFlagReview Action = "flag_review"
)

const (
	ProposalStatusPending  = "pending"
	ProposalStatusAccepted = "accepted"
	ProposalStatusRejected = "rejected"
	ProposalStatusFailed   = "failed"
	ProposalStatusPartial  = "partial"

	ProposalItemStatusAccepted        = "accepted"
	ProposalItemStatusActionCompleted = "action_completed"
)

var validActions = map[Action]bool{
	ActionTrash:      true,
	ActionFlagReview: true,
}

// Proposal is a staged evaluation result — nothing is moved until it is
// accepted through the recipes-style review workflow.
type Proposal struct {
	// RunID uniquely identifies this evaluation run.
	RunID string `json:"run_id"`
	// RuleName identifies which rule produced this proposal.
	RuleName string `json:"rule_name"`
	// Created is when the evaluation was run.
	Created time.Time `json:"created"`
	// Items lists the documents that matched and would be acted on.
	Items []ProposalItem `json:"items"`
	// Status is pending, accepted, rejected, failed, or partial.
	Status string `json:"status"`
}

// ProposalItem is one document matched by a retention evaluation.
type ProposalItem struct {
	// Path is the vault-relative path of the document.
	Path string `json:"path"`
	// Title is the document's title.
	Title string `json:"title"`
	// ReferenceDate is the date used to compute expiry.
	ReferenceDate string `json:"reference_date"`
	// ExpiresAt is when the document would expire.
	ExpiresAt string `json:"expires_at"`
	// Action is what the rule would do.
	Action Action `json:"action"`
	// RuleName identifies which rule matched this item.
	RuleName string `json:"rule_name,omitempty"`
	// Fingerprint binds acceptance to the authoritative state evaluated.
	Fingerprint string `json:"fingerprint,omitempty"`
	// Status is accepted after the action and remains pending when retryable.
	Status string `json:"status,omitempty"`
	// Failure is the last per-item acceptance failure, if any.
	Failure string `json:"failure,omitempty"`
}

// HistoryEntry records an executed retention action.
type HistoryEntry struct {
	// ActionID is a stable proposal-run/item identifier. Older history entries
	// may omit it; new entries use it to make retries idempotent.
	ActionID string `json:"action_id,omitempty"`
	// When the action was taken.
	Timestamp time.Time `json:"timestamp"`
	// RuleName that triggered the action.
	RuleName string `json:"rule_name"`
	// Action that was taken.
	Action Action `json:"action"`
	// Path of the affected document.
	Path string `json:"path"`
	// Title of the affected document.
	Title string `json:"title"`
}

// LoadRules reads retention rules from a YAML file (one rule per document,
// separated by ---).
func LoadRules(path string) ([]Rule, error) {
	data, err := os.ReadFile(path) //nolint:gosec // retention path is rooted in the explicitly selected vault
	if err != nil {
		return nil, err
	}
	docs := strings.Split(string(data), "\n---\n")
	var rules []Rule
	for _, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" || strings.HasPrefix(doc, "#") {
			continue
		}
		var r Rule
		if err := yaml.Unmarshal([]byte(doc), &r); err != nil {
			return nil, fmt.Errorf("parse retention rule: %w", err)
		}
		if err := Validate(r); err != nil {
			return nil, fmt.Errorf("invalid rule %q: %w", r.Name, err)
		}
		r.Period = time.Duration(r.PeriodDays) * 24 * time.Hour
		rules = append(rules, r)
	}
	return rules, nil
}

// Validate checks a single rule for correctness.
func Validate(r Rule) error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("rule name is required")
	}
	if r.PeriodDays <= 0 {
		return fmt.Errorf("period_days must be positive")
	}
	if r.ReferenceField == "" {
		r.ReferenceField = "document_date"
	}
	if !validReferenceField(r.ReferenceField) {
		return fmt.Errorf("unsupported reference field %q", r.ReferenceField)
	}
	if !validActions[r.Action] {
		return fmt.Errorf("unsupported action %q (valid: trash, flag_review)", r.Action)
	}
	return nil
}

func validReferenceField(f string) bool {
	switch f {
	case "document_date", "created", "due_date":
		return true
	}
	return false
}

// Evaluate runs a single rule against a set of document metadata rows and
// returns items that have expired. Each item carries a parsed ExpiresAt date.
func Evaluate(rule Rule, docs []DocMeta, now time.Time) []ProposalItem {
	var items []ProposalItem
	field := rule.ReferenceField
	if field == "" {
		field = "document_date"
	}
	for _, d := range docs {
		if !rule.Selector.Matches(d) {
			continue
		}
		refDate, ok := d.ReferenceDate(field)
		if !ok {
			continue
		}
		expiresAt := refDate.Add(rule.Period)
		if now.Before(expiresAt) {
			continue
		}
		items = append(items, ProposalItem{
			Path:          d.Path,
			Title:         d.Title,
			ReferenceDate: refDate.Format("2006-01-02"),
			ExpiresAt:     expiresAt.Format("2006-01-02"),
			Action:        rule.Action,
		})
	}
	return items
}

// DocMeta is a lightweight document metadata view for retention evaluation.
type DocMeta struct {
	Path          string
	Title         string
	DocumentDate  string
	Created       string
	DueDate       string
	Status        string
	Correspondent string
	DocumentType  string
	Person        string
	Tags          []string
}

// ReferenceDate returns the parsed date for the named reference field.
func (d DocMeta) ReferenceDate(field string) (time.Time, bool) {
	var raw string
	switch field {
	case "document_date":
		raw = d.DocumentDate
	case "created":
		raw = d.Created
	case "due_date":
		raw = d.DueDate
	}
	if raw == "" {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02", raw)
	if err != nil {
		// Try RFC3339 for created dates
		t, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

// Matches returns true when the selector matches the document.
func (s Selector) Matches(d DocMeta) bool {
	if s.DocumentType != "" && !strings.EqualFold(s.DocumentType, d.DocumentType) {
		return false
	}
	if s.Status != "" && !strings.EqualFold(s.Status, d.Status) {
		return false
	}
	if s.Person != "" && !strings.EqualFold(s.Person, d.Person) {
		return false
	}
	if s.Correspondent != "" && !strings.EqualFold(s.Correspondent, d.Correspondent) {
		return false
	}
	// Category is not directly available in DocMeta; it would come from frontmatter.
	// For now, skip the category check or it can be extended.
	if len(s.Tags) > 0 {
		tagSet := make(map[string]bool, len(d.Tags))
		for _, t := range d.Tags {
			tagSet[strings.ToLower(t)] = true
		}
		for _, t := range s.Tags {
			if !tagSet[strings.ToLower(t)] {
				return false
			}
		}
	}
	return true
}

// proposalDir returns the directory where proposals are stored.
func ProposalDir(vaultRoot string) string {
	return filepath.Join(vaultRoot, ".symdesk", "retention")
}

// historyPath returns the path to the retention history file.
func HistoryPath(vaultRoot string) string {
	return filepath.Join(ProposalDir(vaultRoot), "history.json")
}

// WriteProposal saves a proposal to disk atomically.
func WriteProposal(vaultRoot string, p Proposal) error {
	dir := ProposalDir(vaultRoot)
	//nolint:gosec // retention state is stored under the selected vault
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s.json", p.RunID)
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0644)
}

// LoadProposal reads a proposal from disk.
func LoadProposal(vaultRoot, runID string) (Proposal, error) {
	path := filepath.Join(ProposalDir(vaultRoot), fmt.Sprintf("%s.json", runID))
	data, err := os.ReadFile(path) //nolint:gosec // retention path is rooted in the explicitly selected vault
	if err != nil {
		return Proposal{}, err
	}
	var p Proposal
	if err := json.Unmarshal(data, &p); err != nil {
		return Proposal{}, err
	}
	return p, nil
}

// StableActionID returns the durable identifier for one proposal item.
// It is intentionally derived only from the proposal run and item index so a
// retry cannot create a second history row for the same accepted action.
func StableActionID(runID string, itemIndex int) string {
	return fmt.Sprintf("%s:%d", runID, itemIndex)
}

// AppendHistory adds an entry to the retention history log. Entries carrying
// an ActionID are idempotent, while entries from the old format remain
// appendable and readable.
func AppendHistory(vaultRoot string, entry HistoryEntry) error {
	path := HistoryPath(vaultRoot)
	//nolint:gosec // retention state is stored under the selected vault
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	entries, err := LoadHistory(vaultRoot)
	if err != nil {
		return err
	}
	if entry.ActionID != "" {
		for _, existing := range entries {
			if existing.ActionID == entry.ActionID {
				return nil
			}
		}
	}
	entries = append(entries, entry)
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0644)
}

// LoadHistory reads the retention history log.
func LoadHistory(vaultRoot string) ([]HistoryEntry, error) {
	path := HistoryPath(vaultRoot)
	data, err := os.ReadFile(path) //nolint:gosec // retention path is rooted in the explicitly selected vault
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "null" {
		return nil, fmt.Errorf("retention history must be a non-null array")
	}
	var entries []HistoryEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, err
	}
	if entries == nil {
		return nil, fmt.Errorf("retention history must be a non-null array")
	}
	return entries, nil
}

// writeFileAtomic writes a complete retention state file beside its target,
// syncs it, and renames it into place so readers never observe a partial JSON
// document after an interrupted write.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".symdesk-retention-*.tmp") //nolint:gosec // dir is rooted in the selected vault
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil { //nolint:gosec // both paths are in the selected vault state directory
		_ = os.Remove(tmpName)
		return err
	}
	// Persist the directory entry as well where the platform supports it.
	dirFile, err := os.Open(dir) //nolint:gosec // dir is rooted in the selected vault
	if err != nil {
		return err
	}
	err = dirFile.Sync()
	closeErr := dirFile.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// DocMetaFromDocument converts a vault.Document to a DocMeta for evaluation.
func DocMetaFromDocument(doc *vault.Document) DocMeta {
	return DocMeta{
		Path:          doc.Path,
		Title:         doc.Title,
		DocumentDate:  doc.DocumentDate,
		Created:       doc.Created,
		DueDate:       doc.DueDate,
		Status:        doc.Status,
		Correspondent: extractFrontmatterString(doc.Frontmatter, "correspondent"),
		DocumentType:  extractFrontmatterString(doc.Frontmatter, "document_type"),
		Person:        doc.Person,
		Tags:          doc.Tags,
	}
}

func extractFrontmatterString(fm map[string]interface{}, key string) string {
	if v, ok := fm[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
