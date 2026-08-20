package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-ingest/internal/extract"
	"github.com/danieljustus/symaira-ingest/internal/ingest"
	"github.com/danieljustus/symaira-ingest/internal/pdfops"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"github.com/danieljustus/symaira-ingest/internal/writer"
)

func registerPDFTools(server *mcpserver.Server, st *store.Store, engine extract.Engine, defaultVault, defaultArchive string, tools pdfops.Tools) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "split_pdf",
		Description: "Split a PDF after selected pages without modifying the original; optionally ingest the generated parts into the vault.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"input_path": {"type": "string", "description": "PDF to split"},
				"split_at": {"type": "string", "description": "Page boundaries such as 2,4"},
				"output_dir": {"type": "string", "description": "Directory for generated part PDFs"},
				"ingest": {"type": "boolean", "description": "Run generated PDFs through the normal ingest pipeline"}
			},
			"required": ["input_path", "split_at"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				InputPath string `json:"input_path"`
				SplitAt   string `json:"split_at"`
				OutputDir string `json:"output_dir"`
				Ingest    bool   `json:"ingest"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.InputPath) == "" || strings.TrimSpace(args.SplitAt) == "" {
				return nil, fmt.Errorf("input_path and split_at are required")
			}
			inputPath, err := filepath.Abs(args.InputPath)
			if err != nil {
				return nil, fmt.Errorf("invalid input_path: %w", err)
			}
			outputDir := args.OutputDir
			if outputDir == "" {
				outputDir = strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".parts"
			}
			outputDir, err = filepath.Abs(outputDir)
			if err != nil {
				return nil, fmt.Errorf("invalid output_dir: %w", err)
			}
			outputs, err := tools.Split(ctx, inputPath, args.SplitAt, outputDir)
			if err != nil {
				return nil, err
			}
			return completePDFToolResult(ctx, st, engine, defaultVault, defaultArchive, outputs, args.Ingest)
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "merge_pdf",
		Description: "Merge two or more PDFs into a new PDF without modifying the inputs; optionally ingest the result into the vault.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"input_paths": {"type": "array", "items": {"type": "string"}, "description": "Two or more input PDFs"},
				"output_path": {"type": "string", "description": "Output PDF path"},
				"ingest": {"type": "boolean", "description": "Run the generated PDF through the normal ingest pipeline"}
			},
			"required": ["input_paths", "output_path"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				InputPaths []string `json:"input_paths"`
				OutputPath string   `json:"output_path"`
				Ingest     bool     `json:"ingest"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if len(args.InputPaths) < 2 || strings.TrimSpace(args.OutputPath) == "" {
				return nil, fmt.Errorf("input_paths must contain at least two PDFs and output_path is required")
			}
			var err error
			inputs := make([]string, len(args.InputPaths))
			for i, path := range args.InputPaths {
				inputs[i], err = filepath.Abs(path)
				if err != nil {
					return nil, fmt.Errorf("invalid input_paths[%d]: %w", i, err)
				}
			}
			output, err := filepath.Abs(args.OutputPath)
			if err != nil {
				return nil, fmt.Errorf("invalid output_path: %w", err)
			}
			if err := tools.Merge(ctx, inputs, output); err != nil {
				return nil, err
			}
			return completePDFToolResult(ctx, st, engine, defaultVault, defaultArchive, []string{output}, args.Ingest)
		},
	})

	server.RegisterTool(&mcpserver.Tool{
		Name:        "rotate_pdf",
		Description: "Rotate selected pages in a new PDF without modifying the input; qpdf is required for this operation.",
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"input_path": {"type": "string", "description": "PDF to rotate"},
				"output_path": {"type": "string", "description": "Output PDF path"},
				"degrees": {"type": "integer", "description": "Rotation: -270, -180, -90, 90, 180 or 270"},
				"pages": {"type": "string", "description": "Optional page selector such as 1,3-4; empty means all pages"},
				"ingest": {"type": "boolean", "description": "Run the generated PDF through the normal ingest pipeline"}
			},
			"required": ["input_path", "output_path", "degrees"]
		}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				InputPath  string `json:"input_path"`
				OutputPath string `json:"output_path"`
				Degrees    int    `json:"degrees"`
				Pages      string `json:"pages"`
				Ingest     bool   `json:"ingest"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, fmt.Errorf("invalid arguments: %w", err)
			}
			if strings.TrimSpace(args.InputPath) == "" || strings.TrimSpace(args.OutputPath) == "" {
				return nil, fmt.Errorf("input_path and output_path are required")
			}
			inputPath, err := filepath.Abs(args.InputPath)
			if err != nil {
				return nil, fmt.Errorf("invalid input_path: %w", err)
			}
			outputPath, err := filepath.Abs(args.OutputPath)
			if err != nil {
				return nil, fmt.Errorf("invalid output_path: %w", err)
			}
			if err := tools.Rotate(ctx, inputPath, outputPath, args.Degrees, args.Pages); err != nil {
				return nil, err
			}
			return completePDFToolResult(ctx, st, engine, defaultVault, defaultArchive, []string{outputPath}, args.Ingest)
		},
	})
}

func completePDFToolResult(ctx context.Context, st *store.Store, engine extract.Engine, defaultVault, defaultArchive string, outputs []string, shouldIngest bool) (string, error) {
	result := map[string]any{
		"status":         "success",
		"output_paths":   outputs,
		"ingested_paths": []string{},
	}
	if shouldIngest {
		vault, archive, err := resolveVaultArchive("", "", defaultVault, defaultArchive)
		if err != nil {
			return "", err
		}
		pipeline := &ingest.Pipeline{
			Engine:     engine,
			Store:      st,
			Writer:     &writer.NoteWriter{Vault: vault},
			ArchiveDir: archive,
		}
		var notes []string
		for _, output := range outputs {
			res, err := pipeline.Ingest(ctx, output, nil)
			if err != nil {
				return "", fmt.Errorf("ingest generated PDF %s: %w", output, err)
			}
			notes = append(notes, res.VaultPath)
		}
		result["ingested_paths"] = notes
	}
	data, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("marshal PDF tool result: %w", err)
	}
	return string(data), nil
}
