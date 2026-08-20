package paperlessimport

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/paperless"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/version"
	"github.com/danieljustus/symaira-ingest/internal/writer"
	"gopkg.in/yaml.v3"
)

// VerifyMismatch records a single metadata field that differs between a
// Paperless source document and its generated vault note.
type VerifyMismatch struct {
	DocumentID int    `json:"document_id"`
	Field      string `json:"field"`
	Expected   string `json:"expected"`
	Got        string `json:"got"`
}

// VerifyReport is the structured result of comparing Paperless source records
// against generated vault notes and archived originals. It is designed as a
// stable, machine-readable gate for a migration: an empty set of discrepancies
// means the vault faithfully represents the Paperless archive. It never
// contains document content, only IDs, field names, and paths.
type VerifyReport struct {
	SchemaVersion      int              `json:"schema_version"`
	RunID              string           `json:"run_id,omitempty"`
	ToolVersion        string           `json:"tool_version,omitempty"`
	Source             string           `json:"source,omitempty"`
	SourceURL          string           `json:"source_url,omitempty"`
	StartedAt          time.Time        `json:"started_at,omitempty"`
	FinishedAt         time.Time        `json:"finished_at,omitempty"`
	DurationSeconds    float64          `json:"duration_seconds,omitempty"`
	Mode               string           `json:"mode,omitempty"`
	SourceDocuments    int              `json:"source_documents"`
	VaultNotes         int              `json:"vault_notes"`
	Verified           int              `json:"verified"`
	DeepVerify         bool             `json:"deep_verify,omitempty"`
	DeepVerified       int              `json:"deep_verified,omitempty"`
	Missing            []int            `json:"missing,omitempty"`              // source doc IDs with no note
	Duplicate          []int            `json:"duplicate,omitempty"`            // doc IDs with more than one note
	DuplicateContent   []int            `json:"duplicate_content,omitempty"`    // informational: doc IDs whose original bytes duplicate another Paperless ID
	MissingArchive     []int            `json:"missing_archive,omitempty"`      // note exists but the archived original is gone
	HashMismatch       []int            `json:"hash_mismatch,omitempty"`        // archive hash does not match stored hash
	SourceHashMismatch []int            `json:"source_hash_mismatch,omitempty"` // archived original differs from Paperless download in deep verify
	Mismatches         []VerifyMismatch `json:"mismatches,omitempty"`           // metadata differences
}

// Complete reports whether the vault faithfully represents the Paperless
// archive: no missing, duplicate-note, archive-less, hash-mismatched, or
// metadata-mismatched documents. DuplicateContent is informational because
// Paperless can legitimately contain multiple document IDs with identical
// original bytes; symingest writes one note per ID and may deduplicate archive
// blobs by SHA-256.
func (r *VerifyReport) Complete() bool {
	return len(r.Missing) == 0 && len(r.Duplicate) == 0 &&
		len(r.MissingArchive) == 0 && len(r.HashMismatch) == 0 && len(r.SourceHashMismatch) == 0 && len(r.Mismatches) == 0
}

// Verify compares every Paperless source document (bounded by opts.Since/
// opts.Limit/opts.IDs when set) against the stored import state or the notes
// under vault and the archived originals they reference. By default it does
// not download document content; with opts.DeepVerify it re-downloads each
// selected Paperless original and compares SHA-256 against the archived file.
// If st is non-nil, Verify uses the durable Paperless import state table as
// the source of truth for mappings and also checks the vault notes.
func Verify(ctx context.Context, opts Options, vault string, st *store.Store) (*VerifyReport, error) {
	started := time.Now().UTC()
	client := paperless.NewClient(opts.BaseURL, opts.Token)

	docs, err := selectDocuments(ctx, client, opts)
	if err != nil {
		return nil, err
	}
	lu, err := loadLookups(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("load lookup maps: %w", err)
	}

	notes, err := scanVaultNotes(vault)
	if err != nil {
		return nil, fmt.Errorf("scan vault notes: %w", err)
	}

	report := &VerifyReport{
		SchemaVersion:   ReportSchemaVersion,
		RunID:           newRunID(started),
		ToolVersion:     version.Version,
		Source:          "paperless",
		SourceURL:       opts.BaseURL,
		StartedAt:       started,
		Mode:            "verify",
		DeepVerify:      opts.DeepVerify,
		SourceDocuments: len(docs),
	}
	defer func() {
		report.FinishedAt = time.Now().UTC()
		report.DurationSeconds = report.FinishedAt.Sub(report.StartedAt).Seconds()
	}()
	for _, list := range notes {
		report.VaultNotes += len(list)
	}

	targetVault := opts.TargetVault
	targetArchive := opts.TargetArchive

	for _, doc := range docs {
		matches := notes[doc.ID]
		state, stateErr := paperlessImportState(ctx, st, opts.BaseURL, targetVault, targetArchive, doc.ID)
		docOK := true

		if stateErr != nil {
			report.Mismatches = append(report.Mismatches, VerifyMismatch{doc.ID, "import_state", "readable", stateErr.Error()})
			docOK = false
		} else if state != nil {
			if state.Status != "imported" {
				report.Missing = append(report.Missing, doc.ID)
				docOK = false
			} else {
				if len(matches) == 0 && fileExists(state.VaultPath) {
					matches = []*writer.Note{{SourcePath: state.VaultPath, ArchivePath: state.ArchivePath, SHA256: state.SHA256}}
				}
				if !fileExists(state.VaultPath) {
					report.Missing = append(report.Missing, doc.ID)
					docOK = false
				}
				if state.ArchivePath != "" && !fileExists(state.ArchivePath) {
					report.MissingArchive = append(report.MissingArchive, doc.ID)
					docOK = false
				}
				if state.ArchivePath != "" && state.SHA256 != "" {
					if got, err := fileSHA256(state.ArchivePath); err != nil || !strings.EqualFold(got, state.SHA256) {
						report.HashMismatch = append(report.HashMismatch, doc.ID)
						docOK = false
					}
				}
			}
		}

		if len(matches) == 0 {
			if docOK {
				report.Missing = append(report.Missing, doc.ID)
			}
			docOK = false
			continue
		}
		if len(matches) > 1 {
			report.Duplicate = append(report.Duplicate, doc.ID)
			docOK = false
		}
		note := matches[0]

		if note.ArchivePath == "" || !fileExists(note.ArchivePath) {
			report.MissingArchive = append(report.MissingArchive, doc.ID)
			docOK = false
		}
		if note.SHA256 != "" && note.ArchivePath != "" {
			if got, err := fileSHA256(note.ArchivePath); err != nil || !strings.EqualFold(got, note.SHA256) {
				report.HashMismatch = append(report.HashMismatch, doc.ID)
				docOK = false
			}
		}
		if opts.DeepVerify && note.ArchivePath != "" && fileExists(note.ArchivePath) {
			localHash, err := fileSHA256(note.ArchivePath)
			if err != nil {
				report.HashMismatch = append(report.HashMismatch, doc.ID)
				docOK = false
			} else {
				remoteHash, err := paperlessDownloadSHA256(ctx, client, doc.ID)
				if err != nil {
					report.Mismatches = append(report.Mismatches, VerifyMismatch{doc.ID, "source_download", "readable", err.Error()})
					docOK = false
				} else if !strings.EqualFold(localHash, remoteHash) {
					report.SourceHashMismatch = append(report.SourceHashMismatch, doc.ID)
					docOK = false
				} else {
					report.DeepVerified++
				}
			}
		}

		if mm := compareMetadata(doc, note, lu); len(mm) > 0 {
			report.Mismatches = append(report.Mismatches, mm...)
			docOK = false
		}

		if docOK {
			report.Verified++
		}
	}

	report.DuplicateContent = findDuplicateContentIDs(docs, notes, st, opts.BaseURL, targetVault, targetArchive)

	sort.Ints(report.Missing)
	sort.Ints(report.Duplicate)
	sort.Ints(report.DuplicateContent)
	sort.Ints(report.MissingArchive)
	sort.Ints(report.HashMismatch)
	sort.Ints(report.SourceHashMismatch)
	sort.Slice(report.Mismatches, func(i, j int) bool {
		if report.Mismatches[i].DocumentID != report.Mismatches[j].DocumentID {
			return report.Mismatches[i].DocumentID < report.Mismatches[j].DocumentID
		}
		return report.Mismatches[i].Field < report.Mismatches[j].Field
	})
	return report, nil
}

func paperlessDownloadSHA256(ctx context.Context, client *paperless.Client, id int) (string, error) {
	h := sha256.New()
	if err := client.DownloadDocument(ctx, id, h); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func paperlessImportState(ctx context.Context, st *store.Store, baseURL, targetVault, targetArchive string, id int) (*store.PaperlessImportState, error) {
	if st == nil {
		return nil, nil
	}
	var state *store.PaperlessImportState
	var err error
	if targetVault != "" || targetArchive != "" {
		state, err = st.PaperlessImportStateForTarget(ctx, baseURL, targetVault, targetArchive, id)
	} else {
		state, err = st.PaperlessImportStateByID(ctx, baseURL, id)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !os.IsNotExist(err) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) || os.IsNotExist(err) {
		return nil, nil
	}
	return state, nil
}

func findDuplicateContentIDs(docs []paperless.Document, notes map[int][]*writer.Note, st *store.Store, baseURL, targetVault, targetArchive string) []int {
	seen := map[string][]int{}
	for _, doc := range docs {
		var sha string
		if state, _ := paperlessImportState(context.Background(), st, baseURL, targetVault, targetArchive, doc.ID); state != nil {
			sha = state.SHA256
		}
		if sha == "" && len(notes[doc.ID]) > 0 {
			sha = notes[doc.ID][0].SHA256
		}
		if sha != "" {
			seen[sha] = append(seen[sha], doc.ID)
		}
	}
	var dupes []int
	for _, ids := range seen {
		if len(ids) > 1 {
			dupes = append(dupes, ids[1:]...)
		}
	}
	return dupes
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

// compareMetadata returns the metadata fields that differ between a source
// document and its note, comparing against the exact values the importer would
// have written (via resolveDocMeta).
func compareMetadata(doc paperless.Document, note *writer.Note, lu *lookups) []VerifyMismatch {
	var out []VerifyMismatch
	meta := resolveDocMeta(doc, lu)

	wantTags := append([]string(nil), meta.Tags...)
	gotTags := append([]string(nil), note.Tags...)
	sort.Strings(wantTags)
	sort.Strings(gotTags)
	if strings.Join(wantTags, ",") != strings.Join(gotTags, ",") {
		out = append(out, VerifyMismatch{doc.ID, "tags", strings.Join(wantTags, ","), strings.Join(gotTags, ",")})
	}
	if meta.Correspondent != note.Correspondent {
		out = append(out, VerifyMismatch{doc.ID, "correspondent", meta.Correspondent, note.Correspondent})
	}
	if meta.DocumentType != note.DocumentType {
		out = append(out, VerifyMismatch{doc.ID, "document_type", meta.DocumentType, note.DocumentType})
	}

	var gotStorage string
	var gotCreated time.Time
	if note.Paperless != nil {
		gotStorage = note.Paperless.StoragePath
		gotCreated = note.Paperless.Created
	}
	if meta.StoragePath != gotStorage {
		out = append(out, VerifyMismatch{doc.ID, "storage_path", meta.StoragePath, gotStorage})
	}
	if wantCreated := paperlessCreated(doc); !wantCreated.Equal(gotCreated) {
		out = append(out, VerifyMismatch{doc.ID, "created", formatDate(wantCreated), formatDate(gotCreated)})
	}
	return out
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// scanVaultNotes indexes every Paperless-migrated note under vault by its
// Paperless document ID. Notes without Paperless frontmatter (ordinary
// one-shot ingests) are ignored, as are unparseable files.
func scanVaultNotes(vault string) (map[int][]*writer.Note, error) {
	notes := map[int][]*writer.Note{}
	if vault == "" {
		return notes, nil
	}
	if info, err := os.Stat(vault); err != nil {
		if os.IsNotExist(err) {
			return notes, nil
		}
		return nil, err
	} else if !info.IsDir() {
		return notes, nil
	}

	err := filepath.WalkDir(vault, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		note := parseNoteFrontmatter(data)
		if note == nil || note.Paperless == nil || note.Paperless.DocumentID == 0 {
			return nil
		}
		id := note.Paperless.DocumentID
		notes[id] = append(notes[id], note)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return notes, nil
}

// parseNoteFrontmatter extracts the leading YAML frontmatter block written by
// writer.WriteNote and unmarshals it into a Note. It returns nil for files
// without a well-formed frontmatter block.
func parseNoteFrontmatter(data []byte) *writer.Note {
	s := string(data)
	const open = "---\n"
	if !strings.HasPrefix(s, open) {
		return nil
	}
	rest := s[len(open):]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return nil
	}
	var note writer.Note
	if err := yaml.Unmarshal([]byte(rest[:end]), &note); err != nil {
		return nil
	}
	return &note
}
