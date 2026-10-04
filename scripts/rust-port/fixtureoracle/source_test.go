package fixtureoracle

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

func TestSourceRequiresTheActualSelectedProductionBytes(t *testing.T) {
	root, first, git := sourceRepository(t)
	none := func(string) (string, bool) { return "", false }
	got, err := source(root, none)
	if err != nil || got.Commit != first {
		t.Fatalf("initial P: %v %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/example/value.go"), []byte("package example\nconst Value = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := source(root, none); err == nil {
		t.Fatal("changed production bytes were accepted under old P")
	}
	git("add", "--", "internal/example/value.go")
	git("commit", "-qm", "new source")
	second := git("rev-parse", "HEAD")
	lookup := func(name string) (string, bool) {
		value, ok := map[string]string{"PORT_GENERATE": "1", CommitEnvironment: second, ReleaseEnvironment: "current-source"}[name]
		return value, ok
	}
	got, err = source(root, lookup)
	if err != nil || got.Commit != second {
		t.Fatalf("explicit replacement P: %v %v", got, err)
	}
	if _, err := source(root, none); err == nil {
		t.Fatal("replay accepted stale central P")
	}
}

func TestSourceRejectsUnexplainedOrNonexistentIdentities(t *testing.T) {
	root, commit, _ := sourceRepository(t)
	for name, environment := range map[string]map[string]string{
		"replay override":  {CommitEnvironment: commit, ReleaseEnvironment: "other"},
		"commit only":      {"PORT_GENERATE": "1", CommitEnvironment: commit},
		"release only":     {"PORT_GENERATE": "1", ReleaseEnvironment: "other"},
		"uppercase commit": {"PORT_GENERATE": "1", CommitEnvironment: "A" + strings.Repeat("0", 39), ReleaseEnvironment: "other"},
		"missing commit":   {"PORT_GENERATE": "1", CommitEnvironment: strings.Repeat("a", 40), ReleaseEnvironment: "other"},
		"blank release":    {"PORT_GENERATE": "1", CommitEnvironment: commit, ReleaseEnvironment: " "},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := source(root, func(key string) (string, bool) { value, ok := environment[key]; return value, ok }); err == nil {
				t.Fatal("invalid source identity accepted")
			}
		})
	}
}

func TestGenerationEnvironmentReplacesAmbientIdentityAliases(t *testing.T) {
	got := GenerationEnvironment([]string{"KEEP=value", CommitEnvironment + "=old", strings.ToLower(ReleaseEnvironment) + "=old"}, inventory.Oracle{Commit: "selected", Release: "release"})
	if strings.Join(got, "\n") != "KEEP=value\n"+CommitEnvironment+"=selected\n"+ReleaseEnvironment+"=release" {
		t.Fatalf("unexpected generation environment: %v", got)
	}
}

func sourceRepository(t *testing.T) (string, string, func(...string) string) {
	t.Helper()
	root := t.TempDir()
	config, cleanup, err := inventory.PrivateGitConfig()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	hooks := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		prefix := []string{"-C", root, "-c", "user.name=Fixture", "-c", "user.email=fixture@localhost", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + hooks}
		//nolint:gosec // Fixed Git command with test-owned paths, arguments, config and empty hooks.
		command := exec.Command("git", append(prefix, args...)...)
		for _, item := range os.Environ() {
			name, _, _ := strings.Cut(item, "=")
			if !strings.HasPrefix(strings.ToUpper(name), "GIT_") {
				command.Env = append(command.Env, item)
			}
		}
		command.Env = append(command.Env, "GIT_CONFIG_GLOBAL="+config, "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-q")
	if err := os.MkdirAll(filepath.Join(root, "internal/example"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal/example/value.go"), []byte("package example\nconst Value = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "--", "internal/example/value.go")
	git("commit", "-qm", "source P")
	commit := git("rev-parse", "HEAD")
	if err := os.MkdirAll(filepath.Join(root, "testdata/port"), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Oracle inventory.Oracle `json:"oracle"`
	}{inventory.Oracle{Commit: commit, Release: "source"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "testdata/port/provenance.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return root, commit, git
}

func TestCurrentUsesCompiledSourceBeforeTestLocalEnvironmentChanges(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())
	oracle := Current()
	if !fullCommit.MatchString(oracle.Commit) || oracle.Release == "" {
		t.Fatalf("compiled source identity lost after isolated cwd/PATH changes: %#v", oracle)
	}
}

func TestCapturedSourceValidatesWhenAWriterFirstRequestsIt(t *testing.T) {
	root, _, _ := sourceRepository(t)
	read := captureSource(root)
	if err := os.WriteFile(filepath.Join(root, "internal/example/value.go"), []byte("package example\nconst Value = 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if _, err := read(); err == nil || !strings.Contains(err.Error(), "differs from selected P") {
		t.Fatalf("first writer did not validate changed production bytes through captured Git: %v", err)
	}
}
