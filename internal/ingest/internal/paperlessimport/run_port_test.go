package paperlessimport

// Go oracle for the Paperless import orchestration: selectDocuments,
// loadLookups, Run (plan / dry-run / import / resume / retry-failed / limit /
// IDs / concurrency / checkpoints), importOne and WriteMigrationReport. A
// loopback fake Paperless API serves scripted routes; the pipeline is the real
// one with a deterministic engine, writing a real vault, archive and SQLite
// store. Each scenario records the Stats, progress and stderr output, log
// lines, the requests the server saw, the vault/archive trees, the store rows
// and the written migration report.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/extract"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/writer"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	_ "modernc.org/sqlite"
)

type runRoute struct {
	Path    string            `json:"path"`
	Page    string            `json:"page,omitempty"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"` // $BASE is replaced by the server URL
	BodyB64 string            `json:"body_b64,omitempty"`
}

type runSpec struct {
	Since               string `json:"since,omitempty"`
	DryRun              bool   `json:"dry_run,omitempty"`
	Plan                bool   `json:"plan,omitempty"`
	Resume              bool   `json:"resume,omitempty"`
	RetryFailed         bool   `json:"retry_failed,omitempty"`
	Concurrency         int    `json:"concurrency,omitempty"`
	CheckpointEvery     int    `json:"checkpoint_every,omitempty"`
	Limit               int    `json:"limit,omitempty"`
	IDs                 []int  `json:"ids,omitempty"`
	PreserveStorage     bool   `json:"preserve_storage_paths,omitempty"`
	NoProgress          bool   `json:"no_progress,omitempty"`
	ReportPath          string `json:"report_path,omitempty"`
	ReportDryRunFlag    bool   `json:"report_dry_run_flag,omitempty"`
	TargetVaultOverride string `json:"target_vault_override,omitempty"`
	// RoutesAfter replaces/extends the served routes before this run.
	RoutesAfter []runRoute `json:"routes_after,omitempty"`
}

type runScenario struct {
	ID     string     `json:"id"`
	Routes []runRoute `json:"routes"`
	Runs   []runSpec  `json:"runs"`
	// Engine: "ok" | "fail-text" (documents whose bytes contain FAILME fail).
	Engine string `json:"engine"`
}

type runStats struct {
	Imported    int                    `json:"imported"`
	Skipped     int                    `json:"skipped"`
	Failed      int                    `json:"failed"`
	Total       int                    `json:"total"`
	Warnings    []string               `json:"warnings"`
	RunID       string                 `json:"run_id"`
	Source      string                 `json:"source"`
	SourceURL   string                 `json:"source_url"`
	Mode        string                 `json:"mode"`
	SelectedIDs []int                  `json:"selected_ids"`
	Results     []DocumentResult       `json:"results"`
	Audit       *AuditReport           `json:"audit"`
	Finished    bool                   `json:"finished_after_started"`
	Extra       map[string]interface{} `json:"extra,omitempty"`
}

type runObservation struct {
	Stats    *runStats       `json:"stats"`
	Error    string          `json:"error"`
	Progress []string        `json:"progress"`
	Stderr   []string        `json:"stderr"`
	Log      []string        `json:"log"`
	Report   json.RawMessage `json:"report,omitempty"`
}

type runRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	Auth   string `json:"auth"`
}

type fsFile struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	Hash string `json:"sha256,omitempty"`
}

type runCase struct {
	Scenario runScenario      `json:"scenario"`
	Runs     []runObservation `json:"runs"`
	Requests []runRequest     `json:"requests"`
	Vault    []fsFile         `json:"vault"`
	Archive  []fsFile         `json:"archive"`
	Tables   map[string][]any `json:"tables"`
}

type runEngine struct{ mode string }

func (e runEngine) Extract(_ context.Context, path string, kind extract.Kind) (*extract.Result, error) {
	b, _ := os.ReadFile(path) //nolint:gosec // test scratch
	if e.mode == "fail-text" && bytes.Contains(b, []byte("FAILME")) {
		return nil, errors.New("extract text failed: scripted")
	}
	return &extract.Result{Text: fmt.Sprintf("extracted %d bytes", len(b)), MIME: string(kind), Engine: "fake"}, nil
}

func jsonRoute(path string, status int, v any) runRoute {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return runRoute{Path: path, Status: status, Headers: map[string]string{"Content-Type": "application/json"}, Body: string(b)}
}

func pageRoute(path, page string, results []map[string]any, next string) runRoute {
	body := map[string]any{"count": len(results), "results": results, "next": nil}
	if next != "" {
		body["next"] = next
	}
	r := jsonRoute(path, 200, body)
	r.Page = page
	return r
}

func downloadRoute(id int, status int, headers map[string]string, body []byte) runRoute {
	r := runRoute{Path: fmt.Sprintf("/api/documents/%d/download/", id), Status: status, Headers: headers}
	r.BodyB64 = base64.StdEncoding.EncodeToString(body)
	return r
}

func lookupRoutes() []runRoute {
	return []runRoute{
		pageRoute("/api/tags/", "", []map[string]any{{"id": 1, "name": "financial"}, {"id": 2, "name": "tax"}}, ""),
		pageRoute("/api/correspondents/", "", []map[string]any{{"id": 3, "name": "Acme Corp"}}, ""),
		pageRoute("/api/document_types/", "", []map[string]any{{"id": 4, "name": "Invoice"}}, ""),
		pageRoute("/api/storage_paths/", "", []map[string]any{{"id": 5, "name": "Finance/2026"}, {"id": 6, "name": "../Escape/<x>"}}, ""),
	}
}

func doc(id int, title, created string, extra map[string]any) map[string]any {
	d := map[string]any{"id": id, "title": title, "created": created + "T09:30:00Z", "created_date": created, "added": "2026-02-01T10:00:00Z", "modified": "2026-02-02T11:00:00Z", "original_file_name": fmt.Sprintf("orig-%d.pdf", id), "archived_file_name": fmt.Sprintf("%d.pdf", id), "page_count": 2, "mime_type": "application/pdf"}
	for k, v := range extra {
		d[k] = v
	}
	return d
}

var pdfBytes = []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")

func pdfWithTag(tag string) []byte {
	return append(append([]byte{}, pdfBytes...), []byte("% "+tag+"\n")...)
}

func pdfDownload(id int, tag string) runRoute {
	return downloadRoute(id, 200, map[string]string{"Content-Type": "application/pdf", "Content-Disposition": fmt.Sprintf(`attachment; filename="orig-%d.pdf"`, id)}, pdfWithTag(tag))
}

func plainDownload(id int, name, ctype, body string) runRoute {
	h := map[string]string{"Content-Type": ctype}
	if name != "" {
		h["Content-Disposition"] = fmt.Sprintf(`attachment; filename="%s"`, name)
	}
	return downloadRoute(id, 200, h, []byte(body))
}

func baseRoutes(docs []map[string]any, downloads ...runRoute) []runRoute {
	routes := append([]runRoute{}, lookupRoutes()...)
	routes = append(routes, pageRoute("/api/documents/", "", docs, ""))
	for _, d := range docs {
		routes = append(routes, jsonRoute(fmt.Sprintf("/api/documents/%d/", int(d["id"].(int))), 200, d))
	}
	return append(routes, downloads...)
}

func replaceRoute(routes []runRoute, r runRoute) []runRoute {
	for i := range routes {
		if routes[i].Path == r.Path && routes[i].Page == r.Page {
			out := append([]runRoute{}, routes...)
			out[i] = r
			return out
		}
	}
	return append(append([]runRoute{}, routes...), r)
}

func runScenarios() []runScenario {
	docs3 := []map[string]any{
		doc(1, "Invoice March", "2026-03-01", map[string]any{"tags": []int{1, 2}, "correspondent": 3, "document_type": 4, "storage_path": 5}),
		doc(2, "Receipt", "2026-02-15", map[string]any{"tags": []int{1}, "storage_path": 6}),
		doc(3, "Notes", "2026-01-20", map[string]any{"mime_type": "text/plain", "original_file_name": "notes.txt", "archived_file_name": nil}),
	}
	dl3 := []runRoute{pdfDownload(1, "one"), pdfDownload(2, "two"), plainDownload(3, "notes.txt", "text/plain; charset=utf-8", "Plain notes for document three.\n")}
	std := baseRoutes(docs3, dl3...)
	sc := func(id string, routes []runRoute, runs ...runSpec) runScenario {
		return runScenario{ID: id, Routes: routes, Runs: runs, Engine: "ok"}
	}
	out := []runScenario{
		sc("import-basic", std, runSpec{ReportPath: "report.json"}),
		sc("import-basic-checkpoints", std, runSpec{CheckpointEvery: 2}),
		sc("import-no-progress-writer-default", std, runSpec{NoProgress: true}),
		sc("plan", std, runSpec{Plan: true, ReportPath: "plan.json", ReportDryRunFlag: true}),
		sc("dry-run", std, runSpec{DryRun: true, ReportPath: "dry.json", ReportDryRunFlag: true}),
		sc("dry-run-unresolved-and-unsupported", baseRoutes([]map[string]any{
			doc(7, "Odd", "2026-04-01", map[string]any{"tags": []int{1, 99}, "correspondent": 98, "document_type": 97, "storage_path": 96, "mime_type": "application/x-weird", "original_file_name": "odd.weird", "archived_file_name": nil}),
			doc(8, "Sheet", "2026-04-02", map[string]any{"mime_type": "application/vnd.ms-excel", "original_file_name": "s.xlsx", "archived_file_name": nil}),
		}), runSpec{DryRun: true, ReportPath: "dry.json", ReportDryRunFlag: true}),
		sc("resume-second-run-skips", std, runSpec{}, runSpec{Resume: true, ReportPath: "second.json"}),
		sc("limit-one", std, runSpec{Limit: 1}),
		sc("since-filter-query", std, runSpec{Since: "2026-02-01"}),
		sc("explicit-ids", std, runSpec{IDs: []int{3, 1}, Limit: 1}),
		sc("preserve-storage-paths", std, runSpec{PreserveStorage: true}),
		sc("concurrency-three-distinct", std, runSpec{Concurrency: 3, NoProgress: true}),
		sc("target-override-state-key", std, runSpec{TargetVaultOverride: "custom-target"}, runSpec{TargetVaultOverride: "custom-target", Resume: true}),
	}
	// Duplicate content: documents 4 and 5 download identical bytes.
	dupDocs := []map[string]any{doc(4, "First", "2026-03-04", nil), doc(5, "Second", "2026-03-05", nil)}
	out = append(out, sc("duplicate-content-second-skipped", baseRoutes(dupDocs, pdfDownload(4, "same"), pdfDownload(5, "same")), runSpec{}, runSpec{Resume: true}))
	// Download failure, then retry-failed after the server recovers.
	failRoutes := baseRoutes(docs3, pdfDownload(1, "one"), downloadRoute(2, 500, map[string]string{"Content-Type": "text/plain"}, []byte("boom")), plainDownload(3, "notes.txt", "text/plain", "Plain notes.\n"))
	out = append(out, sc("download-failure-then-retry-failed", failRoutes,
		runSpec{ReportPath: "first.json"},
		runSpec{RetryFailed: true, RoutesAfter: []runRoute{pdfDownload(2, "two")}, ReportPath: "retry.json"},
		runSpec{RetryFailed: true}))
	out = append(out, sc("retry-failed-nothing-failed", std, runSpec{}, runSpec{RetryFailed: true}))
	// Content type / extension resolution.
	extDocs := []map[string]any{
		doc(10, "No extension", "2026-05-01", map[string]any{"mime_type": "", "original_file_name": "", "archived_file_name": nil, "file_type": ""}),
		doc(11, "CD wins", "2026-05-02", map[string]any{"mime_type": "application/pdf", "original_file_name": "x.pdf", "archived_file_name": nil}),
		doc(12, "CT only", "2026-05-03", map[string]any{"mime_type": "", "original_file_name": "", "archived_file_name": nil}),
	}
	out = append(out, sc("extension-resolution", baseRoutes(extDocs,
		downloadRoute(10, 200, map[string]string{"Content-Type": "application/octet-stream"}, []byte("raw bytes without any metadata\n")),
		plainDownload(11, "report.TXT", "application/octet-stream", "Text from content disposition.\n"),
		downloadRoute(12, 200, map[string]string{"Content-Type": "text/markdown; charset=utf-8"}, []byte("# heading only content type\n")),
	), runSpec{ReportPath: "ext.json"}))
	// Ingest-stage failure (engine rejects), alongside a good document.
	ingDocs := []map[string]any{doc(20, "Good", "2026-06-01", nil), doc(21, "Bad", "2026-06-02", nil)}
	ing := sc("ingest-failure-recorded", baseRoutes(ingDocs, pdfDownload(20, "good"), pdfDownload(21, "FAILME")), runSpec{ReportPath: "ingest.json"}, runSpec{Resume: true})
	ing.Engine = "fail-text"
	out = append(out, ing)
	// Paginated listing and API failures.
	pg1 := pageRoute("/api/documents/", "", []map[string]any{docs3[0]}, "$BASE/api/documents/?format=json&page=2")
	pg2 := pageRoute("/api/documents/", "2", []map[string]any{docs3[1], docs3[2]}, "")
	paged := baseRoutes(docs3, dl3...)
	paged = replaceRoute(paged, pg1)
	paged = append(paged, pg2)
	out = append(out, sc("paginated-listing", paged, runSpec{}))
	out = append(out, sc("list-documents-http-500", replaceRoute(std, jsonRoute("/api/documents/", 500, map[string]any{"detail": "down"})), runSpec{}))
	out = append(out, sc("lookup-tags-http-500", replaceRoute(std, jsonRoute("/api/tags/", 500, map[string]any{"detail": "down"})), runSpec{}))
	out = append(out, sc("lookup-storage-paths-unauthorized", replaceRoute(std, jsonRoute("/api/storage_paths/", 401, map[string]any{"detail": "no"})), runSpec{}))
	out = append(out, sc("ids-get-document-404", std, runSpec{IDs: []int{1, 999}}))
	out = append(out, sc("empty-archive", baseRoutes(nil), runSpec{ReportPath: "empty.json"}, runSpec{DryRun: true, ReportPath: "empty-dry.json", ReportDryRunFlag: true}))
	return out
}

var (
	reRunID   = regexp.MustCompile(`paperless-\d{8}T\d{6}\.\d{9}Z`)
	reTmpName = regexp.MustCompile(`\.tmp-\d+|symingest-import-\d+`)
	reNow     = regexp.MustCompile(`(?m)^ingested_at: \S+$`)
	reETA     = regexp.MustCompile(`rate=\S+ eta=\S+`)
)

func snapshot(t *testing.T, root, sub string, norm func(string) string) []fsFile {
	t.Helper()
	out := []fsFile{}
	base := filepath.Join(root, sub)
	if _, err := os.Lstat(base); err != nil {
		return out
	}
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		f := fsFile{Path: filepath.ToSlash(rel), Type: "dir"}
		if !d.IsDir() {
			f.Type = "file"
			b, err := os.ReadFile(path) //nolint:gosec // test scratch
			if err != nil {
				return err
			}
			f.Hash = fmt.Sprintf("%x", sha256.Sum256(b))
			if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".json") {
				f.Text = reNow.ReplaceAllString(norm(string(b)), "ingested_at: <NOW>")
				f.Hash = ""
			}
		}
		out = append(out, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func dumpTables(t *testing.T, dbPath string, norm func(string) string, unordered bool) map[string][]any {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	out := map[string][]any{}
	for _, table := range []string{"documents", "jobs", "paperless_import_state"} {
		rows, err := db.Query("SELECT * FROM " + table + " ORDER BY 1") //nolint:gosec // fixed table names
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		list := []any{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{}
			for i, c := range cols {
				if strings.HasSuffix(c, "_at") || c == "updated" || c == "created" {
					continue
				}
				switch v := vals[i].(type) {
				case []byte:
					row[c] = norm(string(v))
				case string:
					row[c] = norm(v)
				default:
					row[c] = v
				}
			}
			if unordered {
				delete(row, "id")
				delete(row, "document_id")
			}
			list = append(list, row)
		}
		_ = rows.Close()
		if unordered {
			sort.Slice(list, func(i, j int) bool {
				a, _ := json.Marshal(list[i])
				b, _ := json.Marshal(list[j])
				return string(a) < string(b)
			})
		}
		out[table] = list
	}
	return out
}

func sortedLines(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func runRunScenario(t *testing.T, sc runScenario) runCase {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)

	routes := append([]runRoute{}, sc.Routes...)
	var requests []runRequest
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, runRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")})
		page := r.URL.Query().Get("page")
		for i := len(routes) - 1; i >= 0; i-- {
			rt := routes[i]
			if rt.Path != r.URL.Path || rt.Page != page {
				continue
			}
			for k, v := range rt.Headers {
				w.Header().Set(k, v)
			}
			w.WriteHeader(rt.Status)
			if rt.BodyB64 != "" {
				b, _ := base64.StdEncoding.DecodeString(rt.BodyB64)
				_, _ = w.Write(b)
			} else {
				_, _ = io.WriteString(w, strings.ReplaceAll(rt.Body, "$BASE", srv.URL))
			}
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	norm := func(s string) string {
		s = strings.ReplaceAll(s, root, "<ROOT>")
		s = strings.ReplaceAll(s, host, "<HOST>")
		s = reRunID.ReplaceAllString(s, "paperless-<RUN>")
		return reTmpName.ReplaceAllString(s, "<TMP>")
	}

	vault := filepath.Join(root, "vault")
	st, err := store.Open(filepath.Join(root, "db", "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	p := &ingest.Pipeline{Engine: runEngine{mode: sc.Engine}, Store: st, Writer: &writer.NoteWriter{Vault: vault}, ArchiveDir: filepath.Join(root, "archive")}

	oldOut, oldFlags := log.Writer(), log.Flags()
	defer func() { log.SetOutput(oldOut); log.SetFlags(oldFlags) }()

	c := runCase{Scenario: sc}
	for _, spec := range sc.Runs {
		if len(spec.RoutesAfter) > 0 {
			routes = append(routes, spec.RoutesAfter...)
		}
		var logBuf, progress bytes.Buffer
		log.SetOutput(&logBuf)
		log.SetFlags(0)
		oldStderr := os.Stderr
		pr, pw, perr := os.Pipe()
		if perr != nil {
			t.Fatal(perr)
		}
		os.Stderr = pw
		var captured bytes.Buffer
		done := make(chan struct{})
		go func() { _, _ = io.Copy(&captured, pr); close(done) }()

		opts := Options{BaseURL: srv.URL, Token: "test-token", Plan: spec.Plan, DryRun: spec.DryRun, Resume: spec.Resume, RetryFailed: spec.RetryFailed,
			Concurrency: spec.Concurrency, CheckpointEvery: spec.CheckpointEvery, Limit: spec.Limit, IDs: spec.IDs, PreserveStoragePaths: spec.PreserveStorage, TargetVault: spec.TargetVaultOverride}
		if spec.Since != "" {
			ts, _ := parseDay(spec.Since)
			opts.Since = ts
		}
		if !spec.NoProgress {
			opts.Progress = &progress
		}
		stats, runErr := Run(context.Background(), opts, p)
		_ = pw.Close()
		<-done
		os.Stderr = oldStderr

		obs := runObservation{Progress: []string{}, Stderr: []string{}, Log: []string{}}
		if runErr != nil {
			obs.Error = norm(runErr.Error())
		}
		if stats != nil {
			s := &runStats{Imported: stats.Imported, Skipped: stats.Skipped, Failed: stats.Failed, Total: stats.Total, Warnings: stats.Warnings, RunID: norm(stats.RunID),
				Source: stats.Source, SourceURL: norm(stats.SourceURL), Mode: stats.Mode, SelectedIDs: stats.SelectedIDs, Results: stats.Results, Audit: stats.Audit,
				Finished: !stats.FinishedAt.Before(stats.StartedAt)}
			if s.Warnings == nil {
				s.Warnings = []string{}
			}
			for i := range s.Results {
				s.Results[i] = normResult(s.Results[i], norm)
			}
			obs.Stats = s
			if spec.ReportPath != "" {
				path := filepath.Join(root, spec.ReportPath)
				rep := stats.BuildMigrationReport(spec.ReportDryRunFlag)
				if werr := WriteMigrationReport(path, rep); werr != nil {
					obs.Error += " | report: " + norm(werr.Error())
				} else if b, rerr := os.ReadFile(path); rerr == nil { //nolint:gosec // test scratch
					obs.Report = scrubReport(t, []byte(norm(string(b))))
				}
			}
		}
		prog := norm(progress.String())
		prog = reETA.ReplaceAllString(prog, "rate=<R> eta=<E>")
		obs.Progress = sortedLines(prog)
		obs.Stderr = sortedLines(norm(captured.String()))
		obs.Log = sortedLines(norm(logBuf.String()))
		if spec.Concurrency > 1 {
			sort.Strings(obs.Progress)
			sort.Strings(obs.Stderr)
			sort.Strings(obs.Log)
			if obs.Stats != nil {
				sort.Strings(obs.Stats.Warnings)
			}
		}
		c.Runs = append(c.Runs, obs)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	c.Requests = requests
	if c.Requests == nil {
		c.Requests = []runRequest{}
	}
	for i := range c.Requests {
		c.Requests[i].Query = norm(c.Requests[i].Query)
	}
	unorderedRequests := false
	for _, spec := range sc.Runs {
		if spec.Concurrency > 1 {
			unorderedRequests = true
		}
	}
	if unorderedRequests {
		sort.Slice(c.Requests, func(i, j int) bool {
			a, _ := json.Marshal(c.Requests[i])
			b, _ := json.Marshal(c.Requests[j])
			return string(a) < string(b)
		})
	}
	c.Vault = snapshot(t, root, "vault", norm)
	c.Archive = snapshot(t, root, "archive", norm)
	unordered := false
	for _, spec := range sc.Runs {
		if spec.Concurrency > 1 {
			unordered = true
		}
	}
	c.Tables = dumpTables(t, filepath.Join(root, "db", "docs.db"), norm, unordered)
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Errorf("%s: leaked temp files: %v", sc.ID, names)
	}
	return c
}

func normResult(r DocumentResult, norm func(string) string) DocumentResult {
	r.VaultPath, r.ArchivePath = norm(r.VaultPath), norm(r.ArchivePath)
	r.ImportRunID = norm(r.ImportRunID)
	r.Error, r.Reason = norm(r.Error), norm(r.Reason)
	for i := range r.Warnings {
		r.Warnings[i] = norm(r.Warnings[i])
	}
	return r
}

// scrubReport drops the per-invocation timestamps and duration of a written
// migration report so the remainder is comparable.
func scrubReport(t *testing.T, raw []byte) json.RawMessage {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"started_at", "finished_at", "duration_seconds"} {
		if _, ok := m[k]; ok {
			m[k] = "<T>"
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPortPaperlessRunContract(t *testing.T) {
	var cases []runCase
	for _, sc := range runScenarios() {
		cases = append(cases, runRunScenario(t, sc))
	}
	imported := 0
	for _, c := range cases {
		for _, r := range c.Runs {
			if r.Stats != nil {
				imported += r.Stats.Imported
			}
		}
	}
	if imported < 15 {
		t.Fatalf("oracle imported only %d documents", imported)
	}
	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"import.go", "report.go", "run_port_test.go"} {
		b, e := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // repository source
		if e != nil {
			t.Fatal(e)
		}
		hashes[f] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, e := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"cases":          cases,
		"normalizations": []string{
			"temp root <ROOT>, loopback host:port <HOST>, run ids paperless-<RUN>, ingested_at <NOW>, rate/eta <R>/<E>, report timestamps <T>; concurrent runs sort progress/log/warnings",
		},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/paperless-run.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
		return
	}
	old, e := os.ReadFile(out) //nolint:gosec // repository fixture
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(old, content) {
		t.Fatal("Paperless run fixture drift; regenerate deliberately")
	}
}

func parseDay(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }
