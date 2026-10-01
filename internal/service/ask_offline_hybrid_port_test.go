package service

import (
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

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

const askOfflineFixturePath = "../../testdata/port/cli/ask-offline.json"

type askOfflineFixture struct {
	SchemaVersion int                     `json:"schema_version"`
	Cases         []askOfflineFixtureCase `json:"cases"`
}

type askOfflineFixtureCase struct {
	ID             string               `json:"id"`
	Query          string               `json:"query"`
	IndexDocuments bool                 `json:"index_documents"`
	Documents      []askOfflineDocument `json:"documents"`
	Sources        []string             `json:"sources,omitempty"`
	Expected       []ai.AIEvent         `json:"expected"`
	Requests       []askOfflineRequest  `json:"requests"`
}

type askOfflineDocument struct {
	Path   string `json:"path"`
	Body   string `json:"body"`
	Source string `json:"source,omitempty"`
}

type askOfflineRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body"`
}

type askOfflineCapturedRequest struct {
	method string
	path   string
	body   json.RawMessage
}

func TestAskHybridOfflineOracle(t *testing.T) {
	cases := askOfflineCases()
	fixture := askOfflineFixture{SchemaVersion: 1, Cases: make([]askOfflineFixtureCase, 0, len(cases))}
	for _, input := range cases {
		t.Run(input.ID, func(t *testing.T) {
			observed := observeAskOfflineCase(t, input)
			fixture.Cases = append(fixture.Cases, observed)
		})
	}
	if os.Getenv("PORT_GENERATE") == "1" {
		encoded, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(askOfflineFixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(askOfflineFixturePath)
	if err != nil {
		t.Fatalf("read Go-generated Ask fixture (generate with PORT_GENERATE=1): %v", err)
	}
	var want askOfflineFixture
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode Ask fixture: %v", err)
	}
	if want.SchemaVersion != fixture.SchemaVersion || len(want.Cases) != len(fixture.Cases) {
		t.Fatalf("Ask fixture header = v%d/%d cases, want v%d/%d", want.SchemaVersion, len(want.Cases), fixture.SchemaVersion, len(fixture.Cases))
	}
	for index := range fixture.Cases {
		got, _ := json.Marshal(fixture.Cases[index])
		previous, _ := json.Marshal(want.Cases[index])
		if string(got) != string(previous) {
			t.Fatalf("Ask fixture case %q is stale; regenerate with PORT_GENERATE=1 go test ./internal/service -run '^TestAskHybridOfflineOracle$'\ncurrent: %s\nfixture: %s", fixture.Cases[index].ID, got, previous)
		}
	}
}

func askOfflineCases() []askOfflineFixtureCase {
	return []askOfflineFixtureCase{
		{
			ID: "hybrid-multi-source-more-than-three-fallback-links", Query: "ask offline needle", IndexDocuments: true,
			Documents: []askOfflineDocument{
				{Path: "one.md", Body: "ask offline needle first note"},
				{Path: "two.md", Body: "ask offline needle second note ask offline needle"},
				{Path: "three.md", Body: "ask offline needle third note ask offline needle ask offline needle"},
				{Path: "four.md", Body: "ask offline needle fourth note ask offline needle ask offline needle ask offline needle"},
				{Path: "five.md", Body: "ask offline needle fifth note ask offline needle ask offline needle ask offline needle ask offline needle"},
				{Path: "guide.md", Source: "guide", Body: strings.Repeat("ask offline needle registered guide ", 8)},
			}, Sources: []string{"guide"},
		},
		{
			ID: "hybrid-semantic-query-can-return-nonlexical-hit", Query: "ask offline absent phrase", IndexDocuments: true,
			Documents: []askOfflineDocument{{Path: "semantic.md", Body: "The indexed passage has no matching query terms."}},
		},
		{
			ID: "registered-external-source-only", Query: "ask external-only needle", IndexDocuments: true,
			Documents: []askOfflineDocument{{Path: "guide.md", Source: "guide", Body: "ask external-only needle from a registered source."}},
			Sources:   []string{"guide"},
		},
		{
			ID: "tag-plan-zero-embedding-requests", Query: "tag:askscope", IndexDocuments: false,
			Documents: []askOfflineDocument{{Path: "tagged.md", Body: "---\ntags: [askscope]\n---\n\nThe scoped note is retrieved without provider access."}},
		},
		{
			ID: "hybrid-empty-results", Query: "ask offline absent phrase", IndexDocuments: false,
		},
		{
			ID: "retrieval-unavailable-sidecar-fallback", Query: "ask sidecar-only needle", IndexDocuments: false,
			Documents: []askOfflineDocument{{Path: "sidecar.md", Body: "# Sidecar Only\n\nThe ask sidecar-only needle is indexed only in FTS."}},
		},
		{
			ID: "empty-query-zero-provider-requests", Query: "   ", IndexDocuments: false,
		},
	}
}

func observeAskOfflineCase(t *testing.T, input askOfflineFixtureCase) askOfflineFixtureCase {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("TMPDIR", filepath.Join(home, "tmp"))
	t.Setenv("TMP", filepath.Join(home, "tmp"))
	t.Setenv("TEMP", filepath.Join(home, "tmp"))
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	for _, name := range []string{"SYMDESK_LLM_PROVIDER", "SYMDESK_LLM_MODEL", "SYMDESK_LLM_API_KEY", "OLLAMA_HOST"} {
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
	vaultRoot := filepath.Join(home, "vault")
	if err := os.MkdirAll(vaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	vaultRoot, err := filepath.EvalSymlinks(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(home, "data", "retrieval.db")

	var requestMu sync.Mutex
	requests := []askOfflineCapturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read local embedding request: %v", err)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected local model request %s %s; Ask must remain offline", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		requestMu.Lock()
		requests = append(requests, askOfflineCapturedRequest{method: r.Method, path: r.URL.RequestURI(), body: append(json.RawMessage(nil), body...)})
		requestMu.Unlock()
		var request struct {
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Errorf("decode embedding request: %v", err)
		}
		response := struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}{}
		for range request.Input {
			response.Data = append(response.Data, struct {
				Embedding []float32 `json:"embedding"`
			}{Embedding: []float32{1, 0, 0}})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	writeRetrievalConfig := func() {
		t.Helper()
		config := fmt.Sprintf("index_path = %q\nollama_url = %q\nmodel = \"ask-fixture-model\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\n", indexPath, server.URL+"/api/embeddings")
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRetrievalConfig()

	sourcePaths := map[string]string{}
	for _, source := range input.Sources {
		path := filepath.Join(home, "external", filepath.FromSlash(source))
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		sourcePaths[source] = path
	}
	documentPaths := make([]string, 0, len(input.Documents))
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
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			t.Fatal(err)
		}
		documentPaths = append(documentPaths, path)
	}
	if input.IndexDocuments {
		client, err := retrieval.OpenForVault(vaultRoot)
		if err != nil {
			t.Fatalf("open fixture retrieval client: %v", err)
		}
		for _, path := range documentPaths {
			if err := client.Index(path, ""); err != nil {
				_ = client.Close()
				t.Fatalf("index fixture document: %v", err)
			}
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
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
	db, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshIndex(vaultRoot); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	requestMu.Lock()
	requests = nil
	requestMu.Unlock()

	svc := New(vaultRoot, db)
	defer func() { _ = svc.Close(); _ = db.Close() }()
	out := make(chan interface{})
	go svc.Ask(context.Background(), input.Query, out)
	input.Expected = nil
	for event := range out {
		actual, ok := event.(ai.AIEvent)
		if !ok {
			t.Fatalf("Ask emitted %T, want ai.AIEvent", event)
		}
		switch actual.Type {
		case ai.AIEventCitation:
			actual.Path = normalizeAskPath(actual.Path, vaultRoot, sourcePaths)
		case ai.AIEventAnswer:
			for source, sourcePath := range sourcePaths {
				prefix, err := filepath.Rel(".", sourcePath)
				if err == nil {
					actual.Text = strings.ReplaceAll(actual.Text, "[["+filepath.ToSlash(prefix)+"/", "[[@source/"+source+"/")
				}
				actual.Text = strings.ReplaceAll(actual.Text, "[["+filepath.ToSlash(sourcePath)+"/", "[[@source/"+source+"/")
			}
			prefix, err := filepath.Rel(".", vaultRoot)
			if err == nil {
				actual.Text = strings.ReplaceAll(actual.Text, "[["+filepath.ToSlash(prefix)+"/", "[[")
			}
		}
		input.Expected = append(input.Expected, actual)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	input.Requests = make([]askOfflineRequest, 0, len(requests))
	for _, request := range requests {
		input.Requests = append(input.Requests, askOfflineRequest{Method: request.method, Path: request.path, Body: request.body})
	}
	if input.IndexDocuments && len(input.Requests) == 0 {
		t.Fatal("indexed plain-query Ask case made no local embedding request")
	}
	if strings.TrimSpace(input.Query) == "" && len(input.Requests) != 0 {
		t.Fatalf("empty query contacted embedding endpoint %d times", len(input.Requests))
	}
	for _, request := range input.Requests {
		if strings.Contains(request.Path, "/api/chat") {
			t.Fatal("offline Ask unexpectedly contacted a chat endpoint")
		}
	}
	citations := 0
	for _, event := range input.Expected {
		if event.Type == ai.AIEventCitation {
			citations++
		}
	}
	if input.ID == "hybrid-multi-source-more-than-three-fallback-links" && citations <= 3 {
		t.Fatalf("multi-source Ask oracle produced %d citations, want more than three", citations)
	}
	if input.ID == "hybrid-multi-source-more-than-three-fallback-links" {
		links := 0
		for _, event := range input.Expected {
			if event.Type == ai.AIEventAnswer && strings.HasPrefix(event.Text, "- [[") {
				links++
			}
		}
		if links != 3 {
			t.Fatalf("multi-source Ask oracle fallback links=%d, want 3", links)
		}
	}
	if input.ID == "registered-external-source-only" {
		for _, event := range input.Expected {
			if event.Type == ai.AIEventCitation && strings.HasPrefix(event.Path, "@source/guide/") {
				return input
			}
		}
		t.Fatal("registered external-only Ask oracle produced no source citation")
	}
	if input.ID == "tag-plan-zero-embedding-requests" && len(input.Requests) != 0 {
		t.Fatalf("tag-scoped Ask oracle made %d embedding requests, want zero", len(input.Requests))
	}
	if input.ID == "hybrid-empty-results" && citations != 0 {
		t.Fatalf("empty Ask oracle produced %d citations, want none", citations)
	}
	return input
}

func normalizeAskPath(path, vaultRoot string, sources map[string]string) string {
	candidate := path
	if !filepath.IsAbs(candidate) {
		if absolute, err := filepath.Abs(candidate); err == nil {
			candidate = absolute
		}
	}
	if canonical, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = canonical
	}
	for source, root := range sources {
		if withinSearchCLIPath(candidate, root) {
			if relative, err := filepath.Rel(root, candidate); err == nil {
				return "@source/" + source + "/" + filepath.ToSlash(relative)
			}
		}
	}
	if withinSearchCLIPath(candidate, vaultRoot) {
		if relative, err := filepath.Rel(vaultRoot, candidate); err == nil {
			return "@vault/" + filepath.ToSlash(relative)
		}
	}
	return filepath.ToSlash(path)
}
