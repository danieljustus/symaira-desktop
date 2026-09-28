package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const recipeValidateFixturePath = "../../testdata/port/ai/recipe-validate.json"

type recipeValidateFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Cases         []recipeValidateCase `json:"cases"`
}

type recipeValidateCase struct {
	Name        string `json:"name"`
	Recipe      string `json:"recipe"`
	ExitCode    int    `json:"exit_code"`
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	JSON        bool   `json:"json"`
	ExactStderr bool   `json:"exact_stderr"`
}

func TestPortRecipeValidateCLIContract(t *testing.T) {
	fixture := observeRecipeValidateCLI(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), recipeValidateFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("recipe validate fixture is stale; regenerate with PORT_GENERATE=1 go test ./cmd/symdesk -run '^TestPortRecipeValidateCLIContract$'")
	}
}

func observeRecipeValidateCLI(t *testing.T) recipeValidateFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "symdesk")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	goEnv := exec.Command("go", "env", "GOMODCACHE")
	goEnv.Env = append(os.Environ(), "HOME="+currentUser.HomeDir)
	moduleCache, err := goEnv.Output()
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", binary, ".") //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	build.Env = append(os.Environ(), "GOMODCACHE="+string(bytes.TrimSpace(moduleCache)), "GOTMPDIR="+root)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go CLI: %v\n%s", err, out)
	}

	inputs := []struct{ name, yaml string }{
		{"valid_json", "version: 1\nname: daily\ntriggers: [manual, save]\ntools: [desk_search]\nwrite_cap: 2\n"},
		{"valid_text", "version: 1\nname: daily\ntriggers: [manual, save]\ntools: [desk_search]\nwrite_cap: 2\n"},
		{"unsupported_version", "version: 2\nname: daily\ntriggers: [manual]\ntools: [desk_search]\nwrite_cap: 2\n"},
		{"whitespace_name", "version: 1\nname: '  '\ntriggers: [manual]\ntools: [desk_search]\nwrite_cap: 2\n"},
		{"unknown_trigger", "version: 1\nname: daily\ntriggers: [magic]\ntools: [desk_search]\nwrite_cap: 2\n"},
		{"negative_write_cap", "version: 1\nname: daily\ntriggers: [manual]\ntools: [desk_search]\nwrite_cap: -1\n"},
		{"empty_tool", "version: 1\nname: daily\ntriggers: [manual]\ntools: ['']\nwrite_cap: 2\n"},
		{"duplicate_tool", "version: 1\nname: daily\ntriggers: [manual]\ntools: [desk_search, desk_search]\nwrite_cap: 2\n"},
		{"invalid_yaml", "version: ["},
		{"missing_file", ""},
	}
	fixture := recipeValidateFixture{SchemaVersion: 1}
	for _, input := range inputs {
		path := filepath.Join(root, input.name+".yml")
		if input.name != "missing_file" {
			if err := os.WriteFile(path, []byte(input.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"recipe", "validate", path}
		if input.name == "valid_json" {
			args = append([]string{"--json"}, args...)
		}
		cmd := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, "config"), "XDG_CACHE_HOME="+filepath.Join(home, "cache"), "XDG_DATA_HOME="+filepath.Join(home, "data"))
		out, err := cmd.Output()
		caseResult := recipeValidateCase{Name: input.name, Recipe: input.yaml, Stdout: string(out), JSON: input.name == "valid_json", ExactStderr: input.name != "invalid_yaml" && input.name != "missing_file"}
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				caseResult.ExitCode = exit.ExitCode()
				caseResult.Stderr = strings.ReplaceAll(string(exit.Stderr), path, "<recipe>")
				if runtime.GOOS == "windows" && input.name == "missing_file" {
					caseResult.Stderr = strings.ReplaceAll(caseResult.Stderr, "The system cannot find the file specified.", "no such file or directory")
				}
			} else {
				t.Fatalf("run Go CLI %s: %v", input.name, err)
			}
		}
		fixture.Cases = append(fixture.Cases, caseResult)
	}
	return fixture
}
