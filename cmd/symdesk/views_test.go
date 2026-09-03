package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func TestViewsCLI_ListSaveGetDeleteSiblingsExec(t *testing.T) {
	vaultDir := setupTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	// Init DB and create test files
	vRoot, db, err := initServiceDeps()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	}()
	svc := service.New(vRoot, db)

	invoicesDir := filepath.Join(svc.VaultRoot, "invoices")
	if err := os.MkdirAll(invoicesDir, 0750); err != nil {
		t.Fatal(err)
	}

	doc1 := &vault.Document{
		Path:        filepath.Join(invoicesDir, "inv1.md"),
		Title:       "Invoice 1",
		Body:        "body 1",
		SHA256:      "h1",
		Created:     "2026-01-01T00:00:00Z",
		Frontmatter: map[string]interface{}{"status": "open", "tags": []string{"billing"}},
	}
	doc2 := &vault.Document{
		Path:        filepath.Join(svc.VaultRoot, "other.md"),
		Title:       "Other Note",
		Body:        "body 2",
		SHA256:      "h2",
		Created:     "2026-01-01T00:00:00Z",
		Frontmatter: map[string]interface{}{"status": "open", "tags": []string{"general"}},
	}
	if err := svc.DB.IndexDocument(doc1); err != nil {
		t.Fatal(err)
	}
	if err := svc.DB.IndexDocument(doc2); err != nil {
		t.Fatal(err)
	}

	viewsCmd := newViewsCmd()
	saveCmd := findSubcommand(t, viewsCmd, "save")
	listCmd := findSubcommand(t, viewsCmd, "list")
	getCmd := findSubcommand(t, viewsCmd, "get")
	execCmd := findSubcommand(t, viewsCmd, "exec")
	siblingsCmd := findSubcommand(t, viewsCmd, "siblings")
	deleteCmd := findSubcommand(t, viewsCmd, "delete")

	// 1. Save a view with source "invoices/"
	viewJSON := `{"id":"v_inv","name":"Invoices View","type":"table","source":"invoices/"}`
	out, err := runCommand(t, saveCmd, []string{viewJSON})
	if err != nil {
		t.Fatalf("views save failed: %v (out=%s)", err, out)
	}

	// 2. Save a sibling view
	siblingJSON := `{"id":"v_inv_board","name":"Invoices Board","type":"board","source":"invoices/"}`
	if _, err := runCommand(t, saveCmd, []string{siblingJSON}); err != nil {
		t.Fatal(err)
	}

	// 3. List views
	out, err = runCommand(t, listCmd, []string{})
	if err != nil {
		t.Fatalf("views list failed: %v (out=%s)", err, out)
	}
	var viewsList []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &viewsList); err != nil {
		t.Fatalf("unmarshal views list: %v (out=%s)", err, out)
	}
	if len(viewsList) != 2 {
		t.Fatalf("expected 2 views, got %d", len(viewsList))
	}

	// 4. Get view
	out, err = runCommand(t, getCmd, []string{"v_inv"})
	if err != nil {
		t.Fatalf("views get failed: %v", err)
	}
	var getResult map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &getResult); err != nil {
		t.Fatalf("unmarshal views get: %v", err)
	}
	if getResult["name"] != "Invoices View" {
		t.Errorf("expected Invoices View, got %v", getResult["name"])
	}

	// 5. Siblings
	out, err = runCommand(t, siblingsCmd, []string{"v_inv"})
	if err != nil {
		t.Fatalf("views siblings failed: %v", err)
	}
	var sibs []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &sibs); err != nil {
		t.Fatalf("unmarshal views siblings: %v", err)
	}
	if len(sibs) != 2 {
		t.Fatalf("expected 2 siblings, got %d", len(sibs))
	}

	// 6. Exec view -> should only return Invoice 1 from invoices/, not Other Note
	out, err = runCommand(t, execCmd, []string{"v_inv"})
	if err != nil {
		t.Fatalf("views exec failed: %v (out=%s)", err, out)
	}
	var execRows []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &execRows); err != nil {
		t.Fatalf("unmarshal views exec: %v (out=%s)", err, out)
	}
	if len(execRows) != 1 {
		t.Fatalf("expected 1 result from invoices/, got %d", len(execRows))
	}
	if execRows[0]["_title"] != "Invoice 1" {
		t.Errorf("expected Invoice 1, got %v", execRows[0]["_title"])
	}

	// 7. Delete view
	_, err = runCommand(t, deleteCmd, []string{"v_inv"})
	if err != nil {
		t.Fatalf("views delete failed: %v", err)
	}
}

func TestViewsCLI_ExecUnresolvableSourceError(t *testing.T) {
	vaultDir := setupTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	viewsCmd := newViewsCmd()
	saveCmd := findSubcommand(t, viewsCmd, "save")
	execCmd := findSubcommand(t, viewsCmd, "exec")

	// Save view with nonexistent folder
	viewJSON := `{"id":"v_bad","name":"Bad Scope","type":"table","source":"nonexistent-dir/"}`
	if _, err := runCommand(t, saveCmd, []string{viewJSON}); err != nil {
		t.Fatal(err)
	}

	_, err := runCommand(t, execCmd, []string{"v_bad"})
	if err == nil {
		t.Fatal("expected error executing view with unresolvable source")
	}
}

func TestViewsCLI_ExportAndImportCSVAndEmbed(t *testing.T) {
	vaultDir := setupTestVault(t)
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	vRoot, db, err := initServiceDeps()
	if err != nil {
		t.Fatal(err)
	}
	defer closeWithWarning("sidecar database", db.Close)
	svc := service.New(vRoot, db)

	// Create a test note
	doc := &vault.Document{
		Path:        filepath.Join(svc.VaultRoot, "item.md"),
		Title:       "Test Item",
		Frontmatter: map[string]interface{}{"status": "open", "amount": "99.99"},
	}
	if err := svc.DB.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}

	viewsCmd := newViewsCmd()
	saveCmd := findSubcommand(t, viewsCmd, "save")
	exportCSVCmd := findSubcommand(t, viewsCmd, "export-csv")
	importCSVCmd := findSubcommand(t, viewsCmd, "import-csv")
	embedCmd := findSubcommand(t, viewsCmd, "embed")

	// Save view
	vJSON := `{"id":"v_test_export","name":"Export View","type":"table","columns":["title","status","amount"]}`
	if _, err := runCommand(t, saveCmd, []string{vJSON}); err != nil {
		t.Fatal(err)
	}

	// 1. Test export-csv
	out, err := runCommand(t, exportCSVCmd, []string{"v_test_export"})
	if err != nil {
		t.Fatalf("export-csv failed: %v", err)
	}
	if !strings.Contains(out, "title,status,amount") || !strings.Contains(out, "Test Item,open,99.99") {
		t.Errorf("unexpected export-csv output:\n%s", out)
	}

	// 2. Test import-csv
	csvPath := filepath.Join(t.TempDir(), "import.csv")
	csvContent := `Title,Status,Amount
Item Alpha,open,10.00
Item Beta,paid,20.00
`
	if err := os.WriteFile(csvPath, []byte(csvContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Dry run
	out, err = runCommand(t, importCSVCmd, []string{csvPath})
	if err != nil {
		t.Fatalf("import-csv dry-run failed: %v", err)
	}
	if !strings.Contains(out, "dry_run\":true") || !strings.Contains(out, "imported_count\":2") {
		t.Errorf("unexpected import-csv dry run output:\n%s", out)
	}

	// Apply
	_ = importCSVCmd.Flags().Set("apply", "true")
	_ = importCSVCmd.Flags().Set("folder", "imported")
	_ = importCSVCmd.Flags().Set("base", "Imported Base")
	out, err = runCommand(t, importCSVCmd, []string{csvPath})
	if err != nil {
		t.Fatalf("import-csv apply failed: %v", err)
	}
	if !strings.Contains(out, "dry_run\":false") || !strings.Contains(out, "imported_count\":2") {
		t.Errorf("unexpected import-csv apply output:\n%s", out)
	}

	// Verify imported file on disk
	importedNote := filepath.Join(svc.VaultRoot, "imported", "item-alpha.md")
	if _, err := os.Stat(importedNote); err != nil {
		t.Fatalf("expected imported note at %s, err: %v", importedNote, err)
	}

	// 3. Test embed
	embedSpec := `
base: imported-base
limit: 5
`
	out, err = runCommand(t, embedCmd, []string{embedSpec})
	if err != nil {
		t.Fatalf("embed command failed: %v", err)
	}
	if !strings.Contains(out, "base_id\":\"imported-base\"") || !strings.Contains(out, "markdown\"") {
		t.Errorf("unexpected embed output:\n%s", out)
	}
}
