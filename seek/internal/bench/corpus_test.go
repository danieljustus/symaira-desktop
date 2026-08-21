package bench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCorpus(t *testing.T) {
	// Create a temporary corpus directory.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "doc1.txt"), []byte("content of doc1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "doc2.txt"), []byte("content of doc2"), 0644); err != nil {
		t.Fatal(err)
	}
	// Non-txt file should be skipped.
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# Readme"), 0644); err != nil {
		t.Fatal(err)
	}

	docs, err := LoadCorpus(dir)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}

	if len(docs) != 2 {
		t.Fatalf("expected 2 docs, got %d", len(docs))
	}
	if docs[0].Path != "doc1" {
		t.Errorf("expected doc path 'doc1', got '%s'", docs[0].Path)
	}
	if docs[1].Path != "doc2" {
		t.Errorf("expected doc path 'doc2', got '%s'", docs[1].Path)
	}
}

func TestLoadCorpus_NoDir(t *testing.T) {
	_, err := LoadCorpus("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for nonexistent directory")
	}
}

func TestLoadJudgments(t *testing.T) {
	yamlContent := `
queries:
  - query: "Go programming"
    relevant_ids: ["go-programming"]
  - query: "machine learning"
    relevant_ids: ["machine-learning", "deep-learning"]
`
	path := filepath.Join(t.TempDir(), "judgments.yaml")
	if err := os.WriteFile(path, []byte(yamlContent), 0644); err != nil {
		t.Fatal(err)
	}

	judgments, err := LoadJudgments(path)
	if err != nil {
		t.Fatalf("LoadJudgments: %v", err)
	}

	if len(judgments) != 2 {
		t.Fatalf("expected 2 judgments, got %d", len(judgments))
	}
	if judgments[0].Query != "Go programming" {
		t.Errorf("expected query 'Go programming', got '%s'", judgments[0].Query)
	}
	if len(judgments[0].RelevantIDs) != 1 || judgments[0].RelevantIDs[0] != "go-programming" {
		t.Errorf("unexpected relevant IDs for query 0: %v", judgments[0].RelevantIDs)
	}
	if len(judgments[1].RelevantIDs) != 2 {
		t.Errorf("expected 2 relevant IDs for query 1, got %d", len(judgments[1].RelevantIDs))
	}
}

func TestLoadJudgments_BadPath(t *testing.T) {
	_, err := LoadJudgments("/nonexistent/judgments.yaml")
	if err == nil {
		t.Fatal("expected error for nonexistent judgment file")
	}
}
