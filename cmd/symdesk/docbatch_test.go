package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func setupBatchVault(t *testing.T, names ...string) string {
	t.Helper()
	vaultDir := t.TempDir()
	for _, n := range names {
		md := "---\ntitle: \"" + strings.TrimSuffix(n, ".md") + "\"\nstatus: \"open\"\ntags: []\n---\n\nBody\n"
		writeTestFile(t, filepath.Join(vaultDir, n), md)
	}
	origCfg := cfg
	cfg = &config.Config{Vault: vaultDir}
	t.Cleanup(func() { cfg = origCfg })
	return vaultDir
}

func execRootCapture(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	jsonFlag = true
	t.Cleanup(func() { jsonFlag = false })

	cmd := newRootCmd()
	cmd.SetArgs(args)
	if stdin != "" {
		cmd.SetIn(strings.NewReader(stdin))
	}

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	execErr := cmd.Execute()

	closeTestResource(t, "stdout pipe writer", w.Close)
	os.Stdout = origStdout
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String(), execErr
}

type batchPayload struct {
	Status  string `json:"status"`
	Updated int    `json:"updated"`
	Failed  int    `json:"failed"`
	Results []struct {
		File   string `json:"file"`
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"results"`
}

func decodeBatch(t *testing.T, out string) batchPayload {
	t.Helper()
	var p batchPayload
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("invalid batch JSON: %v\noutput: %s", err, out)
	}
	return p
}

func TestDocStatusBatchMultipleFiles(t *testing.T) {
	vaultDir := setupBatchVault(t, "a.md", "b.md", "c.md")

	out, err := execRootCapture(t, "", "doc", "status", "a.md", "b.md", "c.md", "done", "--json")
	if err != nil {
		t.Fatal(err)
	}
	p := decodeBatch(t, out)
	if p.Status != "updated" || p.Updated != 3 || p.Failed != 0 {
		t.Fatalf("expected 3 updates, got %+v", p)
	}
	for _, n := range []string{"a.md", "b.md", "c.md"} {
		data := readTestFile(t, filepath.Join(vaultDir, n))
		if !strings.Contains(string(data), "status: \"done\"") {
			t.Errorf("%s frontmatter not updated: %s", n, data)
		}
	}
}

func TestDocStatusBatchPartialFailure(t *testing.T) {
	setupBatchVault(t, "a.md")

	out, err := execRootCapture(t, "", "doc", "status", "a.md", "missing.md", "done", "--json")
	if err != nil {
		t.Fatal(err)
	}
	p := decodeBatch(t, out)
	if p.Status != "partial" || p.Updated != 1 || p.Failed != 1 {
		t.Fatalf("expected partial 1/1, got %+v", p)
	}
	if p.Results[1].File != "missing.md" || p.Results[1].Error == "" {
		t.Fatalf("expected per-file error for missing.md, got %+v", p.Results)
	}
}

func TestDocStatusBatchStdin(t *testing.T) {
	vaultDir := setupBatchVault(t, "a.md", "b.md")

	out, err := execRootCapture(t, "a.md\nb.md\n", "doc", "status", "paid", "--stdin", "--json")
	if err != nil {
		t.Fatal(err)
	}
	p := decodeBatch(t, out)
	if p.Updated != 2 || p.Failed != 0 {
		t.Fatalf("expected 2 updates via stdin, got %+v", p)
	}
	data := readTestFile(t, filepath.Join(vaultDir, "a.md"))
	if !strings.Contains(string(data), "status: \"paid\"") {
		t.Errorf("a.md not updated: %s", data)
	}
}

func TestDocStatusSingleFileErrorKeepsExitBehaviour(t *testing.T) {
	setupBatchVault(t, "a.md")
	_, err := execRootCapture(t, "", "doc", "status", "a.md", "bogus", "--json")
	if err == nil || !strings.Contains(err.Error(), "invalid status") {
		t.Fatalf("expected invalid status error, got %v", err)
	}
}

func TestDocTagBatchAddRemove(t *testing.T) {
	vaultDir := setupBatchVault(t, "a.md", "b.md")

	out, err := execRootCapture(t, "", "doc", "tag", "add", "urgent", "a.md", "b.md", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if p := decodeBatch(t, out); p.Updated != 2 {
		t.Fatalf("expected 2 tag adds, got %+v", p)
	}
	data := readTestFile(t, filepath.Join(vaultDir, "a.md"))
	if !strings.Contains(string(data), "\"urgent\"") {
		t.Errorf("a.md missing tag: %s", data)
	}

	out, err = execRootCapture(t, "", "doc", "tag", "remove", "urgent", "a.md", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if p := decodeBatch(t, out); p.Updated != 1 {
		t.Fatalf("expected 1 tag removal, got %+v", p)
	}
	data = readTestFile(t, filepath.Join(vaultDir, "a.md"))
	if strings.Contains(string(data), "urgent") {
		t.Errorf("a.md still has tag: %s", data)
	}
}

func TestDocTypeAndCorrespondentBatch(t *testing.T) {
	vaultDir := setupBatchVault(t, "a.md", "b.md")

	if _, err := execRootCapture(t, "", "doc", "type", "a.md", "b.md", "invoice", "--json"); err != nil {
		t.Fatal(err)
	}
	if _, err := execRootCapture(t, "", "doc", "correspondent", "a.md", "Power Co", "--json"); err != nil {
		t.Fatal(err)
	}
	data := readTestFile(t, filepath.Join(vaultDir, "a.md"))
	if !strings.Contains(string(data), "document_type: \"invoice\"") || !strings.Contains(string(data), "correspondent: \"Power Co\"") {
		t.Errorf("a.md missing type/correspondent: %s", data)
	}
}
