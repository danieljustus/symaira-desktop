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
)

type indexSymlinkProcessLink struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

type indexSymlinkProcessStep struct {
	ID       string                         `json:"id"`
	Writes   []indexRetryProcessFile        `json:"writes,omitempty"`
	Removes  []string                       `json:"removes,omitempty"`
	Links    []indexSymlinkProcessLink      `json:"links,omitempty"`
	Args     []string                       `json:"args"`
	Result   indexBuildProcessResult        `json:"result"`
	Requests []indexRetrievalProcessRequest `json:"requests"`
	State    indexRetrievalProcessState     `json:"state"`
}

type indexSymlinkProcessFixture struct {
	SchemaVersion int                       `json:"schema_version"`
	Config        string                    `json:"config"`
	Steps         []indexSymlinkProcessStep `json:"steps"`
}

// indexSymlinkOutsideBody is the body of a document outside the vault. The
// fixture records every provider request and retrieval row, so a link that is
// followed out of the vault would surface this marker.
const indexSymlinkOutsideBody = "---\ntitle: Outside\n---\n# Outside\n\nOUTSIDE_SECRET_MARKER must never be indexed.\n"

// TestIndexSymlinkProcessPortFixture runs the real Go `symdesk index` and
// `symdesk index retry` processes over contained, external, broken and
// directory symlinks inside a vault, recording counts, lifecycle rows, hybrid
// retrieval rows and every provider request after each step.
func TestIndexSymlinkProcessPortFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs a privilege that native Windows runners do not grant; the frozen fixture is recorded on Unix")
	}
	fixture := observeIndexSymlinkProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/index-symlink-process.json")
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
		t.Fatalf("index symlink fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./cmd/symdesk -run '^TestIndexSymlinkProcessPortFixture$'\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeIndexSymlinkProcess(t *testing.T) indexSymlinkProcessFixture {
	t.Helper()
	root := t.TempDir()
	home, cwd, dataHome, tempRoot := filepath.Join(root, "home"), filepath.Join(root, "cwd"), filepath.Join(root, "data"), filepath.Join(root, "tmp")
	vault, outside := filepath.Join(root, "vault"), filepath.Join(root, "outside")
	for _, directory := range []string{home, cwd, dataHome, tempRoot, vault, outside} {
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
	fixture := indexSymlinkProcessFixture{SchemaVersion: 1}
	fixture.Config = "index_path = $INDEX_PATH\nollama_url = \"$SERVER/api/embeddings\"\nembedding_dim = 3\ntimeout_seconds = 2\nretry_count = 0\nmodel = \"fixture\"\n"
	config := strings.NewReplacer("$INDEX_PATH", strconv.Quote(filepath.Join(dataHome, "retrieval.db")), "$SERVER", server.URL).Replace(fixture.Config)
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := buildIndexRetrievalBinary(t, root)
	environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempRoot, "TMP=" + tempRoot, "TEMP=" + tempRoot, "LANG=C", "LC_ALL=C", "TZ=UTC", "TERM=dumb", "NO_COLOR=1"}
	step := func(id string, writes []indexRetryProcessFile, removes []string, links []indexSymlinkProcessLink, args ...string) {
		for _, file := range writes {
			target := filepath.Join(vault, filepath.FromSlash(file.Path))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(file.Body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		for _, name := range removes {
			if err := os.Remove(filepath.Join(vault, filepath.FromSlash(name))); err != nil {
				t.Fatal(err)
			}
		}
		for _, link := range links {
			if err := os.Symlink(filepath.FromSlash(link.Target), filepath.Join(vault, filepath.FromSlash(link.Path))); err != nil {
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
		fixture.Steps = append(fixture.Steps, indexSymlinkProcessStep{
			ID: id, Writes: writes, Removes: removes, Links: links, Args: recorded, Result: result, Requests: observed,
			State: readIndexRetryState(t, root, filepath.Join(dataHome, "retrieval.db"), vault),
		})
	}
	file := func(path, body string) indexRetryProcessFile { return indexRetryProcessFile{Path: path, Body: body} }
	link := func(path, target string) indexSymlinkProcessLink {
		return indexSymlinkProcessLink{Path: path, Target: target}
	}
	// Names sort contained < dirlink < external < inside < sub, so each walk
	// reaches the links before the regular documents.
	step("seed", []indexRetryProcessFile{
		file("inside.md", "# Inside\n\ninside symlink oracle document.\n"),
		file("sub/target.md", "# Target\n\ncontained symlink target document.\n"),
		file("../outside/secret.md", indexSymlinkOutsideBody),
		file("../outside/dirnote.md", indexSymlinkOutsideBody),
	}, nil, []indexSymlinkProcessLink{
		link("contained.md", "sub/target.md"),
		link("external.md", "../outside/secret.md"),
		link("broken.md", "missing-target.md"),
		link("dirlink", "../outside"),
	}, "--vault", vault, "index")
	step("unchanged", nil, nil, nil, "--json", "--vault", vault, "index")
	step("retry-nothing-failed", nil, nil, nil, "--json", "--vault", vault, "index", "retry")
	step("contained-retargeted-outside", nil, []string{"contained.md"}, []indexSymlinkProcessLink{link("contained.md", "../outside/secret.md")}, "--json", "--vault", vault, "index")
	step("prune-after-retarget", nil, nil, nil, "--json", "--vault", vault, "index", "--prune")
	step("external-removed", nil, []string{"external.md"}, nil, "--json", "--vault", vault, "index", "--prune")
	return fixture
}
