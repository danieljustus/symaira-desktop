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
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
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
	if call.OutputPath != output || call.Options.Profile != "behoerde" || call.Options.SourceDir != svc.VaultRoot {
		t.Errorf("unexpected render call: %#v", call)
	}
	// The renderer reads the title from frontmatter, not from a body
	// heading — see internal/export.Note's PDF branch.
	if !strings.Contains(string(call.Source), "title: Invoice") || !strings.Contains(string(call.Source), "Body text") {
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
	if len(*calls) != 2 {
		t.Fatalf("expected two render calls, got %d", len(*calls))
	}
	viewCall := (*calls)[1]
	if viewCall.OutputPath != viewOutput || viewCall.Options.Profile != "report" || viewCall.Options.SourceDir != svc.VaultRoot {
		t.Errorf("unexpected view render call: %#v", viewCall)
	}
}

func TestExportPDFNestedNoteSourceDir(t *testing.T) {
	calls := withStubRenderer(t)

	svc := newTestService(t)
	nestedDir := filepath.Join(svc.VaultRoot, "sub", "folder")
	if err := os.MkdirAll(nestedDir, 0750); err != nil {
		t.Fatal(err)
	}
	notePath := filepath.Join("sub", "folder", "nested.md")
	writeExportNote(t, svc, notePath, "Nested Note", "Nested body")

	output := filepath.Join(t.TempDir(), "nested.pdf")
	result, err := svc.Export(notePath, "", output, "pdf", "report")
	if err != nil {
		t.Fatalf("PDF export failed: %v", err)
	}
	if !result.Rendered {
		t.Errorf("expected rendered result")
	}
	if len(*calls) != 1 {
		t.Fatalf("expected one render call, got %d", len(*calls))
	}
	call := (*calls)[0]
	if call.Options.SourceDir != nestedDir {
		t.Errorf("got SourceDir %q, want %q", call.Options.SourceDir, nestedDir)
	}
}

func TestExportPDFWithLocalImage(t *testing.T) {
	prevRender, prevEngine := pdf.RenderFunc, pdf.EngineAvailableFunc
	pdf.RenderFunc = printapi.Render
	pdf.EngineAvailableFunc = printapi.EngineAvailable
	t.Cleanup(func() {
		pdf.RenderFunc, pdf.EngineAvailableFunc = prevRender, prevEngine
	})

	if ok, _ := pdf.EngineAvailable(); !ok {
		t.Skip("skipping integration test: typst engine unavailable")
	}

	svc := newTestService(t)
	imgDir := filepath.Join(svc.VaultRoot, "assets")
	if err := os.MkdirAll(imgDir, 0750); err != nil {
		t.Fatal(err)
	}
	// 1x1 transparent PNG
	pngData := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d,
		0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89, 0x00, 0x00, 0x00,
		0x0a, 0x49, 0x44, 0x41, 0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00, 0x00, 0x00, 0x00, 0x49,
		0x45, 0x4e, 0x44, 0xae, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(filepath.Join(imgDir, "foo.png"), pngData, 0600); err != nil {
		t.Fatal(err)
	}

	writeExportNote(t, svc, "with_image.md", "Image Note", "Here is an image:\n\n![alt](assets/foo.png)")
	output := filepath.Join(t.TempDir(), "image_note.pdf")

	result, err := svc.Export("with_image.md", "", output, "pdf", "report")
	if err != nil {
		t.Fatalf("PDF export failed for note with local image: %v", err)
	}
	if !result.Rendered || result.Path != output {
		t.Errorf("unexpected result: %#v", result)
	}
	fi, err := os.Stat(output)
	if err != nil {
		t.Fatalf("output file stat failed: %v", err)
	}
	if fi.Size() == 0 {
		t.Errorf("output PDF is empty")
	}
}

func TestExportPDFRejectsPathTraversalImage(t *testing.T) {
	prevRender, prevEngine := pdf.RenderFunc, pdf.EngineAvailableFunc
	pdf.RenderFunc = printapi.Render
	pdf.EngineAvailableFunc = printapi.EngineAvailable
	t.Cleanup(func() {
		pdf.RenderFunc, pdf.EngineAvailableFunc = prevRender, prevEngine
	})

	if ok, _ := pdf.EngineAvailable(); !ok {
		t.Skip("skipping integration test: typst engine unavailable")
	}

	svc := newTestService(t)
	writeExportNote(t, svc, "bad_image.md", "Bad Image Note", "![bad](../outside.png)")
	output := filepath.Join(t.TempDir(), "bad_image.pdf")

	_, err := svc.Export("bad_image.md", "", output, "pdf", "report")
	if err == nil {
		t.Fatal("expected error on path traversal image, got nil")
	}
	if !strings.Contains(err.Error(), "traversal") && !strings.Contains(err.Error(), "escapes") {
		t.Errorf("expected traversal error message, got: %v", err)
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

// ExportProfiles is what the app's profile picker is built from; an empty or
// malformed list silently degrades the picker to "default only".
func TestExportProfilesReportsTheRendererProfiles(t *testing.T) {
	profiles := ExportProfiles()
	if len(profiles) == 0 {
		t.Fatal("ExportProfiles returned no profiles; the app picker would be empty")
	}
	seen := map[string]bool{}
	for _, p := range profiles {
		if p.Name == "" || p.Title == "" {
			t.Errorf("profile %+v is missing a name or title", p)
		}
		if seen[p.Name] {
			t.Errorf("duplicate profile name %q", p.Name)
		}
		seen[p.Name] = true
	}
	if !seen["report"] {
		t.Errorf("expected the built-in report profile, got %v", seen)
	}
}
