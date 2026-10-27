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

func paperlessExportContract(t *testing.T, unicodeBoundary bool) map[string]any {
	t.Helper()
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
	if unicodeBoundary {
		manifest = fmt.Sprintf(`[{"id":4,"title":%q,"checksum":"unicode-boundary"}]`, strings.Repeat("x", 119)+"α")
	}
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
			//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
			b, e := os.ReadFile(p)
			if e != nil {
				return e
			}
			body := clock.ReplaceAllString(norm(string(b)), `created: "2000-01-01T00:00:00Z"`)
			rel, _ := filepath.Rel(vault, p)
			out[filepath.ToSlash(rel)] = map[string]any{"path_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(filepath.ToSlash(rel)))), "body": body, "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(body)))}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		return out
	}
	steps := []any{}
	ops := []string{"dry-run", "import", "rerun", "move", "rerun", "collision", "rerun"}
	if unicodeBoundary {
		ops = ops[:3]
	}
	for _, op := range ops {
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
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	t.Logf("replayed %d Paperless export steps (unicode=%v, GOOS=%s)", len(steps), unicodeBoundary, runtime.GOOS)
	return map[string]any{"schema_version": 1, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "input_files": inputs, "steps": steps, "normalizations": []string{"disposable root -> $ROOT; paths -> slash", "top-level created wall clock -> fixed UTC; hashes cover normalized bytes"}, "db_mode": "nil (filesystem-only export importer contract)"}
}

const paperlessWindowsNotCovered = "Windows Unicode-boundary filename behavior is not covered; requires a native Windows oracle"

func TestPortPaperlessExportContract(t *testing.T) {
	checkPaperlessExportFixture(t, "paperless-export.json", paperlessExportContract(t, false), false)
}

func TestPortPaperlessExportUnicodeContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		if !strings.Contains(paperlessWindowsNotCovered, "not covered") {
			t.Fatal("Windows skip must explicitly record missing coverage")
		}
		t.Skip(paperlessWindowsNotCovered)
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Unicode-boundary oracle covers darwin and linux only")
	}
	checkPaperlessExportFixture(t, "paperless-export-unicode.json", paperlessExportContract(t, true), true)
}

func checkPaperlessExportFixture(t *testing.T, name string, actual map[string]any, platform bool) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(filepath.Dir(file), "../../testdata/port/ingest", name)
	//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
	old, err := os.ReadFile(out)
	if err != nil && (!os.IsNotExist(err) || os.Getenv("PORT_GENERATE") != "1") {
		t.Fatal(err)
	}
	expected := map[string]any{}
	if len(old) != 0 {
		if err := json.Unmarshal(old, &expected); err != nil {
			t.Fatal(err)
		}
	}
	if platform {
		// Preserve raw platform JSON, including Go's escaped replacement for invalid
		// UTF-8 path bytes, when updating only the current native entry.
		var saved struct {
			Platforms map[string]json.RawMessage `json:"platforms"`
		}
		if len(old) != 0 {
			if err := json.Unmarshal(old, &saved); err != nil {
				t.Fatal(err)
			}
		}
		platforms := saved.Platforms
		if platforms == nil {
			platforms = map[string]json.RawMessage{}
		}
		entry, err := json.Marshal(map[string]any{"input_files": actual["input_files"], "steps": actual["steps"]})
		if err != nil {
			t.Fatal(err)
		}
		if os.Getenv("PORT_GENERATE") != "1" {
			want, ok := platforms[runtime.GOOS]
			if !ok {
				t.Fatalf("missing Paperless Unicode oracle for %s", runtime.GOOS)
			}
			a, _ := json.Marshal(json.RawMessage(entry))
			b, _ := json.Marshal(want)
			if !bytes.Equal(a, b) {
				t.Fatal("Paperless Unicode platform fixture drift; regenerate deliberately")
			}
		}
		platforms[runtime.GOOS] = entry
		delete(actual, "input_files")
		delete(actual, "steps")
		actual["platforms"] = platforms
		actual["not_covered"] = map[string]string{"windows": paperlessWindowsNotCovered}
	}
	b, err := json.MarshalIndent(actual, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(out, b, 0600); err != nil {
			t.Fatal(err)
		}
	} else if !bytes.Equal(old, b) {
		t.Fatal("Paperless export fixture drift; regenerate deliberately")
	}
}
