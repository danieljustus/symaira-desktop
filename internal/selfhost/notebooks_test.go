package selfhost

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/permissions"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

// createNotebookForTest creates a notebook with sourcePath (a vault-relative
// path already written to disk) as its only source, using the same Server
// db newAITestServer wired up, and re-indexes so it's immediately visible.
func createNotebookForTest(t *testing.T, server *Server, vaultRoot, title, sourcePath string) string {
	t.Helper()
	svc := service.New(vaultRoot, server.db)
	nb, err := svc.NotebookNew(title, "")
	if err != nil {
		t.Fatal(err)
	}
	if sourcePath != "" {
		if _, err := svc.NotebookAddSource(nb.ID, sourcePath); err != nil {
			t.Fatal(err)
		}
	}
	return nb.ID
}

func getNotebooks(t *testing.T, url, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url+"/api/v1/notebooks", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func getNotebook(t *testing.T, url, id, token string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url+"/api/v1/notebooks/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestHandleListNotebooks_RequiresAuthentication(t *testing.T) {
	httpServer, _, _ := newAITestServer(t)
	response := getNotebooks(t, httpServer.URL, "")
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", response.StatusCode, readBody(response))
	}
}

func TestHandleListNotebooks_ReturnsCreatedNotebooks(t *testing.T) {
	httpServer, server, vaultRoot := newAITestServer(t)
	createNotebookForTest(t, server, vaultRoot, "First", "")
	createNotebookForTest(t, server, vaultRoot, "Second", "")

	response := getNotebooks(t, httpServer.URL, testToken)
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}
	var list []map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}
}

func TestHandleGetNotebook_RequiresAuthentication(t *testing.T) {
	httpServer, server, vaultRoot := newAITestServer(t)
	nbID := createNotebookForTest(t, server, vaultRoot, "Auth Test", "")

	response := getNotebook(t, httpServer.URL, nbID, "")
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", response.StatusCode, readBody(response))
	}
}

func TestHandleGetNotebook_ReturnsResolvedSources(t *testing.T) {
	httpServer, server, vaultRoot := newAITestServer(t)
	nbID := createNotebookForTest(t, server, vaultRoot, "Sourced", "Notiz.md")

	response := getNotebook(t, httpServer.URL, nbID, testToken)
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}
	var result notebookResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.ID != nbID {
		t.Errorf("ID = %q, want %q", result.ID, nbID)
	}
	if len(result.Sources) != 1 || result.Sources[0].Path != "Notiz.md" {
		t.Errorf("Sources = %+v, want exactly [Notiz.md]", result.Sources)
	}
}

func TestHandleGetNotebook_UnknownIDReturns404(t *testing.T) {
	httpServer, _, _ := newAITestServer(t)
	response := getNotebook(t, httpServer.URL, "does-not-exist", testToken)
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", response.StatusCode, readBody(response))
	}
}

// TestHandleGetNotebook_OmitsUnreadableSources proves a source the caller
// cannot read is never included in the response — the same "never
// received" contract handleAIAsk already applies to retrieval.
func TestHandleGetNotebook_OmitsUnreadableSources(t *testing.T) {
	httpServer, server, vaultRoot := newAITestServer(t)
	nbID := createNotebookForTest(t, server, vaultRoot, "Mixed Access", "")

	svc := service.New(vaultRoot, server.db)
	if _, err := svc.NotebookAddSource(nbID, "Notiz.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.NotebookAddSource(nbID, "Rechnung.md"); err != nil {
		t.Fatal(err)
	}

	userToken, err := server.perm.UserAdd("restricted-nb", "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := server.perm.SetDocumentRule(permissions.DocumentRule{
		Path:  "Rechnung.md",
		Owner: "admin",
	}); err != nil {
		t.Fatal(err)
	}

	response := getNotebook(t, httpServer.URL, nbID, userToken)
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}
	var result notebookResponse
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	for _, src := range result.Sources {
		if strings.Contains(src.Path, "Rechnung") {
			t.Fatalf("restricted user received an unreadable source: %+v", src)
		}
	}
	if len(result.Sources) != 1 || result.Sources[0].Path != "Notiz.md" {
		t.Errorf("Sources = %+v, want exactly [Notiz.md]", result.Sources)
	}
}

func TestAIAsk_NotebookFieldRestrictsCitations(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	httpServer, server, vaultRoot := newAITestServer(t)
	nbID := createNotebookForTest(t, server, vaultRoot, "Ask Scope", "Notiz.md")

	body, _ := json.Marshal(map[string]string{"query": "Einkaufsliste", "notebook": nbID})
	request, err := http.NewRequest(http.MethodPost, httpServer.URL+"/api/v1/ai/ask", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = response.Body.Close() })
	if response.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", response.StatusCode, readBody(response))
	}

	events := readAIEvents(t, response.Body)
	for _, event := range events {
		if event.Type == ai.AIEventCitation && event.Path != "Notiz.md" {
			t.Fatalf("scoped ask cited a path outside the notebook: %+v", event)
		}
	}
}
