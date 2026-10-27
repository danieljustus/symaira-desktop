package notionimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// Synthetic exports only. Production Run owns every result and written byte.
// The sole clock normalization replaces ingested_at and omits report timestamps;
// paths beneath the disposable root become $ROOT on both implementations.
func TestPortNotionContract(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	sourceDir := filepath.Dir(file)
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	norm := func(s string) string { return strings.ReplaceAll(filepath.ToSlash(s), filepath.ToSlash(root), "$ROOT") }
	clock := regexp.MustCompile(`(?m)^ingested_at: [^\n]*`)
	files := map[string]string{
		"export/Workspace/Alpha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.md":     "# Alpha\n\n[Beta](Beta%20bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.md) ![x](assets/image.png)\n",
		"export/Workspace/Deep/Beta bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.md": "[[Alpha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]]\n",
		"export/Workspace/Tasks cccccccccccccccccccccccccccccccc.csv":    "Name,Status,tags,Empty\r\nOne,Open,private,\r\n,Closed,,\r\n",
		"export/Workspace/assets/image.png":                              "synthetic-image",
		"export/Workspace/.hidden/Skip.md":                               "secret",
		"export/Workspace/ignored.txt":                                   "ignore",
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() map[string]any {
		out := map[string]any{}
		err := filepath.WalkDir(filepath.Join(root, "vault"), func(p string, d os.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			body := clock.ReplaceAllString(norm(string(b)), "ingested_at: 2000-01-01T00:00:00Z")
			rel, _ := filepath.Rel(filepath.Join(root, "vault"), p)
			out[filepath.ToSlash(rel)] = map[string]any{"body": body, "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(body)))}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	steps := []any{}
	for _, op := range []string{"dry-run", "import", "rerun", "move", "rerun", "new-run"} {
		if op == "move" {
			if err := os.Rename(filepath.Join(root, "vault", "Alpha.md"), filepath.Join(root, "vault", "Moved.md")); err != nil {
				t.Fatal(err)
			}
			steps = append(steps, map[string]any{"op": op, "files": snapshot()})
			continue
		}
		runID := "fixed-run"
		if op == "new-run" {
			runID = "next-run"
		}
		stats, err := Run(context.Background(), Options{SourceDir: filepath.Join(root, "export"), Vault: filepath.Join(root, "vault"), DryRun: op == "dry-run", ImportRunID: runID, Progress: io.Discard})
		text := ""
		if err != nil {
			text = norm(err.Error())
		}
		raw, e := json.Marshal(stats.Results)
		if e != nil {
			t.Fatal(e)
		}
		var results any
		if e := json.Unmarshal([]byte(norm(string(raw))), &results); e != nil {
			t.Fatal(e)
		}
		steps = append(steps, map[string]any{"op": op, "results": results, "imported": stats.Imported, "skipped": stats.Skipped, "failed": stats.Failed, "total": stats.Total, "error": text, "files": snapshot()})
	}
	names := []string{" Page / name:*?\"<>| ", "", "   ", "..", "Hi aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Hi <aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa>", "Hi AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "Hi aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.md", "Hi aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  "}
	nameCases := []any{}
	for _, name := range names {
		entry := parseMarkdownEntry(filepath.Join(root, name+".md"), "Folder/"+name+".md", root)
		nameCases = append(nameCases, map[string]any{"input": name, "sanitized": sanitizeBaseName(name), "display": entry.DisplayName, "notion_id": entry.NotionID, "vault_path": norm(computeVaultPath(filepath.Join(root, "vault"), entry))})
	}
	links := []any{}
	for _, body := range []string{"[[Alpha aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa]]", "[[Unknown ffffffffffffffffffffffffffffffff]]", "[[ plain ]]", "[alias](nested/Alpha%20aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.md)", "[alias](unknown.md) ![img](x.md)", "[remote](https://example.test/x.md#anchor)", "[[Alias <aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa>]]"} {
		links = append(links, map[string]any{"input": body, "output": rewriteNotionLinks(body, map[string]string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": "Alpha"}, "")})
	}
	csvCases := []any{}
	for _, body := range []string{"", "Name\n", "Name,Value\nOne\n", "Name,Value\nA,b\"c\n", "Name,Value\n\"A,B\",\"line1\nline2\"\n", "Name,Name\nFirst,Last\n", "Name\n\"unfinished"} {
		p := filepath.Join(root, "case.csv")
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
		entry := parseCSVEntry(p, "case.csv", root)
		r := convertCSV(entry, filepath.Join(root, "unused"), "fixed", true)
		raw, e := json.Marshal(r)
		if e != nil {
			t.Fatal(e)
		}
		var result any
		if e := json.Unmarshal([]byte(norm(string(raw))), &result); e != nil {
			t.Fatal(e)
		}
		csvCases = append(csvCases, map[string]any{"input": body, "results": result})
	}
	hashes := map[string]string{}
	for _, name := range []string{"parse.go", "csv.go", "import.go", "links.go", "assets.go", "yaml.go", "port_contract_test.go"} {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		b, e := os.ReadFile(filepath.Join(sourceDir, name))
		if e != nil {
			t.Fatal(e)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	document := map[string]any{"schema_version": 1, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "normalizations": []string{"disposable root -> $ROOT; paths -> slash", "ingested_at -> fixed UTC instant; report wall clocks omitted", "filesystem hashes are of those normalized bytes"}, "input_files": files, "steps": steps, "names": nameCases, "links": links, "csv": csvCases}
	b, e := json.MarshalIndent(document, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	out := filepath.Join(sourceDir, "../../../../testdata/port/ingest/notion.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.MkdirAll(filepath.Dir(out), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(out, b, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		old, e := os.ReadFile(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, b) {
			t.Fatal("notion fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d import steps, %d names, %d links, %d CSV cases", len(steps), len(names), len(links), len(csvCases))
}
