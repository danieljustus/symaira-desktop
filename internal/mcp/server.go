package mcp

import (
	"context"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/tools"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

var ServerVersion = "0.6.10"

// ToolCapability classifies an MCP tool as read-only or mutating.
type ToolCapability string

const (
	ReadOnly ToolCapability = "read_only"
	Mutating ToolCapability = "mutating"
)

// ToolCapabilities preserves the legacy MCP-facing capability map while the
// canonical definitions live in internal/tools.
var ToolCapabilities = canonicalToolCapabilities()

func canonicalToolCapabilities() map[string]ToolCapability {
	capabilities := make(map[string]ToolCapability)
	for name, capability := range tools.Capabilities() {
		capabilities[name] = ToolCapability(capability)
	}
	return capabilities
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

	registry := tools.NewRegistry(tools.RegistryOptions{
		Config:        cfg,
		GetService:    getService,
		ServerVersion: ServerVersion,
		AllowWrite:    allowWrite,
	})
	for _, entry := range registry.Enabled(allowWrite) {
		server.RegisterTool(adaptTool(entry))
	}

	return server.ServeStdio(context.Background())
}

// serviceFactory opens a fresh service + sidecar per request; the caller
// closes the returned DB. It aliases the canonical registry contract so MCP
// tests and callers retain the existing local type name.
type serviceFactory = tools.ServiceFactory

func adaptTool(entry tools.Tool) *mcpserver.Tool {
	return &mcpserver.Tool{
		Name:        entry.Name,
		Description: entry.Description,
		InputSchema: entry.InputSchema,
		Handler:     entry.Handler,
		Annotations: &mcpserver.ToolAnnotations{
			ReadOnlyHint:    entry.ReadOnly,
			DestructiveHint: entry.Destructive,
		},
	}
}

func registryTool(name string, getService serviceFactory, cfg *config.Config, allowWrite bool) *mcpserver.Tool {
	entry, ok := tools.NewRegistry(tools.RegistryOptions{
		Config:        cfg,
		GetService:    getService,
		ServerVersion: ServerVersion,
		AllowWrite:    allowWrite,
	}).Lookup(name)
	if !ok {
		panic("mcp: canonical tool not found: " + name)
	}
	return adaptTool(entry)
}

// The following constructors are kept as thin compatibility adapters for the
// existing package-local tests. Production registration uses StartServer's
// single registry traversal above.
func newStatusTool(cfg *config.Config, allowWrite bool) *mcpserver.Tool {
	return registryTool("desk_status", nil, cfg, allowWrite)
}

func newLsTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_ls", getService, nil, false)
}

func newSearchTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_search", getService, nil, false)
}

func newPropsTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_props", getService, nil, false)
}

func newBacklinksTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_backlinks", getService, nil, false)
}

func newNoteNewTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_note_new", getService, nil, true)
}

func newAskTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_ask", getService, nil, false)
}

func newTransformTool(configs ...*config.Config) *mcpserver.Tool {
	var cfg *config.Config
	if len(configs) > 0 {
		cfg = configs[0]
	}
	return registryTool("desk_transform", nil, cfg, false)
}

func newIngestTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_ingest", getService, nil, true)
}

func newDocsTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_docs", getService, nil, false)
}

func newDocSetStatusTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("doc_set_status", getService, nil, true)
}

func newDocsReviewTool(getService serviceFactory, cfg *config.Config) *mcpserver.Tool {
	return registryTool("docs_review", getService, cfg, false)
}

func newDocsSimilarTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("docs_similar", getService, nil, false)
}

func registerDeskStatus(server *mcpserver.Server, cfg *config.Config) {
	server.RegisterTool(registryTool("desk_status", nil, cfg, true))
}

func newRelatedTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_related", getService, nil, false)
}

func newIngestJobsTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_ingest_jobs", getService, nil, false)
}

func newIngestRetryTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_ingest_retry", getService, nil, true)
}

func newClipTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("desk_clip", getService, nil, true)
}

func newMeetingListTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("meeting_list", getService, nil, false)
}

func newMeetingGetTool(getService serviceFactory) *mcpserver.Tool {
	return registryTool("meeting_get", getService, nil, false)
}
