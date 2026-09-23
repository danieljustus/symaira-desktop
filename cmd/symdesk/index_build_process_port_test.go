package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

type indexBuildProcessResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type indexBuildProcessFile struct {
	Path  string `json:"path"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type indexBuildProcessFixture struct {
	SchemaVersion    int                     `json:"schema_version"`
	DefaultFirst     indexBuildProcessResult `json:"default_first"`
	DefaultAgain     indexBuildProcessResult `json:"default_again"`
	Reembed          indexBuildProcessResult `json:"reembed"`
	Prune            indexBuildProcessResult `json:"prune"`
	Explicit         indexBuildProcessResult `json:"explicit"`
	Missing          indexBuildProcessResult `json:"missing"`
	DefaultFiles     []indexBuildProcessFile `json:"default_files"`
	ExplicitFiles    []indexBuildProcessFile `json:"explicit_files"`
	Lifecycle        map[string]string       `json:"lifecycle"`
	LifecycleReasons map[string]string       `json:"lifecycle_reasons"`
	ReembedNoNetwork bool                    `json:"reembed_no_network"`
	Metadata         bool                    `json:"metadata"`
}

func TestIndexBuildProcessPortFixture(t *testing.T) {
	fixture := observeIndexBuildProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/index-build-process.json")
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
		t.Fatal("index build fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./cmd/symdesk -run '^TestIndexBuildProcessPortFixture$'")
	}
}

func observeIndexBuildProcess(t *testing.T) indexBuildProcessFixture {
	t.Helper()
	root := t.TempDir()
	home, cwd, dataHome, tempRoot := filepath.Join(root, "home"), filepath.Join(root, "cwd"), filepath.Join(root, "data"), filepath.Join(root, "tmp")
	vault, explicit := filepath.Join(root, "vault"), filepath.Join(root, "explicit")
	for _, directory := range []string{home, cwd, dataHome, tempRoot, vault, explicit} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", dataHome)
	if err := os.WriteFile(filepath.Join(vault, "first.md"), []byte("# First\n\nfirst oracle document."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(vault, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "nested", "second.md"), []byte("# Second\n\nsecond oracle document."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(explicit, "only.md"), []byte("# Explicit\n\nexplicit oracle document."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "draft.DOC"), []byte("legacy office document"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "book.mobi"), []byte("ebook document"), 0o600); err != nil {
		t.Fatal(err)
	}
	var embeddingRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		embeddingRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"embedding": make([]float32, 8)}}})
	}))
	defer server.Close()
	configDir := filepath.Join(home, ".config", "symseek")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "index_path = \"" + filepath.Join(dataHome, "retrieval.db") + "\"\nollama_url = \"" + server.URL + "/api/embeddings\"\nembedding_dim = 8\ntimeout_seconds = 1\nretry_count = 0\nmodel = \"fixture\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(root, "symdesk")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := os.MkdirAll(filepath.Join(root, "go-build"), 0o700); err != nil {
		t.Fatal(err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("resolve build cache home: %v", err)
	}
	goEnv := exec.Command("go", "env", "GOMODCACHE")
	goEnv.Env = append(os.Environ(), "HOME="+currentUser.HomeDir)
	moduleCache, err := goEnv.Output()
	if err != nil {
		t.Fatalf("resolve Go module cache: %v", err)
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "HOME="+currentUser.HomeDir, "GOMODCACHE="+strings.TrimSpace(string(moduleCache)), "GOTMPDIR="+filepath.Join(root, "go-build"))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go CLI process fixture: %v\n%s", err, output)
	}
	environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempRoot, "TMP=" + tempRoot, "TEMP=" + tempRoot, "LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1"}
	run := func(args ...string) indexBuildProcessResult {
		command := exec.Command(binary, args...)
		command.Dir = cwd
		command.Env = environment
		output, err := command.Output()
		result := indexBuildProcessResult{Stdout: normalizeProcessOutput(string(output), root)}
		if err == nil {
			return result
		}
		if exit, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exit.ExitCode()
			result.Stderr = normalizeProcessOutput(string(exit.Stderr), root)
			return result
		}
		t.Fatalf("run Go CLI process %q: %v", strings.Join(args, " "), err)
		return result
	}
	fixture := indexBuildProcessFixture{SchemaVersion: 1}
	fixture.DefaultFirst = run("--json", "--vault", vault, "index")
	fixture.DefaultAgain = run("--json", "--vault", vault, "index")
	requestsBeforeReembed := embeddingRequests.Load()
	fixture.Reembed = run("--vault", vault, "index", "--re-embed")
	fixture.ReembedNoNetwork = embeddingRequests.Load() == requestsBeforeReembed
	if err := os.Remove(filepath.Join(vault, "nested", "second.md")); err != nil {
		t.Fatal(err)
	}
	fixture.Prune = run("--json", "--vault", vault, "index", "--prune")
	fixture.Explicit = run("--json", "--vault", vault, "index", explicit)
	fixture.Missing = run("--json", "--vault", filepath.Join(root, "missing"), "index")
	fixture.DefaultFiles = readIndexBuildFiles(t, vault)
	fixture.ExplicitFiles = readIndexBuildFiles(t, explicit)
	for _, files := range [][]indexBuildProcessFile{fixture.DefaultFiles, fixture.ExplicitFiles} {
		for index := range files {
			files[index].Path = normalizeProcessOutput(files[index].Path, root)
		}
	}
	fixture.Lifecycle, fixture.LifecycleReasons = readIndexBuildLifecycle(t, vault)
	_, err = os.Stat(filepath.Join(filepath.Dir(sidecarPathForFixture(t, vault)), "metadata.json"))
	fixture.Metadata = err == nil
	return fixture
}

func sidecarPathForFixture(t *testing.T, vault string) string {
	t.Helper()
	path, err := sidecar.PathForVault(vault)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func readIndexBuildFiles(t *testing.T, vault string) []indexBuildProcessFile {
	t.Helper()
	path := sidecarPathForFixture(t, vault)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT files.path,files.title,fts_search.body FROM files JOIN fts_search ON fts_search.rowid=files.id ORDER BY files.path")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var files []indexBuildProcessFile
	for rows.Next() {
		var file indexBuildProcessFile
		if err := rows.Scan(&file.Path, &file.Title, &file.Body); err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return files
}

func readIndexBuildLifecycle(t *testing.T, vault string) (map[string]string, map[string]string) {
	t.Helper()
	db, err := sqlitekit.Open(sidecarPathForFixture(t, vault))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT path,state,reason FROM index_lifecycle ORDER BY path")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	states := map[string]string{}
	reasons := map[string]string{}
	for rows.Next() {
		var path, state, reason string
		if err := rows.Scan(&path, &state, &reason); err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(path)
		states[name] = state
		if reason != "" {
			reasons[name] = reason
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return states, reasons
}
