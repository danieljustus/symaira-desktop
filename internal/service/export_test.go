package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func installMockSymprint(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "symprint"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

func writeExportNote(t *testing.T, svc *Service, name, title, body string) string {
	t.Helper()
	path := filepath.Join(svc.VaultRoot, name)
	content := "---\ntitle: " + title + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExportNoteAndViewHTML(t *testing.T) {
	svc := newTestService(t)
	notePath := writeExportNote(t, svc, "invoice.md", "Invoice", "Body text")

	noteOutput := filepath.Join(t.TempDir(), "invoice.html")
	note, err := svc.Export("invoice.md", "", noteOutput, "html", "")
	if err != nil {
		t.Fatalf("note export failed: %v", err)
	}
	if note.Format != "html" || !note.Rendered || note.Path != noteOutput {
		t.Errorf("unexpected note result: %#v", note)
	}
	noteHTML, err := os.ReadFile(noteOutput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(noteHTML), "<h1>Invoice</h1>") || !strings.Contains(string(noteHTML), "Body text") {
		t.Errorf("note HTML did not contain rendered content: %s", noteHTML)
	}

	if err := svc.DB.IndexDocument(&vault.Document{
		Path: notePath, Title: "Invoice", Body: "Body text", SHA256: "invoice", Created: "2026-07-12T00:00:00Z",
		Frontmatter: map[string]interface{}{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ViewsMgr.Save(dbviews.View{ID: "invoices", Name: "Invoices", Type: "table", Columns: []string{"_title"}}); err != nil {
		t.Fatal(err)
	}

	viewOutput := filepath.Join(t.TempDir(), "invoices.html")
	view, err := svc.Export("", "invoices", viewOutput, "html", "")
	if err != nil {
		t.Fatalf("view export failed: %v", err)
	}
	if view.Format != "html" || !view.Rendered || view.Path != viewOutput {
		t.Errorf("unexpected view result: %#v", view)
	}
	viewHTML, err := os.ReadFile(viewOutput)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(viewHTML), "<h1>Invoices</h1>") || !strings.Contains(string(viewHTML), "Invoice") {
		t.Errorf("view HTML did not contain rendered view: %s", viewHTML)
	}
}

func TestExportPDFUsesSymprintWithProfile(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.md")
	installMockSymprint(t, `#!/bin/bash
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"tool":"symprint","version":"1.2.3","schema_version":1}'
  exit 0
fi
if [ "$1" = "render" ]; then
  printf '%s\n' "$@" > "$SYMDESK_TEST_ARGS"
  cat > "$SYMDESK_TEST_STDIN"
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "-o" ]; then
      shift
      printf 'mock pdf' > "$1"
      exit 0
    fi
    shift
  done
fi
echo "unexpected command: $*" >&2
exit 2
`)
	t.Setenv("SYMDESK_TEST_ARGS", argsPath)
	t.Setenv("SYMDESK_TEST_STDIN", stdinPath)

	svc := newTestService(t)
	writeExportNote(t, svc, "invoice.md", "Invoice", "Body text")
	output := filepath.Join(t.TempDir(), "invoice.pdf")
	result, err := svc.Export("invoice.md", "", output, "pdf", "behoerde")
	if err != nil {
		t.Fatalf("PDF export failed: %v", err)
	}
	if result.Format != "pdf" || result.Profile != "behoerde" || !strings.Contains(result.Message, "symprint 1.2.3") {
		t.Errorf("unexpected PDF result: %#v", result)
	}
	pdf, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(pdf) != "mock pdf" {
		t.Errorf("unexpected PDF content %q", pdf)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(args)) != strings.Join([]string{"render", "-", "-o", output, "-p", "behoerde"}, "\n") {
		t.Errorf("unexpected symprint arguments:\n%s", args)
	}
	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stdin), "# Invoice") || !strings.Contains(string(stdin), "Body text") {
		t.Errorf("unexpected symprint input: %q", stdin)
	}

	notePath := filepath.Join(svc.VaultRoot, "invoice.md")
	if err := svc.DB.IndexDocument(&vault.Document{
		Path: notePath, Title: "Invoice", Body: "Body text", SHA256: "invoice", Created: "2026-07-12T00:00:00Z",
		Frontmatter: map[string]interface{}{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.ViewsMgr.Save(dbviews.View{ID: "invoices", Name: "Invoices", Type: "table", Columns: []string{"_title"}}); err != nil {
		t.Fatal(err)
	}
	viewOutput := filepath.Join(t.TempDir(), "invoices.pdf")
	viewResult, err := svc.Export("", "invoices", viewOutput, "pdf", "report")
	if err != nil {
		t.Fatalf("view PDF export failed: %v", err)
	}
	if viewResult.Format != "pdf" || viewResult.Profile != "report" || !viewResult.Rendered {
		t.Errorf("unexpected view PDF result: %#v", viewResult)
	}
	viewPDF, err := os.ReadFile(viewOutput)
	if err != nil {
		t.Fatal(err)
	}
	if string(viewPDF) != "mock pdf" {
		t.Errorf("unexpected view PDF content %q", viewPDF)
	}
}

func TestExportPDFRequiresSymprint(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)

	svc := newTestService(t)
	writeExportNote(t, svc, "invoice.md", "Invoice", "Body text")
	if _, err := svc.Export("invoice.md", "", filepath.Join(t.TempDir(), "invoice.pdf"), "pdf", ""); err == nil || !strings.Contains(err.Error(), "PDF export requires symprint") {
		t.Fatalf("expected graceful missing symprint error, got %v", err)
	}
}
