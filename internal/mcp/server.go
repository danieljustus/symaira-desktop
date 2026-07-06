package mcp

import (
	"context"
	"encoding/json"

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
	
	getService := func() (*service.Service, *sidecar.DB, error) {
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

	return server.ServeStdio(context.Background())
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

