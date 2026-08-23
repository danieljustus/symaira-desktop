package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/pdf"
	"github.com/danieljustus/symaira-desktop/internal/vault"
	printapi "github.com/danieljustus/symaira-print/api"
)

// renderCall records one in-process render so a test can assert on what the
// export path handed the renderer.
type renderCall struct {
	Source     []byte
	OutputPath string
	Options    printapi.Options
}

// withStubRenderer points the in-process PDF seam at a renderer that reports
// an engine and writes a placeholder file, and returns the recorded calls.
// It replaces the mock symprint binary the export path used to execute.
func withStubRenderer(t *testing.T) *[]renderCall {
	t.Helper()
	calls := &[]renderCall{}
	prevRender, prevEngine := pdf.RenderFunc, pdf.EngineAvailableFunc

	pdf.EngineAvailableFunc = func(context.Context) (bool, string) { return true, "1.2.3" }
	pdf.RenderFunc = func(_ context.Context, src []byte, out string, opts printapi.Options) (*printapi.Result, error) {
		*calls = append(*calls, renderCall{Source: src, OutputPath: out, Options: opts})
		if err := os.WriteFile(out, []byte("mock pdf"), 0600); err != nil {
			return nil, err
		}
		return &printapi.Result{
			OutputPath:    out,
			Profile:       opts.Profile,
			EngineVersion: "1.2.3",
			Bytes:         int64(len("mock pdf")),
		}, nil
	}
	t.Cleanup(func() { pdf.RenderFunc, pdf.EngineAvailableFunc = prevRender, prevEngine })
	return calls
}

// withoutRenderEngine simulates a machine without a typesetting engine.
func withoutRenderEngine(t *testing.T) {
	t.Helper()
	prev := pdf.EngineAvailableFunc
	pdf.EngineAvailableFunc = func(context.Context) (bool, string) {
		return false, "typst not found on PATH"
	}
	t.Cleanup(func() { pdf.EngineAvailableFunc = prev })
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

func TestExportPDFUsesTheProfileAndRenderedMarkdown(t *testing.T) {
	calls := withStubRenderer(t)

	svc := newTestService(t)
	writeExportNote(t, svc, "invoice.md", "Invoice", "Body text")
	output := filepath.Join(t.TempDir(), "invoice.pdf")
	result, err := svc.Export("invoice.md", "", output, "pdf", "behoerde")
	if err != nil {
		t.Fatalf("PDF export failed: %v", err)
	}
	if result.Format != "pdf" || result.Profile != "behoerde" || !strings.Contains(result.Message, "typst 1.2.3") {
		t.Errorf("unexpected PDF result: %#v", result)
	}
	rendered, err := os.ReadFile(output) //nolint:gosec // test reads the file it just asked the stub renderer to write
	if err != nil {
		t.Fatal(err)
	}
	if string(rendered) != "mock pdf" {
		t.Errorf("unexpected PDF content %q", rendered)
	}
	if len(*calls) != 1 {
		t.Fatalf("expected exactly one render call, got %d", len(*calls))
	}
	call := (*calls)[0]
	if call.OutputPath != output || call.Options.Profile != "behoerde" {
		t.Errorf("unexpected render call: %#v", call)
	}
	if !strings.Contains(string(call.Source), "# Invoice") || !strings.Contains(string(call.Source), "Body text") {
		t.Errorf("unexpected render input: %q", call.Source)
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

func TestExportPDFRequiresATypesettingEngine(t *testing.T) {
	withoutRenderEngine(t)

	svc := newTestService(t)
	writeExportNote(t, svc, "invoice.md", "Invoice", "Body text")
	if _, err := svc.Export("invoice.md", "", filepath.Join(t.TempDir(), "invoice.pdf"), "pdf", ""); err == nil || !strings.Contains(err.Error(), "PDF export requires a typesetting engine") {
		t.Fatalf("expected graceful missing engine error, got %v", err)
	}
}
