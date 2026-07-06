package mcp

import (
	"context"
	"encoding/json"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

var ServerVersion = "0.1.0-dev"

func StartServer(cfg *config.Config, version string) error {
	if version != "" {
		ServerVersion = version
	}

	server := mcpserver.New("symdesk", ServerVersion)

	registerDeskStatus(server, cfg)

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
