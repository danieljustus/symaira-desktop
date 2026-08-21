package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-seek/internal/db"
	"gopkg.in/yaml.v3"
)

// Judgment pairs a query string with the set of document IDs relevant to it.
type Judgment struct {
	Query       string   `yaml:"query"`
	RelevantIDs []string `yaml:"relevant_ids"`
}

// JudgmentsFile is the top-level YAML structure for the judgment file.
type judgmentsFile struct {
	Queries []Judgment `yaml:"queries"`
}

// LoadCorpus reads all .txt documents from the given directory and returns
// them as db.Document values. Each file's stem (basename without .txt) is
// used as the document path.
func LoadCorpus(dir string) ([]db.Document, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading corpus dir %s: %w", dir, err)
	}

	var docs []db.Document
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		stem := strings.TrimSuffix(e.Name(), ".txt")
		docs = append(docs, db.Document{
			Path: stem,
			Hash: fmt.Sprintf("%x", data),
		})
	}
	return docs, nil
}

// LoadJudgments reads the YAML judgment file and returns the query→relevant-IDs map.
func LoadJudgments(path string) ([]Judgment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading judgments %s: %w", path, err)
	}

	var jf judgmentsFile
	if err := yaml.Unmarshal(data, &jf); err != nil {
		return nil, fmt.Errorf("parsing judgments YAML: %w", err)
	}
	return jf.Queries, nil
}
