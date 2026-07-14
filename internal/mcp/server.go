package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

var ServerVersion = "0.6.10"

// ToolCapability classifies an MCP tool as read-only or mutating.
type ToolCapability string

const (
	ReadOnly ToolCapability = "read_only"
	Mutating ToolCapability = "mutating"
)

// ToolCapabilities maps tool names to their mutation capability.
var ToolCapabilities = map[string]ToolCapability{
	"desk_status":       ReadOnly,
	"desk_ls":           ReadOnly,
	"desk_search":       ReadOnly,
	"desk_props":        ReadOnly,
	"desk_backlinks":    ReadOnly,
	"desk_ask":          ReadOnly,
	"desk_transform":    ReadOnly,
	"desk_docs":         ReadOnly,
	"docs_review":       ReadOnly,
	"docs_similar":      ReadOnly,
	"desk_related":      ReadOnly,
	"desk_ingest_jobs":  ReadOnly,
	"desk_note_new":     Mutating,
	"desk_ingest":       Mutating,
	"doc_set_status":    Mutating,
	"desk_ingest_retry": Mutating,
	"desk_clip":         Mutating,
	"desk_export":       Mutating,
	"desk_autofill":     Mutating,
}

func StartServer(cfg *config.Config, version string, allowWrite bool) error {
	if version != "" {
		ServerVersion = version
	}

	server := mcpserver.New("symdesk", ServerVersion)

	var getService serviceFactory = func() (*service.Service, *sidecar.DB, error) {
		vRoot, err := vault.ResolveVaultRoot("", cfg)
		if err != nil {
			return nil, nil, err
		}
		db, err := sidecar.OpenForVault(vRoot)
		if err != nil {
			return nil, nil, err
		}
		return service.New(vRoot, db), db, nil
	}

	server.RegisterTool(newStatusTool(cfg, allowWrite))
	server.RegisterTool(newLsTool(getService))
	server.RegisterTool(newSearchTool(getService))
	server.RegisterTool(newPropsTool(getService))
	server.RegisterTool(newBacklinksTool(getService))
	server.RegisterTool(newAskTool(getService))
	server.RegisterTool(newTransformTool())
	server.RegisterTool(newDocsTool(getService))
	server.RegisterTool(newDocsReviewTool(getService, cfg))
	server.RegisterTool(newDocsSimilarTool(getService))
	server.RegisterTool(newRelatedTool(getService))
	server.RegisterTool(newIngestJobsTool(getService))

	if allowWrite {
		server.RegisterTool(newNoteNewTool(getService))
		server.RegisterTool(newIngestTool(getService))
		server.RegisterTool(newDocSetStatusTool(getService))
		server.RegisterTool(newIngestRetryTool(getService))
		server.RegisterTool(newClipTool(getService))
		server.RegisterTool(newExportTool(getService))
		server.RegisterTool(newAutofillTool(getService))
	}

	return server.ServeStdio(context.Background())
}

// serviceFactory opens a fresh service + sidecar per request; the caller
// closes the returned DB.
type serviceFactory func() (*service.Service, *sidecar.DB, error)

func newStatusTool(cfg *config.Config, allowWrite bool) *mcpserver.Tool {
	return &mcpserver.Tool{
		Name:        "desk_status",
		Description: "Returns the current version and vault path configuration for symdesk.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			status := map[string]string{
				"version": ServerVersion,
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

func newLsTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.Ls(args.Dir)
		},
	}
}

func newSearchTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.SearchWithMeta(args.Query)
		},
	}
}

func newPropsTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.Props(args.File)
		},
	}
}

func newBacklinksTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.Backlinks(args.File)
		},
	}
}

func newNoteNewTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
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
func newAskTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
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
func newTransformTool() *mcpserver.Tool {
	return &mcpserver.Tool{
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
			go ai.Transform(ctx, args.Text, args.Intent, chunks)
			var b strings.Builder
			for c := range chunks {
				b.WriteString(c.Chunk)
			}
			return map[string]string{"result": b.String()}, nil
		},
	}
}

func newIngestTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.Ingest(args.SourcePath)
		},
	}
}

func newDocsTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()

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

func newDocSetStatusTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			if err := svc.DocStatus(args.File, args.Status); err != nil {
				return nil, err
			}
			return map[string]string{"status": "updated", "file": args.File, "new_status": args.Status}, nil
		},
	}
}

func newDocsReviewTool(getService serviceFactory, cfg *config.Config) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.DocsReview(threshold)
		},
	}
}

func newDocsSimilarTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.SimilarDocs(args.File, args.Threshold)
		},
	}
}

func newExportTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.Export(args.Note, args.View, args.Output, args.Format, args.Profile)
		},
	}
}

func newAutofillTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.Autofill(args.View, args.Property, args.Prompt, args.DryRun)
		},
	}
}

func registerDeskStatus(server *mcpserver.Server, cfg *config.Config) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "desk_status",
		Description: "Returns the current version and vault path configuration for symdesk.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			status := map[string]string{
				"version":      ServerVersion,
				"vault":        cfg.Vault,
				"capabilities": "read_write",
			}
			return status, nil
		},
	})
}

func newRelatedTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			return svc.Related(args.File)
		},
	}
}

func newIngestJobsTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
		Name:        "desk_ingest_jobs",
		Description: "Lists ingestion jobs in the queue from symingest.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer db.Close()
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

func newIngestRetryTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			err = svc.IngestRetry(args.ID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"status": "ok"}, nil
		},
	}
}

func newClipTool(getService serviceFactory) *mcpserver.Tool {
	return &mcpserver.Tool{
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
			defer db.Close()
			path, err := svc.NoteClip(args.URL)
			if err != nil {
				return nil, err
			}
			return map[string]string{"path": path}, nil
		},
	}
}
