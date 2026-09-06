// Command vaultgen freezes read-only Markdown parsing behavior through the production Go vault parser.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/vault"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type document struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Cases         []parseCase      `json:"cases"`
}

type parseInput struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

type parseCase struct {
	Input      parseInput   `json:"input"`
	Document   *documentDTO `json:"document,omitempty"`
	Error      string       `json:"error,omitempty"`
	ErrorClass string       `json:"error_class,omitempty"`
}

type documentDTO struct {
	Path         string                   `json:"path"`
	SHA256       string                   `json:"sha256"`
	Title        string                   `json:"title"`
	Created      string                   `json:"created"`
	Tags         []string                 `json:"tags,omitempty"`
	Aliases      []string                 `json:"aliases,omitempty"`
	Frontmatter  map[string]canonicalYAML `json:"frontmatter"`
	Body         string                   `json:"body"`
	Links        []string                 `json:"links,omitempty"`
	Size         int64                    `json:"size"`
	DocumentDate string                   `json:"document_date"`
	Person       string                   `json:"person"`
	Status       string                   `json:"status"`
	DueDate      string                   `json:"due_date"`
	Confidence   int                      `json:"confidence"`
	OcrJSONPath  string                   `json:"ocr_json_path"`
	Simhash      string                   `json:"simhash"`
	ASN          *int                     `json:"asn,omitempty"`
	Type         string                   `json:"type"`
	DerivedFrom  string                   `json:"derived_from"`
	Derived      bool                     `json:"derived"`
}

type canonicalYAML struct {
	Type  string                   `json:"type"`
	Value any                      `json:"value,omitempty"`
	Items []canonicalYAML          `json:"items,omitempty"`
	Map   map[string]canonicalYAML `json:"map,omitempty"`
}

func main() {
	output := flag.String("output", "testdata/port/vault/parse.json", "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", "ae86331930fdfa2b128b68ae5af7437091b9949a", "Go oracle commit")
	release := flag.String("oracle-release", "v0.12.2", "Go oracle release")
	flag.Parse()

	value := document{SchemaVersion: 1, Oracle: inventory.Oracle{Commit: *commit, Release: *release}, Cases: buildCases()}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatal("marshal fixture: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			fatal("vault parse fixture drift; regenerate deliberately")
		}
		fmt.Println("PASS vault parse fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("PASS vault parse fixture generated (%d cases)\n", len(value.Cases))
}

func buildCases() []parseCase {
	inputs := []parseInput{
		{ID: "minimal-v1-mobile", Path: "notes/Mobile.md", Content: "---\ntitle: \"Einkaufsliste\"\ncreated: \"2026-09-06T12:00:00Z\"\ntags: []\n---\n\nMilch und Brot #Familie\n"},
		{ID: "no-frontmatter", Path: "notes/No_Frontmatter.md", Content: "Body only with [[Target]].\n"},
		{ID: "empty-frontmatter", Path: "notes/Empty.md", Content: "---\n---\nBody\n"},
		{ID: "unclosed-frontmatter", Path: "notes/Unclosed.md", Content: "Before\n---\ntitle: swallowed\nbody swallowed"},
		{ID: "late-frontmatter", Path: "notes/Late.md", Content: "Before\n---\ntitle: Late Title\ntags: [late]\n---\nAfter\n"},
		{ID: "full-v2", Path: "documents/Invoice.md", Content: "---\ntitle: Invoice\ncreated: \"2026-08-01T10:00:00Z\"\ntags: [invoice, Paid]\ndocument_date: \"2026-08-01\"\nperson: Daniel\nstatus: custom-status\ndue_date: \"2026-08-31\"\nconfidence: 91.9\nocr_json_path: OCR/invoice.json\nsimhash: abcdef0123456789\nunknown:\n  enabled: true\n  count: 3\n  values: [one, 2, false]\n---\nInvoice body #paid #Tax [[Customer|Display]] [[Customer#Heading]] ![[scan.pdf]]\n"},
		{ID: "yaml-coercions", Path: "notes/Coercions.md", Content: "---\ntitle: 123\ncreated: 2026-09-06\ntags: [one, 2, true, one]\naliases: [Alias, 3, false]\ndocument_date: 2026-09-05\nconfidence: 12.75\nderived: \"true\"\nderived_from: false\nnull_value: null\n---\nBody\n"},
		{ID: "scalar-tags-aliases", Path: "notes/Scalar.md", Content: "---\ntitle: Scalar\ntags: One\naliases: Alternative\n---\n#one #Two #two\n"},
		{ID: "derived-forced", Path: "notes/Derived.md", Content: "---\ntitle: Derived\nderived: false\nderived_from: source.md\n---\nBody\n"},
		{ID: "notebook-v4", Path: "notebooks/Research.md", Content: "---\ntitle: Research\ntype: notebook\nnotebook_id: research\nsources: [notes/a.md, documents/b.md]\ndescription: bounded set\n---\nNotebook\n"},
		{ID: "base-v5", Path: "bases/Tasks.md", Content: "---\ntitle: Tasks\ntype: base\nbase_id: tasks\naliases: [Task DB, Aufgaben]\nproperties:\n  status:\n    type: select\nviews:\n  - name: Open\n    filter:\n      status: open\n---\n# Tasks #project/tasks\n"},
		{ID: "dataset-v6", Path: "datasets/Finance.md", Content: "---\ntitle: Finance\ntype: dataset\ndataset_id: finance\nidentity_field: id\nsensitivity: confidential\nretention_rule: seven-years\nschema:\n  id: string\n  amount: number\ncoverage:\n  rows: 2\nprovenance:\n  source_name: fixture.csv\n  source_sha256: abc\n---\nDataset handle\n"},
		{ID: "sources-not-inferred", Path: "notes/Sources.md", Content: "---\ntitle: Sources\nsources: [a.md]\n---\nBody\n"},
		{ID: "unknown-type-document-inference", Path: "notes/Unknown.md", Content: "---\ntitle: Unknown\ntype: future-kind\nsource_path: \"\"\nmeeting_id: meeting\nbase_id: base\n---\nBody\n"},
		{ID: "meeting-inference", Path: "meetings/Meeting.md", Content: "---\ntitle: Meeting\nmeeting_id: m-1\n---\nBody\n"},
		{ID: "base-inference", Path: "bases/Base.md", Content: "---\ntitle: Base\nbase_id: b-1\n---\nBody\n"},
		{ID: "all-explicit-types", Path: "notes/Type.md", Content: "---\ntitle: Type\ntype: dataset\n---\nBody\n"},
		{ID: "links-code-and-tags", Path: "notes/Links.md", Content: "---\ntitle: Links\ntags: [Front, front]\n---\n[[One]] [[One]] [[one]] [[Two|Display]] [[Three#Heading]] ![[asset.png]] [[]]\n`[[Inline]] #inline`\n```md\n[[Fenced]] #fenced\n```\n# Heading #heading-tag\nURL https://example.test/#fragment and #123 C# #valid/tag//tail #trail-\n"},
		{ID: "crlf", Path: "notes/CRLF.md", Content: "---\r\ntitle: CRLF\r\ntags: []\r\n---\r\nBody\r\n"},
		{ID: "no-final-newline", Path: "notes/NoFinal.md", Content: "---\ntitle: No Final\n---\nBody"},
		{ID: "excalidraw", Path: "drawings/Sketch.excalidraw.md", Content: "---\ntitle: Sketch\ntags: [drawing]\n---\n# Drawing\n[[HiddenLink]] #hidden\n"},
		{ID: "invalid-yaml", Path: "notes/Bad.md", Content: "---\ntitle: [broken\n---\nBody\n"},
		{ID: "top-level-sequence", Path: "notes/Sequence.md", Content: "---\n- one\n- two\n---\nBody\n"},
		{ID: "duplicate-key", Path: "notes/Duplicate.md", Content: "---\ntitle: One\ntitle: Two\n---\nBody\n"},
		{ID: "asn-zero", Path: "notes/ASN0.md", Content: "---\ntitle: ASN\nasn: 0\n---\nBody\n"},
		{ID: "asn-negative", Path: "notes/ASNNeg.md", Content: "---\ntitle: ASN\nasn: -1\n---\nBody\n"},
		{ID: "asn-string", Path: "notes/ASNString.md", Content: "---\ntitle: ASN\nasn: \"42\"\n---\nBody\n"},
		{ID: "asn-float", Path: "notes/ASNFloat.md", Content: "---\ntitle: ASN\nasn: 42.5\n---\nBody\n"},
		{ID: "asn-valid", Path: "notes/ASN.md", Content: "---\ntitle: ASN\nasn: 42\n---\nBody\n"},
	}
	for _, kind := range []string{"note", "document", "meeting", "notebook", "base"} {
		inputs = append(inputs, parseInput{ID: "explicit-" + kind, Path: "types/" + kind + ".md", Content: fmt.Sprintf("---\ntitle: Type\ntype: %s\n---\nBody\n", kind)})
	}

	result := make([]parseCase, 0, len(inputs))
	for _, input := range inputs {
		parsed, err := vault.ParseBytes(input.Path, []byte(input.Content))
		out := parseCase{Input: input}
		if err != nil {
			out.Error = err.Error()
			switch {
			case strings.HasPrefix(err.Error(), "invalid frontmatter"):
				out.ErrorClass = "frontmatter"
			case strings.HasPrefix(err.Error(), "invalid asn"):
				out.ErrorClass = "asn"
			default:
				out.ErrorClass = "other"
			}
			result = append(result, out)
			continue
		}
		frontmatter := make(map[string]canonicalYAML, len(parsed.Frontmatter))
		keys := make([]string, 0, len(parsed.Frontmatter))
		for key := range parsed.Frontmatter {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			frontmatter[key] = canonicalize(parsed.Frontmatter[key])
		}
		out.Document = &documentDTO{Path: parsed.Path, SHA256: parsed.SHA256, Title: parsed.Title, Created: parsed.Created, Tags: parsed.Tags, Aliases: parsed.Aliases, Frontmatter: frontmatter, Body: parsed.Body, Links: parsed.Links, Size: parsed.Size, DocumentDate: parsed.DocumentDate, Person: parsed.Person, Status: parsed.Status, DueDate: parsed.DueDate, Confidence: parsed.Confidence, OcrJSONPath: parsed.OcrJSONPath, Simhash: parsed.Simhash, ASN: parsed.ASN, Type: parsed.Type, DerivedFrom: parsed.DerivedFrom, Derived: parsed.IsDerived()}
		result = append(result, out)
	}
	return result
}

func canonicalize(value any) canonicalYAML {
	switch typed := value.(type) {
	case nil:
		return canonicalYAML{Type: "null"}
	case string:
		return canonicalYAML{Type: "string", Value: typed}
	case bool:
		return canonicalYAML{Type: "bool", Value: typed}
	case int:
		return canonicalYAML{Type: "int", Value: typed}
	case int64:
		return canonicalYAML{Type: "int", Value: typed}
	case float64:
		return canonicalYAML{Type: "float", Value: typed}
	case time.Time:
		return canonicalYAML{Type: "time", Value: typed.Format(time.RFC3339Nano)}
	case []any:
		items := make([]canonicalYAML, len(typed))
		for index, item := range typed {
			items[index] = canonicalize(item)
		}
		return canonicalYAML{Type: "list", Items: items}
	case map[string]any:
		values := make(map[string]canonicalYAML, len(typed))
		for key, item := range typed {
			values[key] = canonicalize(item)
		}
		return canonicalYAML{Type: "map", Map: values}
	default:
		return canonicalYAML{Type: reflect.TypeOf(value).String(), Value: fmt.Sprint(value)}
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
