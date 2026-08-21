package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/engine"
)

// newExtractCmd builds the "extract" command group for searching, listing,
// and manually importing grounded extraction sidecars.
func newExtractCmd() *cobra.Command {
	extractCmd := &cobra.Command{
		Use:   "extract",
		Short: "Search, list, and import grounded document extractions",
	}

	var searchLimit int
	var searchJSON bool
	searchCmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Full-text search over extraction values and evidence text",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			results, err := dbClient.SearchExtractions(args[0], searchLimit)
			if err != nil {
				return err
			}
			return writeExtractions(os.Stdout, results, searchJSON)
		},
	}
	searchCmd.Flags().IntVarP(&searchLimit, "limit", "l", 10, "Maximum number of results to return")
	searchCmd.Flags().BoolVar(&searchJSON, "json", false, "Output results in JSON format")
	extractCmd.AddCommand(searchCmd)

	var listClass string
	var listLimit int
	var listJSON bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List extractions, optionally filtered by class",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			results, err := dbClient.ListExtractions(listClass, listLimit)
			if err != nil {
				return err
			}
			return writeExtractions(os.Stdout, results, listJSON)
		},
	}
	listCmd.Flags().StringVar(&listClass, "class", "", "Filter by extraction class (e.g. amount, deadline)")
	listCmd.Flags().IntVarP(&listLimit, "limit", "l", 50, "Maximum number of results to return")
	listCmd.Flags().BoolVar(&listJSON, "json", false, "Output results in JSON format")
	extractCmd.AddCommand(listCmd)

	var importDoc string
	importCmd := &cobra.Command{
		Use:          "import [jsonl_path]",
		Short:        "Import a grounded extraction sidecar JSONL file for an already-indexed document",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			sidecarPath := args[0]
			if _, err := os.Stat(sidecarPath); err != nil {
				return fmt.Errorf("cannot access sidecar file: %w", err)
			}

			dbClient, err := db.Open()
			if err != nil {
				return err
			}
			defer dbClient.Close()

			docPath := importDoc
			if docPath == "" {
				docPath, err = resolveDocPathForSidecar(dbClient, sidecarPath)
				if err != nil {
					return err
				}
			}

			if err := engine.ImportExtractionSidecar(dbClient, docPath, sidecarPath); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Imported extractions from %s for document %s\n", sidecarPath, docPath)
			return nil
		},
	}
	importCmd.Flags().StringVar(&importDoc, "doc", "", "Document path these extractions belong to (default: inferred from the sidecar filename matching an indexed document's hash)")
	extractCmd.AddCommand(importCmd)

	return extractCmd
}

// resolveDocPathForSidecar infers which indexed document a sidecar belongs
// to from its filename, which follows symingest's "<sha256>.jsonl"
// convention (the sha256 of the original source file, stored as the
// document's hash).
func resolveDocPathForSidecar(dbClient db.Store, sidecarPath string) (string, error) {
	stem := strings.TrimSuffix(filepath.Base(sidecarPath), filepath.Ext(sidecarPath))

	docs, err := dbClient.ListDocuments()
	if err != nil {
		return "", fmt.Errorf("list documents: %w", err)
	}
	for _, d := range docs {
		if d.Hash == stem {
			return d.Path, nil
		}
	}
	return "", fmt.Errorf("no indexed document has hash %q matching the sidecar filename; specify --doc explicitly", stem)
}

func writeExtractions(w *os.File, results []*db.Extraction, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	if len(results) == 0 {
		fmt.Fprintln(w, "No matching extractions found.")
		return nil
	}
	for idx, e := range results {
		fmt.Fprintf(w, "[%d] %s = %q (doc: %s, matched: %t)\n", idx+1, e.Class, e.Value, e.DocumentPath, e.Matched)
		if e.EvidenceText != "" {
			fmt.Fprintf(w, "    evidence: %s\n", e.EvidenceText)
		}
	}
	return nil
}
