package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// HasSymingest checks if symingest is available (via $SYMAIRA_BIN, the
// managed runtime directory ~/.symaira/bin, or PATH — see compose.Resolve)
// and supports schema_version 1. Returns a boolean indicating compatibility,
// and an error string if there's a mismatch.
func HasSymingest() (bool, string) {
	path, err := compose.Resolve("symingest")
	if err != nil {
		return false, "symingest not found"
	}

	cmd := exec.Command(path, "version", "--json")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return false, fmt.Sprintf("symingest version failed: %v", err)
	}

	var ver struct {
		SchemaVersion int    `json:"schema_version"`
		Version       string `json:"version"`
	}
	if err := json.Unmarshal(out.Bytes(), &ver); err != nil {
		return false, "failed to parse symingest version output"
	}

	if ver.SchemaVersion != 1 {
		return false, fmt.Sprintf("unsupported symingest schema version %d (expected 1)", ver.SchemaVersion)
	}

	return true, ""
}

// IngestFile copies a file into vaultRoot/inbox and creates a corresponding markdown note.
// If symingest is available, it delegates to symingest to perform OCR and metadata extraction.
// Returns the relative path of the new markdown note.
func IngestFile(vaultRoot, sourcePath string) (string, error) {
	// Attempt barcode-based splitting for PDFs.
	if strings.ToLower(filepath.Ext(sourcePath)) == ".pdf" {
		if cfg := DefaultBarcodeConfig(); HasPdfToPPM() || hasSplitTools() {
			notes, err := ingestPDFWithSplit(vaultRoot, sourcePath, cfg)
			if err == nil && len(notes) > 0 {
				return notes[0], nil // backward-compat: return first note
			}
			// Split failed or no separators found — fall through to normal ingest.
		}
	}

	ok, _ := HasSymingest()
	if bin, err := compose.Resolve("symingest"); ok && err == nil {
		// Attempt to use symingest ingest with --json (expected contract)
		cmd := exec.Command(bin, "ingest", "--vault", vaultRoot, "--json", sourcePath)
		var out bytes.Buffer
		cmd.Stdout = &out
		runErr := cmd.Run()
		if runErr == nil {
			var result struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err == nil && result.Path != "" {
				// symingest might return absolute path or relative, let's ensure we return relative
				if filepath.IsAbs(result.Path) {
					if rel, err := filepath.Rel(vaultRoot, result.Path); err == nil {
						return rel, nil
					}
				}
				return result.Path, nil
			}
		}

		// If --json fails (e.g. flag not defined in older v0.7.0 binaries), fallback to standard execution
		cmd = exec.Command(bin, "ingest", "--vault", vaultRoot, sourcePath)
		if err := cmd.Run(); err != nil {
			return "", fmt.Errorf("symingest failed: %w", err)
		}

		// symingest by default creates <basename>.md in the vault root
		baseName := filepath.Base(sourcePath)
		return baseName + ".md", nil
	}

	// Fallback to built-in copy behavior
	return ingestBuiltin(vaultRoot, sourcePath)
}

// IngestFileWithBarcodeSplit ingests a file with configurable barcode-based
// splitting for multi-page PDFs.
//
// When the input is a PDF and barcode scanning tools are available:
//  1. Each page is scanned for a separator barcode matching cfg.SeparatorPattern.
//  2. The PDF is split at separator boundaries.
//  3. Each part is ingested as a separate document.
//
// When no separators are found, or the input is not a PDF, or scanning tools
// are unavailable, the file is ingested as a single document.
//
// Returns a slice of relative note paths (one per resulting document).
func IngestFileWithBarcodeSplit(vaultRoot, sourcePath string, cfg BarcodeConfig) ([]string, error) {
	if strings.ToLower(filepath.Ext(sourcePath)) == ".pdf" && (HasPdfToPPM() || hasSplitTools()) {
		notes, err := ingestPDFWithSplit(vaultRoot, sourcePath, cfg)
		if err == nil && len(notes) > 0 {
			return notes, nil
		}
		// Split failed or no separators — fall through to normal ingest.
	}

	notePath, err := IngestFile(vaultRoot, sourcePath)
	if err != nil {
		return nil, err
	}
	return []string{notePath}, nil
}

// ingestPDFWithSplit attempts barcode-based splitting and ingests each part.
func ingestPDFWithSplit(vaultRoot, pdfPath string, cfg BarcodeConfig) ([]string, error) {
	tmpDir, err := os.MkdirTemp("", "symdesk-split-*")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	result, err := SplitPDFByBarcode(pdfPath, cfg, tmpDir)
	if err != nil {
		return nil, fmt.Errorf("barcode split: %w", err)
	}

	if len(result.Parts) <= 1 && len(result.SeparatorPages) == 0 {
		// No separators found — nothing to split.
		return nil, nil
	}

	var notes []string
	for i, partPath := range result.Parts {
		notePath, err := IngestFile(vaultRoot, partPath)
		if err != nil {
			// Per-document failure: record but continue.
			result.Errors[i+1] = err.Error()
			continue
		}
		notes = append(notes, notePath)
	}

	if len(notes) == 0 {
		return nil, fmt.Errorf("all parts failed to ingest")
	}

	return notes, nil
}

// hasSplitTools reports whether any PDF splitting tool is available.
func hasSplitTools() bool {
	return HasQPDF() || func() bool { ok, _ := HasSymingest(); return ok }()
}

// ingestBuiltin is the fallback copy-to-inbox path when symingest is not available.
func ingestBuiltin(vaultRoot, sourcePath string) (string, error) {
	inboxDir := filepath.Join(vaultRoot, "inbox")
	if err := os.MkdirAll(inboxDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create inbox dir: %w", err)
	}

	baseName := filepath.Base(sourcePath)
	ext := filepath.Ext(baseName)
	nameWithoutExt := strings.TrimSuffix(baseName, ext)

	timestamp := time.Now().Format("20060102_150405")
	targetFileName := fmt.Sprintf("%s_%s%s", nameWithoutExt, timestamp, ext)
	targetFilePath := filepath.Join(inboxDir, targetFileName)

	// Copy the file
	src, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("failed to open source file: %w", err)
	}
	defer func() { _ = src.Close() }()

	dst, err := os.Create(targetFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create target file: %w", err)
	}
	defer func() { _ = dst.Close() }()

	if _, err := io.Copy(dst, src); err != nil {
		return "", fmt.Errorf("failed to copy file: %w", err)
	}

	// Create corresponding markdown note
	noteName := fmt.Sprintf("Ingest_%s.md", timestamp)
	notePath := filepath.Join(inboxDir, noteName)
	relAssetPath := targetFileName // in the same directory

	frontmatter := fmt.Sprintf(`---
title: "Ingested: %s"
status: "inbox"
date: "%s"
---
`, baseName, time.Now().Format(time.RFC3339))

	var body string
	if ext == ".pdf" {
		body = fmt.Sprintf("\n![[%s]]\n\n*OCR Text pending (symingest not installed)...*\n", relAssetPath)
	} else if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
		body = fmt.Sprintf("\n![[%s]]\n\n*Image description pending (symingest not installed)...*\n", relAssetPath)
	} else {
		body = fmt.Sprintf("\n[[%s]]\n\n*Content pending (symingest not installed)...*\n", relAssetPath)
	}

	if err := os.WriteFile(notePath, []byte(frontmatter+body), 0644); err != nil {
		return "", fmt.Errorf("failed to write markdown note: %w", err)
	}

	return filepath.Join("inbox", noteName), nil
}

// IngestJobs lists the jobs from symingest.
func IngestJobs() (string, error) {
	ok, _ := HasSymingest()
	if !ok {
		return "[]", fmt.Errorf("symingest not installed")
	}
	bin, err := compose.Resolve("symingest")
	if err != nil {
		return "[]", fmt.Errorf("symingest not installed: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "jobs", "--json")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "[]", err
	}
	return out.String(), nil
}

// IngestRetry retries a failed job by ID in symingest.
func IngestRetry(jobID string) error {
	ok, _ := HasSymingest()
	if !ok {
		return fmt.Errorf("symingest not installed")
	}
	bin, err := compose.Resolve("symingest")
	if err != nil {
		return fmt.Errorf("symingest not installed: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "retry", jobID)
	return cmd.Run()
}
