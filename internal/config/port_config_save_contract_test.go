package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	cfgSaveFixturePath  = "testdata/port/config/config-save.json"
	cfgSaveSchema       = 1
	cfgSaveOracleCommit = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
	cfgSaveOracleRel    = "post-v0.12.2-security-880"
)

// cfgSavePrefixes maps an error stage to the exact Go wrapper prefix.
var cfgSavePrefixes = map[string]string{
	"create_directory": "failed to create config directory:",
	"create_file":      "failed to create config file:",
	"encode":           "failed to encode config:",
	"close":            "failed to close config file:",
}

type cfgSaveOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type cfgSaveDir struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
}

type cfgSaveFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode"`
}

type cfgSaveSetup struct {
	Dirs []cfgSaveDir `json:"dirs"`
	File *cfgSaveFile `json:"file"`
}

type cfgSaveExpected struct {
	Outcome      string         `json:"outcome"`
	ErrorStage   string         `json:"error_stage,omitempty"`
	ErrorPrefix  string         `json:"error_prefix,omitempty"`
	ErrorMessage string         `json:"error_message_go,omitempty"`
	FileContent  string         `json:"file_content"`
	FileMode     int            `json:"file_mode"`
	DirModes     map[string]int `json:"dir_modes"`
	FileSet      []string       `json:"file_set"`
}

type cfgSaveCase struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Platform    string          `json:"platform"`
	Setup       cfgSaveSetup    `json:"setup"`
	Path        string          `json:"path"`
	Expected    cfgSaveExpected `json:"expected"`
}

type cfgSaveFixture struct {
	SchemaVersion int               `json:"schema_version"`
	Oracle        cfgSaveOracle     `json:"oracle"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []cfgSaveCase     `json:"cases"`
}

// cfgSaveCanonicalConfig returns one fully specified configuration value. Both
// sides must construct exactly these values, so the captured file bytes are a
// comparison of the same document rather than of two unrelated defaults.
func cfgSaveCanonicalConfig() *Config {
	return &Config{
		Vault:                       "/vault",
		Inbox:                       "inbox",
		ReviewThreshold:             85,
		LLMProvider:                 "ollama",
		LLMAPIKey:                   "",
		LLMModel:                    "claude-sonnet-5",
		OllamaURL:                   "http://127.0.0.1:11434",
		RecipeRunner:                "",
		HermesSession:               "",
		Language:                    "de",
		MaxTokens:                   8192,
		AgentMaxIterations:          5,
		HistoryMaxPerFile:           20,
		HistoryMaxAgeDays:           90,
		HistoryCheckpointMaxAgeDays: 30,
		TrashRetentionDays:          30,
		ResultsMaxAgeDays:           30,
		ResultsMaxPerTask:           20,
		DatasetExportMaxSensitivity: "internal",
		StoragePathTemplate:         "{{year}}/{{title}}",
	}
}

func cfgSaveCases() []cfgSaveCase {
	return []cfgSaveCase{
		{
			ID:          "new-parent-new-file",
			Description: "missing parent chain is created and the file is created",
			Platform:    "all",
			Path:        "nested/deep/config.toml",
		},
		{
			ID:          "existing-parent-keeps-mode",
			Description: "an existing parent directory keeps its mode",
			Platform:    "all",
			Setup:       cfgSaveSetup{Dirs: []cfgSaveDir{{Path: "parent", Mode: 0o755}}},
			Path:        "parent/config.toml",
		},
		{
			ID:          "existing-file-truncated-keeps-mode",
			Description: "an existing file keeps its mode and is truncated",
			Platform:    "all",
			Setup: cfgSaveSetup{
				Dirs: []cfgSaveDir{{Path: "parent", Mode: 0o755}},
				File: &cfgSaveFile{
					Path:    "parent/config.toml",
					Content: strings.Repeat("# stale line\n", 64),
					Mode:    0o644,
				},
			},
			Path: "parent/config.toml",
		},
		{
			ID:          "target-is-directory",
			Description: "the target path already is a directory",
			Platform:    "all",
			Setup:       cfgSaveSetup{Dirs: []cfgSaveDir{{Path: "config.toml", Mode: 0o755}}},
			Path:        "config.toml",
		},
		{
			ID:          "parent-is-regular-file",
			Description: "a parent path component is a regular file",
			Platform:    "all",
			Setup: cfgSaveSetup{File: &cfgSaveFile{
				Path:    "nested",
				Content: "not a directory\n",
				Mode:    0o644,
			}},
			Path: "nested/config.toml",
		},
		{
			ID:          "read-only-parent",
			Description: "an unwritable parent directory rejects the created file",
			Platform:    "unix",
			Setup:       cfgSaveSetup{Dirs: []cfgSaveDir{{Path: "ro", Mode: 0o500}}},
			Path:        "ro/config.toml",
		},
		{
			ID:          "deep-missing-chain",
			Description: "every missing ancestor is created by the save",
			Platform:    "all",
			Path:        "a/b/c/config.toml",
		},
		{
			ID:          "root-level-file",
			Description: "a file directly below the sandbox root",
			Platform:    "all",
			Path:        "config.toml",
		},
		{
			ID:          "sibling-directories-untouched",
			Description: "an unrelated sibling directory is left alone",
			Platform:    "all",
			Setup:       cfgSaveSetup{Dirs: []cfgSaveDir{{Path: "other", Mode: 0o755}}},
			Path:        "new/config.toml",
		},
	}
}

// cfgSaveRun executes one case in a fresh sandbox and captures what the Go
// implementation actually did. Generation and verification call the same
// function, so the fixture can never record an expectation the oracle does not
// reproduce.
func cfgSaveRun(t *testing.T, c cfgSaveCase) cfgSaveExpected {
	t.Helper()
	root := t.TempDir()

	for _, dir := range c.Setup.Dirs {
		full := filepath.Join(root, dir.Path)
		if err := os.MkdirAll(full, os.FileMode(dir.Mode)); err != nil {
			t.Fatalf("setup dir %s: %v", dir.Path, err)
		}
		if err := os.Chmod(full, os.FileMode(dir.Mode)); err != nil {
			t.Fatalf("setup chmod %s: %v", dir.Path, err)
		}
	}
	if c.Setup.File != nil {
		full := filepath.Join(root, c.Setup.File.Path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("setup file parent: %v", err)
		}
		//nolint:gosec // the case setup is written into the test's own temp directory
		if err := os.WriteFile(full, []byte(c.Setup.File.Content), os.FileMode(c.Setup.File.Mode)); err != nil {
			t.Fatalf("setup file %s: %v", c.Setup.File.Path, err)
		}
		if err := os.Chmod(full, os.FileMode(c.Setup.File.Mode)); err != nil {
			t.Fatalf("setup file chmod %s: %v", c.Setup.File.Path, err)
		}
	}

	target := filepath.Join(root, c.Path)
	expected := cfgSaveExpected{DirModes: map[string]int{}}

	if err := Save(target, cfgSaveCanonicalConfig()); err != nil {
		expected.Outcome = "error"
		expected.ErrorMessage = strings.ReplaceAll(err.Error(), root, "<sandbox>")
		for stage, prefix := range cfgSavePrefixes {
			if strings.HasPrefix(err.Error(), prefix) {
				expected.ErrorStage = stage
				expected.ErrorPrefix = prefix
				break
			}
		}
		if expected.ErrorStage == "" {
			t.Fatalf("unwrapped error %q does not carry a documented prefix", err.Error())
		}
		expected.FileSet = cfgSaveFileSet(t, root)
		expected.DirModes = cfgSaveDirModes(t, root)
		return expected
	}

	expected.Outcome = "ok"
	//nolint:gosec // the target lives inside the test's own temp directory
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read saved file: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat saved file: %v", err)
	}
	expected.FileContent = string(body)
	expected.FileMode = int(info.Mode().Perm())
	expected.FileSet = cfgSaveFileSet(t, root)
	expected.DirModes = cfgSaveDirModes(t, root)
	return expected
}

// cfgSaveDirModes records the permission bits of every directory inside the
// case sandbox, so a difference in how intermediate ancestors are created
// cannot hide behind the immediate parent. The sandbox root itself is harness
// scaffolding and stays out of the comparison. A directory the case made
// unreadable is recorded from its own metadata and then skipped, because the
// fixture must capture the mode that was set rather than fail on the sandbox
// the case deliberately created.
func cfgSaveDirModes(t *testing.T, root string) map[string]int {
	t.Helper()
	modes := map[string]int{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				return filepath.SkipDir
			}
			return err
		}
		if !info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		modes[filepath.ToSlash(rel)] = int(info.Mode().Perm())
		return nil
	}); err != nil {
		t.Fatalf("walk sandbox: %v", err)
	}
	return modes
}

func cfgSaveFileSet(t *testing.T, root string) []string {
	t.Helper()
	files := []string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				return filepath.SkipDir
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("walk sandbox: %v", err)
	}
	sort.Strings(files)
	return files
}

// TestPortConfigSaveContract owns the CFG-004 fixture. With PORT_GENERATE=1 it
// rewrites the fixture from the live Go implementation; a normal run verifies
// the checked-in fixture against that same implementation.
func TestPortConfigSaveContract(t *testing.T) {
	root := cfgSaveRepoRoot(t)
	fixturePath := filepath.Join(root, cfgSaveFixturePath)

	if os.Getenv("PORT_GENERATE") == "1" {
		fixture := cfgSaveFixture{
			SchemaVersion: cfgSaveSchema,
			Oracle:        cfgSaveOracle{Commit: cfgSaveOracleCommit, Release: cfgSaveOracleRel},
			SourceHashes: map[string]string{
				"internal/config/config.go": cfgSaveHash(t, root, "internal/config/config.go"),
			},
			Cases: cfgSaveCases(),
		}
		for index := range fixture.Cases {
			c := &fixture.Cases[index]
			c.Expected = cfgSaveRun(t, *c)
			if c.Setup.Dirs == nil {
				// A nil slice marshals to null; the fixture is a contract for
				// several languages, so every list is an explicit empty list.
				c.Setup.Dirs = []cfgSaveDir{}
			}
		}
		encoded, err := json.MarshalIndent(fixture, "", "  ")
		if err != nil {
			t.Fatalf("encode fixture: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o750); err != nil {
			t.Fatalf("create fixture dir: %v", err)
		}
		//nolint:gosec // fixture path is derived from the repository root
		if err := os.WriteFile(fixturePath, append(encoded, '\n'), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		return
	}

	//nolint:gosec // fixture path is derived from the repository root
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", cfgSaveFixturePath, err)
	}
	var fixture cfgSaveFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode %s: %v", cfgSaveFixturePath, err)
	}
	if fixture.SchemaVersion != cfgSaveSchema {
		t.Fatalf("fixture schema %d, want %d", fixture.SchemaVersion, cfgSaveSchema)
	}
	if want := cfgSaveHash(t, root, "internal/config/config.go"); fixture.SourceHashes["internal/config/config.go"] != want {
		t.Fatalf("config source drifted: fixture %s, current %s", fixture.SourceHashes["internal/config/config.go"], want)
	}
	if len(fixture.Cases) != len(cfgSaveCases()) {
		t.Fatalf("fixture declares %d cases, the generator defines %d", len(fixture.Cases), len(cfgSaveCases()))
	}

	for _, c := range fixture.Cases {
		if c.Platform == "unix" && runtime.GOOS == "windows" {
			continue
		}
		t.Run(c.ID, func(t *testing.T) {
			observed := cfgSaveRun(t, c)
			if observed.Outcome != c.Expected.Outcome {
				t.Fatalf("outcome: fixture %s, observed %s", c.Expected.Outcome, observed.Outcome)
			}
			if observed.ErrorStage != c.Expected.ErrorStage || observed.ErrorPrefix != c.Expected.ErrorPrefix {
				t.Fatalf("error stage/prefix: fixture %q/%q, observed %q/%q",
					c.Expected.ErrorStage, c.Expected.ErrorPrefix, observed.ErrorStage, observed.ErrorPrefix)
			}
			if observed.FileContent != c.Expected.FileContent {
				t.Fatalf("file content: fixture %d bytes, observed %d bytes",
					len(c.Expected.FileContent), len(observed.FileContent))
			}
			if !cfgSaveStringSlicesEqual(observed.FileSet, c.Expected.FileSet) {
				t.Fatalf("file set: fixture %v, observed %v", c.Expected.FileSet, observed.FileSet)
			}
			if runtime.GOOS == "windows" {
				return
			}
			if observed.FileMode != c.Expected.FileMode {
				t.Fatalf("file mode: fixture %o, observed %o", c.Expected.FileMode, observed.FileMode)
			}
			if !cfgSaveIntMapsEqual(observed.DirModes, c.Expected.DirModes) {
				t.Fatalf("directory modes: fixture %v, observed %v", c.Expected.DirModes, observed.DirModes)
			}
		})
	}
}

func cfgSaveIntMapsEqual(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		other, ok := right[key]
		if !ok || other != value {
			return false
		}
	}
	return true
}

func cfgSaveStringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cfgSaveHash(t *testing.T, root, rel string) string {
	t.Helper()
	//nolint:gosec // path is derived from the repository root
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cfgSaveRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root (go.mod) not found")
		}
		dir = parent
	}
}
