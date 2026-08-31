package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
)

func TestSearchExportAndNotebookCommands(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "source.md"), []byte("---\ntitle: Source\n---\n\ninvoice body"), 0o600); err != nil {
		t.Fatal(err)
	}

	originalConfig := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = originalConfig })
	originalJSON := jsonFlag
	jsonFlag = true
	t.Cleanup(func() { jsonFlag = originalJSON })

	exportCmd := newSearchExportCmd()
	if err := exportCmd.Flags().Set("format", "markdown"); err != nil {
		t.Fatal(err)
	}
	if err := exportCmd.Flags().Set("title", "Invoices"); err != nil {
		t.Fatal(err)
	}
	if err := exportCmd.RunE(exportCmd, []string{"invoice"}); err != nil {
		t.Fatalf("search export command failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "search-results", "invoices.md")); err != nil {
		t.Fatalf("search export did not write the result note: %v", err)
	}

	notebookCmd := newSearchNotebookCmd()
	if err := notebookCmd.Flags().Set("title", "Invoice working set"); err != nil {
		t.Fatal(err)
	}
	if err := notebookCmd.RunE(notebookCmd, []string{"invoice"}); err != nil {
		t.Fatalf("search notebook command failed: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(vaultDir, "notebooks", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("notebook command wrote %d notebook directories, want 1", len(matches))
	}
}

func TestSearchSubcommandsPropagateSetupAndQueryErrors(t *testing.T) {
	originalConfig := cfg
	t.Cleanup(func() { cfg = originalConfig })

	cfg = &config.Config{}
	if err := newSearchExportCmd().RunE(newSearchExportCmd(), []string{"invoice"}); err == nil {
		t.Fatal("search export accepted an unconfigured vault")
	}
	if err := newSearchNotebookCmd().RunE(newSearchNotebookCmd(), []string{"invoice"}); err == nil {
		t.Fatal("search notebook accepted an unconfigured vault")
	}

	cfg = &config.Config{Vault: t.TempDir()}
	exportCmd := newSearchExportCmd()
	if err := exportCmd.RunE(exportCmd, []string{""}); err == nil {
		t.Fatal("search export accepted an empty query")
	}
	notebookCmd := newSearchNotebookCmd()
	if err := notebookCmd.RunE(notebookCmd, []string{""}); err == nil {
		t.Fatal("search notebook accepted an empty query")
	}
}

func TestIndexStatusCommandModes(t *testing.T) {
	vaultDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(vaultDir, "source.md"), []byte("# source"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalConfig := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = originalConfig })
	originalJSON := jsonFlag
	jsonFlag = true
	t.Cleanup(func() { jsonFlag = originalJSON })

	documentsCmd := newIndexStatusCmd()
	if err := documentsCmd.Flags().Set("documents", "true"); err != nil {
		t.Fatal(err)
	}
	if err := documentsCmd.RunE(documentsCmd, nil); err != nil {
		t.Fatalf("index status --documents failed: %v", err)
	}

	originalStatus := retrieval.StatusFunc
	retrieval.StatusFunc = func() (*retrieval.Status, error) {
		return &retrieval.Status{DocumentCount: 3}, nil
	}
	t.Cleanup(func() { retrieval.StatusFunc = originalStatus })

	statusCmd := newIndexStatusCmd()
	if err := statusCmd.RunE(statusCmd, nil); err != nil {
		t.Fatalf("index status failed: %v", err)
	}
}

func TestIndexStatusCommandPropagatesVaultAndStatusErrors(t *testing.T) {
	originalConfig := cfg
	t.Cleanup(func() { cfg = originalConfig })
	originalStatus := retrieval.StatusFunc
	t.Cleanup(func() { retrieval.StatusFunc = originalStatus })

	cfg = &config.Config{}
	documentsCmd := newIndexStatusCmd()
	if err := documentsCmd.Flags().Set("documents", "true"); err != nil {
		t.Fatal(err)
	}
	if err := documentsCmd.RunE(documentsCmd, nil); err == nil {
		t.Fatal("index status --documents accepted an unconfigured vault")
	}

	cfg = &config.Config{Vault: t.TempDir()}
	t.Setenv("SYMDESK_SIDECAR", t.TempDir())
	documentsCmd = newIndexStatusCmd()
	if err := documentsCmd.Flags().Set("documents", "true"); err != nil {
		t.Fatal(err)
	}
	if err := documentsCmd.RunE(documentsCmd, nil); err == nil {
		t.Fatal("index status --documents accepted a sidecar directory")
	}

	t.Setenv("SYMDESK_SIDECAR", "")
	documentsCmd = newIndexStatusCmd()
	if err := documentsCmd.Flags().Set("documents", "true"); err != nil {
		t.Fatal(err)
	}
	if err := documentsCmd.Flags().Set("state", "bogus"); err != nil {
		t.Fatal(err)
	}
	if err := documentsCmd.RunE(documentsCmd, nil); err == nil {
		t.Fatal("index status accepted an invalid state filter")
	}

	cfg = &config.Config{Vault: filepath.Join(t.TempDir(), "missing")}
	retrieval.StatusFunc = func() (*retrieval.Status, error) {
		return &retrieval.Status{DocumentCount: 3}, nil
	}
	statusCmd := newIndexStatusCmd()
	if err := statusCmd.RunE(statusCmd, nil); err == nil {
		t.Fatal("index status accepted a missing configured vault")
	}

	retrieval.StatusFunc = func() (*retrieval.Status, error) {
		return nil, errors.New("status unavailable")
	}
	statusCmd = newIndexStatusCmd()
	if err := statusCmd.RunE(statusCmd, nil); err == nil {
		t.Fatal("index status swallowed the retrieval status error")
	}
}
