package export

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const exportVaultFixturePath = "testdata/port/render/export-vault.json"

// Path tokens inside case inputs and recorded errors. {ROOT} is the disposable
// vault root, {OUTSIDE} a sibling directory that holds a secret the vault must
// never export.
const (
	exportVaultRootToken    = "{ROOT}"
	exportVaultOutsideToken = "{OUTSIDE}"
)

type exportVaultFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Oracle        exportVaultOracle `json:"oracle"`
	GeneratedOn   string            `json:"generated_on"`
	Cases         []exportVaultCase `json:"cases"`
	Notes         exportVaultNotes  `json:"notes"`
}

type exportVaultOracle struct {
	Commit    string            `json:"commit"`
	GoVersion string            `json:"go_version"`
	Sources   map[string]string `json:"sources"`
}

type exportVaultNotes struct {
	Unbounded string `json:"unbounded_embed_cycles"`
	Platform  string `json:"platform"`
}

type exportVaultFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Sparse > 0 creates a file of that many bytes without writing them.
	Sparse int64 `json:"sparse,omitempty"`
}

type exportVaultLink struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

type exportVaultView struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
}

// Exactly one of Note (a vault-relative path) or View is set per case.
type exportVaultCase struct {
	ID       string              `json:"id"`
	Files    []exportVaultFile   `json:"files,omitempty"`
	Dirs     []string            `json:"dirs,omitempty"`
	Symlinks []exportVaultLink   `json:"symlinks,omitempty"`
	Note     *string             `json:"note,omitempty"`
	View     *exportVaultView    `json:"view,omitempty"`
	Rows     []map[string]string `json:"rows,omitempty"`
	Format   string              `json:"format"`
	Output   string              `json:"output"`
	Error    string              `json:"error"`
}

// TestPortRenderExportVaultFixture captures export.Note and export.View over
// real disposable vaults: transclusion expansion, embed error texts, path
// confinement and the PDF/HTML/Markdown shapes. Regenerate deliberately with:
//
//	PORT_RENDER_EXPORT_VAULT_GENERATE=1 GOTOOLCHAIN=go1.26.10 go test -count=1 ./internal/export -run '^TestPortRenderExportVaultFixture$'
func TestPortRenderExportVaultFixture(t *testing.T) {
	fixture := buildExportVaultFixture(t)
	encoded, err := encodeExportVaultFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExportVaultFixture(t, encoded, fixture); err != nil {
		t.Fatalf("generated Go oracle fixture failed its own validator: %v", err)
	}
	assertExportVaultMutationControls(t, fixture, encoded)

	path := filepath.Join(exportMarkdownRepoRoot(t), filepath.FromSlash(exportVaultFixturePath))
	if os.Getenv("PORT_RENDER_EXPORT_VAULT_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // test fixture file
			t.Fatalf("write fixture: %v", err)
		}
		t.Logf("GENERATED %s with %d Go %s cases", exportVaultFixturePath, len(fixture.Cases), runtime.Version())
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository-relative fixture path
	if err != nil {
		t.Fatalf("read fixture %s (regenerate deliberately with PORT_RENDER_EXPORT_VAULT_GENERATE=1): %v", path, err)
	}
	if err := validateExportVaultFixture(t, current, fixture); err != nil {
		t.Fatalf("Go oracle fixture is stale; regenerate deliberately: %v", err)
	}
	t.Logf("PASS %s: %d Go %s cases executed", exportVaultFixturePath, len(fixture.Cases), runtime.Version())
}

func buildExportVaultFixture(t *testing.T) exportVaultFixture {
	t.Helper()
	if runtime.Version() != noteHTMLOracleGoVersion {
		t.Fatalf("export vault oracle requires Go %s, running %s", noteHTMLOracleGoVersion, runtime.Version())
	}
	sources := map[string]string{}
	for _, rel := range []string{"internal/export/export.go", "internal/vault/root.go"} {
		raw, err := os.ReadFile(filepath.Join(exportMarkdownRepoRoot(t), filepath.FromSlash(rel))) //nolint:gosec // pinned source file
		if err != nil {
			t.Fatalf("read pinned Go source %s: %v", rel, err)
		}
		sources[rel] = noteHTMLSHA256(string(bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))))
	}
	if sources["internal/export/export.go"] != noteHTMLOracleSourceSHA256 {
		t.Fatalf("Go oracle source export.go changed: got %s, want pinned %s", sources["internal/export/export.go"], noteHTMLOracleSourceSHA256)
	}
	fixture := exportVaultFixture{
		SchemaVersion: 1,
		GeneratedOn:   runtime.GOOS,
		Oracle: exportVaultOracle{
			Commit:    fixtureoracle.Current().Commit,
			GoVersion: runtime.Version(),
			Sources:   sources,
		},
		Cases: exportVaultCorpus(),
		Notes: exportVaultNotes{
			Unbounded: "Go resolveTransclusions has no cycle or depth bound: a note that embeds itself never returns. No fixture case may contain a cycle.",
			Platform:  "error texts carry Unix syscall wording; replay is Unix-only",
		},
	}
	if runtime.GOOS == "windows" {
		return fixture
	}
	for i := range fixture.Cases {
		fixture.Cases[i].Output, fixture.Cases[i].Error = runExportVaultCase(t, fixture.Cases[i])
	}
	return fixture
}

// runExportVaultCase builds a disposable vault for the case, runs the shipped
// export.Note or export.View and normalizes the temp paths in the result.
func runExportVaultCase(t *testing.T, c exportVaultCase) (string, string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	for _, dir := range []string{home, filepath.Join(base, "xdg-config"), filepath.Join(base, "xdg-data"), filepath.Join(base, "xdg-cache")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "xdg-data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "xdg-cache"))
	t.Setenv("TZ", "UTC")
	t.Setenv("LC_ALL", "C")

	root := filepath.Join(base, "vault")
	outside := filepath.Join(base, "outside")
	for _, dir := range append([]string{root, outside}, c.Dirs...) {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // disposable test vault
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.md"), []byte("---\ntitle: Secret\n---\nTOP-SECRET-OUTSIDE\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolve := func(value string) string {
		return strings.ReplaceAll(strings.ReplaceAll(value, exportVaultRootToken, root), exportVaultOutsideToken, outside)
	}
	for _, file := range c.Files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // disposable test vault
			t.Fatal(err)
		}
		if file.Sparse > 0 {
			handle, err := os.Create(path) //nolint:gosec // disposable test vault
			if err != nil {
				t.Fatal(err)
			}
			if err := handle.Truncate(file.Sparse); err != nil {
				t.Fatal(err)
			}
			if err := handle.Close(); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte(file.Content), 0o644); err != nil { //nolint:gosec // disposable test vault
			t.Fatal(err)
		}
	}
	for _, link := range c.Symlinks {
		path := filepath.Join(root, filepath.FromSlash(link.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // disposable test vault
			t.Fatal(err)
		}
		if err := os.Symlink(resolve(link.Target), path); err != nil {
			t.Fatal(err)
		}
	}

	opts := Options{Format: c.Format}
	var out []byte
	var err error
	switch {
	case c.Note != nil && c.View == nil:
		out, err = Note(root, resolve(*c.Note), opts)
	case c.View != nil && c.Note == nil:
		rows := make([]map[string]string, len(c.Rows))
		for i, row := range c.Rows {
			rows[i] = make(map[string]string, len(row))
			for k, v := range row {
				rows[i][k] = resolve(v)
			}
		}
		out, err = View(root, &dbviews.View{Name: c.View.Name, Columns: c.View.Columns}, rows, opts)
	default:
		t.Fatalf("case %q must set exactly one of note or view", c.ID)
	}

	canonicalRoot, evalErr := filepath.EvalSymlinks(root)
	if evalErr != nil {
		t.Fatal(evalErr)
	}
	canonicalOutside, evalErr := filepath.EvalSymlinks(outside)
	if evalErr != nil {
		t.Fatal(evalErr)
	}
	// Canonical spellings first: the lexical root can be a substring of them.
	normalize := func(value string) string {
		for _, pair := range [][2]string{
			{canonicalRoot, exportVaultRootToken}, {root, exportVaultRootToken},
			{canonicalOutside, exportVaultOutsideToken}, {outside, exportVaultOutsideToken},
		} {
			value = strings.ReplaceAll(value, pair[0], pair[1])
		}
		return value
	}
	text := normalize(string(out))
	errText := ""
	if err != nil {
		errText = normalize(err.Error())
	}
	for _, leak := range []string{base, mustEvalSymlinks(t, base)} {
		if strings.Contains(text, leak) || strings.Contains(errText, leak) {
			t.Fatalf("case %q leaks the disposable directory %q: %q %q", c.ID, leak, text, errText)
		}
	}
	if strings.Contains(text, "TOP-SECRET-OUTSIDE") || strings.Contains(errText, "TOP-SECRET-OUTSIDE") {
		t.Fatalf("case %q exported the outside secret", c.ID)
	}
	return text, errText
}

func mustEvalSymlinks(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func encodeExportVaultFixture(fixture exportVaultFixture) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(fixture); err != nil {
		return nil, fmt.Errorf("marshal fixture: %w", err)
	}
	return buffer.Bytes(), nil
}

func validateExportVaultFixture(t *testing.T, encoded []byte, expected exportVaultFixture) error {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var actual exportVaultFixture
	if err := decoder.Decode(&actual); err != nil {
		return fmt.Errorf("decode fixture: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("fixture has trailing JSON data: %v", err)
	}
	if actual.SchemaVersion != 1 || len(actual.Cases) != len(expected.Cases) {
		return fmt.Errorf("schema/case count = %d/%d, want 1/%d", actual.SchemaVersion, len(actual.Cases), len(expected.Cases))
	}
	if actual.Oracle.Commit != expected.Oracle.Commit || actual.Oracle.GoVersion != expected.Oracle.GoVersion {
		return fmt.Errorf("oracle identity %q/%q, want %q/%q", actual.Oracle.Commit, actual.Oracle.GoVersion, expected.Oracle.Commit, expected.Oracle.GoVersion)
	}
	for rel, want := range expected.Oracle.Sources {
		if actual.Oracle.Sources[rel] != want {
			return fmt.Errorf("pinned source %s digest differs from the checked-out production source", rel)
		}
	}
	if runtime.GOOS == "windows" {
		// Error texts carry Unix syscall wording and symlinks need privileges:
		// Windows validates the stored oracle without re-executing it.
		return nil
	}
	for i, c := range actual.Cases {
		want := expected.Cases[i]
		if c.ID != want.ID {
			return fmt.Errorf("case %d id %q, want %q", i, c.ID, want.ID)
		}
		out, errText := runExportVaultCase(t, withoutOutcome(c))
		if c.Output != out || c.Error != errText {
			return fmt.Errorf("case %q output differs from Go", c.ID)
		}
	}
	canonical, err := encodeExportVaultFixture(expected)
	if err != nil {
		return err
	}
	if !bytes.Equal(encoded, canonical) {
		return fmt.Errorf("fixture bytes differ from deterministic Go JSON encoding")
	}
	return nil
}

func withoutOutcome(c exportVaultCase) exportVaultCase {
	c.Output, c.Error = "", ""
	return c
}

// assertExportVaultMutationControls proves the validator rejects drift: a
// changed output, a changed error and a changed input must each be refused.
func assertExportVaultMutationControls(t *testing.T, fixture exportVaultFixture, encoded []byte) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	mutations := map[string]func(*exportVaultFixture){
		"output": func(f *exportVaultFixture) { f.Cases[0].Output += "mutated" },
		"error": func(f *exportVaultFixture) {
			for i := range f.Cases {
				if f.Cases[i].Error != "" {
					f.Cases[i].Error += "mutated"
					return
				}
			}
		},
		"input": func(f *exportVaultFixture) { f.Cases[0].Files[0].Content += "\nmutated" },
	}
	for name, mutate := range mutations {
		var mutated exportVaultFixture
		if err := json.Unmarshal(encoded, &mutated); err != nil {
			t.Fatal(err)
		}
		mutate(&mutated)
		mutatedBytes, err := encodeExportVaultFixture(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateExportVaultFixture(t, mutatedBytes, fixture); err == nil {
			t.Fatalf("%s mutation control was accepted", name)
		}
	}
	t.Log("PASS mutation controls: output, error and input drift rejected")
}

func strPtr(value string) *string { return &value }
