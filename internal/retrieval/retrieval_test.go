package retrieval

import (
	"errors"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/engine"
)

// Index/Delete/Search swallow failures on purpose. CurrentStatus must not:
// a caller asking whether retrieval is healthy has to hear "unknown" rather
// than a confident zero, which would read as "the index is empty".
func TestCurrentStatusPropagatesTheFailure(t *testing.T) {
	prev := StatusFunc
	t.Cleanup(func() { StatusFunc = prev })
	wantErr := errors.New("index unavailable")
	StatusFunc = func() (*Status, error) { return nil, wantErr }

	status, err := CurrentStatus()

	if !errors.Is(err, wantErr) {
		t.Fatalf("CurrentStatus() error = %v, want %v", err, wantErr)
	}
	if status != nil {
		t.Errorf("CurrentStatus() = %+v, want nil on failure", status)
	}
}

func TestCurrentStatusReportsADegradedBackend(t *testing.T) {
	prev := StatusFunc
	t.Cleanup(func() { StatusFunc = prev })
	StatusFunc = func() (*Status, error) {
		return &Status{DocumentCount: 12, EmbeddingModel: "local-hash", BackendAvailable: false}, nil
	}

	status, err := CurrentStatus()
	if err != nil {
		t.Fatalf("CurrentStatus() error = %v", err)
	}
	if status.BackendAvailable {
		t.Error("BackendAvailable = true, want false for the fallback model")
	}
	if status.DocumentCount != 12 {
		t.Errorf("DocumentCount = %d, want 12", status.DocumentCount)
	}
}

// BackendAvailable is the difference between "search works" and "search
// silently ranks worse", so the fallback model name must never be reported as
// an available backend.
func TestLocalHashModelIsNotAnAvailableBackend(t *testing.T) {
	if engine.LocalHashModelName == "" {
		t.Fatal("LocalHashModelName is empty; the availability check would treat every model as a fallback")
	}
	if engine.LocalHashModelName == "qwen3-embedding:0.6b" {
		t.Fatal("the fallback model name collides with a real embedding model")
	}
}

func TestSeamOverrides(t *testing.T) {
	prevIndex := IndexFunc
	prevDelete := DeleteFunc
	prevSearch := SearchFunc
	prevStatus := StatusFunc
	t.Cleanup(func() {
		IndexFunc = prevIndex
		DeleteFunc = prevDelete
		SearchFunc = prevSearch
		StatusFunc = prevStatus
	})

	var indexedDoc, indexedBody string
	IndexFunc = func(doc, body string) error {
		indexedDoc = doc
		indexedBody = body
		return nil
	}
	Index("doc1.md", "hello world")
	if indexedDoc != "doc1.md" || indexedBody != "hello world" {
		t.Errorf("Index did not invoke IndexFunc properly")
	}

	var deletedDoc string
	DeleteFunc = func(doc string) error {
		deletedDoc = doc
		return nil
	}
	Delete("doc1.md")
	if deletedDoc != "doc1.md" {
		t.Errorf("Delete did not invoke DeleteFunc properly")
	}

	SearchFunc = func(query string, limit int) ([]Result, error) {
		return []Result{{Path: "doc1.md", Score: 0.9, Snippet: "hello"}}, nil
	}
	results := Search("hello")
	if len(results) != 1 || results[0].Path != "doc1.md" {
		t.Errorf("Search did not invoke SearchFunc properly")
	}

	StatusFunc = func() (*Status, error) {
		return &Status{DocumentCount: 5, BackendAvailable: true}, nil
	}
	st, err := CurrentStatus()
	if err != nil || st.DocumentCount != 5 {
		t.Errorf("CurrentStatus did not invoke StatusFunc properly")
	}
}
