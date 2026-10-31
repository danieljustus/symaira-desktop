package ocr

// Go oracle for the Ollama VLM runner. The real llmkit Ollama client talks to
// a loopback fake Ollama server (scripted status/NDJSON/abort behaviour) and
// the Tesseract fallback is the fake tesseract script from the OCR oracle.
// Each scenario records the Result or error text, the requests the fake server
// saw (method, path, content type and JSON body with the image replaced by a
// marker proving it is the file's base64) and the fallback tool transcript.

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/extract"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type vlmScenario struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	FileName string `json:"file_name"`
	FileB64  string `json:"file_b64"`
	Missing  bool   `json:"missing_file,omitempty"`
	Model    string `json:"model"`
	Prompt   string `json:"prompt"`
	// Server behaviour: "stream" (status + lines), "abort" (lines then drop
	// the connection), "refused" (nothing listens).
	Mode   string   `json:"mode"`
	Status int      `json:"status"`
	Body   string   `json:"body,omitempty"` // raw body for non-2xx
	Lines  []string `json:"lines,omitempty"`
	// Fallback tesseract: "ok" or "fail".
	Fallback string `json:"fallback"`
	NilFB    bool   `json:"nil_fallback_runner,omitempty"`
}

type vlmRequest struct {
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	ContentType string         `json:"content_type"`
	Accept      string         `json:"accept"`
	Body        map[string]any `json:"body"`
	ImageMatch  bool           `json:"image_matches_file"`
}

type vlmCase struct {
	Scenario   vlmScenario     `json:"scenario"`
	Result     *extract.Result `json:"result"`
	Error      string          `json:"error"`
	Requests   []vlmRequest    `json:"requests"`
	Transcript []string        `json:"transcript"`
}

func png1() []byte {
	return []byte("\x89PNG\r\n\x1a\nvlm-image-bytes\x00\xff")
}

func vlmScenarios() []vlmScenario {
	ok := func(id, kind, name string, lines ...string) vlmScenario {
		return vlmScenario{ID: id, Kind: kind, FileName: name, FileB64: base64.StdEncoding.EncodeToString(png1()), Model: "paddleocr-vl:0.9b", Mode: "stream", Status: 200, Lines: lines, Fallback: "ok"}
	}
	chunks := []string{
		`{"model":"paddleocr-vl:0.9b","response":"  Hello ","done":false}`,
		`{"model":"paddleocr-vl:0.9b","response":"World\n","done":false}`,
		`{"model":"paddleocr-vl:0.9b","response":"Zweite Zeile  \n","done":false}`,
		`{"model":"paddleocr-vl:0.9b","response":"","done":true}`,
	}
	var out []vlmScenario
	for _, k := range []struct{ id, kind, name string }{
		{"png", "image/png", "scan.png"}, {"jpeg", "image/jpeg", "scan.jpg"}, {"tiff", "image/tiff", "scan.tif"}, {"webp", "image/webp", "scan.webp"},
	} {
		out = append(out, ok("success-"+k.id, k.kind, k.name, chunks...))
	}
	s := ok("success-custom-prompt-html-chars", "image/png", "scan.png", chunks...)
	s.Prompt = "Read <everything> & \"quote\" it\u2028now"
	out = append(out, s)
	s = ok("success-empty-model-uses-default", "image/png", "scan.png", chunks...)
	s.Model = ""
	out = append(out, s)
	out = append(out, ok("success-blank-lines-and-missing-fields", "image/png", "scan.png",
		``, `{"response":"A"}`, ``, `{"model":null,"response":null,"done":null}`, `{"response":"B","extra":{"x":1}}`, `{"done":true}`))
	out = append(out, ok("success-empty-text", "image/png", "scan.png", `{"response":"   \n ","done":true}`))
	out = append(out, ok("success-no-chunks", "image/png", "scan.png"))
	out = append(out, ok("success-unicode-and-escapes", "image/png", "scan.png",
		`{"response":"Gr\u00f6\u00dfe \u20ac ","done":false}`, `{"response":"\ud83d\ude00 \"q\" \\ tab\t","done":true}`))
	// PDF and HEIC bypass the model entirely.
	for _, k := range []struct{ id, kind, name string }{{"pdf", "application/pdf", "scan.pdf"}, {"heic", "image/heic", "scan.heic"}} {
		s := ok("fallback-direct-"+k.id, k.kind, k.name, chunks...)
		out = append(out, s)
	}
	out = append(out, ok("unsupported-kind-docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "a.docx", chunks...))
	out = append(out, ok("unsupported-kind-text", "text/plain", "a.txt", chunks...))
	// Ollama failures fall back to tesseract.
	for _, st := range []struct {
		id     string
		status int
		body   string
	}{
		{"404-model-not-found", 404, `{"error":"model 'paddleocr-vl:0.9b' not found, try pulling it first"}`},
		{"500-plain", 500, "boom"},
		{"503-empty", 503, ""},
		{"401", 401, `{"error":"unauthorized"}`},
		{"400-json-error", 400, `{"error":"bad request: unsupported image"}`},
	} {
		s := ok("fallback-"+st.id, "image/png", "scan.png")
		s.Status, s.Body = st.status, st.body
		out = append(out, s)
		f := s
		f.ID = "fallback-fails-" + st.id
		f.Fallback = "fail"
		out = append(out, f)
	}
	s = ok("fallback-malformed-chunk", "image/png", "scan.png", `{"response":"partial","done":false}`, `not json at all`)
	out = append(out, s)
	f := s
	f.ID, f.Fallback = "fallback-fails-malformed-chunk", "fail"
	out = append(out, f)
	s = ok("fallback-stream-aborted-midway", "image/png", "scan.png", `{"response":"partial","done":false}`)
	s.Mode = "abort"
	out = append(out, s)
	s = ok("fallback-connection-refused", "image/png", "scan.png")
	s.Mode = "refused"
	out = append(out, s)
	f = s
	f.ID, f.Fallback = "fallback-fails-connection-refused", "fail"
	out = append(out, f)
	s = ok("missing-image-file", "image/png", "scan.png", chunks...)
	s.Missing = true
	out = append(out, s)
	return out
}

func vlmNormalize(s, root, tmp, host string) string {
	if host != "" {
		s = strings.ReplaceAll(s, host, "$HOST")
	}
	return normalize(s, root, tmp)
}

func runVLMScenario(t *testing.T, sc vlmScenario) vlmCase {
	t.Helper()
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	dir, tmp := filepath.Join(root, "tools"), filepath.Join(root, "tmp")
	for _, d := range []string{dir, tmp, filepath.Join(root, "in")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("FAKE_PROBE", "")
	write := func(name, content string, mode os.FileMode) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("tesseract", tesseractScript, 0o700)
	write("pdftoppm", pdftoppmScript, 0o700)
	write("sips", sipsScript, 0o700)
	write("langs.out", "List of available languages in \"/usr/share/tessdata/\" (2):\neng\ndeu\n", 0o600)
	write("pages", "1.png\n", 0o600)
	if sc.Fallback == "fail" {
		if err := os.MkdirAll(filepath.Join(dir, "fail"), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"scan.png", "scan.jpg", "scan.tif", "scan.webp"} {
			write("fail/"+n, "1", 0o600)
			write("fail/"+n+".err", "tesseract exploded\n", 0o600)
		}
		write("pdftoppm.exit", "1", 0o600)
		write("pdftoppm.err", "pdftoppm exploded\n", 0o600)
		write("sips.exit", "1", 0o600)
		write("sips.err", "sips exploded\n", 0o600)
	} else {
		if err := os.MkdirAll(filepath.Join(dir, "out"), 0o700); err != nil {
			t.Fatal(err)
		}
		write("out/default", "Fallback  text\n", 0o600)
	}
	raw, _ := base64.StdEncoding.DecodeString(sc.FileB64)
	input := filepath.Join(root, "in", sc.FileName)
	if !sc.Missing {
		if err := os.WriteFile(input, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var requests []vlmRequest
	var baseURL, host string
	switch sc.Mode {
	case "refused":
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		host = l.Addr().String()
		baseURL = "http://" + host
		_ = l.Close()
	default:
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			rec := vlmRequest{Method: r.Method, Path: r.URL.Path, ContentType: r.Header.Get("Content-Type"), Accept: r.Header.Get("Accept")}
			var parsed map[string]any
			if err := json.Unmarshal(body, &parsed); err == nil {
				if imgs, ok := parsed["images"].([]any); ok && len(imgs) == 1 && imgs[0] == base64.StdEncoding.EncodeToString(raw) {
					rec.ImageMatch = true
					parsed["images"] = []any{"$FILE_B64"}
				}
				rec.Body = parsed
			}
			requests = append(requests, rec)
			if sc.Status != 200 {
				w.WriteHeader(sc.Status)
				_, _ = w.Write([]byte(sc.Body))
				return
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.WriteHeader(200)
			fl := w.(http.Flusher)
			for _, line := range sc.Lines {
				_, _ = w.Write([]byte(line + "\n"))
				fl.Flush()
			}
			if sc.Mode == "abort" {
				panic(http.ErrAbortHandler)
			}
		}))
		defer srv.Close()
		baseURL = srv.URL
		host = strings.TrimPrefix(srv.URL, "http://")
	}

	fallback := &Runner{Tesseract: filepath.Join(dir, "tesseract"), PDFToPPM: filepath.Join(dir, "pdftoppm"), SIPS: filepath.Join(dir, "sips"), OCRLang: "eng"}
	vlm := NewVLMRunner(baseURL, sc.Model, "eng", fallback)
	vlm.Prompt = sc.Prompt

	oldStderr := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pw
	var captured bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&captured, pr); close(done) }()
	res, runErr := vlm.Extract(t.Context(), input, extract.Kind(sc.Kind))
	_ = pw.Close()
	<-done
	os.Stderr = oldStderr

	c := vlmCase{Scenario: sc, Requests: requests}
	if c.Requests == nil {
		c.Requests = []vlmRequest{}
	}
	if runErr != nil {
		c.Error = vlmNormalize(runErr.Error(), root, tmp, host)
	} else {
		c.Result = res
	}
	if strings.TrimSpace(captured.String()) != "" {
		c.Error += " | STDERR " + vlmNormalize(captured.String(), root, tmp, host)
	}
	c.Transcript = []string{}
	if b, e := os.ReadFile(filepath.Join(dir, "log")); e == nil {
		for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			c.Transcript = append(c.Transcript, vlmNormalize(l, root, tmp, host))
		}
		c.Transcript = normalizeTranscript(c.Transcript)
	}
	return c
}

func TestPortIngestVLMContract(t *testing.T) {
	var cases []vlmCase
	for _, sc := range vlmScenarios() {
		cases = append(cases, runVLMScenario(t, sc))
	}
	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"vlm.go", "vlm_port_test.go"} {
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
		"scripts":        map[string]string{"tesseract": tesseractScript, "pdftoppm": pdftoppmScript, "sips": sipsScript},
		"cases":          cases,
		"normalizations": []string{
			"temp roots replaced by $DIR/$TMP; loopback host:port by $HOST; the request image by $FILE_B64 (image_matches_file proves equality)",
		},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/vlm.json")
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
		t.Fatal("ingest VLM fixture drift; regenerate deliberately")
	}
}
