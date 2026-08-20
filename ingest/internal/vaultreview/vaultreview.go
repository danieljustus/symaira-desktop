package vaultreview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"gopkg.in/yaml.v3"
)

type Failure struct {
	File    string `json:"file"`
	Check   string `json:"check"`
	Message string `json:"message"`
}

type ValidationReport struct {
	Vault    string    `json:"vault"`
	Files    int       `json:"files"`
	Failures []Failure `json:"failures"`
}

func (r ValidationReport) OK() bool { return len(r.Failures) == 0 }

type ValidationOptions struct {
	// MinBodyLength fails notes whose extracted Markdown body is shorter than
	// this many non-whitespace bytes. Use it as an OCR/extraction quality gate
	// during Paperless cutover; keep zero for structural validation only.
	MinBodyLength int
}

func SplitFrontmatter(data []byte) (frontmatter []byte, body []byte, err error) {
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, nil, fmt.Errorf("missing YAML frontmatter delimiter")
	}
	rest := data[len("---\n"):]
	idx := bytes.Index(rest, []byte("\n---"))
	if idx < 0 {
		return nil, nil, fmt.Errorf("unterminated YAML frontmatter")
	}
	frontmatter = rest[:idx]
	after := rest[idx+len("\n---"):]
	if bytes.HasPrefix(after, []byte("\r\n")) {
		after = after[2:]
	} else if bytes.HasPrefix(after, []byte("\n")) {
		after = after[1:]
	}
	return frontmatter, after, nil
}

func parseNote(path string) (map[string]any, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	fm, body, err := SplitFrontmatter(data)
	if err != nil {
		return nil, nil, err
	}
	var meta map[string]any
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return nil, nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	if meta == nil {
		meta = map[string]any{}
	}
	return meta, body, nil
}

func ValidateVault(vault string) (*ValidationReport, error) {
	return ValidateVaultWithOptions(vault, ValidationOptions{})
}

func ValidateVaultWithOptions(vault string, opts ValidationOptions) (*ValidationReport, error) {
	r := &ValidationReport{Vault: vault}
	seenPaperless := map[int]string{}
	err := filepath.WalkDir(vault, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			r.Failures = append(r.Failures, Failure{File: path, Check: "walk", Message: err.Error()})
			return nil
		}
		if d.IsDir() || strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}
		r.Files++
		meta, body, err := parseNote(path)
		if err != nil {
			r.Failures = append(r.Failures, Failure{File: path, Check: "frontmatter", Message: err.Error()})
			return nil
		}
		validateBody(r, path, body, opts)
		validateRequired(r, path, meta)
		validateArchive(r, path, meta)
		validatePaperlessID(r, path, meta, seenPaperless)
		validateSafeTypes(r, path, meta)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

func validateBody(r *ValidationReport, path string, body []byte, opts ValidationOptions) {
	if opts.MinBodyLength <= 0 {
		return
	}
	length := len(bytes.TrimSpace(body))
	if length < opts.MinBodyLength {
		r.Failures = append(r.Failures, Failure{File: path, Check: "body.min_length", Message: fmt.Sprintf("body length %d below minimum %d", length, opts.MinBodyLength)})
	}
}

func validateRequired(r *ValidationReport, path string, meta map[string]any) {
	for _, key := range []string{"source_path", "ingested_at", "sha256", "mime"} {
		v, ok := meta[key]
		if !ok {
			r.Failures = append(r.Failures, Failure{File: path, Check: "required." + key, Message: "missing required frontmatter field"})
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			r.Failures = append(r.Failures, Failure{File: path, Check: "required." + key, Message: "required field is empty"})
		}
	}
}

func validateArchive(r *ValidationReport, path string, meta map[string]any) {
	archive, _ := meta["archive_path"].(string)
	if archive == "" {
		return
	}
	if _, err := os.Stat(archive); err != nil {
		r.Failures = append(r.Failures, Failure{File: path, Check: "archive.exists", Message: err.Error()})
		return
	}
	want, _ := meta["sha256"].(string)
	if want == "" {
		return
	}
	got, err := fileSHA256(archive)
	if err != nil {
		r.Failures = append(r.Failures, Failure{File: path, Check: "archive.hash", Message: err.Error()})
		return
	}
	if !strings.EqualFold(got, want) {
		r.Failures = append(r.Failures, Failure{File: path, Check: "archive.hash", Message: fmt.Sprintf("sha256 mismatch: frontmatter %s, archive %s", want, got)})
	}
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func validatePaperlessID(r *ValidationReport, path string, meta map[string]any, seen map[int]string) {
	id, ok := paperlessID(meta)
	if !ok {
		return
	}
	if prev := seen[id]; prev != "" {
		r.Failures = append(r.Failures, Failure{File: path, Check: "paperless.document_id.unique", Message: fmt.Sprintf("duplicate Paperless ID %d also in %s", id, prev)})
		return
	}
	seen[id] = path
}

func validateSafeTypes(r *ValidationReport, path string, meta map[string]any) {
	if tags, ok := meta["tags"]; ok {
		list, ok := tags.([]any)
		if !ok {
			r.Failures = append(r.Failures, Failure{File: path, Check: "tags.type", Message: "tags must be a YAML list of strings"})
		} else {
			for _, item := range list {
				if _, ok := item.(string); !ok {
					r.Failures = append(r.Failures, Failure{File: path, Check: "tags.type", Message: "tags must contain only strings"})
					break
				}
			}
		}
	}
	if p, ok := meta["paperless"]; ok {
		pm, ok := p.(map[string]any)
		if !ok {
			r.Failures = append(r.Failures, Failure{File: path, Check: "paperless.type", Message: "paperless must be a YAML mapping"})
			return
		}
		if v, ok := pm["document_id"]; ok {
			if _, ok := numericID(v); !ok {
				r.Failures = append(r.Failures, Failure{File: path, Check: "paperless.document_id.type", Message: "document_id must be numeric"})
			}
		}
	}
}

func paperlessID(meta map[string]any) (int, bool) {
	p, ok := meta["paperless"].(map[string]any)
	if !ok {
		return 0, false
	}
	return numericID(p["document_id"])
}

func numericID(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case uint64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	case string:
		id, err := strconv.Atoi(n)
		return id, err == nil
	}
	return 0, false
}

type Correction struct {
	PaperlessID   int      `yaml:"paperless_id" json:"paperless_id"`
	AddTags       []string `yaml:"add_tags" json:"add_tags"`
	RemoveTags    []string `yaml:"remove_tags" json:"remove_tags"`
	Correspondent *string  `yaml:"correspondent" json:"correspondent"`
	DocumentType  *string  `yaml:"document_type" json:"document_type"`
	StoragePath   *string  `yaml:"storage_path" json:"storage_path"`
}

type CorrectionsFile struct {
	SchemaVersion int          `yaml:"schema_version" json:"schema_version"`
	Corrections   []Correction `yaml:"corrections" json:"corrections"`
}

type ApplyOptions struct {
	DryRun       bool
	Max          int
	RequireCount int
	BackupDir    string
}

type BulkUpdateOptions struct {
	DryRun       bool
	Max          int
	RequireCount int
	BackupDir    string
}

type UpdateResult struct {
	PaperlessID int      `json:"paperless_id,omitempty"`
	File        string   `json:"file"`
	DryRun      bool     `json:"dry_run"`
	Written     bool     `json:"written"`
	BackupPath  string   `json:"backup_path,omitempty"`
	Changes     []string `json:"changes"`
}

func ApplyCorrection(vault string, c Correction, dryRun bool) (*UpdateResult, error) {
	return ApplyCorrectionWithOptions(vault, c, ApplyOptions{DryRun: dryRun})
}

func ApplyCorrectionWithOptions(vault string, c Correction, opts ApplyOptions) (*UpdateResult, error) {
	if c.PaperlessID <= 0 {
		return nil, fmt.Errorf("paperless_id is required")
	}
	for _, tag := range c.RemoveTags {
		if strings.EqualFold(strings.TrimSpace(tag), "inbox") {
			return nil, fmt.Errorf("refusing to remove inbox tag automatically")
		}
	}
	path, meta, body, err := findByPaperlessID(vault, c.PaperlessID)
	if err != nil {
		return nil, err
	}
	res := &UpdateResult{PaperlessID: c.PaperlessID, File: path, DryRun: opts.DryRun}
	applyCorrectionToMeta(meta, c, res)
	if opts.DryRun || len(res.Changes) == 0 {
		return res, nil
	}
	backupPath, err := backupNote(path, opts.BackupDir)
	if err != nil {
		return nil, err
	}
	res.BackupPath = backupPath
	if err := writeNote(path, meta, body); err != nil {
		return nil, err
	}
	res.Written = true
	return res, nil
}

func ApplyCorrectionsFile(vault, path string, dryRun bool) ([]UpdateResult, error) {
	return ApplyCorrectionsFileWithOptions(vault, path, ApplyOptions{DryRun: dryRun})
}

func ApplyCorrectionsFileWithOptions(vault, path string, opts ApplyOptions) ([]UpdateResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	corrections, err := ParseCorrections(data)
	if err != nil {
		return nil, fmt.Errorf("parse corrections YAML: %w", err)
	}
	if opts.RequireCount > 0 && len(corrections) != opts.RequireCount {
		return nil, fmt.Errorf("correction count %d does not match required count %d", len(corrections), opts.RequireCount)
	}
	if opts.Max > 0 && len(corrections) > opts.Max {
		return nil, fmt.Errorf("correction count %d exceeds max %d", len(corrections), opts.Max)
	}
	results := make([]UpdateResult, 0, len(corrections))
	for _, c := range corrections {
		res, err := ApplyCorrectionWithOptions(vault, c, opts)
		if err != nil {
			return results, err
		}
		results = append(results, *res)
	}
	return results, nil
}

func ParseCorrections(data []byte) ([]Correction, error) {
	var doc CorrectionsFile
	if err := yaml.Unmarshal(data, &doc); err == nil && doc.SchemaVersion != 0 {
		if doc.SchemaVersion != paperlessimport.ReportSchemaVersion {
			return nil, fmt.Errorf("schema_version=%d; expected %d", doc.SchemaVersion, paperlessimport.ReportSchemaVersion)
		}
		return doc.Corrections, nil
	}
	var legacy []Correction
	if err := yaml.Unmarshal(data, &legacy); err != nil {
		return nil, err
	}
	return legacy, nil
}

func BulkUpdateByTag(vault, tag string, c Correction, dryRun bool) ([]UpdateResult, error) {
	return BulkUpdateByTagWithOptions(vault, tag, c, BulkUpdateOptions{DryRun: dryRun})
}

func BulkUpdateByTagWithOptions(vault, tag string, c Correction, opts BulkUpdateOptions) ([]UpdateResult, error) {
	if strings.EqualFold(strings.TrimSpace(tag), "") {
		return nil, fmt.Errorf("tag selector is required")
	}
	var ids []int
	err := filepath.WalkDir(vault, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}
		meta, _, err := parseNote(path)
		if err != nil {
			return nil
		}
		if !tagsContain(tagsFromMeta(meta), tag) {
			return nil
		}
		if id, ok := paperlessID(meta); ok {
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Ints(ids)
	if opts.RequireCount > 0 && len(ids) != opts.RequireCount {
		return nil, fmt.Errorf("matched %d document(s), required %d", len(ids), opts.RequireCount)
	}
	if opts.Max > 0 && len(ids) > opts.Max {
		return nil, fmt.Errorf("matched %d document(s), exceeds max %d", len(ids), opts.Max)
	}
	results := make([]UpdateResult, 0, len(ids))
	for _, id := range ids {
		c.PaperlessID = id
		res, err := ApplyCorrectionWithOptions(vault, c, ApplyOptions{DryRun: opts.DryRun, BackupDir: opts.BackupDir})
		if err != nil {
			return results, err
		}
		results = append(results, *res)
	}
	return results, nil
}

func findByPaperlessID(vault string, id int) (string, map[string]any, []byte, error) {
	var foundPath string
	var foundMeta map[string]any
	var foundBody []byte
	err := filepath.WalkDir(vault, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.ToLower(filepath.Ext(path)) != ".md" || foundPath != "" {
			return nil
		}
		meta, body, err := parseNote(path)
		if err != nil {
			return nil
		}
		if got, ok := paperlessID(meta); ok && got == id {
			foundPath, foundMeta, foundBody = path, meta, body
		}
		return nil
	})
	if err != nil {
		return "", nil, nil, err
	}
	if foundPath == "" {
		return "", nil, nil, fmt.Errorf("paperless_id %d not found in vault", id)
	}
	return foundPath, foundMeta, foundBody, nil
}

func applyCorrectionToMeta(meta map[string]any, c Correction, res *UpdateResult) {
	tags := tagsFromMeta(meta)
	beforeTags := strings.Join(tags, ",")
	for _, tag := range c.AddTags {
		if !tagsContain(tags, tag) {
			tags = append(tags, tag)
		}
	}
	for _, tag := range c.RemoveTags {
		tags = removeTag(tags, tag)
	}
	sort.Strings(tags)
	if strings.Join(tags, ",") != beforeTags {
		meta["tags"] = stringSliceToAny(tags)
		res.Changes = append(res.Changes, "tags")
	}
	if c.Correspondent != nil && metaString(meta, "correspondent") != *c.Correspondent {
		meta["correspondent"] = *c.Correspondent
		res.Changes = append(res.Changes, "correspondent")
	}
	if c.DocumentType != nil && metaString(meta, "document_type") != *c.DocumentType {
		meta["document_type"] = *c.DocumentType
		res.Changes = append(res.Changes, "document_type")
	}
	if c.StoragePath != nil {
		p, _ := meta["paperless"].(map[string]any)
		if p == nil {
			p = map[string]any{}
			meta["paperless"] = p
		}
		if metaString(p, "storage_path") != *c.StoragePath {
			p["storage_path"] = *c.StoragePath
			res.Changes = append(res.Changes, "paperless.storage_path")
		}
	}
}

func tagsFromMeta(meta map[string]any) []string {
	var tags []string
	if raw, ok := meta["tags"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && s != "" {
				tags = append(tags, s)
			}
		}
	}
	return tags
}

func stringSliceToAny(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func tagsContain(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, tag) {
			return true
		}
	}
	return false
}

func removeTag(tags []string, tag string) []string {
	out := tags[:0]
	for _, t := range tags {
		if !strings.EqualFold(t, tag) {
			out = append(out, t)
		}
	}
	return out
}

func metaString(meta map[string]any, key string) string {
	if s, ok := meta[key].(string); ok {
		return s
	}
	return ""
}

func writeNote(path string, meta map[string]any, body []byte) error {
	fm, err := yaml.Marshal(meta)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.Write(fm)
	out.WriteString("---\n")
	out.Write(body)
	return os.WriteFile(path, out.Bytes(), 0o600)
}

func backupNote(path, backupDir string) (string, error) {
	if strings.TrimSpace(backupDir) == "" {
		backupDir = filepath.Join(filepath.Dir(path), ".symingest-backups")
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s.%s.bak", filepath.Base(path), time.Now().UTC().Format("20060102T150405.000000000Z"))
	backupPath := filepath.Join(backupDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", err
	}
	return backupPath, nil
}

type ReviewFilters struct {
	Failed           bool
	Warnings         bool
	MissingMetadata  bool
	LowBody          bool
	DuplicateContent bool
	Unsupported      bool
	Unresolved       bool
}

type ReviewFinding struct {
	Kind    string `json:"kind"`
	ID      int    `json:"id,omitempty"`
	Message string `json:"message"`
}

type ReviewDocument struct {
	ID                int      `json:"id"`
	Status            string   `json:"status"`
	Reason            string   `json:"reason,omitempty"`
	MIME              string   `json:"mime,omitempty"`
	ExpectedExtension string   `json:"expected_extension,omitempty"`
	VaultPath         string   `json:"vault_path,omitempty"`
	ArchivePath       string   `json:"archive_path,omitempty"`
	Error             string   `json:"error,omitempty"`
	Warnings          []string `json:"warnings,omitempty"`
	Findings          []string `json:"findings,omitempty"`
}

type ReviewReport struct {
	SchemaVersion int              `json:"schema_version"`
	SourceKind    string           `json:"source_kind"`
	RunID         string           `json:"run_id,omitempty"`
	Total         int              `json:"total"`
	Documents     []ReviewDocument `json:"documents"`
	Findings      []ReviewFinding  `json:"findings,omitempty"`
	Warnings      []string         `json:"warnings,omitempty"`
}

func BuildReviewReport(path string, filters ReviewFilters) (*ReviewReport, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var probe map[string]any
	if err := json.Unmarshal(data, &probe); err != nil {
		return nil, fmt.Errorf("parse report JSON: %w", err)
	}
	if _, ok := probe["source_documents"]; ok {
		return buildVerifyReviewReport(data, filters)
	}
	return buildMigrationReviewReport(data, filters)
}

func buildMigrationReviewReport(data []byte, filters ReviewFilters) (*ReviewReport, error) {
	var migration paperlessimport.MigrationReport
	if err := json.Unmarshal(data, &migration); err != nil {
		return nil, fmt.Errorf("parse migration report JSON: %w", err)
	}
	report := &ReviewReport{SchemaVersion: paperlessimport.ReportSchemaVersion, SourceKind: "migration", RunID: migration.RunID, Warnings: migration.Warnings}
	addMigrationFindings(report, migration, filters)
	for _, d := range migration.Documents {
		if !includeMigrationDocument(d, migration, filters) {
			continue
		}
		report.Documents = append(report.Documents, ReviewDocument{ID: d.ID, Status: d.Status, Reason: d.Reason, MIME: d.MIME, ExpectedExtension: d.ExpectedExtension, VaultPath: d.VaultPath, ArchivePath: d.ArchivePath, Error: d.Error, Warnings: d.Warnings, Findings: migrationDocumentFindings(d, migration)})
	}
	report.Total = len(report.Documents)
	return report, nil
}

func buildVerifyReviewReport(data []byte, filters ReviewFilters) (*ReviewReport, error) {
	var verify paperlessimport.VerifyReport
	if err := json.Unmarshal(data, &verify); err != nil {
		return nil, fmt.Errorf("parse verify report JSON: %w", err)
	}
	report := &ReviewReport{SchemaVersion: paperlessimport.ReportSchemaVersion, SourceKind: "verify", RunID: verify.RunID}
	addVerifyFindings(report, verify, filters)
	report.Total = len(report.Documents)
	return report, nil
}

func includeMigrationDocument(d paperlessimport.DocumentResult, m paperlessimport.MigrationReport, f ReviewFilters) bool {
	if !hasReviewFilters(f) {
		return true
	}
	if f.Failed && d.Status == "failed" {
		return true
	}
	if f.Warnings && (len(d.Warnings) > 0 || d.Error != "") {
		return true
	}
	if f.MissingMetadata && (d.MIME == "" || d.VaultPath == "" || d.ArchivePath == "") {
		return true
	}
	if f.LowBody && hasLowBodySignal(d) {
		return true
	}
	if f.Unsupported && isUnsupportedResult(d, m) {
		return true
	}
	if f.Unresolved && hasUnresolvedSignal(d) {
		return true
	}
	return false
}

func hasReviewFilters(f ReviewFilters) bool {
	return f.Failed || f.Warnings || f.MissingMetadata || f.LowBody || f.DuplicateContent || f.Unsupported || f.Unresolved
}

func hasLowBodySignal(d paperlessimport.DocumentResult) bool {
	needle := strings.ToLower(strings.Join(append([]string{d.Error, d.Reason}, d.Warnings...), " "))
	return strings.Contains(needle, "body.min_length") || strings.Contains(needle, "body length") || strings.Contains(needle, "low body")
}

func hasUnresolvedSignal(d paperlessimport.DocumentResult) bool {
	needle := strings.ToLower(strings.Join(append([]string{d.Error, d.Reason}, d.Warnings...), " "))
	return strings.Contains(needle, "unresolved")
}

func isUnsupportedResult(d paperlessimport.DocumentResult, m paperlessimport.MigrationReport) bool {
	ext := strings.TrimPrefix(strings.ToLower(d.ExpectedExtension), ".")
	if ext != "" && m.UnsupportedFileTypes[ext] > 0 {
		return true
	}
	needle := strings.ToLower(strings.Join([]string{d.Error, d.Reason}, " "))
	return strings.Contains(needle, "unsupported")
}

func migrationDocumentFindings(d paperlessimport.DocumentResult, m paperlessimport.MigrationReport) []string {
	var out []string
	if d.Status == "failed" {
		out = append(out, "failed")
	}
	if d.MIME == "" || d.VaultPath == "" || d.ArchivePath == "" {
		out = append(out, "missing-metadata")
	}
	if len(d.Warnings) > 0 || d.Error != "" {
		out = append(out, "warnings")
	}
	if hasLowBodySignal(d) {
		out = append(out, "low-body")
	}
	if isUnsupportedResult(d, m) {
		out = append(out, "unsupported")
	}
	if hasUnresolvedSignal(d) {
		out = append(out, "unresolved")
	}
	return out
}

func addMigrationFindings(report *ReviewReport, m paperlessimport.MigrationReport, f ReviewFilters) {
	if !hasReviewFilters(f) || f.Unsupported {
		for ext, count := range m.UnsupportedFileTypes {
			if count > 0 {
				report.Findings = append(report.Findings, ReviewFinding{Kind: "unsupported", Message: fmt.Sprintf("%d document(s) with unsupported extension .%s", count, strings.TrimPrefix(ext, "."))})
			}
		}
	}
	if !hasReviewFilters(f) || f.Unresolved {
		addIDFindings(report, "unresolved_tag", m.UnresolvedTagIDs)
		addIDFindings(report, "unresolved_correspondent", m.UnresolvedCorrespondentIDs)
		addIDFindings(report, "unresolved_document_type", m.UnresolvedDocumentTypeIDs)
		addIDFindings(report, "unresolved_storage_path", m.UnresolvedStoragePathIDs)
	}
}

func addVerifyFindings(report *ReviewReport, v paperlessimport.VerifyReport, f ReviewFilters) {
	includeAll := !hasReviewFilters(f)
	if includeAll || f.MissingMetadata || f.Failed {
		addDocFindings(report, "missing", v.Missing)
		addDocFindings(report, "duplicate", v.Duplicate)
		addDocFindings(report, "missing_archive", v.MissingArchive)
		addDocFindings(report, "hash_mismatch", v.HashMismatch)
		addDocFindings(report, "source_hash_mismatch", v.SourceHashMismatch)
	}
	if includeAll || f.DuplicateContent {
		addDocFindings(report, "duplicate_content", v.DuplicateContent)
	}
	if includeAll || f.MissingMetadata || f.Unresolved {
		for _, mm := range v.Mismatches {
			report.Findings = append(report.Findings, ReviewFinding{Kind: "metadata_mismatch", ID: mm.DocumentID, Message: fmt.Sprintf("%s: expected %q got %q", mm.Field, mm.Expected, mm.Got)})
			report.Documents = append(report.Documents, ReviewDocument{ID: mm.DocumentID, Status: "metadata_mismatch", Error: fmt.Sprintf("%s: expected %q got %q", mm.Field, mm.Expected, mm.Got), Findings: []string{"metadata_mismatch"}})
		}
	}
}

func addIDFindings(report *ReviewReport, kind string, ids []int) {
	for _, id := range ids {
		report.Findings = append(report.Findings, ReviewFinding{Kind: kind, ID: id, Message: fmt.Sprintf("%s id %d", strings.ReplaceAll(kind, "_", " "), id)})
	}
}

func addDocFindings(report *ReviewReport, kind string, ids []int) {
	for _, id := range ids {
		report.Findings = append(report.Findings, ReviewFinding{Kind: kind, ID: id, Message: fmt.Sprintf("document %d: %s", id, strings.ReplaceAll(kind, "_", " "))})
		report.Documents = append(report.Documents, ReviewDocument{ID: id, Status: kind, Findings: []string{kind}})
	}
}

func WriteReviewHTML(path string, report *ReviewReport) error {
	const tpl = `<!doctype html><meta charset="utf-8"><title>symingest migration review</title><style>body{font-family:system-ui,sans-serif;margin:2rem;background:#0b1020;color:#e8edf7}a{color:#9cc7ff}table{border-collapse:collapse;width:100%;margin:1rem 0}td,th{border:1px solid #33415c;padding:.45rem;text-align:left;vertical-align:top}th{background:#182036}.failed,.missing,.missing_archive,.hash_mismatch,.source_hash_mismatch,.metadata_mismatch{background:#4a1820}.duplicate_content{background:#3d3214}.chip{display:inline-block;border:1px solid #5b6b8c;border-radius:999px;padding:.1rem .45rem;margin:.1rem}.muted{color:#aab4c8}code{word-break:break-all}</style><h1>symingest migration review</h1><p class="muted">Run {{.RunID}} · {{.SourceKind}} · {{.Total}} documents shown. Document body text is intentionally not included.</p>{{if .Findings}}<h2>Findings</h2><table><thead><tr><th>Kind</th><th>ID</th><th>Message</th></tr></thead><tbody>{{range .Findings}}<tr class="{{.Kind}}"><td>{{.Kind}}</td><td>{{.ID}}</td><td>{{.Message}}</td></tr>{{end}}</tbody></table>{{end}}<h2>Documents</h2><table><thead><tr><th>ID</th><th>Status</th><th>Findings</th><th>MIME</th><th>Vault path</th><th>Archive path</th><th>Error / warnings</th></tr></thead><tbody>{{range .Documents}}<tr class="{{.Status}}"><td>{{.ID}}</td><td>{{.Status}}</td><td>{{range .Findings}}<span class="chip">{{.}}</span>{{end}}</td><td>{{.MIME}}</td><td><a href="file://{{.VaultPath}}"><code>{{.VaultPath}}</code></a></td><td><a href="file://{{.ArchivePath}}"><code>{{.ArchivePath}}</code></a></td><td>{{.Error}}{{range .Warnings}}<br>{{.}}{{end}}</td></tr>{{end}}</tbody></table>`
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return template.Must(template.New("review").Parse(tpl)).Execute(f, report)
}
