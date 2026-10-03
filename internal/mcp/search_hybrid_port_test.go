package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

const searchHybridMCPFixturePath = "../../testdata/port/mcp/search-hybrid.json"

// Resolve test-owned storage roots before passing them to SQLite's no-symlink
// boundary, including when macOS or a test runner supplies an aliased TMPDIR.
func canonicalOracleTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

type searchHybridMCPFixture struct {
	SchemaVersion int                          `json:"schema_version"`
	Cases         []searchHybridMCPFixtureCase `json:"cases"`
}

type searchHybridMCPFixtureCase struct {
	ID                    string                    `json:"id"`
	Query                 string                    `json:"query"`
	EmbeddingDim          int                       `json:"embedding_dim"`
	ProviderStatus        int                       `json:"provider_status"`
	ProviderDimension     int                       `json:"provider_dimension"`
	ExpandQuery           bool                      `json:"expand_query,omitempty"`
	RerankQuery           bool                      `json:"rerank_query,omitempty"`
	VectorBackend         string                    `json:"vector_backend,omitempty"`
	VectorQuantization    string                    `json:"vector_quantization,omitempty"`
	ExpandModel           string                    `json:"expand_model,omitempty"`
	ExpandedText          string                    `json:"expanded_text,omitempty"`
	ChatResponse          string                    `json:"chat_response,omitempty"`
	ChatErrorBody         string                    `json:"chat_error_body,omitempty"`
	ChatStatus            int                       `json:"chat_status,omitempty"`
	Documents             []searchHybridMCPDocument `json:"documents"`
	ExternalDocuments     []searchHybridMCPDocument `json:"external_documents,omitempty"`
	UnregisteredDocuments []searchHybridMCPDocument `json:"unregistered_documents,omitempty"`
	Requests              []searchHybridMCPRequest  `json:"requests"`
	Expected              json.RawMessage           `json:"expected"`
}

type searchHybridMCPDocument struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

type searchHybridMCPRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

type capturedSearchEmbeddingRequest struct {
	Method string
	Path   string
	Body   []byte
}

func TestSearchHybridMCPOracle(t *testing.T) {
	cases := searchHybridMCPCases()
	fixture := searchHybridMCPFixture{SchemaVersion: 1, Cases: make([]searchHybridMCPFixtureCase, 0, len(cases))}
	for _, input := range cases {
		input := input
		t.Run(input.ID, func(t *testing.T) {
			fixture.Cases = append(fixture.Cases, observeSearchHybridMCPCase(t, input))
		})
	}
	assertSearchHybridMCPInertVectorSettingsMatchControl(t, fixture.Cases)
	if os.Getenv("PORT_GENERATE") == "1" {
		encoded, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(searchHybridMCPFixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(searchHybridMCPFixturePath)
	if err != nil {
		t.Fatalf("read Go-generated MCP search fixture (generate with PORT_GENERATE=1): %v", err)
	}
	var want searchHybridMCPFixture
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode MCP search fixture: %v", err)
	}
	if want.SchemaVersion != fixture.SchemaVersion || len(want.Cases) != len(fixture.Cases) {
		t.Fatalf("MCP search fixture header = v%d/%d cases, want v%d/%d", want.SchemaVersion, len(want.Cases), fixture.SchemaVersion, len(fixture.Cases))
	}
	for index := range fixture.Cases {
		got, previous := fixture.Cases[index], want.Cases[index]
		sameRequests := sameSearchRequests(got.Requests, previous.Requests)
		sameExpected := sameSearchJSON(got.Expected, previous.Expected)
		if got.ID != previous.ID || got.Query != previous.Query || got.EmbeddingDim != previous.EmbeddingDim ||
			got.ProviderStatus != previous.ProviderStatus || got.ProviderDimension != previous.ProviderDimension ||
			!reflect.DeepEqual(got.Documents, previous.Documents) ||
			!reflect.DeepEqual(got.ExternalDocuments, previous.ExternalDocuments) ||
			!reflect.DeepEqual(got.UnregisteredDocuments, previous.UnregisteredDocuments) ||
			!sameRequests || !sameExpected {
			currentJSON, _ := json.MarshalIndent(got, "", "  ")
			previousJSON, _ := json.MarshalIndent(previous, "", "  ")
			t.Fatalf("MCP search fixture case %q is stale (requests=%t expected=%t); regenerate with PORT_GENERATE=1 go test ./internal/mcp -run '^TestSearchHybridMCPOracle$'\ncurrent: %s\nfixture: %s", got.ID, sameRequests, sameExpected, currentJSON, previousJSON)
		}
	}
}

func sameSearchJSON(left, right []byte) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func sameSearchRequests(left, right []searchHybridMCPRequest) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Method != right[index].Method || left[index].Path != right[index].Path || !sameSearchJSON(left[index].Body, right[index].Body) {
			return false
		}
	}
	return true
}

func assertSearchHybridMCPInertVectorSettingsMatchControl(t *testing.T, cases []searchHybridMCPFixtureCase) {
	t.Helper()
	var control *searchHybridMCPFixtureCase
	for index := range cases {
		if cases[index].ID == "hybrid-success-float-metadata" {
			control = &cases[index]
		}
	}
	if control == nil {
		t.Fatal("inert vector config comparison control case missing")
	}
	for _, id := range []string{
		"turbo-prod-quantization-config-is-inert-in-mcp-search",
		"raw-unknown-backend-is-inert-in-mcp-search",
		"raw-unknown-quantization-is-inert-in-mcp-search",
	} {
		var configured *searchHybridMCPFixtureCase
		for index := range cases {
			if cases[index].ID == id {
				configured = &cases[index]
				break
			}
		}
		if configured == nil {
			t.Fatalf("inert vector config comparison case %q missing", id)
		}
		if !sameSearchJSON(control.Expected, configured.Expected) {
			t.Fatalf("%s MCP result differs from off control: control=%s configured=%s", id, control.Expected, configured.Expected)
		}
		if !sameSearchRequests(control.Requests, configured.Requests) {
			t.Fatalf("%s MCP provider requests differ from off control: control=%+v configured=%+v", id, control.Requests, configured.Requests)
		}
	}
}

func searchHybridMCPCases() []searchHybridMCPFixtureCase {
	return []searchHybridMCPFixtureCase{
		{
			ID: "hybrid-success-float-metadata", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in this note."}},
		},
		{
			ID: "hybrid-provider-failure-local-hash", Query: "offline needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusServiceUnavailable, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{{Path: "offline.md", Body: "# Offline Note\n\nAn offline needle remains searchable through the lexical branch."}},
		},
		{
			ID: "hybrid-registered-external-source", Query: "registered external needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents:             []searchHybridMCPDocument{},
			ExternalDocuments:     []searchHybridMCPDocument{{Path: "registered.md", Body: "# Registered Source\n\nA registered external needle."}},
			UnregisteredDocuments: []searchHybridMCPDocument{{Path: "unregistered.md", Body: "# Unregistered Source\n\nAn unregistered external needle."}},
		},
		{
			ID: "scoped-tag-and-positive-term", Query: "tag:retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{
				{Path: "projects/tagged.md", Body: "---\ntitle: Tagged Note\ntags: [retrieval]\n---\n\nA scoped needle finds this note."},
				{Path: "archive/untagged.md", Body: "---\ntitle: Untagged Note\ntags: [archive]\n---\n\nAn untagged needle must not match."},
			},
		},
		{
			ID: "scoped-tag-only", Query: "tag:retrieval",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{
				{Path: "tagged.md", Body: "---\ntitle: Tagged Note\ntags: [retrieval]\n---\n\nThis document matches the tag-only plan."},
				{Path: "untagged.md", Body: "---\ntitle: Untagged Note\ntags: [archive]\n---\n\nThis document must not match the tag-only plan."},
			},
		},
		{
			ID: "scoped-path-and-negative-term", Query: "path:projects -private needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{
				{Path: "projects/public.md", Body: "# Public Note\n\nA public needle is available."},
				{Path: "projects/private.md", Body: "# Private Note\n\nA private needle is excluded."},
				{Path: "archive/other.md", Body: "# Other Note\n\nAn unrelated needle is outside the path."},
			},
		},
		{
			// Go applies path filters to absolute indexed paths. Target the file
			// name so macOS's canonical /private/var temp root is not excluded.
			ID: "scoped-negated-singleton-filters-keep-matches", Query: "-path:private.md -status:draft -type:pdf needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{
				{Path: "public.md", Body: "---\ntitle: Public Note\nstatus: open\ndocument_type: note\n---\n\nA public needle remains."},
				{Path: "archive/other.md", Body: "---\ntitle: Other Note\nstatus: open\ndocument_type: note\n---\n\nAnother retained needle."},
				{Path: "private.md", Body: "---\ntitle: Private Note\nstatus: draft\ndocument_type: pdf\n---\n\nA private needle is excluded."},
			},
		},
		{
			ID: "scoped-unicode-type-date", Query: "tag:Μ type:note created:2026-09-29 needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{
				{Path: "unicode.md", Body: "---\ntitle: Unicode Tag\ntags: [µ]\ncreated: 2026-09-29\n---\n\nA date-scoped needle."},
				{Path: "other.md", Body: "---\ntitle: Other Tag\ntags: [other]\ncreated: 2026-09-29\n---\n\nA date-scoped needle without the tag."},
			},
		},
		{
			ID: "malformed-query-fallback-hint", Query: `"unterminated`,
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{{Path: "syntax.md", Body: "A malformed search query must still return plain full-text results."}},
		},
		{
			ID: "whitespace-query-empty-results", Query: "   ",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			Documents: []searchHybridMCPDocument{{Path: "ignored.md", Body: "Whitespace queries return no results."}},
		},
		{
			ID: "hyde-expansion-success", Query: "orbital greenhouse",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			ExpandQuery: true, ExpandModel: "fixture-chat-model",
			ExpandedText: "A greenhouse in orbit grows food for a space station.",
			ChatResponse: `{"meſſage":{"CONTENT":"discarded"},"message":{"content":null,"Content":"A greenhouse in orbit grows food for a space station."}} {"trailing":true}`,
			Documents:    []searchHybridMCPDocument{{Path: "space.md", Body: "# Space agriculture\n\nA greenhouse in orbit grows food for a space station."}},
		},
		{
			ID: "hyde-chat-failure-keeps-query-vector", Query: "orbital greenhouse",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			ExpandQuery: true, ExpandedText: "unused", ChatStatus: http.StatusServiceUnavailable,
			ChatErrorBody: strings.Repeat("x", 512) + "TAIL_MARKER",
			Documents:     []searchHybridMCPDocument{{Path: "space.md", Body: "# Space agriculture\n\nA greenhouse in orbit grows food for a space station."}},
		},
		{
			ID: "hyde-identical-passage-reuses-query-cache", Query: "cached identical passage",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			ExpandQuery: true, ExpandModel: "fixture-chat-model", ExpandedText: "cached identical passage",
			Documents: []searchHybridMCPDocument{{Path: "cache.md", Body: "# Cache behavior\n\nA cached identical passage remains searchable."}},
		},
		{
			ID: "rerank-config-flag-is-inert-in-mcp-search", Query: "rerank flag needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			RerankQuery: true,
			Documents:   []searchHybridMCPDocument{{Path: "rerank.md", Body: "# Rerank Flag\n\nA rerank flag needle remains on ordinary hybrid search."}},
		},
		{
			ID: "turbo-prod-quantization-config-is-inert-in-mcp-search", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			VectorBackend: "sqlite", VectorQuantization: "turbo-prod",
			Documents: []searchHybridMCPDocument{{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in this note."}},
		},
		{
			ID: "raw-unknown-backend-is-inert-in-mcp-search", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			VectorBackend: "alternate-fixture-backend", VectorQuantization: "off",
			Documents: []searchHybridMCPDocument{{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in this note."}},
		},
		{
			ID: "raw-unknown-quantization-is-inert-in-mcp-search", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			VectorBackend: "sqlite", VectorQuantization: "unknown-fixture-mode",
			Documents: []searchHybridMCPDocument{{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in this note."}},
		},
	}
}

func observeSearchHybridMCPCase(t *testing.T, input searchHybridMCPFixtureCase) searchHybridMCPFixtureCase {
	t.Helper()
	home := canonicalOracleTempDir(t)
	vaultRoot := filepath.Join(canonicalOracleTempDir(t), "vault")
	registeredRoot := filepath.Join(canonicalOracleTempDir(t), "registered-source")
	unregisteredRoot := filepath.Join(canonicalOracleTempDir(t), "unregistered-source")
	if err := os.MkdirAll(vaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))

	var requestMu sync.Mutex
	requests := []capturedSearchEmbeddingRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read embedding request: %v", err)
		}
		requestMu.Lock()
		requests = append(requests, capturedSearchEmbeddingRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: body})
		requestMu.Unlock()
		if r.URL.Path == "/api/chat" {
			status := input.ChatStatus
			if status == 0 {
				status = http.StatusOK
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			if status >= 300 {
				body := input.ChatErrorBody
				if body == "" {
					body = `{"error":"fixture chat failure"}`
				}
				_, _ = w.Write([]byte(body))
				return
			}
			if input.ChatResponse != "" {
				_, _ = w.Write([]byte(input.ChatResponse))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": input.ExpandedText}})
			return
		}
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode embedding request: %v", err)
		}
		status := http.StatusOK
		dimension := input.EmbeddingDim
		if len(request.Input) == 1 && request.Input[0] == input.Query {
			status = input.ProviderStatus
			dimension = input.ProviderDimension
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 300 {
			_, _ = w.Write([]byte(`{"error":"fixture provider failure"}`))
			return
		}
		response := struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}{Data: make([]struct {
			Embedding []float32 `json:"embedding"`
		}, len(request.Input))}
		for index := range response.Data {
			vector := make([]float32, dimension)
			if request.Input[index] == input.ExpandedText && len(vector) > 1 {
				vector[1] = 1
			} else if len(vector) > 0 {
				vector[0] = 1
			}
			response.Data[index].Embedding = vector
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	indexPath := filepath.Join(canonicalOracleTempDir(t), "retrieval.db")
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configText := fmt.Sprintf("index_path = %q\nollama_url = %q\nmodel = %q\nembedding_dim = %d\ntimeout_seconds = 5\nretry_count = 0\nexpand_query = %t\nexpand_model = %q\nexpand_timeout_seconds = 5\nrerank_query = %t\n", indexPath, server.URL+"/api/embeddings", "fixture-model", input.EmbeddingDim, input.ExpandQuery, input.ExpandModel, input.RerankQuery)
	if input.VectorBackend != "" {
		configText += fmt.Sprintf("vector_backend = %q\n", input.VectorBackend)
	}
	if input.VectorQuantization != "" {
		configText += fmt.Sprintf("vector_quantization = %q\n", input.VectorQuantization)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, document := range input.Documents {
		writeSearchHybridDocument(t, vaultRoot, document)
	}
	for _, document := range input.ExternalDocuments {
		writeSearchHybridDocument(t, registeredRoot, document)
	}
	for _, document := range input.UnregisteredDocuments {
		writeSearchHybridDocument(t, unregisteredRoot, document)
	}
	sidecarDB, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := sidecarDB.RefreshIndex(vaultRoot); err != nil {
		_ = sidecarDB.Close()
		t.Fatal(err)
	}
	index, err := retrieval.OpenForVault(vaultRoot)
	if err != nil {
		_ = sidecarDB.Close()
		t.Fatal(err)
	}
	for _, document := range input.Documents {
		if err := index.Index(filepath.Join(vaultRoot, filepath.FromSlash(document.Path)), ""); err != nil {
			_ = index.Close()
			_ = sidecarDB.Close()
			t.Fatalf("index fixture document: %v", err)
		}
	}
	if len(input.ExternalDocuments) > 0 {
		registry, err := retrieval.NewSourceRegistry(vaultRoot)
		if err != nil {
			_ = index.Close()
			_ = sidecarDB.Close()
			t.Fatal(err)
		}
		registeredSource, err := registry.Add(registeredRoot)
		if err != nil {
			_ = index.Close()
			_ = sidecarDB.Close()
			t.Fatalf("register external fixture source: %v", err)
		}
		// Use the registry's identity for indexing and fixture path tokens.
		registeredRoot = registeredSource.Path
		for _, document := range input.ExternalDocuments {
			if err := index.Index(filepath.Join(registeredRoot, filepath.FromSlash(document.Path)), ""); err != nil {
				_ = index.Close()
				_ = sidecarDB.Close()
				t.Fatalf("index registered external fixture document: %v", err)
			}
		}
	}
	for _, document := range input.UnregisteredDocuments {
		if err := index.Index(filepath.Join(unregisteredRoot, filepath.FromSlash(document.Path)), ""); err != nil {
			_ = index.Close()
			_ = sidecarDB.Close()
			t.Fatalf("index unregistered fixture document: %v", err)
		}
	}
	if err := index.Close(); err != nil {
		_ = sidecarDB.Close()
		t.Fatal(err)
	}
	requestMu.Lock()
	requests = nil
	requestMu.Unlock()

	factory := func() (*service.Service, *sidecar.DB, error) {
		db, err := sidecar.OpenForVault(vaultRoot)
		if err != nil {
			return nil, nil, err
		}
		svc := service.New(vaultRoot, db)
		t.Cleanup(func() {
			if err := svc.Close(); err != nil {
				t.Errorf("close Search oracle retrieval client: %v", err)
			}
		})
		return svc, db, nil
	}
	mcp := mcpserver.New("symdesk", "test-version")
	mcp.RegisterTool(newSearchTool(factory))
	requestBody, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "desk_search", "arguments": map[string]any{"query": input.Query}},
	})
	if err != nil {
		_ = sidecarDB.Close()
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := mcp.ServeIO(context.Background(), bytes.NewReader(append(requestBody, '\n')), &output); err != nil {
		_ = sidecarDB.Close()
		t.Fatalf("run actual Go MCP ServeIO: %v", err)
	}
	if err := sidecarDB.Close(); err != nil {
		t.Fatal(err)
	}
	input.Expected = normalizeSearchHybridMCPFrame(t, output.Bytes(), vaultRoot, registeredRoot, unregisteredRoot)
	requestMu.Lock()
	defer requestMu.Unlock()
	input.Requests = make([]searchHybridMCPRequest, 0, len(requests))
	for _, request := range requests {
		input.Requests = append(input.Requests, searchHybridMCPRequest{Method: request.Method, Path: request.Path, Body: request.Body})
	}
	if input.RerankQuery {
		for _, request := range input.Requests {
			if request.Path == "/api/chat" {
				t.Fatal("production Go MCP search unexpectedly activated rerank_query")
			}
		}
	}
	return input
}

func writeSearchHybridDocument(t *testing.T, root string, document searchHybridMCPDocument) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(document.Path))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(document.Body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func normalizeSearchHybridMCPFrame(t *testing.T, frame []byte, vaultRoot, registeredRoot, unregisteredRoot string) json.RawMessage {
	t.Helper()
	var response map[string]json.RawMessage
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode Go MCP frame: %v: %s", err, frame)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatalf("decode Go MCP result: %v: %s", err, response["result"])
	}
	var content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(result["content"], &content); err != nil || len(content) != 1 || content[0].Type != "text" {
		t.Fatalf("Go MCP result is not one text block: %v: %s", err, response["result"])
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(content[0].Text), &fields) == nil && fields["results"] != nil {
		// Decode the inner tool payload before projecting native Windows paths;
		// its JSON-escaped backslashes do not match an unescaped temporary root.
		// Keep the Go result field order and every non-path value unchanged.
		var payload service.SearchResponse
		if err := json.Unmarshal([]byte(content[0].Text), &payload); err != nil {
			t.Fatal(err)
		}
		for i := range payload.Results {
			path := filepath.ToSlash(payload.Results[i].Path)
			for _, projection := range []struct{ root, marker string }{
				{vaultRoot, "$VAULT"}, {registeredRoot, "$EXTERNAL"}, {unregisteredRoot, "$UNREGISTERED"},
			} {
				root := filepath.ToSlash(projection.root)
				if path == root || strings.HasPrefix(path, root+"/") {
					path = projection.marker + strings.TrimPrefix(path, root)
					break
				}
			}
			payload.Results[i].Path = path
		}
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		content[0].Text = string(encoded)
	}
	result["content"], _ = json.Marshal(content)
	response["result"], _ = json.Marshal(result)
	normalized, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}
