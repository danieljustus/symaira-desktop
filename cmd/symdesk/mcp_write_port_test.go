package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type mcpWriteCase struct {
	HashInputs   map[string]string           `json:"hash_inputs,omitempty"`
	StdoutByGOOS map[string]string           `json:"stdout_by_goos,omitempty"`
	AfterDB      map[string][]map[string]any `json:"after_db"`
	mcpSurfaceCase
	Files         map[string]string                 `json:"files,omitempty"`
	Indexed       bool                              `json:"indexed,omitempty"`
	ExternalLink  bool                              `json:"external_link,omitempty"`
	NoVault       bool                              `json:"no_vault,omitempty"`
	AfterFiles    map[string]string                 `json:"after_files"`
	SidecarExists bool                              `json:"sidecar_exists"`
	AfterIndex    map[string]map[string]interface{} `json:"after_index"`
}
type mcpWriteFixture struct {
	SchemaVersion int            `json:"schema_version"`
	Cases         []mcpWriteCase `json:"cases"`
}

func mcpWriteCases() []mcpWriteCase {
	files := map[string]string{"alpha.md": "---\ntitle: Alpha <&>\nstatus: open\n---\nbody\n"}
	var cases []mcpWriteCase
	add := func(id, name, args string) {
		params := map[string]any{"name": name}
		if args != "" {
			params["arguments"] = json.RawMessage(args)
		}
		request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "call", "method": "tools/call", "params": params})
		if err != nil {
			panic(err)
		}
		cases = append(cases, mcpWriteCase{mcpSurfaceCase: mcpSurfaceCase{ID: id, AllowWrite: true, Request: string(request)}, Files: files})
	}
	for _, name := range []string{"desk_note_new", "doc_set_status", "notebook_create", "notebook_add_source", "notebook_remove_source", "desk_asset_store", "desk_undo_task"} {
		for i, args := range []string{"", `null`, `{}`, `[]`, `{"title":false,"file":7}`, `{"title":"","file":"alpha.md"}`} {
			add(fmt.Sprintf("%s-decode-%d", name, i), name, args)
		}
		add(name+"-read-only", name, `{"title":"No Write","file":"alpha.md","status":"paid"}`)
		cases[len(cases)-1].AllowWrite = false
	}
	for _, status := range []string{"open", "paid", "submitted", "done", "needs_review", "waiting_for_reply", "invalid"} {
		args, _ := json.Marshal(map[string]string{"file": "alpha.md", "status": status})
		add("status-"+status, "doc_set_status", string(args))
	}
	add("status-traversal", "doc_set_status", `{"file":"../outside.md","status":"paid"}`)
	add("note-create", "desk_note_new", `{"title":"New Note","content":"# Hello <&>\nBody"}`)
	add("note-overwrite", "desk_note_new", `{"title":"alpha","content":"Replacement"}`)
	add("note-traversal", "desk_note_new", `{"title":"../outside","content":"no"}`)
	add("note-template-missing", "desk_note_new", `{"title":"Missing Template","template":"absent"}`)
	add("note-template", "desk_note_new", `{"title":"Templated","template":"basic","content":"Extra"}`)
	cases[len(cases)-1].Files = map[string]string{"templates/basic.md": "Hello {{title}}"}
	add("note-no-vault", "desk_note_new", `{"title":"No Vault"}`)
	cases[len(cases)-1].NoVault = true
	for _, args := range []string{`{"title":"Research <&>","description":"A description"}`, `{"title":"   "}`, `{"title":"Research"}`, `{"title":"日本語"}`} {
		add("notebook-create-"+args, "notebook_create", args)
	}
	notebookFiles := map[string]string{"alpha.md": files["alpha.md"], "notebooks/research.md": "---\ntype: notebook\ntitle: Research\ncreated: '2026-01-02'\nnotebook_id: research\nsources: [alpha.md]\n---\nHand-edited body\n"}
	for _, name := range []string{"notebook_add_source", "notebook_remove_source"} {
		for _, source := range []string{"alpha.md", "missing.md", "notebooks/research.md", "../outside.md", "   ", " alpha.md "} {
			args, _ := json.Marshal(map[string]string{"notebook": "research", "path": source})
			add(name+"-"+source, name, string(args))
			cases[len(cases)-1].Files = notebookFiles
		}
		add(name+"-missing", name, `{"notebook":"missing","path":"alpha.md"}`)
	}
	for _, args := range []string{`{"data":"hello","preferred_name":"test.txt"}`, `{"data":"world","preferred_name":"a b.png","extension":"svg"}`, `{"data":"SGVsbG8=","is_base64":true,"preferred_name":"base64.txt"}`, `{"data":"SGVs\r\nbG8=","is_base64":true,"preferred_name":"lines.txt"}`, `{"data":"Zh==","is_base64":true,"preferred_name":"bits.txt"}`, `{"data":"bad!","is_base64":true}`, `{"data":"Zg","is_base64":true}`, `{"data":"a","is_base64":true}`, `{"data":"hi","is_base64":7}`, `{"data":"hi","preferred_name":"folder/a:b.txt"}`, `{"data":"collision","preferred_name":"same.txt"}`} {
		add("asset-"+args, "desk_asset_store", args)
	}
	cases[len(cases)-1].Files = map[string]string{"assets/same.txt": "old", "assets/same-2.txt": "older"}
	add("undo-missing", "desk_undo_task", `{"task_id":"missing"}`)
	add("undo-traversal", "desk_undo_task", `{"task_id":"../escape"}`)
	body := "---\ntitle: Before\n---\nbefore\n"
	hash := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(hash[:])
	cp := map[string]any{"task_id": "task", "timestamp": "2026-01-02T03:04:05Z", "files": []any{map[string]any{"rel_path": "alpha.md", "entry": map[string]any{"id": digest, "timestamp": "2026-01-02T03:04:05Z", "size": len(body)}}}, "new_files": []string{"new.md"}, "skipped": []string{}}
	cpBytes, _ := json.MarshalIndent(cp, "", "  ")
	add("undo-task", "desk_undo_task", `{"task_id":"task"}`)
	cases[len(cases)-1].Files = map[string]string{"alpha.md": "after\n", "new.md": "new\n", ".symdesk/history/objects/" + digest: body, ".symdesk/history/checkpoints/task.json": string(cpBytes)}
	manifest, _ := json.MarshalIndent(cp["files"].([]any)[0].(map[string]any)["entry"], "", "  ")
	cases[len(cases)-1].Files[".symdesk/history/manifest/alpha.md.json"] = "[" + string(manifest) + "]"
	for _, data := range []string{"Zg==", "Zg=", "Zg===", "====", "AA=A", "A===", "AAA=", "AAAA", "AAAA=", "AA==AA==", "AA==!", "AA==\n!", "A\r\n!", "A", "AA", "AAA", "A!AA", "AA!A", "AAA!", "!!!!", "Zg== ", "Zg==\n"} {
		args, _ := json.Marshal(map[string]any{"data": data, "is_base64": true, "preferred_name": "binary.txt"})
		// Valid binary zero bytes are preserved as JSON escapes in the file snapshot.
		add("base64-"+data, "desk_asset_store", string(args))
	}
	add("asset-anonymous", "desk_asset_store", `{"data":"anonymous","extension":"txt"}`)
	add("asset-binary", "desk_asset_store", `{"data":"/wCA","is_base64":true,"preferred_name":"binary.bin"}`)
	// The same production call must preserve framing and notification effects.
	original := append([]mcpWriteCase(nil), cases...)
	for _, c := range original {
		if c.ID == "note-create" || c.ID == "status-paid" || c.ID == "notebook_add_source-missing.md" || c.ID == "undo-task" || c.ID == "asset-binary" {
			c.ID += "-framed"
			c.Framed = true
			cases = append(cases, c)
		}
		if c.ID == "note-create-framed" || c.ID == "desk_note_new-read-only" {
			var request map[string]any
			if err := json.Unmarshal([]byte(c.Request), &request); err != nil {
				panic(err)
			}
			delete(request, "id")
			raw, _ := json.Marshal(request)
			c.ID += "-notification"
			c.Request = string(raw)
			cases = append(cases, c)
		}
	}
	add("note-frontmatter-template", "desk_note_new", `{"title":"Template Note","template":"fm","content":"More"}`)
	templateBody := "---\ntags: [template]\n---\n\nBody"
	cases[len(cases)-1].Files = map[string]string{"templates/fm.md": templateBody}
	cases[len(cases)-1].HashInputs = map[string]string{"Template_Note.md": templateBody + "\nMore"}
	add("status-missing-file", "doc_set_status", `{"file":"missing.md","status":"paid"}`)
	return cases
}

// Normalize only wall-clock generated timestamps/daily journal filenames.
// Content, history object hashes, manifest formatting and index properties remain exact.
var mcpWriteTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z`)
var mcpWriteAsset = regexp.MustCompile(`pasted-\d{4}-\d{2}-\d{2}-\d{6}`)
var mcpWriteJournal = regexp.MustCompile(`journal/\d{4}-\d{2}-\d{2}\.ndjson`)

func normalizeMCPWrite(s string) string {
	return mcpWriteAsset.ReplaceAllString(mcpWriteJournal.ReplaceAllString(mcpWriteTimestamp.ReplaceAllString(s, "<TIME>"), "journal/<DATE>.ndjson"), "pasted-<CLOCK>")
}

func TestMCPWriteProcessPortFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		// This frozen transcript contains Unix os.Root syscall diagnostics and
		// path-bearing write/history observations. No native Windows capture has
		// been recorded; do not manufacture one by rewriting Unix observations.
		// Keep the frozen bytes checked here and by portgen provenance. Linux
		// and macOS still execute and compare every process case below.
		data, err := os.ReadFile("../../testdata/port/mcp/write-process.json")
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != "6d0a8572b1082e6410260be3d7fb37c5fe9001597c55fe103c32714e62453294" {
			t.Fatal("frozen MCP write transcript checksum changed")
		}
		t.Skip("MCP write process syscall transcript recorded on Linux/macOS only; Windows frozen checksum verified, native replay pending")
	}
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	fixture := mcpWriteFixture{SchemaVersion: 1}
	var previous mcpWriteFixture
	if data, err := os.ReadFile("../../testdata/port/mcp/write-process.json"); err == nil {
		if err := json.Unmarshal(data, &previous); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range mcpWriteCases() {
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
		if c.AllowWrite {
			args = append(args, "--allow-write")
		}
		//nolint:gosec // binary is the locally built symdesk fixture; arguments come from fixed test cases.
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = filepath.Join(root, "cwd")
		cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "USERPROFILE=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "TMPDIR=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "SYMDESK_SIDECAR=" + dbPath, "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH="}
		if s := os.Getenv("SYSTEMROOT"); s != "" {
			cmd.Env = append(cmd.Env, "SYSTEMROOT="+s)
		}
		var stdout, stderr bytes.Buffer
		input := c.Request + "\n"
		if c.Framed {
			input = "Content-Length: " + fmtMCPSize(len(c.Request)) + "\r\n\r\n" + c.Request
		}
		cmd.Stdin = strings.NewReader(input)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			cancel()
			t.Fatalf("%s: %v: %s", c.ID, err, stderr.String())
		}
		cancel()
		c.Stdout = normalizeMCPWrite(strings.ReplaceAll(stdout.String(), root, "$ROOT"))
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
			//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if utf8.Valid(data) {
				c.AfterFiles[normalizeMCPWrite(filepath.ToSlash(rel))] = normalizeMCPWrite(string(data))
			} else {
				c.AfterFiles[normalizeMCPWrite(filepath.ToSlash(rel))] = "<BINARY_HEX>" + hex.EncodeToString(data)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		c.AfterDB = map[string][]map[string]any{}
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
				for k, v := range props {
					if text, ok := v.(string); ok {
						props[k] = normalizeMCPWrite(text)
					}
				}
				c.AfterIndex[filepath.ToSlash(rel)] = props
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if c.SidecarExists {
			raw, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"files", "file_properties", "links", "index_lifecycle"} {
				//nolint:gosec // SQL identifiers come from the fixed fixture table list or its SQLite schema, never user input.
				rows, err := raw.Query("SELECT * FROM " + table + " ORDER BY 1,2")
				if err != nil {
					t.Fatal(err)
				}
				cols, err := rows.Columns()
				if err != nil {
					t.Fatal(err)
				}
				entries := []map[string]any{}
				for rows.Next() {
					vals := make([]any, len(cols))
					ptrs := make([]any, len(cols))
					for i := range vals {
						ptrs[i] = &vals[i]
					}
					if err := rows.Scan(ptrs...); err != nil {
						t.Fatal(err)
					}
					entry := map[string]any{}
					for i, col := range cols {
						v := vals[i]
						if ts, ok := v.(time.Time); ok {
							v = ts.UTC().Format(time.RFC3339Nano)
						}
						if b, ok := v.([]byte); ok {
							v = string(b)
						}
						if text, ok := v.(string); ok {
							v = normalizeMCPWrite(strings.ReplaceAll(filepath.ToSlash(text), filepath.ToSlash(root), "$ROOT"))
						}
						entry[col] = v
					}
					if table == "files" {
						path := strings.ReplaceAll(entry["path"].(string), "$ROOT", filepath.ToSlash(root))
						body, err := os.ReadFile(filepath.FromSlash(path))
						if err != nil {
							t.Fatal(err)
						}
						rel, _ := filepath.Rel(vaultRoot, filepath.FromSlash(path))
						if input, ok := c.HashInputs[filepath.ToSlash(rel)]; ok {
							body = []byte(input)
						}
						digest := sha256.Sum256(body)
						if entry["sha256"] != hex.EncodeToString(digest[:]) {
							t.Fatalf("%s: index SHA does not match file", c.ID)
						}
						normalized := sha256.Sum256([]byte(normalizeMCPWrite(string(body))))
						entry["sha256"] = hex.EncodeToString(normalized[:])
					}
					for _, col := range []string{"mtime_ns"} {
						if v, ok := entry[col]; ok && v != nil {
							entry[col] = "<MTIME>"
						}
					}
					entries = append(entries, entry)
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				if err := rows.Close(); err != nil {
					t.Fatal(err)
				}
				c.AfterDB[table] = entries
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
		}
		// os.Root diagnostics carry native syscall/path wording. Record each
		// native OS independently, never invent the unexecuted platform text.
		if c.ID == "undo-missing" || c.ID == "status-missing-file" {
			for _, old := range previous.Cases {
				if old.ID == c.ID {
					c.StdoutByGOOS = old.StdoutByGOOS
				}
			}
			if c.StdoutByGOOS == nil {
				c.StdoutByGOOS = map[string]string{}
			}
			c.StdoutByGOOS[runtime.GOOS] = c.Stdout
			c.Stdout = ""
		}
		fixture.Cases = append(fixture.Cases, c)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkMCPSurfaceBytes(t, "../../testdata/port/mcp/write-process.json", append(data, '\n'))
}
