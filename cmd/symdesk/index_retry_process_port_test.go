package main

import (
	"bytes"
	"encoding/json"
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

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

type indexRetryProcessFile struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

// indexRetryProcessSeed records a lifecycle row written through the Go
// sidecar API before a step runs. The current `symdesk index` walk only visits
// Markdown files, so no Go CLI path leaves a failed row for another extension;
// the row stands in for state written by an earlier binary or another surface.
type indexRetryProcessSeed struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type indexRetryProcessStep struct {
	ID       string                         `json:"id"`
	Dirs     []string                       `json:"dirs,omitempty"`
	Writes   []indexRetryProcessFile        `json:"writes,omitempty"`
	Seeds    []indexRetryProcessSeed        `json:"seeds,omitempty"`
	Removes  []string                       `json:"removes,omitempty"`
	Args     []string                       `json:"args"`
	Result   indexBuildProcessResult        `json:"result"`
	Requests []indexRetrievalProcessRequest `json:"requests"`
	State    indexRetrievalProcessState     `json:"state"`
}

type indexRetryProcessFixture struct {
	SchemaVersion int                     `json:"schema_version"`
	Config        string                  `json:"config"`
	Steps         []indexRetryProcessStep `json:"steps"`
}

// indexRetryBrokenFrontmatter repeats a top-level frontmatter key, so the Go
// document parser fails and the lifecycle row is recorded as failed until the
// file is repaired.
var indexRetryBrokenFrontmatter = "---\ntitle: first\ntitle: second\n---\n# Broken\n\nbroken retry oracle document.\n"

// TestIndexRetryProcessPortFixture runs the real Go `symdesk index` and
// `symdesk index retry` processes through failing, still-broken, repaired,
// removed, pruned and nothing-to-retry states, recording the lifecycle rows,
// hybrid retrieval rows and every provider request after each step.
func TestIndexRetryProcessPortFixture(t *testing.T) {
	fixture := observeIndexRetryProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/index-retry-process.json")
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
		offset := 0
		for offset < len(current) && offset < len(encoded) && current[offset] == encoded[offset] {
			offset++
		}
		from := max(offset-160, 0)
		t.Fatalf("index retry fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./cmd/symdesk -run '^TestIndexRetryProcessPortFixture$'\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeIndexRetryProcess(t *testing.T) indexRetryProcessFixture {
	t.Helper()
	root := t.TempDir()
	home, cwd, dataHome, tempRoot := filepath.Join(root, "home"), filepath.Join(root, "cwd"), filepath.Join(root, "data"), filepath.Join(root, "tmp")
	vault := filepath.Join(root, "vault")
	for _, directory := range []string{home, cwd, dataHome, tempRoot, vault} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", dataHome)

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
		for range body.Input {
			data = append(data, map[string]any{"embedding": []float32{0.25, -0.5, 1}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := indexRetryProcessFixture{SchemaVersion: 1}
	fixture.Config = "index_path = $INDEX_PATH\nollama_url = \"$SERVER/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nmodel = \"fixture\"\n"
	config := strings.NewReplacer("$INDEX_PATH", strconv.Quote(filepath.Join(dataHome, "retrieval.db")), "$SERVER", server.URL).Replace(fixture.Config)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := buildIndexRetrievalBinary(t, root)
	environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempRoot, "TMP=" + tempRoot, "TEMP=" + tempRoot, "LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1"}
	stepWithSeeds := func(id string, dirs []string, writes []indexRetryProcessFile, seeds []indexRetryProcessSeed, removes []string, args ...string) {
		for _, name := range dirs {
			if err := os.MkdirAll(filepath.Join(vault, filepath.FromSlash(name)), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		for _, file := range writes {
			if err := os.WriteFile(filepath.Join(vault, filepath.FromSlash(file.Path)), []byte(file.Body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if len(seeds) > 0 {
			seedIndexRetryLifecycle(t, vault, seeds)
		}
		for _, name := range removes {
			if err := os.Remove(filepath.Join(vault, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		before := len(requests)
		mu.Unlock()
		command := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
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
		result.Stdout = normalizeRetryText(stdout.String(), root)
		result.Stderr = normalizeRetryText(stderr.String(), root)
		mu.Lock()
		observed := append([]indexRetrievalProcessRequest{}, requests[before:]...)
		mu.Unlock()
		recorded := make([]string, 0, len(args))
		for _, arg := range args {
			recorded = append(recorded, normalizeRetryText(arg, root))
		}
		fixture.Steps = append(fixture.Steps, indexRetryProcessStep{
			ID: id, Dirs: dirs, Writes: writes, Seeds: seeds, Removes: removes, Args: recorded, Result: result, Requests: observed,
			State: readIndexRetryState(t, root, filepath.Join(dataHome, "retrieval.db"), vault),
		})
	}
	step := func(id string, writes []indexRetryProcessFile, removes []string, args ...string) {
		stepWithSeeds(id, nil, writes, nil, removes, args...)
	}
	// The vault walk stops at the first parse failure, so each run leaves the
	// failing file recorded and the rest of the vault for a later run.
	step("fail-bad", []indexRetryProcessFile{{Path: "bad.md", Body: indexRetryBrokenFrontmatter}}, nil, "--vault", vault, "index")
	step("retry-still-broken", nil, nil, "--json", "--vault", vault, "index", "retry")
	step("repair-bad", []indexRetryProcessFile{{Path: "bad.md", Body: "# Repaired\n\nrepaired retry oracle document.\n"}}, nil, "--vault", vault, "index", "retry")
	step("break-gone", []indexRetryProcessFile{{Path: "gone.md", Body: indexRetryBrokenFrontmatter}}, nil, "--vault", vault, "index")
	step("retry-after-stat-failure", nil, []string{"gone.md"}, "--vault", vault, "index", "retry")
	step("prune-vault", nil, nil, "--json", "--vault", vault, "index", "--prune")
	step("retry-nothing-failed", nil, nil, "--json", "--vault", vault, "index", "retry")

	// Non-Markdown failed rows. Go's retry does not look at the extension: it
	// stats each row, then runs the Markdown parser and hybrid indexer on the
	// stored path. Only a recognised-but-unsupported extension changes the
	// state a parse failure is recorded with.
	seedFailed := func(paths ...string) []indexRetryProcessSeed {
		seeds := make([]indexRetryProcessSeed, 0, len(paths))
		for _, path := range paths {
			seeds = append(seeds, indexRetryProcessSeed{Path: path, State: "failed", Reason: "interrupted by an earlier index worker"})
		}
		return seeds
	}
	stepWithSeeds("retry-nonmarkdown-indexes", nil, []indexRetryProcessFile{
		{Path: "notes.txt", Body: "# Plain notes\n\nplain text retry oracle document.\n"},
		{Path: "scan.pdf", Body: "%PDF-1.4\nstub pdf retry oracle body without a text layer.\n"},
		{Path: "legacy.doc", Body: "legacy word retry oracle body.\n"},
		{Path: "README", Body: "extensionless retry oracle readme.\n"},
		{Path: "UPPER.MD", Body: "---\ntitle: Upper\n---\nuppercase markdown extension retry oracle.\n"},
	}, seedFailed("README", "UPPER.MD", "legacy.doc", "notes.txt", "scan.pdf"), nil, "--json", "--vault", vault, "index", "retry")
	stepWithSeeds("retry-nonmarkdown-failures", []string{"folder.txt"}, []indexRetryProcessFile{
		{Path: "broken.txt", Body: indexRetryBrokenFrontmatter},
		{Path: "broken.doc", Body: indexRetryBrokenFrontmatter},
	}, seedFailed("broken.doc", "broken.txt", "folder.txt", "missing.txt"), nil, "--json", "--vault", vault, "index", "retry")
	step("retry-nonmarkdown-again", nil, nil, "--vault", vault, "index", "retry")
	step("prune-nonmarkdown", nil, nil, "--json", "--vault", vault, "index", "--prune")
	step("repair-nonmarkdown", []indexRetryProcessFile{{Path: "broken.txt", Body: "# Fixed text\n\nrepaired plain text retry oracle.\n"}}, nil, "--json", "--vault", vault, "index", "retry")
	return fixture
}

// seedIndexRetryLifecycle records lifecycle rows through the Go sidecar API in
// the same disposable HOME/XDG roots the CLI process uses.
func seedIndexRetryLifecycle(t *testing.T, vaultRoot string, seeds []indexRetryProcessSeed) {
	t.Helper()
	db, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close seeded sidecar: %v", err)
		}
	}()
	for _, seed := range seeds {
		if err := db.SetIndexStatus(filepath.Join(vaultRoot, filepath.FromSlash(seed.Path)), sidecar.IndexState(seed.State), seed.Reason); err != nil {
			t.Fatal(err)
		}
	}
}

// readIndexRetryState reads the same state as readIndexRetrievalState, but
// treats a retrieval database that Go has not created yet as empty: Go opens
// the hybrid index only when a document is actually written to it.
func readIndexRetryState(t *testing.T, root, retrievalPath, vault string) indexRetrievalProcessState {
	t.Helper()
	if _, err := os.Stat(retrievalPath); err == nil {
		state := readIndexRetrievalState(t, root, retrievalPath, vault)
		for name, reason := range state.Reasons {
			state.Reasons[name] = normalizeRetryText(reason, root)
		}
		return state
	}
	lifecycle, reasons := readIndexBuildLifecycle(t, vault)
	for name, reason := range reasons {
		reasons[name] = normalizeRetryText(reason, root)
	}
	return indexRetrievalProcessState{Documents: []indexRetrievalProcessDocument{}, Chunks: []indexRetrievalProcessChunk{}, Lifecycle: lifecycle, Reasons: reasons}
}

// normalizeRetryText replaces the temporary root with $ROOT and rewrites the
// separators of $ROOT path tokens to "/", so the frozen fixture is the same on
// every platform. Only path tokens are touched: a whole-output separator
// rewrite would also turn the JSON escapes in the failure text into slashes on
// Windows. The missing-file wording is recorded as the Unix oracle reports it;
// the Windows oracle reports the same missing file through GetFileAttributesEx.
func normalizeRetryText(value, root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		value = strings.ReplaceAll(value, resolved, "$ROOT")
	}
	value = strings.ReplaceAll(value, root, "$ROOT")
	value = strings.ReplaceAll(value, strings.ReplaceAll(root, `\`, `\\`), "$ROOT")
	value = slashRootPaths(value)
	for _, name := range []string{"gone.md", "missing.txt"} {
		value = strings.ReplaceAll(value,
			"GetFileAttributesEx $ROOT/vault/"+name+": The system cannot find the file specified.",
			"lstat $ROOT/vault/"+name+": no such file or directory")
	}
	return value
}

// slashRootPaths rewrites the separators of every $ROOT path token to "/".
// A doubled backslash inside a token is one separator.
func slashRootPaths(text string) string {
	var out strings.Builder
	for {
		index := strings.Index(text, "$ROOT")
		if index < 0 {
			out.WriteString(text)
			return out.String()
		}
		out.WriteString(text[:index+len("$ROOT")])
		text = text[index+len("$ROOT"):]
		consumed := 0
		for consumed < len(text) {
			c := text[consumed]
			switch {
			case c == '\\' || c == '/':
				out.WriteByte('/')
				consumed++
				if consumed < len(text) && text[consumed] == '\\' {
					consumed++
				}
			case c == '.' || c == '_' || c == '-' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
				out.WriteByte(c)
				consumed++
			default:
				goto done
			}
		}
	done:
		text = text[consumed:]
	}
}
