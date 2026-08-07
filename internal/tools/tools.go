package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/health"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

// Capability classifies a tool according to whether it can mutate vault state.
type Capability string

const (
	ReadOnly Capability = "read_only"
	Mutating Capability = "mutating"
)

// Handler is the common invocation contract shared by the MCP server and the
// in-process AI loop. The input is the raw JSON arguments object.
type Handler func(context.Context, json.RawMessage) (any, error)

// Tool is the canonical description of one SymDesk tool. MCP-specific
// transport adapters should consume this type rather than redefining tool
// metadata or handlers.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Handler     Handler
	ReadOnly    bool
}

// Definition is an alternate descriptive name for Tool for callers that
// prefer to talk about registry entries.
type Definition = Tool

// ServiceFactory opens a fresh service and sidecar for one request. The
// caller owns and must close the returned database.
type ServiceFactory func() (*service.Service, *sidecar.DB, error)

// RegistryOptions supplies runtime dependencies used to construct handlers.
type RegistryOptions struct {
	Config        *config.Config
	GetService    ServiceFactory
	ServerVersion string
	AllowWrite    bool
}

// Registry is the canonical, ordered tool catalog. The order is stable so
// transports preserve the existing MCP tools/list response order.
type Registry struct {
	tools  []Tool
	byName map[string]Tool
}

// NewRegistry constructs the complete tool catalog, including mutating tools.
// Consumers decide which entries are enabled for their surface.
func NewRegistry(options RegistryOptions) *Registry {
	cfg := options.Config
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	entry := func(readOnly bool, tool *Tool) Tool {
		tool.ReadOnly = readOnly
		return *tool
	}
	entries := []Tool{
		entry(true, newStatusTool(cfg, options.AllowWrite, options.ServerVersion)),
		entry(true, newLsTool(options.GetService)),
		entry(true, newSearchTool(options.GetService)),
		entry(true, newPropsTool(options.GetService)),
		entry(true, newBacklinksTool(options.GetService)),
		entry(true, newAskTool(options.GetService)),
		entry(true, newTransformTool(cfg)),
		entry(true, newDocsTool(options.GetService)),
		entry(true, newDocsReviewTool(options.GetService, cfg)),
		entry(true, newDocsSimilarTool(options.GetService)),
		entry(true, newVaultHealthTool(options.GetService)),
		entry(true, newRelatedTool(options.GetService)),
		entry(true, newIngestJobsTool(options.GetService)),
		entry(true, newMeetingListTool(options.GetService)),
		entry(true, newMeetingGetTool(options.GetService)),
		entry(false, newUndoTaskTool(options.GetService)),
		entry(false, newNoteNewTool(options.GetService)),
		entry(false, newIngestTool(options.GetService)),
		entry(false, newDocSetStatusTool(options.GetService)),
		entry(false, newIngestRetryTool(options.GetService)),
		entry(false, newClipTool(options.GetService)),
		entry(false, newExportTool(options.GetService)),
		entry(false, newAutofillTool(options.GetService)),
		entry(false, newMeetingImportTool(options.GetService)),
	}
	registry := &Registry{tools: make([]Tool, 0, len(entries)), byName: make(map[string]Tool, len(entries))}
	for _, entry := range entries {
		registry.tools = append(registry.tools, entry)
		registry.byName[entry.Name] = entry
	}
	return registry
}

// Definitions returns the complete ordered catalog for callers that do not
// need registry lookup operations.
func Definitions(options RegistryOptions) []Tool {
	return NewRegistry(options).All()
}

// All returns the complete ordered catalog.
func (r *Registry) All() []Tool {
	if r == nil {
		return nil
	}
	entries := make([]Tool, len(r.tools))
	copy(entries, r.tools)
	return entries
}

// Enabled returns the catalog entries allowed for a read-only or read-write
// consumer while preserving canonical order.
func (r *Registry) Enabled(allowWrite bool) []Tool {
	if r == nil {
		return nil
	}
	if allowWrite {
		return r.All()
	}
	entries := make([]Tool, 0, len(r.tools))
	for _, entry := range r.tools {
		if entry.ReadOnly {
			entries = append(entries, entry)
		}
	}
	return entries
}

// Lookup returns a copy of the named canonical entry.
func (r *Registry) Lookup(name string) (Tool, bool) {
	if r == nil {
		return Tool{}, false
	}
	entry, ok := r.byName[name]
	return entry, ok
}

// Capabilities derives the legacy name-to-capability view from the canonical
// registry. It is useful to compatibility callers while keeping the registry
// as the source of truth.
func Capabilities() map[string]Capability {
	capabilities := make(map[string]Capability)
	for _, entry := range NewRegistry(RegistryOptions{}).All() {
		if entry.ReadOnly {
			capabilities[entry.Name] = ReadOnly
		} else {
			capabilities[entry.Name] = Mutating
		}
	}
	return capabilities
}

func newStatusTool(cfg *config.Config, allowWrite bool, version string) *Tool {
	return &Tool{
		Name:        "desk_status",
		Description: "Returns the current version and vault path configuration for symdesk.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			status := map[string]string{
				"version": version,
				"vault":   cfg.Vault,
			}
			if allowWrite {
				status["capabilities"] = "read_write"
			} else {
				status["capabilities"] = "read_only"
			}
			return status, nil
		},
	}
}

func newLsTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_ls",
		Description: "Lists files in the vault.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"dir":{"type":"string"}}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Dir string `json:"dir"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.Ls(args.Dir)
		},
	}
}

func newSearchTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_search",
		Description: "Searches notes with full-text terms plus path:, tag:, type:, status:, quoted phrases, -negation and /regex/. Invalid syntax falls back to plain full-text and returns a hint.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Query == "" {
				return nil, fmt.Errorf("query is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.SearchWithMeta(args.Query)
		},
	}
}

func newPropsTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_props",
		Description: "Gets properties (frontmatter) for a note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				File string `json:"file"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.File == "" {
				return nil, fmt.Errorf("file is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.Props(args.File)
		},
	}
}

func newBacklinksTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_backlinks",
		Description: "Gets backlinks for a note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				File string `json:"file"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.File == "" {
				return nil, fmt.Errorf("file is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.Backlinks(args.File)
		},
	}
}

func newNoteNewTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_note_new",
		Description: "Create a new note in the Symaira vault.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string","description":"The title of the new note"},"content":{"type":"string","description":"The Markdown body content of the note"},"template":{"type":"string","description":"Optional template name to use"}},"required":["title","content"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Title    string `json:"title"`
				Content  string `json:"content"`
				Template string `json:"template"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Title == "" {
				return nil, fmt.Errorf("title is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			path, err := svc.NoteNew(args.Title, args.Content, args.Template)
			if err != nil {
				return nil, err
			}
			return map[string]string{"path": path}, nil
		},
	}
}

// newAskTool answers a question grounded in vault search results. MCP has
// no streaming result, so the chunks are aggregated into one answer.
func newAskTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_ask",
		Description: "Asks the AI a question about the vault. Uses a local Ollama instance when configured; otherwise returns the top search results with a note that AI is not configured. The answer is returned as one aggregated text (no streaming).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Query == "" {
				return nil, fmt.Errorf("query is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			answer, err := svc.AskText(ctx, args.Query)
			if err != nil {
				return nil, err
			}
			return map[string]string{"answer": answer}, nil
		},
	}
}

// newIngestTool copies a document into the vault inbox and creates a stub
// note per the vault contract.
//
// newTransformTool applies a local AI action (summarize, rewrite, continue) to
// a piece of text. It operates purely on the given text and never touches the
// vault. MCP has no streaming result, so the chunks are aggregated.
func newTransformTool(configs ...*config.Config) *Tool {
	cfg := config.DefaultConfig()
	if len(configs) > 0 && configs[0] != nil {
		cfg = configs[0]
	}
	return &Tool{
		Name:        "desk_transform",
		Description: "Transforms the given text with a local AI action. intent is one of summarize, rewrite or continue. Uses a local Ollama instance when configured; otherwise returns a note that AI is not configured. The result is returned as one aggregated text (no streaming).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"},"intent":{"type":"string","enum":["summarize","rewrite","continue"]}},"required":["text","intent"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Text   string `json:"text"`
				Intent string `json:"intent"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Text == "" {
				return nil, fmt.Errorf("text is required")
			}

			chunks := make(chan ai.AskChunk)
			go ai.Transform(ctx, cfg, args.Text, args.Intent, chunks)
			var b strings.Builder
			for c := range chunks {
				b.WriteString(c.Chunk)
			}
			return map[string]string{"result": b.String()}, nil
		},
	}
}

func newIngestTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_ingest",
		Description: "Ingests a file into the vault: copies it into inbox/ and creates a corresponding markdown note. Takes an absolute source path, returns the relative path of the new note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"source_path":{"type":"string"}},"required":["source_path"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				SourcePath string `json:"source_path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.SourcePath == "" {
				return nil, fmt.Errorf("source_path is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.Ingest(args.SourcePath)
		},
	}
}

func newDocsTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_docs",
		Description: "Lists indexed documents with optional filters (status, person, correspondent, type, year, due-before, min/max confidence). Returns structured document metadata.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"type":{"type":"string"},"status":{"type":"string"},"person":{"type":"string"},"correspondent":{"type":"string"},"year":{"type":"string"},"due_before":{"type":"string"},"min_confidence":{"type":"integer"},"max_confidence":{"type":"integer"}}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Type          string `json:"type"`
				Status        string `json:"status"`
				Person        string `json:"person"`
				Correspondent string `json:"correspondent"`
				Year          string `json:"year"`
				DueBefore     string `json:"due_before"`
				MinConfidence *int   `json:"min_confidence"`
				MaxConfidence *int   `json:"max_confidence"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()

			f := sidecar.DocsFilter{
				Type:          args.Type,
				Status:        args.Status,
				Person:        args.Person,
				Correspondent: args.Correspondent,
				Year:          args.Year,
				DueBefore:     args.DueBefore,
				MinConfidence: args.MinConfidence,
				MaxConfidence: args.MaxConfidence,
			}
			return svc.DocsList(f)
		},
	}
}

func newDocSetStatusTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "doc_set_status",
		Description: "Sets the status of a document (open|paid|submitted|done|needs_review|waiting_for_reply). Updates frontmatter and re-indexes.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"},"status":{"type":"string"}},"required":["file","status"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				File   string `json:"file"`
				Status string `json:"status"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.File == "" || args.Status == "" {
				return nil, fmt.Errorf("file and status are required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			if err := svc.DocStatus(args.File, args.Status); err != nil {
				return nil, err
			}
			return map[string]string{"status": "updated", "file": args.File, "new_status": args.Status}, nil
		},
	}
}

func newDocsReviewTool(getService ServiceFactory, cfg *config.Config) *Tool {
	return &Tool{
		Name:        "docs_review",
		Description: "Returns documents needing review: confidence below threshold or missing document_type/document_date.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"threshold":{"type":"integer"}}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Threshold int `json:"threshold"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			threshold := cfg.ReviewThreshold
			if args.Threshold > 0 {
				threshold = args.Threshold
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.DocsReview(threshold)
		},
	}
}

func newDocsSimilarTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "docs_similar",
		Description: "Finds near-duplicate documents using SimHash similarity. Returns documents with similarity >= threshold.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"},"threshold":{"type":"integer"}},"required":["file"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				File      string `json:"file"`
				Threshold int    `json:"threshold"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.File == "" {
				return nil, fmt.Errorf("file is required")
			}
			if args.Threshold <= 0 {
				args.Threshold = 50
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.SimilarDocs(args.File, args.Threshold)
		},
	}
}

func newVaultHealthTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "vault_health",
		Description: "Scans the Markdown vault for parse errors, missing frontmatter, broken wikilinks and near-duplicate documents, returning a reviewable repair plan without changing files.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"duplicate_threshold":{"type":"integer"}}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				DuplicateThreshold int `json:"duplicate_threshold"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.DuplicateThreshold <= 0 {
				args.DuplicateThreshold = 90
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return health.Scan(svc.VaultRoot, db, args.DuplicateThreshold)
		},
	}
}

func newExportTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_export",
		Description: "Exports a note or view to PDF or HTML. Provide either note or view, not both.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"note":{"type":"string","description":"vault-relative note path"},"view":{"type":"string","description":"view id"},"output":{"type":"string","description":"output file path"},"format":{"type":"string","enum":["pdf","html"]},"profile":{"type":"string","description":"symprint profile for PDF"}},"oneOf":[{"required":["note"]},{"required":["view"]}]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Note    string `json:"note"`
				View    string `json:"view"`
				Output  string `json:"output"`
				Format  string `json:"format"`
				Profile string `json:"profile"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Note == "" && args.View == "" {
				return nil, fmt.Errorf("note or view is required")
			}
			if args.Format == "" {
				args.Format = "pdf"
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.Export(args.Note, args.View, args.Output, args.Format, args.Profile)
		},
	}
}

func newAutofillTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_autofill",
		Description: "Autofills a frontmatter property on all notes matching a view using the configured AI provider.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"view":{"type":"string","description":"view id"},"property":{"type":"string","description":"frontmatter property to fill"},"prompt":{"type":"string","description":"extra instruction for the AI"},"dry_run":{"type":"boolean","description":"show changes without writing"}},"required":["view","property"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				View     string `json:"view"`
				Property string `json:"property"`
				Prompt   string `json:"prompt"`
				DryRun   bool   `json:"dry_run"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.View == "" || args.Property == "" {
				return nil, fmt.Errorf("view and property are required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.Autofill(args.View, args.Property, args.Prompt, args.DryRun)
		},
	}
}

func newRelatedTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_related",
		Description: "Gets related entities and notes for a given file path based on composition with symmemory.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				File string `json:"file"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.File == "" {
				return nil, fmt.Errorf("file is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.Related(args.File)
		},
	}
}

func newIngestJobsTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_ingest_jobs",
		Description: "Lists ingestion jobs in the queue from symingest.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			jobsStr, err := svc.IngestJobs()
			if err != nil {
				return nil, err
			}
			var jobs []any
			if err := json.Unmarshal([]byte(jobsStr), &jobs); err != nil {
				return nil, err
			}
			return jobs, nil
		},
	}
}

func newIngestRetryTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_ingest_retry",
		Description: "Retries a failed ingestion job by ID.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			err = svc.IngestRetry(args.ID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"status": "ok"}, nil
		},
	}
}

func newClipTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_clip",
		Description: "Fetches a URL via symfetch and saves it as a note in the vault.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.URL == "" {
				return nil, fmt.Errorf("url is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			path, err := svc.NoteClip(args.URL)
			if err != nil {
				return nil, err
			}
			return map[string]string{"path": path}, nil
		},
	}
}

func newMeetingListTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "meeting_list",
		Description: "Lists meetings already imported into the vault as reviewed meeting notes.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }() //nolint:errcheck // matches every other read-only tool in this file
			return svc.MeetingList()
		},
	}
}

func newMeetingGetTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "meeting_get",
		Description: "Gets one imported meeting note by its vault-relative path, including the reviewed transcript.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"vault-relative meeting note path"}},"required":["path"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Path == "" {
				return nil, fmt.Errorf("path is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }() //nolint:errcheck // matches every other read-only tool in this file
			return svc.MeetingShow(args.Path)
		},
	}
}

// newUndoTaskTool rejects a whole agent run as one unit: restores every
// file recorded in the task's checkpoint and deletes files the task
// created (issue #405). Mutating — only exposed on write-enabled surfaces.
func newUndoTaskTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_undo_task",
		Description: "Rejects an agent run as a unit: restores every file that existed before the task and deletes files the task created. Takes the task id from a prior checkpoint.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"task_id":{"type":"string","description":"the checkpoint task id to undo"}},"required":["task_id"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				TaskID string `json:"task_id"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.TaskID == "" {
				return nil, fmt.Errorf("task_id is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }() //nolint:errcheck // matches every other mutating tool in this file
			cp, err := svc.CheckpointUndo(args.TaskID)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"status":   "undone",
				"task_id":  cp.TaskID,
				"restored": len(cp.Files),
				"deleted":  len(cp.NewFiles),
				"skipped":  cp.Skipped,
				"partial":  cp.Partial(),
			}, nil
		},
	}
}

// newMeetingImportTool is a reviewed mutation: it is only registered when
// allowWrite is set, exactly like the vault's other note-creating tools.
func newMeetingImportTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "meeting_import",
		Description: "Imports one SymMeet meeting into the vault as a contract-v2 meeting note. Requires symmeet on PATH with a compatible artifact schema.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"meeting_id":{"type":"string"}},"required":["meeting_id"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				MeetingID string `json:"meeting_id"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.MeetingID == "" {
				return nil, fmt.Errorf("meeting_id is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }() //nolint:errcheck // matches every other mutating tool in this file
			path, err := svc.MeetingImport(args.MeetingID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"path": path, "status": "imported"}, nil
		},
	}
}
