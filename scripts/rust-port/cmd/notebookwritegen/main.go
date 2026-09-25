// Command notebookwritegen records notebook source-write behavior from Go.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/danieljustus/symaira-desktop/internal/notebook"
)

type fixture struct {
	SchemaVersion int    `json:"schema_version"`
	Oracle        string `json:"oracle"`
	SourceSHA256  string `json:"source_sha256"`
	Path          string `json:"path"`
	Initial       string `json:"initial"`
	Steps         []step `json:"steps"`
}

type step struct {
	Operation string             `json:"operation"`
	Source    string             `json:"source"`
	Output    *notebook.Notebook `json:"output"`
	Markdown  string             `json:"markdown"`
	UnixMode  uint32             `json:"unix_mode"`
}

func main() {
	output := flag.String("output", "testdata/port/vault/notebook-write.json", "fixture output path")
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
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("PASS notebook write fixture (%d steps)\n", len(value.Steps))
}

func build(root string) (fixture, error) {
	const rel = "notebooks/research.md"
	const initial = "---\ntype: notebook\ntitle: Research\ncreated: \"2026-01-02T03:04:05Z\"\ntags:\n  - notebook\nnotebook_id: research\ndescription: Keep me\nsources:\n  - z.md\nquery: source query\ncustom: keep me\n---\n\n# Original body\n"
	vaultRoot, err := os.MkdirTemp("", "notebook-write-oracle-")
	if err != nil {
		return fixture{}, err
	}
	defer os.RemoveAll(vaultRoot)
	path := filepath.Join(vaultRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return fixture{}, err
	}
	if err := os.WriteFile(path, []byte(initial), 0600); err != nil {
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
		written, err := os.ReadFile(path)
		if err != nil {
			return fixture{}, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return fixture{}, err
		}
		steps = append(steps, step{
			Operation: op.name,
			Source:    op.source,
			Output:    after,
			Markdown:  string(written),
			UnixMode:  uint32(info.Mode().Perm()),
		})
	}
	goSource, err := os.ReadFile(filepath.Join(root, "internal/notebook/notebook.go"))
	if err != nil {
		return fixture{}, err
	}
	digest := sha256.Sum256(goSource)
	return fixture{
		SchemaVersion: 1,
		Oracle:        "internal/notebook (Go production API)",
		SourceSHA256:  hex.EncodeToString(digest[:]),
		Path:          rel,
		Initial:       initial,
		Steps:         steps,
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
