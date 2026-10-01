package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

const searchCLIHybridFixturePath = "../../testdata/port/cli/search-hybrid.json"

type searchCLIHybridFixture struct {
	SchemaVersion int                          `json:"schema_version"`
	Cases         []searchCLIHybridFixtureCase `json:"cases"`
}

type searchCLIHybridFixtureCase struct {
	ID                 string                    `json:"id"`
	Query              string                    `json:"query"`
	EmbeddingDim       int                       `json:"embedding_dim"`
	ProviderStatus     int                       `json:"provider_status"`
	ProviderDimension  int                       `json:"provider_dimension"`
	ExpandQuery        bool                      `json:"expand_query,omitempty"`
	RerankQuery        bool                      `json:"rerank_query,omitempty"`
	VectorBackend      string                    `json:"vector_backend,omitempty"`
	VectorQuantization string                    `json:"vector_quantization,omitempty"`
	ExpandModel        string                    `json:"expand_model,omitempty"`
	ExpandedText       string                    `json:"expanded_text,omitempty"`
	ChatResponse       string                    `json:"chat_response,omitempty"`
	ChatErrorBody      string                    `json:"chat_error_body,omitempty"`
	ChatStatus         int                       `json:"chat_status,omitempty"`
	IndexDocuments     bool                      `json:"index_documents"`
	Documents          []searchCLIHybridDocument `json:"documents"`
	Sources            []string                  `json:"sources"`
	Requests           []searchCLIHybridRequest  `json:"requests"`
	Expected           SearchResponse            `json:"expected"`
}

type searchCLIHybridDocument struct {
	Path       string `json:"path"`
	Body       string `json:"body"`
	Source     string `json:"source,omitempty"`
	IndexModel string `json:"index_model,omitempty"`
}

type searchCLIHybridRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

type capturedSearchEmbedding struct {
	method string
	path   string
	body   json.RawMessage
}

func TestSearchCLIHybridOracle(t *testing.T) {
	cases := searchCLIHybridCases()
	fixture := searchCLIHybridFixture{SchemaVersion: 1, Cases: make([]searchCLIHybridFixtureCase, 0, len(cases))}
	for _, input := range cases {
		t.Run(input.ID, func(t *testing.T) {
			observed := observeSearchCLIHybridCase(t, input)
			fixture.Cases = append(fixture.Cases, observed)
		})
	}
	assertSearchCLIInertVectorSettingsMatchControl(t, fixture.Cases)
	if os.Getenv("PORT_GENERATE") == "1" {
		encoded, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(searchCLIHybridFixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(searchCLIHybridFixturePath)
	if err != nil {
		t.Fatalf("read Go-generated search CLI fixture (generate with PORT_GENERATE=1): %v", err)
	}
	var want searchCLIHybridFixture
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode search CLI fixture: %v", err)
	}
	if want.SchemaVersion != fixture.SchemaVersion || len(want.Cases) != len(fixture.Cases) {
		t.Fatalf("search CLI fixture header = v%d/%d cases, want v%d/%d", want.SchemaVersion, len(want.Cases), fixture.SchemaVersion, len(fixture.Cases))
	}
	for index := range fixture.Cases {
		if !equalSearchCLIHybridCase(fixture.Cases[index], want.Cases[index]) {
			got, _ := json.MarshalIndent(fixture.Cases[index], "", "  ")
			previous, _ := json.MarshalIndent(want.Cases[index], "", "  ")
			t.Fatalf("search CLI fixture case %q is stale; regenerate with PORT_GENERATE=1 go test ./internal/service -run '^TestSearchCLIHybridOracle$'\ncurrent: %s\nfixture: %s", fixture.Cases[index].ID, got, previous)
		}
	}
}

func searchCLIHybridCases() []searchCLIHybridFixtureCase {
	return []searchCLIHybridFixtureCase{
		{
			ID: "multi-root-success-metadata-anchor-snippet", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			Documents: []searchCLIHybridDocument{
				{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in the vault note near its conclusion."},
				{Path: "guide.md", Source: "source", Body: "# Source Heading\n\nA retrieval needle appears in this registered read-only guide."},
				{Path: "nested/note.md", Source: "source", Body: "# Nested Heading\n\nA retrieval needle appears in this nested note."},
			},
			Sources: []string{"source", "source/nested"},
		},
		{
			ID: "provider-failure-local-hash-fallback", Query: "offline needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusServiceUnavailable, ProviderDimension: 3, IndexDocuments: true,
			Documents: []searchCLIHybridDocument{{Path: "offline.md", Body: "# Offline Heading\n\nAn offline needle remains searchable by lexical retrieval."}},
		},
		{
			ID: "wrong-dimension-local-hash-fallback", Query: "dimension needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 2, IndexDocuments: true,
			Documents: []searchCLIHybridDocument{{Path: "dimension.md", Body: "# Dimension Heading\n\nA dimension needle remains searchable by lexical retrieval."}},
		},
		{
			ID: "invalid-query-zero-provider-requests", Query: "owner:daniel",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			Documents: []searchCLIHybridDocument{{Path: "owner.md", Body: "# Owner\n\nThe owner daniel query is handled by safe plain text fallback."}},
		},
		{
			ID: "mixed-spaces-zero-provider-requests", Query: "mixed needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			Documents: []searchCLIHybridDocument{
				{Path: "first.md", Body: "# First\n\nA mixed needle is in this first source.", IndexModel: "fixture-model-a"},
				{Path: "second.md", Body: "# Second\n\nA mixed needle is in this second source.", IndexModel: "fixture-model-b"},
			},
		},
		{
			ID: "empty-retrieval-falls-back-to-sidecar", Query: "lexical only needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: false,
			Documents: []searchCLIHybridDocument{{Path: "sidecar-only.md", Body: "# Sidecar Only\n\nA lexical only needle exists solely in the sidecar."}},
		},
		{
			ID: "unicode-expanding-width-snippet", Query: "needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			Documents: []searchCLIHybridDocument{{Path: "unicode-width.md", Body: "İ" + strings.Repeat("x", 300) + "needle" + strings.Repeat("y", 300)}},
		},
		{
			ID: "unicode-three-byte-width-snippet", Query: "needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			Documents: []searchCLIHybridDocument{{Path: "unicode-kelvin.md", Body: strings.Repeat("x", 200) + "KKneedle" + strings.Repeat("y", 400)}},
		},
		{
			ID: "hyde-expansion-success", Query: "orbital greenhouse",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			ExpandQuery: true, ExpandModel: "fixture-chat-model",
			ExpandedText: "A greenhouse in orbit grows food for a space station.",
			Documents:    []searchCLIHybridDocument{{Path: "space.md", Body: "# Space agriculture\n\nA greenhouse in orbit grows food for a space station."}},
		},
		{
			ID: "hyde-chat-failure-keeps-query-vector", Query: "orbital greenhouse",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			ExpandQuery: true, ExpandedText: "unused", ChatStatus: http.StatusServiceUnavailable,
			ChatErrorBody: strings.Repeat("x", 512) + "TAIL_MARKER",
			Documents:     []searchCLIHybridDocument{{Path: "space.md", Body: "# Space agriculture\n\nA greenhouse in orbit grows food for a space station."}},
		},
		{
			ID: "hyde-chat-go-json-duplicate-case-null-and-trailing", Query: "orbital greenhouse",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			ExpandQuery: true, ExpandModel: "fixture-chat-model",
			ExpandedText: "A greenhouse in orbit grows food for a space station.",
			ChatResponse: `{"meſſage":{"CONTENT":"discarded"},"message":{"content":null,"Content":"A greenhouse in orbit grows food for a space station."}} {"trailing":true}`,
			Documents:    []searchCLIHybridDocument{{Path: "space.md", Body: "# Space agriculture\n\nA greenhouse in orbit grows food for a space station."}},
		},
		{
			ID: "hyde-identical-passage-reuses-query-cache", Query: "cached identical passage",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3, IndexDocuments: true,
			ExpandQuery: true, ExpandModel: "fixture-chat-model",
			ExpandedText: "cached identical passage",
			Documents:    []searchCLIHybridDocument{{Path: "cache.md", Body: "# Cache behavior\n\nA cached identical passage remains searchable."}},
		},
		{
			ID: "hyde-unknown-dimension-mismatch-keeps-original", Query: "unknown dimension query",
			EmbeddingDim: 0, ProviderStatus: http.StatusServiceUnavailable, ProviderDimension: 3, IndexDocuments: true,
			ExpandQuery: true, ExpandedText: "a different length passage",
			Documents: []searchCLIHybridDocument{{Path: "unknown-dim.md", Body: "# Unknown dimensions\n\nA different length passage."}},
		},
		{
			ID: "rerank-config-flag-is-inert-in-cli-search", Query: "rerank flag needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			RerankQuery: true,
			Documents:   []searchCLIHybridDocument{{Path: "rerank.md", Body: "# Rerank Flag\n\nA rerank flag needle remains on ordinary hybrid search."}},
		},
		{
			ID: "turbo-prod-quantization-config-is-inert-in-cli-search", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			VectorBackend: "sqlite", VectorQuantization: "turbo-prod", IndexDocuments: true,
			Documents: []searchCLIHybridDocument{
				{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in the vault note near its conclusion."},
				{Path: "guide.md", Source: "source", Body: "# Source Heading\n\nA retrieval needle appears in this registered read-only guide."},
				{Path: "nested/note.md", Source: "source", Body: "# Nested Heading\n\nA retrieval needle appears in this nested note."},
			},
			Sources: []string{"source", "source/nested"},
		},
		{
			ID: "raw-unknown-backend-is-inert-in-cli-search", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			VectorBackend: "alternate-fixture-backend", VectorQuantization: "off", IndexDocuments: true,
			Documents: []searchCLIHybridDocument{
				{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in the vault note near its conclusion."},
				{Path: "guide.md", Source: "source", Body: "# Source Heading\n\nA retrieval needle appears in this registered read-only guide."},
				{Path: "nested/note.md", Source: "source", Body: "# Nested Heading\n\nA retrieval needle appears in this nested note."},
			},
			Sources: []string{"source", "source/nested"},
		},
		{
			ID: "raw-unknown-quantization-is-inert-in-cli-search", Query: "retrieval needle",
			EmbeddingDim: 3, ProviderStatus: http.StatusOK, ProviderDimension: 3,
			VectorBackend: "sqlite", VectorQuantization: "unknown-fixture-mode", IndexDocuments: true,
			Documents: []searchCLIHybridDocument{
				{Path: "vault.md", Body: "---\ntitle: Vault Retrieval Note\ntags: [retrieval]\n---\n\n# Vault Heading\n\nA retrieval needle appears in the vault note near its conclusion."},
				{Path: "guide.md", Source: "source", Body: "# Source Heading\n\nA retrieval needle appears in this registered read-only guide."},
				{Path: "nested/note.md", Source: "source", Body: "# Nested Heading\n\nA retrieval needle appears in this nested note."},
			},
			Sources: []string{"source", "source/nested"},
		},
	}
}

func observeSearchCLIHybridCase(t *testing.T, input searchCLIHybridFixtureCase) searchCLIHybridFixtureCase {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	vaultRoot := filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(vaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(t.TempDir(), "retrieval.db")
	var requestMu sync.Mutex
	requests := []capturedSearchEmbedding{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestBody, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read embedding request: %v", readErr)
		}
		requestMu.Lock()
		requests = append(requests, capturedSearchEmbedding{method: r.Method, path: r.URL.RequestURI(), body: requestBody})
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
		if err := json.Unmarshal(requestBody, &request); err != nil {
			t.Errorf("decode embedding request: %v", err)
		}
		status := http.StatusOK
		dimension := 3
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
		type embedding struct {
			Embedding []float32 `json:"embedding"`
		}
		response := struct {
			Data []embedding `json:"data"`
		}{Data: make([]embedding, len(request.Input))}
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
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig := func(model string) {
		t.Helper()
		config := fmt.Sprintf("index_path = %q\nollama_url = %q\nmodel = %q\nembedding_dim = %d\ntimeout_seconds = 5\nretry_count = 0\nexpand_query = %t\nexpand_model = %q\nexpand_timeout_seconds = 5\nrerank_query = %t\n", indexPath, server.URL+"/api/embeddings", model, input.EmbeddingDim, input.ExpandQuery, input.ExpandModel, input.RerankQuery)
		if input.VectorBackend != "" {
			config += fmt.Sprintf("vector_backend = %q\n", input.VectorBackend)
		}
		if input.VectorQuantization != "" {
			config += fmt.Sprintf("vector_quantization = %q\n", input.VectorQuantization)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("fixture-model")
	documentPaths := make([]string, 0, len(input.Documents))
	sourcePaths := map[string]string{}
	for _, source := range input.Sources {
		root := filepath.Join(t.TempDir(), filepath.FromSlash(source))
		if parent, nested, ok := strings.Cut(source, "/"); ok && sourcePaths[parent] != "" {
			root = filepath.Join(sourcePaths[parent], filepath.FromSlash(nested))
		}
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		// SourceRegistry.Add stores the canonical identity. Retain that same
		// coordinate system for fixture setup and source-token projection.
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		sourcePaths[source] = root
	}
	for _, document := range input.Documents {
		root := vaultRoot
		if document.Source != "" {
			root = sourcePaths[document.Source]
		}
		path := filepath.Join(root, filepath.FromSlash(document.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(document.Body), 0o600); err != nil {
			t.Fatal(err)
		}
		documentPaths = append(documentPaths, path)
	}
	if input.IndexDocuments {
		models := map[string][]string{}
		for index, document := range input.Documents {
			model := document.IndexModel
			if model == "" {
				model = "fixture-model"
			}
			models[model] = append(models[model], documentPaths[index])
		}
		for model, paths := range models {
			writeConfig(model)
			client, err := retrieval.Open()
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				if err := client.Index(path, ""); err != nil {
					_ = client.Close()
					t.Fatalf("index fixture document: %v", err)
				}
			}
			if err := client.Close(); err != nil {
				t.Fatal(err)
			}
		}
		writeConfig("fixture-model")
	}
	for _, source := range input.Sources {
		registry, err := retrieval.NewSourceRegistry(vaultRoot)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Add(sourcePaths[source]); err != nil {
			t.Fatalf("register source %q: %v", source, err)
		}
	}
	sidecarDB, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := sidecarDB.RefreshIndex(vaultRoot); err != nil {
		t.Fatal(err)
	}
	requestMu.Lock()
	requests = nil
	requestMu.Unlock()
	service := New(vaultRoot, sidecarDB)
	defer func() { _ = service.Close(); _ = sidecarDB.Close() }()
	response, err := service.SearchWithMeta(input.Query)
	if err != nil {
		t.Fatalf("SearchWithMeta: %v", err)
	}
	input.Expected = response
	for index := range input.Expected.Results {
		result := &input.Expected.Results[index]
		if filepath.IsAbs(result.Path) {
			for source, sourcePath := range sourcePaths {
				if withinSearchCLIPath(result.Path, sourcePath) {
					if relative, err := filepath.Rel(sourcePath, result.Path); err == nil {
						result.Path = "@source/" + source + "/" + filepath.ToSlash(relative)
					}
					break
				}
			}
		} else {
			result.Path = "@vault/" + filepath.ToSlash(result.Path)
		}
	}
	sort.SliceStable(input.Expected.Results, func(i, j int) bool {
		left, right := input.Expected.Results[i], input.Expected.Results[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		return left.Path < right.Path
	})
	requestMu.Lock()
	defer requestMu.Unlock()
	input.Requests = make([]searchCLIHybridRequest, 0, len(requests))
	for _, request := range requests {
		if len(request.body) == 0 {
			continue
		}
		input.Requests = append(input.Requests, searchCLIHybridRequest{Method: request.method, Path: request.path, Body: request.body})
	}
	if input.RerankQuery {
		for _, request := range input.Requests {
			if request.Path == "/api/chat" {
				t.Fatal("production Go CLI search unexpectedly activated rerank_query")
			}
		}
	}
	return input
}

func equalSearchCLIHybridCase(left, right searchCLIHybridFixtureCase) bool {
	left.Expected.Results = sortedSearchCLIResults(left.Expected.Results)
	right.Expected.Results = sortedSearchCLIResults(right.Expected.Results)
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func assertSearchCLIInertVectorSettingsMatchControl(t *testing.T, cases []searchCLIHybridFixtureCase) {
	t.Helper()
	var control *searchCLIHybridFixtureCase
	for index := range cases {
		if cases[index].ID == "multi-root-success-metadata-anchor-snippet" {
			control = &cases[index]
		}
	}
	if control == nil {
		t.Fatal("inert vector config comparison control case missing")
	}
	for _, id := range []string{
		"turbo-prod-quantization-config-is-inert-in-cli-search",
		"raw-unknown-backend-is-inert-in-cli-search",
		"raw-unknown-quantization-is-inert-in-cli-search",
	} {
		var configured *searchCLIHybridFixtureCase
		for index := range cases {
			if cases[index].ID == id {
				configured = &cases[index]
				break
			}
		}
		if configured == nil {
			t.Fatalf("inert vector config comparison case %q missing", id)
		}
		controlResults := sortedSearchCLIResults(control.Expected.Results)
		configuredResults := sortedSearchCLIResults(configured.Expected.Results)
		if control.Expected.Hint != configured.Expected.Hint || !reflect.DeepEqual(controlResults, configuredResults) {
			t.Fatalf("%s search result differs from off control: control=%+v configured=%+v", id, control.Expected, configured.Expected)
		}
		if !reflect.DeepEqual(control.Requests, configured.Requests) {
			t.Fatalf("%s provider requests differ from off control: control=%+v configured=%+v", id, control.Requests, configured.Requests)
		}
	}
}

func sortedSearchCLIResults(results []SearchResult) []SearchResult {
	copyResults := append([]SearchResult(nil), results...)
	sort.SliceStable(copyResults, func(i, j int) bool {
		if copyResults[i].Score != copyResults[j].Score {
			return copyResults[i].Score > copyResults[j].Score
		}
		return copyResults[i].Path < copyResults[j].Path
	})
	return copyResults
}

func withinSearchCLIPath(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
