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
	"time"
)

const historyTasksFixturePath = "../../testdata/port/cli/history-tasks.json"

type historyTasksFixture struct {
	SchemaVersion int                    `json:"schema_version"`
	Cases         []historyTasksPortCase `json:"cases"`
}

type historyTasksPortCase struct {
	Name      string            `json:"name"`
	JSON      bool              `json:"json"`
	Manifests map[string]string `json:"manifests"`
	ExitCode  int               `json:"exit_code"`
	Stdout    string            `json:"stdout"`
	Stderr    string            `json:"stderr"`
}

func TestPortHistoryTasksCLIContract(t *testing.T) {
	fixture := observeHistoryTasksCLI(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), historyTasksFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("history tasks fixture is stale; regenerate through portgen")
	}
}

func observeHistoryTasksCLI(t *testing.T) historyTasksFixture {
	t.Helper()
	root := t.TempDir()
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

	fixture := historyTasksFixture{SchemaVersion: 1}
	inputs := []historyTasksPortCase{
		{Name: "empty", Manifests: map[string]string{}},
		{Name: "newest-first", Manifests: map[string]string{
			"older.json": `{"task_id":"older","timestamp":"2024-01-01T00:00:00Z","files":[],"new_files":[],"skipped":[]}`,
			"newer.json": `{"task_id":"newer","timestamp":"2025-01-01T00:00:00Z","files":[],"new_files":["new.md"],"skipped":[]}`,
		}},
		{Name: "partial", Manifests: map[string]string{
			"partial.json": `{"task_id":"partial","timestamp":"2025-02-03T04:05:06Z","files":[],"new_files":[],"skipped":["blocked.md"]}`,
		}},
	}
	for _, input := range inputs {
		for _, jsonOutput := range []bool{true, false} {
			input.JSON = jsonOutput
			if jsonOutput {
				input.Name = strings.TrimSuffix(input.Name, "-text")
				input.Name += "-json"
			} else {
				input.Name = strings.TrimSuffix(input.Name, "-json")
				input.Name += "-text"
			}
			caseRoot := filepath.Join(root, input.Name)
			vault := filepath.Join(caseRoot, "vault")
			if err := os.MkdirAll(filepath.Join(vault, ".symdesk", "history", "checkpoints"), 0o700); err != nil {
				t.Fatal(err)
			}
			for name, manifest := range input.Manifests {
				if err := os.WriteFile(filepath.Join(vault, ".symdesk", "history", "checkpoints", name), []byte(manifest), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			home := filepath.Join(caseRoot, "home")
			if err := os.MkdirAll(home, 0o700); err != nil {
				t.Fatal(err)
			}
			args := []string{"--vault", vault, "history", "tasks"}
			if jsonOutput {
				args = append([]string{"--json"}, args...)
			}
			cmd := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
			cmd.Env = append(os.Environ(), "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
				"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
				"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
				"XDG_DATA_HOME="+filepath.Join(home, "data"))
			out, err := cmd.Output()
			input.Stdout = string(out)
			if runtime.GOOS == "windows" && !jsonOutput {
				// Go's Windows Local zone comes from the system, not TZ. Check the
				// local rendering before normalizing the cross-platform oracle.
				for _, manifest := range input.Manifests {
					var checkpoint struct {
						Timestamp time.Time `json:"timestamp"`
					}
					if err := json.Unmarshal([]byte(manifest), &checkpoint); err != nil {
						t.Fatal(err)
					}
					const layout = "2006-01-02 15:04:05"
					local := checkpoint.Timestamp.Local().Format(layout)
					if !strings.Contains(input.Stdout, local) {
						t.Fatalf("history tasks %s did not render local timestamp %s", input.Name, local)
					}
					input.Stdout = strings.ReplaceAll(input.Stdout, local, checkpoint.Timestamp.UTC().Format(layout))
				}
			}
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					input.ExitCode = exit.ExitCode()
					input.Stderr = strings.TrimSpace(string(exit.Stderr))
				} else {
					t.Fatalf("run Go history tasks CLI %s: %v", input.Name, err)
				}
			}
			fixture.Cases = append(fixture.Cases, input)
		}
	}
	return fixture
}
