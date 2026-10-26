package paperless

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

func TestPortPaperlessExportContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	norm := func(s string) string { return strings.ReplaceAll(filepath.ToSlash(s), filepath.ToSlash(root), "$ROOT") }
	vault := filepath.Join(root, "vault")
	export := filepath.Join(root, "export")
	for _, p := range []string{vault, export} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	manifest := `[
 {"id":1,"title":"Invoice / α","content":"OCR body","tags":["tax","2024"],"checksum":"first","correspondent":"Sender","document_type":"Invoice","created":"2024-02-03T04:05:06Z","added":"2024-02-04","modified":"2024-02-05","original_file_name":"original.pdf","archived_file_name":"first.PDF","notes":[{"note":"Reviewed","created":"2024-02-06"}]},
 {"id":2,"title":"","archive_serial_number":77,"original_file_name":"missing.txt","notes":[]},
 {"id":3,"title":"Odd:*? title...  ","checksum":"","archived_file_name":"third.png","tags":[]}
]`
	inputs := map[string]string{"export/manifest.json": manifest, "export/first.PDF": "synthetic-pdf", "export/third.png": "synthetic-image"}
	for name, body := range inputs {
		if e := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	clock := regexp.MustCompile(`(?m)^created: "[^"\n]*"`)
	snapshot := func() map[string]any {
		out := map[string]any{}
		e := filepath.WalkDir(vault, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			body := clock.ReplaceAllString(norm(string(b)), `created: "2000-01-01T00:00:00Z"`)
			rel, _ := filepath.Rel(vault, p)
			out[filepath.ToSlash(rel)] = map[string]any{"body": body, "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(body)))}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	steps := []any{}
	for _, op := range []string{"dry-run", "import", "rerun", "move", "rerun", "collision", "rerun"} {
		if op == "move" {
			if e := os.Rename(filepath.Join(vault, "paperless", "Paperless_1_Invoice _ α.md"), filepath.Join(vault, "Moved.md")); e != nil {
				t.Fatal(e)
			}
			steps = append(steps, map[string]any{"op": op, "files": snapshot()})
			continue
		}
		if op == "collision" {
			if e := os.WriteFile(filepath.Join(vault, "collision.md"), []byte("---\nasn: 77\n---\n\nUser document\n"), 0600); e != nil {
				t.Fatal(e)
			}
			steps = append(steps, map[string]any{"op": op, "files": snapshot()})
			continue
		}
		summary, e := Import(ImportOptions{VaultRoot: vault, ExportDir: export, DryRun: op == "dry-run"})
		message := ""
		if e != nil {
			message = norm(e.Error())
		}
		b, e := json.Marshal(summary)
		if e != nil {
			t.Fatal(e)
		}
		var result any
		if e := json.Unmarshal([]byte(norm(string(b))), &result); e != nil {
			t.Fatal(e)
		}
		steps = append(steps, map[string]any{"op": op, "summary": result, "error": message, "files": snapshot()})
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	hashes := map[string]string{}
	for _, name := range []string{"importer.go", "manifest.go", "port_contract_test.go"} {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	b, e := json.MarshalIndent(map[string]any{"schema_version": 1, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "input_files": inputs, "steps": steps, "normalizations": []string{"disposable root -> $ROOT; paths -> slash", "top-level created wall clock -> fixed UTC; hashes cover normalized bytes"}, "db_mode": "nil (filesystem-only export importer contract)"}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	out := filepath.Join(dir, "../../testdata/port/ingest/paperless-export.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.WriteFile(out, b, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		old, e := os.ReadFile(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, b) {
			t.Fatal("Paperless export fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d Paperless export steps", len(steps))
}
