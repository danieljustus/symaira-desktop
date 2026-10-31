package ingest

// Freezes the classification the real Pipeline applies while ingesting text
// sources: rule order, first-wins scalars, tag de-duplication, case folding and
// preset precedence, plus the persisted document columns.
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
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/writer"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type classifyCase struct {
	Name     string   `json:"name"`
	FileName string   `json:"file_name"`
	Body     string   `json:"body"`
	Category string   `json:"preset_category,omitempty"`
	Tags     []string `json:"preset_tags,omitempty"`
	Corr     string   `json:"preset_correspondent,omitempty"`
	DocType  string   `json:"preset_document_type,omitempty"`
}

func TestPortIngestClassifyPipelineContract(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(dir, "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	rules := [][3]string{
		{"invoice", "category", "Finance"},
		{"rechnung", "category", "Later category loses"},
		{"invoice", "tag", "billing"},
		{"INVOICE", "tag", "billing"},
		{"rechnung", "tag", "de"},
		{"acme", "correspondent", "ACME GmbH"},
		{"Acme", "correspondent", "Loses"},
		{"contract", "document_type", "Contract"},
		{"straße", "tag", "sharp-s"},
		{"İSTANBUL", "tag", "dotted-I"},
		{"σίσυφος", "tag", "sigma"},
		{"日本語", "tag", "ja"},
		{"x", "tag", "single-x"},
	}
	for _, r := range rules {
		if _, err := s.AddRule(ctx, r[0], r[1], r[2]); err != nil {
			t.Fatal(err)
		}
	}
	cases := []classifyCase{
		{Name: "no match", FileName: "n1.txt", Body: "lorem ipsum"},
		{Name: "category and tags", FileName: "n2.txt", Body: "INVOICE 2024 total"},
		{Name: "rechnung overrides nothing", FileName: "n3.md", Body: "# Rechnung\n\nAcme invoice for the contract"},
		{Name: "duplicate tag values", FileName: "n4.txt", Body: "invoice INVOICE Invoice"},
		{Name: "csv", FileName: "n5.csv", Body: "a,b\nacme,invoice\n"},
		{Name: "unicode folding", FileName: "n6.txt", Body: "Hauptstraße ΣΊΣΥΦΟΣ İstanbul 日本語"},
		{Name: "ascii fold of dotted I", FileName: "n7.txt", Body: "istanbul"},
		{Name: "preset category", FileName: "n8.txt", Body: "invoice acme contract", Category: "Preset"},
		{Name: "preset tags replace", FileName: "n9.txt", Body: "invoice acme contract", Tags: []string{"p1", "p2"}},
		{Name: "preset correspondent and type", FileName: "n10.txt", Body: "invoice acme contract", Corr: "Me", DocType: "Letter"},
		{Name: "all presets", FileName: "n11.txt", Body: "invoice acme contract", Category: "C", Tags: []string{"t"}, Corr: "K", DocType: "D"},
		{Name: "preset on empty match", FileName: "n12.txt", Body: "nothing", Category: "C"},
		{Name: "crlf", FileName: "n13.txt", Body: "invoice\r\nacme\r\n"},
		{Name: "empty file", FileName: "n14.txt", Body: ""},
		{Name: "single letter pattern", FileName: "n15.txt", Body: "X marks the spot"},
	}
	type outcome struct {
		Name          string   `json:"name"`
		Error         string   `json:"error"`
		Category      string   `json:"category"`
		Tags          []string `json:"tags"`
		Correspondent string   `json:"correspondent"`
		DocumentType  string   `json:"document_type"`
		Text          string   `json:"text"`
		DocCategory   string   `json:"doc_category"`
		DocTags       []string `json:"doc_tags"`
		DocCorr       string   `json:"doc_correspondent"`
		DocType       string   `json:"doc_document_type"`
	}
	var outcomes []outcome
	for i, c := range cases {
		vault := filepath.Join(dir, fmt.Sprintf("vault%d", i))
		p := &Pipeline{Store: s, Writer: &writer.NoteWriter{Vault: vault}, ArchiveDir: filepath.Join(dir, fmt.Sprintf("archive%d", i))}
		path := filepath.Join(dir, c.FileName)
		if err := os.WriteFile(path, []byte(c.Body), 0o600); err != nil {
			t.Fatal(err)
		}
		var opts *IngestOptions
		if c.Category != "" || len(c.Tags) > 0 || c.Corr != "" || c.DocType != "" {
			opts = &IngestOptions{PresetCategory: c.Category, PresetTags: c.Tags, PresetCorrespondent: c.Corr, PresetDocumentType: c.DocType}
		}
		o := outcome{Name: c.Name}
		res, err := p.Ingest(ctx, path, opts)
		if err != nil {
			o.Error = strings.ReplaceAll(err.Error(), dir, "<ROOT>")
		} else {
			o.Category, o.Tags, o.Correspondent, o.DocumentType = res.Category, res.Tags, res.Correspondent, res.DocumentType
			o.Text = res.Extract.Text
			doc, derr := s.ByHashInVault(ctx, res.SHA256, p.destinationVaultRoot())
			if derr != nil {
				t.Fatal(derr)
			}
			o.DocCategory, o.DocTags, o.DocCorr, o.DocType = doc.Category, doc.Tags, doc.Correspondent, doc.DocumentType
		}
		outcomes = append(outcomes, o)
	}
	_, thisFile, _, _ := runtime.Caller(0)
	srcDir := filepath.Dir(thisFile)
	hashes := map[string]string{}
	for _, name := range []string{"pipeline.go", "classify_port_test.go"} {
		//nolint:gosec // path derived from the repository checkout
		b, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			t.Fatal(err)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, err := json.MarshalIndent(map[string]any{
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"schema_version": 1,
		"normalizations": []string{"temporary root replaced by <ROOT> in error text"},
		"rules":          rules,
		"cases":          cases,
		"outcomes":       outcomes,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	target := filepath.Join(srcDir, "../../../../testdata/port/ingest/classify-pipeline.json")
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
			t.Fatal("ingest classify pipeline fixture drift; regenerate deliberately")
		}
	}
	t.Logf("froze %d pipeline classifications", len(outcomes))
}
