package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

type indexRetrievalProcessFile struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

type indexRetrievalProcessRequest struct {
	Method      string          `json:"method"`
	Path        string          `json:"path"`
	ContentType string          `json:"content_type"`
	Body        json.RawMessage `json:"body"`
}

type indexRetrievalProcessDocument struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

type indexRetrievalProcessChunk struct {
	DocumentPath     string `json:"document_path"`
	ChunkIndex       int    `json:"chunk_index"`
	Content          string `json:"content"`
	Hash             string `json:"hash"`
	Embedding        string `json:"embedding"`
	Norm             string `json:"norm"`
	BinarySignature  string `json:"binary_signature"`
	SignatureType    string `json:"binary_signature_type"`
	Dim              int    `json:"dim"`
	Model            string `json:"model"`
	CharStart        *int   `json:"char_start"`
	CharEnd          *int   `json:"char_end"`
	EmbeddingPending int    `json:"embedding_pending"`
	AnchorKind       string `json:"anchor_kind"`
	AnchorValue      string `json:"anchor_value"`
	ContentNorm      string `json:"content_norm"`
}

type indexRetrievalProcessState struct {
	Documents    []indexRetrievalProcessDocument `json:"documents"`
	Chunks       []indexRetrievalProcessChunk    `json:"chunks"`
	Generation   int64                           `json:"generation"`
	IndexStorage int                             `json:"index_storage"`
	Lifecycle    map[string]string               `json:"lifecycle"`
	Reasons      map[string]string               `json:"lifecycle_reasons"`
}

type indexRetrievalProcessStep struct {
	ID       string                         `json:"id"`
	Writes   []indexRetrievalProcessFile    `json:"writes,omitempty"`
	Args     []string                       `json:"args"`
	Result   indexBuildProcessResult        `json:"result"`
	Requests []indexRetrievalProcessRequest `json:"requests"`
	State    indexRetrievalProcessState     `json:"state"`
}

type indexRetrievalProcessFixture struct {
	SchemaVersion int                         `json:"schema_version"`
	Config        string                      `json:"config"`
	Files         []indexRetrievalProcessFile `json:"files"`
	LargeFile     string                      `json:"large_file"`
	LargeBytes    int                         `json:"large_bytes"`
	Steps         []indexRetrievalProcessStep `json:"steps"`
}

// The fake provider is deterministic per input text so batch and per-text
// requests agree. Inputs containing indexRetrievalFailMarker fail with a
// transient 500 to drive Go's batch-to-per-text fallback and pending chunks.
const indexRetrievalFailMarker = "provider-fail"

func indexRetrievalVector(text string) []float32 {
	return []float32{float32(len(text)%97) / 97, 0.5, -0.25}
}

// TestIndexRetrievalProcessPortFixture runs the real Go `symdesk index`
// process and records the hybrid retrieval database it writes per indexed
// Markdown document: rows, embeddings, pending fallbacks, generation, the
// lifecycle effect of a retrieval failure, and every provider request.
func TestIndexRetrievalProcessPortFixture(t *testing.T) {
	fixture := observeIndexRetrievalProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/index-retrieval-process.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("index retrieval fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./cmd/symdesk -run '^TestIndexRetrievalProcessPortFixture$'")
	}
}

func observeIndexRetrievalProcess(t *testing.T) indexRetrievalProcessFixture {
	t.Helper()
	root := t.TempDir()
	home, cwd, dataHome, tempRoot, vault := filepath.Join(root, "home"), filepath.Join(root, "cwd"), filepath.Join(root, "data"), filepath.Join(root, "tmp"), filepath.Join(root, "vault")
	for _, directory := range []string{home, cwd, dataHome, tempRoot, filepath.Join(vault, "nested")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", dataHome)
	fixture := indexRetrievalProcessFixture{SchemaVersion: 1, LargeFile: "big.md", LargeBytes: 10<<20 + 1}
	fixture.Files = []indexRetrievalProcessFile{
		{Path: "alpha.md", Body: "---\ntitle: Alpha Oracle\ntags: [retrieval, oracle]\ntype: note\n---\n# Alpha\n\nIntro text.\n\n## Details\n\n" + strings.Repeat("alpha retrieval contract text ", 50) + "\n"},
		{Path: "nested/beta.md", Body: "plain beta document without headings\nsecond line\n"},
		{Path: "dup.md", Body: "# Same\n\nrepeat body\n\n# Same\n\nrepeat body\n"},
		{Path: "failing.md", Body: "# Failing\n\n" + indexRetrievalFailMarker + " marker text\n\n# Healthy\n\nhealthy section text\n"},
		{Path: "empty.md", Body: ""},
	}
	for _, file := range fixture.Files {
		if err := os.WriteFile(filepath.Join(vault, filepath.FromSlash(file.Path)), []byte(file.Body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// One byte over the retrieval parser limit: the sidecar indexes it while
	// the hybrid write fails, so Go records a failed lifecycle row.
	if err := os.WriteFile(filepath.Join(vault, fixture.LargeFile), []byte("# Big\n\n"+strings.Repeat("x", fixture.LargeBytes-len("# Big\n\n"))), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var requests []indexRetrievalProcessRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var raw bytes.Buffer
		if _, err := raw.ReadFrom(r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		mu.Lock()
		requests = append(requests, indexRetrievalProcessRequest{Method: r.Method, Path: r.URL.RequestURI(), ContentType: r.Header.Get("Content-Type"), Body: append(json.RawMessage(nil), raw.Bytes()...)})
		mu.Unlock()
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.Unmarshal(raw.Bytes(), &body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		data := make([]map[string]any, 0, len(body.Input))
		for _, input := range body.Input {
			if strings.Contains(input, indexRetrievalFailMarker) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"fixture provider failure"}`))
				return
			}
			data = append(data, map[string]any{"embedding": indexRetrievalVector(input)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.Config = "index_path = $INDEX_PATH\nollama_url = \"$SERVER/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nmodel = \"fixture\"\n"
	config := strings.NewReplacer("$INDEX_PATH", strconv.Quote(filepath.Join(dataHome, "retrieval.db")), "$SERVER", server.URL).Replace(fixture.Config)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := buildIndexRetrievalBinary(t, root)
	environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempRoot, "TMP=" + tempRoot, "TEMP=" + tempRoot, "LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1"}
	step := func(id string, writes []indexRetrievalProcessFile, args ...string) {
		for _, file := range writes {
			if err := os.WriteFile(filepath.Join(vault, filepath.FromSlash(file.Path)), []byte(file.Body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		before := len(requests)
		mu.Unlock()
		command := exec.Command(binary, append([]string{"--vault", vault}, args...)...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		command.Dir = cwd
		command.Env = environment
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		result := indexBuildProcessResult{}
		if err := command.Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run Go CLI process %q: %v", strings.Join(args, " "), err)
			}
			result.ExitCode = exit.ExitCode()
		}
		result.Stdout = normalizeProcessOutput(stdout.String(), root)
		result.Stderr = normalizeProcessOutput(stderr.String(), root)
		mu.Lock()
		observed := append([]indexRetrievalProcessRequest{}, requests[before:]...)
		mu.Unlock()
		fixture.Steps = append(fixture.Steps, indexRetrievalProcessStep{
			ID: id, Writes: writes, Args: args, Result: result, Requests: observed,
			State: readIndexRetrievalState(t, root, filepath.Join(dataHome, "retrieval.db"), vault),
		})
	}
	step("initial", nil, "--json", "index")
	step("unchanged", nil, "--json", "index")
	alpha := strings.Replace(fixture.Files[0].Body, "title: Alpha Oracle", "title: Alpha Renamed", 1)
	step("frontmatter-only", []indexRetrievalProcessFile{{Path: "alpha.md", Body: alpha}}, "--json", "index")
	step("body-change", []indexRetrievalProcessFile{{Path: "nested/beta.md", Body: "changed beta body\n"}}, "index")

	if len(fixture.Steps[0].Requests) == 0 || len(fixture.Steps[1].Requests) != 0 || len(fixture.Steps[2].Requests) != 0 || len(fixture.Steps[3].Requests) != 1 {
		t.Fatalf("unexpected Go provider request counts: %d/%d/%d/%d", len(fixture.Steps[0].Requests), len(fixture.Steps[1].Requests), len(fixture.Steps[2].Requests), len(fixture.Steps[3].Requests))
	}
	if fixture.Steps[0].State.Lifecycle["big.md"] != "failed" {
		t.Fatalf("oversized retrieval write did not fail the lifecycle row: %v", fixture.Steps[0].State.Lifecycle)
	}
	return fixture
}

func buildIndexRetrievalBinary(t *testing.T, _ string) string {
	t.Helper()
	return processFixtureBinary(t)
}

func readIndexRetrievalState(t *testing.T, root, retrievalPath, vault string) indexRetrievalProcessState {
	t.Helper()
	db, err := sqlitekit.Open(retrievalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	state := indexRetrievalProcessState{}
	rows, err := db.Query("SELECT path, hash FROM documents ORDER BY path")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var document indexRetrievalProcessDocument
		if err := rows.Scan(&document.Path, &document.Hash); err != nil {
			t.Fatal(err)
		}
		document.Path = normalizeProcessOutput(document.Path, root)
		state.Documents = append(state.Documents, document)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	rows, err = db.Query("SELECT document_path, chunk_index, content, hash, embedding, norm, binary_signature, typeof(binary_signature), embedding_dim, embedding_model, char_start, char_end, embedding_pending, anchor_kind, anchor_value, content_norm FROM chunks ORDER BY document_path, chunk_index")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var chunk indexRetrievalProcessChunk
		var embedding, signature []byte
		var norm float64
		if err := rows.Scan(&chunk.DocumentPath, &chunk.ChunkIndex, &chunk.Content, &chunk.Hash, &embedding, &norm, &signature, &chunk.SignatureType, &chunk.Dim, &chunk.Model, &chunk.CharStart, &chunk.CharEnd, &chunk.EmbeddingPending, &chunk.AnchorKind, &chunk.AnchorValue, &chunk.ContentNorm); err != nil {
			t.Fatal(err)
		}
		chunk.DocumentPath = normalizeProcessOutput(chunk.DocumentPath, root)
		chunk.Embedding = hex.EncodeToString(embedding)
		chunk.BinarySignature = hex.EncodeToString(signature)
		chunk.Norm = strconv.FormatUint(math.Float64bits(norm), 16)
		state.Chunks = append(state.Chunks, chunk)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT value FROM index_meta WHERE key='generation'").Scan(&state.Generation); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM index_storage").Scan(&state.IndexStorage); err != nil {
		t.Fatal(err)
	}
	state.Lifecycle, state.Reasons = readIndexBuildLifecycle(t, vault)
	for name, reason := range state.Reasons {
		state.Reasons[name] = normalizeProcessOutput(reason, root)
	}
	return state
}
