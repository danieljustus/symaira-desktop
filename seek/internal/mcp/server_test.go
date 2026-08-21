package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/engine"
)

type fakeStore struct {
	searchFunc            func(query string, limit int) ([]*db.SearchResult, error)
	getDocFunc            func(path string) (*db.Document, error)
	listDocsFunc          func() ([]*db.Document, error)
	searchExtractionsFunc func(query string, limit int) ([]*db.Extraction, error)
	listExtractionsFunc   func(class string, limit int) ([]*db.Extraction, error)
	getDocExtractionsFunc func(docPath string) ([]*db.Extraction, error)

	folderContexts     map[string]string
	mu                 sync.Mutex
	capturedPathPrefix string
}

func (f *fakeStore) Close() error                                             { return nil }
func (f *fakeStore) SaveDocument(doc *db.Document) error                      { return nil }
func (f *fakeStore) DeleteDocument(path string) error                         { return nil }
func (f *fakeStore) SaveChunks(chunks []*db.Chunk) error                      { return nil }
func (f *fakeStore) GetChunksForDocument(docPath string) ([]*db.Chunk, error) { return nil, nil }
func (f *fakeStore) GetStats() (*db.Stats, error)                             { return &db.Stats{}, nil }

func (f *fakeStore) GetDocument(path string) (*db.Document, error) {
	if f.getDocFunc != nil {
		return f.getDocFunc(path)
	}
	return nil, nil
}

func (f *fakeStore) ListDocuments() ([]*db.Document, error) {
	if f.listDocsFunc != nil {
		return f.listDocsFunc()
	}
	return []*db.Document{}, nil
}

func (f *fakeStore) SearchBM25(query string, limit int) ([]*db.SearchResult, error) {
	if f.searchFunc != nil {
		return f.searchFunc(query, limit)
	}
	return []*db.SearchResult{}, nil
}

func (f *fakeStore) SearchBM25WithPath(query string, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	f.mu.Lock()
	f.capturedPathPrefix = pathPrefix
	f.mu.Unlock()
	return f.SearchBM25(query, limit)
}

func (f *fakeStore) SearchVector(queryVec []float32, limit int) ([]*db.SearchResult, error) {
	return []*db.SearchResult{}, nil
}

func (f *fakeStore) SearchVectorWithPath(queryVec []float32, pathPrefix string, limit int) ([]*db.SearchResult, error) {
	f.mu.Lock()
	f.capturedPathPrefix = pathPrefix
	f.mu.Unlock()
	return f.SearchVector(queryVec, limit)
}

func (f *fakeStore) DetectMixedEmbeddingSpaces() (map[string]int, error) {
	return nil, nil
}

func (f *fakeStore) SetFolderContext(path, text string) error {
	if f.folderContexts == nil {
		f.folderContexts = make(map[string]string)
	}
	f.folderContexts[path] = text
	return nil
}

func (f *fakeStore) GetFolderContexts() ([]db.FolderContext, error) {
	var contexts []db.FolderContext
	for p, t := range f.folderContexts {
		contexts = append(contexts, db.FolderContext{PathPrefix: p, ContextText: t})
	}
	return contexts, nil
}

func (f *fakeStore) SaveExtractions(extractions []*db.Extraction) error { return nil }
func (f *fakeStore) DeleteExtractionsForDocument(docPath string) error  { return nil }

func (f *fakeStore) GetDocumentExtractions(docPath string) ([]*db.Extraction, error) {
	if f.getDocExtractionsFunc != nil {
		return f.getDocExtractionsFunc(docPath)
	}
	return nil, nil
}

func (f *fakeStore) ListExtractions(class string, limit int) ([]*db.Extraction, error) {
	if f.listExtractionsFunc != nil {
		return f.listExtractionsFunc(class, limit)
	}
	return nil, nil
}

func (f *fakeStore) SearchExtractions(queryStr string, limit int) ([]*db.Extraction, error) {
	if f.searchExtractionsFunc != nil {
		return f.searchExtractionsFunc(queryStr, limit)
	}
	return nil, nil
}

func (f *fakeStore) GetMatchingContext(path string) (*db.FolderContext, error) {
	if f.folderContexts == nil {
		return nil, nil
	}
	var best *db.FolderContext
	bestLen := 0
	for prefix, text := range f.folderContexts {
		if strings.HasPrefix(path, prefix) && len(prefix) > bestLen {
			best = &db.FolderContext{PathPrefix: prefix, ContextText: text}
			bestLen = len(prefix)
		}
	}
	return best, nil
}

func (f *fakeStore) Upsert(_ context.Context, _ []*db.Chunk) error { return nil }
func (f *fakeStore) Delete(_ context.Context, _ string) error      { return nil }
func (f *fakeStore) Search(_ context.Context, _ []float32, _ int) ([]*db.SearchResult, error) {
	return []*db.SearchResult{}, nil
}
func (f *fakeStore) SearchWithPath(_ context.Context, _ []float32, pathPrefix string, _ int) ([]*db.SearchResult, error) {
	f.mu.Lock()
	f.capturedPathPrefix = pathPrefix
	f.mu.Unlock()
	return []*db.SearchResult{}, nil
}

type fakeEmbedder struct{}

func (f *fakeEmbedder) GenerateVector(text string) []float32 {
	return make([]float32, 768)
}

func (f *fakeEmbedder) GenerateVectors(texts []string) [][]float32 {
	result := make([][]float32, len(texts))
	for i := range texts {
		result[i] = make([]float32, 768)
	}
	return result
}

func (f *fakeEmbedder) GenerateVectorsWithModel(texts []string) []engine.EmbeddingResult {
	result := make([]engine.EmbeddingResult, len(texts))
	for i := range texts {
		result[i] = engine.EmbeddingResult{Vector: make([]float32, 768), Model: f.ModelName()}
	}
	return result
}

func (f *fakeEmbedder) GenerateVectorNoRetry(text string) []float32 {
	return f.GenerateVector(text)
}

func (f *fakeEmbedder) GenerateVectorNoRetryWithModel(text string) engine.EmbeddingResult {
	return engine.EmbeddingResult{Vector: f.GenerateVector(text), Model: f.ModelName()}
}

func (f *fakeEmbedder) Dim() int {
	return 768
}

func (f *fakeEmbedder) ModelName() string {
	return "fake-model"
}

func newTestServer(store db.Store, vectorStore db.VectorStore, embedder engine.Embedder) *mcpserver.Server {
	ServerVersion = "test-version"
	s := mcpserver.New("symseek", ServerVersion)
	registerSearchDocuments(s, store, vectorStore, embedder, engine.SearchOptions{})
	registerReadDocument(s, store, embedder)
	registerListDocuments(s, store, embedder)
	registerGetContext(s, store, vectorStore, embedder, engine.SearchOptions{})
	registerIndexDocument(s, store, embedder)
	registerIndexURL(s, store, embedder)
	registerMultiGet(s, store, embedder)
	registerSetContext(s, store)
	registerGetContexts(s, store)
	registerSearchExtractions(s, store)
	registerListExtractions(s, store)
	registerGetDocumentExtractions(s, store)
	return s
}

type jsonRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func pipeRequest(t *testing.T, server *mcpserver.Server, req jsonRPCRequest) jsonRPCResponse {
	t.Helper()

	reqData, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal request: %v", err)
	}

	var input strings.Builder
	fmt.Fprintf(&input, "Content-Length: %d\r\n\r\n%s", len(reqData), reqData)

	var stdout bytes.Buffer
	err = server.ServeIO(context.Background(), strings.NewReader(input.String()), &stdout)
	if err != nil && err != io.EOF {
		t.Logf("ServeIO returned: %v", err)
	}

	br := bufio.NewReader(&stdout)
	var contentLength int
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("failed to read response header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if rest, ok := strings.CutPrefix(line, "Content-Length:"); ok {
			fmt.Sscanf(strings.TrimSpace(rest), "%d", &contentLength)
		}
	}

	if contentLength <= 0 {
		t.Fatalf("invalid content length: %d", contentLength)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(br, body); err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	var resp jsonRPCResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v\nbody: %s", err, body)
	}
	return resp
}

func TestServerInitialize(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "initialize",
	})

	if resp.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want %q", resp.JSONRPC, "2.0")
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("result is not a map: %T", resp.Result)
	}
	if result["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %q, want %q", result["protocolVersion"], "2024-11-05")
	}
	si := result["serverInfo"].(map[string]interface{})
	if si["name"] != "symseek" {
		t.Errorf("serverInfo.name = %q, want %q", si["name"], "symseek")
	}
	if si["version"] != "test-version" {
		t.Errorf("serverInfo.version = %q, want %q", si["version"], "test-version")
	}
}

func TestServerToolsList(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/list",
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	result := resp.Result.(map[string]interface{})
	tools := result["tools"].([]interface{})

	names := make(map[string]bool)
	for _, tool := range tools {
		toolMap := tool.(map[string]interface{})
		names[toolMap["name"].(string)] = true
	}
	expected := []string{"search_documents", "read_document", "list_documents", "get_context", "index_document", "index_url", "multi_get", "set_context", "get_contexts"}
	for _, n := range expected {
		if !names[n] {
			t.Errorf("missing tool: %s", n)
		}
	}
}

func TestServerMethodNotFound(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "nonexistent",
	})

	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	errMap := resp.Error.(map[string]interface{})
	if int(errMap["code"].(float64)) != -32601 {
		t.Errorf("error code = %v, want -32601", errMap["code"])
	}
}

func TestServerUnknownTool(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "unknown_tool",
		"arguments": map[string]interface{}{},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error == nil {
		t.Fatal("expected error for unknown tool")
	}
	errMap := resp.Error.(map[string]interface{})
	if int(errMap["code"].(float64)) != -32601 {
		t.Errorf("error code = %v, want -32601", errMap["code"])
	}
}

func TestServerInvalidParams(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name": 123}`),
	})

	if resp.Error == nil {
		t.Fatal("expected error for invalid params")
	}
	errMap := resp.Error.(map[string]interface{})
	if int(errMap["code"].(float64)) != -32602 {
		t.Errorf("error code = %v, want -32602", errMap["code"])
	}
}

func TestServerNotificationsInitialized(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	var stdout bytes.Buffer
	reqData, _ := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	})
	input := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(reqData), reqData)

	err := server.ServeIO(context.Background(), strings.NewReader(input), &stdout)
	if err != nil && err != io.EOF {
		t.Logf("ServeIO returned: %v", err)
	}

	if stdout.Len() != 0 {
		t.Errorf("expected no output for notification, got: %s", stdout.String())
	}
}

func TestServerSearchDocuments(t *testing.T) {
	store := &fakeStore{
		searchFunc: func(query string, limit int) ([]*db.SearchResult, error) {
			return []*db.SearchResult{}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "search_documents",
		"arguments": map[string]interface{}{
			"query": "test query",
			"limit": float64(5),
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestServerSearchDocumentsMissingQuery(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "search_documents",
		"arguments": map[string]interface{}{},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] != true {
		t.Fatal("expected isError=true for missing query")
	}
}

// TestServerSearchDocumentsWithPathPrefix is a regression test for issue #254.
// The path_prefix argument must be forwarded to the search backend.
func TestServerSearchDocumentsWithPathPrefix(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "search_documents",
		"arguments": map[string]interface{}{
			"query":       "test query",
			"limit":       float64(5),
			"path_prefix": "/home/user/docs/project-a/",
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	if store.capturedPathPrefix != "/home/user/docs/project-a/" {
		t.Errorf("expected path prefix %q, got %q", "/home/user/docs/project-a/", store.capturedPathPrefix)
	}
}

func TestServerListDocuments(t *testing.T) {
	store := &fakeStore{
		listDocsFunc: func() ([]*db.Document, error) {
			return []*db.Document{
				{Path: "/home/user/doc1.md"},
				{Path: "/home/user/doc2.md"},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "list_documents",
		"arguments": map[string]interface{}{},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestServerListDocumentsWithFilter(t *testing.T) {
	store := &fakeStore{
		listDocsFunc: func() ([]*db.Document, error) {
			return []*db.Document{
				{Path: "/home/user/projects/doc1.md"},
				{Path: "/home/user/other/doc2.md"},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "list_documents",
		"arguments": map[string]interface{}{
			"folder": "/home/user/projects",
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestServerGetContext(t *testing.T) {
	store := &fakeStore{
		searchFunc: func(query string, limit int) ([]*db.SearchResult, error) {
			return []*db.SearchResult{}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	t.Run("with max_chars", func(t *testing.T) {
		params, _ := json.Marshal(map[string]interface{}{
			"name": "get_context",
			"arguments": map[string]interface{}{
				"topic":     "test topic",
				"max_chars": float64(100),
			},
		})
		resp := pipeRequest(t, server, jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      float64(1),
			Method:  "tools/call",
			Params:  params,
		})
		if resp.Error != nil {
			t.Fatalf("unexpected error: %v", resp.Error)
		}
	})

	t.Run("with deprecated max_tokens", func(t *testing.T) {
		params, _ := json.Marshal(map[string]interface{}{
			"name": "get_context",
			"arguments": map[string]interface{}{
				"topic":      "test topic",
				"max_tokens": float64(100),
			},
		})
		resp := pipeRequest(t, server, jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      float64(1),
			Method:  "tools/call",
			Params:  params,
		})
		if resp.Error != nil {
			t.Fatalf("unexpected error: %v", resp.Error)
		}
	})

	t.Run("missing topic", func(t *testing.T) {
		params, _ := json.Marshal(map[string]interface{}{
			"name":      "get_context",
			"arguments": map[string]interface{}{},
		})
		resp := pipeRequest(t, server, jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      float64(1),
			Method:  "tools/call",
			Params:  params,
		})
		if resp.Error != nil {
			t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
		}
		result, ok := resp.Result.(map[string]interface{})
		if !ok {
			t.Fatalf("expected result map, got %T", resp.Result)
		}
		if result["isError"] != true {
			t.Fatal("expected isError=true for missing topic")
		}
	})
}

func TestServerReadDocument_SymlinkSwapRejected(t *testing.T) {
	tmpDir := t.TempDir()
	origFile := filepath.Join(tmpDir, "original.txt")
	sensitiveFile := filepath.Join(tmpDir, "secret.txt")

	if err := os.WriteFile(origFile, []byte("indexed content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sensitiveFile, []byte("secret data"), 0644); err != nil {
		t.Fatal(err)
	}

	resolvedOrigFile, err := filepath.EvalSymlinks(origFile)
	if err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{
		getDocFunc: func(path string) (*db.Document, error) {
			if path == resolvedOrigFile {
				return &db.Document{Path: resolvedOrigFile}, nil
			}
			return nil, nil
		},
	}

	os.Remove(origFile)
	if err := os.Symlink(sensitiveFile, origFile); err != nil {
		t.Fatal(err)
	}

	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "read_document",
		"arguments": map[string]interface{}{
			"path": origFile,
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] != true {
		t.Fatal("expected isError=true for symlink-swap path")
	}
}

func TestServerReadDocument_LargeFileTruncated(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	tmpDir := filepath.Join(home, ".symseek-test-"+strings.ToLower(t.Name()))
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	bigFile := filepath.Join(tmpDir, "big.txt")

	bigContent := strings.Repeat("A", 11<<20)
	if err := os.WriteFile(bigFile, []byte(bigContent), 0644); err != nil {
		t.Fatal(err)
	}

	resolvedBigFile, err := filepath.EvalSymlinks(bigFile)
	if err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{
		getDocFunc: func(path string) (*db.Document, error) {
			if path == resolvedBigFile {
				return &db.Document{Path: resolvedBigFile}, nil
			}
			return nil, nil
		},
	}

	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "read_document",
		"arguments": map[string]interface{}{
			"path": bigFile,
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}

	result := resp.Result.(map[string]interface{})
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)

	if !strings.HasSuffix(text, "[Truncated: file exceeds 10 MB read limit]") {
		t.Error("expected truncation notice at end of content")
	}
	if len(text) > 11<<20 {
		t.Errorf("content too large: %d bytes", len(text))
	}
}

func TestIndexSingleFile(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}

	_, err := IndexSingleFile(store, embed, "/nonexistent/file.txt")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestServerSearchDocumentsWithResults(t *testing.T) {
	store := &fakeStore{
		searchFunc: func(query string, limit int) ([]*db.SearchResult, error) {
			return []*db.SearchResult{
				{
					Chunk: &db.Chunk{
						DocumentPath: "/test/doc.md",
						ChunkIndex:   0,
						Content:      "Test content",
					},
					RRFScore: 0.5,
				},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "search_documents",
		"arguments": map[string]interface{}{
			"query": "test",
			"limit": float64(5),
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

func TestServerListDocumentsEmpty(t *testing.T) {
	store := &fakeStore{
		listDocsFunc: func() ([]*db.Document, error) {
			return []*db.Document{}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "list_documents", map[string]interface{}{})
	if isError {
		t.Fatalf("unexpected error: %s", got)
	}
	var result struct {
		Count     int  `json:"count"`
		HasMore   bool `json:"has_more"`
		Documents []struct {
			Path string `json:"path"`
		} `json:"documents"`
	}
	if err := json.Unmarshal([]byte(got), &result); err != nil {
		t.Fatalf("failed to unmarshal response: %v\nraw: %s", err, got)
	}
	if result.Count != 0 {
		t.Errorf("expected count 0, got %d", result.Count)
	}
	if result.HasMore {
		t.Errorf("expected has_more false for empty result")
	}
	if len(result.Documents) != 0 {
		t.Errorf("expected 0 documents, got %d", len(result.Documents))
	}
}

func TestServerListDocumentsPagination(t *testing.T) {
	docs := make([]*db.Document, 10)
	for i := range docs {
		docs[i] = &db.Document{
			Path:      fmt.Sprintf("/home/user/doc-%02d.md", i),
			UpdatedAt: time.Date(2026, 7, 30, 12, 0, i, 0, time.UTC),
		}
	}
	store := &fakeStore{
		listDocsFunc: func() ([]*db.Document, error) { return docs, nil },
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	t.Run("default limit", func(t *testing.T) {
		got, isError := callTool(t, server, "list_documents", map[string]interface{}{})
		if isError {
			t.Fatalf("unexpected error: %s", got)
		}
		var result struct {
			Count     int  `json:"count"`
			HasMore   bool `json:"has_more"`
			Documents []struct {
				Path string `json:"path"`
			} `json:"documents"`
		}
		if err := json.Unmarshal([]byte(got), &result); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, got)
		}
		if result.Count != 10 {
			t.Errorf("expected count 10, got %d", result.Count)
		}
		if result.HasMore {
			t.Errorf("expected has_more false (10 docs <= default limit 50)")
		}
		if len(result.Documents) != 10 {
			t.Errorf("expected 10 documents returned, got %d", len(result.Documents))
		}
	})

	t.Run("limit 3", func(t *testing.T) {
		got, isError := callTool(t, server, "list_documents", map[string]interface{}{
			"limit": float64(3),
		})
		if isError {
			t.Fatalf("unexpected error: %s", got)
		}
		var result struct {
			Count     int  `json:"count"`
			HasMore   bool `json:"has_more"`
			Documents []struct {
				Path string `json:"path"`
			} `json:"documents"`
		}
		if err := json.Unmarshal([]byte(got), &result); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, got)
		}
		if result.Count != 10 {
			t.Errorf("expected count 10, got %d", result.Count)
		}
		if !result.HasMore {
			t.Errorf("expected has_more true (3 < 10)")
		}
		if len(result.Documents) != 3 {
			t.Errorf("expected 3 documents, got %d", len(result.Documents))
		}
	})

	t.Run("offset 5", func(t *testing.T) {
		got, isError := callTool(t, server, "list_documents", map[string]interface{}{
			"limit":  float64(3),
			"offset": float64(5),
		})
		if isError {
			t.Fatalf("unexpected error: %s", got)
		}
		var result struct {
			Count     int  `json:"count"`
			HasMore   bool `json:"has_more"`
			Documents []struct {
				Path string `json:"path"`
			} `json:"documents"`
		}
		if err := json.Unmarshal([]byte(got), &result); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, got)
		}
		if result.Count != 10 {
			t.Errorf("expected count 10, got %d", result.Count)
		}
		if len(result.Documents) != 3 {
			t.Errorf("expected 3 documents, got %d", len(result.Documents))
		}
	})

	t.Run("offset beyond total", func(t *testing.T) {
		got, isError := callTool(t, server, "list_documents", map[string]interface{}{
			"limit":  float64(3),
			"offset": float64(100),
		})
		if isError {
			t.Fatalf("unexpected error: %s", got)
		}
		var result struct {
			Count     int  `json:"count"`
			HasMore   bool `json:"has_more"`
			Documents []struct {
				Path string `json:"path"`
			} `json:"documents"`
		}
		if err := json.Unmarshal([]byte(got), &result); err != nil {
			t.Fatalf("unmarshal: %v\nraw: %s", err, got)
		}
		if result.Count != 10 {
			t.Errorf("expected count 10, got %d", result.Count)
		}
		if len(result.Documents) != 0 {
			t.Errorf("expected 0 documents, got %d", len(result.Documents))
		}
	})
}

func TestServerGetContextWithResults(t *testing.T) {
	store := &fakeStore{
		searchFunc: func(query string, limit int) ([]*db.SearchResult, error) {
			return []*db.SearchResult{
				{
					Chunk: &db.Chunk{
						DocumentPath: "/test/doc.md",
						ChunkIndex:   0,
						Content:      "Test content",
					},
					RRFScore: 0.5,
				},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "get_context",
		"arguments": map[string]interface{}{
			"topic":     "test topic",
			"max_chars": float64(100),
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
}

// ---------------------------------------------------------------------------
// StartServer smoke test  (covers lines 24-42)
// ---------------------------------------------------------------------------

// TestStartServer_StdinEOF verifies that StartServer builds the configured
// MCP server, opens the database, registers all tools, and serves over stdio.
// With stdin at EOF, ServeStdio returns immediately without blocking.
func TestStartServer_StdinEOF(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Redirect stdin to EOF so ServeStdio returns immediately.
	origStdin := os.Stdin
	r, w, _ := os.Pipe()
	os.Stdin = r
	w.Close() // immediate EOF
	defer func() { os.Stdin = origStdin }()

	// Redirect stdout to /dev/null so MCP JSON-RPC output does not pollute
	// test output.
	origStdout := os.Stdout
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = devNull
	defer func() {
		os.Stdout = origStdout
		devNull.Close()
	}()

	if err := StartServer(engine.OllamaConfig{}, nil, engine.RerankConfig{}, engine.ExpandConfig{}); err != nil {
		t.Fatalf("StartServer returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// registerIndexDocument tests  (covers lines 224-266)
// ---------------------------------------------------------------------------

// TestRegisterIndexDocument_MissingPath verifies validation error when the
// path argument is omitted.
func TestRegisterIndexDocument_MissingPath(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "index_document",
		"arguments": map[string]interface{}{},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] != true {
		t.Fatal("expected isError=true for missing path")
	}
}

// TestRegisterIndexDocument_PathOutsideHome verifies that a path outside the
// home directory is rejected by pathutil.RestrictToHome.
func TestRegisterIndexDocument_PathOutsideHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "index_document",
		"arguments": map[string]interface{}{
			"path": "/etc/passwd",
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] != true {
		t.Fatal("expected isError=true for path outside home")
	}
}

// TestRegisterIndexDocument_NonexistentPath verifies error when the path does
// not exist on disk (os.Stat fails).
func TestRegisterIndexDocument_NonexistentPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "index_document",
		"arguments": map[string]interface{}{
			"path": filepath.Join(home, "nonexistent.txt"),
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] != true {
		t.Fatal("expected isError=true for nonexistent path")
	}
}

// TestRegisterIndexDocument_ValidFile exercises the single-file indexing path
// (line 260-264): IndexSingleFile → engine.IndexFile. A real file under a
// temporary HOME is created and indexed through the MCP tool interface.
func TestRegisterIndexDocument_ValidFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a small test file inside the HOME directory.
	testFile := filepath.Join(home, "hello.md")
	if err := os.WriteFile(testFile, []byte("# Hello\nWorld"), 0644); err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "index_document",
		"arguments": map[string]interface{}{
			"path": testFile,
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] == true {
		t.Fatalf("expected success, got tool error: %v", result["content"])
	}

	// Verify the response text indicates successful indexing.
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "Successfully indexed file") {
		t.Errorf("unexpected response text: %s", text)
	}
}

// TestRegisterIndexDocument_ValidDirectory exercises the directory indexing
// path (lines 253-258): engine.IndexDirectory.
func TestRegisterIndexDocument_ValidDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Create a small directory with one supported file.
	testDir := filepath.Join(home, "docs")
	if err := os.MkdirAll(testDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(testDir, "note.txt"), []byte("some content"), 0644); err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "index_document",
		"arguments": map[string]interface{}{
			"path": testDir,
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] == true {
		t.Fatalf("expected success, got tool error: %v", result["content"])
	}

	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "Successfully indexed directory") {
		t.Errorf("unexpected response text: %s", text)
	}
}

// ---------------------------------------------------------------------------
// registerIndexURL tests  (covers lines 273-294)
// ---------------------------------------------------------------------------

// TestRegisterIndexURL_MissingURL verifies validation error when the url
// argument is omitted.
func TestRegisterIndexURL_MissingURL(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name":      "index_url",
		"arguments": map[string]interface{}{},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] != true {
		t.Fatal("expected isError=true for missing URL")
	}
}

// TestRegisterIndexURL_FetchFailure verifies error handling when the URL
// cannot be fetched (connection refused).
func TestRegisterIndexURL_FetchFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SEEK_ALLOW_PRIVATE_URLS", "1")

	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "index_url",
		"arguments": map[string]interface{}{
			"url": "http://127.0.0.1:1/nonexistent",
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] != true {
		t.Fatal("expected isError=true for fetch failure")
	}
}

// TestRegisterIndexURL_Success exercises the happy path: a local HTTP test
// server returns plain text content that is indexed successfully.
func TestRegisterIndexURL_Success(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SEEK_ALLOW_PRIVATE_URLS", "1")

	// Start a local HTTP server that returns plain-text content.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "Hello from test server")
	}))
	defer ts.Close()

	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	params, _ := json.Marshal(map[string]interface{}{
		"name": "index_url",
		"arguments": map[string]interface{}{
			"url": ts.URL,
		},
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	if result["isError"] == true {
		t.Fatalf("expected success, got tool error: %v", result["content"])
	}

	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	if !strings.Contains(text, "Successfully indexed URL") {
		t.Errorf("unexpected response text: %s", text)
	}
}

// ---------------------------------------------------------------------------
// read_document line-range tests
// ---------------------------------------------------------------------------

func newReadDocStore(t *testing.T, content string) (*fakeStore, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	testFile := filepath.Join(home, "lines.txt")
	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	resolved, err := filepath.EvalSymlinks(testFile)
	if err != nil {
		t.Fatal(err)
	}

	store := &fakeStore{
		getDocFunc: func(path string) (*db.Document, error) {
			if path == resolved {
				return &db.Document{Path: resolved}, nil
			}
			return nil, nil
		},
	}
	return store, testFile
}

func callReadDoc(t *testing.T, server *mcpserver.Server, args map[string]interface{}) (string, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]interface{}{
		"name":      "read_document",
		"arguments": args,
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})

	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	isError := result["isError"] == true
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	return text, isError
}

func TestReadDocument_DefaultFullFile(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5"
	store, testFile := newReadDocStore(t, content)
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callReadDoc(t, server, map[string]interface{}{"path": testFile})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	if got != content {
		t.Errorf("full file content mismatch:\ngot:  %q\nwant: %q", got, content)
	}
}

func TestReadDocument_FromLineAndMaxLines(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5"
	store, testFile := newReadDocStore(t, content)
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callReadDoc(t, server, map[string]interface{}{
		"path":     testFile,
		"fromLine": float64(2),
		"maxLines": float64(3),
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	want := "line2\nline3\nline4"
	if got != want {
		t.Errorf("line range mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestReadDocument_FromLine1WithMaxLines(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5"
	store, testFile := newReadDocStore(t, content)
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callReadDoc(t, server, map[string]interface{}{
		"path":     testFile,
		"fromLine": float64(1),
		"maxLines": float64(2),
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	want := "line1\nline2"
	if got != want {
		t.Errorf("line range mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestReadDocument_OutOfRangeFromLine(t *testing.T) {
	content := "line1\nline2\nline3"
	store, testFile := newReadDocStore(t, content)
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callReadDoc(t, server, map[string]interface{}{
		"path":     testFile,
		"fromLine": float64(100),
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	if got != "" {
		t.Errorf("expected empty string for out-of-range fromLine, got: %q", got)
	}
}

func TestReadDocument_InvalidFromLine(t *testing.T) {
	content := "line1\nline2\nline3"
	store, testFile := newReadDocStore(t, content)
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	_, isError := callReadDoc(t, server, map[string]interface{}{
		"path":     testFile,
		"fromLine": float64(-1),
	})
	if !isError {
		t.Fatal("expected error for negative fromLine")
	}

	_, isError = callReadDoc(t, server, map[string]interface{}{
		"path":     testFile,
		"fromLine": float64(-5),
	})
	if !isError {
		t.Fatal("expected error for negative fromLine")
	}
}

func TestReadDocument_MaxLinesExceedsRemaining(t *testing.T) {
	content := "line1\nline2\nline3"
	store, testFile := newReadDocStore(t, content)
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callReadDoc(t, server, map[string]interface{}{
		"path":     testFile,
		"fromLine": float64(2),
		"maxLines": float64(100),
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	want := "line2\nline3"
	if got != want {
		t.Errorf("line range mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// multi_get tests
// ---------------------------------------------------------------------------

func callMultiGet(t *testing.T, server *mcpserver.Server, args map[string]interface{}) (string, bool) {
	t.Helper()
	params, _ := json.Marshal(map[string]interface{}{
		"name":      "multi_get",
		"arguments": args,
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected result map, got %T", resp.Result)
	}
	isError := result["isError"] == true
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"].(string)
	return text, isError
}

func newMultiGetStore(t *testing.T, files map[string]string) (*fakeStore, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)

	docs := make([]*db.Document, 0, len(files))
	for name, content := range files {
		p := filepath.Join(home, name)
		dir := filepath.Dir(p)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
		resolved, _ := filepath.EvalSymlinks(p)
		docs = append(docs, &db.Document{Path: resolved})
	}

	store := &fakeStore{
		listDocsFunc: func() ([]*db.Document, error) {
			return docs, nil
		},
	}
	return store, home
}

func TestMultiGet_MultipleFiles(t *testing.T) {
	store, _ := newMultiGetStore(t, map[string]string{
		"docs/a.md":  "content A",
		"docs/b.md":  "content B",
		"other/c.md": "content C",
	})
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callMultiGet(t, server, map[string]interface{}{
		"pattern": "docs/*.md",
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	if !strings.Contains(got, "content A") {
		t.Error("expected content A in output")
	}
	if !strings.Contains(got, "content B") {
		t.Error("expected content B in output")
	}
	if strings.Contains(got, "content C") {
		t.Error("should not contain content C (different directory)")
	}
	if !strings.Contains(got, "2 file(s) matched") {
		t.Errorf("expected 2 file(s) matched message, got: %s", got)
	}
}

func TestMultiGet_NoMatches(t *testing.T) {
	store, _ := newMultiGetStore(t, map[string]string{
		"docs/a.md": "content A",
	})
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callMultiGet(t, server, map[string]interface{}{
		"pattern": "nonexistent/**/*.txt",
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	if !strings.Contains(got, "No indexed documents matched pattern") {
		t.Errorf("expected no-match message, got: %s", got)
	}
}

func TestMultiGet_MaxBytesSkip(t *testing.T) {
	store, home := newMultiGetStore(t, map[string]string{
		"docs/big.md":   strings.Repeat("X", 200),
		"docs/small.md": "small content",
	})
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callMultiGet(t, server, map[string]interface{}{
		"pattern":  "docs/*.md",
		"maxBytes": float64(100),
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	if !strings.Contains(got, "small content") {
		t.Error("expected small content in output")
	}
	if !strings.Contains(got, "SKIPPED") {
		t.Errorf("expected SKIPPED message for big file, got: %s", got)
	}
	_ = home
}

func TestMultiGet_MaxLinesSkip(t *testing.T) {
	store, _ := newMultiGetStore(t, map[string]string{
		"docs/many.md": "line1\nline2\nline3\nline4\nline5",
		"docs/few.md":  "only one line",
	})
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callMultiGet(t, server, map[string]interface{}{
		"pattern":  "docs/*.md",
		"maxLines": float64(2),
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	if !strings.Contains(got, "only one line") {
		t.Error("expected few.md content in output")
	}
	if !strings.Contains(got, "SKIPPED") {
		t.Errorf("expected SKIPPED message for many.md, got: %s", got)
	}
}

func TestMultiGet_MissingPattern(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	_, isError := callMultiGet(t, server, map[string]interface{}{})
	if !isError {
		t.Fatal("expected error for missing pattern")
	}
}

func TestMultiGet_RestrictsToHome(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	store := &fakeStore{
		listDocsFunc: func() ([]*db.Document, error) {
			return []*db.Document{}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	_, isError := callMultiGet(t, server, map[string]interface{}{
		"pattern": "/etc/**/*.conf",
	})
	if !isError {
		t.Fatal("expected error for pattern outside home")
	}
}

func TestMultiGet_DeepGlob(t *testing.T) {
	store, _ := newMultiGetStore(t, map[string]string{
		"a/b/c/d.md": "deep content",
		"a/x.md":     "shallow content",
		"z.md":       "root content",
	})
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callMultiGet(t, server, map[string]interface{}{
		"pattern": "a/**/*.md",
	})
	if isError {
		t.Fatalf("expected success, got error: %s", got)
	}
	if !strings.Contains(got, "deep content") {
		t.Error("expected deep content in output")
	}
	if !strings.Contains(got, "shallow content") {
		t.Error("expected shallow content in output")
	}
	if strings.Contains(got, "root content") {
		t.Error("should not contain root content")
	}
}

// ---------------------------------------------------------------------------
// set_context / get_contexts tests
// ---------------------------------------------------------------------------

func callTool(t *testing.T, server *mcpserver.Server, name string, args map[string]interface{}) (string, bool) {
	t.Helper()
	rawText, isError, err := callToolResult(t, server, name, args)
	if err != nil {
		t.Fatalf("callTool failed: %v", err)
	}
	text, ok := rawText.(string)
	if !ok {
		t.Fatalf("expected tool result text to be a string, got %T", rawText)
	}
	return text, isError
}

// callToolResult returns the parsed value of content[0].text from a tool call.
// For text results this is a string; for structured JSON results it is the parsed
// Go value (e.g. []interface{} or map[string]interface{}).
func callToolResult(t *testing.T, server *mcpserver.Server, name string, args map[string]interface{}) (any, bool, error) {
	t.Helper()
	params, _ := json.Marshal(map[string]interface{}{
		"name":      name,
		"arguments": args,
	})
	resp := pipeRequest(t, server, jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      float64(1),
		Method:  "tools/call",
		Params:  params,
	})
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		return nil, false, fmt.Errorf("expected result map, got %T", resp.Result)
	}
	isError := result["isError"] == true
	content := result["content"].([]interface{})
	text := content[0].(map[string]interface{})["text"]
	return text, isError, nil
}

func TestSetContext_Roundtrip(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "set_context", map[string]interface{}{
		"path": "/home/user/docs/api",
		"text": "API documentation for the project",
	})
	if isError {
		t.Fatalf("set_context failed: %s", got)
	}
	if !strings.Contains(got, "Context set for /home/user/docs/api") {
		t.Errorf("unexpected set_context response: %s", got)
	}

	got2, isError2 := callTool(t, server, "get_contexts", map[string]interface{}{})
	if isError2 {
		t.Fatalf("get_contexts failed: %s", got2)
	}
	if !strings.Contains(got2, "/home/user/docs/api") {
		t.Errorf("expected path in get_contexts output, got: %s", got2)
	}
	if !strings.Contains(got2, "API documentation for the project") {
		t.Errorf("expected context text in get_contexts output, got: %s", got2)
	}
}

func TestGetContexts_Empty(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "get_contexts", map[string]interface{}{})
	if isError {
		t.Fatalf("get_contexts failed: %s", got)
	}
	if !strings.Contains(got, "No folder contexts configured") {
		t.Errorf("expected empty message, got: %s", got)
	}
}

func TestSetContext_MissingPath(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	_, isError := callTool(t, server, "set_context", map[string]interface{}{
		"text": "some text",
	})
	if !isError {
		t.Fatal("expected error for missing path")
	}
}

func TestSetContext_MissingText(t *testing.T) {
	store := &fakeStore{}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	_, isError := callTool(t, server, "set_context", map[string]interface{}{
		"path": "/some/path",
	})
	if !isError {
		t.Fatal("expected error for missing text")
	}
}

func TestSearchDocuments_LongestPrefixMatch(t *testing.T) {
	store := &fakeStore{
		searchFunc: func(query string, limit int) ([]*db.SearchResult, error) {
			return []*db.SearchResult{
				{
					Chunk: &db.Chunk{
						DocumentPath: "/home/user/docs/api/auth.md",
						ChunkIndex:   0,
						Content:      "Auth content",
					},
					RRFScore: 0.75,
				},
			}, nil
		},
		folderContexts: map[string]string{
			"/home/user/docs":     "General docs",
			"/home/user/docs/api": "API documentation",
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "search_documents", map[string]interface{}{
		"query":  "auth",
		"limit":  float64(5),
		"format": "text",
	})
	if isError {
		t.Fatalf("search_documents failed: %s", got)
	}
	if !strings.Contains(got, "Context: /home/user/docs/api — API documentation") {
		t.Errorf("expected longest prefix context in output, got:\n%s", got)
	}
}

func TestSearchDocuments_NoContextMatch(t *testing.T) {
	store := &fakeStore{
		searchFunc: func(query string, limit int) ([]*db.SearchResult, error) {
			return []*db.SearchResult{
				{
					Chunk: &db.Chunk{
						DocumentPath: "/home/user/projects/app.go",
						ChunkIndex:   0,
						Content:      "Go code",
					},
					RRFScore: 0.6,
				},
			}, nil
		},
		folderContexts: map[string]string{
			"/home/user/docs": "Docs context",
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

	got, isError := callTool(t, server, "search_documents", map[string]interface{}{
		"query":  "app",
		"limit":  float64(5),
		"format": "text",
	})
	if isError {
		t.Fatalf("search_documents failed: %s", got)
	}
	if strings.Contains(got, "Context:") {
		t.Errorf("expected no context line when no prefix matches, got:\n%s", got)
	}
}

func TestSearchDocuments_StructuredJSON(t *testing.T) {
	charStart := 42
	charEnd := 123
	store := &fakeStore{
		searchFunc: func(query string, limit int) ([]*db.SearchResult, error) {
			return []*db.SearchResult{
				{
					Chunk: &db.Chunk{
						DocumentPath: "/home/user/docs/api.md",
						UUID:         "chunk-uuid-1",
						ChunkIndex:   0,
						Content:      "API documentation content",
						CharStart:    &charStart,
						CharEnd:      &charEnd,
					},
					RRFScore: 0.85,
				},
			}, nil
		},
	}
	embed := &fakeEmbedder{}
	server := newTestServer(store, store, embed)

		got, isError, err := callToolResult(t, server, "search_documents", map[string]interface{}{
		"query": "api",
		"limit": float64(5),
	})
	if err != nil {
		t.Fatalf("search_documents failed: %v", err)
	}
	if isError {
		t.Fatalf("search_documents returned error: %v", got)
	}

	gotStr, ok := got.(string)
	if !ok {
		t.Fatalf("expected string content, got %T: %v", got, got)
	}

	var results []*db.StructuredSearchResult
	if err := json.Unmarshal([]byte(gotStr), &results); err != nil {
		t.Fatalf("expected valid JSON string, got %q: %v", gotStr, err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if r.Path != "/home/user/docs/api.md" {
		t.Errorf("expected path /home/user/docs/api.md, got %q", r.Path)
	}
	if r.ChunkID != "chunk-uuid-1" {
		t.Errorf("expected chunk_id chunk-uuid-1, got %q", r.ChunkID)
	}
	if r.CharStart == nil || *r.CharStart != 42 {
		t.Errorf("expected char_start 42, got %v", r.CharStart)
	}
	if r.CharEnd == nil || *r.CharEnd != 123 {
		t.Errorf("expected char_end 123, got %v", r.CharEnd)
	}
	if r.Snippet != "API documentation content" {
		t.Errorf("expected snippet 'API documentation content', got %q", r.Snippet)
	}
	if r.Score <= 0 {
		t.Errorf("expected positive score, got %f", r.Score)
	}
}
