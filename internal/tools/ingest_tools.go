package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

// ingestOptions routes the absorbed ingest facade at the same vault the CLI
// writes into, so MCP-triggered re-OCR and rule edits act on the same
// document store as `symdesk ingest` / `symdesk rules` (issue #636).
func ingestOptions(cfg *config.Config) ingest.Options {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	return ingest.Options{Vault: cfg.Vault}
}

// newIngestReocrTool exposes the reocr job kind (re-run OCR/extraction for an
// already-ingested document) as an MCP tool. It is the canonical surface
// behind the legacy `reocr` alias and returns the same envelope the CLI
// writes, so a client written against symingest 0.12.1 can parse the result.
func newIngestReocrTool(cfg *config.Config) *Tool {
	return &Tool{
		Name:        "desk_ingest_reocr",
		Description: "Re-runs OCR/extraction for an already-ingested document, either by its registered archived source path or by document ID, and refreshes the existing note in place. Legacy alias: reocr.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"document_id":{"type":"integer","description":"reprocess by document ID"},"archive_path":{"type":"string","description":"the registered archived original path"},"source":{"type":"string","description":"legacy alias for archive_path"}},"anyOf":[{"required":["document_id"]},{"required":["archive_path"]}]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				DocumentID  int64  `json:"document_id"`
				ArchivePath string `json:"archive_path"`
				Source      string `json:"source"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Source != "" && args.ArchivePath == "" {
				args.ArchivePath = args.Source
			}
			if args.ArchivePath == "" && args.DocumentID == 0 {
				return nil, fmt.Errorf("document_id or archive_path is required")
			}
			if args.ArchivePath != "" && args.DocumentID != 0 {
				return nil, fmt.Errorf("provide either document_id or archive_path, not both")
			}
			opts := ingestOptions(cfg)
			var res *ingest.ReprocessResult
			var err error
			if args.DocumentID != 0 {
				res, err = ingest.ReprocessFunc(ctx, opts, args.DocumentID)
			} else {
				res, err = ingest.ReprocessByArchivePathFunc(ctx, opts, args.ArchivePath)
			}
			code := "internal"
			switch {
			case errors.Is(err, ingest.ErrDocumentNotFound):
				code = "not_found"
			case errors.Is(err, ingest.ErrNoArchivedOriginal):
				code = "no_archived_original"
			}
			if err != nil && code == "internal" {
				return nil, err
			}
			// The legacy envelope always carries document_id and status, so a
			// failing reprocess is reported through the envelope rather than as
			// a bare tool error (parity with `symdesk ingest reocr --json`).
			resp := map[string]any{
				"schema_version": ingest.SchemaVersion,
				"document_id":    args.DocumentID,
				"status":         "completed",
			}
			if err != nil {
				resp["status"] = "failed"
				resp["error"] = map[string]string{"code": code, "message": err.Error()}
				return resp, nil
			}
			if res != nil {
				resp["document_id"] = res.DocumentID
				resp["job_id"] = res.JobID
				resp["status"] = res.Status
				resp["output_path"] = res.OutputPath
			}
			return resp, nil
		},
	}
}

// newRulesListTool lists the configured classification rules. Canonical
// surface behind the legacy `list_rules` alias.
func newRulesListTool(cfg *config.Config) *Tool {
	return &Tool{
		Name:        "desk_rules_list",
		Description: "Lists the configured document classification rules. Legacy alias: list_rules.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			rules, err := ingest.RulesFunc(ctx, ingestOptions(cfg))
			if err != nil {
				return nil, err
			}
			if rules == nil {
				rules = []ingest.Rule{}
			}
			return map[string]any{"schema_version": ingest.SchemaVersion, "rules": rules}, nil
		},
	}
}

// newRulesAddTool adds a classification rule. Mutating — gated behind the
// existing write gate like every other rule edit. Canonical surface behind
// the legacy `add_rule` alias.
func newRulesAddTool(cfg *config.Config) *Tool {
	return &Tool{
		Name:        "desk_rules_add",
		Description: "Adds a document classification rule. kind must be one of category, tag, correspondent or document_type. Legacy alias: add_rule.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"case-insensitive substring matched against extracted document text"},"kind":{"type":"string","enum":["category","tag","correspondent","document_type"]},"value":{"type":"string","description":"the category, tag, correspondent or document type to assign"}},"required":["pattern","kind","value"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Pattern string `json:"pattern"`
				Kind    string `json:"kind"`
				Value   string `json:"value"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Pattern == "" || args.Kind == "" || args.Value == "" {
				return nil, fmt.Errorf("pattern, kind and value are required")
			}
			rule, err := ingest.AddRuleFunc(ctx, ingestOptions(cfg), args.Pattern, args.Kind, args.Value)
			if err != nil {
				return nil, err
			}
			return map[string]any{"schema_version": ingest.SchemaVersion, "rule": rule}, nil
		},
	}
}

// newRulesUpdateTool replaces an existing classification rule's fields.
// Mutating.
func newRulesUpdateTool(cfg *config.Config) *Tool {
	return &Tool{
		Name:        "desk_rules_update",
		Description: "Updates an existing document classification rule by ID, replacing its pattern, kind and value.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"rule ID"},"pattern":{"type":"string"},"kind":{"type":"string","enum":["category","tag","correspondent","document_type"]},"value":{"type":"string"}},"required":["id","pattern","kind","value"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				ID      int64  `json:"id"`
				Pattern string `json:"pattern"`
				Kind    string `json:"kind"`
				Value   string `json:"value"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.ID == 0 || args.Pattern == "" || args.Kind == "" || args.Value == "" {
				return nil, fmt.Errorf("id, pattern, kind and value are required")
			}
			rule, err := ingest.UpdateRuleFunc(ctx, ingestOptions(cfg), args.ID, args.Pattern, args.Kind, args.Value)
			if err != nil {
				return nil, err
			}
			return map[string]any{"schema_version": ingest.SchemaVersion, "rule": rule}, nil
		},
	}
}

// newRulesDeleteTool deletes a classification rule by ID. Mutating — the
// canonical surface behind the legacy `delete_rule` alias.
func newRulesDeleteTool(cfg *config.Config) *Tool {
	return &Tool{
		Name:        "desk_rules_delete",
		Description: "Deletes a document classification rule by ID. Legacy alias: delete_rule.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"integer","description":"rule ID"}},"required":["id"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.ID == 0 {
				return nil, fmt.Errorf("id is required")
			}
			if err := ingest.DeleteRuleFunc(ctx, ingestOptions(cfg), args.ID); err != nil {
				return nil, err
			}
			return map[string]any{"schema_version": ingest.SchemaVersion, "id": args.ID, "deleted": true}, nil
		},
	}
}

// newSplitPDFTool exposes the in-process PDF split (ingest.SplitPDFAtSpec) as
// an MCP tool. Mutating: it writes part PDFs to the given output directory.
// It fails with a clear message when the Poppler utilities the split needs
// are absent. Canonical surface behind the legacy `split_pdf` alias.
func newSplitPDFTool() *Tool {
	return &Tool{
		Name:        "desk_split_pdf",
		Description: "Splits a PDF into parts after the given pages and writes them into an output directory. at is a comma-separated page spec such as \"2,4\" or \"2-3,6\". Requires the Poppler utilities (pdfinfo, pdfseparate, pdfunite) on PATH. Legacy alias: split_pdf.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"at":{"type":"string","description":"split after these pages, e.g. 2,4 or 2-3,6"},"output_dir":{"type":"string","description":"directory for the generated part PDFs"}},"required":["input","at","output_dir"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Input     string `json:"input"`
				At        string `json:"at"`
				OutputDir string `json:"output_dir"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Input == "" || args.At == "" || args.OutputDir == "" {
				return nil, fmt.Errorf("input, at and output_dir are required")
			}
			if !ingest.HasPopplerSplit() {
				return nil, fmt.Errorf("PDF splitting requires the Poppler utilities pdfinfo, pdfseparate and pdfunite on PATH")
			}
			parts, err := ingest.SplitPDFFunc(ctx, args.Input, args.At, args.OutputDir)
			if err != nil {
				return nil, err
			}
			return map[string]any{"parts": parts}, nil
		},
	}
}

// newMergePDFTool exposes the in-process PDF merge (ingest.MergePDFs) as an
// MCP tool. Mutating: it writes the merged PDF to the given output path.
// It fails with a clear message when the Poppler pdfunite utility is absent.
// Canonical surface behind the legacy `merge_pdf` alias.
func newMergePDFTool() *Tool {
	return &Tool{
		Name:        "desk_merge_pdf",
		Description: "Merges two or more PDFs into one output file without modifying the inputs. inputs is an array of PDF paths; output is the destination PDF path. Requires the Poppler utility pdfunite on PATH. Legacy alias: merge_pdf.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"inputs":{"type":"array","items":{"type":"string"},"description":"input PDF paths, at least two"},"output":{"type":"string","description":"destination PDF path"}},"required":["inputs","output"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Inputs []string `json:"inputs"`
				Output string   `json:"output"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if len(args.Inputs) < 2 {
				return nil, fmt.Errorf("at least two input PDFs are required")
			}
			if args.Output == "" {
				return nil, fmt.Errorf("output is required")
			}
			if err := ingest.MergePDFsFunc(ctx, args.Inputs, args.Output); err != nil {
				return nil, err
			}
			return map[string]any{"output": args.Output}, nil
		},
	}
}

// newRotatePDFTool exposes the in-process PDF rotation (ingest.RotatePDF) as
// an MCP tool. Mutating: it writes the rotated PDF to the given output path.
// It fails with a clear message when qpdf is absent. Canonical surface behind
// the legacy `rotate_pdf` alias.
func newRotatePDFTool() *Tool {
	return &Tool{
		Name:        "desk_rotate_pdf",
		Description: "Rotates pages of a PDF and writes the result to an output path without modifying the input. degrees must be one of -270, -180, -90, 90, 180, 270; pages is an optional comma-separated 1-based page selector (e.g. 1-3,5), empty rotates all pages. Requires qpdf on PATH. Legacy alias: rotate_pdf.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"output":{"type":"string","description":"destination PDF path"},"degrees":{"type":"integer","description":"rotation in degrees: -270, -180, -90, 90, 180 or 270"},"pages":{"type":"string","description":"optional page selector, e.g. 1-3,5; empty rotates all pages"}},"required":["input","output","degrees"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Input   string `json:"input"`
				Output  string `json:"output"`
				Degrees int    `json:"degrees"`
				Pages   string `json:"pages"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Input == "" || args.Output == "" {
				return nil, fmt.Errorf("input and output are required")
			}
			if err := ingest.RotatePDFFunc(ctx, args.Input, args.Output, args.Degrees, args.Pages); err != nil {
				return nil, err
			}
			return map[string]any{"output": args.Output}, nil
		},
	}
}
