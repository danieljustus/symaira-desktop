package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	desktopconfig "github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/extract"
	ingestengine "github.com/danieljustus/symaira-desktop/internal/ingest/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/ocr"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/pdfops"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/version"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/writer"
)

// SchemaVersion is the contract version of the types in this package. A
// consumer that pins to it gets a compile error rather than silent drift when
// the shapes below change incompatibly.
const SchemaVersion = 1

// ErrDuplicate reports that the source was already ingested — the same
// condition the CLI reports as a skipped duplicate, not a failure.
var ErrDuplicate = ingestengine.ErrDuplicate

// ErrNoVault reports that an operation needing a destination vault ran
// without one configured. Consumers use it to prompt for vault setup instead
// of surfacing a generic failure.
var ErrNoVault = errors.New("no vault configured")

// ErrNoArchivedOriginal reports that Reprocess was asked to re-OCR a document
// that has no archived original recorded.
var ErrNoArchivedOriginal = ingestengine.ErrNoArchivedOriginal

// ErrDocumentNotFound reports that a document ID or archive path does not
// match any ingested document.
var ErrDocumentNotFound = errors.New("document not found")

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
		if r.vault != "" {
			// Vault-relative default (#660): keep the archive inside the
			// active vault so a copied or synced vault stays self-contained.
			// The Paperless importer already uses the same shape with
			// `<vault>/archive/paperless`; ingest follows the same convention
			// under `<vault>/archive/ingest`. A caller that wants a shared
			// archive outside the vault sets Options.Archive explicitly.
			r.archive = filepath.Join(r.vault, "archive", "ingest")
		} else {
			// No vault: fall back to the legacy XDG default so an embedded
			// consumer without a configured vault still works.
			if r.archive, err = defaultDataPath("archive"); err != nil {
				return resolved{}, err
			}
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

// defaultDataPath delegates to the shared internal/config path resolver
// (one symdesk/ app dir under XDG, with legacy symingest fallbacks).
func defaultDataPath(name string) (string, error) {
	return desktopconfig.IngestDataPath(name)
}

// ArchivePath reports where symingest preserves original files, resolved the
// same way the CLI resolves it. The default is inside the configured vault;
// callers that configure an explicit shared archive receive that path instead.
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
func Ingest(ctx context.Context, source string, opts Options) (result *Result, err error) {
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
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close document store: %w", closeErr))
			result = nil
		}
	}()

	pipeline := &ingestengine.Pipeline{
		Engine:     ocr.NewEngine(r.ocrLang, r.ollamaBaseURL, r.ollamaModel),
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: r.vault},
		ArchiveDir: r.archive,
		VaultRoot:  r.vault,
	}

	res, err := pipeline.Ingest(ctx, abs, nil)
	if err != nil {
		return nil, err
	}
	return newResult(res), nil
}

func newResult(res *ingestengine.Result) *Result {
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

	res, err := ingestengine.ExtractText(ctx, abs, kind, ocr.NewEngine(r.ocrLang, r.ollamaBaseURL, r.ollamaModel))
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
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// JobPage is the paged queue response used by the CLI and newer clients.
// Jobs that still expect the legacy top-level array can use Jobs unchanged.
type JobPage struct {
	Jobs   []Job `json:"jobs"`
	Total  int   `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// Jobs lists the most recent queued jobs, newest first, capped at limit
// (a limit of zero or less applies the CLI's default of 100). It preserves the
// legacy array response and now honors opts.Vault when supplied.
func Jobs(ctx context.Context, opts Options, limit int) ([]Job, error) {
	page, err := JobsPage(ctx, opts, limit, 0)
	if err != nil {
		return nil, err
	}
	return page.Jobs, nil
}

// JobsPage lists one vault-scoped page and its total count.
func JobsPage(ctx context.Context, opts Options, limit, offset int) (*JobPage, error) {
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	st, closeStore, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	vaultRoot := opts.Vault
	if vaultRoot != "" {
		if abs, absErr := filepath.Abs(vaultRoot); absErr == nil {
			vaultRoot = filepath.Clean(abs)
		}
	}
	jobs, total, err := st.ListJobsPage(ctx, vaultRoot, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	out := make([]Job, 0, len(jobs))
	for _, j := range jobs {
		job := Job{
			ID: j.ID, DocumentID: j.DocumentID, Status: j.Status,
			Attempts: j.Attempts, Kind: j.Kind, SourcePath: j.SourcePath,
			CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt,
		}
		if j.LastError != nil {
			job.LastError = *j.LastError
		}
		out = append(out, job)
	}
	return &JobPage{Jobs: out, Total: total, Limit: limit, Offset: offset}, nil
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

	poller, err := ingestengine.NewMailPoller(st, accounts, ingestengine.MailPollerOptions{
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

// SplitPDFAtSpec splits input after each page in atSpec ("2,4" or "2-3,6") and
// writes the parts into outputDir, returning their paths in document order.
// It requires the Poppler utilities pdfinfo, pdfseparate and pdfunite.
func SplitPDFAtSpec(ctx context.Context, input, atSpec, outputDir string) ([]string, error) {
	abs, err := filepath.Abs(input)
	if err != nil {
		return nil, fmt.Errorf("invalid split input %q: %w", input, err)
	}
	outDir, err := filepath.Abs(outputDir)
	if err != nil {
		return nil, fmt.Errorf("invalid split output directory %q: %w", outputDir, err)
	}
	return pdfops.NewTools().Split(ctx, abs, atSpec, outDir)
}

// MergePDFs combines two or more PDFs into output without modifying the inputs.
// It requires the Poppler pdfunite utility on PATH and reports a clear error
// when it is absent.
func MergePDFs(ctx context.Context, inputs []string, output string) error {
	absInputs := make([]string, len(inputs))
	for i, input := range inputs {
		abs, err := filepath.Abs(input)
		if err != nil {
			return fmt.Errorf("invalid merge input %q: %w", input, err)
		}
		absInputs[i] = abs
	}
	absOut, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("invalid merge output %q: %w", output, err)
	}
	return pdfops.NewTools().Merge(ctx, absInputs, absOut)
}

// RotatePDF rotates selected pages of input by degrees and writes the result
// to output without modifying the input. An empty pageSpec rotates all pages.
// It requires qpdf on PATH and reports a clear error when it is absent.
func RotatePDF(ctx context.Context, input, output string, degrees int, pageSpec string) error {
	abs, err := filepath.Abs(input)
	if err != nil {
		return fmt.Errorf("invalid rotate input %q: %w", input, err)
	}
	absOut, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("invalid rotate output %q: %w", output, err)
	}
	return pdfops.NewTools().Rotate(ctx, abs, absOut, degrees, pageSpec)
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
func PaperlessMigrate(ctx context.Context, opts PaperlessOptions) (result *PaperlessStats, err error) {
	if opts.BaseURL == "" {
		return nil, fmt.Errorf("BaseURL is required")
	}
	if opts.Token == "" {
		return nil, fmt.Errorf("token is required")
	}

	r, err := opts.resolve()
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
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close document store: %w", closeErr))
			result = nil
		}
	}()

	pipeline := &ingestengine.Pipeline{
		Engine:     ocr.NewEngine(r.ocrLang, r.ollamaBaseURL, r.ollamaModel),
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: r.vault},
		ArchiveDir: r.archive,
		VaultRoot:  r.vault,
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

// Rule is a classification rule, reduced to a facade-level type with JSON
// tags matching symaira-appkit's SymingestRulesContract.ClassificationRule
// (issue #609) — store.ClassificationRule never crosses the package boundary.
type Rule struct {
	ID        int64  `json:"id"`
	Pattern   string `json:"pattern"`
	Kind      string `json:"kind"`
	Value     string `json:"value"`
	CreatedAt string `json:"created_at"`
}

func newRule(r *store.ClassificationRule) Rule {
	return Rule{ID: r.ID, Pattern: r.Pattern, Kind: r.Kind, Value: r.Value, CreatedAt: r.CreatedAt}
}

// RuleMatch is a classification rule that matched a piece of text, reduced to
// the fields the rules-test contract carries — no created_at, matching
// symaira-appkit's ClassificationRuleMatch. Rule is not reused here because
// its extra field would not match the contract byte-for-byte.
type RuleMatch struct {
	ID      int64  `json:"id"`
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
}

// Rules lists every configured classification rule.
func Rules(ctx context.Context, opts Options) ([]Rule, error) {
	st, closeStore, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	rules, err := st.ListRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	out := make([]Rule, 0, len(rules))
	for _, r := range rules {
		out = append(out, newRule(r))
	}
	return out, nil
}

// AddRule adds a new classification rule. kind must be one of "category",
// "tag", "correspondent" or "document_type".
func AddRule(ctx context.Context, opts Options, pattern, kind, value string) (*Rule, error) {
	st, closeStore, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	r, err := st.AddRule(ctx, pattern, kind, value)
	if err != nil {
		return nil, err
	}
	rule := newRule(r)
	return &rule, nil
}

// UpdateRule replaces an existing classification rule's fields.
func UpdateRule(ctx context.Context, opts Options, id int64, pattern, kind, value string) (*Rule, error) {
	st, closeStore, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	r, err := st.UpdateRule(ctx, id, pattern, kind, value)
	if err != nil {
		return nil, err
	}
	rule := newRule(r)
	return &rule, nil
}

// DeleteRule deletes a classification rule by ID.
func DeleteRule(ctx context.Context, opts Options, id int64) error {
	st, closeStore, err := openStore(opts)
	if err != nil {
		return err
	}
	defer closeStore()

	return st.DeleteRule(ctx, id)
}

// TestRules reports which configured rules would match text, using the exact
// case-insensitive substring semantics the pipeline uses to classify a
// document (pipeline.go:296-330) — this is not a second matching rule.
func TestRules(ctx context.Context, opts Options, text string) ([]RuleMatch, error) {
	st, closeStore, err := openStore(opts)
	if err != nil {
		return nil, err
	}
	defer closeStore()

	rules, err := st.ListRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}

	lowerText := strings.ToLower(text)
	var matches []RuleMatch
	for _, r := range rules {
		if strings.Contains(lowerText, strings.ToLower(r.Pattern)) {
			matches = append(matches, RuleMatch{ID: r.ID, Pattern: r.Pattern, Kind: r.Kind, Value: r.Value})
		}
	}
	return matches, nil
}

// ProposedRule is a not-yet-persisted classification rule under test by
// DryRunRule, matching symaira-appkit's ProposedClassificationRule (no id or
// created_at — the rule does not exist yet).
type ProposedRule struct {
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
}

// DryRunMatch is one document a proposed rule would match.
type DryRunMatch struct {
	DocumentID int64  `json:"document_id"`
	NotePath   string `json:"note_path"`
	Title      string `json:"title"`
	// MatchedRuleIDs is always [0], a placeholder for the proposed rule,
	// which has no ID of its own since it is never persisted. The field
	// exists for parity with symaira-appkit's RulesDryRunMatch contract.
	MatchedRuleIDs []int64 `json:"matched_rule_ids"`
}

// DryRunSkipped is one document DryRunRule could not evaluate.
type DryRunSkipped struct {
	DocumentID int64  `json:"document_id"`
	NotePath   string `json:"note_path"`
	Reason     string `json:"reason"`
}

// DryRunResult reports which already-ingested documents a proposed
// classification rule would match, without persisting the rule or touching
// any document.
type DryRunResult struct {
	ProposedRule     ProposedRule    `json:"proposed_rule"`
	VaultPath        string          `json:"vault_path"`
	TotalDocuments   int             `json:"total_documents"`
	MatchedDocuments int             `json:"matched_documents"`
	SkippedDocuments int             `json:"skipped_documents"`
	Matches          []DryRunMatch   `json:"matches"`
	Skipped          []DryRunSkipped `json:"skipped"`
}

// DryRunRule validates a proposed classification rule and reports which
// already-ingested documents its pattern would match by reading each
// document's note file under the vault — never persisting the rule. A
// document whose note file is missing or unreadable is reported in Skipped
// with a reason rather than silently dropped.
func DryRunRule(ctx context.Context, opts Options, pattern, kind, value string) (*DryRunResult, error) {
	if err := store.ValidateClassificationRule(pattern, kind, value); err != nil {
		return nil, err
	}
	r, err := opts.resolve()
	if err != nil {
		return nil, err
	}

	st, err := store.Open(r.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open document store: %w", err)
	}
	defer func() { _ = st.Close() }()

	docs, err := st.ListDocuments(ctx)
	if err != nil {
		return nil, fmt.Errorf("list documents: %w", err)
	}

	result := &DryRunResult{
		ProposedRule:   ProposedRule{Pattern: pattern, Kind: kind, Value: value},
		VaultPath:      r.vault,
		TotalDocuments: len(docs),
		Matches:        []DryRunMatch{},
		Skipped:        []DryRunSkipped{},
	}

	patternLower := strings.ToLower(pattern)
	for _, doc := range docs {
		var notePath string
		if doc.VaultPath != nil {
			notePath = *doc.VaultPath
		}
		if notePath == "" {
			result.Skipped = append(result.Skipped, DryRunSkipped{DocumentID: doc.ID, Reason: "no note path recorded"})
			continue
		}
		// #nosec G304 -- notePath is the note location this pipeline
		// recorded for the document, under the configured vault root.
		data, readErr := os.ReadFile(notePath)
		if readErr != nil {
			result.Skipped = append(result.Skipped, DryRunSkipped{DocumentID: doc.ID, NotePath: notePath, Reason: readErr.Error()})
			continue
		}
		if !strings.Contains(strings.ToLower(noteBody(string(data))), patternLower) {
			continue
		}
		title := strings.TrimSuffix(filepath.Base(notePath), filepath.Ext(notePath))
		result.Matches = append(result.Matches, DryRunMatch{
			DocumentID:     doc.ID,
			NotePath:       notePath,
			Title:          title,
			MatchedRuleIDs: []int64{0},
		})
	}
	result.MatchedDocuments = len(result.Matches)
	result.SkippedDocuments = len(result.Skipped)
	return result, nil
}

// noteBody strips the YAML frontmatter writer.NoteWriter always emits
// ("---\n<yaml>---\n\n<body>"), so DryRunRule matches against the same
// extracted text the pipeline classified rather than incidental frontmatter.
func noteBody(content string) string {
	const delim = "---\n"
	if !strings.HasPrefix(content, delim) {
		return content
	}
	rest := content[len(delim):]
	idx := strings.Index(rest, "\n---\n")
	if idx == -1 {
		return content
	}
	return strings.TrimPrefix(rest[idx+len("\n---\n"):], "\n")
}

// MailAccountRecord is the full mail-account representation used by the
// `symdesk mail rules` CLI surface, matching symaira-appkit's
// SymingestRulesContract.MailAccount field-for-field. It is distinct from
// MailAccount (used by MailAccounts/FetchMail) because that type is reduced
// to what the mail poller displays and omits the filter/action fields a
// client editing an account needs to round-trip.
type MailAccountRecord struct {
	ID             string   `json:"id"`
	Host           string   `json:"host"`
	Port           int      `json:"port"`
	Username       string   `json:"username"`
	PasswordSecret string   `json:"password_secret"`
	Folder         string   `json:"folder"`
	From           []string `json:"from"`
	Subject        []string `json:"subject"`
	HasAttachment  bool     `json:"has_attachment"`
	Action         string   `json:"action"`
	MoveTo         string   `json:"move_to"`
	ArchiveMail    bool     `json:"archive_mail"`
}

func toIMAPAccount(a MailAccountRecord) config.IMAPAccount {
	return config.IMAPAccount{
		Host:           a.Host,
		Port:           a.Port,
		Username:       a.Username,
		PasswordSecret: a.PasswordSecret,
		Folder:         a.Folder,
		From:           a.From,
		Subject:        a.Subject,
		HasAttachment:  a.HasAttachment,
		Action:         a.Action,
		MoveTo:         a.MoveTo,
		ArchiveMail:    a.ArchiveMail,
	}
}

func fromIMAPAccount(a config.IMAPAccount) MailAccountRecord {
	view := config.ViewAccount(a)
	return MailAccountRecord{
		ID:             view.ID,
		Host:           view.Host,
		Port:           view.Port,
		Username:       view.Username,
		PasswordSecret: view.PasswordSecret,
		Folder:         view.Folder,
		From:           view.From,
		Subject:        view.Subject,
		HasAttachment:  view.HasAttachment,
		Action:         view.Action,
		MoveTo:         view.MoveTo,
		ArchiveMail:    view.ArchiveMail,
	}
}

func viewAccounts(accounts []config.IMAPAccount) []MailAccountRecord {
	out := make([]MailAccountRecord, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, fromIMAPAccount(a))
	}
	return out
}

// MailConfigurationResult is what every mail-account facade write/read
// function below returns, matching symaira-appkit's
// SymingestRulesContract.MailConfigurationResponse minus schema_version and
// operation, which the CLI attaches (they vary per call site, not per type).
type MailConfigurationResult struct {
	ConfigPath     string              `json:"config_path"`
	Accounts       []MailAccountRecord `json:"accounts"`
	ReloadRequired bool                `json:"reload_required"`
	Warnings       []string            `json:"warnings"`
}

// readMailConfigLenient treats a missing configuration file as an empty
// document rather than an error, for callers (ListMailAccounts,
// CreateMailAccount) where "nothing configured yet" is a normal starting
// state rather than a failure.
func readMailConfigLenient(path string) (*config.MailConfigDocument, error) {
	doc, err := config.ReadMailConfig(path)
	if err != nil {
		if errors.Is(err, config.ErrConfigNotFound) {
			return &config.MailConfigDocument{Path: path}, nil
		}
		return nil, fmt.Errorf("read mail configuration: %w", err)
	}
	return doc, nil
}

// writeAccountsAt validates and writes accounts to an already-resolved path,
// then returns them read back and masked.
func writeAccountsAt(path string, accounts []config.IMAPAccount, pollInterval string) (*MailConfigurationResult, error) {
	validation := config.ValidateMailAccounts(accounts, pollInterval)
	if !validation.Valid {
		return nil, fmt.Errorf("invalid mail configuration: %s", strings.Join(validation.Errors, "; "))
	}
	if err := config.WriteMailConfig(path, accounts, pollInterval); err != nil {
		return nil, fmt.Errorf("write mail configuration: %w", err)
	}
	return &MailConfigurationResult{
		ConfigPath:     path,
		Accounts:       viewAccounts(accounts),
		ReloadRequired: true,
		Warnings:       validation.Warnings,
	}, nil
}

// ListMailAccounts reads every configured mail account for the `mail rules
// list` CLI surface. A missing configuration file is not an error — mail
// ingestion is optional — so the result is an empty account list.
func ListMailAccounts(configPath string) (*MailConfigurationResult, error) {
	path, err := config.ConfigPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mail config path: %w", err)
	}
	doc, err := readMailConfigLenient(path)
	if err != nil {
		return nil, err
	}
	return &MailConfigurationResult{
		ConfigPath:     path,
		Accounts:       viewAccounts(doc.Accounts),
		ReloadRequired: false,
		Warnings:       []string{},
	}, nil
}

// SetMailAccounts validates and replaces the entire configured account list
// at configPath. An empty configPath uses the location the CLI would use.
//
// Like UpdateMailAccount, it never writes a masked value back over a stored
// secret: an account whose PasswordSecret is empty or equals the mask of the
// secret currently stored under the same ID keeps that stored secret. A
// caller that reads accounts (which are masked) and writes the list back
// unchanged must not silently destroy every password.
func SetMailAccounts(configPath string, accounts []MailAccountRecord, pollInterval string) (*MailConfigurationResult, error) {
	path, err := config.ConfigPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mail config path: %w", err)
	}
	existing, err := readMailConfigLenient(path)
	if err != nil {
		return nil, err
	}
	converted := make([]config.IMAPAccount, len(accounts))
	for i, a := range accounts {
		converted[i] = preserveStoredSecret(toIMAPAccount(a), existing.Accounts)
	}
	return writeAccountsAt(path, converted, pollInterval)
}

// preserveStoredSecret returns account with its PasswordSecret restored from
// the matching stored account when the caller supplied nothing or echoed the
// mask back. Returns it unchanged when the caller supplied a real new secret
// or when no stored account shares its ID.
func preserveStoredSecret(account config.IMAPAccount, stored []config.IMAPAccount) config.IMAPAccount {
	idx := indexOfAccount(stored, config.AccountID(account))
	if idx == -1 {
		return account
	}
	prior := stored[idx].PasswordSecret
	if account.PasswordSecret == "" || account.PasswordSecret == config.MaskPasswordSecret(prior) {
		account.PasswordSecret = prior
	}
	return account
}

// CreateMailAccount appends a new IMAP account to the mail configuration.
// account.PasswordSecret is stored as given: this is a new account, so there
// is no prior stored secret to preserve.
func CreateMailAccount(configPath string, account MailAccountRecord) (*MailConfigurationResult, error) {
	path, err := config.ConfigPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mail config path: %w", err)
	}
	doc, err := readMailConfigLenient(path)
	if err != nil {
		return nil, err
	}
	accounts := append(doc.Accounts, toIMAPAccount(account))
	return writeAccountsAt(path, accounts, doc.IMAPPollInterval)
}

// UpdateMailAccount replaces the fields of the configured account identified
// by id (as returned by ListMailAccounts). Never overwrites the stored
// secret with a masked value: when account.PasswordSecret is empty or equals
// the mask of the account's current secret — the value a client round-trips
// unchanged after reading it — the existing stored secret is kept.
func UpdateMailAccount(configPath, id string, account MailAccountRecord) (*MailConfigurationResult, error) {
	path, err := config.ConfigPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mail config path: %w", err)
	}
	doc, err := config.ReadMailConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read mail configuration: %w", err)
	}
	idx := indexOfAccount(doc.Accounts, id)
	if idx == -1 {
		return nil, fmt.Errorf("no configured mail account with ID %q", id)
	}

	existing := doc.Accounts[idx]
	updated := toIMAPAccount(account)
	if updated.PasswordSecret == "" || updated.PasswordSecret == config.MaskPasswordSecret(existing.PasswordSecret) {
		updated.PasswordSecret = existing.PasswordSecret
	}
	doc.Accounts[idx] = updated
	return writeAccountsAt(path, doc.Accounts, doc.IMAPPollInterval)
}

// DeleteMailAccount removes the configured account identified by id.
func DeleteMailAccount(configPath, id string) (*MailConfigurationResult, error) {
	path, err := config.ConfigPath(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve mail config path: %w", err)
	}
	doc, err := config.ReadMailConfig(path)
	if err != nil {
		return nil, fmt.Errorf("read mail configuration: %w", err)
	}
	idx := indexOfAccount(doc.Accounts, id)
	if idx == -1 {
		return nil, fmt.Errorf("no configured mail account with ID %q", id)
	}
	accounts := append(append([]config.IMAPAccount{}, doc.Accounts[:idx]...), doc.Accounts[idx+1:]...)
	return writeAccountsAt(path, accounts, doc.IMAPPollInterval)
}

func indexOfAccount(accounts []config.IMAPAccount, id string) int {
	for i, a := range accounts {
		if config.AccountID(a) == id {
			return i
		}
	}
	return -1
}

// ReprocessResult reports the outcome of a re-OCR run against an existing
// document — a facade-level reduction of ingestengine.ReprocessResult that
// never exposes store.Job.
type ReprocessResult struct {
	DocumentID int64  `json:"document_id"`
	JobID      int64  `json:"job_id"`
	Status     string `json:"status"` // "completed" or "already_running"
	OutputPath string `json:"output_path"`
}

func newReprocessResult(documentID int64, res *ingestengine.ReprocessResult) *ReprocessResult {
	out := &ReprocessResult{DocumentID: documentID}
	if res.Job != nil {
		out.JobID = res.Job.ID
	}
	if res.AlreadyRunning {
		out.Status = "already_running"
		return out
	}
	out.Status = "completed"
	if res.Result != nil {
		out.OutputPath = res.Result.VaultPath
	}
	return out
}

func newReprocessPipeline(r resolved, st *store.Store) *ingestengine.Pipeline {
	return &ingestengine.Pipeline{
		Engine:     ocr.NewEngine(r.ocrLang, r.ollamaBaseURL, r.ollamaModel),
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: r.vault},
		ArchiveDir: r.archive,
		VaultRoot:  r.vault,
	}
}

// Reprocess re-runs OCR/extraction for an already-ingested document,
// wrapping Pipeline.Reprocess (the reocr job kind). A document ID with no
// matching document returns ErrDocumentNotFound; a document with no archived
// original returns ErrNoArchivedOriginal. A pending or running reprocess job
// for the same document is reported with Status "already_running" rather
// than duplicated.
func Reprocess(ctx context.Context, opts Options, documentID int64) (*ReprocessResult, error) {
	r, err := opts.resolve()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(r.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open document store: %w", err)
	}
	defer func() { _ = st.Close() }()

	res, err := newReprocessPipeline(r, st).Reprocess(ctx, documentID, "", nil)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: id %d", ErrDocumentNotFound, documentID)
		}
		return nil, err
	}
	return newReprocessResult(documentID, res), nil
}

// archivePathCandidates returns the stored-path and physical-path forms a caller
// may have supplied. New notes store the vault-relative form; legacy notes and
// explicit shared archives may still store an absolute form.
func archivePathCandidates(r resolved, input string) ([]string, error) {
	if input == "" {
		return nil, fmt.Errorf("archive path is empty")
	}

	candidates := make([]string, 0, 3)
	appendCandidate := func(candidate string) {
		for _, existing := range candidates {
			if existing == candidate {
				return
			}
		}
		candidates = append(candidates, candidate)
	}

	if filepath.IsAbs(input) {
		abs, err := filepath.Abs(input)
		if err != nil {
			return nil, fmt.Errorf("invalid archive path %q: %w", input, err)
		}
		if r.vault != "" {
			vaultRoot, rootErr := filepath.Abs(r.vault)
			if rootErr != nil {
				return nil, fmt.Errorf("invalid vault path %q: %w", r.vault, rootErr)
			}
			rel, relErr := filepath.Rel(vaultRoot, abs)
			if relErr == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
				appendCandidate(filepath.ToSlash(rel))
			}
		}
		appendCandidate(filepath.Clean(abs))
		return candidates, nil
	}

	rel := filepath.Clean(filepath.FromSlash(input))
	appendCandidate(filepath.ToSlash(rel))
	if r.vault != "" {
		physical, err := filepath.Abs(filepath.Join(r.vault, rel))
		if err != nil {
			return nil, fmt.Errorf("invalid archive path %q: %w", input, err)
		}
		appendCandidate(filepath.Clean(physical))
	} else {
		physical, err := filepath.Abs(rel)
		if err != nil {
			return nil, fmt.Errorf("invalid archive path %q: %w", input, err)
		}
		appendCandidate(filepath.Clean(physical))
	}
	return candidates, nil
}

// ReprocessByArchivePath is Reprocess for a caller that has the archived
// original's path rather than the document ID — the CLI's positional
// argument form of `ingest reocr`.
func ReprocessByArchivePath(ctx context.Context, opts Options, archivePath string) (*ReprocessResult, error) {
	r, err := opts.resolve()
	if err != nil {
		return nil, err
	}
	candidates, err := archivePathCandidates(r, archivePath)
	if err != nil {
		return nil, err
	}

	st, err := store.Open(r.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open document store: %w", err)
	}
	defer func() { _ = st.Close() }()

	var doc *store.Document
	for _, candidate := range candidates {
		doc, err = st.ByArchivePath(ctx, candidate)
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("look up document by archive path: %w", err)
		}
	}
	if doc == nil {
		return nil, fmt.Errorf("%w: archive path %q", ErrDocumentNotFound, archivePath)
	}

	res, err := newReprocessPipeline(r, st).Reprocess(ctx, doc.ID, "", nil)
	if err != nil {
		return nil, err
	}
	return newReprocessResult(doc.ID, res), nil
}
