package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The ingest pipeline runs in-process since the repo consolidation absorbed
// symaira-ingest into this repository. These seams keep it out of a plain
// `go test ./...`, which would otherwise write into the developer's real
// vault, archive and document store under $HOME and reach their real IMAP
// accounts. Tests override them through testsupport.IsolateSideEffects.
//
// Every consumer of the pipeline in this repository goes through this one
// list — including internal/mail and internal/selfhost, which cannot host
// their own seams without an import cycle through testsupport.
var (
	IngestFunc       = Ingest
	JobsFunc         = Jobs
	RetryJobFunc     = RetryJob
	SplitPDFFunc     = SplitPDFAtSpec
	ExtractTextFunc  = ExtractText
	MailAccountsFunc = MailAccounts
	FetchMailFunc    = FetchMail
)

// HasSymingest reports whether the ingest pipeline is usable, and why not when
// it is not.
//
// The pipeline is linked into this binary rather than resolved as a sibling
// process, so it is always present; the function survives because call sites
// and the CLI's status output still ask. It reports unavailable only when the
// configuration cannot be read at all.
func HasSymingest() (bool, string) {
	if _, err := ArchivePath(); err != nil {
		return false, fmt.Sprintf("ingest configuration unavailable: %v", err)
	}
	return true, ""
}

// IngestFile copies a file into the vault and creates a corresponding
// markdown note, running OCR and metadata extraction through the in-process
// ingest pipeline. Returns the relative path of the new markdown note.
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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	result, err := IngestFunc(ctx, sourcePath, Options{Vault: vaultRoot})
	if err != nil {
		// Fall back to the built-in inbox copy only when the pipeline could
		// not run at all, so a file is never lost for want of configuration.
		// Every other failure — a corrupt source, a duplicate, a broken OCR
		// run — is reported: silently writing a placeholder note in those
		// cases would hide the real problem behind a plausible-looking note.
		if errors.Is(err, ErrNoVault) {
			return ingestBuiltin(vaultRoot, sourcePath)
		}
		return "", err
	}

	if filepath.IsAbs(result.VaultPath) {
		if rel, relErr := filepath.Rel(vaultRoot, result.VaultPath); relErr == nil {
			return rel, nil
		}
	}
	return result.VaultPath, nil
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
	return HasQPDF() || HasPopplerSplit()
}

// ingestBuiltin is the fallback copy-to-inbox path when the ingest pipeline
// cannot run — no vault configured, or no extraction engine available.
func ingestBuiltin(vaultRoot, sourcePath string) (string, error) {
	inboxDir := filepath.Join(vaultRoot, "inbox")
	//nolint:gosec // G301: the vault inbox is group/other readable on purpose —
	// other Symaira tools and the user's editor read it. Tightening it here
	// would silently diverge from every other vault directory.
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
		body = fmt.Sprintf("\n![[%s]]\n\n*OCR Text pending (ingest pipeline unavailable)...*\n", relAssetPath)
	} else if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
		body = fmt.Sprintf("\n![[%s]]\n\n*Image description pending (ingest pipeline unavailable)...*\n", relAssetPath)
	} else {
		body = fmt.Sprintf("\n[[%s]]\n\n*Content pending (ingest pipeline unavailable)...*\n", relAssetPath)
	}

	if err := os.WriteFile(notePath, []byte(frontmatter+body), 0644); err != nil {
		return "", fmt.Errorf("failed to write markdown note: %w", err)
	}

	return filepath.Join("inbox", noteName), nil
}

// IngestJobs lists the queued ingest jobs as a JSON array.
func IngestJobs() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	jobs, err := JobsFunc(ctx, Options{}, 0)
	if err != nil {
		return "[]", err
	}
	if jobs == nil {
		jobs = []Job{}
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return "[]", fmt.Errorf("failed to marshal jobs: %w", err)
	}
	return string(data), nil
}

// IngestRetry retries a failed job by ID.
func IngestRetry(jobID string) error {
	id, err := strconv.ParseInt(strings.TrimSpace(jobID), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid job ID %q: %w", jobID, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return RetryJobFunc(ctx, Options{}, id)
}
