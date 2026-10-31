package extract

// Go oracle for the optional Magika advisory (RUST-010, INGEST-002). Detect and
// the filetype helpers are driven against fake `magika` executables placed on
// PATH; every scenario records the prediction, the warnings logged through the
// logf callback, what Detect wrote to stderr, the detected kind and the argv
// transcript. The advisory must never change the kind. The script is stored in
// the fixture so the Rust replay executes the identical tool.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/filetype"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const magikaScript = `#!/bin/sh
PATH=/usr/bin:/bin
d=${0%/*}
US=$(printf '\037'); l=M; for a in "$@"; do l="$l$US$a"; done
printf '%s\n' "$l" >> "$d/log"
[ -f "$d/stderr" ] && cat "$d/stderr" >&2
[ -f "$d/stdout" ] && cat "$d/stdout"
[ -f "$d/exit" ] && exit "$(cat "$d/exit")"
exit 0
`

type magikaScenario struct {
	ID string `json:"id"`
	// Tool: "ok", "missing", "noexec", "dir".
	Tool   string `json:"tool"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
	// File is the name of the file given to Detect/filetype; Content its bytes.
	File    string `json:"file"`
	Content []byte `json:"content"`
	// ExtMIME is passed to VerifyAgainstGuess.
	ExtMIME string `json:"ext_mime"`
}

type magikaCase struct {
	Scenario      magikaScenario             `json:"scenario"`
	Available     bool                       `json:"available"`
	Prediction    *filetype.MagikaPrediction `json:"prediction"`
	PredictionLog []string                   `json:"prediction_log"`
	VerifyLog     []string                   `json:"verify_log"`
	Kind          string                     `json:"kind"`
	DetectError   string                     `json:"detect_error"`
	DetectStderr  string                     `json:"detect_stderr"`
	Transcript    []string                   `json:"transcript"`
}

func magikaJSON(label, mime string, score float64) string {
	return fmt.Sprintf(`{"path":"x","result":{"dl":{"ct_label":"%s","score":%v},"output":{"ct_label":"%s","group":"g","mime_type":"%s","score":%v}}}`, label, score, label, mime, score)
}

func magikaScenarios() []magikaScenario {
	pdf := []byte("%PDF-1.4\n%fake\n")
	png := []byte("\x89PNG\r\n\x1a\n0000")
	sc := []magikaScenario{
		{ID: "missing", Tool: "missing", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "noexec", Tool: "noexec", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "dir", Tool: "dir", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "agree-pdf", Tool: "ok", Stdout: magikaJSON("pdf", "application/pdf", 0.99), File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "mismatch-png-as-pdf", Tool: "ok", Stdout: magikaJSON("png", "image/png", 0.5), File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "mismatch-score-rounding", Tool: "ok", Stdout: magikaJSON("png", "image/png", 0.12349), File: "b.png", Content: png, ExtMIME: "application/pdf"},
		{ID: "mismatch-score-rounding-up", Tool: "ok", Stdout: magikaJSON("jpeg", "image/jpeg", 0.9996), File: "b.png", Content: png, ExtMIME: "image/png"},
		{ID: "mismatch-score-zero", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"txt"}}}`, File: "c.png", Content: png, ExtMIME: "image/png"},
		{ID: "label-uppercase", Tool: "ok", Stdout: magikaJSON("PDF", "application/pdf", 0.7), File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "label-uppercase-mismatch", Tool: "ok", Stdout: magikaJSON("DOCX", "x", 0.7), File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "label-text", Tool: "ok", Stdout: magikaJSON("text", "text/plain", 0.9), File: "n.png", Content: png, ExtMIME: "image/png"},
		{ID: "label-unmapped", Tool: "ok", Stdout: magikaJSON("elf", "application/x-elf", 0.9), File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "label-empty", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":""}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "invalid-json", Tool: "ok", Stdout: "not json", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "empty-stdout", Tool: "ok", Stdout: "", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-array", Tool: "ok", Stdout: "[]", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "null-result", Tool: "ok", Stdout: `{"path":"x","result":null}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "no-output-field", Tool: "ok", Stdout: `{"result":{"dl":{"ct_label":"pdf"}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "error-field", Tool: "ok", Stdout: `{"path":"x","result":null,"error":{"code":1}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "exit-nonzero-stderr", Tool: "ok", Exit: 2, Stderr: "  model missing \n", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "exit-nonzero-silent", Tool: "ok", Exit: 3, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "exit-nonzero-valid-json", Tool: "ok", Exit: 1, Stdout: magikaJSON("png", "image/png", 0.5), File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "stderr-on-success", Tool: "ok", Stderr: "warn\n", Stdout: magikaJSON("png", "image/png", 0.5), File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-string-top", Tool: "ok", Stdout: `"x"`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-number-top", Tool: "ok", Stdout: `12`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-bool-top", Tool: "ok", Stdout: `true`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-null-top", Tool: "ok", Stdout: `null`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-trailing", Tool: "ok", Stdout: `{} x`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-truncated-object", Tool: "ok", Stdout: `{"result":`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "json-bad-key", Tool: "ok", Stdout: `{result:1}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-path-number", Tool: "ok", Stdout: `{"path":5,"result":{"output":{"ct_label":"png"}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-result-string", Tool: "ok", Stdout: `{"result":"x"}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-result-array", Tool: "ok", Stdout: `{"result":[]}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-output-bool", Tool: "ok", Stdout: `{"result":{"output":true}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-dl-number", Tool: "ok", Stdout: `{"result":{"dl":7,"output":{"ct_label":"png"}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-label-number", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":3}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-score-string", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"png","score":"0.5"}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-score-overflow", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"png","score":1e999}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-first-error-wins", Tool: "ok", Stdout: `{"result":{"output":{"mime_type":[],"ct_label":3}},"path":4}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "type-null-members", Tool: "ok", Stdout: `{"path":null,"result":{"output":{"ct_label":null,"score":null,"mime_type":null}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "keys-case-insensitive", Tool: "ok", Stdout: `{"RESULT":{"Output":{"CT_LABEL":"png","Score":0.25}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "keys-duplicate-last-wins", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"png"}},"result":{"output":{"ct_label":"pdf"}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "keys-duplicate-null-resets", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"png"}},"result":null}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "keys-duplicate-object-merges", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"png","score":0.5}},"result":{"output":{"ct_label":"pdf"}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "unknown-members-ignored", Tool: "ok", Stdout: `{"x":[1,{"a":null}],"result":{"extra":1,"output":{"ct_label":"png","unknown":{}}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "string-escapes", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"p\u006eg","mime_type":"\ud83d\ude00"}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "invalid-utf8-label", Tool: "ok", Stdout: "{\"result\":{\"output\":{\"ct_label\":\"\xff\"}}}", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "leading-whitespace-bom", Tool: "ok", Stdout: "\ufeff{}", File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "unicode-file-name", Tool: "ok", Stdout: magikaJSON("png", "image/png", 0.5), File: "dä tei.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "large-score", Tool: "ok", Stdout: `{"result":{"output":{"ct_label":"png","score":1e-7}}}`, File: "a.pdf", Content: pdf, ExtMIME: "application/pdf"},
		{ID: "ext-only-text-no-magika-conflict", Tool: "ok", Stdout: magikaJSON("txt", "text/plain", 0.8), File: "t.txt", Content: []byte("hello\n"), ExtMIME: "text/plain"},
		{ID: "text-detected-as-markdown", Tool: "ok", Stdout: magikaJSON("markdown", "text/markdown", 0.8), File: "t.txt", Content: []byte("# hello\n"), ExtMIME: "text/plain"},
		{ID: "unsupported-extension-error", Tool: "ok", Stdout: magikaJSON("pdf", "application/pdf", 0.8), File: "t.zzz", Content: []byte("hello\n"), ExtMIME: "text/plain"},
		{ID: "container-zip-unknown", Tool: "ok", Stdout: magikaJSON("pdf", "application/pdf", 0.8), File: "t.zip", Content: []byte("PK\x03\x04junk"), ExtMIME: "application/zip"},
	}
	return sc
}

// captureStderr runs f and returns what it wrote to os.Stderr.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	f()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return <-done
}

func runMagikaScenario(t *testing.T, sc magikaScenario) magikaCase {
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	files := filepath.Join(root, "files")
	for _, d := range []string{tools, files} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	switch sc.Tool {
	case "ok", "noexec":
		mode := os.FileMode(0o700)
		if sc.Tool == "noexec" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(tools, "magika"), []byte(magikaScript), mode); err != nil { //nolint:gosec // fake tool must be executable
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(tools, "magika"), mode); err != nil { //nolint:gosec // fake tool must be executable
			t.Fatal(err)
		}
	case "dir":
		if err := os.Mkdir(filepath.Join(tools, "magika"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if sc.Stdout != "" {
		write("stdout", sc.Stdout)
	}
	if sc.Stderr != "" {
		write("stderr", sc.Stderr)
	}
	if sc.Exit != 0 {
		write("exit", fmt.Sprint(sc.Exit))
	}
	path := filepath.Join(files, sc.File)
	if err := os.WriteFile(path, sc.Content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools)

	norm := func(s string) string { return strings.ReplaceAll(s, root, "$DIR") }
	c := magikaCase{Scenario: sc}
	c.Available = filetype.Available()
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, norm(fmt.Sprintf(format, args...))) }
	pred, err := filetype.Detect(path, logf)
	if err != nil {
		t.Fatalf("filetype.Detect returned an error: %v", err)
	}
	c.Prediction = pred
	c.PredictionLog = logs
	logs = nil
	filetype.VerifyAgainstGuess(path, sc.ExtMIME, logf)
	c.VerifyLog = logs
	if c.VerifyLog == nil {
		c.VerifyLog = []string{}
	}
	if c.PredictionLog == nil {
		c.PredictionLog = []string{}
	}
	// nil logf must be accepted.
	filetype.VerifyAgainstGuess(path, sc.ExtMIME, nil)

	// Remove the transcript produced by the calls above; Detect is recorded alone.
	_ = os.Remove(filepath.Join(tools, "log"))
	var kind Kind
	var derr error
	c.DetectStderr = norm(captureStderr(t, func() { kind, derr = Detect(path) }))
	c.Kind = string(kind)
	if derr != nil {
		c.DetectError = norm(derr.Error())
	}
	if b, e := os.ReadFile(filepath.Join(tools, "log")); e == nil { //nolint:gosec // test scratch
		for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
			c.Transcript = append(c.Transcript, norm(line))
		}
	}
	if c.Transcript == nil {
		c.Transcript = []string{}
	}
	return c
}

func TestPortIngestMagikaContract(t *testing.T) {
	cases := []magikaCase{}
	for _, sc := range magikaScenarios() {
		cases = append(cases, runMagikaScenario(t, sc))
	}
	labels := []string{"pdf", "PDF", "Pdf", "png", "jpeg", "jpg", "tiff", "webp", "txt", "text", "TEXT", "csv", "markdown", "html", "rtf", "docx", "xlsx", "odt", "eml", "pptx", "ods", "epub", "unknown", "", " pdf", "pdf "}
	mapping := map[string]string{}
	order := []string{}
	for _, l := range labels {
		mapping[l] = filetype.LabelToMIME(l)
		order = append(order, l)
	}

	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"extract.go", "magika_port_test.go", "../filetype/magika.go"} {
		b, e := os.ReadFile(filepath.Join(src, f)) //nolint:gosec // repository source
		if e != nil {
			t.Fatal(e)
		}
		hashes[filepath.Base(f)] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, e := json.MarshalIndent(map[string]any{
		"schema_version": 1,
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"script":         magikaScript,
		"cases":          cases,
		"label_order":    order,
		"label_to_mime":  mapping,
		"normalizations": []string{"scenario temp root replaced by $DIR"},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/magika.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e = os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
	} else {
		old, e := os.ReadFile(out) //nolint:gosec // repository fixture
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, content) {
			t.Fatal("ingest Magika fixture drift; regenerate deliberately")
		}
	}
	t.Logf("froze %d Magika scenarios", len(cases))
}
