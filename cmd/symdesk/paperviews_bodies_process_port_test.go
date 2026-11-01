package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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
)

// RUST-013 slice r013-paperviews: the command bodies of `paperless import`,
// `paperless migrate`, `views exec|new-entry|export-csv|import-csv|embed`.
// Every step runs the real Go `symdesk` process in an isolated
// HOME/XDG/cwd with a disposable vault; steps of a snapshot case also record
// the whole vault tree after the step. `paperless migrate` talks to a
// loopback fake Paperless-ngx whose requests are recorded per step. The Rust
// replay is `crates/symdesk-cli/tests/cli_paperviews_bodies.rs`.

const paperviewsFixturePath = "../../testdata/port/cli/cli-paperviews-bodies-process.json"

type pvFakeFile struct {
	ContentType string `json:"content_type"`
	Disposition string `json:"disposition,omitempty"`
	Body        string `json:"body"`
}

// pvFake describes the loopback Paperless-ngx. Documents are served in the
// listed order (newest first, like `ordering=-created_date`).
type pvFake struct {
	Token        string                      `json:"token"`
	PageSize     int                         `json:"page_size,omitempty"`
	Documents    []map[string]any            `json:"documents"`
	Entities     map[string][]map[string]any `json:"entities,omitempty"`
	Files        map[string]pvFakeFile       `json:"files,omitempty"`
	FailDownload []int                       `json:"fail_download,omitempty"`
}

type pvStep struct {
	Args     []string          `json:"args"`
	Stdin    string            `json:"stdin,omitempty"`
	ExitCode int               `json:"exit_code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
	Vault    map[string]string `json:"vault,omitempty"`
	// Requests are the HTTP requests the fake Paperless served in the step.
	Requests []string `json:"requests,omitempty"`
}

type pvCase struct {
	Name            string            `json:"name"`
	Files           map[string]string `json:"files,omitempty"`
	RootFiles       map[string]string `json:"root_files,omitempty"`
	SkipIndex       bool              `json:"skip_index,omitempty"`
	HomeFiles       map[string]string `json:"home_files,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	SnapshotVault   bool              `json:"snapshot_vault,omitempty"`
	VolatileHistory bool              `json:"volatile_history,omitempty"`
	Paperless       *pvFake           `json:"paperless,omitempty"`
	Steps           []pvStep          `json:"steps"`
}

type pvFixture struct {
	SchemaVersion int      `json:"schema_version"`
	Cases         []pvCase `json:"cases"`
}

func pvBoth(args ...string) []pvStep {
	return []pvStep{{Args: args}, {Args: append([]string{"--json"}, args...)}}
}
func pvJSON(args ...string) []pvStep { return []pvStep{{Args: append([]string{"--json"}, args...)}} }
func pvText(args ...string) []pvStep { return []pvStep{{Args: args}} }
func pvConcat(groups ...[]pvStep) []pvStep {
	var out []pvStep
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}

var (
	pvLogTimeRE = regexp.MustCompile(`\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}`)
	pvRunIDRE   = regexp.MustCompile(`paperless-\d{8}T\d{6}\.\d{9}Z`)
	pvTempRE    = regexp.MustCompile(`\.tmp-\d+|symingest-import-\d+`)
	pvImportRE  = regexp.MustCompile(`Imported from CSV on \d{4}-\d{2}-\d{2}`)
	// A YAML type error quotes the first characters of the offending scalar;
	// for a path argument that is the host's root prefix.
	// Copying a directory as an archive file fails in read(2) on macOS and in
	// copy_file_range(2) on Linux.
	pvDirCopyRE         = regexp.MustCompile(`(copy archive file: )[^\n]*: is a directory`)
	pvTruncatedScalarRE = regexp.MustCompile("(cannot unmarshal !!str `)[^`\n]*\\.\\.\\.(` into)")
)

// pvNormalize hides the wall clock and the process-specific names these
// commands write on top of the shared bodies normalization.
func pvNormalize(text string) string {
	text = pvLogTimeRE.ReplaceAllString(text, "$$LOGTIME")
	text = pvRunIDRE.ReplaceAllString(text, "$$RUNID")
	text = pvTempRE.ReplaceAllString(text, "$$TMP")
	text = pvDirCopyRE.ReplaceAllString(text, "${1}$$DIRERR")
	text = pvTruncatedScalarRE.ReplaceAllString(text, "${1}$$TRUNC${2}")
	return pvImportRE.ReplaceAllString(text, "Imported from CSV on $$DATE")
}

// ---- loopback Paperless-ngx ------------------------------------------------

type pvFakeServer struct {
	mu       sync.Mutex
	requests []string
	server   *httptest.Server
}

func (s *pvFakeServer) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.requests
	s.requests = nil
	return out
}

func startPVFake(t *testing.T, fake *pvFake) *pvFakeServer {
	t.Helper()
	state := &pvFakeServer{}
	state.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		state.requests = append(state.requests, r.Method+" "+r.URL.RequestURI())
		state.mu.Unlock()
		if r.Header.Get("Authorization") != "Token "+fake.Token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"detail":"Invalid token."}`))
			return
		}
		query := r.URL.Query()
		path := r.URL.Path
		switch {
		case path == "/api/documents/":
			docs := []map[string]any{}
			for _, doc := range fake.Documents {
				since := query.Get("created__date__gte")
				if since != "" {
					if created, _ := doc["created_date"].(string); created < since {
						continue
					}
				}
				docs = append(docs, doc)
			}
			size := fake.PageSize
			if size <= 0 {
				size = 100
			}
			if value, err := strconv.Atoi(query.Get("page_size")); err == nil && value > 0 && value < size {
				size = value
			}
			page := 1
			if value, err := strconv.Atoi(query.Get("page")); err == nil && value > 0 {
				page = value
			}
			start := (page - 1) * size
			if start > len(docs) {
				start = len(docs)
			}
			end := start + size
			if end > len(docs) {
				end = len(docs)
			}
			var next any
			if end < len(docs) {
				values := url.Values{}
				for key, list := range query {
					values[key] = list
				}
				values.Set("page", strconv.Itoa(page+1))
				next = "http://" + r.Host + "/api/documents/?" + values.Encode()
			}
			pvWriteJSON(w, map[string]any{"count": len(docs), "next": next, "previous": nil, "results": docs[start:end]})
		case strings.HasPrefix(path, "/api/documents/") && strings.HasSuffix(path, "/download/"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/documents/"), "/download/")
			for _, failing := range fake.FailDownload {
				if strconv.Itoa(failing) == id {
					http.Error(w, "boom", http.StatusInternalServerError)
					return
				}
			}
			file, ok := fake.Files[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", file.ContentType)
			if file.Disposition != "" {
				w.Header().Set("Content-Disposition", file.Disposition)
			}
			_, _ = w.Write([]byte(file.Body))
		case strings.HasPrefix(path, "/api/") && strings.Count(path, "/") == 3:
			kind := strings.Trim(strings.TrimPrefix(path, "/api/"), "/")
			results := fake.Entities[kind]
			if results == nil {
				results = []map[string]any{}
			}
			pvWriteJSON(w, map[string]any{"count": len(results), "next": nil, "previous": nil, "results": results})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(state.server.Close)
	return state
}

func pvWriteJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// ---- observation -------------------------------------------------------------

func TestPortCLIPaperviewsBodiesProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("CLI paperviews fixture is recorded and replayed on Unix hosts")
	}
	binary := processFixtureBinary(t)
	fixture := pvFixture{SchemaVersion: 1}
	for _, c := range pvCases() {
		fixture.Cases = append(fixture.Cases, observePVCase(t, binary, c))
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), paperviewsFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
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
		t.Fatalf("CLI paperviews fixture is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observePVCase(t *testing.T, binary string, c pvCase) pvCase {
	t.Helper()
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	home := filepath.Join(root, "home")
	vault := filepath.Join(root, "vault")
	cwd := filepath.Join(root, "cwd")
	for _, directory := range []string{home, vault, cwd} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles := func(base string, files map[string]string) {
		for name, content := range files {
			target := filepath.Join(base, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFiles(vault, c.Files)
	writeFiles(root, c.RootFiles)
	closedPort := closedLoopbackPort(t)
	if c.HomeFiles == nil {
		c.HomeFiles = cliErrorsHomeFiles()
	}
	writeCLIErrorsHomeFiles(t, home, c.HomeFiles, closedPort, startCLIErrorsEmbeddingServer(t).URL)
	var fake *pvFakeServer
	paperlessURL := "http://127.0.0.1:" + closedPort
	if c.Paperless != nil {
		fake = startPVFake(t, c.Paperless)
		paperlessURL = fake.server.URL
	}
	materialize := func(value string) string {
		value = strings.ReplaceAll(value, "$CLOSED_PORT", closedPort)
		value = strings.ReplaceAll(value, "$PAPERLESS_URL", paperlessURL)
		return materializePortProcessArg(value, root, string(filepath.Separator))
	}
	baseEnv := func() []string {
		environment := cleanDuplicatesEnv(os.Environ())
		return append(environment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
			"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
			"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
			"XDG_DATA_HOME="+filepath.Join(home, "data"))
	}
	environment := baseEnv()
	for name, value := range c.Env {
		environment = append(environment, name+"="+materialize(value))
	}
	run := func(env []string, args []string, input string) (int, string, string) {
		cmd := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Dir = cwd
		cmd.Stdin = strings.NewReader(input)
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run Go CLI %v: %v", args, err)
			}
			code = exit.ExitCode()
		}
		return code, stdout.String(), stderr.String()
	}
	if !c.SkipIndex {
		if code, _, stderr := run(baseEnv(), []string{"--vault", vault, "index"}, ""); code != 0 {
			t.Fatalf("setup index for %s exited %d: %s", c.Name, code, stderr)
		}
	}
	observed := c
	observed.Steps = nil
	for _, step := range c.Steps {
		args := append([]string{"--vault", vault}, step.Args...)
		for i := range args {
			args[i] = materialize(args[i])
		}
		code, stdout, stderr := run(environment, args, materialize(step.Stdin))
		recorded := pvStep{
			Args:     step.Args,
			Stdin:    step.Stdin,
			ExitCode: code,
			Stdout:   pvNormalize(normalizeBodiesText(c.VolatileHistory, normalizeCLIErrors(stdout, root))),
			Stderr:   pvNormalize(normalizeBodiesText(c.VolatileHistory, normalizeCLIErrors(stderr, root))),
		}
		if fake != nil {
			recorded.Requests = fake.take()
		}
		if c.SnapshotVault {
			snapshot := snapshotBodiesVault(t, vault, root, c.VolatileHistory)
			recorded.Vault = map[string]string{}
			keys := make([]string, 0, len(snapshot))
			for key := range snapshot {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				recorded.Vault[pvNormalize(key)] = pvNormalize(snapshot[key])
			}
		}
		observed.Steps = append(observed.Steps, recorded)
	}
	return observed
}

// ---- cases ---------------------------------------------------------------------

func pvCases() []pvCase {
	var cases []pvCase
	cases = append(cases, pvPaperlessImportCases()...)
	cases = append(cases, pvPaperlessMigrateCases()...)
	cases = append(cases, pvViewsCases()...)
	return cases
}

func pvManifest(entries ...string) string {
	return "[" + strings.Join(entries, ",") + "]"
}

func pvPaperlessImportCases() []pvCase {
	vault := bodiesVault()
	vault["asn-holder.md"] = "---\ntitle: Holder\nasn: 7\n---\nowns ASN 7\n"
	entry := func(id int, title, extra string) string {
		return fmt.Sprintf(`{"id":%d,"title":%q,"content":"content of %d","checksum":"sum%d","original_file_name":"doc%d.pdf"%s}`, id, title, id, id, id, extra)
	}
	manifest := pvManifest(
		entry(1, "Invoice March", `,"correspondent":"ACME","document_type":"invoice","tags":["finance","tax"],"created":"2024-03-05T09:00:00Z","added":"2024-03-05T09:05:00Z","modified":"2024-03-06T10:00:00Z","archive_serial_number":42,"notes":[{"note":"checked by hand","created":"2024-03-07T08:00:00Z"},{"note":"second"}]`),
		entry(2, "Needs ASN 7", `,"archive_serial_number":7`),
		entry(3, "Receipt/Jan: 2024?", `,"archived_file_name":"receipt-archived.pdf","extra_field":"kept"`),
		entry(4, "   ", `,"tags":[]`),
		entry(5, "Missing archive file", `,"archive_serial_number":43`),
	)
	root := map[string]string{
		"export/manifest.json":           manifest,
		"export/doc1.pdf":                "%PDF-1.4 invoice march\n",
		"export/receipt-archived.pdf":    "%PDF-1.4 receipt\n",
		"export/doc2.pdf":                "%PDF-1.4 two\n",
		"export/doc4.pdf":                "%PDF-1.4 four\n",
		"badmanifest/manifest.json":      "{not json",
		"emptymanifest/manifest.json":    "[]",
		"nullmanifest/manifest.json":     "null",
		"notexport/file.txt":             "no manifest here\n",
		"dirarchive/manifest.json":       pvManifest(entry(9, "Dir as archive", `,"archived_file_name":"sub"`)),
		"dirarchive/sub/inner.txt":       "inner\n",
		"export2/manifest.json":          pvManifest(entry(1, "Invoice March renamed", `,"correspondent":"ACME","archive_serial_number":42`)),
		"export2/doc1.pdf":               "%PDF-1.4 changed bytes\n",
		"export/not-a-document.txt":      "stray\n",
		"nullfields/manifest.json":       `[{"id":11,"title":"Nulls","content":null,"tags":null,"notes":null,"correspondent":null,"checksum":null}]`,
		"unicode/manifest.json":          pvManifest(entry(12, "Ünï <&> “quoted”", `,"correspondent":"Müller & Söhne","tags":["ä","b c"]`)),
		"unicode/doc12.pdf":              "%PDF-1.4 unicode\n",
		"bigid/manifest.json":            pvManifest(entry(2147483648, "Big id", ``)),
		"negative/manifest.json":         pvManifest(entry(-5, "Negative", `,"archive_serial_number":-3`)),
		"asn-zero/manifest.json":         pvManifest(entry(13, "Zero ASN", `,"archive_serial_number":0`)),
		"emptytitle/manifest.json":       pvManifest(entry(14, "", ``)),
		"dotsonly/manifest.json":         pvManifest(entry(15, "...", ``)),
		"duplicate-ids/manifest.json":    pvManifest(entry(16, "First 16", ``), entry(16, "Second 16", ``)),
		"nested/manifest.json":           pvManifest(entry(17, "Nested archive", `,"original_file_name":"deep/er/file.pdf"`)),
		"nested/deep/er/file.pdf":        "%PDF-1.4 deep\n",
		"parent-escape/manifest.json":    pvManifest(entry(18, "Escape", `,"original_file_name":"../export/doc1.pdf"`)),
		"crlf/manifest.json":             "[\r\n{\"id\":19,\"title\":\"Crlf\",\"content\":\"a\\r\\nb\"}\r\n]\r\n",
		"trailing-data/manifest.json":    "[] []",
		"bom/manifest.json":              "\ufeff[]",
		"whitespace/manifest.json":       " \n\t[ ]\n",
		"empty-file/manifest.json":       "",
		"top-level-object/manifest.json": "{}",
	}
	return []pvCase{{
		Name: "paperless-import", Files: vault, RootFiles: root, SnapshotVault: true, VolatileHistory: true,
		Steps: pvConcat(
			pvBoth("paperless", "import", "$ROOT/export", "--dry-run"),
			pvBoth("paperless", "import", "$ROOT/export"),
			pvJSON("docs", "list"),
			pvBoth("paperless", "import", "$ROOT/export"),
			pvBoth("paperless", "import", "$ROOT/export2"),
			pvJSON("docs", "list"),
			pvBoth("paperless", "import", "$ROOT/export2", "--dry-run"),
			pvBoth("paperless", "import", "$ROOT/missing-dir"),
			pvBoth("paperless", "import", "$ROOT/export/manifest.json"),
			pvBoth("paperless", "import", "$ROOT/notexport"),
			pvBoth("paperless", "import", "$ROOT/badmanifest"),
			pvBoth("paperless", "import", "$ROOT/emptymanifest"),
			pvBoth("paperless", "import", "$ROOT/nullmanifest"),
			pvBoth("paperless", "import", "$ROOT/dirarchive"),
			pvBoth("paperless", "import", "$ROOT/nullfields"),
			pvBoth("paperless", "import", "$ROOT/unicode"),
			pvBoth("paperless", "import", "$ROOT/bigid"),
			pvBoth("paperless", "import", "$ROOT/negative"),
			pvBoth("paperless", "import", "$ROOT/asn-zero"),
			pvBoth("paperless", "import", "$ROOT/emptytitle"),
			pvBoth("paperless", "import", "$ROOT/dotsonly"),
			pvBoth("paperless", "import", "$ROOT/duplicate-ids"),
			pvBoth("paperless", "import", "$ROOT/nested"),
			pvBoth("paperless", "import", "$ROOT/parent-escape"),
			pvBoth("paperless", "import", "$ROOT/crlf"),
			pvBoth("paperless", "import", "$ROOT/trailing-data"),
			pvBoth("paperless", "import", "$ROOT/bom"),
			pvBoth("paperless", "import", "$ROOT/whitespace"),
			pvBoth("paperless", "import", "$ROOT/empty-file"),
			pvBoth("paperless", "import", "$ROOT/top-level-object"),
			pvJSON("docs", "list"),
			pvBoth("paperless", "import"),
			pvBoth("paperless", "import", "a", "b"),
		),
	}, {
		Name: "paperless-import-unindexed", Files: vault, RootFiles: root, SkipIndex: true, SnapshotVault: true, VolatileHistory: true,
		Steps: pvConcat(
			pvBoth("paperless", "import", "$ROOT/export"),
			pvJSON("docs", "list"),
		),
	}}
}

func pvFakePaperless() *pvFake {
	return &pvFake{
		Token: "fake-paperless-token-0123456789",
		Documents: []map[string]any{
			{"id": 101, "title": "Invoice March", "created": "2024-03-05T09:00:00Z", "created_date": "2024-03-05", "added": "2024-03-05T09:05:00Z", "modified": "2024-03-06T10:00:00Z",
				"file_type": ".txt", "mime_type": "text/plain", "original_file_name": "invoice-march.txt", "archived_file_name": "invoice-march-archived.txt", "page_count": 1,
				"correspondent": 1, "document_type": map[string]any{"id": 1, "name": "invoice"}, "storage_path": 1, "tags": []any{1, 2}},
			{"id": 102, "title": "Letter February", "created": "2024-02-10T08:00:00Z", "created_date": "2024-02-10", "added": "2024-02-10T08:05:00Z", "modified": "2024-02-10T08:05:00Z",
				"file_type": ".txt", "mime_type": "text/plain", "original_file_name": "letter-february.txt", "page_count": 2, "tags": []any{2}},
			{"id": 103, "title": "Receipt/Jan: 2024", "created": "2024-01-15T00:00:00Z", "created_date": "2024-01-15", "added": "2024-01-15T00:05:00Z", "modified": "2024-01-16T00:00:00Z",
				"file_type": ".md", "mime_type": "text/markdown", "original_file_name": "receipt.md", "page_count": 1, "correspondent": map[string]any{"id": 1, "name": "ACME"}, "tags": []any{map[string]any{"id": 1, "name": "finance"}}},
			{"id": 104, "title": "Broken download", "created": "2023-12-01T00:00:00Z", "created_date": "2023-12-01", "added": "2023-12-01T00:05:00Z", "modified": "2023-12-01T00:05:00Z",
				"file_type": ".txt", "mime_type": "text/plain", "original_file_name": "broken.txt", "page_count": 1},
		},
		Entities: map[string][]map[string]any{
			"tags":           {{"id": 1, "name": "finance", "slug": "finance"}, {"id": 2, "name": "tax", "slug": "tax"}},
			"correspondents": {{"id": 1, "name": "ACME", "slug": "acme"}},
			"document_types": {{"id": 1, "name": "invoice", "slug": "invoice"}},
			"storage_paths":  {{"id": 1, "name": "Finance/Invoices", "slug": "finance-invoices"}},
		},
		Files: map[string]pvFakeFile{
			"101": {ContentType: "text/plain; charset=utf-8", Disposition: `attachment; filename="invoice-march.txt"`, Body: "Invoice March\nAmount due 120 EUR\nmarker-invoice-march\n"},
			"102": {ContentType: "text/plain", Body: "Dear reader, this is the february letter.\nmarker-letter-february\n"},
			"103": {ContentType: "text/markdown", Disposition: `attachment; filename*=UTF-8''receipt%20jan.md`, Body: "# Receipt\n\nmarker-receipt-january\n"},
		},
		FailDownload: []int{104},
	}
}

func pvPaperlessMigrateCases() []pvCase {
	vault := map[string]string{"existing.md": "---\ntitle: Existing\n---\nexisting note\n"}
	envVault := map[string]string{"SYMINGEST_VAULT": "$ROOT/vault"}
	args := func(extra ...string) []string {
		return append([]string{"paperless", "migrate", "--base-url", "$PAPERLESS_URL", "--token", "fake-paperless-token-0123456789"}, extra...)
	}
	both := func(extra ...string) []pvStep { return pvBoth(args(extra...)...) }
	paged := pvFakePaperless()
	paged.PageSize = 2
	return []pvCase{{
		Name: "paperless-migrate-flags", Files: vault, Env: envVault, Paperless: pvFakePaperless(), SnapshotVault: true, VolatileHistory: true,
		Steps: pvConcat(
			pvBoth("paperless", "migrate"),
			pvBoth("paperless", "migrate", "--base-url", "$PAPERLESS_URL"),
			pvBoth("paperless", "migrate", "--token", "fake-paperless-token-0123456789"),
			both("--since", "nonsense"),
			both("--since", "2024-13-01T00:00:00Z"),
			both("--since", "2024-02-30T00:00:00Z"),
			both("--since", "2024-02-01"),
			both("--since", "2024-02-01T00:00:00"),
			both("--since", "2024-02-01T00:00:00Zjunk"),
			both("--since", "2024-02-01T25:00:00Z"),
			both("--since", "2024-2-01T00:00:00Z"),
			both("--since", "2024-02-01T00:00:00+25:00"),
			pvBoth("paperless", "migrate", "--base-url", "$PAPERLESS_URL", "--token", "wrong-token"),
			pvBoth("paperless", "migrate", "--base-url", "$PAPERLESS_URL", "--token", "fake-paperless-token-0123456789", "extra"),
		),
	}, {
		Name: "paperless-migrate-no-vault", Files: vault, Paperless: pvFakePaperless(), SnapshotVault: true,
		Steps: pvBoth(args()...),
	}, {
		Name: "paperless-migrate-run", Files: vault, Env: envVault, Paperless: pvFakePaperless(), SnapshotVault: true, VolatileHistory: true,
		Steps: pvConcat(
			both("--plan"),
			both("--dry-run"),
			both("--plan", "--dry-run", "--limit", "2"),
			both("--limit", "2", "--dry-run"),
			both("--since", "2024-02-01T00:00:00Z", "--dry-run"),
			both("--since", "0001-01-01T00:00:00Z", "--plan"),
			both("--limit", "3"),
			both("--limit", "3"),
			both(),
			both("--retry-failed"),
			both("--resume"),
			both("--deep-verify", "--dry-run"),
			both("--concurrency", "0", "--limit", "1"),
			both("--concurrency", "-4", "--limit", "-1", "--dry-run"),
		),
	}, {
		Name: "paperless-migrate-paged", Files: vault, Env: envVault, Paperless: paged, SnapshotVault: true, VolatileHistory: true,
		Steps: pvConcat(
			both("--dry-run"),
			both("--limit", "3", "--dry-run"),
			both(),
		),
	}, {
		Name: "paperless-migrate-config", Files: vault, Env: map[string]string{"SYMINGEST_VAULT": "$ROOT/other-vault", "SYMINGEST_ARCHIVE_PATH": "$ROOT/archive-elsewhere", "SYMINGEST_DB_PATH": "$ROOT/db/ingest.db"},
		Paperless: pvFakePaperless(), SnapshotVault: true, VolatileHistory: true,
		RootFiles: map[string]string{"other-vault/keep.md": "keep\n"},
		Steps: pvConcat(
			both("--limit", "2"),
			both("--limit", "2"),
			both("--dry-run"),
		),
	}}
}

func pvViewsVault() map[string]string {
	vault := bodiesVault()
	vault["invoices/one.md"] = "---\ntitle: One\nstatus: open\namount: 10\ndue_date: 2026-03-01\ntags: [inv, q1]\n---\nfirst links to [[invoices/two]]\n"
	vault["invoices/two.md"] = "---\ntitle: Two\nstatus: paid\namount: 20\ndue_date: 2026-02-01\ntags: [paid]\n---\nsecond\n"
	vault["invoices/three.md"] = "---\ntitle: Three\nstatus: open\namount: 5.5\ndue_date: 2026-01-15\ncorrespondent: ACME\n---\nthird\n"
	vault["invoices/sub/four.md"] = "---\ntitle: Four\nstatus: Open\namount: abc\n---\nfourth\n"
	vault["templates/inv.md"] = "---\ntitle: \"{{title}}\"\nstatus: draft\n---\n\nTemplate body\n"
	vault["templates/plain.md"] = "Plain template for {{title}}\n"
	return vault
}

func pvViewsCases() []pvCase {
	const (
		viewAll    = `{"id":"all","name":"All notes","columns":["title","status"]}`
		viewOpen   = `{"id":"open","name":"Open","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"open"}],"columns":["title","amount"]}`
		viewPaid   = `{"id":"paid","name":"Paid","source":"invoices/","filters":[{"key":"status","value":"paid"}],"filter_group":{"operator":"and","filters":[{"key":"amount","operator":"gt","value":"5"}]}}`
		viewTag    = `{"id":"tagged","name":"Tagged","source":"tag:inv"}`
		viewTagHsh = `{"id":"tagged2","name":"Tagged hash","source":"tag:#PAID"}`
		viewNB     = `{"id":"nbview","name":"Notebook view","source":"notebook:nb"}`
		viewComp   = `{"id":"comp","name":"Computed","source":"invoices/","columns":["title","label","links"],"computed":{"label":{"formula":"{title} costs {amount}"},"links":{"rollup":"count"}}}`
		viewRoll   = `{"id":"roll","name":"Rollups","source":"invoices/","computed":{"n":{"rollup":"count"},"s":{"rollup":"sum(links.amount)"},"l":{"rollup":"list(links.title)"},"x":{"rollup":"other"}}}`
		viewBadDir = `{"id":"baddir","name":"Bad dir","source":"nowhere/"}`
		viewFile   = `{"id":"filedir","name":"File dir","source":"invoices/one.md"}`
		viewEscape = `{"id":"escape","name":"Escape","source":"../outside/"}`
		viewEmptyT = `{"id":"emptytag","name":"Empty tag","source":"tag:"}`
		viewEmptyN = `{"id":"emptynb","name":"Empty notebook","source":"notebook:"}`
		viewMissNB = `{"id":"missnb","name":"Missing notebook","source":"notebook:ghost"}`
		viewDSet   = `{"id":"dset","name":"Dataset view","source":"dataset:rows"}`
		viewEmptyD = `{"id":"emptyds","name":"Empty dataset","source":"dataset:"}`
		viewMissD  = `{"id":"missds","name":"Missing dataset","source":"dataset:ghost"}`
		viewNone   = `{"id":"none","name":"No match","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"nothing"}]}`
		viewOps    = `{"id":"ops","name":"Ops","source":"invoices/","filters":[{"key":"title","operator":"contains","value":"O"}],"filter_group":{"operator":"any","filters":[{"key":"amount","operator":"lt","value":"6"},{"key":"status","operator":"in","value":"paid, closed"}]}}`
		viewDates  = `{"id":"dates","name":"Dates","source":"invoices/","filters":[{"key":"due_date","operator":"on_or_after","value":"2026-02-01"}]}`
		viewNotEq  = `{"id":"noteq","name":"Not equal","source":"invoices/","filters":[{"key":"status","operator":"not_equals","value":"OPEN"}]}`
		viewEmpty  = `{"id":"empty","name":"Empty op","source":"invoices/","filters":[{"key":"correspondent","operator":"is_empty"}]}`
		viewNested = `{"id":"nested","name":"Nested","source":"invoices/","filter_group":{"operator":"all","groups":[{"operator":"any","filters":[{"key":"status","value":"paid"},{"key":"status","value":"open"}]}],"filters":[{"key":"amount","operator":"gte","value":"5.5"}]}}`
	)
	vault := pvViewsVault()
	save := func(spec string) []pvStep { return pvJSON("views", "save", spec) }
	var cases []pvCase

	cases = append(cases, pvCase{Name: "views-exec", Files: vault, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		pvJSON("notebook", "new", "Nb"),
		pvJSON("notebook", "add-source", "nb", "invoices/one.md"),
		pvJSON("notebook", "add-source", "nb", "invoices/two.md"),
		pvJSON("notebook", "add-source", "nb", "missing.md"),
		save(viewAll), save(viewOpen), save(viewPaid), save(viewTag), save(viewTagHsh), save(viewNB), save(viewComp), save(viewRoll),
		save(viewBadDir), save(viewFile), save(viewEscape), save(viewEmptyT), save(viewEmptyN), save(viewMissNB), save(viewEmptyD), save(viewMissD),
		save(viewNone), save(viewOps), save(viewDates), save(viewNotEq), save(viewEmpty), save(viewNested),
		pvBoth("views", "exec", "all"),
		pvBoth("views", "exec", "open"),
		pvBoth("views", "exec", "paid"),
		pvBoth("views", "exec", "tagged"),
		pvBoth("views", "exec", "tagged2"),
		pvBoth("views", "exec", "nbview"),
		pvBoth("views", "exec", "comp"),
		pvBoth("views", "exec", "roll"),
		pvBoth("views", "exec", "baddir"),
		pvBoth("views", "exec", "filedir"),
		pvBoth("views", "exec", "escape"),
		pvBoth("views", "exec", "emptytag"),
		pvBoth("views", "exec", "emptynb"),
		pvBoth("views", "exec", "missnb"),
		pvBoth("views", "exec", "emptyds"),
		pvBoth("views", "exec", "missds"),
		pvBoth("views", "exec", "none"),
		pvBoth("views", "exec", "ops"),
		pvBoth("views", "exec", "dates"),
		pvBoth("views", "exec", "noteq"),
		pvBoth("views", "exec", "empty"),
		pvBoth("views", "exec", "nested"),
		pvBoth("views", "exec", "missing"),
		pvBoth("views", "exec", ""),
		pvBoth("views", "exec"),
		pvBoth("views", "exec", "a", "b"),
	)})

	cases = append(cases, pvCase{Name: "views-exec-unindexed", Files: vault, SkipIndex: true, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		save(viewAll), save(viewOpen),
		pvBoth("views", "exec", "all"),
		pvBoth("views", "exec", "open"),
	)})

	cases = append(cases, pvCase{Name: "views-exec-dataset", Files: vault, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		pvJSON("dataset", "sync", "rows", "--rows", `[{"identity":"r1","values":{"id":"r1","name":"Alpha","qty":3}},{"identity":"r2","values":{"id":"r2","name":"Beta","qty":8}},{"identity":"r3","values":{"id":"r3","name":"Gamma","qty":1}}]`, "--provenance", `{"imported_at":"2026-01-01T00:00:00Z","source_name":"producer.csv","source_sha256":"sha-rows"}`, "--source-name", "fixture", "--schema", `{"id":{"type":"text"},"name":{"type":"text"},"qty":{"type":"number"}}`, "--identity-field", "id"),
		save(viewDSet),
		save(`{"id":"dset-big","name":"Big rows","source":"dataset:rows","filters":[{"key":"qty","operator":"gt","value":"2"}],"sorts":[{"key":"qty","ascending":false}]}`),
		pvBoth("views", "exec", "dset"),
		pvBoth("views", "exec", "dset-big"),
	)})

	cases = append(cases, pvCase{Name: "views-new-entry", Files: vault, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		save(`{"id":"tpl","name":"With template","source":"invoices/","template":{"ref":"inv","defaults":{"priority":"high"}}}`),
		save(`{"id":"eq","name":"Equality","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"open"},{"key":"amount","operator":"gt","value":"1"}]}`),
		save(`{"id":"plaintpl","name":"Plain template","template":{"ref":"plain"}}`),
		save(`{"id":"bare","name":"Bare"}`),
		save(`{"id":"badtpl","name":"Missing template","template":{"ref":"ghost"}}`),
		save(`{"id":"asnkey","name":"ASN default","template":{"defaults":{"asn":"5"}}}`),
		pvBoth("views", "new-entry", "tpl", "From Template"),
		pvBoth("views", "new-entry", "tpl", "From Template"),
		pvBoth("views", "new-entry", "eq", "Equal Entry"),
		pvBoth("views", "new-entry", "plaintpl", "Plain Entry"),
		pvBoth("views", "new-entry", "bare", "Bare Entry"),
		pvBoth("views", "new-entry", "badtpl", "No Template File"),
		pvBoth("views", "new-entry", "asnkey", "Asn Entry"),
		pvBoth("views", "new-entry", "bare", ""),
		pvBoth("views", "new-entry", "bare", "../escape"),
		pvBoth("views", "new-entry", "bare", "With/Slash"),
		pvBoth("views", "new-entry", "missing", "Title"),
		pvBoth("views", "new-entry", "bare"),
		pvBoth("views", "new-entry"),
		pvBoth("views", "new-entry", "a", "b", "c"),
		pvBoth("views", "exec", "tpl"),
		pvBoth("views", "exec", "eq"),
	)})

	csvText := "Name,Status,Amount,Due Date,Tags\nAlpha,open,10,2026-01-02,\"a, b\"\nBeta,paid,20.5,2026-02-03,c\n\n\"Gamma, Inc\",open,3,,\n,open,4,2026-03-04,d\nShort,row\nDelta,paid,5,2026-04-05,e,extra\n"
	cases = append(cases, pvCase{Name: "views-csv-export", Files: vault, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		save(viewAll), save(viewOpen), save(viewPaid), save(viewComp), save(viewNone), save(viewDSet), save(viewBadDir),
		save(`{"id":"nocols","name":"No columns","source":"invoices/"}`),
		save(`{"id":"blankcols","name":"Blank columns","source":"invoices/","columns":[" ","",""]}`),
		save(`{"id":"pathcols","name":"Path columns","source":"invoices/","columns":["_title","_path","status","nothing"]}`),
		save(`{"id":"tagcols","name":"Tag columns","source":"tag:inv","columns":["title","tags"]}`),
		pvText("views", "export-csv", "all"),
		pvText("views", "export-csv", "open"),
		pvText("views", "export-csv", "paid"),
		pvText("views", "export-csv", "comp"),
		pvText("views", "export-csv", "none"),
		pvText("views", "export-csv", "nocols"),
		pvText("views", "export-csv", "blankcols"),
		pvText("views", "export-csv", "pathcols"),
		pvText("views", "export-csv", "tagcols"),
		pvText("views", "export-csv", "dset"),
		pvText("views", "export-csv", "baddir"),
		pvText("views", "export-csv", "missing"),
		pvJSON("views", "export-csv", "open"),
		pvBoth("views", "export-csv", "open", "--output", "$ROOT/vault/exports/open.csv"),
		pvBoth("views", "export-csv", "open", "--output=$ROOT/vault/exports/open2.csv"),
		pvBoth("views", "export-csv", "all", "--output", "$ROOT/vault/all.csv", "--output", "$ROOT/vault/all-last.csv"),
		pvBoth("views", "export-csv", "open", "--output", "$ROOT/vault/nodir/missing/open.csv"),
		pvText("--output", "$ROOT/vault/before.csv", "views", "export-csv", "all"),
		pvText("--output", "json", "views", "export-csv", "all"),
		pvBoth("views", "export-csv"),
		pvBoth("views", "export-csv", "a", "b"),
	)})

	cases = append(cases, pvCase{Name: "views-csv-import", Files: vault, RootFiles: map[string]string{
		"cwd/data.csv":     csvText,
		"cwd/crlf.csv":     "Title,Note\r\nOne,first\r\nTwo,\"multi\r\nline\"\r\n",
		"cwd/lazy.csv":     "name,desc\nQuote,he said \"hi\" there\n\"unterminated,x\n",
		"cwd/empty.csv":    "",
		"cwd/headers.csv":  "Name,Amount\n",
		"cwd/tabs.csv":     "Name\tAmount\nTab\t5\n",
		"cwd/select.csv":   "Name,Kind,Qty,When\nA,x,1,2026-01-01\nB,y,2,2026-01-02\nC,x,3,2026-01-03\nD,x,4,2026-01-04\n",
		"cwd/titles.csv":   "Subject,Code,Name\nS1,c1,N1\n,c2,N2\n,,\nlast,,\n",
		"cwd/collide.csv":  "Title\nInvoice\nInvoice\nInvoice\n",
		"cwd/slugs.csv":    "Title\n!!!\nÜber Café\n   \nA/B\n",
		"cwd/mapped.csv":   "Col One,col-two,THREE\nv1,v2,v3\n",
		"cwd/special.csv":  "Title,Tags,Status,Amount\nTagged,\"x, y ,,z\",open,1\nNoTags,,paid,2\n",
		"cwd/keys.csv":     "col 10,col 2,Col-A,created,type,title\n1,2,3,4,5,Explicit\n",
		"cwd/yaml.csv":     "Title,Note\nYes,yes\nNull,null\nNum,123\nTrue,true\nColon,\"a: b\"\nHash,\"# not comment\"\nQuote,\"it's\"\nSpace,\" lead\"\n",
		"cwd/bom.csv":      "\ufeffName,Amount\nBom,1\n",
		"cwd/dup.csv":      "Name,name,NAME\na,b,c\n",
		"cwd/noheader.csv": "single\n",
		"cwd/unicode.csv":  "Name,Ort\nMüller,Köln\n",
	}, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "imports"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "/imports/", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "imports", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "imports", "--on-collision", "skip"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "imports", "--on-collision", "error", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "imports", "--on-collision", "SKIP ", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "imports", "--on-collision", "weird"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "../outside", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/data.csv", "--folder", "..", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/crlf.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/lazy.csv"),
		pvBoth("views", "import-csv", "$ROOT/cwd/empty.csv"),
		pvBoth("views", "import-csv", "$ROOT/cwd/headers.csv", "--base", "Headers Only", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/tabs.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/select.csv", "--base", "Kinds", "--folder", "kinds"),
		pvBoth("views", "import-csv", "$ROOT/cwd/select.csv", "--base", "Kinds", "--folder", "kinds", "--apply"),
		pvJSON("views", "base", "list"),
		pvBoth("views", "import-csv", "$ROOT/cwd/titles.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/titles.csv", "--title-col", "code", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/titles.csv", "--title-col", "SUBJECT"),
		pvBoth("views", "import-csv", "$ROOT/cwd/collide.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/collide.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/slugs.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/mapped.csv", "--map", "Col One=first, col_two = second ,THREE=third", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/mapped.csv", "--map", "col_one=alias,broken,=empty,x=", "--base", "Mapped", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/special.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/keys.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/yaml.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/bom.csv", "--apply"),
		pvBoth("views", "import-csv", "$ROOT/cwd/dup.csv", "--base", "Dups"),
		pvBoth("views", "import-csv", "$ROOT/cwd/noheader.csv"),
		pvBoth("views", "import-csv", "$ROOT/cwd/unicode.csv", "--apply"),
		pvJSON("docs", "list"),
		pvJSON("views", "list"),
		pvBoth("views", "import-csv", "$ROOT/cwd/missing.csv"),
		pvBoth("views", "import-csv", "$ROOT/cwd"),
		pvBoth("views", "import-csv"),
		pvBoth("views", "import-csv", "a", "b"),
	)})

	embedBase := `{"id":"inv","path":"bases/inv.md","title":"Invoices","created":"2026-01-02T03:04:05Z","tags":["base"],"properties":{"amount":{"type":"number","label":"Amount (EUR)"},"status":{"type":"select","label":"Pipe | Status"}},"views":[{"id":"open-inv","name":"Open invoices","source":"invoices/","filters":[{"key":"status","operator":"equals","value":"open"}],"columns":["title","status","amount"]},{"id":"all-inv","name":"All invoices","source":"invoices/"},{"id":"ds-inv","name":"Dataset rows","source":"dataset:rows","columns":["name","qty"]}]}`
	embedEmpty := `{"id":"emptybase","path":"bases/emptybase.md","title":"Empty base","views":[]}`
	embedOther := `{"id":"other","path":"bases/other.md","title":"Other","views":[{"id":"only","name":"Only view","source":"invoices/sub/"}]}`
	cases = append(cases, pvCase{Name: "views-embed", Files: vault, RootFiles: map[string]string{
		"cwd/spec.yaml":  "base: inv\nview: all-inv\nlimit: 2\n",
		"cwd/spec2.yaml": "base: Invoices\nview: OPEN INVOICES\ncap: 1\ncolumns: [title, amount, _path]\n",
		"cwd/list.yaml":  "- a\n- b\n",
		"cwd/num.yaml":   "base: 5\nlimit: 3\n",
		"cwd/empty.yaml": "",
		"cwd/limit.yaml": "base: inv\nlimit: abc\n",
	}, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		pvJSON("views", "base", "save", embedBase),
		pvJSON("views", "base", "save", embedEmpty),
		pvJSON("views", "base", "save", embedOther),
		pvJSON("dataset", "sync", "rows", "--rows", `[{"identity":"r1","values":{"id":"r1","name":"Alpha","qty":3}},{"identity":"r2","values":{"id":"r2","name":"Beta","qty":8}},{"identity":"r3","values":{"id":"r3","name":"Gamma","qty":1}}]`, "--provenance", `{"imported_at":"2026-01-01T00:00:00Z","source_name":"producer.csv","source_sha256":"sha-rows"}`, "--source-name", "fixture", "--schema", `{"id":{"type":"text"},"name":{"type":"text"},"qty":{"type":"number"}}`, "--identity-field", "id"),
		pvBoth("views", "embed", "base: inv"),
		pvBoth("views", "embed", "base: inv\nview: all-inv\nlimit: 2"),
		pvBoth("views", "embed", "base: inv\nview: all-inv\ncap: 3"),
		pvBoth("views", "embed", "base: inv\nview: open-inv"),
		pvBoth("views", "embed", "base: Invoices\nview: Open Invoices\ncolumns: [title, status]"),
		pvBoth("views", "embed", "base: inv\nview: ds-inv"),
		pvBoth("views", "embed", "base: inv\nview: ds-inv\nlimit: 2"),
		pvBoth("views", "embed", "base: inv\nview: nope"),
		pvBoth("views", "embed", "base: emptybase"),
		pvBoth("views", "embed", "base: ghost"),
		pvBoth("views", "embed", "base: bases/other.md"),
		pvBoth("views", "embed", "limit: 3"),
		pvBoth("views", "embed", "base: \"  \""),
		pvBoth("views", "embed", "base: inv\nbase: other"),
		pvBoth("views", "embed", "base: 5"),
		pvBoth("views", "embed", "$ROOT/cwd/spec.yaml"),
		pvBoth("views", "embed", "$ROOT/cwd/spec2.yaml"),
		pvBoth("views", "embed", "$ROOT/cwd/empty.yaml"),
		pvBoth("views", "embed", "$ROOT/cwd/num.yaml"),
		pvBoth("views", "embed", "$ROOT/cwd/list.yaml"),
		pvBoth("views", "embed", "$ROOT/cwd/limit.yaml"),
		pvBoth("views", "embed", "$ROOT"),
		pvBoth("views", "embed", "just text"),
		pvBoth("views", "embed", "[a]"),
		pvBoth("views", "embed", "123"),
		pvBoth("views", "embed", "true"),
		pvBoth("views", "embed", "--spec", "base: inv\nview: all-inv\nlimit: 1"),
		pvBoth("views", "embed", "--base", "inv"),
		pvBoth("views", "embed", "--base", "inv", "--view", "open-inv", "--limit", "1"),
		pvBoth("views", "embed", "--base", "inv", "--limit", "0"),
		pvBoth("views", "embed", "--base", "inv", "--limit", "-3"),
		pvBoth("views", "embed", "--base", "inv", "--limit", "0x2", "--view", "all-inv"),
		pvBoth("views", "embed", "--spec", "base: inv", "--base", "other"),
		pvBoth("views", "embed", "--view", "all-inv"),
		pvBoth("views", "embed"),
		pvBoth("views", "embed", "a", "b"),
	)})

	cases = append(cases, pvCase{Name: "views-embed-unindexed", Files: vault, SkipIndex: true, SnapshotVault: true, VolatileHistory: true, Steps: pvConcat(
		pvJSON("views", "base", "save", embedBase),
		pvBoth("views", "embed", "base: inv\nview: open-inv"),
	)})

	return cases
}
