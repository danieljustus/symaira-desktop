package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

var ServerVersion = "0.1.0-dev"

func StartServer(cfg *config.Config, version string) error {
	if version != "" {
		ServerVersion = version
	}

	server := mcpserver.New("symdesk", ServerVersion)

	// Init service lazily per request, or we can init once. We init per request here for simplicity
	// because MCP server might be long-running and config might change, though opening DB repeatedly is fast enough.

	var getService serviceFactory = func() (*service.Service, *sidecar.DB, error) {
		vRoot, err := vault.ResolveVaultRoot("", cfg)
		if err != nil {
			return nil, nil, err
		}
		db, err := sidecar.Open("")
		if err != nil {
			return nil, nil, err
		}
		return service.New(vRoot, db), db, nil
	}

	server.RegisterTool(newStatusTool(cfg))
	server.RegisterTool(newLsTool(getService))
	server.RegisterTool(newSearchTool(getService))
	server.RegisterTool(newPropsTool(getService))
	server.RegisterTool(newBacklinksTool(getService))
	server.RegisterTool(newNoteNewTool(getService))
	server.RegisterTool(newAskTool(getService))
	server.RegisterTool(newIngestTool(getService))
	server.RegisterTool(newDocsTool(getService))

	return server.ServeStdio(context.Background())
}

// serviceFactory opens a fresh service + sidecar per request; the caller
// closes the returned DB.
type serviceFactory func() (*service.Service, *sidecar.DB, error)

func newStatusTool(cfg *config.Config) *mcpserver.Tool {
	return &mcpserver.Tool{
		Name:        "desk_status",
		Description: "Returns the current version and vault path configuration for symdesk.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			status := map[string]string{
				"version": ServerVersion,
				"vault":   cfg.Vault,
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
		Description: "Searches for notes in the vault.",
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
			return svc.Search(args.Query)
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
		Description: "Creates a new note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"content":{"type":"string"}},"required":["title"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Title   string `json:"title"`
				Content string `json:"content"`
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
			path, err := svc.NoteNew(args.Title, args.Content)
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
			answer, err := svc.AskText(args.Query)
			if err != nil {
				return nil, err
			}
			return map[string]string{"answer": answer}, nil
		},
	}
}

// newIngestTool copies a document into the vault inbox and creates a stub
// note per the vault contract.
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

func registerDeskStatus(server *mcpserver.Server, cfg *config.Config) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "desk_status",
		Description: "Returns the current version and vault path configuration for symdesk.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			status := map[string]string{
				"version": ServerVersion,
				"vault":   cfg.Vault,
			}
			return status, nil
		},
	})
}
