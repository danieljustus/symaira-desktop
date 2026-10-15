package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	for _, key := range []string{"XDG_DATA_HOME", "PATH", "home"} {
		environment, err := isolatedEnvForCase(home, tmp, root, root, nil, nil, []string{key}, replacements)
		if err == nil || !strings.Contains(err.Error(), "cannot unset sandbox variable") || environment != nil {
			t.Fatalf("accepted unsafe unset of %q: env=%v err=%v", key, environment, err)
		}
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
	mtimeNS := int64(1_600_000_000_000_000_000)
	for _, setup := range []SetupFile{
		{Path: "home-alias", Kind: "symlink", LinkTarget: "../home"},
		{Path: "absolute-alias", Kind: "symlink", LinkTarget: filepath.Join(workspace, "vault")},
		{Path: "relative-alias", Kind: "symlink", LinkTarget: "absolute-alias"},
		{Path: "absolute-file-alias", Kind: "symlink", LinkTarget: filepath.Join(workspace, "vault", "marker.md")},
		{Path: "relative-file-alias", Kind: "symlink", LinkTarget: "absolute-file-alias"},
		{Path: "slash-file-alias", Kind: "symlink", LinkTarget: "vault/marker.md"},
		{Path: "slash-directory-alias", Kind: "symlink", LinkTarget: "alias/../vault"},
		{Path: "relative-alias/nested", Kind: "directory", Mode: 0o700},
		{Path: "relative-alias/nested/note.md", Content: "through aliases", MTimeNS: &mtimeNS},
		{Path: "relative-alias/child-alias", Kind: "symlink", LinkTarget: "."},
		{Path: "dangling-alias", Kind: "symlink", LinkTarget: "missing/target.toml"},
	} {
		if err := setupSandboxFile(root, home, workspace, setup, replacements); err != nil {
			t.Fatalf("rejected a confined fixture symlink: %v", err)
		}
	}
	if target, err := os.Readlink(filepath.Join(workspace, "absolute-alias")); err != nil || target != filepath.Join(workspace, "vault") {
		t.Fatalf("changed the requested absolute link target: %q, %v", target, err)
	}
	for _, alias := range []string{"absolute-file-alias", "relative-file-alias", "slash-file-alias", "slash-directory-alias/marker.md"} {
		// #nosec G304 -- fixed aliases created above inside this test's t.TempDir.
		if content, err := os.ReadFile(filepath.Join(workspace, alias)); err != nil || string(content) != "fixture" {
			t.Fatalf("file alias %q is not traversable: %q, %v", alias, content, err)
		}
	}
	if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "alias", Kind: "symlink", LinkTarget: "home-alias"}, replacements); err == nil {
		t.Fatal("replaced an existing fixture alias")
	}
	if target, err := os.Readlink(filepath.Join(workspace, "alias")); err != nil || target != "vault" {
		t.Fatalf("failed link creation changed the existing alias: %q, %v", target, err)
	}
	note := filepath.Join(workspace, "relative-alias", "nested", "note.md")
	// #nosec G304 -- fixed aliased fixture under this test's t.TempDir.
	if content, err := os.ReadFile(note); err != nil || string(content) != "through aliases" {
		t.Fatalf("aliased write did not reach its confined target: %q, %v", content, err)
	}
	if info, err := os.Stat(note); err != nil || info.ModTime().UnixNano() != mtimeNS {
		t.Fatalf("aliased write did not preserve its fixture timestamp: %v, %v", info, err)
	}
	if _, err := buildManifest(root); err != nil {
		t.Fatalf("confined fixture links did not retain a valid manifest: %v", err)
	}
	if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "../outside", Content: "bad"}, replacements); err == nil {
		t.Fatal("accepted fixture path outside workspace")
	}
	if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "bad-link", Kind: "symlink", LinkTarget: "../../outside"}, replacements); err == nil {
		t.Fatal("accepted symlink target outside sandbox")
	}
	for _, target := range []string{"", "missing/../vault", "vault/marker.md/child", root + "-other/target"} {
		if err := setupSandboxFile(root, home, workspace, SetupFile{Path: "invalid-link", Kind: "symlink", LinkTarget: target}, replacements); err == nil {
			t.Fatalf("accepted an invalid fixture target: %q", target)
		}
	}
	if strings.Contains(resolved, "outside") {
		t.Fatalf("resolved alias escaped sandbox: %q", resolved)
	}
}

func TestSandboxFixtureSymlinkChainCannotChangeOutsideFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink traversal control; Windows retains its native fixture tests")
	}
	parent := t.TempDir()
	root := filepath.Join(parent, "sandbox")
	home, workspace := filepath.Join(root, "home"), filepath.Join(root, "workspace")
	for _, path := range []string{home, workspace} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(parent, "escape.txt")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	var setupErr error
	for _, setup := range []SetupFile{
		{Path: "a", Kind: "symlink", LinkTarget: "../home"},
		{Path: "b", Kind: "symlink", LinkTarget: "a/../.."},
		{Path: "b/escape.txt", Content: "outside"},
	} {
		if setupErr = setupSandboxFile(root, home, workspace, setup, nil); setupErr != nil {
			break
		}
	}
	content, err := os.ReadFile(outside) // #nosec G304 -- fixed sentinel in this test's t.TempDir, outside only its nested sandbox.
	if err != nil || string(content) != "original" {
		t.Fatalf("fixture setup modified a test-owned file outside its sandbox: %q, %v", content, err)
	}
	if setupErr == nil {
		t.Fatal("accepted a symlink chain escaping the sandbox")
	}
	retained := filepath.Join(workspace, "retained-bad")
	for _, target := range []string{outside, "retained-bad", "missing/../../.."} {
		if err := os.Symlink(target, retained); err != nil {
			t.Fatal(err)
		}
		if _, err := buildManifest(root); err == nil {
			t.Fatalf("accepted an unconfined or cyclic retained symlink: %q", target)
		}
		if err := os.Remove(retained); err != nil {
			t.Fatal(err)
		}
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
	if _, err := RunAt(os.Args[0], caseSpec, filepath.Join(output, "child")); err == nil {
		t.Fatal("accepted an existing file as a sandbox parent")
	}
	after, err := buildManifest(root)
	if err != nil || !reflect.DeepEqual(after, retained) {
		t.Fatalf("rejected run changed original evidence: %v", err)
	}
}

func TestRunAtRetainsFailedSetupAndStartEvidence(t *testing.T) {
	for _, test := range []struct {
		name, want string
		input      Case
	}{
		{"missing-binary", "start ", Case{}},
		{"escaping-setup", "workspace path escapes sandbox", Case{Setup: []SetupFile{{Path: "../escape"}}}},
		{"escaping-cwd", "workspace path escapes sandbox", Case{WorkingDir: "../escape"}},
		{"invalid-environment", "cannot override sandbox variable", Case{Env: map[string]string{"HOME": "unvalidated"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "case")
			_, err := RunAt(filepath.Join(parent, "missing-binary"), test.input, root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wrong failure for retained run: %v", err)
			}
			if info, err := os.Stat(filepath.Join(root, "workspace")); err != nil || !info.IsDir() {
				t.Fatalf("failure removed its sandbox: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "escape")); !os.IsNotExist(err) {
				t.Fatalf("rejected path escaped the workspace: %v", err)
			}
		})
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
