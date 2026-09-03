package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/notebook"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/tools"
)

func newAskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ask [query]",
		Short: "Ask the AI a question about the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			closeDB := func() {
				if db != nil {
					closeWithWarning("sidecar database", db.Close)
					db = nil
				}
			}
			defer closeDB()
			svc := service.New(vRoot, db)

			notebookRef, _ := cmd.Flags().GetString("notebook")

			out := make(chan interface{})

			agent, _ := cmd.Flags().GetBool("agent")
			if agent {
				// Agentic loop (issue #317): only read-only tools are ever
				// exposed; the classic one-shot Ask remains the fallback
				// when no tools are enabled.
				// The resolved vault root also feeds the loop's result
				// externalizer (issue #406), which stores over-threshold
				// tool results outside the vault tree.
				cfg.Vault = vRoot
				agentTools := readOnlyAgentTools(vRoot, db, cfg)
				if notebookRef != "" {
					scoped, scopeErr := scopeAgentToolsToNotebook(vRoot, notebookRef, agentTools)
					if scopeErr != nil {
						return scopeErr
					}
					agentTools = scoped
				}
				if len(agentTools) == 0 {
					go svc.AskScoped(cmd.Context(), notebookRef, args[0], out)
				} else {
					// The CLI's own db handle is only used by the one-shot
					// Ask fallback; the loop's tools open fresh sidecars per
					// call, so release the shared handle before running.
					closeDB()
					events := make(chan ai.AIEvent)
					go ai.RunAgent(cmd.Context(), cfg, args[0], agentTools, events)
					go func() {
						for event := range events {
							out <- event
						}
						close(out)
					}()
				}
			} else {
				go svc.AskScoped(cmd.Context(), notebookRef, args[0], out)
			}

			return outputStream(out)
		},
	}
	cmd.Flags().Bool("agent", false, "run the bounded agentic tool loop (read-only tools only)")
	cmd.Flags().String("notebook", "", "restrict retrieval and citations to this notebook's sources (issue #425)")
	return cmd
}

// readOnlyAgentTools adapts the canonical registry's read-only entries to
// the ai.AgentTool shape the loop executes. Mutating tools are never passed
// to the model (issue #317).
func readOnlyAgentTools(vRoot string, db *sidecar.DB, cfg *config.Config) []ai.AgentTool {
	registry := tools.NewRegistry(tools.RegistryOptions{
		Config: cfg,
		// The registry handlers open a fresh sidecar per invocation and
		// close it themselves (same contract as the MCP server), so a shared
		// db must never be passed here — the first tool call would close it
		// for every subsequent call.
		GetService: func() (*service.Service, *sidecar.DB, error) {
			fresh, err := sidecar.OpenForVault(vRoot)
			if err != nil {
				return nil, nil, err
			}
			return service.New(vRoot, fresh), fresh, nil
		},
	})
	var out []ai.AgentTool
	for _, entry := range registry.Enabled(false) {
		entry := entry
		out = append(out, ai.AgentTool{
			Name:        entry.Name,
			Description: entry.Description,
			InputSchema: entry.InputSchema,
			ReadOnly:    entry.ReadOnly,
			Handler:     entry.Handler,
		})
	}
	return out
}

// scopeAgentToolsToNotebook wraps desk_search, desk_ls and desk_backlinks
// so a result outside the notebook's sources never reaches the model
// (issue #425). Every other tool passes through unchanged — the notebook
// boundary applies to the loop's discovery surface, not to every tool.
func scopeAgentToolsToNotebook(vRoot, notebookRef string, agentTools []ai.AgentTool) ([]ai.AgentTool, error) {
	nb, err := notebook.Resolve(vRoot, notebookRef)
	if err != nil {
		return nil, fmt.Errorf("resolve notebook %q: %w", notebookRef, err)
	}
	refs, err := nb.ResolveSources(vRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve notebook sources: %w", err)
	}
	allowed := make(map[string]bool, len(refs))
	for _, r := range refs {
		if !r.Missing {
			allowed[r.Path] = true
		}
	}

	scoped := make([]ai.AgentTool, len(agentTools))
	for i, t := range agentTools {
		t := t
		switch t.Name {
		case "desk_search":
			t.Handler = scopedSearchHandler(t.Handler, allowed)
		case "desk_ls":
			t.Handler = scopedLsHandler(t.Handler, allowed)
		case "desk_backlinks":
			t.Handler = scopedBacklinksHandler(t.Handler, allowed)
		}
		scoped[i] = t
	}
	return scoped, nil
}

// agentToolHandler matches ai.AgentTool.Handler's signature; kept as a
// named type only to make the three wrapper functions below readable.
type agentToolHandler = func(ctx context.Context, input json.RawMessage) (any, error)

type scopedSearchOutput struct {
	Results   []service.SearchResult `json:"results"`
	Hint      string                 `json:"hint,omitempty"`
	ScopedOut int                    `json:"scoped_out,omitempty"`
}

// scopedSearchHandler filters desk_search's results to allowed paths. The
// SearchWithMeta response shape is asserted directly (see
// internal/tools/tools.go newSearchTool) rather than round-tripped through
// JSON, so a future field added to SearchResponse is preserved automatically.
func scopedSearchHandler(inner agentToolHandler, allowed map[string]bool) agentToolHandler {
	return func(ctx context.Context, input json.RawMessage) (any, error) {
		res, err := inner(ctx, input)
		if err != nil {
			return nil, err
		}
		resp, ok := res.(service.SearchResponse)
		if !ok {
			return res, nil
		}
		out := scopedSearchOutput{Hint: resp.Hint}
		for _, r := range resp.Results {
			if allowed[r.Path] {
				out.Results = append(out.Results, r)
			} else {
				out.ScopedOut++
			}
		}
		return out, nil
	}
}

type scopedLsOutput struct {
	Files     []service.FileEntry `json:"files"`
	ScopedOut int                 `json:"scoped_out,omitempty"`
}

// scopedLsHandler filters desk_ls's results to allowed paths (see
// internal/tools/tools.go newLsTool / Service.Ls).
func scopedLsHandler(inner agentToolHandler, allowed map[string]bool) agentToolHandler {
	return func(ctx context.Context, input json.RawMessage) (any, error) {
		res, err := inner(ctx, input)
		if err != nil {
			return nil, err
		}
		entries, ok := res.([]service.FileEntry)
		if !ok {
			return res, nil
		}
		var out scopedLsOutput
		for _, e := range entries {
			if allowed[e.Path] {
				out.Files = append(out.Files, e)
			} else {
				out.ScopedOut++
			}
		}
		return out, nil
	}
}

type scopedBacklinksOutput struct {
	Paths     []string `json:"paths"`
	ScopedOut int      `json:"scoped_out,omitempty"`
}

// scopedBacklinksHandler filters desk_backlinks's results to allowed paths
// (see internal/tools/tools.go newBacklinksTool / Service.Backlinks).
func scopedBacklinksHandler(inner agentToolHandler, allowed map[string]bool) agentToolHandler {
	return func(ctx context.Context, input json.RawMessage) (any, error) {
		res, err := inner(ctx, input)
		if err != nil {
			return nil, err
		}
		paths, ok := res.([]string)
		if !ok {
			return res, nil
		}
		var out scopedBacklinksOutput
		for _, p := range paths {
			if allowed[p] {
				out.Paths = append(out.Paths, p)
			} else {
				out.ScopedOut++
			}
		}
		return out, nil
	}
}
