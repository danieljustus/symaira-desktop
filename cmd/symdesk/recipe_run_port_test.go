package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Real-process contract for `symdesk recipe run|diff|accept|reject`. The Go CLI
// runs against a disposable HOME/XDG tree and a loopback shell runner; the
// Rust replay (crates/symdesk-cli/tests/recipe_run.rs) executes the very same
// runner script from the fixture.

// recipeRunPath is fixed: check mode runs with a sanitized environment, and
// the double only needs the POSIX basics.
const recipeRunPath = "/usr/bin:/bin"

const recipeRunFixturePath = "../../testdata/port/ai/recipe-run-process.json"

type recipeRunFixture struct {
	SchemaVersion int             `json:"schema_version"`
	RunnerScript  string          `json:"runner_script"`
	Cases         []recipeRunCase `json:"cases"`
}

type recipeRunCase struct {
	Name     string            `json:"name"`
	Files    map[string]string `json:"files,omitempty"`
	Symlinks map[string]string `json:"symlinks,omitempty"` // vault-relative link -> "$ROOT"-relative target
	Steps    []recipeRunStep   `json:"steps"`
}

type recipeRunStep struct {
	Args []string `json:"args"`
	// Env entries are added to the minimal child environment.
	Env map[string]string `json:"env,omitempty"`
	// Write places files (vault-relative) before the step, after $RUN expansion.
	Write map[string]string `json:"write,omitempty"`
	// NoVault omits the injected --vault flag.
	NoVault bool `json:"no_vault,omitempty"`
	// InexactPrefix marks a step whose Go error embeds encoding/json's own
	// diagnostic text; a replay must match exit status and this prefix only.
	InexactPrefix string `json:"inexact_prefix,omitempty"`
	ExitCode      int    `json:"exit_code"`
	Stdout        string `json:"stdout"`
	Stderr        string `json:"stderr"`
	// Vault is the whole vault tree after the step: files, "dir/" entries and
	// 0o-octal modes. Runner is the runner's own transcript (argv, cwd, request).
	Vault  map[string]string `json:"vault"`
	Runner string            `json:"runner,omitempty"`
}

const recipeRunScript = `#!/bin/sh
# Loopback recipe runner: argv is "recipe run --request FILE --response FILE".
log="$FAKE_RUNNER_LOG"
{
  printf 'argc=%s\n' "$#"
  for a in "$@"; do printf 'arg=%s\n' "$a"; done
  printf 'cwd=%s\n' "$(pwd)"
  printf 'home=%s\n' "$HOME"
  printf 'runner_env=%s\n' "$SYMDESK_RECIPE_RUNNER"
  printf 'request<<\n'
  cat "$4"
  printf '\n>>\n'
} >> "$log"
resp="$6"
case "$FAKE_RUNNER_MODE" in
  ok)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["step one","step <two> & more"],"changes":[{"path":"notes/out.md","content":"# Out <&>\n\nbody\n"},{"path":"deep/new/dir/x.md","content":"x\u00e9\n"}]}
JSON
    ;;
  overwrite)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"existing.md","content":"replaced\n"},{"path":"a/b/c.md","content":"nested\n"}]}
JSON
    ;;
  empty)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":[],"changes":[]}
JSON
    ;;
  nulls)
    cat > "$resp" <<'JSON'
{"contract_version":1}
JSON
    ;;
  extra_fields)
    cat > "$resp" <<'JSON'
{"CONTRACT_VERSION":1,"unknown":{"a":1},"Trace":["t"],"changes":[{"path":"k.md","content":"k","extra":true}]}
JSON
    ;;
  over_cap)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"a.md","content":"a"},{"path":"b.md","content":"b"},{"path":"c.md","content":"c"}]}
JSON
    ;;
  traversal)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"../escape.md","content":"x"}]}
JSON
    ;;
  absolute)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"/etc/passwd","content":"x"}]}
JSON
    ;;
  symlink)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"link/x.md","content":"x"}]}
JSON
    ;;
  duplicate)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"a.md","content":"1"},{"path":"a.md","content":"2"}]}
JSON
    ;;
  blank_path)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"  ","content":"x"}]}
JSON
    ;;
  bad_contract)
    cat > "$resp" <<'JSON'
{"contract_version":2,"trace":["t"],"changes":[]}
JSON
    ;;
  type_mismatch)
    cat > "$resp" <<'JSON'
{"contract_version":"1","trace":[],"changes":[]}
JSON
    ;;
  syntax)
    cat > "$resp" <<'JSON'
{"contract_version":x}
JSON
    ;;
  malformed)
    printf '{"contract_version":' > "$resp" ;;
  no_response)
    : ;;
  fail)
    printf 'out-line\n'; printf 'err-line\n' >&2; exit 3 ;;
  atomic)
    cat > "$resp" <<'JSON'
{"contract_version":1,"trace":["t"],"changes":[{"path":"existing.md","content":"new existing\n"},{"path":"fresh.md","content":"fresh\n"},{"path":"blocked","content":"x"}]}
JSON
    ;;
  *)
    printf 'unknown mode %s\n' "$FAKE_RUNNER_MODE" >&2; exit 9 ;;
esac
`

const recipeRunYAML = "version: 1\nname: Daily Digest/ü\ntriggers: [manual, save]\ntools: [desk_search]\nwrite_cap: 2\n"

var (
	recipeRunIDPattern   = regexp.MustCompile(`[0-9]{16,19}-[A-Za-z0-9-]+`)
	recipeRunTimePattern = regexp.MustCompile(`"created": "[^"]*"`)
	recipeRunTempPattern = regexp.MustCompile(`\.symdesk-proposal-[0-9]+`)
)

func recipeRunCases() []recipeRunCase {
	run := func(mode string, extra ...string) recipeRunStep {
		args := append([]string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, extra...)
		return recipeRunStep{Args: args, Env: map[string]string{"FAKE_RUNNER_MODE": mode}}
	}
	runInexact := func(mode string) recipeRunStep {
		st := run(mode)
		st.InexactPrefix = `{"error":"parse runner response: `
		return st
	}
	step := func(args ...string) recipeRunStep { return recipeRunStep{Args: args} }
	base := map[string]string{"recipe.yml": recipeRunYAML}
	cases := []recipeRunCase{
		{Name: "ok_json_flow", Files: base, Steps: []recipeRunStep{
			run("ok"),
			step("--json", "recipe", "diff", "$RUN"),
			step("recipe", "diff", "$RUN"),
			step("--json", "recipe", "accept", "$RUN"),
			step("--json", "recipe", "accept", "$RUN"),
			step("--json", "recipe", "reject", "$RUN"),
			step("--json", "recipe", "diff", "$RUN"),
		}},
		{Name: "ok_text_flow", Files: base, Steps: []recipeRunStep{
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "ok"}},
			step("recipe", "reject", "$RUN"),
			step("recipe", "accept", "$RUN"),
			step("recipe", "reject", "$RUN"),
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml", "--trigger", "save"}, Env: map[string]string{"FAKE_RUNNER_MODE": "ok"}},
			step("recipe", "accept", "$RUN"),
			step("recipe", "accept", "$RUN"),
		}},
		{Name: "accept_overwrites_and_nests", Files: map[string]string{"recipe.yml": recipeRunYAML}, Steps: []recipeRunStep{
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "overwrite"}, Write: map[string]string{"existing.md": "original\n"}},
			step("--json", "recipe", "accept", "$RUN"),
		}},
		{Name: "empty_and_null_changes", Files: base, Steps: []recipeRunStep{
			run("empty"),
			step("--json", "recipe", "diff", "$RUN"),
			step("recipe", "diff", "$RUN"),
			step("--json", "recipe", "accept", "$RUN"),
			run("nulls"),
			step("--json", "recipe", "diff", "$RUN"),
			step("recipe", "diff", "$RUN"),
			run("extra_fields"),
			step("--json", "recipe", "accept", "$RUN"),
		}},
		{Name: "response_validation", Files: base, Symlinks: map[string]string{"link": "outside"}, Steps: []recipeRunStep{
			run("over_cap"), run("traversal"), run("absolute"), step("--json", "recipe", "accept", "$RUN"), run("symlink"),
			run("duplicate"), run("blank_path"), run("bad_contract"),
			run("malformed"), runInexact("type_mismatch"), runInexact("syntax"), run("no_response"), run("fail"), run("unknown_mode_exits_nine"),
		}},
		{Name: "configuration_errors", Files: map[string]string{"recipe.yml": recipeRunYAML, "bad.yml": "version: 2\nname: x\ntriggers: [manual]\ntools: [t]\nwrite_cap: 1\n"}, Steps: []recipeRunStep{
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"SYMDESK_RECIPE_RUNNER": ""}},
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"SYMDESK_RECIPE_RUNNER": ""}},
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"SYMDESK_RECIPE_RUNNER": "no-such-runner-xyz"}},
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"SYMDESK_RECIPE_RUNNER": "$ROOT/missing/runner"}},
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml", "--trigger", "commit"}, Env: map[string]string{"FAKE_RUNNER_MODE": "ok"}},
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml", "--trigger", "bogus"}, Env: map[string]string{"FAKE_RUNNER_MODE": "ok"}},
			{Args: []string{"recipe", "run", "$ROOT/recipe.yml", "--trigger", ""}, Env: map[string]string{"FAKE_RUNNER_MODE": "ok"}},
			{Args: []string{"recipe", "run", "$ROOT/bad.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "ok"}},
			{Args: []string{"recipe", "run", "$ROOT/absent.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "ok"}},
			{Args: []string{"recipe", "run"}},
			{Args: []string{"recipe", "run", "a", "b"}},
			{Args: []string{"recipe", "diff"}},
			{Args: []string{"recipe", "accept", "nope"}},
			{Args: []string{"--json", "recipe", "reject", "nope"}},
			{Args: []string{"--json", "recipe", "diff", "nope"}},
		}},
		{Name: "vault_resolution", Files: base, Steps: []recipeRunStep{
			{Args: []string{"recipe", "diff", "x"}, NoVault: true},
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, NoVault: true, Env: map[string]string{"FAKE_RUNNER_MODE": "empty"}},
			{Args: []string{"--vault", "$ROOT/nope", "recipe", "diff", "x"}, NoVault: true},
			{Args: []string{"--vault", "$ROOT/recipe.yml", "recipe", "diff", "x"}, NoVault: true},
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, NoVault: true, Env: map[string]string{"FAKE_RUNNER_MODE": "empty", "SYMDESK_VAULT": "$ROOT/vault"}},
			{Args: []string{"--json", "recipe", "diff", "$RUN"}, NoVault: true, Env: map[string]string{"SYMDESK_VAULT": "$ROOT/vault"}},
		}},
		{Name: "runner_resolution_tiers", Files: base, Steps: []recipeRunStep{
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "empty", "SYMDESK_RECIPE_RUNNER": "fake-runner", "SYMAIRA_BIN": "$ROOT/bin"}},
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "empty", "SYMDESK_RECIPE_RUNNER": "fake-runner", "PATH": "$ROOT/bin:$PATH"}},
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "empty", "SYMDESK_RECIPE_RUNNER": "fake-runner", "SYMAIRA_BIN": "$ROOT/missing"}},
		}},
		{Name: "accept_is_atomic", Files: map[string]string{"recipe.yml": strings.Replace(recipeRunYAML, "write_cap: 2", "write_cap: 5", 1)}, Steps: []recipeRunStep{
			{Args: []string{"--json", "recipe", "run", "$ROOT/recipe.yml"}, Env: map[string]string{"FAKE_RUNNER_MODE": "atomic"}, Write: map[string]string{"existing.md": "original\n", "blocked/keep.md": "keep\n"}},
			step("--json", "recipe", "accept", "$RUN"),
			step("recipe", "accept", "$RUN"),
			step("--json", "recipe", "reject", "$RUN"),
			step("--json", "recipe", "accept", "$RUN"),
		}},
		{Name: "accept_revalidates_manifest", Files: base, Steps: []recipeRunStep{
			run("ok"),
			{Args: []string{"--json", "recipe", "accept", "$RUN"}, Write: map[string]string{".symdesk/runs/$RUN/manifest.json": `{"request":{"contract_version":1,"run_id":"$RUN","vault":"v","recipe":{"version":1,"name":"n","triggers":["manual"],"tools":["t"],"write_cap":1},"trigger":"manual"},"response":{"contract_version":1,"trace":null,"changes":[{"path":"../../x.md","content":"x"}]},"status":"pending","created":"2026-01-02T03:04:05Z"}`}},
			{Args: []string{"--json", "recipe", "accept", "$RUN"}, Write: map[string]string{".symdesk/runs/$RUN/manifest.json": `{"request":{"contract_version":1,"run_id":"$RUN","vault":"v","recipe":{"version":1,"name":"n","triggers":["manual"],"tools":["t"],"write_cap":0},"trigger":"manual"},"response":{"contract_version":1,"trace":null,"changes":[{"path":"a.md","content":"x"}]},"status":"pending","created":"2026-01-02T03:04:05Z"}`}},
			{Args: []string{"--json", "recipe", "accept", "$RUN"}, Write: map[string]string{".symdesk/runs/$RUN/manifest.json": `{"request":{},"response":{},"status":"weird"}`}},
			{Args: []string{"--json", "recipe", "diff", "$RUN"}, InexactPrefix: `{"error":"`, Write: map[string]string{".symdesk/runs/$RUN/manifest.json": `not json`}},
			{Args: []string{"--json", "recipe", "reject", "$RUN"}, Write: map[string]string{".symdesk/runs/$RUN/manifest.json": `{"request":{},"response":{},"status":"pending"}`}},
			{Args: []string{"--json", "recipe", "diff", "$RUN"}},
		}},
	}
	return cases
}

func TestPortRecipeRunProcessContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell runner fixture is Unix-only")
	}
	fixture := observeRecipeRunCLI(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), recipeRunFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // fixed repository fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		portProcessFixtureDifference(t, "recipe run fixture is stale; regenerate with make recipe-run-fixtures-generate", current, encoded)
	}
}

func observeRecipeRunCLI(t *testing.T) recipeRunFixture {
	t.Helper()
	binary := processFixtureBinary(t)
	fixture := recipeRunFixture{SchemaVersion: 1, RunnerScript: recipeRunScript}
	for _, c := range recipeRunCases() {
		fixture.Cases = append(fixture.Cases, observeRecipeRunCase(t, binary, c))
	}
	return fixture
}

func observeRecipeRunCase(t *testing.T, binary string, c recipeRunCase) recipeRunCase {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	vault := filepath.Join(root, "vault")
	logDir := filepath.Join(root, "runner-log")
	for _, dir := range []string{home, vault, logDir, filepath.Join(root, "bin"), filepath.Join(root, "outside")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runner := filepath.Join(root, "bin", "fake-runner")
	if err := os.WriteFile(runner, []byte(recipeRunScript), 0o700); err != nil { //nolint:gosec // executable test double in the test-owned root
		t.Fatal(err)
	}
	for name, content := range c.Files {
		dest := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dest, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range c.Symlinks {
		if err := os.Symlink(filepath.Join(root, target), filepath.Join(vault, link)); err != nil {
			t.Fatal(err)
		}
	}
	expand := func(s, runID string) string {
		s = strings.ReplaceAll(s, "$ROOT", root)
		return strings.ReplaceAll(s, "$RUN", runID)
	}
	var ids []string
	normalize := func(s string) string {
		for i, id := range ids {
			s = strings.ReplaceAll(s, id, fmt.Sprintf("<RUN%d>", i+1))
		}
		s = strings.ReplaceAll(s, root, "<ROOT>")
		s = recipeRunTimePattern.ReplaceAllString(s, `"created": "<TIME>"`)
		return recipeRunTempPattern.ReplaceAllString(s, ".symdesk-proposal-<N>")
	}
	// Run directories are discovered in name order (UnixNano prefix, so
	// chronological); each distinct id gets a stable ordinal token.
	discover := func() {
		entries, err := os.ReadDir(filepath.Join(vault, ".symdesk", "runs"))
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() || !recipeRunIDPattern.MatchString(entry.Name()) {
				continue
			}
			known := false
			for _, id := range ids {
				known = known || id == entry.Name()
			}
			if !known {
				ids = append(ids, entry.Name())
			}
		}
	}
	out := recipeRunCase{Name: c.Name, Files: c.Files, Symlinks: c.Symlinks}
	var runID string
	for index, st := range c.Steps {
		for name, content := range st.Write {
			dest := filepath.Join(vault, expand(name, runID))
			if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dest, []byte(expand(content, runID)), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{"--vault", vault}
		if st.NoVault {
			args = nil
		}
		for _, a := range st.Args {
			args = append(args, expand(a, runID))
		}
		cmd := exec.Command(binary, args...) //nolint:gosec // test-built CLI with controlled arguments
		cmd.Dir = root
		env := []string{
			"PATH=" + recipeRunPath, "HOME=" + home, "TZ=UTC",
			"XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
			"XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
			"XDG_DATA_HOME=" + filepath.Join(home, "data"),
			"SYMDESK_RECIPE_RUNNER=" + runner,
			"FAKE_RUNNER_LOG=" + filepath.Join(logDir, "runner.log"),
		}
		for k, v := range st.Env {
			v = strings.ReplaceAll(expand(v, runID), "$PATH", recipeRunPath)
			env = append(env, k+"="+v)
		}
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		exitCode := 0
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("%s step %d: %v", c.Name, index, err)
			}
			exitCode = exit.ExitCode()
		}
		discover()
		if strings.Contains(stdout.String(), "run_id") {
			if id := recipeRunIDPattern.FindString(stdout.String()); id != "" {
				runID = id
			}
		}
		rec := recipeRunStep{Args: st.Args, Env: st.Env, Write: st.Write, NoVault: st.NoVault, InexactPrefix: st.InexactPrefix, ExitCode: exitCode}
		rec.Stdout = normalize(stdout.String())
		rec.Stderr = normalize(stderr.String())
		rec.Vault = recipeRunSnapshot(t, vault, normalize)
		if b, err := os.ReadFile(filepath.Join(logDir, "runner.log")); err == nil { //nolint:gosec // test-owned log
			rec.Runner = normalize(string(b))
			_ = os.Remove(filepath.Join(logDir, "runner.log"))
		}
		out.Steps = append(out.Steps, rec)
	}
	return out
}

func recipeRunSnapshot(t *testing.T, vault string, normalize func(string) string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.WalkDir(vault, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(vault, path)
		if err != nil || rel == "." {
			return err
		}
		key := normalize(filepath.ToSlash(rel))
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot[key] = "symlink:" + normalize(target)
		case entry.IsDir():
			snapshot[key+"/"] = ""
		default:
			body, err := os.ReadFile(path) //nolint:gosec // test-owned vault
			if err != nil {
				return err
			}
			snapshot[key] = "mode:" + info.Mode().Perm().String() + "\n" + normalize(string(body))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(snapshot))
	for k := range snapshot {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return snapshot
}
