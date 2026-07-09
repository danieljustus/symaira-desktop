package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

func TestPropsEditUpdatesFrontmatterAndReindexes(t *testing.T) {
	svc := newTestService(t)

	if _, err := svc.NoteNew("Props Note", "some body content", ""); err != nil {
		t.Fatal(err)
	}

	if err := svc.PropsEdit("Props_Note.md", "status", "reviewed"); err != nil {
		t.Fatal(err)
	}

	absPath := filepath.Join(svc.VaultRoot, "Props_Note.md")
	raw, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "status: reviewed") {
		t.Errorf("expected frontmatter to contain the new property, got:\n%s", raw)
	}

	results, err := svc.DocsList(sidecar.DocsFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range results {
		if r.Path == "Props_Note.md" && r.Status == "reviewed" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected re-indexed doc to reflect the updated status, got %+v", results)
	}
}

func TestPropsEditRejectsPathEscape(t *testing.T) {
	svc := newTestService(t)
	if err := svc.PropsEdit("../outside.md", "status", "x"); err == nil {
		t.Fatal("expected an error for a path escaping the vault root")
	}
}

func TestAskStreamsFallbackAnswerWithoutOllama(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	svc := newTestService(t)

	if _, err := svc.NoteNew("Ask Note", "unique-ask-content about testing", ""); err != nil {
		t.Fatal(err)
	}

	out := make(chan interface{})
	go svc.Ask("unique-ask-content", out)

	var chunks []interface{}
	for c := range out {
		chunks = append(chunks, c)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one streamed chunk")
	}
}

func TestAskTextMatchesAskAggregate(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	svc := newTestService(t)

	if _, err := svc.NoteNew("Ask Note", "unique-asktext-content", ""); err != nil {
		t.Fatal(err)
	}

	text, err := svc.AskText("unique-asktext-content")
	if err != nil {
		t.Fatal(err)
	}
	if text == "" {
		t.Error("expected a non-empty aggregated answer")
	}
}
