package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func newSourcesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sources",
		Aliases: []string{"source"},
		Short:   "Manage read-only external search folders",
	}
	cmd.AddCommand(newSourcesAddCmd(), newSourcesListCmd(), newSourcesRemoveCmd(), newSourcesWatchCmd())
	return cmd
}

func sourceRegistry() (*retrieval.SourceRegistry, error) {
	vRoot, err := vault.ResolveVaultRoot("", cfg)
	if err != nil {
		return nil, err
	}
	return retrieval.NewSourceRegistry(vRoot)
}

func newSourcesAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <folder>",
		Short: "Register and index an external folder in place",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := sourceRegistry()
			if err != nil {
				return err
			}
			source, err := registry.Add(args[0])
			if err != nil {
				return err
			}
			if err := retrieval.IndexDirectory(source.Path); err != nil {
				return fmt.Errorf("index external source %s: %w", source.Path, err)
			}
			return outputResult(map[string]interface{}{
				"status": "indexed",
				"source": source,
			})
		},
	}
}

func newSourcesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered external search folders",
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := sourceRegistry()
			if err != nil {
				return err
			}
			sources, err := registry.List()
			if err != nil {
				return err
			}
			return outputResult(sources)
		},
	}
}

func newSourcesRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <id-or-folder>",
		Short: "Unregister a folder and remove only its indexed documents",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := sourceRegistry()
			if err != nil {
				return err
			}
			sources, err := registry.List()
			if err != nil {
				return err
			}
			var selected *retrieval.Source
			for i := range sources {
				if sources[i].ID == args[0] || sources[i].Path == args[0] {
					selected = &sources[i]
					break
				}
			}
			if selected == nil {
				return fmt.Errorf("external source %q not registered", args[0])
			}
			removed, err := retrieval.RemoveDirectory(selected.Path)
			if err != nil {
				return fmt.Errorf("remove indexed source %s: %w", selected.Path, err)
			}
			if err := registry.Remove(selected.ID); err != nil {
				return err
			}
			return outputResult(map[string]interface{}{
				"status":  "removed",
				"source":  *selected,
				"removed": removed,
			})
		},
	}
}

func newSourcesWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "watch <id-or-folder>",
		Short: "Watch a registered external folder for changes",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry, err := sourceRegistry()
			if err != nil {
				return err
			}
			sources, err := registry.List()
			if err != nil {
				return err
			}
			var path string
			for _, source := range sources {
				if source.ID == args[0] || source.Path == args[0] {
					path = source.Path
					break
				}
			}
			if path == "" {
				return fmt.Errorf("external source %q not registered", args[0])
			}
			return retrieval.WatchDirectory(cmd.Context(), path)
		},
	}
}
