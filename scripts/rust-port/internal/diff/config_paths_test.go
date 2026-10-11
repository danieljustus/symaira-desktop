package diff

import (
	"os"
	"path/filepath"
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
	if got, want := values["XDG_DATA_HOME"], "  "+root+string(os.PathSeparator)+"segment"+string(os.PathSeparator)+".."+string(os.PathSeparator)+"data  "; got != want {
		t.Fatalf("XDG_DATA_HOME = %q, want the unnormalized case value %q", got, want)
	}
	if got, want := values["SYMINGEST_DB_PATH"], filepath.Join(home, "ingest.db"); got != want {
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
	if resolved != filepath.Join(workspace, "vault") {
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
