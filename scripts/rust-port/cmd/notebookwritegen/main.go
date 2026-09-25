// Command notebookwritegen records notebook source-write behavior from Go.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/notebook"
)

type fixture struct {
	SchemaVersion int        `json:"schema_version"`
	Oracle        string     `json:"oracle"`
	SourceSHA256  string     `json:"source_sha256"`
	Path          string     `json:"path"`
	Initial       string     `json:"initial"`
	Steps         []step     `json:"steps"`
	Creations     []creation `json:"creations"`
	Rejected      []rejected `json:"rejected"`
}

type step struct {
	Operation string             `json:"operation"`
	Source    string             `json:"source"`
	Output    *notebook.Notebook `json:"output"`
	Markdown  string             `json:"markdown"`
	UnixMode  uint32             `json:"unix_mode"`
}

type creation struct {
	Name            string             `json:"name"`
	Operation       string             `json:"operation"`
	Title           string             `json:"title"`
	Description     string             `json:"description"`
	Query           string             `json:"query,omitempty"`
	Existing        []string           `json:"existing,omitempty"`
	Output          *notebook.Notebook `json:"output"`
	Markdown        string             `json:"markdown"`
	UnixMode        uint32             `json:"unix_mode"`
	NotebookDirMode uint32             `json:"notebooks_dir_mode"`
}

type rejected struct {
	Name      string `json:"name"`
	Operation string `json:"operation"`
	Title     string `json:"title,omitempty"`
	Root      string `json:"root,omitempty"`
	Error     string `json:"error"`
}

func main() {
	output := flag.String("output", "testdata/port/vault/notebook-write.json", "fixture output path")
	check := flag.Bool("check", false, "compare fixture without writing")
	flag.Parse()
	root, err := repoRoot()
	if err != nil {
		fatal("find repository root: %v", err)
	}
	value, err := build(root)
	if err != nil {
		fatal("build fixture: %v", err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatal("encode fixture: %v", err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, filepath.FromSlash(*output))
	if *check {
		current, err := os.ReadFile(path) // #nosec G304 -- path is the explicit fixture output selected by this command.
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(current, data) {
			fatal("notebook write fixture drift; regenerate from Go production API")
		}
		fmt.Printf("PASS notebook write fixture (%d steps)\n", len(value.Steps))
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("PASS notebook write fixture (%d steps)\n", len(value.Steps))
}

func build(root string) (result fixture, resultErr error) {
	const rel = "notebooks/research.md"
	const initial = "---\ntype: notebook\ntitle: Research\ncreated: \"2026-01-02T03:04:05Z\"\ntags:\n  - notebook\nnotebook_id: research\ndescription: Keep me\nsources:\n  - z.md\nquery: source query\ncustom: keep me\n---\n\n# Original body\n"
	vaultRoot, err := os.MkdirTemp("", "notebook-write-oracle-")
	if err != nil {
		return fixture{}, err
	}
	defer func() {
		if err := os.RemoveAll(vaultRoot); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	path := filepath.Join(vaultRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fixture{}, err
	}
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil { // #nosec G304 -- path is under the fresh temporary vault root.
		return fixture{}, err
	}
	steps := []step{}
	for _, op := range []struct{ name, source string }{
		{"add", "b.md"}, {"add", "a.md"}, {"add", "b.md"},
		{"remove", "z.md"}, {"remove", "missing.md"},
	} {
		nb, err := notebook.Load(vaultRoot, rel)
		if err != nil {
			return fixture{}, err
		}
		if op.name == "add" {
			err = notebook.AddSource(vaultRoot, nb, op.source)
		} else {
			err = notebook.RemoveSource(vaultRoot, nb, op.source)
		}
		if err != nil {
			return fixture{}, fmt.Errorf("%s %s: %w", op.name, op.source, err)
		}
		after, err := notebook.Load(vaultRoot, rel)
		if err != nil {
			return fixture{}, err
		}
		written, err := os.ReadFile(path) // #nosec G304 -- path is under the fresh temporary vault root.
		if err != nil {
			return fixture{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return fixture{}, err
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			return fixture{}, fmt.Errorf("notebook file mode = %#o, want 0600", info.Mode().Perm())
		}
		steps = append(steps, step{
			Operation: op.name,
			Source:    op.source,
			Output:    after,
			Markdown:  string(written),
			UnixMode:  0600,
		})
	}
	goSource, err := os.ReadFile(filepath.Join(root, "internal/notebook/notebook.go")) // #nosec G304 -- root comes from this compiled generator's repository location.
	if err != nil {
		return fixture{}, err
	}
	digest := sha256.Sum256(goSource)
	creations, err := buildCreations()
	if err != nil {
		return fixture{}, err
	}
	rejectedCases, err := buildRejected()
	if err != nil {
		return fixture{}, err
	}
	return fixture{
		SchemaVersion: 2,
		Oracle:        "internal/notebook (Go production API)",
		SourceSHA256:  hex.EncodeToString(digest[:]),
		Path:          rel,
		Initial:       initial,
		Steps:         steps,
		Creations:     creations,
		Rejected:      rejectedCases,
	}, nil
}

func buildCreations() ([]creation, error) {
	specs := []creation{
		{Name: "trimmed_title", Operation: "new", Title: "  Research  ", Description: "Keep me"},
		{Name: "slug_collisions", Operation: "new", Title: "Research", Existing: []string{"research.md", "research-2.md"}},
		{Name: "punctuation_slug", Operation: "new", Title: "!!!"},
		{Name: "query_trimmed", Operation: "new_with_query", Title: "Query Results", Description: "Promoted from search", Query: "  source query \n"},
	}
	for index := range specs {
		caseData, err := buildCreation(specs[index])
		if err != nil {
			return nil, fmt.Errorf("creation %s: %w", specs[index].Name, err)
		}
		specs[index] = caseData
	}
	return specs, nil
}

func buildCreation(caseData creation) (result creation, resultErr error) {
	vaultRoot, err := os.MkdirTemp("", "notebook-create-oracle-")
	if err != nil {
		return creation{}, err
	}
	defer func() {
		if err := os.RemoveAll(vaultRoot); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	if len(caseData.Existing) != 0 {
		if err := os.MkdirAll(filepath.Join(vaultRoot, "notebooks"), 0750); err != nil {
			return creation{}, err
		}
		for _, name := range caseData.Existing {
			if err := os.WriteFile(filepath.Join(vaultRoot, "notebooks", name), []byte("occupied"), 0600); err != nil { // #nosec G304 -- fixture names are fixed by this generator.
				return creation{}, err
			}
		}
	}
	var nb *notebook.Notebook
	if caseData.Operation == "new_with_query" {
		nb, err = notebook.NewWithQuery(vaultRoot, caseData.Title, caseData.Description, caseData.Query)
	} else {
		nb, err = notebook.New(vaultRoot, caseData.Title, caseData.Description)
	}
	if err != nil {
		return creation{}, err
	}
	if _, err := time.Parse(time.RFC3339, nb.Created); err != nil {
		return creation{}, fmt.Errorf("invalid Go creation time: %w", err)
	}
	path := filepath.Join(vaultRoot, filepath.FromSlash(nb.Path))
	written, err := os.ReadFile(path) // #nosec G304 -- notebook path is returned by the Go production API inside a fresh vault root.
	if err != nil {
		return creation{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return creation{}, err
	}
	dirInfo, err := os.Stat(filepath.Join(vaultRoot, "notebooks"))
	if err != nil {
		return creation{}, err
	}
	if runtime.GOOS != "windows" && (info.Mode().Perm() != 0600 || dirInfo.Mode().Perm() != 0750) {
		return creation{}, fmt.Errorf("go notebook modes: file %#o, directory %#o", info.Mode().Perm(), dirInfo.Mode().Perm())
	}
	if strings.Count(string(written), nb.Created) != 1 {
		return creation{}, fmt.Errorf("go notebook creation timestamp missing or duplicated")
	}
	caseData.Output = nb
	caseData.Markdown = strings.Replace(string(written), nb.Created, "2026-01-02T03:04:05Z", 1)
	caseData.Output.Created = "2026-01-02T03:04:05Z"
	caseData.UnixMode = 0600
	caseData.NotebookDirMode = 0750
	return caseData, nil
}

func buildRejected() (result []rejected, resultErr error) {
	root, err := os.MkdirTemp("", "notebook-create-invalid-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil && resultErr == nil {
			resultErr = err
		}
	}()
	_, titleErr := notebook.New(root, "  ", "")
	if titleErr == nil {
		return nil, fmt.Errorf("empty notebook title unexpectedly succeeded")
	}
	missingRoot := filepath.Join(root, "missing")
	_, pathErr := notebook.New(missingRoot, "Valid", "")
	if pathErr == nil {
		return nil, fmt.Errorf("missing vault root unexpectedly succeeded")
	}
	if !strings.HasPrefix(pathErr.Error(), "cannot resolve vault root:") {
		return nil, fmt.Errorf("unexpected missing vault root error: %w", pathErr)
	}
	return []rejected{
		{Name: "empty_title", Operation: "new", Title: "  ", Error: titleErr.Error()},
		{Name: "missing_vault_root", Operation: "new", Title: "Valid", Root: "missing", Error: "cannot resolve vault root"},
	}, nil
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("locate generator source")
	}
	return filepath.Abs(filepath.Join(filepath.Dir(file), "../../../../"))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
