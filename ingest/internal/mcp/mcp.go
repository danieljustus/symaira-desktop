// Package mcp exposes the one-shot ingestion pipeline as an MCP server.
package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/ocr"
	"github.com/danieljustus/symaira-ingest/internal/paperlessimport"
	"github.com/danieljustus/symaira-ingest/internal/pdfops"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

// activeWatchers tracks running watchers by directory path to prevent leaks.
// Each entry stores a cancel function to stop the watcher and its worker.
var activeWatchers sync.Map

type watcherEntry struct {
	cancel  context.CancelFunc
	watcher *ingest.Watcher
}

func resolveVaultArchive(argVault, argArchive, defaultVault, defaultArchive string) (vault, archive string, err error) {
	vault = argVault
	if vault == "" {
		vault = defaultVault
	}
	if vault == "" {
		return "", "", fmt.Errorf("no vault configured")
	}

	archive = argArchive
	if archive == "" {
		archive = defaultArchive
	}
	if archive == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("determine default archive path: %w", err)
		}
		archive = filepath.Join(home, ".local", "share", "symingest", "archive")
	}
	return vault, archive, nil
}

func parseMCPDocumentIDs(raw json.RawMessage) ([]int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var ids []int
	if err := json.Unmarshal(raw, &ids); err == nil {
		for _, id := range ids {
			if id <= 0 {
				return nil, fmt.Errorf("document ID %d must be positive", id)
			}
		}
		return ids, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("ids must be an array of positive integers or a comma-separated string")
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("document ID %q is not a number", part)
		}
		if id <= 0 {
			return nil, fmt.Errorf("document ID %d must be positive", id)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// Register adds the ingest_file tool to the MCP server.
func Register(server *mcpserver.Server, st *store.Store, engine extract.Engine, defaultVault, defaultArchive string) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "ingest_file",
		Description: "Ingest a single file into the vault, returning metadata about the generated Markdown note including the vault_path where it was written.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"path": {"type": "string", "description": "Absolute or relative path to the source file"},
				"vault_path": {"type": "string", "description": "Optional vault directory override"},
				"archive_path": {"type": "string", "description": "Optional archive directory override"},
				"category": {"type": "string", "description": "Preset category metadata (overrides classification rules)"},
				"tags": {"type": "array", "items": {"type": "string"}, "description": "Preset tags (overrides classification rules)"},
				"correspondent": {"type": "string", "description": "Preset correspondent metadata (overrides classification rules)"},
				"document_type": {"type": "string", "description": "Preset document type metadata (overrides classification rules)"}
			},
			"required": ["path"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Path          string   `json:"path"`
				VaultPath     string   `json:"vault_path"`
				ArchivePath   string   `json:"archive_path"`
				Category      string   `json:"category"`
				Tags          []string `json:"tags"`
				Correspondent string   `json:"correspondent"`
				DocumentType  string   `json:"document_type"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			source, err := filepath.Abs(args.Path)
			if err != nil {
				return nil, fmt.Errorf("invalid source path: %w", err)
			}

			vault, archive, err := resolveVaultArchive(args.VaultPath, args.ArchivePath, defaultVault, defaultArchive)
			if err != nil {
				return nil, err
			}

			pipeline := &ingest.Pipeline{
				Engine:     engine,
				Store:      st,
				Writer:     &writer.NoteWriter{Vault: vault},
				ArchiveDir: archive,
			}

			var opts *ingest.IngestOptions
			if args.Category != "" || len(args.Tags) > 0 || args.Correspondent != "" || args.DocumentType != "" {
				opts = &ingest.IngestOptions{
					PresetCategory:      args.Category,
					PresetTags:          args.Tags,
					PresetCorrespondent: args.Correspondent,
					PresetDocumentType:  args.DocumentType,
				}
			}

			res, err := pipeline.Ingest(ctx, source, opts)
			if errors.Is(err, ingest.ErrDuplicate) {
				var vPath, aPath string
				if dupErr, ok := err.(*ingest.DuplicateError); ok {
					vPath = dupErr.VaultPath
					aPath = dupErr.ArchivePath
				}
				data, mErr := json.Marshal(map[string]any{
					"status":       "duplicate",
					"source":       source,
					"vault_path":   vPath,
					"archive_path": aPath,
				})
				if mErr != nil {
					return nil, fmt.Errorf("marshal duplicate result: %w", mErr)
				}
				return string(data), nil
			}
			if err != nil {
				return nil, err
			}

			data, mErr := json.Marshal(map[string]any{
				"status":       "success",
				"source":       source,
				"vault_path":   res.VaultPath,
				"archive_path": res.ArchivePath,
				"mime":         res.Extract.MIME,
				"engine":       res.Extract.Engine,
				"text_length":  len(res.Extract.Text),
			})
			if mErr != nil {
				return nil, fmt.Errorf("marshal ingest result: %w", mErr)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "reocr",
		Description: "Re-run OCR and structured extraction for an already-ingested document, updating its existing Markdown note while preserving user-owned frontmatter fields.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"document_id": {"type": "integer", "description": "Existing symingest document ID"},
				"source": {"type": "string", "description": "Archived original path registered for an existing document"},
				"vault_path": {"type": "string", "description": "Optional vault directory override"},
				"lang": {"type": "string", "description": "Optional Tesseract language override, e.g. deu+eng"}
			},
			"anyOf": [
				{"required": ["document_id"]},
				{"required": ["source"]}
			]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				DocumentID *int64 `json:"document_id"`
				Source     string `json:"source"`
				VaultPath  string `json:"vault_path"`
				Lang       string `json:"lang"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if (args.DocumentID == nil) == (strings.TrimSpace(args.Source) == "") {
				return nil, fmt.Errorf("exactly one of document_id or source is required")
			}
			if args.DocumentID != nil && *args.DocumentID <= 0 {
				return nil, fmt.Errorf("document_id must be positive")
			}

			vault, _, err := resolveVaultArchive(args.VaultPath, "", defaultVault, defaultArchive)
			if err != nil {
				return nil, err
			}

			var doc *store.Document
			var source string
			if args.DocumentID != nil {
				doc, err = st.ByID(ctx, *args.DocumentID)
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("document %d does not exist", *args.DocumentID)
				}
				if err != nil {
					return nil, fmt.Errorf("look up document %d: %w", *args.DocumentID, err)
				}
			} else {
				rawSource := strings.TrimSpace(args.Source)
				source, err = filepath.Abs(rawSource)
				if err != nil {
					return nil, fmt.Errorf("invalid source path: %w", err)
				}
				doc, err = st.ByArchivePath(ctx, source)
				if errors.Is(err, sql.ErrNoRows) && filepath.Clean(rawSource) != source {
					source = filepath.Clean(rawSource)
					doc, err = st.ByArchivePath(ctx, source)
				}
				if errors.Is(err, sql.ErrNoRows) {
					return nil, fmt.Errorf("archived source path is not registered: %s", source)
				}
				if err != nil {
					return nil, fmt.Errorf("look up archived source: %w", err)
				}
			}
			if doc.ArchivePath == nil || *doc.ArchivePath == "" {
				return nil, fmt.Errorf("document %d has no recorded archived source", doc.ID)
			}
			if source == "" {
				source = *doc.ArchivePath
			}

			reocrEngine := engine
			if strings.TrimSpace(args.Lang) != "" {
				reocrEngine = ocr.DefaultRunner(strings.TrimSpace(args.Lang))
			}
			pipeline := &ingest.Pipeline{
				Engine:     reocrEngine,
				Store:      st,
				Writer:     &writer.NoteWriter{Vault: vault},
				ArchiveDir: defaultArchive,
			}
			result, err := pipeline.Reprocess(ctx, doc.ID, source, nil)
			if err != nil {
				return nil, err
			}
			status := "completed"
			outputPath := ""
			if result.AlreadyRunning {
				status = "already_running"
				if doc.VaultPath != nil {
					outputPath = *doc.VaultPath
				}
			} else if result.Result != nil {
				outputPath = result.Result.VaultPath
			}
			data, err := json.Marshal(map[string]any{
				"schema_version": 1,
				"status":         status,
				"document_id":    doc.ID,
				"job_id":         result.Job.ID,
				"output_path":    outputPath,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal reocr result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "list_jobs",
		Description: "List jobs in the ingestion queue, including their status, attempts, error messages, and source path.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"limit": {"type": "integer", "description": "Maximum number of jobs to return (default: all)"}
			}
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Limit int `json:"limit"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			jobs, err := st.ListJobs(ctx, args.Limit)
			if err != nil {
				return nil, fmt.Errorf("list jobs: %w", err)
			}
			data, err := json.Marshal(map[string]any{
				"status": "success",
				"jobs":   jobs,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal list_jobs result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "retry_job",
		Description: "Reset a failed job's status to pending and its attempts count to 0 so that it gets retried.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"job_id": {"type": "integer", "description": "The ID of the job to retry"}
			},
			"required": ["job_id"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				JobID int64 `json:"job_id"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			if err := st.RetryJob(ctx, args.JobID); err != nil {
				return nil, fmt.Errorf("retry job %d: %w", args.JobID, err)
			}

			data, err := json.Marshal(map[string]any{
				"status":  "success",
				"message": fmt.Sprintf("job %d reset to pending status", args.JobID),
			})
			if err != nil {
				return nil, fmt.Errorf("marshal retry result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "start_watch",
		Description: "Start recursively watching a directory for new or modified files and process them automatically in the background.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"directory": {"type": "string", "description": "Absolute path to the directory to watch"},
				"vault_path": {"type": "string", "description": "Optional vault directory path override"},
				"archive_path": {"type": "string", "description": "Optional archive directory path override"}
			},
			"required": ["directory"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Directory   string `json:"directory"`
				VaultPath   string `json:"vault_path"`
				ArchivePath string `json:"archive_path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			dir, err := filepath.Abs(args.Directory)
			if err != nil {
				return nil, fmt.Errorf("invalid directory path: %w", err)
			}

			if _, loaded := activeWatchers.LoadOrStore(dir, nil); loaded {
				return nil, fmt.Errorf("directory %s is already being watched", dir)
			}

			vault, archive, err := resolveVaultArchive(args.VaultPath, args.ArchivePath, defaultVault, defaultArchive)
			if err != nil {
				activeWatchers.Delete(dir)
				return nil, err
			}

			watcher, err := ingest.NewWatcher(st, dir)
			if err != nil {
				activeWatchers.Delete(dir)
				return nil, fmt.Errorf("initialize watcher: %w", err)
			}

			watchCtx, cancel := context.WithCancel(context.Background())
			pipeline := &ingest.Pipeline{
				Engine:     engine,
				Store:      st,
				Writer:     &writer.NoteWriter{Vault: vault},
				ArchiveDir: archive,
			}

			if err := watcher.Start(watchCtx); err != nil {
				cancel()
				watcher.Close()
				activeWatchers.Delete(dir)
				return nil, fmt.Errorf("start watcher: %w", err)
			}
			go ingest.StartWorker(watchCtx, pipeline)

			activeWatchers.Store(dir, &watcherEntry{cancel: cancel, watcher: watcher})

			data, err := json.Marshal(map[string]any{
				"status":  "success",
				"message": fmt.Sprintf("started watching directory %s with vault %s and archive %s", dir, vault, archive),
			})
			if err != nil {
				return nil, fmt.Errorf("marshal start_watch result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "stop_watch",
		Description: "Stop watching a directory that was previously started with start_watch.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"directory": {"type": "string", "description": "Absolute path to the directory to stop watching"}
			},
			"required": ["directory"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Directory string `json:"directory"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			dir, err := filepath.Abs(args.Directory)
			if err != nil {
				return nil, fmt.Errorf("invalid directory path: %w", err)
			}

			entry, loaded := activeWatchers.LoadAndDelete(dir)
			if !loaded {
				return nil, fmt.Errorf("directory %s is not being watched", dir)
			}

			if w := entry.(*watcherEntry); w != nil {
				w.cancel()
				w.watcher.Close()
			}

			data, err := json.Marshal(map[string]any{
				"status":  "success",
				"message": fmt.Sprintf("stopped watching directory %s", dir),
			})
			if err != nil {
				return nil, fmt.Errorf("marshal stop_watch result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "list_rules",
		Description: "List all document classification rules configured in the system.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {}
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			rules, err := st.ListRules(ctx)
			if err != nil {
				return nil, fmt.Errorf("list rules: %w", err)
			}
			data, err := json.Marshal(map[string]any{
				"status": "success",
				"rules":  rules,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal list_rules result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "add_rule",
		Description: "Add a new classification rule to automatically categorize, tag, or assign correspondent/document type to ingested documents matching a pattern.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"pattern": {"type": "string", "description": "Case-insensitive substring text pattern to match in document"},
				"kind": {"type": "string", "enum": ["category", "tag", "correspondent", "document_type"], "description": "Metadata type to apply"},
				"value": {"type": "string", "description": "The metadata value to set or tag to add"}
			},
			"required": ["pattern", "kind", "value"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Pattern string `json:"pattern"`
				Kind    string `json:"kind"`
				Value   string `json:"value"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			rule, err := st.AddRule(ctx, args.Pattern, args.Kind, args.Value)
			if err != nil {
				return nil, fmt.Errorf("add rule: %w", err)
			}

			data, err := json.Marshal(map[string]any{
				"status": "success",
				"rule":   rule,
			})
			if err != nil {
				return nil, fmt.Errorf("marshal add_rule result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "delete_rule",
		Description: "Delete a document classification rule by ID.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"rule_id": {"type": "integer", "description": "The ID of the rule to delete"}
			},
			"required": ["rule_id"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				RuleID int64 `json:"rule_id"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}

			if err := st.DeleteRule(ctx, args.RuleID); err != nil {
				return nil, fmt.Errorf("delete rule %d: %w", args.RuleID, err)
			}

			data, err := json.Marshal(map[string]any{
				"status":  "success",
				"message": fmt.Sprintf("rule %d deleted successfully", args.RuleID),
			})
			if err != nil {
				return nil, fmt.Errorf("marshal delete_rule result: %w", err)
			}
			return string(data), nil
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "import_paperless",
		Description: "Import documents from a Paperless-ngx instance into the vault. Downloads original files and ingests them with preset metadata from Paperless (tags, correspondent, document type). Supports the same bounded pilot controls as the CLI.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"base_url": {"type": "string", "description": "Paperless-ngx instance URL (or set PAPERLESS_URL env)"},
				"token": {"type": "string", "description": "API token (or set PAPERLESS_TOKEN env)"},
				"since": {"type": "string", "description": "Only import documents whose Paperless created date is on or after this date (YYYY-MM-DD)"},
				"limit": {"type": "integer", "description": "Import at most N documents (newest first); 0 means no limit"},
				"ids": {"oneOf": [{"type": "array", "items": {"type": "integer"}}, {"type": "string"}], "description": "Import only these Paperless document IDs; takes precedence over since and limit"},
				"preserve_storage_paths": {"type": "boolean", "description": "Place notes under vault subdirectories derived from each document's Paperless storage path"},
				"report_path": {"type": "string", "description": "Write a JSON migration report to this path"},
				"dry_run": {"type": "boolean", "description": "List what would be imported without writing"},
				"vault_path": {"type": "string", "description": "Optional vault directory override; required if no default vault is configured"},
				"archive_path": {"type": "string", "description": "Optional archive directory override"},
				"db_path": {"type": "string", "description": "Optional SQLite database path override, for an isolated import run that does not touch the default document store"}
			},
			"required": ["base_url", "token"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				BaseURL              string          `json:"base_url"`
				Token                string          `json:"token"`
				Since                string          `json:"since"`
				Limit                int             `json:"limit"`
				IDs                  json.RawMessage `json:"ids"`
				PreserveStoragePaths bool            `json:"preserve_storage_paths"`
				ReportPath           string          `json:"report_path"`
				DryRun               bool            `json:"dry_run"`
				VaultPath            string          `json:"vault_path"`
				ArchivePath          string          `json:"archive_path"`
				DBPath               string          `json:"db_path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if args.Limit < 0 {
				return nil, fmt.Errorf("limit must be zero or positive")
			}
			ids, err := parseMCPDocumentIDs(args.IDs)
			if err != nil {
				return nil, fmt.Errorf("invalid ids: %w", err)
			}

			vault, archive, err := resolveVaultArchive(args.VaultPath, args.ArchivePath, defaultVault, defaultArchive)
			if err != nil {
				return nil, err
			}

			pipelineStore := st
			if args.DBPath != "" {
				overrideStore, err := store.Open(args.DBPath)
				if err != nil {
					return nil, fmt.Errorf("open db_path: %w", err)
				}
				defer overrideStore.Close()
				pipelineStore = overrideStore
			}

			var since time.Time
			if args.Since != "" {
				var err error
				since, err = time.Parse("2006-01-02", args.Since)
				if err != nil {
					return nil, fmt.Errorf("invalid since date: %w", err)
				}
			}

			pipeline := &ingest.Pipeline{
				Engine:     engine,
				Store:      pipelineStore,
				Writer:     &writer.NoteWriter{Vault: vault},
				ArchiveDir: archive,
			}

			opts := paperlessimport.Options{
				BaseURL:              args.BaseURL,
				Token:                args.Token,
				Since:                since,
				DryRun:               args.DryRun,
				Limit:                args.Limit,
				IDs:                  ids,
				PreserveStoragePaths: args.PreserveStoragePaths,
			}

			stats, err := paperlessimport.Run(ctx, opts, pipeline)
			if err != nil {
				return nil, err
			}

			result := map[string]any{
				"status":   "success",
				"imported": stats.Imported,
				"skipped":  stats.Skipped,
				"failed":   stats.Failed,
				"total":    stats.Total,
			}
			if (args.Limit > 0 || len(ids) > 0) && len(stats.SelectedIDs) > 0 {
				result["selected_ids"] = stats.SelectedIDs
			}
			if stats.Audit != nil {
				result["audit"] = stats.Audit
			}
			if args.ReportPath != "" {
				if err := paperlessimport.WriteMigrationReport(args.ReportPath, stats.BuildMigrationReport(args.DryRun)); err != nil {
					return nil, fmt.Errorf("write report_path: %w", err)
				}
				result["report_path"] = args.ReportPath
			}

			data, mErr := json.Marshal(result)
			if mErr != nil {
				return nil, fmt.Errorf("marshal import result: %w", mErr)
			}
			return string(data), nil
		},
	})

	registerPDFTools(server, st, engine, defaultVault, defaultArchive, pdfops.DefaultTools())
}

// StopAllWatchers cancels all active watchers and closes their resources.
// Call this during MCP server shutdown to prevent goroutine leaks.
func StopAllWatchers() {
	activeWatchers.Range(func(key, value any) bool {
		if entry, ok := value.(*watcherEntry); ok && entry != nil {
			entry.cancel()
			if entry.watcher != nil {
				entry.watcher.Close()
			}
		}
		activeWatchers.Delete(key)
		return true
	})
}
