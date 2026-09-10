// Command representativegen generates the Go-owned RUST-006 CLI fixture.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const fixturePath = "testdata/port/representative/cases.json"
const fixedMTimeNS int64 = 1767323045000000000

type suite struct {
	SchemaVersion int       `json:"schema_version"`
	Oracle        oracle    `json:"oracle"`
	Cases         []caseDef `json:"cases"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type caseDef struct {
	ID                   string      `json:"id"`
	Binary               string      `json:"binary"`
	Stage                string      `json:"stage"`
	Args                 []string    `json:"args"`
	PrepareArgs          []string    `json:"prepare_args,omitempty"`
	TimeoutMS            int         `json:"timeout_ms"`
	StdoutMode           string      `json:"stdout_mode"`
	StderrMode           string      `json:"stderr_mode"`
	CompareFiles         bool        `json:"compare_files"`
	CompareSidecarLayout bool        `json:"compare_sidecar_layout,omitempty"`
	Setup                []setupFile `json:"setup,omitempty"`
}

type setupFile struct {
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	MTimeNS *int64 `json:"mtime_ns,omitempty"`
}

type httpSuite struct {
	SchemaVersion int        `json:"schema_version"`
	Oracle        oracle     `json:"oracle"`
	Cases         []httpCase `json:"cases"`
}

type httpCase struct {
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Auth    string            `json:"auth,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func main() {
	check := flag.Bool("check", false, "fail when the checked-in fixture differs")
	flag.Parse()
	root, err := repoRoot()
	if err != nil {
		fatal("find repository root: %v", err)
	}
	content, err := json.MarshalIndent(generated(), "", "  ")
	if err != nil {
		fatal("encode fixture: %v", err)
	}
	content = append(content, '\n')
	path := filepath.Join(root, fixturePath)
	if *check {
		checkHTTPFixture(root)
		//nolint:gosec // path is the fixed repository fixture path
		actual, readErr := os.ReadFile(path)
		if readErr != nil {
			fatal("read %s: %v", fixturePath, readErr)
		}
		if !bytes.Equal(actual, content) {
			fatal("fixture drift in %s; run representative-fixtures-generate", fixturePath)
		}
		fmt.Printf("PASS representative fixture verified: %s\n", fixturePath)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // checked-in repository directory
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil { //nolint:gosec // checked-in non-secret fixture
		fatal("write fixture: %v", err)
	}
	writeHTTPFixture(root)
	fmt.Printf("PASS generated %s\n", fixturePath)
}

func generated() suite {
	vault := "${WORKSPACE}/vault"
	prepare := []string{"ls", "--vault", vault, "--json"}
	return suite{
		SchemaVersion: 1,
		Oracle:        oracle{Commit: "745c08e8144971c61133c5d0e5d61c7ce405aad2", Release: "post-v0.12.2-security-880"},
		Cases: []caseDef{
			{ID: "version-positional-ls-not-command", Args: []string{"version", "ls"}},
			{ID: "version-positional-search-not-command", Args: []string{"version", "search"}},
			{ID: "desk-ls-json", Args: []string{"ls", "--vault", vault, "--json"}, Setup: fixtureFiles(), CompareSidecarLayout: true},
			{ID: "desk-ls-text", Args: []string{"--output=text", "ls", "--vault", vault}, StdoutMode: "console_text", StderrMode: "console_text", Setup: fixtureFiles(), CompareSidecarLayout: true},
			{ID: "desk-ls-dir-json", Args: []string{"ls", "--dir", "nested", "--vault", vault, "--output=json"}, Setup: fixtureFiles(), CompareSidecarLayout: true},
			{ID: "desk-search-json", Args: []string{"search", "needle", "--vault", vault, "--json"}, PrepareArgs: prepare, Setup: fixtureFiles(), CompareSidecarLayout: true},
			{ID: "desk-search-text", Args: []string{"--output=text", "search", "needle", "--vault", vault}, PrepareArgs: prepare, StdoutMode: "console_text", StderrMode: "console_text", Setup: fixtureFiles(), CompareSidecarLayout: true},
			{ID: "desk-search-inherited-output", Args: []string{"--output=json", "search", "needle", "--vault", vault}, PrepareArgs: prepare, Setup: fixtureFiles(), CompareSidecarLayout: true},
			{ID: "desk-search-fresh-index-empty", Args: []string{"search", "needle", "--vault", vault, "--json"}, Setup: fixtureFiles(), CompareSidecarLayout: true},
			{ID: "desk-search-too-many-json", Args: []string{"search", "--json", "one", "two"}},
			{ID: "desk-search-required-text", Args: []string{"search"}, StdoutMode: "console_text", StderrMode: "console_text"},
			{ID: "desk-search-required-json", Args: []string{"search", "--json"}},
		},
	}
}

func fixtureFiles() []setupFile {
	mtime := fixedMTimeNS
	return []setupFile{
		{Path: "vault/alpha.md", Content: "---\ntitle: Alpha Note\ncreated: 2026-01-02T03:04:05Z\n---\nneedle alpha body\n", Mode: 0o600, MTimeNS: &mtime},
		{Path: "vault/nested/beta.md", Content: "---\ntitle: Nested Beta\n---\nneedle beta body\n", Mode: 0o600, MTimeNS: &mtime},
	}
}

func (c *caseDef) setDefaults() {
	if c.Binary == "" {
		c.Binary = "symdesk"
	}
	if c.Stage == "" {
		c.Stage = "representative"
	}
	if c.TimeoutMS == 0 {
		c.TimeoutMS = 10000
	}
	if c.StdoutMode == "" {
		c.StdoutMode = "bytes"
	}
	if c.StderrMode == "" {
		c.StderrMode = "bytes"
	}
}

func (s suite) MarshalJSON() ([]byte, error) {
	for i := range s.Cases {
		s.Cases[i].setDefaults()
	}
	type alias suite
	return json.Marshal(alias(s))
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
