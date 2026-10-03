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
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/tools"
)

const askOfflineMCPFixturePath = "../../testdata/port/mcp/ask-offline.json"

type askOfflineMCPFixture struct {
	SchemaVersion int                        `json:"schema_version"`
	Cases         []askOfflineMCPFixtureCase `json:"cases"`
}

type askOfflineMCPFixtureCase struct {
	ID               string                     `json:"id"`
	EmbeddingDim     int                        `json:"embedding_dim,omitempty"`
	Documents        []askOfflineMCPDocument    `json:"documents"`
	ExpectedTool     json.RawMessage            `json:"expected_tool"`
	Calls            []askOfflineMCPFixtureCall `json:"calls"`
	ProviderRequests []askOfflineMCPRequest     `json:"provider_requests,omitempty"`
}

type askOfflineMCPFixtureCall struct {
	ID            string          `json:"id"`
	Tool          string          `json:"tool,omitempty"`
	ArgumentsJSON string          `json:"arguments_json"`
	RawParamsJSON string          `json:"raw_params_json,omitempty"`
	RawFrameJSON  string          `json:"raw_frame_json,omitempty"`
	Expected      json.RawMessage `json:"expected"`
}

type askOfflineMCPDocument struct {
	Path            string    `json:"path"`
	Body            string    `json:"body"`
	EmbeddingMarker string    `json:"embedding_marker,omitempty"`
	Embedding       []float32 `json:"embedding,omitempty"`
}

type askOfflineMCPRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

func TestAskOfflineMCPOracle(t *testing.T) {
	input := askOfflineMCPFixtureCase{
		ID: "unscoped-offline-search-plan",
		Documents: []askOfflineMCPDocument{{
			Path: "notes/ask.md",
			Body: "---\ntitle: Ask note\ntags: [askscope]\n---\n\nThis note is selected by the tag plan.",
		}, {
			Path: "notebooks/notebook-fixture.md",
			Body: "---\ntype: notebook\ntitle: Fixture notebook\nnotebook_id: notebook-fixture\nsources:\n  - notes/ask.md\n---\n\n# Fixture notebook\n",
		}},
		Calls: []askOfflineMCPFixtureCall{
			{ID: "query-lowercase", ArgumentsJSON: `{"query":"tag:askscope"}`},
			{ID: "query-uppercase", ArgumentsJSON: `{"QUERY":"tag:askscope"}`},
			{ID: "query-title-case", ArgumentsJSON: `{"Query":"tag:askscope"}`},
			{ID: "query-null", ArgumentsJSON: `{"query":null}`},
			{ID: "query-null-keeps-prior-value", ArgumentsJSON: `{"query":"tag:askscope","QUERY":null}`},
			{ID: "arguments-null", ArgumentsJSON: `null`},
			{ID: "arguments-missing"},
			{ID: "query-wrong-type", ArgumentsJSON: `{"query":42}`},
			{ID: "query-whitespace", ArgumentsJSON: `{"query":"   "}`},
			{ID: "query-trailing-space", ArgumentsJSON: `{"query":"tag:askscope"}   `},
			{ID: "notebook-kelvin-case", ArgumentsJSON: `{"query":"tag:askscope","notebooK":"notebook-fixture"}`},
			{ID: "notebook-wrong-type", ArgumentsJSON: `{"query":"tag:askscope","notebook":7}`},
			{ID: "duplicate-folded-query-last-valid", ArgumentsJSON: `{"query":"","QUERY":"tag:askscope"}`},
			{ID: "duplicate-folded-query-last-empty", ArgumentsJSON: `{"QUERY":"tag:askscope","query":""}`},
			{ID: "duplicate-exact-query-last-valid", ArgumentsJSON: `{"query":"","query":"tag:askscope"}`},
			{ID: "duplicate-notebook-null-keeps-value", ArgumentsJSON: `{"query":"tag:askscope","notebook":"notebook-fixture","NOTEBOOK":null}`},
			{ID: "query-first-type-error-in-wire-order", ArgumentsJSON: `{"notebook":7,"query":42}`},
			{ID: "duplicate-envelope-arguments-last-write", RawParamsJSON: `{"name":"desk_ask","arguments":{"query":""},"arguments":{"query":"tag:askscope"}}`},
			{ID: "duplicate-top-level-params-last-write", RawFrameJSON: `{"jsonrpc":"2.0","id":0,"method":"tools/call","params":{"name":"desk_ask","arguments":{"query":""}},"params":{"name":"desk_ask","arguments":{"query":"tag:askscope"}}}`},
			{ID: "search-duplicate-folded-query-last-write", Tool: "desk_search", RawParamsJSON: `{"name":"desk_search","arguments":{"query":"","QUERY":"tag:askscope"}}`},
			{ID: "search-null-keeps-prior-query", Tool: "desk_search", RawParamsJSON: `{"name":"desk_search","arguments":{"query":"tag:askscope","QUERY":null}}`},
			{ID: "search-wrong-query-type", Tool: "desk_search", RawParamsJSON: `{"name":"desk_search","arguments":{"query":42}}`},
			{ID: "search-missing-arguments", Tool: "desk_search", RawParamsJSON: `{"name":"desk_search"}`},
		},
	}
	hybrid := askOfflineMCPFixtureCase{
		ID: "hybrid-semantic-top-three-fallback", EmbeddingDim: 3,
		Documents: []askOfflineMCPDocument{
			{Path: "semantic/top-1.md", Body: "Semantic source marker one.", EmbeddingMarker: "marker one", Embedding: []float32{1, 0, 0}},
			{Path: "semantic/top-2.md", Body: "Semantic source marker two.", EmbeddingMarker: "marker two", Embedding: []float32{0.8, 0.6, 0}},
			{Path: "semantic/top-3.md", Body: "Semantic source marker three.", EmbeddingMarker: "marker three", Embedding: []float32{0.6, 0.8, 0}},
			{Path: "semantic/not-top-3.md", Body: "Semantic source marker four.", EmbeddingMarker: "marker four", Embedding: []float32{0, 1, 0}},
		},
		Calls: []askOfflineMCPFixtureCall{{ID: "semantic-query", ArgumentsJSON: `{"query":"violet cosmic wavelength"}`}},
	}
	got := observeAskOfflineMCPCase(t, input)
	assertAskOfflineMCPOracleBehavior(t, got)
	notebook := askOfflineMCPFixtureCase{
		ID: "notebook-scoped-offline-search",
		Documents: []askOfflineMCPDocument{
			{Path: "notebooks/notebook-fixture.md", Body: "---\ntype: notebook\ntitle: Fixture notebook\nnotebook_id: notebook-fixture\nsources:\n  - notes/unmatched.md\n  - notes/missing.md\n  - notes/matched.md\n---\n\n# Fixture notebook\n"},
			{Path: "notebooks/empty.md", Body: "---\ntype: notebook\ntitle: Empty notebook\nnotebook_id: empty\nsources: []\n---\n\n# Empty notebook\n"},
			{Path: "notes/matched.md", Body: "---\ntitle: Matched source\n---\n\nThe needle is visible only inside this notebook source."},
			{Path: "notes/unmatched.md", Body: "---\ntitle: Fallback source\n---\n\nFallback-only passage for conceptual questions."},
			{Path: "notes/outside.md", Body: "---\ntitle: Outside source\n---\n\nThe needle outside the selected notebook must not appear."},
		},
		Calls: []askOfflineMCPFixtureCall{
			{ID: "scoped-hit-plus-unmatched-fallback", ArgumentsJSON: `{"query":"needle","notebook":"notebook-fixture"}`},
			{ID: "scoped-conceptual-fallback", ArgumentsJSON: `{"query":"summarize these sources","notebook":"notebook-fixture"}`},
			{ID: "scoped-path-reference", ArgumentsJSON: `{"query":"needle","notebook":"notebooks/notebook-fixture.md"}`},
			{ID: "scoped-whitespace-query-fallback", ArgumentsJSON: `{"query":"   ","notebook":"notebook-fixture"}`},
			{ID: "empty-notebook-source-set", ArgumentsJSON: `{"query":"needle","notebook":"empty"}`},
			{ID: "missing-notebook", ArgumentsJSON: `{"query":"needle","notebook":"does-not-exist"}`},
		},
	}
	gotNotebook := observeAskOfflineMCPCase(t, notebook)
	assertNotebookAskOracleBehavior(t, gotNotebook)
	hybridGot := observeAskOfflineMCPCase(t, hybrid)
	assertAskHybridMCPOracleBehavior(t, hybridGot)
	current := askOfflineMCPFixture{SchemaVersion: 1, Cases: []askOfflineMCPFixtureCase{got, gotNotebook, hybridGot}}
	if os.Getenv("PORT_GENERATE") == "1" {
		encoded, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(askOfflineMCPFixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(askOfflineMCPFixturePath)
	if err != nil {
		t.Fatalf("read Go-generated offline Ask MCP fixture (generate with PORT_GENERATE=1): %v", err)
	}
	var want askOfflineMCPFixture
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode offline Ask MCP fixture: %v", err)
	}
	if want.SchemaVersion != current.SchemaVersion || !sameAskOfflineMCPCases(want.Cases, current.Cases) {
		gotJSON, _ := json.MarshalIndent(current, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("offline Ask MCP fixture is stale; regenerate with PORT_GENERATE=1 go test ./internal/mcp -run '^TestAskOfflineMCPOracle$'\ncurrent: %s\nfixture: %s", gotJSON, wantJSON)
	}
}

func sameAskOfflineMCPCases(left, right []askOfflineMCPFixtureCase) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		a, b := left[i], right[i]
		if a.ID != b.ID || !reflect.DeepEqual(a.Documents, b.Documents) ||
			a.EmbeddingDim != b.EmbeddingDim ||
			!sameAskMCPJSON(a.ExpectedTool, b.ExpectedTool) ||
			len(a.Calls) != len(b.Calls) || !sameAskMCPRequests(a.ProviderRequests, b.ProviderRequests) {
			return false
		}
		for j := range a.Calls {
			if a.Calls[j].ID != b.Calls[j].ID || a.Calls[j].Tool != b.Calls[j].Tool || a.Calls[j].ArgumentsJSON != b.Calls[j].ArgumentsJSON || a.Calls[j].RawParamsJSON != b.Calls[j].RawParamsJSON || a.Calls[j].RawFrameJSON != b.Calls[j].RawFrameJSON ||
				!sameAskMCPJSON(a.Calls[j].Expected, b.Calls[j].Expected) {
				return false
			}
		}
	}
	return true
}

func sameAskMCPRequests(left, right []askOfflineMCPRequest) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Method != right[index].Method || left[index].Path != right[index].Path || !sameAskMCPJSON(left[index].Body, right[index].Body) {
			return false
		}
	}
	return true
}

func assertAskHybridMCPOracleBehavior(t *testing.T, fixture askOfflineMCPFixtureCase) {
	t.Helper()
	if len(fixture.Calls) != 1 || fixture.Calls[0].ID != "semantic-query" {
		t.Fatalf("hybrid Ask fixture call shape changed: %+v", fixture.Calls)
	}
	text := askMCPText(t, fixture.Calls[0].Expected)
	if strings.Count(text, "- [[") != 3 {
		t.Fatalf("Go hybrid Ask fallback citations = %d, want exactly 3: %q", strings.Count(text, "- [["), text)
	}
	for _, path := range []string{"semantic/top-1.md", "semantic/top-2.md", "semantic/top-3.md"} {
		if !strings.Contains(text, path) {
			t.Fatalf("Go Ask fallback missed semantic top-three source %q: %q", path, text)
		}
	}
	if strings.Contains(text, "not-top-3.md") || strings.Contains(text, "violet cosmic wavelength") {
		t.Fatalf("Go Ask included a lower-ranked or lexical-only candidate: %q", text)
	}
	if len(fixture.ProviderRequests) != 1 || fixture.ProviderRequests[0].Method != http.MethodPost || fixture.ProviderRequests[0].Path != "/v1/embeddings" {
		t.Fatalf("actual Go MCP Ask embedding trace = %+v, want one query embedding request", fixture.ProviderRequests)
	}
	var request struct {
		Input []string `json:"input"`
	}
	if err := json.Unmarshal(fixture.ProviderRequests[0].Body, &request); err != nil || len(request.Input) != 1 || request.Input[0] != "violet cosmic wavelength" {
		t.Fatalf("actual Go Ask embedding request body = %s (err %v)", fixture.ProviderRequests[0].Body, err)
	}
}

func sameAskMCPJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func assertAskOfflineMCPOracleBehavior(t *testing.T, fixture askOfflineMCPFixtureCase) {
	t.Helper()
	byID := make(map[string]askOfflineMCPFixtureCall, len(fixture.Calls))
	for _, call := range fixture.Calls {
		byID[call.ID] = call
	}
	for _, id := range []string{"query-lowercase", "query-uppercase", "query-title-case", "query-null-keeps-prior-value", "query-trailing-space", "duplicate-folded-query-last-valid", "duplicate-exact-query-last-valid", "duplicate-envelope-arguments-last-write"} {
		if !sameAskMCPResult(t, byID["query-lowercase"].Expected, byID[id].Expected) {
			t.Fatalf("Go Query field matching differs for %s", id)
		}
	}
	for _, id := range []string{"query-null", "arguments-null", "duplicate-folded-query-last-empty"} {
		if got := askMCPText(t, byID[id].Expected); got != "query is required" {
			t.Fatalf("Go null/missing Query result for %s = %q", id, got)
		}
	}
	if got := askMCPText(t, byID["arguments-missing"].Expected); got != "unexpected end of JSON input" {
		t.Fatalf("Go missing Arguments result = %q", got)
	}
	if got := askMCPText(t, byID["query-wrong-type"].Expected); got != "json: cannot unmarshal number into Go struct field .query of type string" {
		t.Fatalf("Go wrong Query type error = %q", got)
	}
	if got := askMCPText(t, byID["notebook-wrong-type"].Expected); got != "json: cannot unmarshal number into Go struct field .notebook of type string" {
		t.Fatalf("Go wrong Notebook type error = %q", got)
	}
	if got := askMCPText(t, byID["query-first-type-error-in-wire-order"].Expected); got != "json: cannot unmarshal number into Go struct field .notebook of type string" {
		t.Fatalf("Go decoder did not report the first wrong field in wire order: %q", got)
	}
	for _, id := range []string{"search-duplicate-folded-query-last-write", "search-null-keeps-prior-query"} {
		if result := byID[id].Expected; askMCPIsError(t, result) {
			t.Fatalf("Go desk_search %s unexpectedly returned an error", id)
		}
	}
	if got := askMCPText(t, byID["search-wrong-query-type"].Expected); got != "json: cannot unmarshal number into Go struct field .query of type string" {
		t.Fatalf("Go desk_search wrong Query type error = %q", got)
	}
	if got := askMCPText(t, byID["search-missing-arguments"].Expected); got != "unexpected end of JSON input" {
		t.Fatalf("Go desk_search missing Arguments error = %q", got)
	}
	if !sameAskMCPResult(t, byID["query-lowercase"].Expected, byID["notebook-kelvin-case"].Expected) ||
		!sameAskMCPResult(t, byID["notebook-kelvin-case"].Expected, byID["duplicate-notebook-null-keeps-value"].Expected) {
		t.Fatalf("Go null duplicate did not preserve the earlier Notebook value:\nunscoped: %s\nnotebook: %s\nnull duplicate: %s", byID["query-lowercase"].Expected, byID["notebook-kelvin-case"].Expected, byID["duplicate-notebook-null-keeps-value"].Expected)
	}
	if got := askMCPText(t, byID["query-whitespace"].Expected); !strings.HasSuffix(got, "Here are the most relevant search results from your vault:\\n\\n\"}") {
		t.Fatalf("Go whitespace query should succeed with an empty-result fallback, got %q", got)
	}
}

func assertNotebookAskOracleBehavior(t *testing.T, fixture askOfflineMCPFixtureCase) {
	t.Helper()
	byID := make(map[string]askOfflineMCPFixtureCall, len(fixture.Calls))
	for _, call := range fixture.Calls {
		byID[call.ID] = call
	}
	matched := askMCPText(t, byID["scoped-hit-plus-unmatched-fallback"].Expected)
	for _, path := range []string{"notes/matched.md", "notes/unmatched.md"} {
		if !strings.Contains(matched, "[["+path+"]]") {
			t.Fatalf("scoped match/fallback omitted %s: %q", path, matched)
		}
	}
	for _, path := range []string{"notes/outside.md", "notes/missing.md"} {
		if strings.Contains(matched, path) {
			t.Fatalf("scoped match/fallback leaked or retained %s: %q", path, matched)
		}
	}
	conceptual := askMCPText(t, byID["scoped-conceptual-fallback"].Expected)
	if strings.Count(conceptual, "[[") != 2 {
		t.Fatalf("conceptual scoped fallback citations = %d, want 2: %q", strings.Count(conceptual, "[["), conceptual)
	}
	if !sameAskMCPResult(t, byID["scoped-hit-plus-unmatched-fallback"].Expected, byID["scoped-path-reference"].Expected) {
		t.Fatal("notebook path reference differed from notebook id lookup")
	}
	if got := askMCPText(t, byID["empty-notebook-source-set"].Expected); strings.Contains(got, "[[") {
		t.Fatalf("empty notebook scope emitted citations: %q", got)
	}
	if !askMCPIsError(t, byID["missing-notebook"].Expected) || askMCPText(t, byID["missing-notebook"].Expected) != "notebook not found" {
		t.Fatalf("missing notebook result = %s", byID["missing-notebook"].Expected)
	}
}

func sameAskMCPResult(t *testing.T, left, right json.RawMessage) bool {
	t.Helper()
	var leftFrame, rightFrame map[string]json.RawMessage
	if err := json.Unmarshal(left, &leftFrame); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(right, &rightFrame); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(leftFrame["result"], rightFrame["result"])
}

func askMCPText(t *testing.T, frame json.RawMessage) string {
	t.Helper()
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode Go MCP result: %v", err)
	}
	if len(response.Result.Content) != 1 {
		t.Fatalf("Go MCP content blocks = %d, want 1: %s", len(response.Result.Content), frame)
	}
	return response.Result.Content[0].Text
}

func askMCPIsError(t *testing.T, frame json.RawMessage) bool {
	t.Helper()
	var response struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode Go MCP error flag: %v", err)
	}
	return response.Result.IsError
}

func observeAskOfflineMCPCase(t *testing.T, input askOfflineMCPFixtureCase) askOfflineMCPFixtureCase {
	t.Helper()
	home := canonicalOracleTempDir(t)
	vaultRoot := filepath.Join(home, "vault")
	if err := os.MkdirAll(vaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("TMPDIR", filepath.Join(home, "tmp"))
	t.Setenv("TMP", filepath.Join(home, "tmp"))
	t.Setenv("TEMP", filepath.Join(home, "tmp"))
	for _, name := range []string{"SYMDESK_OLLAMA_URL", "SYMDESK_LLM_PROVIDER", "SYMDESK_LLM_MODEL", "SYMDESK_LLM_API_KEY", "OLLAMA_HOST"} {
		t.Setenv(name, "")
	}
	for _, path := range []string{
		filepath.Join(home, "config"),
		filepath.Join(home, "cache"),
		filepath.Join(home, "data"),
		filepath.Join(home, "tmp"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, document := range input.Documents {
		path := filepath.Join(vaultRoot, filepath.FromSlash(document.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(document.Body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var requestMu sync.Mutex
	var providerRequests []askOfflineMCPRequest
	ollamaURL := ""
	var fakeProvider *httptest.Server
	if input.EmbeddingDim > 0 {
		fakeProvider = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read Ask fixture embedding request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			requestMu.Lock()
			providerRequests = append(providerRequests, askOfflineMCPRequest{Method: r.Method, Path: r.URL.RequestURI(), Body: append(json.RawMessage(nil), body...)})
			requestMu.Unlock()
			var request struct {
				Input []string `json:"input"`
			}
			if err := json.Unmarshal(body, &request); err != nil || len(request.Input) == 0 {
				t.Errorf("decode Ask fixture embedding request %s (err %v)", body, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			data := make([]map[string][]float32, 0, len(request.Input))
			for _, text := range request.Input {
				var vector []float32
				if text == "violet cosmic wavelength" {
					vector = []float32{1, 0, 0}
				} else if strings.Contains(text, "__SYMDESK_SEARCH_METADATA_START__") {
					vector = []float32{0, 1, 0}
				} else {
					for _, document := range input.Documents {
						base := strings.TrimSuffix(filepath.Base(document.Path), filepath.Ext(document.Path))
						if document.EmbeddingMarker != "" && (strings.Contains(text, document.EmbeddingMarker) || strings.Contains(text, base)) {
							vector = document.Embedding
							break
						}
					}
				}
				if len(vector) != input.EmbeddingDim {
					t.Errorf("no configured embedding vector for %q: %v", text, vector)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				data = append(data, map[string][]float32{"embedding": vector})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		}))
		defer fakeProvider.Close()
		ollamaURL = fakeProvider.URL + "/api/embeddings"
		indexPath := filepath.Join(home, "retrieval.db")
		configDir := filepath.Join(home, ".config", "symseek")
		if err := os.MkdirAll(configDir, 0o700); err != nil {
			t.Fatal(err)
		}
		configText := fmt.Sprintf("index_path = %q\nollama_url = %q\nmodel = %q\nembedding_dim = %d\ntimeout_seconds = 5\nretry_count = 0\n", indexPath, ollamaURL, "fixture-model", input.EmbeddingDim)
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configText), 0o600); err != nil {
			t.Fatal(err)
		}
		index, err := retrieval.OpenForVault(vaultRoot)
		if err != nil {
			t.Fatal(err)
		}
		for _, document := range input.Documents {
			if err := index.Index(filepath.Join(vaultRoot, filepath.FromSlash(document.Path)), ""); err != nil {
				_ = index.Close()
				t.Fatalf("index Ask hybrid fixture document %q: %v", document.Path, err)
			}
		}
		if err := index.Close(); err != nil {
			t.Fatal(err)
		}
		requestMu.Lock()
		providerRequests = nil
		requestMu.Unlock()
	}
	db, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshIndex(vaultRoot); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	factory := func() (*service.Service, *sidecar.DB, error) {
		opened, err := sidecar.OpenForVault(vaultRoot)
		if err != nil {
			return nil, nil, err
		}
		return service.New(vaultRoot, opened), opened, nil
	}
	entry, ok := tools.NewRegistry(tools.RegistryOptions{
		Config: &config.Config{Vault: vaultRoot}, GetService: factory, AllowWrite: false,
	}).Lookup("desk_ask")
	if !ok {
		_ = db.Close()
		t.Fatal("canonical tools registry has no desk_ask entry")
	}
	server := mcpserver.New("symdesk", "test-version")
	server.RegisterTool(adaptTool(entry))
	searchEntry, ok := tools.NewRegistry(tools.RegistryOptions{
		Config: &config.Config{Vault: vaultRoot}, GetService: factory, AllowWrite: false,
	}).Lookup("desk_search")
	if !ok {
		_ = db.Close()
		t.Fatal("canonical tools registry has no desk_search entry")
	}
	server.RegisterTool(adaptTool(searchEntry))
	listRequest := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var output bytes.Buffer
	output.Write(listRequest)
	output.WriteByte('\n')
	for index := range input.Calls {
		call := &input.Calls[index]
		if call.RawFrameJSON != "" {
			request := strings.Replace(call.RawFrameJSON, `"id":0`, fmt.Sprintf(`"id":%d`, index+2), 1)
			output.WriteString(request)
			output.WriteByte('\n')
			continue
		}
		var params json.RawMessage
		if call.RawParamsJSON != "" {
			params = json.RawMessage(call.RawParamsJSON)
		} else {
			paramsValue := struct {
				Name      string           `json:"name"`
				Arguments *json.RawMessage `json:"arguments,omitempty"`
			}{Name: call.Tool}
			if paramsValue.Name == "" {
				paramsValue.Name = "desk_ask"
			}
			if call.ArgumentsJSON != "" {
				arguments := json.RawMessage(call.ArgumentsJSON)
				paramsValue.Arguments = &arguments
			}
			encodedParams, err := json.Marshal(paramsValue)
			if err != nil {
				t.Fatalf("marshal Ask params for %q: %v", call.ID, err)
			}
			params = encodedParams
		}
		request, err := json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}{JSONRPC: "2.0", ID: index + 2, Method: "tools/call", Params: params})
		if err != nil {
			_ = db.Close()
			t.Fatalf("marshal actual Go MCP call %q: %v", call.ID, err)
		}
		output.Write(request)
		output.WriteByte('\n')
	}
	var actual bytes.Buffer
	if err := server.ServeIO(context.Background(), &output, &actual); err != nil {
		_ = db.Close()
		t.Fatalf("run actual Go MCP ServeIO: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	frames := bytes.Split(bytes.TrimSpace(actual.Bytes()), []byte{'\n'})
	if len(frames) != len(input.Calls)+1 {
		t.Fatalf("Go MCP returned %d frames, want %d: %q", len(frames), len(input.Calls)+1, actual.String())
	}
	byID := make(map[int][]byte, len(frames))
	for _, frame := range frames {
		var response struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(frame, &response); err != nil {
			t.Fatalf("decode Go MCP response id: %v: %s", err, frame)
		}
		byID[response.ID] = frame
	}
	input.ExpectedTool = normalizeAskToolFrame(t, byID[1])
	for index := range input.Calls {
		input.Calls[index].Expected = normalizeAskMCPFrame(t, byID[index+2], vaultRoot)
	}
	requestMu.Lock()
	for _, request := range providerRequests {
		request.Body = normalizeAskMCPRequestBody(t, request.Body, vaultRoot)
		input.ProviderRequests = append(input.ProviderRequests, request)
	}
	requestMu.Unlock()
	_ = ollamaURL
	return input
}

func normalizeAskMCPRequestBody(t *testing.T, body json.RawMessage, vaultRoot string) json.RawMessage {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("decode captured Go Ask provider request: %v: %s", err, body)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return json.RawMessage(encoded)
}

func normalizeAskToolFrame(t *testing.T, frame []byte) json.RawMessage {
	t.Helper()
	var response struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode Go tools/list frame: %v: %s", err, frame)
	}
	for _, tool := range response.Result.Tools {
		var identity struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(tool, &identity); err != nil {
			t.Fatal(err)
		}
		if identity.Name == "desk_ask" {
			return tool
		}
	}
	t.Fatalf("Go ask catalog response has no desk_ask entry: %s", frame)
	return nil
}

func normalizeAskMCPFrame(t *testing.T, frame []byte, vaultRoot string) json.RawMessage {
	t.Helper()
	var value any
	if err := json.Unmarshal(frame, &value); err != nil {
		t.Fatalf("decode Go Ask MCP frame: %v: %s", err, frame)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if result, ok := response["result"].(map[string]any); ok {
		if content, ok := result["content"].([]any); ok {
			for _, item := range content {
				if block, ok := item.(map[string]any); ok {
					if text, ok := block["text"].(string); ok {
						block["text"] = strings.ReplaceAll(text, vaultRoot, "$VAULT")
					}
				}
			}
		}
	}
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
