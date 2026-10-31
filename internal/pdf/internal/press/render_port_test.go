package press

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const (
	pressRenderFixturePath = "testdata/port/render/press-render.json"
	pressRenderCanonTool   = "scripts/rust-port/pdfops/pdfcanon.py"
)

// pressRenderFixture records what the real press pipeline does: the strict
// frontmatter contract (Parse + Validate + the meta.json it hands Typst) and
// full renders through the installed Typst binary (typed errors, resolved
// options and the PDF that comes out).
type pressRenderFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Oracle        pressRenderOracle `json:"oracle"`
	Meta          []pressMetaCase   `json:"meta"`
	Render        []pressRenderCase `json:"render"`
}

type pressRenderOracle struct {
	Commit        string `json:"commit"`
	GoVersion     string `json:"go_version"`
	TypstVersion  string `json:"typst_version"`
	CanonTool     string `json:"canon_tool"`
	CanonSHA256   string `json:"canon_tool_sha256"`
	Normalisation string `json:"normalisation"`
}

type pressMetaCase struct {
	ID      string       `json:"id"`
	Source  string       `json:"source"`
	Profile string       `json:"profile"`
	Want    pressMetaOut `json:"want"`
}

// Detail is exact for decode errors; YAML syntax errors carry the yaml.v3
// scanner wording, which the port reproduces only up to the reason.
type pressMetaOut struct {
	ParseError *ContractError `json:"parse_error,omitempty"`
	Syntax     bool           `json:"syntax_error,omitempty"`
	Body       string         `json:"body"`
	Meta       string         `json:"meta_json,omitempty"`
	Resolved   string         `json:"resolved_profile,omitempty"`
	Form       string         `json:"resolved_form,omitempty"`
	Issues     []Issue        `json:"issues,omitempty"`
}

type pressFile struct {
	Path   string `json:"path"`
	Data   string `json:"data_b64,omitempty"`
	LinkTo string `json:"link_to,omitempty"`
	Dir    bool   `json:"dir,omitempty"`
}

type pressRenderReq struct {
	Source           string      `json:"source"`
	Files            []pressFile `json:"files,omitempty"`
	NoSourceDir      bool        `json:"no_source_dir,omitempty"`
	ProfileOverride  string      `json:"profile_override,omitempty"`
	StandardOverride []string    `json:"standard_override,omitempty"`
	Reproducible     *bool       `json:"reproducible,omitempty"`
	DefaultProfile   string      `json:"default_profile,omitempty"`
	ShortDiagnostics bool        `json:"short_diagnostics,omitempty"`
	TypstBin         string      `json:"typst_bin,omitempty"`
	IgnoreFonts      bool        `json:"ignore_system_fonts,omitempty"`
	TimeoutMS        int64       `json:"timeout_ms,omitempty"`
	Preexisting      string      `json:"preexisting_output,omitempty"`
	Overwrite        bool        `json:"overwrite,omitempty"`
}

type pressRenderCase struct {
	ID   string          `json:"id"`
	Req  pressRenderReq  `json:"request"`
	Want pressRenderWant `json:"want"`
}

type pressRenderResult struct {
	Profile       string   `json:"profile"`
	Engine        string   `json:"engine"`
	EngineVersion string   `json:"engine_version"`
	PDFStandard   []string `json:"pdf_standard,omitempty"`
	Reproducible  bool     `json:"reproducible"`
}

// ErrorKind: contract | render | engine-unavailable | overwrite.
type pressRenderWant struct {
	OK        bool               `json:"ok"`
	ErrorKind string             `json:"error_kind,omitempty"`
	ErrorText string             `json:"error_text,omitempty"`
	Contract  *ContractError     `json:"contract,omitempty"`
	Render    *RenderError       `json:"render_error,omitempty"`
	Result    *pressRenderResult `json:"result,omitempty"`
	// PDF is the SHA-256 of the exact bytes for reproducible renders; others
	// carry the pdfcanon description with dates normalised.
	PDFSHA256 string          `json:"pdf_sha256,omitempty"`
	PDFSize   int             `json:"pdf_size,omitempty"`
	Canon     json.RawMessage `json:"canon,omitempty"`
}

func TestPortPressRenderFixture(t *testing.T) {
	generate := os.Getenv("PORT_RENDER_PRESS_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1"
	root := pressPortRoot(t)
	path := filepath.Join(root, filepath.FromSlash(pressRenderFixturePath))
	if !generate && runtime.GOOS == "windows" {
		current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
		if err != nil {
			t.Fatalf("read fixture: %v", err)
		}
		var stored pressRenderFixture
		if err := json.Unmarshal(current, &stored); err != nil {
			t.Fatalf("decode stored oracle: %v", err)
		}
		canonical, err := encodePressRenderFixture(stored)
		if err != nil || !bytes.Equal(current, canonical) {
			t.Fatalf("stored oracle is not canonical: %v", err)
		}
		t.Logf("PASS %s on Windows: stored Go oracle validated, not regenerated (no typst/qpdf)", pressRenderFixturePath)
		return
	}
	for _, tool := range []string{"python3", "qpdf", "typst"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("press render oracle needs %s: %v", tool, err)
		}
	}
	if generate {
		fixture := buildPressRenderFixture(t, root)
		encoded, err := encodePressRenderFixture(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := validatePressRenderFixture(root, encoded); err != nil {
			t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
		}
		// Mutation control: a one-character change to a recorded PDF digest or
		// meta.json must be rejected by the validator.
		var mutated pressRenderFixture
		if err := json.Unmarshal(encoded, &mutated); err != nil {
			t.Fatal(err)
		}
		mutated.Meta[0].Want.Meta += " "
		mb, _ := encodePressRenderFixture(mutated)
		if err := validatePressRenderFixture(root, mb); err == nil || !strings.Contains(err.Error(), "differs from Go") {
			t.Fatalf("meta mutation control = %v, want Go-output rejection", err)
		}
		if err := json.Unmarshal(encoded, &mutated); err != nil {
			t.Fatal(err)
		}
		for i := range mutated.Render {
			if mutated.Render[i].Want.PDFSHA256 != "" {
				mutated.Render[i].Want.PDFSHA256 = strings.Replace(mutated.Render[i].Want.PDFSHA256, mutated.Render[i].Want.PDFSHA256[:1], "x", 1)
				break
			}
		}
		mb, _ = encodePressRenderFixture(mutated)
		if err := validatePressRenderFixture(root, mb); err == nil || !strings.Contains(err.Error(), "differs from Go") {
			t.Fatalf("pdf mutation control = %v, want Go-output rejection", err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("GENERATED %s: %d meta, %d render cases (Go %s, typst %s)", pressRenderFixturePath, len(fixture.Meta), len(fixture.Render), runtime.Version(), fixture.Oracle.TypstVersion)
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_PRESS_GENERATE=1): %v", pressRenderFixturePath, err)
	}
	if err := validatePressRenderFixture(root, current); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	t.Logf("PASS %s", pressRenderFixturePath)
}

func encodePressRenderFixture(f pressRenderFixture) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func pressPortRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatal("repository root not found")
		}
	}
}

func typstVersionString() string {
	out, err := exec.Command("typst", "--version").Output() //nolint:gosec // fixed tool name
	if err != nil {
		return ""
	}
	f := strings.Fields(string(out))
	if len(f) >= 2 {
		return f[1]
	}
	return ""
}

func validatePressRenderFixture(root string, encoded []byte) error {
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.DisallowUnknownFields()
	var actual pressRenderFixture
	if err := dec.Decode(&actual); err != nil {
		return fmt.Errorf("decode fixture: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("fixture has trailing JSON data: %v", err)
	}
	tool, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pressRenderCanonTool))) //nolint:gosec // fixed repository path
	if err != nil {
		return err
	}
	sum := sha256.Sum256(tool)
	if actual.Oracle.CanonSHA256 != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("canon tool digest changed; regenerate")
	}
	if v := typstVersionString(); v != actual.Oracle.TypstVersion {
		return fmt.Errorf("fixture was recorded with typst %s, this host has %s: pin the engine or regenerate", actual.Oracle.TypstVersion, v)
	}
	for _, c := range actual.Meta {
		got := observePressMeta(c.Source, c.Profile)
		a, _ := json.Marshal(c.Want)
		b, _ := json.Marshal(got)
		if !bytes.Equal(a, b) {
			return fmt.Errorf("meta case %q differs from Go", c.ID)
		}
	}
	for _, c := range actual.Render {
		got, err := observePressRender(root, c.Req)
		if err != nil {
			return fmt.Errorf("render case %q: %w", c.ID, err)
		}
		a, _ := json.Marshal(c.Want)
		b, _ := json.Marshal(got)
		if !bytes.Equal(a, b) {
			return fmt.Errorf("render case %q differs from Go:\n  recorded %.400s\n  observed %.400s", c.ID, a, b)
		}
	}
	canonical, err := encodePressRenderFixture(actual)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}

func observePressMeta(source, profile string) pressMetaOut {
	doc, err := Parse([]byte(source))
	if err != nil {
		var ce *ContractError
		if !errors.As(err, &ce) {
			return pressMetaOut{Body: err.Error()}
		}
		return pressMetaOut{ParseError: ce, Syntax: !strings.Contains(ce.Detail, "cannot unmarshal") && !strings.Contains(ce.Detail, "not found in type")}
	}
	out := pressMetaOut{Body: string(doc.Body)}
	if prof, ok := Lookup(profile); ok {
		if doc.Front.Form == "" {
			doc.Front.Form = prof.Form
		}
		doc.Front.Profile = prof.Name
		out.Resolved = prof.Name
		out.Form = doc.Front.Form
		out.Issues = doc.Validate(prof)
	}
	meta, err := json.MarshalIndent(doc.Front, "", "  ")
	if err != nil {
		out.Meta = "ERROR: " + err.Error()
	} else {
		out.Meta = string(meta)
	}
	return out
}

var pressWorkPath = regexp.MustCompile(`(?:/[^\s:"'()]*)?/symprint-[0-9]+`)

func normalisePressText(s string) string { return pressWorkPath.ReplaceAllString(s, "{WORK}") }

var pressDatePatterns = []*regexp.Regexp{
	regexp.MustCompile(`D:\d{14}[Z+\-][0-9'Z]*`),
	regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+\-]\d{2}:\d{2})`),
}

func normalisePressDates(s string) string {
	for _, re := range pressDatePatterns {
		s = re.ReplaceAllString(s, "{DATE}")
	}
	return s
}

func observePressRender(root string, req pressRenderReq) (pressRenderWant, error) {
	tmp, err := os.MkdirTemp("", "press-port-")
	if err != nil {
		return pressRenderWant{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	srcDir := filepath.Join(tmp, "src")
	outside := filepath.Join(tmp, "outside")
	for _, d := range []string{srcDir, outside} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return pressRenderWant{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.png"), pressPNG(), 0o600); err != nil {
		return pressRenderWant{}, err
	}
	for _, f := range req.Files {
		p := filepath.Join(srcDir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return pressRenderWant{}, err
		}
		switch {
		case f.Dir:
			if err := os.MkdirAll(p, 0o700); err != nil {
				return pressRenderWant{}, err
			}
		case f.LinkTo != "":
			target := strings.ReplaceAll(f.LinkTo, "{OUTSIDE}", outside)
			if err := os.Symlink(target, p); err != nil {
				return pressRenderWant{}, err
			}
		default:
			data, err := base64.StdEncoding.DecodeString(f.Data)
			if err != nil {
				return pressRenderWant{}, err
			}
			if err := os.WriteFile(p, data, 0o600); err != nil {
				return pressRenderWant{}, err
			}
		}
	}
	out := filepath.Join(tmp, "out", "result.pdf")
	if req.Preexisting != "" {
		if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
			return pressRenderWant{}, err
		}
		if req.Preexisting == "dir" {
			if err := os.MkdirAll(out, 0o700); err != nil {
				return pressRenderWant{}, err
			}
		} else if err := os.WriteFile(out, []byte(req.Preexisting), 0o600); err != nil {
			return pressRenderWant{}, err
		}
	}
	var want pressRenderWant
	if err := CheckOverwrite(out, req.Overwrite); err != nil {
		want.ErrorKind = "overwrite"
		want.ErrorText = strings.ReplaceAll(err.Error(), out, "{OUT}")
		return want, nil
	}
	r := Request{
		Source:           []byte(req.Source),
		SourceName:       "doc.md",
		OutputPath:       out,
		ProfileOverride:  req.ProfileOverride,
		StandardOverride: req.StandardOverride,
		Reproducible:     req.Reproducible,
		DefaultProfile:   req.DefaultProfile,
		ShortDiagnostics: req.ShortDiagnostics,
		Engine:           EngineConfig{TypstBin: req.TypstBin, IgnoreSystemFonts: req.IgnoreFonts, Timeout: 60 * time.Second},
	}
	if req.TimeoutMS > 0 {
		r.Engine.Timeout = time.Duration(req.TimeoutMS) * time.Millisecond
	}
	if !req.NoSourceDir {
		r.SourceDir = srcDir
	}
	ResetProbeCache()
	res, err := Render(context.Background(), r)
	if err != nil {
		var ce *ContractError
		var re *RenderError
		switch {
		case errors.As(err, &ce):
			want.ErrorKind = "contract"
			want.Contract = ce
		case errors.As(err, &re):
			want.ErrorKind = "render"
			cp := *re
			cp.Err = nil
			cp.Message = normalisePressText(cp.Message)
			cp.Detail = normalisePressText(cp.Detail)
			cp.Hint = normalisePressText(cp.Hint)
			want.Render = &cp
		default:
			return want, fmt.Errorf("untyped error: %w", err)
		}
		want.ErrorText = normalisePressText(strings.ReplaceAll(err.Error(), srcDir, "{SRC}"))
		return want, nil
	}
	want.OK = true
	want.Result = &pressRenderResult{Profile: res.Profile, Engine: res.Engine, EngineVersion: res.EngineVersion, PDFStandard: res.PDFStandard, Reproducible: res.Reproducible}
	if res.OutputPath != out {
		return want, fmt.Errorf("output path %q, want %q", res.OutputPath, out)
	}
	data, err := os.ReadFile(out) //nolint:gosec // disposable temp path
	if err != nil {
		return want, err
	}
	if int64(len(data)) != res.Bytes {
		return want, fmt.Errorf("result bytes %d, file %d", res.Bytes, len(data))
	}
	if res.Reproducible {
		sum := sha256.Sum256(data)
		want.PDFSHA256 = hex.EncodeToString(sum[:])
		want.PDFSize = len(data)
		return want, nil
	}
	canon, err := exec.Command("python3", "-I", filepath.Join(root, filepath.FromSlash(pressRenderCanonTool)), out).Output() //nolint:gosec // fixed tool, disposable paths
	if err != nil {
		return want, fmt.Errorf("pdfcanon: %w", err)
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(normalisePressDates(string(canon))), "", "  "); err != nil {
		return want, err
	}
	want.Canon = json.RawMessage(buf.Bytes())
	return want, nil
}

func pressPNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{uint8(60 * x), uint8(90 * y), uint8(200 - 40*x), 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func pressB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func buildPressRenderFixture(t *testing.T, root string) pressRenderFixture {
	t.Helper()
	if runtime.Version() != "go1.26.10" {
		t.Fatalf("press render oracle requires Go go1.26.10, running %s", runtime.Version())
	}
	tool, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pressRenderCanonTool))) //nolint:gosec // fixed repository path
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(tool)
	f := pressRenderFixture{
		SchemaVersion: 1,
		Oracle: pressRenderOracle{
			Commit:       fixtureoracle.Current().Commit,
			GoVersion:    runtime.Version(),
			TypstVersion: typstVersionString(),
			CanonTool:    pressRenderCanonTool,
			CanonSHA256:  hex.EncodeToString(sum[:]),
			Normalisation: "reproducible renders: SHA-256 of the exact PDF bytes (SOURCE_DATE_EPOCH=0); others: pdfcanon.py description with " +
				"ISO/PDF dates replaced by {DATE}; engine diagnostics: work dir paths replaced by {WORK}, source dir by {SRC}",
		},
	}
	for _, c := range pressMetaCorpus() {
		f.Meta = append(f.Meta, pressMetaCase{ID: c.id, Source: c.source, Profile: c.profile, Want: observePressMeta(c.source, c.profile)})
	}
	for _, c := range pressRenderCorpus() {
		want, err := observePressRender(root, c.req)
		if err != nil {
			t.Fatalf("render case %q: %v", c.id, err)
		}
		f.Render = append(f.Render, pressRenderCase{ID: c.id, Req: c.req, Want: want})
	}
	return f
}

type pressMetaInput struct{ id, source, profile string }
type pressRenderInput struct {
	id  string
	req pressRenderReq
}

func pressMetaCorpus() []pressMetaInput {
	var out []pressMetaInput
	add := func(id, profile, source string) { out = append(out, pressMetaInput{id, source, profile}) }
	add("empty", "report", "")
	add("body-only", "report", "# Title\n\nbody\n")
	add("body-only-crlf", "report", "# Title\r\n\r\nbody\r\n")
	add("front-only", "report", "---\ntitle: T\n---\n")
	add("crlf-front", "report", "---\r\ntitle: T\r\nauthor: A\r\n---\r\n\r\nbody\r\n")
	add("dots-terminator", "report", "---\ntitle: T\n...\n\nbody\n")
	add("unclosed-front", "report", "---\ntitle: T\n\nbody\n")
	add("empty-front", "report", "---\n---\nbody\n")
	add("comment-only-front", "report", "---\n# nothing\n---\nbody\n")
	add("body-blank-trim", "report", "---\ntitle: T\n---\n\n\n\nbody\n")
	add("body-dashes", "report", "---\ntitle: T\n---\nbody\n---\nmore\n")
	add("all-fields-report", "report", "---\nprofile: report\nlang: de\ntitle: Titel\nsubtitle: Sub\nauthor: [A, B]\ndate: 2026-07-16\nkeywords: [x, y]\ntitlepage: true\ntoc: false\ntoc-depth: 3\nheader-left: HL\nheader-right: HR\nfooter-left: FL\nfooter-right: FR\n---\nbody\n")
	add("scalar-author-keywords", "report", "---\ntitle: T\nauthor: Jane\nkeywords: one\n---\n")
	add("empty-scalar-author", "report", "---\ntitle: T\nauthor: \"\"\nkeywords:\n---\n")
	add("null-values", "report", "---\ntitle: ~\nauthor: null\ndate: null\n---\n")
	add("number-into-string", "report", "---\ntitle: 2026\nsubtitle: 3.50\nbetreff: 0x1F\nduration: 1e3\nlocation: true\n---\n")
	add("bool-into-string", "report", "---\ntitle: yes\nsubtitle: Null\n---\n")
	add("number-list-elements", "report", "---\nauthor: [1, 2.5, true]\n---\n")
	add("yaml11-bools", "report", "---\ntoc: yes\ntitlepage: n\n---\n")
	add("bool-bad", "report", "---\ntoc: maybe\n---\n")
	add("int-bad-toc-depth", "report", "---\ntoc-depth: deep\n---\n")
	add("int-float-toc-depth", "report", "---\ntoc-depth: 2.5\n---\n")
	add("int-negative-toc-depth", "report", "---\ntoc-depth: -1\n---\n")
	add("int-zero-toc-depth", "report", "---\ntoc-depth: 0\n---\n")
	add("int-hex-toc-depth", "report", "---\ntoc-depth: 0x3\n---\n")
	add("unknown-key", "report", "---\ntitel: T\n---\n")
	add("unknown-key-after-valid", "report", "---\ntitle: T\nfoo: 1\nbar: 2\n---\n")
	add("title-map", "report", "---\ntitle:\n  a: b\n---\n")
	add("title-seq", "report", "---\ntitle: [a, b]\n---\n")
	add("title-seq-block", "report", "---\ntitle:\n  - a\n---\n")
	add("author-map", "report", "---\nauthor:\n  name: A\n---\n")
	add("author-nested-seq", "report", "---\nauthor: [[a]]\n---\n")
	add("recipient-string", "brief", "---\nrecipient: Max\n---\n")
	add("recipient-ok", "brief", "---\nrecipient:\n  name: Max Mustermann\n  address:\n    - Str 1\n    - 12345 Ort\n---\n")
	add("recipient-address-scalar", "brief", "---\nrecipient:\n  name: Max\n  address: Str 1\n---\n")
	add("recipient-unknown-field", "brief", "---\nrecipient:\n  name: Max\n  phone: 1\n---\n")
	add("recipient-no-name", "brief", "---\nrecipient:\n  address: [a]\n---\n")
	add("sender-recipient-betreff", "behoerde", "---\nlang: de\ntitle: T\nform: A\nsender:\n  name: S\n  address: [S1]\nrecipient:\n  name: R\nbetreff: Betreff\ninfoblock:\n  Zeichen: 42\n  Datum: 2026-07-16\n---\n")
	add("infoblock-nested", "brief", "---\nrecipient: {name: R}\ninfoblock:\n  a:\n    b: 1\n---\n")
	add("infoblock-seq", "brief", "---\nrecipient: {name: R}\ninfoblock: [a]\n---\n")
	add("date-din-dotted", "report", "---\ntitle: T\ndate: 16.07.2026\n---\n")
	add("date-iso", "report", "---\ntitle: T\ndate: 2026-07-16\n---\n")
	add("date-bad-no-zero", "report", "---\ntitle: T\ndate: 1.7.2026\n---\n")
	add("date-text", "report", "---\ntitle: T\ndate: heute\n---\n")
	add("date-quoted", "report", "---\ntitle: T\ndate: \"2026-07-16\"\n---\n")
	add("date-timestamp", "report", "---\ntitle: T\ndate: 2026-07-16T10:00:00Z\n---\n")
	add("form-bad", "brief", "---\nrecipient: {name: R}\nform: C\n---\n")
	add("form-lower", "brief", "---\nrecipient: {name: R}\nform: a\n---\n")
	add("form-a-report", "report", "---\ntitle: T\nform: A\n---\n")
	add("behoerde-missing-all", "behoerde", "---\nprofile: behoerde\n---\nbody\n")
	add("behoerde-ok", "behoerde", "---\nlang: de\ntitle: T\nrecipient: {name: R}\n---\n")
	add("behoerde-lang-empty", "behoerde", "---\nlang: \"\"\ntitle: T\nrecipient: {name: R}\n---\n")
	add("meeting-missing", "meeting", "---\ntitle: M\n---\n")
	add("meeting-ok", "meeting", "---\ntitle: M\nlang: de\ndate: 2026-07-16\nmeeting_id: SYM-42\nparticipants: [Daniel, Anti]\nduration: 1h 30m\nlocation: Online\n---\n")
	add("meeting-id-number", "meeting", "---\ntitle: M\nlang: de\ndate: 2026-07-16\nmeeting_id: 42\n---\n")
	add("rechnung-missing", "rechnung", "---\nprofile: rechnung\n---\n")
	add("rechnung-data", "rechnung", "---\nrecipient: {name: R}\ndata:\n  number: \"2026-0042\"\n  currency: EUR\n  items:\n    - {description: A, qty: 2, unit_price: 50.0}\n    - {description: B, qty: 1, unit_price: 19.99}\n---\n")
	add("rechnung-data-empty-map", "rechnung", "---\nrecipient: {name: R}\ndata: {}\n---\n")
	add("data-scalar-kinds", "rechnung", "---\nrecipient: {name: R}\ndata:\n  i: 7\n  neg: -3\n  f: 0.1\n  big: 12345678901234567890\n  e: 1.5e300\n  small: 1e-7\n  whole: 3.0\n  hex: 0xFF\n  oct: 0o17\n  t: true\n  n: null\n  s: \"a<b>&c\"\n  u: \"é ü 日本\"\n  ts: 2026-07-16\n  nl: \"a\\nb\"\n  tab: \"a\\tb\"\n  esc: \"q\\\"uote\\\\\"\n---\n")
	add("data-inf-nan", "rechnung", "---\nrecipient: {name: R}\ndata:\n  x: .inf\n---\n")
	add("data-nan", "rechnung", "---\nrecipient: {name: R}\ndata:\n  x: .nan\n---\n")
	add("data-int-keys", "rechnung", "---\nrecipient: {name: R}\ndata:\n  1: a\n  2: b\n---\n")
	add("data-nested-int-keys", "rechnung", "---\nrecipient: {name: R}\ndata:\n  m:\n    1: a\n---\n")
	add("data-list-of-lists", "rechnung", "---\nrecipient: {name: R}\ndata:\n  rows: [[1, 2], [3, [4, 5]], []]\n  empty: []\n---\n")
	add("data-scalar", "rechnung", "---\nrecipient: {name: R}\ndata: 5\n---\n")
	add("data-seq", "rechnung", "---\nrecipient: {name: R}\ndata: [1]\n---\n")
	add("data-anchor-alias", "rechnung", "---\nrecipient: {name: R}\ndata:\n  a: &x {k: v}\n  b: *x\n---\n")
	add("data-key-order", "rechnung", "---\nrecipient: {name: R}\ndata:\n  z: 1\n  a: 2\n  m: {y: 1, b: 2}\n---\n")
	add("data-html-escapes", "rechnung", "---\nrecipient: {name: R}\ndata:\n  s: \"</script>\\u2028\\u2029\"\n---\n")
	add("pdf-options", "report", "---\ntitle: T\npdf:\n  standard: [a-2b, ua-1]\n  reproducible: true\n---\n")
	add("pdf-standard-scalar", "report", "---\ntitle: T\npdf:\n  standard: a-3b\n---\n")
	add("pdf-repro-bad", "report", "---\ntitle: T\npdf:\n  reproducible: sometimes\n---\n")
	add("pdf-unknown", "report", "---\ntitle: T\npdf:\n  level: 1\n---\n")
	add("pdf-null", "report", "---\ntitle: T\npdf:\n---\n")
	add("profile-in-front", "report", "---\nprofile: brief\ntitle: T\nrecipient: {name: R}\n---\n")
	add("duplicate-key", "report", "---\ntitle: A\ntitle: B\n---\n")
	add("syntax-bad-indent", "report", "---\ntitle: T\n  author: x\n---\n")
	add("syntax-unclosed-flow", "report", "---\ntitle: [a, b\n---\n")
	add("syntax-tab", "report", "---\ntitle:\tT\nauthor:\n\t- a\n---\n")
	add("syntax-colon", "report", "---\ntitle: a: b\n---\n")
	add("syntax-top-level-seq", "report", "---\n- a\n- b\n---\n")
	add("syntax-top-level-scalar", "report", "---\njust text\n---\n")
	add("multi-doc", "report", "---\ntitle: A\n---\nbody\n")
	add("unicode-values", "report", "---\ntitle: \"Größe – 日本語 🙂\"\nauthor: [\"Zoë\"]\n---\n")
	add("long-lines-multiline", "report", "---\ntitle: >\n  folded\n  text\nsubtitle: |\n  literal\n  text\n---\n")
	add("quoted-escapes", "report", "---\ntitle: \"tab\\there \\u00e9\"\nsubtitle: 'it''s'\n---\n")
	add("unknown-profile-name", "nope", "---\ntitle: T\n---\n")
	add("tags", "report", "---\ntitle: !!str 12\nsubtitle: !!int 5\n---\n")
	add("tag-bad", "report", "---\ntitle: !!int abc\n---\n")
	add("binary", "report", "---\ntitle: !!binary aGk=\n---\n")
	add("merge-key", "rechnung", "---\nrecipient: {name: R}\ndata:\n  base: &b {a: 1}\n  other:\n    <<: *b\n    c: 2\n---\n")
	return out
}

func pressRender(id string, r pressRenderReq) pressRenderInput { return pressRenderInput{id, r} }

func pressRenderCorpus() []pressRenderInput {
	tr, fa := true, false
	png := pressB64(pressPNG())
	longBody := "# Überschrift\n\nErster Absatz mit **fett**, *kursiv*, `code` und einem [Link](https://example.org).\n\n" +
		"## Liste\n\n- Eins\n- Zwei\n  - Zwei A\n1. Erster\n2. Zweiter\n\n> Zitat\n\n```go\nfmt.Println(\"hi\")\n```\n\n| A | B |\n|---|---|\n| 1 | 2 |\n| 3 | 4 |\n\n---\n\nSchluss mit Umlauten äöüß und Gedankenstrich –.\n"
	letter := "---\nprofile: brief\nlang: de\ntitle: Brief\nsender:\n  name: Absender GmbH\n  address: [Weg 1, 10115 Berlin]\nrecipient:\n  name: Max Mustermann\n  address: [Str 1, 12345 Ort]\nbetreff: Betreff der Sache\ndate: 2026-07-16\ninfoblock:\n  Zeichen: AB-42\n  Telefon: 030 123\n---\n\nSehr geehrter Herr Mustermann,\n\nhiermit teilen wir mit.\n\nMit freundlichen Grüßen\n"
	var out []pressRenderInput
	add := func(id string, r pressRenderReq) { out = append(out, pressRender(id, r)) }
	rep := func(front, body string) string { return "---\n" + front + "---\n\n" + body }

	add("report-basic-repro", pressRenderReq{Source: rep("profile: report\ntitle: Bericht\nlang: de\n", longBody), Reproducible: &tr})
	add("report-basic-nonrepro", pressRenderReq{Source: rep("profile: report\ntitle: Bericht\nlang: de\n", "Kurzer Text.\n"), Reproducible: &fa})
	add("report-default-repro-by-config", pressRenderReq{Source: rep("title: Nur Titel\n", "Text\n"), DefaultProfile: "report", Reproducible: &tr})
	add("report-toc-titlepage", pressRenderReq{Source: rep("profile: report\ntitle: Langer Bericht\nsubtitle: Untertitel\nauthor: [Anna, Bernd]\ndate: 2026-07-16\nkeywords: [a, b]\ntitlepage: true\ntoc: true\ntoc-depth: 2\nheader-left: HL\nheader-right: HR\nfooter-left: FL\nfooter-right: FR\nlang: de\n", "# Eins\n\n## Eins A\n\n### Eins A i\n\n# Zwei\n\ntext\n"), Reproducible: &tr})
	add("report-math", pressRenderReq{Source: rep("profile: report\ntitle: Mathe\n", "Inline $a^2 + b^2 = c^2$ und Block:\n\n$$\\frac{a}{b} \\sum_{i=0}^n x_i$$\n"), Reproducible: &tr})
	add("report-unicode-body", pressRenderReq{Source: rep("profile: report\ntitle: Unicode\n", "Größe – „Zitat“ … ñ ç ø 日本語 🙂\n"), Reproducible: &tr})
	add("report-no-frontmatter-default", pressRenderReq{Source: "# Nur Text\n\nohne Frontmatter\n", DefaultProfile: "report", Reproducible: &tr})
	add("report-crlf", pressRenderReq{Source: "---\r\nprofile: report\r\ntitle: CRLF\r\n---\r\n\r\n# Zeile\r\n\r\nText\r\n", Reproducible: &tr})
	add("report-image-png", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![Alt](img/logo.png)\n\ntext\n"), Files: []pressFile{{Path: "img/logo.png", Data: png}}, Reproducible: &tr})
	add("report-image-title-and-angle", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![Alt](<img/logo.png> \"Titel\")\n\n![B](img/../img/logo.png)\n"), Files: []pressFile{{Path: "img/logo.png", Data: png}}, Reproducible: &tr})
	add("report-image-same-twice", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](logo.png)\n\n![B](logo.png)\n"), Files: []pressFile{{Path: "logo.png", Data: png}}, Reproducible: &tr})
	add("report-image-symlink-inside", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](link.png)\n"), Files: []pressFile{{Path: "real/logo.png", Data: png}, {Path: "link.png", LinkTo: "real/logo.png"}}, Reproducible: &tr})
	add("report-image-remote-ref", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](https://example.invalid/a.png)\n"), Reproducible: &tr})
	add("report-image-data-ref", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](data:image/png;base64,AAAA)\n"), Reproducible: &tr})
	add("report-image-missing", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](nope.png)\n"), Reproducible: &tr})
	add("report-image-absolute", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](/etc/hosts)\n"), Reproducible: &tr})
	add("report-image-traversal", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](../outside/secret.png)\n"), Reproducible: &tr})
	add("report-image-traversal-dotdot-only", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](..)\n"), Reproducible: &tr})
	add("report-image-symlink-escape", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](esc.png)\n"), Files: []pressFile{{Path: "esc.png", LinkTo: "{OUTSIDE}/secret.png"}}, Reproducible: &tr})
	add("report-image-directory", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](imgdir)\n"), Files: []pressFile{{Path: "imgdir", Dir: true}}, Reproducible: &tr})
	add("report-image-no-source-dir", pressRenderReq{Source: rep("profile: report\ntitle: Bild\n", "![A](logo.png)\n"), NoSourceDir: true, Reproducible: &tr})
	add("report-image-no-source-dir-remote-only", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "text without images\n"), NoSourceDir: true, Reproducible: &tr})
	add("report-image-reference-style-unresolved", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "![A][ref]\n\n[ref]: logo.png\n"), Files: []pressFile{{Path: "logo.png", Data: png}}, Reproducible: &tr})
	add("report-standard-a2b-override", pressRenderReq{Source: rep("profile: report\ntitle: A\nlang: de\n", "Text\n"), StandardOverride: []string{"a-2b"}, Reproducible: &tr})
	add("report-standard-a3a-ua1-override", pressRenderReq{Source: rep("profile: report\ntitle: A\nlang: de\n", "Text\n"), StandardOverride: []string{"a-3a", "ua-1"}, Reproducible: &tr})
	add("report-standard-front", pressRenderReq{Source: rep("profile: report\ntitle: A\nlang: de\npdf:\n  standard: [a-2b]\n  reproducible: true\n", "Text\n")})
	add("report-standard-override-beats-front", pressRenderReq{Source: rep("profile: report\ntitle: A\npdf:\n  standard: [a-1b]\n", "Text\n"), StandardOverride: []string{"a-2b"}, Reproducible: &tr})
	add("report-standard-bogus", pressRenderReq{Source: rep("profile: report\ntitle: A\n", "Text\n"), StandardOverride: []string{"bogus"}, Reproducible: &tr})
	add("report-standard-bogus-short", pressRenderReq{Source: rep("profile: report\ntitle: A\n", "Text\n"), StandardOverride: []string{"bogus"}, Reproducible: &tr, ShortDiagnostics: true})
	add("report-standard-conflict", pressRenderReq{Source: rep("profile: report\ntitle: A\n", "Text\n"), StandardOverride: []string{"a-1b", "a-2a"}, Reproducible: &tr})
	add("report-repro-front-true", pressRenderReq{Source: rep("profile: report\ntitle: A\npdf:\n  reproducible: true\n", "Text\n")})
	add("report-repro-override-false-beats-front", pressRenderReq{Source: rep("profile: report\ntitle: A\npdf:\n  reproducible: true\n", "Text\n"), Reproducible: &fa})
	add("report-ignore-system-fonts", pressRenderReq{Source: rep("profile: report\ntitle: A\n", "Text\n"), IgnoreFonts: true, Reproducible: &tr})
	add("override-profile-beats-front", pressRenderReq{Source: rep("profile: report\ntitle: T\nrecipient: {name: R}\n", "Hallo\n"), ProfileOverride: "brief", Reproducible: &tr})
	add("override-profile-unknown", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Hallo\n"), ProfileOverride: "nope"})
	add("front-profile-unknown", pressRenderReq{Source: rep("profile: nope\ntitle: T\n", "Hallo\n")})
	add("default-profile-unknown", pressRenderReq{Source: rep("title: T\n", "Hallo\n"), DefaultProfile: "nope"})
	add("no-profile-at-all", pressRenderReq{Source: rep("title: T\n", "Hallo\n")})
	add("profile-name-case-sensitive", pressRenderReq{Source: rep("profile: Report\ntitle: T\n", "Hallo\n")})
	add("brief-form-b", pressRenderReq{Source: letter, Reproducible: &tr})
	add("brief-form-a", pressRenderReq{Source: strings.Replace(letter, "date:", "form: A\ndate:", 1), Reproducible: &tr})
	add("brief-no-sender", pressRenderReq{Source: rep("profile: brief\nrecipient:\n  name: R\n  address: [Str 1]\n", "Text\n"), Reproducible: &tr})
	add("behoerde-full", pressRenderReq{Source: strings.Replace(letter, "profile: brief", "profile: behoerde", 1), Reproducible: &tr})
	add("behoerde-nonrepro", pressRenderReq{Source: rep("profile: behoerde\nlang: de\ntitle: T\nrecipient: {name: R}\n", "Text\n"), Reproducible: &fa})
	add("behoerde-missing-lang", pressRenderReq{Source: rep("profile: behoerde\ntitle: T\nrecipient: {name: R}\n", "Text\n")})
	add("behoerde-missing-all", pressRenderReq{Source: rep("profile: behoerde\n", "Text\n")})
	add("behoerde-bad-date-and-form", pressRenderReq{Source: rep("profile: behoerde\ntitle: T\nlang: de\nrecipient: {name: R}\ndate: 1.2.3\nform: Z\n", "Text\n")})
	add("behoerde-image-no-alt-ua1", pressRenderReq{Source: strings.Replace(letter, "profile: brief", "profile: behoerde", 1) + "\n![](logo.png)\n", Files: []pressFile{{Path: "logo.png", Data: png}}, Reproducible: &tr})
	add("behoerde-image-alt-ua1", pressRenderReq{Source: strings.Replace(letter, "profile: brief", "profile: behoerde", 1) + "\n![Logo der Firma](logo.png)\n", Files: []pressFile{{Path: "logo.png", Data: png}}, Reproducible: &tr})
	add("behoerde-math-ua1", pressRenderReq{Source: strings.Replace(letter, "profile: brief", "profile: behoerde", 1) + "\nFormel $x^2$\n", Reproducible: &tr})
	add("behoerde-standard-override-none-needed", pressRenderReq{Source: strings.Replace(letter, "profile: brief", "profile: behoerde", 1), StandardOverride: []string{"a-2b"}, Reproducible: &tr})
	add("meeting-full", pressRenderReq{Source: "---\nprofile: meeting\ntitle: Projekt-Meeting\nlang: de\ndate: 2026-07-16\nmeeting_id: SYM-42\nparticipants: [Daniel, Anti Gravity]\nduration: 1h 30m\nlocation: Online\n---\n# Summary\n\n- Punkt\n\n# Decisions\n\n1. Ja\n\n# Action Items\n\n- [ ] Aufgabe\n\n# Notes\n\nNotiz\n\n# Transcript\n\n**Daniel:** Hallo\n", Reproducible: &tr})
	add("meeting-missing-date", pressRenderReq{Source: rep("profile: meeting\ntitle: M\nlang: de\n", "Text\n")})
	add("meeting-nonrepro", pressRenderReq{Source: rep("profile: meeting\ntitle: M\nlang: de\ndate: 2026-07-16\n", "Text\n"), Reproducible: &fa})
	add("rechnung-items", pressRenderReq{Source: "---\nprofile: rechnung\nrecipient:\n  name: Kunde AG\n  address: [Weg 2, 10115 Berlin]\ndata:\n  number: \"2026-0042\"\n  currency: EUR\n  items:\n    - {description: Leistung A, qty: 2, unit_price: 50.0}\n    - {description: Leistung B, qty: 1, unit_price: 19.99}\n---\n\nZahlbar in 14 Tagen.\n", Reproducible: &tr})
	add("rechnung-missing-data", pressRenderReq{Source: rep("profile: rechnung\nrecipient: {name: R}\n", "Text\n")})
	add("rechnung-template-error-long", pressRenderReq{Source: "---\nprofile: rechnung\nrecipient: {name: R}\ndata:\n  items:\n    - {description: A, qty: x, unit_price: y}\n---\n\nText\n", Reproducible: &tr})
	add("rechnung-template-error-short", pressRenderReq{Source: "---\nprofile: rechnung\nrecipient: {name: R}\ndata:\n  items:\n    - {description: A, qty: x, unit_price: y}\n---\n\nText\n", Reproducible: &tr, ShortDiagnostics: true})
	add("contract-unknown-key", pressRenderReq{Source: rep("profile: report\ntitel: x\n", "Text\n")})
	add("contract-title-type", pressRenderReq{Source: rep("profile: report\ntitle: [a]\n", "Text\n")})
	add("contract-yaml-syntax", pressRenderReq{Source: rep("profile: report\ntitle: [a\n", "Text\n")})
	add("contract-lang-invalid-for-typst", pressRenderReq{Source: rep("profile: report\ntitle: T\nlang: not a language!\n", "Text\n"), Reproducible: &tr})
	add("compile-timeout-1ms", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Text\n"), TimeoutMS: 1, Reproducible: &tr})
	add("engine-missing", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Text\n"), TypstBin: "/nonexistent/dir/typst-missing"})
	add("contract-checked-before-engine", pressRenderReq{Source: rep("profile: behoerde\n", "Text\n"), TypstBin: "/nonexistent/dir/typst-missing"})
	add("overwrite-existing-pdf", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Text\n"), Preexisting: "%PDF-1.4 old", Reproducible: &tr})
	add("overwrite-existing-text-refused", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Text\n"), Preexisting: "not a pdf", Reproducible: &tr})
	add("overwrite-existing-text-forced", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Text\n"), Preexisting: "not a pdf", Overwrite: true, Reproducible: &tr})
	add("overwrite-short-file-refused", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Text\n"), Preexisting: "%PD", Reproducible: &tr})
	add("overwrite-directory", pressRenderReq{Source: rep("profile: report\ntitle: T\n", "Text\n"), Preexisting: "dir", Overwrite: true, Reproducible: &tr})
	return out
}
