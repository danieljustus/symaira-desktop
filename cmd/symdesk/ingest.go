package main

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newIngestCmd() *cobra.Command {
	ingestCmd := &cobra.Command{
		Use:   "ingest",
		Short: "Ingest a file or manage ingestion jobs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			res, err := svc.Ingest(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}

	ingestJobsCmd := &cobra.Command{
		Use:   "jobs",
		Short: "List ingestion jobs in the queue",
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			res, err := svc.IngestJobs()
			if err != nil {
				return err
			}
			if jsonFlag {
				fmt.Println(res)
				return nil
			}
			var rawJobs []map[string]interface{}
			if err := json.Unmarshal([]byte(res), &rawJobs); err == nil {
				for _, rj := range rawJobs {
					fmt.Printf("Job ID: %v | Status: %v | Source: %v\n", rj["id"], rj["status"], rj["source_path"])
				}
			} else {
				fmt.Println(res)
			}
			return nil
		},
	}

	ingestRetryCmd := &cobra.Command{
		Use:   "retry [job-id]",
		Short: "Retry a failed ingestion job",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			err = svc.IngestRetry(args[0])
			if err != nil {
				return err
			}

			res := map[string]interface{}{
				"status": "retried",
				"job_id": args[0],
			}
			return outputResult(res)
		},
	}
	ingestCmd.AddCommand(ingestJobsCmd)
	ingestCmd.AddCommand(ingestRetryCmd)
	ingestCmd.AddCommand(newIngestReocrCmd())
	ingestCmd.AddCommand(newIngestSplitCmd())
	ingestCmd.AddCommand(newIngestMergeCmd())
	ingestCmd.AddCommand(newIngestRotateCmd())

	return ingestCmd
}

// newIngestSplitCmd exposes the in-process SplitPDFAtSpec as `symdesk ingest
// split` (issue #636). It mirrors the absorbed `symingest split --at N,M
// --output-dir DIR <input.pdf>` contract and fails with a clear message when
// the Poppler utilities probed by internal/ingest/split.go are absent instead
// of erroring opaquely deeper in the pipeline.
func newIngestSplitCmd() *cobra.Command {
	var atSpec string
	var outputDir string
	cmd := &cobra.Command{
		Use:   "split <input.pdf>",
		Short: "Split a PDF into parts after the given pages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if atSpec == "" {
				return fmt.Errorf("--at is required (comma-separated pages, e.g. 2,4 or 2-3,6)")
			}
			if outputDir == "" {
				return fmt.Errorf("--output-dir is required")
			}
			if !ingest.HasPopplerSplit() {
				return fmt.Errorf("PDF splitting requires the Poppler utilities pdfinfo, pdfseparate and pdfunite on PATH (brew install poppler)")
			}
			parts, err := ingest.SplitPDFAtSpec(cmd.Context(), args[0], atSpec, outputDir)
			if err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(map[string]interface{}{"schema_version": ingest.SchemaVersion, "parts": parts})
			}
			for _, p := range parts {
				fmt.Println(p)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&atSpec, "at", "", "split after these pages, e.g. 2,4 or 2-3,6")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "directory for the generated part PDFs")
	return cmd
}

// newIngestMergeCmd exposes the in-process PDF merge (issue #637) as `symdesk
// ingest merge`. It mirrors the absorbed `symingest merge` contract: combine
// two or more PDFs into one output file without modifying the inputs.
func newIngestMergeCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "merge <input.pdf> [<input.pdf> ...]",
		Short: "Merge two or more PDFs into one file",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			if err := ingest.MergePDFs(cmd.Context(), args, output); err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(map[string]interface{}{"schema_version": ingest.SchemaVersion, "output": output})
			}
			fmt.Println(output)
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "destination PDF path")
	return cmd
}

// newIngestRotateCmd exposes the in-process PDF rotation (issue #637) as
// `symdesk ingest rotate`. It mirrors the absorbed `symingest rotate` contract:
// rotate all pages or a page selector by a multiple of 90 degrees.
func newIngestRotateCmd() *cobra.Command {
	var output string
	var degrees int
	var pages string
	cmd := &cobra.Command{
		Use:   "rotate <input.pdf>",
		Short: "Rotate pages of a PDF and write the result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output == "" {
				return fmt.Errorf("--output is required")
			}
			if degrees == 0 {
				return fmt.Errorf("--degrees is required (one of -270, -180, -90, 90, 180, 270)")
			}
			if err := ingest.RotatePDF(cmd.Context(), args[0], output, degrees, pages); err != nil {
				return err
			}
			if jsonFlag {
				return outputResult(map[string]interface{}{"schema_version": ingest.SchemaVersion, "output": output})
			}
			fmt.Println(output)
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "destination PDF path")
	cmd.Flags().IntVar(&degrees, "degrees", 0, "rotation in degrees (one of -270, -180, -90, 90, 180, 270)")
	cmd.Flags().StringVar(&pages, "pages", "", "optional 1-based page selector, e.g. 1-3,5 (empty rotates all pages)")
	return cmd
}

// newIngestReocrCmd wraps the absorbed pipeline's reocr job kind (issue
// #609): re-run OCR/extraction for a document already in the store, either
// by its archived original's path or by --document-id.
func newIngestReocrCmd() *cobra.Command {
	var documentID int64
	cmd := &cobra.Command{
		Use:   "reocr [archive-path]",
		Short: "Re-run OCR/extraction for an already-ingested document",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var archivePath string
			if len(args) == 1 {
				archivePath = args[0]
			}
			if archivePath == "" && documentID == 0 {
				return fmt.Errorf("provide an archive path or --document-id")
			}
			if archivePath != "" && documentID != 0 {
				return fmt.Errorf("provide either an archive path or --document-id, not both")
			}

			opts := ingest.Options{Vault: cfg.Vault}
			ctx := cmd.Context()
			var res *ingest.ReprocessResult
			var err error
			if documentID != 0 {
				res, err = ingest.Reprocess(ctx, opts, documentID)
			} else {
				res, err = ingest.ReprocessByArchivePath(ctx, opts, archivePath)
			}
			if err != nil {
				return reportReocrError(documentID, err)
			}
			return outputReocrResult(res)
		},
	}
	cmd.Flags().Int64Var(&documentID, "document-id", 0, "reprocess by document ID instead of archive path")
	return cmd
}

type reocrResponse struct {
	SchemaVersion int         `json:"schema_version"`
	DocumentID    int64       `json:"document_id"`
	JobID         int64       `json:"job_id"`
	Status        string      `json:"status"`
	OutputPath    string      `json:"output_path"`
	Error         *reocrError `json:"error,omitempty"`
}

type reocrError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func outputReocrResult(res *ingest.ReprocessResult) error {
	if jsonFlag {
		return outputResult(reocrResponse{
			SchemaVersion: ingest.SchemaVersion,
			DocumentID:    res.DocumentID,
			JobID:         res.JobID,
			Status:        res.Status,
			OutputPath:    res.OutputPath,
		})
	}
	fmt.Printf("document %d: %s (job %d)\n", res.DocumentID, res.Status, res.JobID)
	if res.OutputPath != "" {
		fmt.Printf("  output: %s\n", res.OutputPath)
	}
	return nil
}

// reportReocrError builds the reocr JSON envelope's error object rather than
// letting main's generic {"error": ...} land as a second JSON document on
// stdout (issue #438) — this response always carries document_id/status,
// which that generic envelope cannot express.
func reportReocrError(documentID int64, err error) error {
	if !jsonFlag {
		return err
	}
	code, exitCode, kind := "internal", exitcodes.ExitGeneric, exitcodes.KindInternal
	switch {
	case errors.Is(err, ingest.ErrDocumentNotFound):
		code, exitCode, kind = "not_found", exitcodes.ExitNotFound, exitcodes.KindNotFound
	case errors.Is(err, ingest.ErrNoArchivedOriginal):
		code, exitCode, kind = "no_archived_original", exitcodes.ExitData, exitcodes.KindValidation
	}

	resp := reocrResponse{
		SchemaVersion: ingest.SchemaVersion,
		DocumentID:    documentID,
		Status:        "failed",
		Error:         &reocrError{Code: code, Message: err.Error()},
	}
	if b, marshalErr := json.Marshal(resp); marshalErr == nil {
		fmt.Println(string(b))
	}
	return jsonReportedError{exitcodes.Wrap(err, exitCode, kind, err.Error())}
}
