package ocr

// Go oracle for the OCR adapter (tesseract / pdftoppm / sips). The Runner is
// driven against fake tool scripts that log their argv, working directory and
// environment probe; every scenario records the Result or error text, what
// the Runner wrote to stderr, and the normalized process transcript. The
// scripts themselves are stored in the fixture so the Rust replay executes
// byte-identical tools.

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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/extract"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const tesseractScript = `#!/bin/sh
d=$(dirname "$0")
US=$(printf '\037'); RS=$(printf '\036'); l=T; for a in "$@"; do l="$l$US$a"; done
printf '%s\n' "$l$RS$(pwd)$RS$FAKE_PROBE" >> "$d/log"
if [ "$1" = "--list-langs" ]; then
  [ -f "$d/langs.out" ] && cat "$d/langs.out"
  [ -f "$d/langs.err" ] && cat "$d/langs.err" >&2
  [ -f "$d/langs.exit" ] && exit "$(cat "$d/langs.exit")"
  exit 0
fi
lang=$2
img=$3
base=$(basename "$img")
if [ -f "$d/sleep/$base" ]; then exec sleep "$(cat "$d/sleep/$base")"; fi
if [ -f "$d/fail/$base" ]; then
  [ -s "$d/fail/$base.err" ] && cat "$d/fail/$base.err" >&2
  exit "$(cat "$d/fail/$base")"
fi
if [ -f "$d/out/$lang/$base" ]; then cat "$d/out/$lang/$base"; exit 0; fi
if [ -f "$d/out/$base" ]; then cat "$d/out/$base"; exit 0; fi
if [ -f "$d/out/default" ]; then cat "$d/out/default"; exit 0; fi
exit 0
`

const pdftoppmScript = `#!/bin/sh
d=$(dirname "$0")
US=$(printf '\037'); RS=$(printf '\036'); l=P; for a in "$@"; do l="$l$US$a"; done
printf '%s\n' "$l$RS$(pwd)$RS$FAKE_PROBE" >> "$d/log"
if [ -f "$d/pdftoppm.exit" ]; then
  [ -f "$d/pdftoppm.err" ] && cat "$d/pdftoppm.err" >&2
  exit "$(cat "$d/pdftoppm.exit")"
fi
prefix=$5
while IFS= read -r name; do
  [ -n "$name" ] && printf 'PNG-%s' "$name" > "$prefix-$name"
done < "$d/pages"
exit 0
`

const sipsScript = `#!/bin/sh
d=$(dirname "$0")
US=$(printf '\037'); RS=$(printf '\036'); l=S; for a in "$@"; do l="$l$US$a"; done
printf '%s\n' "$l$RS$(pwd)$RS$FAKE_PROBE" >> "$d/log"
if [ -f "$d/sips.exit" ]; then
  [ -f "$d/sips.err" ] && cat "$d/sips.err" >&2
  exit "$(cat "$d/sips.exit")"
fi
out=$6
printf 'SIPS-PNG' > "$out"
exit 0
`

// ocrScenario describes the files placed next to the fake tools.
type ocrScenario struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	FileName string `json:"file_name"`
	Lang     string `json:"lang"`
	// Tool selection: "ok", "missing", "empty", "dot", "dir", "noexec".
	Tesseract string `json:"tesseract"`
	PDFToPPM  string `json:"pdftoppm"`
	SIPS      string `json:"sips"`
	// Files written into the tool directory (relative path -> content).
	Files map[string]string `json:"files"`
	// Ctx: "background", "cancelled", or "timeout-<ms>".
	Ctx string `json:"ctx"`
	// Probe is the FAKE_PROBE value visible in the environment.
	Probe string `json:"probe"`
	// Repeat runs Extract this many extra times on the same Runner
	// (language-validation cache evidence).
	Repeat int `json:"repeat,omitempty"`
}

type ocrCase struct {
	Scenario   ocrScenario     `json:"scenario"`
	Result     *extract.Result `json:"result"`
	Error      string          `json:"error"`
	Stderr     string          `json:"stderr"`
	Transcript []string        `json:"transcript"`
}

var tmpPDFDir = regexp.MustCompile(`symingest-(pdf|heic)-[0-9]+`)

func germanText() string {
	return "Der Vertrag und die Rechnung wurden nicht bezahlt, weil das Konto leer ist und der Kunde die Ware nicht erhalten hat. Es ist ein Problem mit der Lieferung und mit den Zahlungen der Firma."
}

func normalize(s, dir, tmp string) string {
	if cwd, err := os.Getwd(); err == nil {
		s = strings.ReplaceAll(s, cwd, "$CWD")
	}
	s = strings.ReplaceAll(s, dir, "$DIR")
	s = strings.ReplaceAll(s, tmp, "$TMP")
	return tmpPDFDir.ReplaceAllString(s, "symingest-$1-N")
}

func normalizeTranscript(lines []string) []string {
	// Concurrent page OCR makes the order of consecutive per-page tesseract
	// invocations scheduler-dependent: sort each maximal run.
	isPage := func(l string) bool {
		return strings.HasPrefix(l, "T") && !strings.Contains(l, "--list-langs") && strings.Contains(l, "page-")
	}
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		if !isPage(lines[i]) {
			out = append(out, lines[i])
			i++
			continue
		}
		j := i
		for j < len(lines) && isPage(lines[j]) {
			j++
		}
		run := append([]string(nil), lines[i:j]...)
		sort.Strings(run)
		out = append(out, run...)
		i = j
	}
	return out
}

func runOCRScenario(t *testing.T, sc ocrScenario) ocrCase {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "tools")
	tmp := filepath.Join(root, "tmp")
	for _, d := range []string{dir, tmp, filepath.Join(root, "in")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("FAKE_PROBE", sc.Probe)
	// Resolve symlinked temp roots (macOS /var -> /private/var) so cwd output
	// and the placeholders agree.
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
		dir = filepath.Join(root, "tools")
		tmp = filepath.Join(root, "tmp")
		t.Setenv("TMPDIR", tmp)
	}
	write := func(name, content string, mode os.FileMode) {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	scripts := map[string]string{"tesseract": tesseractScript, "pdftoppm": pdftoppmScript, "sips": sipsScript}
	paths := map[string]string{}
	for name, mode := range map[string]string{"tesseract": sc.Tesseract, "pdftoppm": sc.PDFToPPM, "sips": sc.SIPS} {
		switch mode {
		case "ok", "":
			write(name, scripts[name], 0o700)
			paths[name] = filepath.Join(dir, name)
		case "missing":
			paths[name] = filepath.Join(dir, "no-such", name)
		case "empty":
			paths[name] = ""
		case "dot":
			paths[name] = "."
		case "dir":
			if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
			paths[name] = filepath.Join(dir, name)
		case "noexec":
			write(name, scripts[name], 0o600)
			paths[name] = filepath.Join(dir, name)
		case "badformat":
			write(name, "not a script\n", 0o700)
			paths[name] = filepath.Join(dir, name)
		}
	}
	for name, content := range sc.Files {
		write(name, content, 0o600)
	}
	input := filepath.Join(root, "in", sc.FileName)
	if err := os.WriteFile(input, []byte("IMG"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Capture what the runner prints to stderr.
	oldStderr := os.Stderr
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = pw
	var captured bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&captured, pr); close(done) }()

	ctx := context.Background()
	var cancel context.CancelFunc
	switch {
	case sc.Ctx == "cancelled":
		ctx, cancel = context.WithCancel(ctx)
		cancel()
	case strings.HasPrefix(sc.Ctx, "cancel-when:"):
		// Cancel once every '|'-separated marker appears in the tool log, so
		// the outcome does not depend on wall-clock timing or machine load.
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		markers := strings.Split(strings.TrimPrefix(sc.Ctx, "cancel-when:"), "|")
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if b, e := os.ReadFile(filepath.Join(dir, "log")); e == nil {
					all := true
					for _, m := range markers {
						if !strings.Contains(string(b), m) {
							all = false
						}
					}
					if all {
						cancel()
						return
					}
				}
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	r := &Runner{Tesseract: paths["tesseract"], PDFToPPM: paths["pdftoppm"], SIPS: paths["sips"], OCRLang: sc.Lang}
	res, runErr := r.Extract(ctx, input, extract.Kind(sc.Kind))
	for i := 0; i < sc.Repeat; i++ {
		res, runErr = r.Extract(ctx, input, extract.Kind(sc.Kind))
	}
	_ = pw.Close()
	<-done
	os.Stderr = oldStderr

	c := ocrCase{Scenario: sc}
	if runErr != nil {
		c.Error = normalize(runErr.Error(), root, tmp)
	} else {
		c.Result = res
	}
	c.Stderr = normalize(captured.String(), root, tmp)
	if b, e := os.ReadFile(filepath.Join(dir, "log")); e == nil {
		lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
		for i := range lines {
			lines[i] = normalize(lines[i], root, tmp)
		}
		c.Transcript = normalizeTranscript(lines)
	}
	if left, e := os.ReadDir(tmp); e == nil && len(left) > 0 {
		names := make([]string, 0, len(left))
		for _, l := range left {
			names = append(names, tmpPDFDir.ReplaceAllString(l.Name(), "symingest-N"))
		}
		c.Transcript = append(c.Transcript, "LEFTOVER "+strings.Join(names, ","))
	}
	return c
}

func ocrScenarios() []ocrScenario {
	langs := "List of available languages in \"/usr/share/tessdata/\" (3):\neng\ndeu\nosd\n"
	base := func(id, kind, name string) ocrScenario {
		return ocrScenario{ID: id, Kind: kind, FileName: name, Lang: "eng", Tesseract: "ok", PDFToPPM: "ok", SIPS: "ok", Ctx: "background", Probe: "probe-" + id,
			Files: map[string]string{"langs.out": langs, "out/default": "Hello  World\r\n\r\n\r\n\r\nSecond   line\n", "pages": "1.png\n10.png\n2.png\n"}}
	}
	var out []ocrScenario
	add := func(sc ocrScenario, f func(*ocrScenario)) {
		if f != nil {
			f(&sc)
		}
		out = append(out, sc)
	}
	for _, k := range []struct{ kind, name string }{
		{"image/png", "scan.png"}, {"image/jpeg", "scan.jpg"}, {"image/tiff", "scan.tif"}, {"image/webp", "scan.webp"},
		{"image/png", "name with spaces & ünï.png"}, {"image/png", "-dash.png"},
	} {
		add(base("image-"+k.name, k.kind, k.name), nil)
	}
	add(base("image-deu", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = "deu" })
	add(base("image-deu-plus-eng", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = "deu+eng" })
	add(base("image-lang-spaces", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = " eng + deu " })
	add(base("image-lang-empty", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = "" })
	add(base("image-lang-plus-only", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = "+" })
	add(base("image-lang-missing-partial", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = "eng+fra" })
	add(base("image-lang-missing-all", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = "fra+spa" })
	add(base("image-lang-list-fails", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Lang = "fra"
		s.Files["langs.exit"] = "3"
		s.Files["langs.err"] = "boom"
	})
	add(base("image-lang-list-empty", "image/png", "scan.png"), func(s *ocrScenario) { s.Lang = "eng"; s.Files["langs.out"] = "" })
	add(base("image-lang-list-colon-filter", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Lang = "eng"
		s.Files["langs.out"] = "List of available languages (2):\n eng \n\nscript/Latin\nfoo:bar\navailable-x\ndeu\n"
	})
	add(base("image-german-retry", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Files["out/default"] = germanText()
		s.Files["out/deu+eng/scan.png"] = "Neu erkannter Text mit Umlauten: Größe und Straße.\n"
	})
	add(base("image-german-retry-unavailable", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Files["out/default"] = germanText()
		s.Files["langs.out"] = "List of available languages (2):\neng\nosd\n"
	})
	add(base("image-german-already-deu", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Lang = "deu"
		s.Files["out/default"] = germanText()
	})
	add(base("image-german-retry-fails", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Files["out/default"] = germanText()
		s.Files["fail/scan.png"] = "7"
	})
	add(base("image-fail-stderr", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Files["fail/scan.png"] = "2"
		s.Files["fail/scan.png.err"] = "  Error in pixReadStream: bad format  \n\n"
	})
	add(base("image-fail-silent", "image/png", "scan.png"), func(s *ocrScenario) { s.Files["fail/scan.png"] = "3" })
	add(base("image-empty-output", "image/png", "scan.png"), func(s *ocrScenario) { s.Files["out/default"] = "" })
	add(base("image-hyphen-output", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Files["out/default"] = "Dokumen-\nt mit Ab-\r\nsatz  und\u00a0Tab\t\tende\n"
	})
	add(base("image-ctx-cancelled", "image/png", "scan.png"), func(s *ocrScenario) { s.Ctx = "cancelled" })
	add(base("image-ctx-cancel-kills-tool", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Ctx = "cancel-when:-l\x1feng\x1fscan.png"
		s.Files["sleep/scan.png"] = "20"
	})
	add(base("image-tesseract-missing", "image/png", "scan.png"), func(s *ocrScenario) { s.Tesseract = "missing" })
	add(base("image-tesseract-empty-path", "image/png", "scan.png"), func(s *ocrScenario) { s.Tesseract = "empty" })
	add(base("image-tesseract-dot-path", "image/png", "scan.png"), func(s *ocrScenario) { s.Tesseract = "dot" })
	add(base("image-tesseract-dir", "image/png", "scan.png"), func(s *ocrScenario) { s.Tesseract = "dir" })
	add(base("image-tesseract-noexec", "image/png", "scan.png"), func(s *ocrScenario) { s.Tesseract = "noexec" })
	add(base("image-tesseract-badformat", "image/png", "scan.png"), func(s *ocrScenario) { s.Tesseract = "badformat" })
	add(base("image-env-probe-empty", "image/png", "scan.png"), func(s *ocrScenario) { s.Probe = "" })
	add(base("image-repeat-cache", "image/png", "scan.png"), func(s *ocrScenario) { s.Repeat = 2; s.Lang = "eng+fra" })
	add(base("image-repeat-list-fails-cached", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Repeat = 1
		s.Files["langs.exit"] = "1"
	})
	add(base("image-repeat-german-retry", "image/png", "scan.png"), func(s *ocrScenario) {
		s.Repeat = 1
		s.Files["out/default"] = germanText()
		s.Files["out/deu+eng/scan.png"] = "Neu\n"
	})
	add(base("image-unsupported-kind", "text/plain", "scan.txt"), nil)
	add(base("image-unsupported-kind-empty", "", "scan.bin"), nil)
	// PDF
	add(base("pdf-three-pages", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		s.Files["out/default"] = "page text"
		s.Files["out/page-1.png"] = "Seite eins\n"
		s.Files["out/page-10.png"] = "Seite zehn\n"
		s.Files["out/page-2.png"] = "Seite zwei\n"
	})
	add(base("pdf-many-pages", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		var pages, outs []string
		for i := 1; i <= 12; i++ {
			pages = append(pages, fmt.Sprintf("%02d.png", i))
			s.Files[fmt.Sprintf("out/page-%02d.png", i)] = fmt.Sprintf("body of page %d\n", i)
		}
		_ = outs
		s.Files["pages"] = strings.Join(pages, "\n") + "\n"
	})
	add(base("pdf-no-pages", "application/pdf", "doc.pdf"), func(s *ocrScenario) { s.Files["pages"] = "" })
	add(base("pdf-non-png-artifacts", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		s.Files["pages"] = "1.png\n2.PNG\n3.txt\n4.png.bak\n"
		s.Files["out/default"] = "x"
	})
	add(base("pdf-pdftoppm-fails", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		s.Files["pdftoppm.exit"] = "1"
		s.Files["pdftoppm.err"] = "Syntax Error: Couldn't read xref table\n"
	})
	add(base("pdf-pdftoppm-fails-silent", "application/pdf", "doc.pdf"), func(s *ocrScenario) { s.Files["pdftoppm.exit"] = "9" })
	add(base("pdf-pdftoppm-missing", "application/pdf", "doc.pdf"), func(s *ocrScenario) { s.PDFToPPM = "missing" })
	add(base("pdf-pdftoppm-empty-path", "application/pdf", "doc.pdf"), func(s *ocrScenario) { s.PDFToPPM = "empty" })
	add(base("pdf-tesseract-missing", "application/pdf", "doc.pdf"), func(s *ocrScenario) { s.Tesseract = "missing" })
	add(base("pdf-page-fails", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		s.Files["pages"] = "1.png\n2.png\n3.png\n"
		s.Files["fail/page-2.png"] = "4"
		s.Files["fail/page-2.png.err"] = "page two broke\n"
	})
	add(base("pdf-german-retry", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		s.Files["pages"] = "1.png\n2.png\n"
		s.Files["out/default"] = germanText()
		s.Files["out/deu+eng/page-1.png"] = "Erste Seite mit Größe\n"
		s.Files["out/deu+eng/page-2.png"] = "Zweite Seite\n"
	})
	add(base("pdf-lang-missing-all", "application/pdf", "doc.pdf"), func(s *ocrScenario) { s.Lang = "fra" })
	add(base("pdf-ctx-cancelled", "application/pdf", "doc.pdf"), func(s *ocrScenario) { s.Ctx = "cancelled" })
	add(base("pdf-ctx-cancel-kills-page", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		s.Ctx = "cancel-when:page-1.png"
		s.Files["pages"] = "1.png\n"
		s.Files["sleep/page-1.png"] = "20"
	})
	add(base("pdf-empty-text-pages", "application/pdf", "doc.pdf"), func(s *ocrScenario) {
		s.Files["pages"] = "1.png\n2.png\n"
		s.Files["out/default"] = ""
	})
	// HEIC
	add(base("heic-ok", "image/heic", "photo.heic"), func(s *ocrScenario) { s.Files["out/default"] = "from heic\n" })
	add(base("heic-sips-fails", "image/heic", "photo.heic"), func(s *ocrScenario) {
		s.Files["sips.exit"] = "13"
		s.Files["sips.err"] = "Error: unsupported\n"
	})
	add(base("heic-sips-missing", "image/heic", "photo.heic"), func(s *ocrScenario) { s.SIPS = "missing" })
	add(base("heic-sips-empty", "image/heic", "photo.heic"), func(s *ocrScenario) { s.SIPS = "empty" })
	add(base("heic-tesseract-fails", "image/heic", "photo.heic"), func(s *ocrScenario) { s.Files["fail/converted.png"] = "5" })
	add(base("heic-tesseract-missing", "image/heic", "photo.heic"), func(s *ocrScenario) { s.Tesseract = "missing" })
	add(base("heic-german-retry", "image/heic", "photo.heic"), func(s *ocrScenario) {
		s.Files["out/default"] = germanText()
		s.Files["out/deu+eng/converted.png"] = "Neu\n"
	})
	return out
}

func TestPortIngestOCRContract(t *testing.T) {
	cases := []ocrCase{}
	for _, sc := range ocrScenarios() {
		sc := sc
		cases = append(cases, runOCRScenario(t, sc))
	}

	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"ocr.go", "ocr_port_test.go"} {
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
			"temp roots replaced by $DIR (case) and $TMP (TMPDIR); symingest-pdf/heic-<random> by symingest-pdf-N",
			"consecutive per-page tesseract invocations sorted (4-way concurrent in Go)",
			"invalid UTF-8 in Go result strings encoded using encoding/json replacement semantics",
		},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/ocr.json")
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
			var prev struct {
				Cases []json.RawMessage `json:"cases"`
			}
			if json.Unmarshal(old, &prev) == nil {
				for i, c := range cases {
					cur, _ := json.Marshal(c)
					var a, b bytes.Buffer
					if i < len(prev.Cases) && json.Compact(&a, prev.Cases[i]) == nil && json.Compact(&b, cur) == nil && !bytes.Equal(a.Bytes(), b.Bytes()) {
						t.Errorf("case %q differs:\nrecorded %s\ncurrent  %s", c.Scenario.ID, a.String(), b.String())
					}
				}
			}
			t.Fatal("ingest OCR fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d OCR scenarios", len(cases))
}
