package ingest

// Freezes the real facade rule operations (Rules, AddRule, TestRules,
// DryRunRule) over a real store and real note files. The pipeline's own
// classification is frozen by TestPortIngestClassifyPipelineContract in the
// engine package.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/store"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type classRule struct {
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
}

type classNote struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	// Mode "file" writes Content; "missing" records a path that does not exist;
	// "dir" records a directory as the note path.
	Mode string `json:"mode"`
}

func TestPortIngestClassificationContract(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	vault := filepath.Join(root, "vault")
	if err := os.MkdirAll(vault, 0o700); err != nil {
		t.Fatal(err)
	}
	opts := Options{Vault: vault, Archive: filepath.Join(root, "archive"), DBPath: filepath.Join(root, "db", "ingest.sqlite")}
	ctx := context.Background()
	norm := func(s string) string { return strings.ReplaceAll(s, root, "<ROOT>") }

	rules := []classRule{
		{"invoice", "category", "Finance"},
		{"RECHNUNG", "category", "Ignored second category"},
		{"invoice", "tag", "billing"},
		{"Rechnung", "tag", "billing"},
		{"rechnung", "tag", "de"},
		{"acme", "correspondent", "ACME GmbH"},
		{"ACME", "correspondent", "Second ACME"},
		{"contract", "document_type", "Contract"},
		{"straße", "tag", "ß-tag"},
		{"İSTANBUL", "tag", "dotted-capital-I"},
		{"σίσυφος", "tag", "sigma"},
		{"kelvin k", "tag", "kelvin"},
		{"ǅ", "tag", "titlecase"},
		{"日本語", "category", "Japanese"},
		{" ", "tag", "space-only-pattern"},
	}
	type step struct {
		Op      string `json:"op"`
		Text    string `json:"text,omitempty"`
		Pattern string `json:"pattern,omitempty"`
		Kind    string `json:"kind,omitempty"`
		Value   string `json:"value,omitempty"`
		Result  any    `json:"result"`
		Error   string `json:"error"`
	}
	steps := []step{}
	record := func(s step, result any, err error) {
		s.Result = result
		if err != nil {
			s.Error = norm(err.Error())
		}
		steps = append(steps, s)
	}
	list := func() {
		r, err := Rules(ctx, opts)
		for i := range r {
			r[i].CreatedAt = "<CREATED_AT>"
		}
		record(step{Op: "rules"}, r, err)
	}
	list()
	for _, r := range rules {
		added, err := AddRule(ctx, opts, r.Pattern, r.Kind, r.Value)
		if added != nil {
			added.CreatedAt = "<CREATED_AT>"
		}
		record(step{Op: "add", Pattern: r.Pattern, Kind: r.Kind, Value: r.Value}, added, err)
	}
	for _, bad := range []classRule{{"", "tag", "v"}, {"p", "bogus", "v"}, {"p", "tag", ""}} {
		added, err := AddRule(ctx, opts, bad.Pattern, bad.Kind, bad.Value)
		record(step{Op: "add", Pattern: bad.Pattern, Kind: bad.Kind, Value: bad.Value}, added, err)
	}
	list()
	texts := []string{
		"",
		"Nothing relevant here",
		"INVOICE 2024 from Acme",
		"Rechnung Nr. 5 — ACME GmbH, Vertrag contract",
		"Hauptstraße 5 STRASSE",
		"İstanbul and istanbul and I\u0307stanbul",
		"ΣΊΣΥΦΟΣ ΣΊΣΥΦΟΣ",
		"KELVIN K and kelvin k",
		"Ǆǅǆ titlecase",
		"日本語のテキスト",
		"a b c",
		"nospace",
		"CONTRACT\r\ninvoice\r\n",
		"invalid utf8 \xff\xfe invoice",
		"invoice\x00nul",
	}
	for _, text := range texts {
		m, err := TestRules(ctx, opts, text)
		if m == nil {
			m = []RuleMatch{}
		}
		record(step{Op: "test", Text: text}, m, err)
	}

	// Documents with real note files for the dry run.
	st, err := store.Open(opts.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	notes := []classNote{
		{"a.md", "---\nsource: x\n---\n\nINVOICE body\n", "file"},
		{"b.md", "no frontmatter but invoice mentioned", "file"},
		{"c.md", "---\ninvoice: only in frontmatter\n---\n\nunrelated body\n", "file"},
		{"d.md", "---\ninvoice: unterminated frontmatter\n\nbody\n", "file"},
		{"e.md", "---\r\ninvoice: crlf frontmatter\r\n---\r\n\r\nbody invoice\r\n", "file"},
		{"f.md", "---\n---\n\ninvoice after empty frontmatter\n", "file"},
		{"g.md", "---\nk: v\n---\ninvoice with no blank line\n", "file"},
		{"h.md", "missing", "missing"},
		{"sub", "directory", "dir"},
		{"i.md", "STRASSE and Straße\n", "file"},
		{"j.md", "---\nk: v\n---\n\n\n\ninvoice after many blanks", "file"},
		{"k 日本語.md", "Rechnung 日本語", "file"},
	}
	for i, n := range notes {
		path := filepath.Join(vault, n.Name)
		switch n.Mode {
		case "file":
			if err := os.WriteFile(path, []byte(n.Content), 0o600); err != nil {
				t.Fatal(err)
			}
		case "dir":
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		hash := fmt.Sprintf("hash-%d", i)
		doc, _, err := st.CreateOrGet(ctx, "src"+n.Name, hash, "text/markdown", vault)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetVaultAndArchivePath(ctx, doc.ID, path, "", "", nil, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	// Documents the dry run cannot list: never written (no vault_path).
	if _, _, err := st.CreateOrGet(ctx, "pending.txt", "hash-pending", "text/plain", vault); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []classRule{
		{"invoice", "tag", "x"},
		{"INVOICE", "category", "x"},
		{"body", "tag", "x"},
		{"straße", "tag", "x"},
		{"strasse", "tag", "x"},
		{"日本語", "category", "x"},
		{"no-such-text", "tag", "x"},
		{"---", "tag", "x"},
		{"source", "tag", "x"},
		{"", "tag", "x"},
		{"p", "bogus", "x"},
		{"p", "tag", ""},
	} {
		r, err := DryRunRule(ctx, opts, p.Pattern, p.Kind, p.Value)
		if r != nil {
			r.VaultPath = norm(r.VaultPath)
			for i := range r.Matches {
				r.Matches[i].NotePath = norm(r.Matches[i].NotePath)
			}
			for i := range r.Skipped {
				r.Skipped[i].NotePath = norm(r.Skipped[i].NotePath)
				r.Skipped[i].Reason = norm(r.Skipped[i].Reason)
			}
		}
		record(step{Op: "dryrun", Pattern: p.Pattern, Kind: p.Kind, Value: p.Value}, r, err)
	}
	// Rules are untouched by a dry run.
	list()

	var out []map[string]any
	for _, s := range steps {
		raw, err := json.Marshal(s.Result)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{"input": map[string]any{"op": s.Op, "text": s.Text, "pattern": s.Pattern, "kind": s.Kind, "value": s.Value}, "result_json": string(raw), "error": s.Error})
	}
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	hashes := map[string]string{}
	for _, name := range []string{"api.go", "classification_port_test.go", "internal/store/store.go"} {
		//nolint:gosec // path derived from the repository checkout
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	notesOut := []map[string]string{}
	for _, n := range notes {
		notesOut = append(notesOut, map[string]string{"name": n.Name, "content": n.Content, "mode": n.Mode})
	}
	content, err := json.MarshalIndent(map[string]any{
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"schema_version": 1,
		"normalizations": []string{"temporary root replaced by <ROOT>; rule created_at replaced by <CREATED_AT>"},
		"rules":          rules,
		"notes":          notesOut,
		"steps":          out,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	target := filepath.Join(dir, "../../testdata/port/ingest/classification.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		//nolint:gosec // fixture path is derived from the repository checkout
		old, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(old, content) {
			t.Fatal("ingest classification fixture drift; regenerate deliberately")
		}
	}
	t.Logf("froze %d classification steps", len(steps))
}
