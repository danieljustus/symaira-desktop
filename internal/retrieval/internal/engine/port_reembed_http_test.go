package engine

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/db"
)

type reembedHTTPFixture struct {
	SchemaVersion int                      `json:"schema_version"`
	Cases         []reembedHTTPFixtureCase `json:"cases"`
}

type reembedHTTPFixtureCase struct {
	ID                  string                      `json:"id"`
	DocumentPath        string                      `json:"document_path,omitempty"`
	DocumentBody        string                      `json:"document_body"`
	ResponseStatus      int                         `json:"response_status"`
	EmbeddingDim        int                         `json:"embedding_dim"`
	ResponseDimension   int                         `json:"response_dimension"`
	TransientOnce       bool                        `json:"transient_once,omitempty"`
	Requests            []reembedHTTPFixtureRequest `json:"requests"`
	PendingChunks       []pendingRebuildChunk       `json:"pending_chunks"`
	ResolvedChunks      []pendingRebuildChunk       `json:"resolved_chunks"`
	DocumentHash        string                      `json:"document_hash"`
	ReembeddedDocuments int                         `json:"reembedded_documents"`
	RemainingPending    int                         `json:"remaining_pending"`
	Generation          int64                       `json:"generation"`
}

type reembedHTTPFixtureRequest struct {
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	ContentType string          `json:"content_type"`
	Accept      string          `json:"accept"`
	Body        json.RawMessage `json:"body"`
}

type reembedHTTPRequestBody struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions"`
}

// TestReembedHTTPPortFixture calls the production ReembedPending path with a
// real Ollama-compatible HTTP server. Only the server is fake; Go owns the
// parser, chunker, retry/fallback policy, and database replacement behavior.
func TestReembedHTTPPortFixture(t *testing.T) {
	cases := []reembedHTTPFixtureCase{
		{ID: "provider-resolves-pending-markdown", ResponseStatus: http.StatusOK, EmbeddingDim: 3},
		{ID: "provider-retries-transient-batch", ResponseStatus: http.StatusOK, EmbeddingDim: 3, TransientOnce: true},
		{ID: "provider-404-keeps-pending-markdown", ResponseStatus: http.StatusNotFound, EmbeddingDim: 3},
		{ID: "provider-auto-detects-response-dimension", ResponseStatus: http.StatusOK, ResponseDimension: 3},
		{ID: "provider-dimension-mismatch-keeps-pending-markdown", ResponseStatus: http.StatusOK, EmbeddingDim: 3, ResponseDimension: 2},
		{ID: "provider-resolves-pending-plain-text", DocumentPath: "doc.txt", ResponseStatus: http.StatusOK, EmbeddingDim: 3},
	}
	fixture := reembedHTTPFixture{SchemaVersion: 1}
	for index := range cases {
		testCase := cases[index]
		t.Run(testCase.ID, func(t *testing.T) {
			fixture.Cases = append(fixture.Cases, runReembedHTTPFixtureCase(t, testCase))
		})
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(file), "../../../../testdata/port/retrieval/reembed-http-cli.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("re-embed HTTP fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./internal/retrieval/internal/engine -run '^TestReembedHTTPPortFixture$'")
	}
}

func runReembedHTTPFixtureCase(t *testing.T, testCase reembedHTTPFixtureCase) reembedHTTPFixtureCase {
	t.Helper()
	root := t.TempDir()
	privateDirs := map[string]string{
		"HOME":            filepath.Join(root, "home"),
		"USERPROFILE":     filepath.Join(root, "userprofile"),
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"),
		"XDG_CACHE_HOME":  filepath.Join(root, "xdg-cache"),
		"XDG_DATA_HOME":   filepath.Join(root, "xdg-data"),
		"APPDATA":         filepath.Join(root, "appdata"),
		"LOCALAPPDATA":    filepath.Join(root, "localappdata"),
		"TMPDIR":          filepath.Join(root, "tmp"),
		"TMP":             filepath.Join(root, "tmp"),
		"TEMP":            filepath.Join(root, "tmp"),
	}
	for key, directory := range privateDirs {
		t.Setenv(key, directory)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create private %s: %v", key, err)
		}
	}
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })
	dbClient, err := db.OpenAt(filepath.Join(root, "retrieval.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbClient.Close() })
	documentBody := "# Re-embed oracle\n\n" + strings.Repeat("local embedding contract text ", 62)
	documentPath := testCase.DocumentPath
	if documentPath == "" {
		documentPath = "doc.md"
	}
	if documentPath == "doc.txt" {
		documentBody = "Re-embed oracle\n\n" + strings.Repeat("local embedding contract text ", 62)
	}
	testCase.DocumentPath = documentPath
	testCase.DocumentBody = documentBody
	oldBody := "# Old pending content\n\nold state"
	if documentPath == "doc.txt" {
		oldBody = "Old pending content\n\nold state"
	}
	if err := os.WriteFile(documentPath, []byte(oldBody), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := IndexStdin(dbClient, &fallbackEmbedder{dim: 3}, strings.NewReader(oldBody), documentPath); err != nil {
		t.Fatal(err)
	}
	pending, err := dbClient.GetChunksForDocument(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	testCase.PendingChunks = portPendingRebuildChunks(pending)
	if err := os.WriteFile(documentPath, []byte(documentBody), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := make([]reembedHTTPFixtureRequest, 0, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var raw bytes.Buffer
		if _, copyErr := raw.ReadFrom(request.Body); copyErr != nil {
			t.Errorf("read request body: %v", copyErr)
		}
		requests = append(requests, reembedHTTPFixtureRequest{
			Method: request.Method, Path: request.URL.RequestURI(),
			ContentType: request.Header.Get("Content-Type"), Accept: request.Header.Get("Accept"),
			Body: append(json.RawMessage(nil), raw.Bytes()...),
		})
		status := testCase.ResponseStatus
		if testCase.TransientOnce && len(requests) == 1 {
			status = http.StatusServiceUnavailable
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		if status != http.StatusOK {
			message := `{"error":"temporary local failure"}`
			if status == http.StatusNotFound {
				message = `{"error":"model not found"}`
			}
			_, _ = writer.Write([]byte(message))
			return
		}
		var requestBody reembedHTTPRequestBody
		if err := json.Unmarshal(raw.Bytes(), &requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		responseDimension := testCase.ResponseDimension
		if responseDimension == 0 {
			responseDimension = 3
		}
		vectors := make([][]float32, len(requestBody.Input))
		for index := range vectors {
			vectors[index] = make([]float32, responseDimension)
			for dimension := range vectors[index] {
				vectors[index][dimension] = []float32{0.25, -0.5, 1}[dimension%3]
			}
		}
		response, _ := json.Marshal(map[string]any{"data": func() []map[string][]float32 {
			items := make([]map[string][]float32, len(vectors))
			for index, vector := range vectors {
				items[index] = map[string][]float32{"embedding": vector}
			}
			return items
		}()})
		_, _ = writer.Write(response)
	}))
	defer server.Close()
	generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL: server.URL + "/api/embeddings?ignored=1", Model: "fixture-model", Dim: testCase.EmbeddingDim,
		Timeout: 2 * time.Second, RetryCount: 1, RetryBackoff: time.Millisecond,
	})
	reembedded, err := ReembedPending(dbClient, generator)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := dbClient.GetChunksForDocument(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	document, err := dbClient.GetDocument(documentPath)
	if err != nil || document == nil {
		t.Fatalf("read rebuilt document: document=%v error=%v", document, err)
	}
	connection, err := sqlitekit.Open(filepath.Join(root, "retrieval.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if err := connection.QueryRow("SELECT value FROM index_meta WHERE key='generation'").Scan(&testCase.Generation); err != nil {
		t.Fatal(err)
	}
	pendingCount, err := dbClient.CountPendingChunks()
	if err != nil {
		t.Fatal(err)
	}
	testCase.DocumentHash = document.Hash
	testCase.ReembeddedDocuments = reembedded
	testCase.RemainingPending = pendingCount
	testCase.ResolvedChunks = portPendingRebuildChunks(resolved)
	testCase.Requests = requests
	if testCase.ResponseStatus == http.StatusNotFound {
		// Go counts a committed fallback replacement as re-embedded even though
		// its chunks remain pending; Rust intentionally labels that result as
		// incomplete rather than repeating success-shaped output.
		if len(requests) != len(resolved)+1 || pendingCount == 0 || reembedded != 1 {
			t.Fatalf("Go failure control: requests=%d pending=%d reembedded=%d", len(requests), pendingCount, reembedded)
		}
	} else if testCase.ResponseStatus == http.StatusOK && testCase.ResponseDimension > 0 && testCase.EmbeddingDim > 0 && testCase.ResponseDimension != testCase.EmbeddingDim {
		if len(requests) != 1 || pendingCount == 0 || reembedded != 1 {
			t.Fatalf("Go dimension-mismatch control: requests=%d pending=%d reembedded=%d", len(requests), pendingCount, reembedded)
		}
	} else if len(requests) != 1+btoi(testCase.TransientOnce) || pendingCount != 0 || reembedded != 1 {
		t.Fatalf("Go success: requests=%d pending=%d reembedded=%d", len(requests), pendingCount, reembedded)
	}
	return testCase
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
