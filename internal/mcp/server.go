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

	registerDeskStatus(server, cfg)

	server.RegisterTool(&mcpserver.Tool{
		Name:        "desk_ls",
		Description: "Lists files in the vault.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"dir":{"type":"string"}}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Dir string `json:"dir"`
			}
			json.Unmarshal(input, &args)
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer db.Close()
			return svc.Ls(args.Dir)
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "desk_search",
		Description: "Searches for notes in the vault.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Query string `json:"query"`
			}
			json.Unmarshal(input, &args)
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer db.Close()
			return svc.Search(args.Query)
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "desk_props",
		Description: "Gets properties (frontmatter) for a note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				File string `json:"file"`
			}
			json.Unmarshal(input, &args)
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer db.Close()
			return svc.Props(args.File)
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "desk_backlinks",
		Description: "Gets backlinks for a note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				File string `json:"file"`
			}
			json.Unmarshal(input, &args)
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer db.Close()
			return svc.Backlinks(args.File)
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "desk_note_new",
		Description: "Creates a new note.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"content":{"type":"string"}},"required":["title"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Title   string `json:"title"`
				Content string `json:"content"`
			}
			json.Unmarshal(input, &args)
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
	})

	server.RegisterTool(newAskTool(getService))
	server.RegisterTool(newIngestTool(getService))

	return server.ServeStdio(context.Background())
}

// serviceFactory opens a fresh service + sidecar per request; the caller
// closes the returned DB.
type serviceFactory func() (*service.Service, *sidecar.DB, error)

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
