package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

func seedReocrDocument(t *testing.T, vaultDir string) *ingest.Result {
	t.Helper()
	srcFile := filepath.Join(t.TempDir(), "invoice.txt")
	if err := os.WriteFile(srcFile, []byte("Invoice #42 from Acme Corp."), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := ingest.Ingest(context.Background(), srcFile, ingest.Options{Vault: vaultDir})
	if err != nil {
		t.Fatalf("seed ingest failed: %v", err)
	}
	return res
}

func TestIngestReocrByArchivePathJSON(t *testing.T) {
	isolateIngestConfig(t)
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	res := seedReocrDocument(t, vaultDir)

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	cmd := newIngestReocrCmd()
	cmd.SetContext(context.Background())
	out, err := runCommand(t, cmd, []string{res.ArchivePath})
	if err != nil {
		t.Fatalf("ingest reocr failed: %v", err)
	}
	var resp reocrResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("ingest reocr output is not valid JSON: %v\noutput: %s", err, out)
	}
	if resp.Status != "completed" || resp.DocumentID == 0 || resp.OutputPath == "" {
		t.Fatalf("unexpected reocr response: %+v", resp)
	}
	if resp.Error != nil {
		t.Errorf("expected no error object on success, got %+v", resp.Error)
	}
}

func TestIngestReocrByDocumentIDJSON(t *testing.T) {
	isolateIngestConfig(t)
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	res := seedReocrDocument(t, vaultDir)
	first, err := ingest.ReprocessByArchivePath(context.Background(), ingest.Options{Vault: vaultDir}, res.ArchivePath)
	if err != nil {
		t.Fatalf("discovering document id failed: %v", err)
	}

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	cmd := newIngestReocrCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("document-id", strconv.FormatInt(first.DocumentID, 10)); err != nil {
		t.Fatal(err)
	}
	out, err := runCommand(t, cmd, nil)
	if err != nil {
		t.Fatalf("ingest reocr --document-id failed: %v", err)
	}
	var resp reocrResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if resp.DocumentID != first.DocumentID || resp.Status != "completed" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestIngestReocrUnknownDocumentIDErrorEnvelope(t *testing.T) {
	isolateIngestConfig(t)
	vaultDir := t.TempDir()
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	cmd := newIngestReocrCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("document-id", "9999"); err != nil {
		t.Fatal(err)
	}
	out, runErr := runCommand(t, cmd, nil)
	if runErr == nil {
		t.Fatal("expected an error for an unknown document id")
	}
	var reported jsonReportedError
	if !errors.As(runErr, &reported) {
		t.Fatalf("expected the error to be reported as JSON, got %T: %v", runErr, runErr)
	}
	var resp reocrResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("reocr error output is not valid JSON: %v\noutput: %s", err, out)
	}
	if resp.Status != "failed" || resp.Error == nil || resp.Error.Code != "not_found" {
		t.Fatalf("unexpected error envelope: %+v", resp)
	}
}

// The ErrNoArchivedOriginal error path (a document row with no archive_path)
// requires reaching into the document store directly to construct, which
// cmd/symdesk cannot do (internal/ingest/internal is off-limits per the
// module boundary guard). It is covered facade-side in
// internal/ingest/reprocess_test.go; this package only needs to prove the
// CLI plumbs *a* facade error into the {status:"failed", error:{...}}
// envelope correctly, which TestIngestReocrUnknownDocumentIDErrorEnvelope
// above already does for ErrDocumentNotFound.

func TestIngestReocrMissingArgumentError(t *testing.T) {
	isolateIngestConfig(t)
	origCfg := cfg
	cfg = &config.Config{Vault: t.TempDir()}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = false
	cmd := newIngestReocrCmd()
	cmd.SetContext(context.Background())
	if _, err := runCommand(t, cmd, nil); err == nil {
		t.Error("expected error when neither archive path nor --document-id is given")
	}
}

func TestIngestReocrConflictingArgumentsError(t *testing.T) {
	isolateIngestConfig(t)
	origCfg := cfg
	cfg = &config.Config{Vault: t.TempDir()}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = false
	cmd := newIngestReocrCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("document-id", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := runCommand(t, cmd, []string{"/some/archive.pdf"}); err == nil {
		t.Error("expected error when both an archive path and --document-id are given")
	}
}
