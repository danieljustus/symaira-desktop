package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/config"
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
			defer db.Close()
			svc := service.New(vRoot, db)

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
				if len(agentTools) == 0 {
					go svc.Ask(cmd.Context(), args[0], out)
				} else {
					// The CLI's own db handle is only used by the one-shot
					// Ask fallback; the loop's tools open fresh sidecars per
					// call, so release the shared handle before running.
					_ = db.Close()
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
				go svc.Ask(cmd.Context(), args[0], out)
			}

			return outputStream(out)
		},
	}
	cmd.Flags().Bool("agent", false, "run the bounded agentic tool loop (read-only tools only)")
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
