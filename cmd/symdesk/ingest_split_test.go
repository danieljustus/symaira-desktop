package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

// writeMinimalPDF writes a minimal single-page PDF (the same fixture the
// ingest package tests use).
func writeMinimalPDF(t *testing.T, path string) {
	t.Helper()
	content := `%PDF-1.4
1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj
2 0 obj<</Type/Pages/Kids[3 0 R]/Count 1>>endobj
3 0 obj<</Type/Page/MediaBox[0 0 612 792]/Parent 2 0 R/Resources<<>>>>endobj
xref
0 4
0000000000 65535 f
0000000009 00000 n
0000000058 00000 n
0000000115 00000 n
trailer<</Size 4/Root 1 0 R>>
startxref
149
%%EOF
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

// makeMultiPagePDF builds a 3-page PDF by concatenating a minimal single-page
// PDF with qpdf, the same approach as internal/ingest/barcode_test.go.
func makeMultiPagePDF(t *testing.T, srcDir, outPath string) {
	t.Helper()
	if !ingest.HasQPDF() {
		t.Skip("qpdf not available")
	}
	one := filepath.Join(srcDir, "one.pdf")
	writeMinimalPDF(t, one)
	//nolint:gosec // test fixture construction with qpdf; paths are test-controlled
	cmd := exec.Command("qpdf", "--empty", "--pages", one, "1", one, "1", one, "1", "--", outPath)
	out, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(out), "operation succeeded with warnings") {
		t.Fatalf("qpdf concat: %v: %s", err, out)
	}
}

func TestIngestSplitMissingFlags(t *testing.T) {
	cmd := newIngestSplitCmd()
	cmd.SetContext(context.Background())
	if _, err := runCommand(t, cmd, []string{"x.pdf"}); err == nil {
		t.Error("expected error when --at and --output-dir are missing")
	}
}

func TestIngestSplitJSON(t *testing.T) {
	if !ingest.HasPopplerSplit() {
		t.Skip("poppler not available")
	}
	srcDir := t.TempDir()
	outDir := t.TempDir()
	pdfPath := filepath.Join(srcDir, "multi.pdf")
	makeMultiPagePDF(t, srcDir, pdfPath)

	origCfg := cfg
	cfg = &config.Config{}
	t.Cleanup(func() { cfg = origCfg })

	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	cmd := newIngestSplitCmd()
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("at", "2"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("output-dir", outDir); err != nil {
		t.Fatal(err)
	}
	out, err := runCommand(t, cmd, []string{pdfPath})
	if err != nil {
		t.Fatalf("ingest split failed: %v", err)
	}
	var resp struct {
		SchemaVersion int      `json:"schema_version"`
		Parts         []string `json:"parts"`
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("split output is not valid JSON: %v\noutput: %s", err, out)
	}
	if resp.SchemaVersion != ingest.SchemaVersion || len(resp.Parts) != 2 {
		t.Fatalf("unexpected split response: %+v", resp)
	}
	for _, p := range resp.Parts {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("part missing: %s: %v", p, err)
		}
	}
}
