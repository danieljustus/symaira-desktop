package service

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mockOllama(t *testing.T, response string, captureBody *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if captureBody != nil {
			b, _ := io.ReadAll(r.Body)
			*captureBody = string(b)
		}
		_, _ = fmt.Fprintf(w, `{"response":%q,"done":true}`+"\n", response)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNotebookGenerate_BuiltinKind_WritesContractConformArtifact(t *testing.T) {
	srv := mockOllama(t, "Generated briefing body.", nil)
	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)

	svc := newTestService(t)
	docPath, err := svc.NoteNew("Source Doc", "some content about the project", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Briefing Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	res, err := svc.NotebookGenerate(nb.ID, "briefing", false)
	if err != nil {
		t.Fatalf("NotebookGenerate: %v", err)
	}
	if res.DryRun {
		t.Error("DryRun = true, want false")
	}
	if !strings.Contains(res.Content, "Generated briefing body.") {
		t.Errorf("Content = %q, want to contain the mocked response", res.Content)
	}
	wantPath := filepath.Join("notebooks", nb.ID, "briefing.md")
	if res.Path != wantPath {
		t.Errorf("Path = %q, want %q", res.Path, wantPath)
	}

	data, err := os.ReadFile(filepath.Join(svc.VaultRoot, wantPath))
	if err != nil {
		t.Fatalf("artifact was not written: %v", err)
	}
	content := string(data)
	for _, want := range []string{"artifact_kind: briefing", "notebook_id: " + nb.ID, "Generated briefing body."} {
		if !strings.Contains(content, want) {
			t.Errorf("artifact content missing %q:\n%s", want, content)
		}
	}

	// The artifact must be indexed (searchable), same as any other write.
	results, err := svc.Search("Generated briefing")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Error("generated artifact is not searchable; sidecar out of sync")
	}
}

func TestNotebookGenerate_DryRunWritesNothing(t *testing.T) {
	srv := mockOllama(t, "Dry run content.", nil)
	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)

	svc := newTestService(t)
	docPath, err := svc.NoteNew("Doc", "content", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Dry Run Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	res, err := svc.NotebookGenerate(nb.ID, "faq", true)
	if err != nil {
		t.Fatalf("NotebookGenerate: %v", err)
	}
	if !res.DryRun {
		t.Error("DryRun = false, want true")
	}
	if !strings.Contains(res.Content, "Dry run content.") {
		t.Errorf("Content = %q, want the generated text even in dry-run", res.Content)
	}

	if _, err := os.Stat(filepath.Join(svc.VaultRoot, "notebooks", nb.ID, "faq.md")); !os.IsNotExist(err) {
		t.Errorf("expected no artifact file to exist after dry-run, stat err = %v", err)
	}
}

func TestNotebookGenerate_CustomTemplateOverridesBuiltin(t *testing.T) {
	var capturedBody string
	srv := mockOllama(t, "Custom kind output.", &capturedBody)
	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)

	svc := newTestService(t)
	templatePath := filepath.Join(svc.VaultRoot, "templates", "notebook-elevator-pitch.md")
	if err := os.MkdirAll(filepath.Dir(templatePath), 0750); err != nil {
		t.Fatal(err)
	}
	templateBody := "---\ntitle: Elevator Pitch Template\ncreated: \"2026-01-01\"\ntags: []\n---\nWrite a one-paragraph elevator pitch for the sources below.\n"
	if err := os.WriteFile(templatePath, []byte(templateBody), 0600); err != nil {
		t.Fatal(err)
	}

	docPath, err := svc.NoteNew("Doc", "content", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Custom Kind Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	res, err := svc.NotebookGenerate(nb.ID, "elevator-pitch", false)
	if err != nil {
		t.Fatalf("NotebookGenerate: %v", err)
	}
	if res.Kind != "elevator-pitch" {
		t.Errorf("Kind = %q, want elevator-pitch", res.Kind)
	}
	if !strings.Contains(capturedBody, "elevator pitch") {
		t.Errorf("prompt sent to the model did not include the custom template instruction: %s", capturedBody)
	}

	wantPath := filepath.Join(svc.VaultRoot, "notebooks", nb.ID, "elevator-pitch.md")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected artifact at %s: %v", wantPath, err)
	}
}

func TestNotebookGenerate_UnknownKindErrors(t *testing.T) {
	svc := newTestService(t)
	docPath, err := svc.NoteNew("Doc", "content", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Unknown Kind Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	_, err = svc.NotebookGenerate(nb.ID, "does-not-exist", false)
	if err == nil {
		t.Fatal("expected an error for an unknown artifact kind")
	}
	if !strings.Contains(err.Error(), "briefing") {
		t.Errorf("error should list known built-in kinds, got: %v", err)
	}
}

func TestNotebookGenerate_NoSourcesErrors(t *testing.T) {
	svc := newTestService(t)
	nb, err := svc.NotebookNew("Empty Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookGenerate(nb.ID, "briefing", false); err == nil {
		t.Fatal("expected an error generating from a notebook with no sources")
	}
}

func TestNotebookGenerate_WithoutConfiguredLLMDegradesHonestly(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	svc := newTestService(t)
	docPath, err := svc.NoteNew("Doc", "content", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("No LLM Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	_, err = svc.NotebookGenerate(nb.ID, "briefing", false)
	if err == nil {
		t.Fatal("expected an honest error when no LLM is configured")
	}
	if _, statErr := os.Stat(filepath.Join(svc.VaultRoot, "notebooks", nb.ID, "briefing.md")); !os.IsNotExist(statErr) {
		t.Error("expected no artifact file to exist when generation failed")
	}
}

func TestNotebookGenerate_RegenerateSnapshotsPreviousVersion(t *testing.T) {
	response := "First version content."
	srv := mockOllama(t, response, nil)
	t.Setenv("SYMDESK_OLLAMA_URL", srv.URL)

	svc := newTestService(t)
	docPath, err := svc.NoteNew("Doc", "content", "")
	if err != nil {
		t.Fatal(err)
	}
	nb, err := svc.NotebookNew("Regenerate Notebook", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nb.ID, docPath); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.NotebookGenerate(nb.ID, "briefing", false); err != nil {
		t.Fatalf("first generate: %v", err)
	}

	// Point at a second mock server returning different content, then
	// regenerate over the same artifact path.
	srv.Close()
	srv2 := mockOllama(t, "Second version content.", nil)
	t.Setenv("SYMDESK_OLLAMA_URL", srv2.URL)

	relPath := filepath.Join("notebooks", nb.ID, "briefing.md")
	if _, err := svc.NotebookGenerate(nb.ID, "briefing", false); err != nil {
		t.Fatalf("second generate: %v", err)
	}

	entries, err := svc.HistoryList(relPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 snapshot of the pre-regeneration content, got %d", len(entries))
	}

	if _, err := svc.HistoryRestore(relPath, ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(svc.VaultRoot, relPath))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "First version content.") {
		t.Errorf("restored artifact lost the first version's content:\n%s", data)
	}
}
