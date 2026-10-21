package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const grammarProcessContractPath = "testdata/port/cli/symroom-grammar-process.json"

type grammarProcessContract struct {
	SchemaVersion  int                  `json:"schema_version"`
	OracleRevision string               `json:"oracle_revision"`
	Cases          []grammarProcessCase `json:"cases"`
}

type grammarProcessCase struct {
	Name     string   `json:"name"`
	Args     []string `json:"args"`
	ExitCode int      `json:"exit_code"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
}

// TestPortGrammarProcessContract records the shipped Go process result for the
// usage, help and unknown-flag paths of every top-level command and nested
// action in the parser grammar. PORT_GENERATE=1 deliberately writes the fixture.
func TestPortGrammarProcessContract(t *testing.T) {
	root := memberCLIRoot(t)
	doc, err := buildSymRoomGrammar(fixtureoracle.Current())
	if err != nil {
		t.Fatal(err)
	}
	oracle := buildGrammarProcessOracle(t, root)
	fixture := grammarProcessContract{SchemaVersion: 1, OracleRevision: fixtureoracle.Current().Commit}
	for _, args := range grammarProcessVectors(doc) {
		fixture.Cases = append(fixture.Cases, runGrammarProcessCase(t, oracle, args))
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, grammarProcessContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", grammarProcessContractPath)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", grammarProcessContractPath, err)
	}
	if !bytes.Equal(got, data) {
		reportGrammarProcessDrift(t, got, fixture)
		t.Fatalf("Go grammar process fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

// reportGrammarProcessDrift names each case whose live Go result differs from
// the committed fixture, so a platform-only difference is diagnosable from CI.
func reportGrammarProcessDrift(t *testing.T, committed []byte, live grammarProcessContract) {
	t.Helper()
	var want grammarProcessContract
	if err := json.Unmarshal(committed, &want); err != nil {
		t.Logf("committed fixture does not parse: %v", err)
		return
	}
	if len(want.Cases) != len(live.Cases) {
		t.Logf("case count differs: committed %d, live %d", len(want.Cases), len(live.Cases))
	}
	for i := range min(len(want.Cases), len(live.Cases)) {
		w, l := want.Cases[i], live.Cases[i]
		if w.Name != l.Name || w.ExitCode != l.ExitCode || w.Stdout != l.Stdout || w.Stderr != l.Stderr {
			t.Logf("case %q drifts: committed exit=%d stdout=%q stderr=%q; live exit=%d stdout=%q stderr=%q",
				l.Name, w.ExitCode, truncateForDrift(w.Stdout), truncateForDrift(w.Stderr),
				l.ExitCode, truncateForDrift(l.Stdout), truncateForDrift(l.Stderr))
		}
	}
}

func truncateForDrift(s string) string {
	if len(s) > 240 {
		return s[:240] + "..."
	}
	return s
}

// grammarProcessVectors enumerates usage, help and unknown-flag argv for every
// command and action. `identity create` is excluded because it always writes a
// new random identity; its success path is covered by the identity fixtures.
// `run list` without a room is excluded because its missing-file errno text is
// platform-specific and cannot share one fixture across the native matrix.
// `index rebuild` is excluded because it prints the index path with the host
// separator (`.symroom\index.sqlite` on Windows).
func grammarProcessVectors(doc inventory.SymRoomGrammarDocument) [][]string {
	vectors := [][]string{{}}
	for _, sub := range doc.Subcommands {
		vectors = append(vectors,
			[]string{sub.Name},
			[]string{sub.Name, "--help"},
			[]string{sub.Name, "--bogus"},
			[]string{sub.Name, "bogus"},
		)
		for _, action := range sub.Actions {
			if sub.Name == "identity" && action.Name == "create" {
				continue
			}
			if sub.Name == "run" && action.Name == "list" {
				continue
			}
			if sub.Name == "index" && action.Name == "rebuild" {
				continue
			}
			vectors = append(vectors,
				[]string{sub.Name, action.Name},
				[]string{sub.Name, action.Name, "--help"},
				[]string{sub.Name, action.Name, "--bogus"},
			)
		}
	}
	return vectors
}

func buildGrammarProcessOracle(t *testing.T, root string) string {
	t.Helper()
	path := oracleExecutablePath(t, "symroom-go-grammar-oracle")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/symroom") //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom grammar oracle: %v\n%s", err, output)
	}
	return path
}

func runGrammarProcessCase(t *testing.T, oracle string, args []string) grammarProcessCase {
	t.Helper()
	temp := t.TempDir()
	work := filepath.Join(temp, "work")
	home := filepath.Join(temp, "home")
	dataHome := filepath.Join(temp, "data")
	configHome := filepath.Join(temp, "config")
	tmp := filepath.Join(temp, "tmp")
	for _, dir := range []string{work, home, dataHome, configHome, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(oracle, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir = work
	// Windows resolves the home directory from USERPROFILE, so both home
	// variables point at the disposable root on every platform.
	cmd.Env = []string{
		"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "XDG_CONFIG_HOME=" + configHome,
		"TMPDIR=" + tmp, "PATH=" + tmp, "TZ=UTC", "LC_ALL=C", "LANG=C",
	}
	if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+systemRoot)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	exitCode := 0
	if err := cmd.Run(); err != nil {
		exitError, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run Go symroom %s: %v", strings.Join(args, " "), err)
		}
		exitCode = exitError.ExitCode()
	}
	return grammarProcessCase{
		Name:     grammarProcessCaseName(args),
		Args:     args,
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}
}

func grammarProcessCaseName(args []string) string {
	if len(args) == 0 {
		return "no-args"
	}
	return strings.Join(args, " ")
}
