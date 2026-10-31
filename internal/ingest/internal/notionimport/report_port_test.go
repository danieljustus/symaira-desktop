package notionimport

// Go oracle for the Notion migration report writer: BuildMigrationReport and
// WriteMigrationReport with fixed timestamps (UTC, sub-second, non-UTC offset,
// zero), HTML-escaped warnings, nil and populated results. The exact bytes of
// each written file are the expectation.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type notionReportCase struct {
	ID      string       `json:"id"`
	Stats   reportInput  `json:"stats"`
	DryRun  bool         `json:"dry_run"`
	Written string       `json:"written"`
	Error   string       `json:"error"`
	Results []PageResult `json:"-"`
}

type reportInput struct {
	Imported   int          `json:"imported"`
	Skipped    int          `json:"skipped"`
	Failed     int          `json:"failed"`
	Total      int          `json:"total"`
	Warnings   []string     `json:"warnings"`
	RunID      string       `json:"run_id"`
	Source     string       `json:"source"`
	Mode       string       `json:"mode"`
	StartedAt  string       `json:"started_at"`
	FinishedAt string       `json:"finished_at"`
	Results    []PageResult `json:"results"`
}

func TestPortNotionReportContract(t *testing.T) {
	utc := func(s string) string { return s }
	inputs := []struct {
		id     string
		in     reportInput
		dryRun bool
	}{
		{"empty-zero-times", reportInput{RunID: "", Source: "", Mode: ""}, false},
		{"nil-results-dry-run", reportInput{RunID: "notion-20260102T030405.000000000Z", Source: "notion", Mode: "dry-run", StartedAt: utc("2026-01-02T03:04:05Z"), FinishedAt: utc("2026-01-02T03:04:05Z")}, true},
		{"mixed-results", reportInput{Imported: 2, Skipped: 1, Failed: 1, Total: 4, RunID: "notion-20260102T030405.123456789Z", Source: "notion", Mode: "import",
			StartedAt: utc("2026-01-02T03:04:05.123456789Z"), FinishedAt: utc("2026-01-02T03:04:07.623456789Z"),
			Warnings: []string{"plain warning", "html <b>&amp;</b> \u2028 line", "unicode ünï ✓ \"quoted\" \\ back\ttab"},
			Results: []PageResult{
				{SourcePath: "a/Page One abc.md", VaultPath: "/v/a.md", SHA256: "aa", Status: "imported"},
				{SourcePath: "b.md", Status: "skipped", Reason: "already imported"},
				{SourcePath: "c <x>.md", Status: "failed", Error: "read: boom & <bang>"},
				{SourcePath: "d.csv", VaultPath: "/v/d.md", Status: "would-import"},
			}}, false},
		{"offset-timezone", reportInput{Total: 1, Imported: 1, RunID: "r", Source: "notion", Mode: "import", StartedAt: utc("2026-06-30T23:59:59+02:00"), FinishedAt: utc("2026-07-01T00:00:00.5+02:00"),
			Results: []PageResult{{SourcePath: "e.md", Status: "imported"}}}, false},
		{"long-duration-and-tiny-fraction", reportInput{RunID: "r", Source: "notion", Mode: "import", StartedAt: utc("2026-01-01T00:00:00Z"), FinishedAt: utc("2026-01-03T03:46:40.000000001Z")}, false},
		{"finished-before-started", reportInput{RunID: "r", Source: "notion", Mode: "import", StartedAt: utc("2026-01-01T00:00:10Z"), FinishedAt: utc("2026-01-01T00:00:00Z")}, true},
	}
	dir := t.TempDir()
	var cases []notionReportCase
	for _, in := range inputs {
		parse := func(s string) time.Time {
			if s == "" {
				return time.Time{}
			}
			ts, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				t.Fatal(err)
			}
			return ts
		}
		st := &Stats{Imported: in.in.Imported, Skipped: in.in.Skipped, Failed: in.in.Failed, Total: in.in.Total, Warnings: in.in.Warnings, RunID: in.in.RunID,
			Source: in.in.Source, Mode: in.in.Mode, StartedAt: parse(in.in.StartedAt), FinishedAt: parse(in.in.FinishedAt), Results: in.in.Results}
		path := filepath.Join(dir, in.id+".json")
		c := notionReportCase{ID: in.id, Stats: in.in, DryRun: in.dryRun}
		if err := WriteMigrationReport(path, st.BuildMigrationReport(in.dryRun)); err != nil {
			c.Error = err.Error()
		} else if b, err := os.ReadFile(path); err == nil { //nolint:gosec // test scratch
			c.Written = string(b)
			info, _ := os.Stat(path)
			if info.Mode().Perm() != 0o600 {
				t.Fatalf("report mode %v", info.Mode().Perm())
			}
		}
		cases = append(cases, c)
	}
	// Unwritable destination: the exact wrapped error text.
	bad := notionReportCase{ID: "unwritable-directory", Stats: reportInput{RunID: "r"}}
	if err := WriteMigrationReport(filepath.Join(dir, "missing-dir", "x.json"), (&Stats{RunID: "r"}).BuildMigrationReport(false)); err != nil {
		bad.Error = err.Error()
	}
	cases = append(cases, bad)

	hashes := map[string]string{}
	_, file, _, _ := runtime.Caller(0)
	src := filepath.Dir(file)
	for _, f := range []string{"report.go", "report_port_test.go"} {
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
		"cases":          cases,
		"normalizations": []string{"the unwritable-directory error replaces the scratch directory by $DIR"},
	}, "", " ")
	if e != nil {
		t.Fatal(e)
	}
	content = bytes.ReplaceAll(content, []byte(filepath.ToSlash(dir)), []byte("$DIR"))
	content = append(content, '\n')
	out := filepath.Join(src, "../../../../testdata/port/ingest/notion-report.json")
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
		t.Fatal("Notion report fixture drift; regenerate deliberately")
	}
}
