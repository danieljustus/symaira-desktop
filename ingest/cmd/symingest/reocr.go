package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

type reocrErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type reocrResponse struct {
	SchemaVersion int                 `json:"schema_version"`
	DocumentID    int64               `json:"document_id"`
	JobID         int64               `json:"job_id"`
	Status        string              `json:"status"`
	OutputPath    string              `json:"output_path"`
	Error         *reocrErrorResponse `json:"error"`
}

func printReocrResponse(response reocrResponse, jsonFlag bool) error {
	if jsonFlag {
		data, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal,
				"failed to marshal reocr response")
		}
		fmt.Fprintln(stdout, string(data))
		return nil
	}
	if response.Error != nil {
		return nil
	}
	if response.Status == "already_running" {
		fmt.Fprintf(stdout, "reprocess already running for document %d (job %d)\n", response.DocumentID, response.JobID)
		return nil
	}
	fmt.Fprintf(stdout, "reprocessed: document %d\njob ID: %d\nstatus: %s\noutput path: %s\n",
		response.DocumentID, response.JobID, response.Status, response.OutputPath)
	return nil
}

func reocrFailure(jsonFlag bool, documentID, jobID int64, code, message string) error {
	response := reocrResponse{
		SchemaVersion: 1,
		DocumentID:    documentID,
		JobID:         jobID,
		Status:        "failed",
		Error:         &reocrErrorResponse{Code: code, Message: message},
	}
	if err := printReocrResponse(response, jsonFlag); err != nil {
		return err
	}
	return exitcodes.Wrapf(nil, exitcodes.ExitData, exitcodes.KindValidation, "%s: %s", code, message)
}

func runReocr(args []string) error {
	fs := flag.NewFlagSet("reocr", flag.ContinueOnError)
	jsonFlag := fs.Bool("json", false, "Output a versioned JSON response")
	documentIDFlag := fs.String("document-id", "", "Document ID to reprocess")
	sourceFlag := fs.String("source", "", "Archived source path to reprocess (use for numeric paths)")
	langFlag := fs.String("lang", "", "Tesseract language override")
	ocrLang, vault, archive, db := registerSharedFlags(fs)
	configureUsage(fs, "reocr [flags] <document-id|archived-source-path>", "Re-run extraction/OCR for an already-ingested document. A numeric operand is a document ID; a non-numeric operand is an archived source path. Use --document-id or --source to make the identifier explicit.\n\nJSON responses use schema_version 1 and include document_id, job_id, status, output_path, and error.")
	help, err := parseFlags(fs, args, "invalid reocr flags")
	if help || err != nil {
		return err
	}
	if *langFlag != "" {
		*ocrLang = *langFlag
	}
	cfg, err := resolveConfig(fs, ocrLang, vault, archive, db)
	if err != nil {
		return err
	}
	if cfg.vault == "" {
		return reocrFailure(*jsonFlag, 0, 0, "vault_not_configured", "no vault configured; use --vault, SYMINGEST_VAULT env, or set vault in config")
	}

	remaining := fs.Args()
	if *documentIDFlag != "" && (*sourceFlag != "" || len(remaining) > 0) {
		return reocrFailure(*jsonFlag, 0, 0, "invalid_identifier", "use exactly one of --document-id, --source, or a positional identifier")
	}
	if *sourceFlag != "" && len(remaining) > 0 {
		return reocrFailure(*jsonFlag, 0, 0, "invalid_identifier", "use exactly one of --source or a positional identifier")
	}
	if *documentIDFlag == "" && *sourceFlag == "" && len(remaining) == 0 {
		fs.Usage()
		return reocrFailure(*jsonFlag, 0, 0, "invalid_identifier", "a document ID or archived source path is required")
	}
	if len(remaining) > 1 {
		return reocrFailure(*jsonFlag, 0, 0, "invalid_identifier", "expected exactly one document ID or archived source path")
	}

	st, err := store.Open(cfg.db)
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "failed to open document store")
	}
	defer st.Close()
	ctx := context.Background()

	var documentID int64
	var sourcePath string
	if *documentIDFlag != "" {
		documentID, err = strconv.ParseInt(*documentIDFlag, 10, 64)
		if err != nil || documentID <= 0 {
			return reocrFailure(*jsonFlag, 0, 0, "invalid_identifier", fmt.Sprintf("invalid document ID %q; must be a positive integer", *documentIDFlag))
		}
	} else if *sourceFlag != "" {
		sourcePath = *sourceFlag
	} else if id, parseErr := strconv.ParseInt(remaining[0], 10, 64); parseErr == nil {
		if id <= 0 {
			return reocrFailure(*jsonFlag, 0, 0, "invalid_identifier", fmt.Sprintf("invalid document ID %q; must be a positive integer", remaining[0]))
		}
		documentID = id
	} else {
		sourcePath = remaining[0]
	}

	var doc *store.Document
	if documentID != 0 {
		doc, err = st.ByID(ctx, documentID)
		if errors.Is(err, sql.ErrNoRows) {
			return reocrFailure(*jsonFlag, documentID, 0, "document_not_found", fmt.Sprintf("document %d does not exist", documentID))
		}
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to look up document")
		}
	} else {
		rawSourcePath := sourcePath
		sourcePath, err = filepath.Abs(sourcePath)
		if err != nil {
			return reocrFailure(*jsonFlag, 0, 0, "invalid_identifier", fmt.Sprintf("invalid archived source path: %v", err))
		}
		doc, err = st.ByArchivePath(ctx, sourcePath)
		if errors.Is(err, sql.ErrNoRows) && filepath.Clean(rawSourcePath) != sourcePath {
			sourcePath = filepath.Clean(rawSourcePath)
			doc, err = st.ByArchivePath(ctx, sourcePath)
		}
		if errors.Is(err, sql.ErrNoRows) {
			return reocrFailure(*jsonFlag, 0, 0, "source_not_registered", fmt.Sprintf("archived source path is not registered: %s", sourcePath))
		}
		if err != nil {
			return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to look up archived source")
		}
		documentID = doc.ID
	}
	if doc.ArchivePath == nil || *doc.ArchivePath == "" {
		return reocrFailure(*jsonFlag, documentID, 0, "archive_missing", "document has no recorded archived source")
	}
	if sourcePath == "" {
		sourcePath = *doc.ArchivePath
	}
	if _, err := os.Stat(sourcePath); err != nil {
		code := "source_missing"
		message := fmt.Sprintf("archived source is unavailable: %s", sourcePath)
		if !os.IsNotExist(err) {
			message = fmt.Sprintf("cannot access archived source %s: %v", sourcePath, err)
		}
		return reocrFailure(*jsonFlag, documentID, 0, code, message)
	}

	pipeline := &ingest.Pipeline{
		Engine:     ocr.NewEngine(cfg.ocrLang, cfg.ollamaBaseURL, cfg.ollamaModel),
		Store:      st,
		Writer:     &writer.NoteWriter{Vault: cfg.vault},
		ArchiveDir: cfg.archive,
	}
	configurePostIndex(pipeline, cfg)
	result, err := pipeline.Reprocess(ctx, documentID, sourcePath, nil)
	if err != nil {
		code := "reprocess_failed"
		if strings.Contains(err.Error(), "hash mismatch") {
			code = "source_hash_mismatch"
		} else if strings.Contains(err.Error(), "stat archived source") && os.IsNotExist(err) {
			code = "source_missing"
		}
		return reocrFailure(*jsonFlag, documentID, 0, code, err.Error())
	}
	response := reocrResponse{SchemaVersion: 1, DocumentID: documentID, JobID: result.Job.ID, Status: "completed"}
	if result.AlreadyRunning {
		response.Status = "already_running"
		if doc.VaultPath != nil {
			response.OutputPath = *doc.VaultPath
		}
	} else if result.Result != nil {
		response.OutputPath = result.Result.VaultPath
	}
	return printReocrResponse(response, *jsonFlag)
}
