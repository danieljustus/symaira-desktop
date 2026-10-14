package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCaseSandboxEnvironmentAllowsConfinedPathsAndUnsetsHome(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	tmp := filepath.Join(root, "tmp")
	replacements := map[string]string{
		"${SANDBOX}":   root,
		"${HOME}":      home,
		"${WORKSPACE}": workspace,
		"${TMPDIR}":    tmp,
	}
	environment, err := isolatedEnvForCase(
		home,
		tmp,
		filepath.Join(root, "runtime"),
		filepath.Join(home, ".local", "state"),
		nil,
		map[string]string{
			"XDG_DATA_HOME":             "  ${SANDBOX}/segment/../data  ",
			"SYMINGEST_DB_PATH":         "${HOME}/ingest.db",
			"SYMINGEST_SYMSEEK_ENABLED": "maybe",
		},
		[]string{"HOME", "USERPROFILE"},
		replacements,
	)
	if err != nil {
		t.Fatalf("create confined environment: %v", err)
	}
	values := environmentMap(environment)
	if _, exists := values["HOME"]; exists {
		t.Fatal("HOME remained set after requested unset")
	}
	if _, exists := values["USERPROFILE"]; exists {
		t.Fatal("USERPROFILE remained set after requested unset")
	}
	if got, want := values["XDG_DATA_HOME"], "  "+root+"/segment/../data  "; got != want {
		t.Fatalf("XDG_DATA_HOME = %q, want the unnormalized case value %q", got, want)
	}
	if got, want := values["SYMINGEST_DB_PATH"], home+"/ingest.db"; got != want {
		t.Fatalf("SYMINGEST_DB_PATH = %q, want %q", got, want)
	}
	if got := values["SYMINGEST_SYMSEEK_ENABLED"]; got != "maybe" {
		t.Fatalf("non-path test environment value = %q, want maybe", got)
	}
}

func TestCaseSandboxEnvironmentRejectsEscapesAndUnvalidatedPathOverrides(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	tmp := filepath.Join(root, "tmp")
	replacements := map[string]string{
		"${SANDBOX}":   root,
		"${HOME}":      home,
		"${WORKSPACE}": workspace,
		"${TMPDIR}":    tmp,
	}
	base := func(sandbox, extra map[string]string) error {
		_, err := isolatedEnvForCase(home, tmp, filepath.Join(root, "runtime"), filepath.Join(home, ".local", "state"), extra, sandbox, nil, replacements)
		return err
	}
	if err := base(map[string]string{"XDG_DATA_HOME": "${SANDBOX}/../../outside"}, nil); err == nil {
		t.Fatal("accepted an XDG path that escapes the sandbox")
	}
	if err := base(nil, map[string]string{"SYMINGEST_DB_PATH": "${SANDBOX}/outside.db"}); err == nil {
		t.Fatal("accepted an unvalidated path override through the general environment")
	}
	if err := base(nil, map[string]string{"HOME": "${SANDBOX}/other-home"}); err == nil {
		t.Fatal("accepted a reserved HOME override through the general environment")
	}
	if err := base(map[string]string{"UNKNOWN_TEST_SETTING": "value"}, nil); err == nil {
		t.Fatal("accepted an unsupported sandbox environment variable")
	}
}

func TestSandboxFixtureSymlinkAndPathContainment(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	replacements := map[string]string{
		"${SANDBOX}":   root,
		"${HOME}":      home,
		"${WORKSPACE}": workspace,
		"${TMPDIR}":    filepath.Join(root, "tmp"),
	}
	if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "vault/marker.md", Content: "fixture"}, replacements); err != nil {
		t.Fatalf("create target file: %v", err)
	}
	if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "alias", Kind: "symlink", LinkTarget: "vault"}, replacements); err != nil {
		t.Fatalf("create contained symlink: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(workspace, "alias"))
	if err != nil {
		t.Fatal(err)
	}
	resolvedInfo, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	targetInfo, err := os.Stat(filepath.Join(workspace, "vault"))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(resolvedInfo, targetInfo) {
		t.Fatalf("symlink resolved to %q", resolved)
	}
	if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "../outside", Content: "bad"}, replacements); err == nil {
		t.Fatal("accepted fixture path outside workspace")
	}
	if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "bad-link", Kind: "symlink", LinkTarget: "../../outside"}, replacements); err == nil {
		t.Fatal("accepted symlink target outside sandbox")
	}
	if strings.Contains(resolved, "outside") {
		t.Fatalf("resolved alias escaped sandbox: %q", resolved)
	}
}

func TestIsolatedEnvironmentKeepsRawBase64Bytes(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	tmp := filepath.Join(root, "tmp")
	runtimeDir := filepath.Join(root, "runtime")
	replacements := map[string]string{
		"${SANDBOX}": root,
		"${HOME}":    home,
		"${TMPDIR}":  tmp,
	}
	environment, err := isolatedEnvForCaseWithBase64(
		home,
		tmp,
		runtimeDir,
		filepath.Join(home, ".local", "state"),
		nil,
		nil,
		nil,
		map[string]string{"UNRELATED_BINARY_ENV": "/w=="},
		replacements,
	)
	if err != nil {
		t.Fatalf("create environment with raw bytes: %v", err)
	}
	value, ok := environmentMap(environment)["UNRELATED_BINARY_ENV"]
	if !ok || len(value) != 1 || value[0] != 0xff {
		t.Fatalf("raw environment bytes = %v, want [255]", []byte(value))
	}
}

func TestRunAtRetainsEvidenceAndRejectsExistingRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "retained", "case")
	caseSpec := Case{
		ID:   "retained-helper",
		Args: []string{"-test.run=^TestRunAndCompareIdenticalHelper$"},
		Env:  map[string]string{"SYMDESK_PORT_HELPER": "1", "PORT_OUTPUT": "${WORKSPACE}/out.txt"},
	}
	if _, err := RunAt(os.Args[0], caseSpec, "relative-root"); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative root was not rejected: %v", err)
	}
	result, err := RunAt(os.Args[0], caseSpec, root)
	if err != nil {
		t.Fatal(err)
	}
	if result.SandboxRoot != root || result.ExitCode != 0 || result.TimedOut || result.Signal != "" || !strings.Contains(string(result.Stdout), "ok\n") {
		t.Fatalf("unexpected retained process result: %#v", result)
	}
	output := filepath.Join(root, "workspace", "out.txt")
	content, err := os.ReadFile(output) // #nosec G304 -- fixed helper output under the test-owned sandbox.
	if err != nil || string(content) != "deterministic\n" {
		t.Fatalf("helper output not retained: %q, %v", content, err)
	}
	retained, err := buildManifest(root)
	if err != nil || !reflect.DeepEqual(retained, result.Files) || reflect.DeepEqual(result.FilesBefore, result.Files) {
		t.Fatalf("before/after evidence does not match retained files: %v", err)
	}
	if _, err := RunAt(os.Args[0], caseSpec, root); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing root was not rejected: %v", err)
	}
	if _, err := RunAt(os.Args[0], caseSpec, output); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing file was not rejected: %v", err)
	}
	after, err := buildManifest(root)
	if err != nil || !reflect.DeepEqual(after, retained) {
		t.Fatalf("rejected run changed original evidence: %v", err)
	}
}

func TestRawEnvironmentRejectsInvalidAndConflictingInputs(t *testing.T) {
	root := t.TempDir()
	home, tmp := filepath.Join(root, "home"), filepath.Join(root, "tmp")
	for _, test := range []struct {
		name, key, value, want string
	}{
		{"empty key", "", "eA==", "non-empty canonical uppercase"},
		{"lowercase key", "lower", "eA==", "non-empty canonical uppercase"},
		{"equals in key", "BAD=KEY", "eA==", "non-empty canonical uppercase"},
		{"nul in key", "BAD\x00KEY", "eA==", "non-empty canonical uppercase"},
		{"reserved", "HOME", "eA==", "cannot override sandbox variable"},
		{"path override", "SYMINGEST_DB_PATH", "eA==", "must use sandbox_env"},
		{"collision", "NO_COLOR", "eA==", "collides with an existing variable"},
		{"malformed base64", "RAW_TEST", "!", "canonical standard base64"},
		{"noncanonical base64", "RAW_TEST", "eA==\n", "canonical standard base64"},
		{"nul in value", "RAW_TEST", "AA==", "contains a NUL byte"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment, err := isolatedEnvForCaseWithBase64(home, tmp, root, root, nil, nil, nil,
				map[string]string{test.key: test.value}, map[string]string{"${SANDBOX}": root})
			if err == nil || !strings.Contains(err.Error(), test.want) || environment != nil {
				t.Fatalf("invalid raw environment was not rejected: env=%v err=%v", environment, err)
			}
		})
	}
}

func environmentMap(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, pair := range values {
		name, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		result[name] = value
	}
	return result
}
