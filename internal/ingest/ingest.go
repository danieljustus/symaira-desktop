package ingest

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// IngestFile copies a file into vaultRoot/inbox and creates a corresponding markdown note.
// Returns the relative path of the new markdown note.
func IngestFile(vaultRoot, sourcePath string) (string, error) {
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
	defer src.Close()

	dst, err := os.Create(targetFilePath)
	if err != nil {
		return "", fmt.Errorf("failed to create target file: %w", err)
	}
	defer dst.Close()

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
		body = fmt.Sprintf("\n![[%s]]\n\n*OCR Text pending...*\n", relAssetPath)
	} else if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
		body = fmt.Sprintf("\n![[%s]]\n\n*Image description pending...*\n", relAssetPath)
	} else {
		body = fmt.Sprintf("\n[[%s]]\n\n*Content pending...*\n", relAssetPath)
	}

	if err := os.WriteFile(notePath, []byte(frontmatter+body), 0644); err != nil {
		return "", fmt.Errorf("failed to write markdown note: %w", err)
	}

	return filepath.Join("inbox", noteName), nil
}
