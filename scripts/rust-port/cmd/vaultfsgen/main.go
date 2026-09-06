// Command vaultfsgen freezes native read-only walk and secure-path behavior.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/vault"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

type document struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	Tree          []treeFile       `json:"tree"`
	WalkAll       []walkEntry      `json:"walk_all"`
	WalkMarkdown  []string         `json:"walk_markdown"`
	SecurePaths   []secureCase     `json:"secure_paths"`
}

type treeFile struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	SymlinkTo string `json:"symlink_to,omitempty"`
	Directory bool   `json:"directory,omitempty"`
}

type walkEntry struct {
	Path    string `json:"path"`
	Type    string `json:"type"`
	Symlink string `json:"symlink,omitempty"`
}

type secureCase struct {
	ID         string `json:"id"`
	Input      string `json:"input"`
	Result     string `json:"result,omitempty"`
	ErrorClass string `json:"error_class,omitempty"`
}

func main() {
	output := flag.String("output", "testdata/port/vault/filesystem.json", "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", "ae86331930fdfa2b128b68ae5af7437091b9949a", "Go oracle commit")
	release := flag.String("oracle-release", "v0.12.2", "Go oracle release")
	flag.Parse()

	value, err := build(inventory.Oracle{Commit: *commit, Release: *release})
	if err != nil {
		fatal("build fixture: %v", err)
	}
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
			fatal("vault filesystem fixture drift; regenerate on %s", runtime.GOOS)
		}
		fmt.Println("PASS vault filesystem fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("PASS vault filesystem fixture generated (%s)\n", runtime.GOOS)
}

func build(oracle inventory.Oracle) (document, error) {
	root, err := os.MkdirTemp("", "symdesk-vault-fs-")
	if err != nil {
		return document{}, err
	}
	defer func() { _ = os.RemoveAll(root) }()
	outside, err := os.MkdirTemp("", "symdesk-vault-outside-")
	if err != nil {
		return document{}, err
	}
	defer func() { _ = os.RemoveAll(outside) }()

	tree := []treeFile{
		{Path: "a.md", Content: "a"}, {Path: "Upper.MD", Content: "upper"}, {Path: "image.png", Content: "png"}, {Path: ".hidden.md", Content: "hidden"},
		{Path: "folder/b.md", Content: "b"}, {Path: ".obsidian/x.md", Content: "x"}, {Path: "vendor/v.md", Content: "v"},
		{Path: "node_modules/n.md", Content: "n"}, {Path: "build/build.md", Content: "build"}, {Path: "dist/dist.md", Content: "dist"}, {Path: "venv/venv.md", Content: "venv"}, {Path: "__pycache__/cache.md", Content: "cache"},
		{Path: "real/inside.md", Content: "inside"}, {Path: "etc/passwd", Content: "not-system"},
	}
	for _, item := range tree {
		path := filepath.Join(root, filepath.FromSlash(item.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return document{}, err
		}
		if err := os.WriteFile(path, []byte(item.Content), 0o600); err != nil {
			return document{}, err
		}
	}
	outsideFile := filepath.Join(outside, "outside.md")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		return document{}, err
	}
	if runtime.GOOS != "windows" {
		links := []treeFile{{Path: "inside-link.md", SymlinkTo: "real/inside.md"}, {Path: "outside-link.md", SymlinkTo: "<OUTSIDE>/outside.md"}, {Path: "dir-link", SymlinkTo: "<OUTSIDE>", Directory: true}}
		for _, item := range links {
			target := item.SymlinkTo
			if strings.HasPrefix(target, "<OUTSIDE>") {
				target = filepath.Join(outside, strings.TrimPrefix(strings.TrimPrefix(target, "<OUTSIDE>"), "/"))
			} else {
				target = filepath.Join(root, filepath.FromSlash(target))
			}
			if err := os.Symlink(target, filepath.Join(root, filepath.FromSlash(item.Path))); err != nil {
				return document{}, err
			}
			tree = append(tree, item)
		}
	}

	var all []walkEntry
	if err := vault.WalkAll(root, func(path string, entry fs.DirEntry) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := walkEntry{Path: filepath.ToSlash(rel), Type: "file"}
		if entry.Type()&os.ModeSymlink != 0 {
			item.Type = "symlink"
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			item.Symlink = normalize(target, root, outside)
		}
		all = append(all, item)
		return nil
	}); err != nil {
		return document{}, err
	}
	var markdown []string
	if err := vault.Walk(root, func(path string) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		markdown = append(markdown, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		return document{}, err
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Path < all[j].Path })
	sort.Strings(markdown)

	secureInputs := []struct{ id, input string }{{"existing", "folder/b.md"}, {"missing", "folder/new/note.md"}, {"traversal", "../outside.md"}, {"absolute", "/etc/passwd"}, {"root", "."}}
	if runtime.GOOS != "windows" {
		secureInputs = append(secureInputs, struct{ id, input string }{"contained-symlink", "inside-link.md"}, struct{ id, input string }{"external-symlink", "outside-link.md"})
	}
	secure := make([]secureCase, 0, len(secureInputs))
	for _, item := range secureInputs {
		resolved, err := vault.SecurePath(root, item.input)
		out := secureCase{ID: item.id, Input: item.input}
		if err != nil {
			switch {
			case strings.Contains(err.Error(), "path traversal denied"):
				out.ErrorClass = "traversal"
			case strings.Contains(err.Error(), "symlink escape denied"):
				out.ErrorClass = "symlink_escape"
			default:
				out.ErrorClass = "other"
			}
		} else {
			out.Result = normalize(resolved, root, outside)
		}
		secure = append(secure, out)
	}
	return document{SchemaVersion: 1, Oracle: oracle, Tree: tree, WalkAll: all, WalkMarkdown: markdown, SecurePaths: secure}, nil
}

func normalize(value, root, outside string) string {
	value = filepath.Clean(value)
	rootCandidates := []string{filepath.Clean(root)}
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		rootCandidates = append(rootCandidates, canonical)
	}
	for _, candidate := range rootCandidates {
		if strings.HasPrefix(value, candidate) {
			return filepath.ToSlash("<ROOT>" + strings.TrimPrefix(value, candidate))
		}
	}
	outsideCandidates := []string{filepath.Clean(outside)}
	if canonical, err := filepath.EvalSymlinks(outside); err == nil {
		outsideCandidates = append(outsideCandidates, canonical)
	}
	for _, candidate := range outsideCandidates {
		if strings.HasPrefix(value, candidate) {
			return filepath.ToSlash("<OUTSIDE>" + strings.TrimPrefix(value, candidate))
		}
	}
	return filepath.ToSlash(value)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
