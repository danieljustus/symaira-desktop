package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type mcpReadCase struct {
	mcpSurfaceCase
	Files         map[string]string                 `json:"files,omitempty"`
	Indexed       bool                              `json:"indexed,omitempty"`
	ExternalLink  bool                              `json:"external_link,omitempty"`
	NoVault       bool                              `json:"no_vault,omitempty"`
	AfterFiles    map[string]string                 `json:"after_files"`
	FileMtime     int64                             `json:"file_mtime,omitempty"`
	SidecarExists bool                              `json:"sidecar_exists"`
	AfterIndex    map[string]map[string]interface{} `json:"after_index"`
}
type mcpReadFixture struct {
	SchemaVersion int           `json:"schema_version"`
	Cases         []mcpReadCase `json:"cases"`
}

func mcpReadCases() []mcpReadCase {
	files := map[string]string{
		"alpha.md":                   "---\ntitle: Alpha <&>\ncreated: 2026-01-02\ntags: [one, two]\nstatus: open\nnumber: 42\noffset_time: 2026-01-02T03:04:05.1200+05:30\nutc_time: 2026-01-02T03:04:05.123456789Z\nquoted_date: '2026-01-02'\n---\nneedle alpha\n",
		"notebooks/research.md":      "---\ntype: notebook\ntitle: Research <&>\nnotebook_id: research-id\ndescription: My notebook\ncreated: 2026-01-02\nsources: [alpha.md, missing.md, ../outside.md, alpha.md]\nquery: needle\n---\n# Research\n",
		"notebooks/empty.md":         "---\ntype: notebook\ntitle: AAA Empty\ncreated: 2026-01-02\nsources: []\n---\n",
		"notebooks/ordinary.md":      "---\ntitle: Ordinary\n---\nnot a notebook\n",
		"notebooks/ignored.MD":       "---\ntype: notebook\ntitle: Ignored\n---\n",
		"notebooks/nested/hidden.md": "---\ntype: notebook\ntitle: Hidden\n---\n",
	}
	var cases []mcpReadCase
	add := func(id, name, args string) {
		suffix := ""
		if args != "" {
			suffix = `,"arguments":` + args
		}
		cases = append(cases, mcpReadCase{mcpSurfaceCase: mcpSurfaceCase{ID: id, Request: `{"jsonrpc":"2.0","id":"call","method":"tools/call","params":{"name":"` + name + `"` + suffix + `}}`}, Files: files})
	}
	add("notebook-list", "notebook_list", `{}`)
	add("notebook-list-ignored-arguments", "notebook_list", `false`)
	add("notebook-list-empty", "notebook_list", `{}`)
	cases[len(cases)-1].Files = nil
	for _, ref := range []string{"research", "notebooks/research", "notebooks/research.md", " elsewhere/research.md ", "empty", "missing", "ordinary", " ", "notebooks/../../outside"} {
		arg, _ := json.Marshal(map[string]string{"notebook": ref})
		add("notebook-get-"+ref, "notebook_get", string(arg))
	}
	for _, args := range []string{"", `null`, `{}`, `[]`, `{"notebook":7}`, `{"NOTEBOOK":"research","notebook":null}`, `{"notebook":false,"notebook":"research"}`} {
		add("notebook-get-decode-"+args, "notebook_get", args)
	}
	for _, args := range []string{`{}`, `{"notebook":"research"}`, `{"notebook":"research","query":"needle"}`, `{"notebook":"research","query":"unmatchedtoken"}`, `{"notebook":"empty","query":"needle"}`, `{"notebook":"missing","query":"needle"}`, `{"notebook":"research","query":false}`, `[]`} {
		add("notebook-ask-"+args, "notebook_ask", args)
	}
	for _, args := range []string{`{"file":"alpha.md"}`, `{"file":"missing.md"}`, `{"file":"../outside"}`, `{}`, `null`, `[]`, `{"file":false}`, `{"FILE":"alpha.md","file":null}`} {
		add("props-"+args, "desk_props", args)
		cases[len(cases)-1].Indexed = true
	}
	for _, body := range []string{
		"---\ntype: notebook\ntitle: Typed\nnotebook_id: 42\ndescription: false\nquery: 123\nsources: null\n---\n",
		"---\ntype: notebook\nsources: wrong-shape\n---\n",
		"---\ntype: notebook\nsources: [false, 7, null, '', alpha.md]\n---\n",
		"---\ntype: notebook\ntitle: First\ntitle: Second\n---\n",
	} {
		add("notebook-legacy-"+body, "notebook_get", `{"notebook":"legacy"}`)
		cases[len(cases)-1].Files = map[string]string{"notebooks/legacy.md": body, "alpha.md": files["alpha.md"]}
	}
	for _, args := range []string{
		`{"file":"target.md"}`, `{"file":"nested/target.md"}`, `{"file":"Target Title"}`,
		`{"file":"missing.md"}`, `{"file":"unknown.md"}`, `{"file":"../outside"}`, `{"file":"contact:relate:person:123"}`,
		`{"file":"contact:broken"}`, `{}`, `null`, `[]`, `{"file":7}`,
		`{"FILE":"nested/target.md","file":null}`, `{"file":false,"file":"target.md"}`,
	} {
		add("backlinks-"+args, "desk_backlinks", args)
		cases[len(cases)-1].Indexed = true
		cases[len(cases)-1].Files = map[string]string{
			"nested/target.md": "---\ntitle: Target Title\naliases: [Alias Name, Another Alias]\n---\nbody\n",
			"a.md":             "---\ntitle: Link A\n---\n[[Target Title]] [[Alias Name]]\n",
			"b.md":             "---\ntitle: Link B\n---\n[[target]] [[target.md]] [[Another Alias]]\n",
			"c.md":             "---\ntitle: Link C\n---\n[[nested/target.md]] [[missing]]\n",
			"contacts.md":      "---\ntitle: Contact Links\n---\n[[contact:relate:person:123]] [[contact:broken]]\n",
		}
	}
	add("backlinks-external-source", "desk_backlinks", `{"file":"alpha.md"}`)
	cases[len(cases)-1].Indexed = true
	cases[len(cases)-1].ExternalLink = true
	add("props-unindexed", "desk_props", `{"file":"alpha.md"}`)
	add("notebook-no-vault", "notebook_get", `{"notebook":"research"}`)
	cases[len(cases)-1].NoVault = true
	add("search-alias", "search_documents", `{"query":"  "}`)
	add("search-alias-missing", "search_documents", `{}`)
	add("search-alias-bad-type", "search_documents", `{"query":false}`)
	// Remaining read handlers share the same isolated process/state oracle.
	for _, name := range []string{"desk_docs", "list_documents", "docs_review", "docs_similar"} {
		for _, args := range []string{`{}`, `null`, `[]`, `{"file":"alpha.md"}`, `{"file":"../outside"}`, `{"file":false}`, `{"threshold":1}`, `{"threshold":90.0}`} {
			add(name+"-"+args, name, args)
			cases[len(cases)-1].Indexed = true
		}
	}
	for _, args := range []string{`{"type":"invoice"}`, `{"status":"open"}`, `{"person":"Ada"}`, `{"correspondent":"Bank"}`, `{"year":"2026"}`, `{"due_before":"2026-02-01"}`, `{"min_confidence":0,"max_confidence":50}`, `{"min_confidence":false}`, `{"TYPE":"invoice","type":null}`, `{"status":false,"status":"open"}`} {
		add("docs-filter-"+args, "desk_docs", args)
		cases[len(cases)-1].Indexed = true
	}
	metadata := map[string]string{
		"a.md": "---\ntitle: Invoice <&>\ntype: invoice\ndocument_type: bill\ndocument_date: '2026-01-02'\nconfidence: 40\nperson: Ada\ncorrespondent: Bank\nstatus: open\ndue_date: '2026-01-31'\nasn: 7\ntags: [finance]\n---\nalpha beta gamma delta\n",
		"b.md": "---\ntitle: Reviewed\ntype: invoice\ndocument_type: bill\ndocument_date: '2026-01-03'\nconfidence: 95\nreview_ignored: true\n---\nalpha beta gamma epsilon\n",
	}
	for _, name := range []string{"desk_docs", "docs_review", "docs_similar"} {
		for _, args := range []string{`{}`, `{"file":"a.md","threshold":1}`, `{"file":"b.md","threshold":101}`, `{"type":"bill","status":"open","person":"Ada","correspondent":"Bank","year":"2026","due_before":"2026-02-01","min_confidence":0,"max_confidence":50}`, `{"file":"../outside"}`} {
			add("metadata-"+name+"-"+args, name, args)
			cases[len(cases)-1].Files = metadata
			cases[len(cases)-1].Indexed = true
		}
	}
	for _, indexed := range []bool{false, true} {
		for _, args := range []string{"", `{}`, `false`, `{"title":7}`} {
			add(fmt.Sprintf("diagram-%v-%s", indexed, args), "desk_diagram", args)
			cases[len(cases)-1].Indexed = indexed
			cases[len(cases)-1].Files = map[string]string{
				"a.md": "---\ntitle: Alpha <&> \"quote\"\naliases: [Alias]\n---\n[[b]] [[Bee]] [[Ghost]] [[b]]\n",
				"b.md": "---\ntitle: Bee\n---\n[[Alias]] [[a]]\n",
				"c.md": "---\ntitle: Third\n---\n[[a]]\n",
				"d.md": "fourth\n", "e.md": "fifth\n",
			}
		}
	}
	for _, args := range []string{"", `{}`, `false`, `[]`} {
		add("meetings-"+args, "meeting_list", args)
		cases[len(cases)-1].Files = map[string]string{
			"meetings/a.md": "---\ntype: meeting\ntitle: Review <&>\nmeeting_id: m1\nstarted_at: '2026-01-02T03:04:05Z'\nduration_ms: 2500\nlanguage: de\nsymmeet_source: {review_state: reviewed}\n---\nReviewed transcript\n",
			"meetings/b.md": "---\ntype: meeting\nmeeting_id: 42\nstarted_at: 2026-01-02T03:04:05Z\nduration_ms: 2.75\n---\n",
			"ordinary.md": "not a meeting\n",
			"broken.md": "---\ntitle: [invalid\n---\n",
		}
	}
	for _, args := range []string{`{}`, `null`, `[]`, `{"path":false}`, `{"path":"../outside"}`, `{"path":"ordinary.md"}`, `{"path":"meeting.md"}`} {
		add("meeting-get-"+args, "meeting_get", args)
		cases[len(cases)-1].Files = map[string]string{
			"meeting.md": "---\ntype: meeting\ntitle: Review <&>\nmeeting_id: m1\nstarted_at: '2026-01-02T03:04:05Z'\nduration_ms: 2500\nlanguage: de\nsymmeet_source: {review_state: reviewed}\n---\nReviewed transcript\n",
			"ordinary.md": "not a meeting\n",
		}
		cases[len(cases)-1].FileMtime = 1767225600
	}
	add("meetings-empty", "meeting_list", `{}`)
	cases[len(cases)-1].Files = nil
	add("docs-unindexed", "desk_docs", `{}`)
	add("review-unindexed", "docs_review", `{}`)
	add("similar-unindexed", "docs_similar", `{"file":"alpha.md"}`)
	return cases
}

func TestMCPReadProcessPortFixture(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	fixture := mcpReadFixture{SchemaVersion: 1}
	for _, c := range mcpReadCases() {
		root := t.TempDir()
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{"home", "config", "data", "cache", "tmp", "vault", "cwd"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		vaultRoot := filepath.Join(root, "vault")
		for path, body := range c.Files {
			p := filepath.Join(vaultRoot, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.FileMtime > 0 {
				stamp := time.Unix(c.FileMtime, 0)
				if err := os.Chtimes(p, stamp, stamp); err != nil { t.Fatal(err) }
			}
		}
		dbPath := filepath.Join(root, "data", "sidecar.db")
		if c.Indexed {
			db, err := sidecar.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			paths := make([]string, 0, len(c.Files))
			for p := range c.Files {
				paths = append(paths, p)
			}
			sort.Strings(paths)
			for _, p := range paths {
				doc, err := vault.ParseBytes(filepath.Join(vaultRoot, filepath.FromSlash(p)), []byte(c.Files[p]))
				if err != nil {
					t.Fatal(err)
				}
				if err := db.IndexDocument(doc); err != nil {
					t.Fatal(err)
				}
			}
			if c.ExternalLink {
				path := filepath.Join(root, "external", "link.md")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("[[alpha]]\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				doc, err := vault.ParseBytes(path, []byte("[[alpha]]\n"))
				if err != nil {
					t.Fatal(err)
				}
				if err := db.IndexDocument(doc); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		args := []string{"mcp"}
		if !c.NoVault {
			args = []string{"--vault", vaultRoot, "mcp"}
		}
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = filepath.Join(root, "cwd")
		cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "USERPROFILE=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "TMPDIR=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "SYMDESK_SIDECAR=" + dbPath, "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH="}
		if s := os.Getenv("SYSTEMROOT"); s != "" {
			cmd.Env = append(cmd.Env, "SYSTEMROOT="+s)
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdin = strings.NewReader(c.Request + "\n")
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			cancel()
			t.Fatalf("%s: %v: %s", c.ID, err, stderr.String())
		}
		cancel()
		c.Stdout = strings.ReplaceAll(stdout.String(), root, "$ROOT")
		c.Stderr = strings.ReplaceAll(stderr.String(), root, "$ROOT")
		c.AfterFiles = map[string]string{}
		err = filepath.WalkDir(vaultRoot, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(vaultRoot, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			c.AfterFiles[filepath.ToSlash(rel)] = string(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		c.AfterIndex = map[string]map[string]interface{}{}
		if _, err := os.Stat(dbPath); err == nil {
			c.SidecarExists = true
			db, err := sidecar.Open(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			documents, err := db.ListFiles("")
			if err != nil {
				t.Fatal(err)
			}
			for _, doc := range documents {
				props, err := db.GetProperties(doc.Path)
				if err != nil {
					t.Fatal(err)
				}
				rel, err := filepath.Rel(vaultRoot, doc.Path)
				if err != nil {
					t.Fatal(err)
				}
				c.AfterIndex[filepath.ToSlash(rel)] = props
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		fixture.Cases = append(fixture.Cases, c)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkMCPSurfaceBytes(t, "../../testdata/port/mcp/read-process.json", append(data, '\n'))
}
