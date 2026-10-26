package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type helpProcessResult struct {
	Args     []string `json:"args"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}

type helpProcessFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	Commands      int                 `json:"commands"`
	Help          map[string]string   `json:"help"`
	Completion    map[string]string   `json:"completion"`
	RootUsage     string              `json:"root_usage"`
	Cases         []helpProcessResult `json:"cases"`
}

// Run the executable, not Cobra in-process: initialization, stream routing,
// exit status and completion generation are all observable contracts.
func TestPortHelpProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH=" + root}
	fixture := helpProcessFixture{SchemaVersion: 1, Help: map[string]string{}, Completion: map[string]string{}}
	run := func(args []string) helpProcessResult {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir, cmd.Env = root, environment
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				t.Fatalf("help process timeout: %v", args)
			}
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		result := helpProcessResult{Args: args, ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
		fixture.Cases = append(fixture.Cases, result)
		return result
	}
	treeBytes, err := os.ReadFile("../../testdata/port/cli/symdesk-command-tree.json")
	if err != nil {
		t.Fatal(err)
	}
	var tree struct {
		Commands []struct {
			Path string `json:"path"`
		} `json:"commands"`
	}
	if err := json.Unmarshal(treeBytes, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Commands) != 207 {
		t.Fatalf("frozen tree changed: %d", len(tree.Commands))
	}
	for _, node := range tree.Commands {
		path := strings.TrimPrefix(node.Path, "symdesk")
		tokens := strings.Fields(path)
		result := run(append(append([]string{}, tokens...), "--help"))
		if result.ExitCode != 0 || result.Stderr != "" || result.Stdout == "" {
			t.Fatalf("invalid help %s: %+v", node.Path, result)
		}
		fixture.Help[strings.Join(tokens, " ")] = result.Stdout
		run(append([]string{"help"}, tokens...))
	}
	fixture.Commands = len(tree.Commands)
	run([]string{})
	unknown := run([]string{"help", "not-a-command"})
	_, fixture.RootUsage, _ = strings.Cut(unknown.Stderr, "\n")
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		for _, noDesc := range []bool{false, true} {
			args := []string{"completion", shell}
			key := shell
			if noDesc {
				args = append(args, "--no-descriptions")
				key += "-no-descriptions"
			}
			result := run(args)
			if result.ExitCode != 0 || result.Stderr != "" || result.Stdout == "" {
				t.Fatalf("completion %s: %+v", key, result)
			}
			fixture.Completion[key] = result.Stdout
		}
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	paths := []string{"../../testdata/port/cli/help-process.json", "../../testdata/port/cli/cobra-help.json"}
	for _, path := range paths {
		if os.Getenv("PORT_GENERATE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0o644); err != nil {
				t.Fatal(err)
			}
		} else {
			current, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(current, encoded) {
				t.Fatal(fmt.Sprintf("help process fixture drift: %s", path))
			}
		}
	}
	t.Logf("verified %d command paths and %d real-process cases", fixture.Commands, len(fixture.Cases))
}
