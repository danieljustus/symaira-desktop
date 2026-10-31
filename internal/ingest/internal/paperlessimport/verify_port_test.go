package paperlessimport

// Go oracle for Verify. Documents are imported through Run against the loopback
// fake Paperless API, the resulting vault/archive/state are mutated in scripted
// ways (deleted notes and archives, corrupted bytes, edited metadata, copied
// notes, foreign files, state changes), and Verify reports are recorded.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/writer"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type verifyMutation struct {
	Op      string `json:"op"`
	Doc     int    `json:"doc,omitempty"`
	Path    string `json:"path,omitempty"`
	Content string `json:"content,omitempty"`
	Find    string `json:"find,omitempty"`
	Replace string `json:"replace,omitempty"`
	Status  string `json:"status,omitempty"`
}

type verifySpec struct {
	UseStore      bool       `json:"use_store"`
	VaultOverride string     `json:"vault_override,omitempty"` // "none" = empty string, "missing" = nonexistent dir, "file" = a regular file
	TargetVault   string     `json:"target_vault,omitempty"`
	TargetArchive string     `json:"target_archive,omitempty"`
	Deep          bool       `json:"deep,omitempty"`
	IDs           []int      `json:"ids,omitempty"`
	Limit         int        `json:"limit,omitempty"`
	RoutesAfter   []runRoute `json:"routes_after,omitempty"`
}

type verifyScenario struct {
	ID        string           `json:"id"`
	Routes    []runRoute       `json:"routes"`
	Engine    string           `json:"engine"`
	Mutations []verifyMutation `json:"mutations"`
	Verifies  []verifySpec     `json:"verifies"`
}

type verifyObservation struct {
	Report json.RawMessage `json:"report,omitempty"`
	Error  string          `json:"error"`
}

type verifyCase struct {
	Scenario verifyScenario      `json:"scenario"`
	Imported int                 `json:"imported"`
	Verifies []verifyObservation `json:"verifies"`
}

func verifyScenarios() []verifyScenario {
	docs3 := []map[string]any{
		doc(1, "Invoice March", "2026-03-01", map[string]any{"tags": []int{1, 2}, "correspondent": 3, "document_type": 4, "storage_path": 5}),
		doc(2, "Receipt", "2026-02-15", map[string]any{"tags": []int{1}}),
		doc(3, "Notes", "2026-01-20", map[string]any{"mime_type": "text/plain", "original_file_name": "notes.txt", "archived_file_name": nil}),
	}
	dl := []runRoute{pdfDownload(1, "one"), pdfDownload(2, "two"), plainDownload(3, "notes.txt", "text/plain; charset=utf-8", "Plain notes for document three.\n")}
	std := baseRoutes(docs3, dl...)
	v := func(id string, muts []verifyMutation, specs ...verifySpec) verifyScenario {
		return verifyScenario{ID: id, Routes: std, Engine: "ok", Mutations: muts, Verifies: specs}
	}
	st := verifySpec{UseStore: true}
	notes := verifySpec{}
	return []verifyScenario{
		v("clean-with-store", nil, st),
		v("clean-notes-only", nil, notes),
		v("clean-with-target-override", nil, verifySpec{UseStore: true, TargetVault: "<VAULT>", TargetArchive: "<ARCHIVE>"}),
		v("missing-note", []verifyMutation{{Op: "rm-note", Doc: 2}}, st, notes),
		v("missing-archive", []verifyMutation{{Op: "rm-archive", Doc: 1}}, st, notes),
		v("corrupt-archive-hash-mismatch", []verifyMutation{{Op: "corrupt-archive", Doc: 2}}, st, notes),
		v("edited-note-metadata", []verifyMutation{{Op: "edit-note", Doc: 1, Find: "- financial", Replace: "- finance"}, {Op: "edit-note", Doc: 1, Find: "correspondent: Acme Corp", Replace: "correspondent: Other"}}, st, notes),
		v("edited-note-created-and-storage", []verifyMutation{{Op: "edit-note", Doc: 1, Find: "storage_path: Finance/2026", Replace: "storage_path: Elsewhere"}, {Op: "edit-note", Doc: 1, Find: "created: 2026-03-01T09:30:00Z", Replace: "created: 2025-01-01T00:00:00Z"}}, notes),
		v("duplicate-note-copy", []verifyMutation{{Op: "copy-note", Doc: 3}}, st, notes),
		v("foreign-files-ignored", []verifyMutation{
			{Op: "write-file", Path: "vault/plain.md", Content: "# no frontmatter\n"},
			{Op: "write-file", Path: "vault/broken.md", Content: "---\n: : [unclosed\n---\nbody\n"},
			{Op: "write-file", Path: "vault/ordinary.md", Content: "---\nsource_path: /x\nsha256: ab\nmime: text/plain\ntags: []\ncategory: c\narchive_path: /a\ningested_at: 2026-01-01T00:00:00Z\n---\nbody\n"},
			{Op: "write-file", Path: "vault/zero-id.md", Content: "---\nsource_path: /x\npaperless:\n  document_id: 0\n---\n"},
			{Op: "write-file", Path: "vault/not-markdown.txt", Content: "---\npaperless:\n  document_id: 2\n---\n"},
			{Op: "write-file", Path: "vault/UPPER.MD", Content: "---\nsource_path: /u\npaperless:\n  document_id: 777\n---\n"},
			{Op: "write-file", Path: "vault/unterminated.md", Content: "---\nsource_path: /x\n"},
		}, st, notes),
		v("state-failed-counts-as-missing", []verifyMutation{{Op: "state-status", Doc: 2, Status: "failed"}}, st),
		v("state-pending", []verifyMutation{{Op: "state-status", Doc: 1, Status: "pending"}}, st),
		v("state-only-note-moved", []verifyMutation{{Op: "rm-note", Doc: 3}}, st),
		v("vault-override-missing-dir", nil, verifySpec{UseStore: true, VaultOverride: "missing"}, verifySpec{VaultOverride: "missing"}),
		v("vault-override-file", nil, verifySpec{VaultOverride: "file"}),
		v("vault-override-empty-string", nil, verifySpec{UseStore: true, VaultOverride: "none"}),
		v("deep-verify-clean", nil, verifySpec{UseStore: true, Deep: true}, verifySpec{Deep: true}),
		v("deep-verify-source-changed", nil, verifySpec{UseStore: true, Deep: true, RoutesAfter: []runRoute{pdfDownload(2, "changed upstream")}}),
		v("deep-verify-download-fails", nil, verifySpec{Deep: true, RoutesAfter: []runRoute{downloadRoute(1, 500, map[string]string{"Content-Type": "text/plain"}, []byte("boom"))}}),
		v("deep-verify-skips-missing-archive", []verifyMutation{{Op: "rm-archive", Doc: 2}}, verifySpec{UseStore: true, Deep: true}),
		v("selection-ids", nil, verifySpec{UseStore: true, IDs: []int{2, 1}}),
		v("selection-limit", nil, verifySpec{UseStore: true, Limit: 2}),
		v("selection-unknown-id-fails", nil, verifySpec{UseStore: true, IDs: []int{1, 404}}),
		v("lookup-failure", nil, verifySpec{UseStore: true, RoutesAfter: []runRoute{jsonRoute("/api/tags/", 500, map[string]any{"detail": "down"})}}),
		{ID: "duplicate-content-informational", Engine: "ok", Routes: baseRoutes([]map[string]any{doc(4, "First", "2026-03-04", nil), doc(5, "Second", "2026-03-05", nil)}, pdfDownload(4, "same"), pdfDownload(5, "same")), Verifies: []verifySpec{st, notes}},
		{ID: "unimported-documents-missing", Engine: "ok", Routes: std, Mutations: []verifyMutation{{Op: "rm-note", Doc: 1}, {Op: "rm-note", Doc: 2}, {Op: "rm-note", Doc: 3}}, Verifies: []verifySpec{notes}},
	}
}

var reArchivePath = regexp.MustCompile(`(?m)^archive_path: (.+)$`)

func findNote(t *testing.T, vault string, id int) string {
	t.Helper()
	var found string
	_ = filepath.WalkDir(vault, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasPrefix(d.Name(), fmt.Sprintf("paperless-%d-", id)) && strings.HasSuffix(d.Name(), ".md") && found == "" {
			found = path
		}
		return nil
	})
	if found == "" {
		t.Fatalf("no note for document %d", id)
	}
	return found
}

func archiveOf(t *testing.T, note string) string {
	t.Helper()
	b, err := os.ReadFile(note) //nolint:gosec // test scratch
	if err != nil {
		t.Fatal(err)
	}
	m := reArchivePath.FindSubmatch(b)
	if m == nil {
		t.Fatalf("note %s has no archive_path", note)
	}
	return strings.Trim(string(m[1]), `"'`)
}

func runVerifyScenario(t *testing.T, sc verifyScenario) verifyCase {
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
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	vault, archive := filepath.Join(root, "vault"), filepath.Join(root, "archive")
	norm := func(s string) string {
		s = strings.ReplaceAll(s, root, "<ROOT>")
		s = strings.ReplaceAll(s, host, "<HOST>")
		s = reRunID.ReplaceAllString(s, "paperless-<RUN>")
		return reTmpName.ReplaceAllString(s, "<TMP>")
	}
	st, err := store.Open(filepath.Join(root, "db", "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	p := &ingest.Pipeline{Engine: runEngine{mode: sc.Engine}, Store: st, Writer: &writer.NoteWriter{Vault: vault}, ArchiveDir: archive}
	stats, err := Run(context.Background(), Options{BaseURL: srv.URL, Token: "test-token", Progress: io.Discard}, p)
	if err != nil {
		t.Fatal(err)
	}
	c := verifyCase{Scenario: sc, Imported: stats.Imported}

	for _, m := range sc.Mutations {
		switch m.Op {
		case "rm-note":
			if err := os.Remove(findNote(t, vault, m.Doc)); err != nil {
				t.Fatal(err)
			}
		case "rm-archive":
			if err := os.Remove(archiveOf(t, findNote(t, vault, m.Doc))); err != nil {
				t.Fatal(err)
			}
		case "corrupt-archive":
			f, err := os.OpenFile(archiveOf(t, findNote(t, vault, m.Doc)), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = f.WriteString("tampered")
			_ = f.Close()
		case "edit-note":
			path := findNote(t, vault, m.Doc)
			b, _ := os.ReadFile(path) //nolint:gosec // test scratch
			if !bytes.Contains(b, []byte(m.Find)) {
				t.Fatalf("%s: %q not in note:\n%s", sc.ID, m.Find, b)
			}
			if err := os.WriteFile(path, bytes.Replace(b, []byte(m.Find), []byte(m.Replace), 1), 0o600); err != nil {
				t.Fatal(err)
			}
		case "copy-note":
			path := findNote(t, vault, m.Doc)
			b, _ := os.ReadFile(path) //nolint:gosec // test scratch
			if err := os.WriteFile(filepath.Join(filepath.Dir(path), "copy-"+filepath.Base(path)), b, 0o600); err != nil {
				t.Fatal(err)
			}
		case "write-file":
			path := filepath.Join(root, m.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(m.Content), 0o600); err != nil {
				t.Fatal(err)
			}
		case "state-status":
			state, err := st.PaperlessImportStateForTarget(context.Background(), srv.URL, vault, archive, m.Doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertPaperlessImportStateForTarget(context.Background(), srv.URL, vault, archive, m.Doc, m.Status, "", state.VaultPath, state.ArchivePath, state.SHA256); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown mutation %q", m.Op)
		}
	}

	for _, spec := range sc.Verifies {
		routes = append(routes, spec.RoutesAfter...)
		opts := Options{BaseURL: srv.URL, Token: "test-token", DeepVerify: spec.Deep, IDs: spec.IDs, Limit: spec.Limit, TargetVault: spec.TargetVault, TargetArchive: spec.TargetArchive}
		opts.TargetVault = strings.ReplaceAll(opts.TargetVault, "<VAULT>", vault)
		opts.TargetArchive = strings.ReplaceAll(opts.TargetArchive, "<ARCHIVE>", archive)
		target := vault
		switch spec.VaultOverride {
		case "none":
			target = ""
		case "missing":
			target = filepath.Join(root, "no-such-vault")
		case "file":
			target = filepath.Join(root, "vault-as-file")
			if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var useStore *store.Store
		if spec.UseStore {
			useStore = st
		}
		rep, verr := Verify(context.Background(), opts, target, useStore)
		obs := verifyObservation{}
		if verr != nil {
			obs.Error = norm(verr.Error())
		}
		if rep != nil {
			b, _ := json.Marshal(rep)
			var m map[string]any
			_ = json.Unmarshal([]byte(norm(string(b))), &m)
			for _, k := range []string{"started_at", "finished_at", "duration_seconds"} {
				if _, ok := m[k]; ok {
					m[k] = "<T>"
				}
			}
			m["complete"] = rep.Complete()
			obs.Report, _ = json.Marshal(m)
		}
		c.Verifies = append(c.Verifies, obs)
	}
	return c
}

func TestPortPaperlessVerifyContract(t *testing.T) {
	var cases []verifyCase
	for _, sc := range verifyScenarios() {
		cases = append(cases, runVerifyScenario(t, sc))
	}
	reports, complete := 0, 0
	for _, c := range cases {
		for _, o := range c.Verifies {
			if o.Report != nil {
				reports++
				if strings.Contains(string(o.Report), `"complete":true`) {
					complete++
				}
			}
		}
	}
	if reports < 25 || complete < 3 {
		t.Fatalf("oracle too thin: %d reports, %d complete", reports, complete)
	}
	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"verify.go", "verify_port_test.go"} {
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
		"normalizations": []string{"temp root <ROOT>, loopback host <HOST>, run ids <RUN>, report timestamps <T>; complete is Complete()"},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/paperless-verify.json")
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
		t.Fatal("Paperless verify fixture drift; regenerate deliberately")
	}
}
