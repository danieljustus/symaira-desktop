package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

// sourcesProcessFile is one file written below the fixture root. Body is
// UTF-8 text; Base64 carries binary payloads (a generated .docx).
type sourcesProcessFile struct {
	Path   string `json:"path"`
	Body   string `json:"body,omitempty"`
	Base64 string `json:"base64,omitempty"`
}

type sourcesProcessLink struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// sourcesProcessWatchAction is one filesystem change applied while
// `sources watch` is running, followed by the stderr line that proves the
// watcher processed it.
type sourcesProcessWatchAction struct {
	Writes  []sourcesProcessFile `json:"writes,omitempty"`
	Removes []string             `json:"removes,omitempty"`
	Wait    string               `json:"wait"`
}

type sourcesProcessState struct {
	Exists        bool                              `json:"exists"`
	Documents     []indexRetrievalProcessDocument   `json:"documents"`
	Chunks        []indexRetrievalProcessChunk      `json:"chunks"`
	Extractions   []indexRetrievalProcessExtraction `json:"extractions,omitempty"`
	ExtractionFTS map[string][]int64                `json:"extraction_fts,omitempty"`
	Generation    int64                             `json:"generation"`
	IndexStorage  int                               `json:"index_storage"`
}

type sourcesProcessStep struct {
	ID           string                         `json:"id"`
	Writes       []sourcesProcessFile           `json:"writes,omitempty"`
	Fills        []indexRetrievalProcessFill    `json:"fills,omitempty"`
	Removes      []string                       `json:"removes,omitempty"`
	Links        []sourcesProcessLink           `json:"links,omitempty"`
	Args         []string                       `json:"args"`
	Watch        []sourcesProcessWatchAction    `json:"watch,omitempty"`
	Result       indexBuildProcessResult        `json:"result"`
	Requests     []indexRetrievalProcessRequest `json:"requests"`
	State        sourcesProcessState            `json:"state"`
	Registry     string                         `json:"registry"`
	RegistryMode string                         `json:"registry_mode,omitempty"`
}

type sourcesProcessFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Config        string               `json:"config"`
	Vault         []sourcesProcessFile `json:"vault"`
	// Sources lists the root-relative folders whose registry identity
	// (folder-<sha256 of the canonical path>) is masked as $ID:<folder>.
	Sources []string `json:"sources"`
	// SortedStderr records that stderr lines are compared as a sorted list:
	// Go prepares the files of one directory in parallel and prints their
	// `Indexed:` lines in completion order. Watch steps keep line order.
	SortedStderr bool                 `json:"sorted_stderr"`
	Steps        []sourcesProcessStep `json:"steps"`
}

// sourcesProcessFailMarker makes the fake provider answer 500, which drives
// Go's batch-to-per-text fallback and a pending (unembedded) chunk.
const sourcesProcessFailMarker = "provider-fail"

var sourcesProcessIDPattern = regexp.MustCompile(`\$ID:[A-Za-z0-9_./-]+`)

// TestSourcesProcessPortFixture runs the real Go `symdesk sources
// add|list|remove|watch` processes against a loopback embedding provider and
// disposable HOME/XDG roots, recording output, exit status, provider requests,
// the registry file and the shared retrieval database after every step.
func TestSourcesProcessPortFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation and SIGINT delivery are not available on native Windows runners; the frozen fixture is recorded on Unix")
	}
	fixture := observeSourcesProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/sources-process.json")
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
		portProcessFixtureDifference(t, "sources fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./cmd/symdesk -run '^TestSourcesProcessPortFixture$'", current, encoded)
	}
}

func sourcesProcessDocx(t *testing.T, text string) string {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	entry, err := archive.Create("word/document.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>` + text + `</w:t></w:r></w:p></w:body></w:document>`)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes())
}

func sourcesProcessSHA(label string) string {
	sum := sha256.Sum256([]byte(label))
	return hex.EncodeToString(sum[:])
}

func observeSourcesProcess(t *testing.T) sourcesProcessFixture {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	home, cwd, dataHome, tempRoot, vault := filepath.Join(root, "home"), filepath.Join(root, "cwd"), filepath.Join(root, "data"), filepath.Join(root, "tmp"), filepath.Join(root, "vault")
	for _, directory := range []string{home, cwd, dataHome, tempRoot, filepath.Join(vault, "inner")} {
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
		for _, input := range body.Input {
			if strings.Contains(input, sourcesProcessFailMarker) {
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
	fixture := sourcesProcessFixture{SchemaVersion: 1, SortedStderr: true}
	fixture.Config = "index_path = $INDEX_PATH\nollama_url = \"$SERVER/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nmodel = \"fixture\"\n"
	config := strings.NewReplacer("$INDEX_PATH", strconv.Quote(filepath.Join(dataHome, "retrieval.db")), "$SERVER", server.URL).Replace(fixture.Config)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Sources = []string{"ext/a", "ext/b", "ext/c", "ext/build", "ext/rel", "ext/gone", "ext/w", "ext/alias", "cwd"}
	ids := map[string]string{}
	for _, rel := range fixture.Sources {
		ids["folder-"+sourcesProcessSHA(filepath.Join(canonical, filepath.FromSlash(rel)))] = "$ID:" + rel
	}

	fixture.Vault = []sourcesProcessFile{
		{Path: "note.md", Body: "---\ntitle: Vault Note\n---\n# Vault Note\n\nvault resident document that sources must never touch.\n"},
		{Path: "inner/inside.md", Body: "# Inside\n\ninside the vault.\n"},
	}
	for _, file := range fixture.Vault {
		writeSourcesProcessFile(t, vault, file)
	}

	binary := buildIndexRetrievalBinary(t, root)
	environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempRoot, "TMP=" + tempRoot, "TEMP=" + tempRoot, "LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1"}
	normalize := func(value string) string {
		for id, name := range ids {
			value = strings.ReplaceAll(value, id, name)
		}
		value = strings.ReplaceAll(value, canonical, "$ROOT")
		value = strings.ReplaceAll(value, root, "$ROOT")
		value = strings.ReplaceAll(value, strings.ReplaceAll(root, `\`, `\\`), "$ROOT")
		return filepath.ToSlash(strings.ReplaceAll(value, `\\`, "/"))
	}
	expand := func(arg string) string {
		arg = sourcesProcessIDPattern.ReplaceAllStringFunc(arg, func(token string) string {
			return "folder-" + sourcesProcessSHA(filepath.Join(canonical, filepath.FromSlash(strings.TrimPrefix(token, "$ID:"))))
		})
		return strings.ReplaceAll(arg, "$ROOT", canonical)
	}
	sortStderr := func(value string) string {
		if value == "" {
			return value
		}
		lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
		kept := lines[:0]
		for _, line := range lines {
			// Provider retry diagnostics carry transport-specific text.
			if !strings.HasPrefix(line, "engine: ") {
				kept = append(kept, line)
			}
		}
		sort.Strings(kept)
		if len(kept) == 0 {
			return ""
		}
		return strings.Join(kept, "\n") + "\n"
	}
	keepStderr := func(value string) string {
		var kept strings.Builder
		for _, line := range strings.SplitAfter(value, "\n") {
			if !strings.HasPrefix(line, "engine: ") {
				kept.WriteString(line)
			}
		}
		return kept.String()
	}

	stepWith := func(step sourcesProcessStep) {
		for _, file := range step.Writes {
			writeSourcesProcessFile(t, root, file)
		}
		for _, fill := range step.Fills {
			writeSourcesProcessFile(t, root, sourcesProcessFile{Path: fill.Path, Body: fill.Prefix + strings.Repeat("x", fill.Bytes-len(fill.Prefix))})
		}
		for _, name := range step.Removes {
			if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
		for _, link := range step.Links {
			if err := os.Symlink(filepath.FromSlash(link.Target), filepath.Join(root, filepath.FromSlash(link.Path))); err != nil {
				t.Fatal(err)
			}
		}
		mu.Lock()
		before := len(requests)
		mu.Unlock()
		args := make([]string, 0, len(step.Args)+2)
		args = append(args, "--vault", vault)
		for _, arg := range step.Args {
			args = append(args, expand(arg))
		}
		command := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		command.Dir = cwd
		command.Env = environment
		var stdout bytes.Buffer
		stderr := &sourcesProcessBuffer{}
		command.Stdout, command.Stderr = &stdout, stderr
		result := indexBuildProcessResult{}
		if step.Watch != nil {
			if err := command.Start(); err != nil {
				t.Fatalf("start Go CLI process %q: %v", strings.Join(args, " "), err)
			}
			stderr.wait(t, "Watching ")
			for _, action := range step.Watch {
				for _, file := range action.Writes {
					writeSourcesProcessFile(t, root, file)
				}
				for _, name := range action.Removes {
					if err := os.RemoveAll(filepath.Join(root, filepath.FromSlash(name))); err != nil {
						t.Fatal(err)
					}
				}
				stderr.wait(t, action.Wait)
			}
			if err := command.Process.Signal(os.Interrupt); err != nil {
				t.Fatal(err)
			}
		}
		var runErr error
		if step.Watch != nil {
			runErr = command.Wait()
		} else {
			runErr = command.Run()
		}
		if runErr != nil {
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) {
				t.Fatalf("run Go CLI process %q: %v", strings.Join(args, " "), runErr)
			}
			result.ExitCode = exit.ExitCode()
		}
		result.Stdout = normalize(stdout.String())
		if step.Watch != nil {
			result.Stderr = normalize(keepStderr(stderr.String()))
		} else {
			result.Stderr = normalize(sortStderr(stderr.String()))
		}
		mu.Lock()
		observed := append([]indexRetrievalProcessRequest{}, requests[before:]...)
		mu.Unlock()
		// The Go indexer prepares files in parallel, so request arrival
		// order is not stable; the multiset of requests is.
		sort.SliceStable(observed, func(i, j int) bool { return string(observed[i].Body) < string(observed[j].Body) })
		recorded := make([]string, 0, len(step.Args))
		recorded = append(recorded, step.Args...)
		step.Args, step.Result, step.Requests = recorded, result, observed
		step.State = readSourcesProcessState(t, normalize, filepath.Join(dataHome, "retrieval.db"))
		registry, err := os.ReadFile(filepath.Join(vault, ".symdesk", "search-sources.json")) //nolint:gosec // fixture-owned path
		switch {
		case err == nil:
			step.Registry = normalize(string(registry))
			if info, statErr := os.Stat(filepath.Join(vault, ".symdesk", "search-sources.json")); statErr == nil {
				step.RegistryMode = info.Mode().Perm().String()
			}
		case errors.Is(err, os.ErrNotExist):
		default:
			t.Fatal(err)
		}
		fixture.Steps = append(fixture.Steps, step)
	}
	step := func(id string, args ...string) sourcesProcessStep {
		return sourcesProcessStep{ID: id, Args: args}
	}
	with := func(base sourcesProcessStep, writes ...sourcesProcessFile) sourcesProcessStep {
		base.Writes = writes
		return base
	}
	file := func(path, body string) sourcesProcessFile { return sourcesProcessFile{Path: path, Body: body} }

	sidecarDigest := sourcesProcessSHA("external-sidecar")
	longText := func(seed string) string { return strings.Repeat(seed+" ", 120) }
	sourceA := []sourcesProcessFile{
		file("ext/a/a.md", "---\ntitle: Alpha External\ntags: [external, oracle]\ntype: note\n---\n# Alpha\n\nIntro text for alpha external.\n\n## Details\n\n"+longText("alphadetail")+"\n"),
		file("ext/a/plain.txt", "plain external text\nsecond line\n"),
		file("ext/a/UPPER.TXT", "upper-case extension is lowered by the walk\n"),
		file("ext/a/notes.text", "the .text extension is a supported plain text format\n"),
		file("ext/a/nested/code.go", "package main\n\nfunc main() { println(\"external go source\") }\n"),
		file("ext/a/nested/data.json", "{\"external\":\"json document\",\"n\":1}\n"),
		file("ext/a/nested/config.yaml", "external: yaml document\n"),
		file("ext/a/table.csv", "name,qty\nwidget,3\n\"quoted, cell\",4\n"),
		file("ext/a/page.html", "<html><head><title>T</title></head><body><p>external html page text</p></body></html>\n"),
		file("ext/a/memo.rtf", "{\\rtf1\\ansi external rtf memo\\par}\n"),
		{Path: "ext/a/report.docx", Base64: sourcesProcessDocx(t, "external docx report text")},
		file("ext/a/empty.md", ""),
		file("ext/a/noext", "files without an extension are not indexed\n"),
		file("ext/a/.hidden/skip.md", "# Hidden\n\nhidden directories are skipped.\n"),
		file("ext/a/node_modules/skip.md", "# Modules\n\nnode_modules is skipped.\n"),
		file("ext/a/vendor/skip.js", "// vendor is skipped\n"),
		file("ext/a/sc.md", "---\ntitle: Sidecar External\nsha256: "+sidecarDigest+"\n---\n# Sidecar\n\ninvoice grounded external sidecar text.\n"),
		file("ext/a/.symaira/extractions/"+sidecarDigest+".jsonl", `{"field":"invoice","value":"external invoice","span":{"start":0,"end":7,"snippet":"invoice"},"matched":true}`+"\n"+`{"field":"cafe","value":"cafe external","matched":false}`+"\n"),
		file("ext/a/ünïcode/日本語.md", "# Unicode\n\nunicode directory and file names.\n"),
	}

	step0 := func(s sourcesProcessStep) { stepWith(s) }
	step0(step("list-empty", "sources", "list"))
	step0(step("list-empty-json", "--json", "sources", "list"))
	step0(step("vault-index", "index"))
	step0(step("add-missing", "sources", "add", "$ROOT/ext/missing"))
	step0(step("add-empty-argument", "sources", "add", ""))
	step0(with(step("add-file", "sources", "add", "$ROOT/ext/file.txt"), file("ext/file.txt", "a file is not a folder\n")))
	step0(step("add-inside-vault", "sources", "add", "$ROOT/vault/inner"))
	step0(step("add-vault-itself", "sources", "add", "$ROOT/vault"))
	step0(with(step("add-a", "sources", "add", "$ROOT/ext/a"), sourceA...))
	step0(step("remove-without-argument", "sources", "remove"))
	step0(step("add-a-again", "sources", "add", "$ROOT/ext/a"))
	step0(step("add-a-json", "--json", "sources", "add", "$ROOT/ext/a"))
	step0(step("source-alias-add-again", "source", "add", "$ROOT/ext/a"))
	step0(step("list-text", "sources", "list"))
	step0(step("list-json", "--json", "sources", "list"))

	edit := step("edit-a", "sources", "add", "$ROOT/ext/a")
	edit.Writes = []sourcesProcessFile{
		file("ext/a/a.md", "---\ntitle: Alpha Renamed\ntags: [external]\n---\n# Alpha\n\nIntro text for alpha external, edited.\n"),
		file("ext/a/new.txt", "freshly added external text\n"),
		file("ext/a/nested/code.go", "package main\n\nfunc main() { println(\"edited go source\") }\n"),
	}
	edit.Removes = []string{"ext/a/plain.txt", "ext/a/table.csv", "ext/a/nested/data.json"}
	step0(edit)
	sidecarEdit := step("edit-a-sidecar-body", "sources", "add", "$ROOT/ext/a")
	sidecarEdit.Writes = []sourcesProcessFile{
		file("ext/a/sc.md", "---\ntitle: Sidecar External\nsha256: "+sidecarDigest+"\n---\n# Sidecar\n\ninvoice grounded external sidecar text, body changed.\n"),
		file("ext/a/.symaira/extractions/"+sidecarDigest+".jsonl", `{"field":"invoice","value":"re-imported invoice","span":{"start":0,"end":7,"snippet":"invoice"},"matched":true}`+"\n"),
	}
	step0(sidecarEdit)

	addB := step("add-b-skipped-and-pending", "sources", "add", "$ROOT/ext/b")
	addB.Writes = []sourcesProcessFile{
		file("ext/b/ok.md", "# OK\n\nhealthy external document.\n"),
		file("ext/b/failing.md", "# Failing\n\n"+sourcesProcessFailMarker+" marker text\n\n# Healthy\n\nhealthy section text\n"),
		file("ext/b/bad.docx", "this is not a zip archive\n"),
	}
	addB.Fills = []indexRetrievalProcessFill{{Path: "ext/b/big.txt", Prefix: "oversized external text ", Bytes: 10<<20 + 1}}
	addB.Links = []sourcesProcessLink{{Path: "ext/b/link.md", Target: "ok.md"}, {Path: "ext/b/dirlink", Target: "../a/nested"}}
	step0(addB)
	step0(with(step("add-c-unsupported-formats", "sources", "add", "$ROOT/ext/c"),
		file("ext/c/readable.md", "# Readable\n\nsupported next to unsupported formats.\n"),
		file("ext/c/legacy.doc", "legacy word document\n"),
		file("ext/c/book.mobi", "mobi book\n"),
	))
	step0(with(step("add-build-named-root", "sources", "add", "$ROOT/ext/build"), file("ext/build/inside.md", "# Build\n\nthe root folder is named build and is skipped by the walk.\n")))
	step0(with(step("add-relative", "sources", "add", "../ext/rel"), file("ext/rel/rel.md", "# Relative\n\nregistered through a relative path.\n")))
	step0(sourcesProcessStep{ID: "add-through-symlink", Args: []string{"sources", "add", "$ROOT/ext/alias"}, Links: []sourcesProcessLink{{Path: "ext/alias", Target: "a"}}})
	step0(with(step("add-gone", "sources", "add", "$ROOT/ext/gone"), file("ext/gone/gone.md", "# Gone\n\nthe folder disappears later.\n")))
	gone := step("watch-gone-folder", "sources", "watch", "$ID:ext/gone")
	gone.Removes = []string{"ext/gone"}
	step0(gone)
	step0(step("remove-gone-folder", "sources", "remove", "$ID:ext/gone"))
	step0(step("remove-unregistered", "sources", "remove", "$ROOT/ext/never"))
	step0(step("watch-unregistered", "sources", "watch", "unknown-id"))

	watch := with(step("watch-live", "sources", "watch", "$ROOT/ext/w"))
	step0(with(step("add-w", "sources", "add", "$ROOT/ext/w"), file("ext/w/first.md", "# First\n\nwatched folder initial document.\n")))
	watch.Watch = []sourcesProcessWatchAction{
		{Writes: []sourcesProcessFile{file("ext/w/live.md", "# Live\n\ncreated while watching.\n")}, Wait: "Watch: indexed=1 removed=0"},
		{Removes: []string{"ext/w/live.md"}, Wait: "Watch: indexed=0 removed=1"},
	}
	step0(watch)

	step0(step("list-before-remove", "--json", "sources", "list"))
	step0(step("remove-by-id", "sources", "remove", "$ID:ext/a"))
	step0(step("remove-by-path", "--json", "sources", "remove", "$ROOT/ext/b"))
	step0(step("remove-w", "sources", "remove", "$ID:ext/w"))
	step0(step("list-after-remove", "sources", "list"))
	step0(step("vault-index-unchanged", "index"))
	return fixture
}

func writeSourcesProcessFile(t *testing.T, root string, file sourcesProcessFile) {
	t.Helper()
	target := filepath.Join(root, filepath.FromSlash(file.Path))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte(file.Body)
	if file.Base64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(file.Base64)
		if err != nil {
			t.Fatal(err)
		}
		body = decoded
	}
	if err := os.WriteFile(target, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

// sourcesProcessBuffer collects a running process's stderr.
type sourcesProcessBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *sourcesProcessBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *sourcesProcessBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func (b *sourcesProcessBuffer) wait(t *testing.T, needle string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), needle) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in stderr:\n%s", needle, b.String())
}

func readSourcesProcessState(t *testing.T, normalize func(string) string, retrievalPath string) sourcesProcessState {
	t.Helper()
	state := sourcesProcessState{Documents: []indexRetrievalProcessDocument{}, Chunks: []indexRetrievalProcessChunk{}}
	if _, err := os.Stat(retrievalPath); err != nil {
		return state
	}
	state.Exists = true
	db, err := sqlitekit.Open(retrievalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT path, hash FROM documents ORDER BY path")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var document indexRetrievalProcessDocument
		if err := rows.Scan(&document.Path, &document.Hash); err != nil {
			t.Fatal(err)
		}
		document.Path = normalize(document.Path)
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
		chunk.DocumentPath = normalize(chunk.DocumentPath)
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
	rows, err = db.Query("SELECT e.id, e.document_path, c.chunk_index, e.class, e.value, e.evidence_text, e.span_start, e.span_end, e.matched, e.producer, e.source_ref FROM extractions e LEFT JOIN chunks c ON c.id = e.chunk_id ORDER BY e.document_path, e.id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var extraction indexRetrievalProcessExtraction
		if err := rows.Scan(&extraction.ID, &extraction.DocumentPath, &extraction.ChunkIndex, &extraction.Class, &extraction.Value, &extraction.EvidenceText, &extraction.SpanStart, &extraction.SpanEnd, &extraction.Matched, &extraction.Producer, &extraction.SourceRef); err != nil {
			t.Fatal(err)
		}
		extraction.ID = 0 // row ids follow parallel commit order
		extraction.DocumentPath = normalize(extraction.DocumentPath)
		extraction.SourceRef = normalize(extraction.SourceRef)
		state.Extractions = append(state.Extractions, extraction)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	state.ExtractionFTS = map[string][]int64{}
	for _, term := range indexRetrievalExtractionTerms {
		var count int64
		if err := db.QueryRow("SELECT COUNT(*) FROM extractions_fts WHERE extractions_fts MATCH ?", term).Scan(&count); err != nil {
			t.Fatal(err)
		}
		state.ExtractionFTS[term] = []int64{count}
	}
	return state
}
