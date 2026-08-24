// Package api is the stable in-process entry point into symingest.
//
// symingest's logic lives in internal/ packages, which Go's import rules make
// unreachable from other modules. Consumers that link this module rather than
// executing the symingest binary — symdesk since the repo consolidation — go
// through this package instead, so the internal layout stays free to change.
//
// The surface is deliberately narrow: it covers exactly what an embedding
// consumer needs — run a document through the ingest pipeline, extract text
// without persisting anything, inspect and retry the job queue, read and poll
// the configured mail accounts, split a PDF, migrate a Paperless-ngx
// instance, and report the configured archive path. Everything richer stays
// CLI/MCP-only.
package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-ingest/internal/config"
	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-ingest/internal/pdfops"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/version"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// SchemaVersion is the contract version of the types in this package. A
// consumer that pins to it gets a compile error rather than silent drift when
// the shapes below change incompatibly.
const SchemaVersion = 1

// ErrDuplicate reports that the source was already ingested — the same
// condition the CLI reports as a skipped duplicate, not a failure.
var ErrDuplicate = ingest.ErrDuplicate

// ErrNoVault reports that an operation needing a destination vault ran
// without one configured. Consumers use it to prompt for vault setup instead
// of surfacing a generic failure.
var ErrNoVault = errors.New("no vault configured")

// Version returns the symingest version this package was built from.
func Version() string { return version.Version }

// Options selects the storage locations and OCR engine a call operates on.
//
// The zero value resolves everything from the user's symingest configuration
// (~/.config/symingest/config.toml plus SYMINGEST_* environment overrides), so
// an embedding consumer honors the same setup the CLI would. Each non-empty
// field overrides the corresponding configured value; a consumer that wants a
// throwaway location — a scratch OCR run, a test — sets it explicitly.
type Options struct {
	// Vault is the directory Markdown notes are written to.
	Vault string
	// Archive is the directory original files are preserved in.
	Archive string
	// DBPath is the SQLite document store.
	DBPath string
	// OCRLang is the Tesseract language code (default "eng").
	OCRLang string
	// OllamaBaseURL and OllamaModel select the VLM OCR engine. When
	// OllamaModel is empty, OCR falls back to Tesseract.
	OllamaBaseURL string
	OllamaModel   string
	// DisableVLM forces Tesseract even when the configuration names a VLM
	// model. A consumer that lets its user choose the engine explicitly sets
	// it so "tesseract" means Tesseract, rather than whatever the user's
	// symingest configuration happens to prefer.
	DisableVLM bool
}

// resolved is Options with every path filled in from configuration and the
// documented defaults, so the call sites below never re-derive them.
type resolved struct {
	vault         string
	archive       string
	dbPath        string
	ocrLang       string
	ollamaBaseURL string
	ollamaModel   string
}

func (o Options) resolve() (resolved, error) {
	cfg, err := config.Load()
	if err != nil {
		return resolved{}, fmt.Errorf("load symingest configuration: %w", err)
	}

	r := resolved{
		vault:         firstNonEmpty(o.Vault, cfg.Vault),
		archive:       firstNonEmpty(o.Archive, cfg.ArchivePath),
		dbPath:        firstNonEmpty(o.DBPath, cfg.DBPath),
		ocrLang:       firstNonEmpty(o.OCRLang, cfg.OCRLang, "eng"),
		ollamaBaseURL: firstNonEmpty(o.OllamaBaseURL, cfg.OllamaBaseURL),
		ollamaModel:   firstNonEmpty(o.OllamaModel, cfg.OllamaModel),
	}

	if o.DisableVLM {
		r.ollamaBaseURL, r.ollamaModel = "", ""
	}

	if r.archive == "" {
		if r.archive, err = defaultDataPath("archive"); err != nil {
			return resolved{}, err
		}
	}
	if r.dbPath == "" {
		if r.dbPath, err = defaultDataPath("symingest.db"); err != nil {
			return resolved{}, err
		}
	}
	return r, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// defaultDataPath mirrors the CLI's XDG data location for the document store
// and the archive, so an embedding consumer and the binary address the same
// state rather than two divergent copies.
func defaultDataPath(name string) (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "symingest", name), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory; set %s explicitly: %w", name, err)
	}
	return filepath.Join(home, ".local", "share", "symingest", name), nil
}

// ArchivePath reports where symingest preserves original files, resolved the
// same way the CLI resolves it. Consumers use it to warn when the archive sits
// inside the vault, which would make the vault index its own originals.
func ArchivePath() (string, error) {
	r, err := Options{}.resolve()
	if err != nil {
		return "", err
	}
	return r.archive, nil
}

// Result describes a completed ingest.
type Result struct {
	SourcePath    string   `json:"source_path"`
	SHA256        string   `json:"sha256"`
	Kind          string   `json:"kind"`
	VaultPath     string   `json:"vault_path"`
	ArchivePath   string   `json:"archive_path"`
	Category      string   `json:"category,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Correspondent string   `json:"correspondent,omitempty"`
	DocumentType  string   `json:"document_type,omitempty"`
	// Text and Engine carry the extraction that produced the note, so a
	// consumer does not have to re-read and re-parse the written Markdown.
	Text   string `json:"text,omitempty"`
	Engine string `json:"engine,omitempty"`
}

// Ingest runs source through the full pipeline: extract, classify, archive the
// original, and write a Markdown note into the vault. It is the in-process
// equivalent of `symingest ingest`.
//
// A source whose content hash is already recorded returns ErrDuplicate.
func Ingest(ctx context.Context, source string, opts Options) (*Result, error) {
	r, err := opts.resolve()
	if err != nil {
		return nil, err
	}
	if r.vault == "" {
		return nil, ErrNoVault
	}

	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, fmt.Errorf("invalid source path %q: %w", source, err)
	}

	st, err := store.Open(r.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open document store: %w", err)
	}
	defer st.Close()

	pipeline := &ingest.Pipeline{
		Engine:     ocr.NewEngine(r.ocrLang, r.ollamaBaseURL, r.ollamaModel),
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: r.vault},
		ArchiveDir: r.archive,
	}

	res, err := pipeline.Ingest(ctx, abs, nil)
	if err != nil {
		return nil, err
	}
	return newResult(res), nil
}

func newResult(res *ingest.Result) *Result {
	out := &Result{
		SourcePath:    res.SourcePath,
		SHA256:        res.SHA256,
		Kind:          string(res.Kind),
		VaultPath:     res.VaultPath,
		ArchivePath:   res.ArchivePath,
		Category:      res.Category,
		Tags:          res.Tags,
		Correspondent: res.Correspondent,
		DocumentType:  res.DocumentType,
	}
	if res.Extract != nil {
		out.Text = res.Extract.Text
		out.Engine = res.Extract.Engine
	}
	return out
}

// Extraction is the text recovered from a file, plus the engine that produced
// it ("tesseract", an Ollama model name, or a structured-format reader).
type Extraction struct {
	Text   string `json:"text"`
	MIME   string `json:"mime,omitempty"`
	Engine string `json:"engine,omitempty"`
}

// ExtractText recovers the text of a single file without touching the vault,
// the archive, or the document store. Consumers that only want OCR — the
// self-hosted server's document worker — use it instead of driving a full
// ingest into a throwaway vault and reading the note back.
func ExtractText(ctx context.Context, source string, opts Options) (*Extraction, error) {
	r, err := opts.resolve()
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, fmt.Errorf("invalid source path %q: %w", source, err)
	}

	kind, err := extract.Detect(abs)
	if err != nil {
		return nil, fmt.Errorf("detect source type: %w", err)
	}

	res, err := ingest.ExtractText(ctx, abs, kind, ocr.NewEngine(r.ocrLang, r.ollamaBaseURL, r.ollamaModel))
	if err != nil {
		return nil, err
	}
	return &Extraction{Text: res.Text, MIME: res.MIME, Engine: res.Engine}, nil
}

// Job is a queued ingest unit, reduced to the fields a consumer can present.
type Job struct {
	ID         int64  `json:"id"`
	DocumentID int64  `json:"document_id"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	Kind       string `json:"kind"`
	SourcePath string `json:"source_path"`
	LastError  string `json:"last_error,omitempty"`
}

// Jobs lists the most recent queued jobs, newest first, capped at limit
// (a limit of zero or less applies the CLI's default of 100).
func Jobs(ctx context.Context, opts Options, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = 100
	}
	st, closeStore, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	jobs, err := st.ListJobs(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}

	out := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		job := Job{
			ID:         j.ID,
			DocumentID: j.DocumentID,
			Status:     j.Status,
			Attempts:   j.Attempts,
			Kind:       j.Kind,
			SourcePath: j.SourcePath,
		}
		if j.LastError != nil {
			job.LastError = *j.LastError
		}
		out = append(out, job)
	}
	return out, nil
}

// RetryJob resets a failed job to pending so the worker picks it up again.
func RetryJob(ctx context.Context, opts Options, jobID int64) error {
	st, closeStore, err := openStore(opts)
	if err != nil {
		return err
	}
	defer closeStore()

	if err := st.RetryJob(ctx, jobID); err != nil {
		return fmt.Errorf("retry job %d: %w", jobID, err)
	}
	return nil
}

func openStore(opts Options) (*store.Store, func(), error) {
	r, err := opts.resolve()
	if err != nil {
		return nil, nil, err
	}
	st, err := store.Open(r.dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open document store: %w", err)
	}
	return st, func() { _ = st.Close() }, nil
}

// MailAccount is a configured IMAP account, reduced to what a consumer can
// display. No credential value is ever exposed: PasswordSecret is the masked
// locator, not the secret it points at.
type MailAccount struct {
	ID                       string `json:"id"`
	Host                     string `json:"host"`
	Port                     int    `json:"port"`
	Username                 string `json:"username"`
	Folder                   string `json:"folder"`
	PasswordSecret           string `json:"password_secret"`
	PasswordSecretKind       string `json:"password_secret_kind"`
	PasswordSecretConfigured bool   `json:"password_secret_configured"`
}

// MailAccounts reads the configured IMAP accounts. An empty configPath uses
// the location the CLI would use.
//
// A missing configuration file is not an error: mail ingestion is optional, so
// the result is simply empty and a consumer can keep polling for one to appear.
func MailAccounts(configPath string) ([]MailAccount, error) {
	path, err := config.ConfigPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mail config path: %w", err)
	}
	document, err := config.ReadMailConfig(path)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read mail configuration: %w", err)
	}

	out := make([]MailAccount, 0, len(document.Accounts))
	for _, account := range document.Accounts {
		view := config.ViewAccount(account)
		out = append(out, MailAccount{
			ID:                       view.ID,
			Host:                     view.Host,
			Port:                     view.Port,
			Username:                 view.Username,
			Folder:                   view.Folder,
			PasswordSecret:           view.PasswordSecret,
			PasswordSecretKind:       view.PasswordSecretKind,
			PasswordSecretConfigured: view.PasswordSecretConfigured,
		})
	}
	return out, nil
}

// MailFetchOptions adjusts a single mail poll.
type MailFetchOptions struct {
	Options

	// ConfigPath is the mail configuration to read accounts from. Empty uses
	// the CLI's location.
	ConfigPath string

	// AccountID restricts the poll to one account, identified by the ID from
	// MailAccounts. Empty polls every configured account.
	AccountID string

	// StagingDir is where fetched attachments are written. It is required:
	// FetchMail hands the files back rather than ingesting them, so the
	// caller owns their location and their cleanup.
	StagingDir string
}

// MailAttachment is one fetched attachment, staged on disk and ready to be
// handed to Ingest or to the consumer's own pipeline.
type MailAttachment struct {
	Path          string `json:"path"`
	MessageID     string `json:"message_id"`
	Correspondent string `json:"correspondent,omitempty"`
	AccountID     string `json:"account_id"`
}

// MailFetchResult reports the outcome of one poll across the selected
// accounts. Errors are per-account: one unreachable server must not hide the
// attachments another account delivered.
type MailFetchResult struct {
	Attachments []MailAttachment  `json:"attachments"`
	Errors      map[string]string `json:"errors,omitempty"`
}

// FetchMail runs a single IMAP poll and stages the new attachments in
// opts.StagingDir, returning their paths.
//
// Idempotency is the store's: a message already recorded as processed is not
// fetched again, and each account's UID cursor advances, so a consumer polling
// on its own schedule sees each message exactly once. Unlike the CLI's
// watcher, no ingest job is queued — the caller decides what to do with the
// staged files.
//
// Per-account failures are returned in MailFetchResult.Errors, already reduced
// to a credential-free reason. The error return is reserved for failures that
// prevented polling at all.
func FetchMail(ctx context.Context, opts MailFetchOptions) (*MailFetchResult, error) {
	if opts.StagingDir == "" {
		return nil, fmt.Errorf("StagingDir is required: FetchMail stages attachments rather than ingesting them")
	}
	if err := os.MkdirAll(opts.StagingDir, 0o700); err != nil {
		return nil, fmt.Errorf("create mail staging directory: %w", err)
	}

	path, err := config.ConfigPath(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mail config path: %w", err)
	}
	document, err := config.ReadMailConfig(path)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return &MailFetchResult{}, nil
		}
		return nil, fmt.Errorf("read mail configuration: %w", err)
	}

	accounts := document.Accounts
	if opts.AccountID != "" {
		accounts = nil
		for _, account := range document.Accounts {
			if config.AccountID(account) == opts.AccountID {
				accounts = append(accounts, account)
			}
		}
		if len(accounts) == 0 {
			return nil, fmt.Errorf("no configured mail account with ID %q", opts.AccountID)
		}
	}
	if len(accounts) == 0 {
		return &MailFetchResult{}, nil
	}

	st, closeStore, err := openStore(opts.Options)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	poller, err := ingest.NewMailPoller(st, accounts, ingest.MailPollerOptions{
		ProcessingDir: opts.StagingDir,
		FailedDir:     filepath.Join(opts.StagingDir, "failed"),
	})
	if err != nil {
		return nil, fmt.Errorf("create mail poller: %w", err)
	}

	result := &MailFetchResult{}
	currentAccount := ""
	poller.Enqueue = func(_ context.Context, workPath, msgID, correspondent string) error {
		result.Attachments = append(result.Attachments, MailAttachment{
			Path:          workPath,
			MessageID:     msgID,
			Correspondent: correspondent,
			AccountID:     currentAccount,
		})
		return nil
	}

	for _, account := range accounts {
		currentAccount = config.AccountID(account)
		if err := poller.PollOnce(ctx, account); err != nil {
			if result.Errors == nil {
				result.Errors = make(map[string]string)
			}
			result.Errors[currentAccount] = err.Error()
		}
	}
	return result, nil
}

// SplitPDF splits input after each page in atSpec ("2,4" or "2-3,6") and
// writes the parts into outputDir, returning their paths in document order.
// It requires the Poppler utilities pdfinfo, pdfseparate and pdfunite.
func SplitPDF(ctx context.Context, input, atSpec, outputDir string) ([]string, error) {
	abs, err := filepath.Abs(input)
	if err != nil {
		return nil, fmt.Errorf("invalid split input %q: %w", input, err)
	}
	outDir, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, fmt.Errorf("invalid split output directory %q: %w", outputDir, err)
	}
	return pdfops.DefaultTools().Split(ctx, abs, atSpec, outDir)
}

// PaperlessOptions configures a migration from a live Paperless-ngx instance.
type PaperlessOptions struct {
	Options

	BaseURL string
	Token   string
	Since   time.Time

	DryRun      bool
	Plan        bool
	Resume      bool
	DeepVerify  bool
	RetryFailed bool
	Concurrency int
	Limit       int
}

// PaperlessStats summarizes a completed migration.
type PaperlessStats struct {
	Total    int      `json:"total"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Failed   int      `json:"failed"`
	Warnings []string `json:"warnings,omitempty"`
}

// PaperlessMigrate imports the documents of a running Paperless-ngx instance
// through the ingest pipeline. It is idempotent: re-running updates the notes
// already keyed on the Paperless document ID and checksum.
func PaperlessMigrate(ctx context.Context, opts PaperlessOptions) (*PaperlessStats, error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("BaseURL is required")
	}
	if opts.Token == "" {
		return nil, fmt.Errorf("Token is required")
	}

	r, err := opts.Options.resolve()
	if err != nil {
		return nil, err
	}
	if r.vault == "" {
		return nil, ErrNoVault
	}

	st, err := store.Open(r.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open document store: %w", err)
	}
	defer st.Close()

	pipeline := &ingest.Pipeline{
		Engine:     ocr.NewEngine(r.ocrLang, r.ollamaBaseURL, r.ollamaModel),
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: r.vault},
		ArchiveDir: r.archive,
	}

	stats, err := paperlessimport.Run(ctx, paperlessimport.Options{
		BaseURL:       opts.BaseURL,
		Token:         opts.Token,
		Since:         opts.Since,
		DryRun:        opts.DryRun,
		Plan:          opts.Plan,
		Resume:        opts.Resume,
		DeepVerify:    opts.DeepVerify,
		RetryFailed:   opts.RetryFailed,
		Concurrency:   opts.Concurrency,
		Limit:         opts.Limit,
		TargetVault:   r.vault,
		TargetArchive: r.archive,
	}, pipeline)
	if err != nil {
		return nil, err
	}

	return &PaperlessStats{
		Total:    stats.Total,
		Imported: stats.Imported,
		Skipped:  stats.Skipped,
		Failed:   stats.Failed,
		Warnings: stats.Warnings,
	}, nil
}
