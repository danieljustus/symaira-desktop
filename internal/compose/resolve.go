package compose

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ResolveOrigin identifies which of the three binary-resolution sources
// produced a Resolve/ResolveWithOrigin match.
type ResolveOrigin string

const (
	// OriginSymairaBinEnv means the binary was found under the directory
	// named by the SYMAIRA_BIN environment variable.
	OriginSymairaBinEnv ResolveOrigin = "symaira_bin_env"
	// OriginManagedRuntime means the binary was found in the default
	// managed-runtime directory, ~/.symaira/bin.
	OriginManagedRuntime ResolveOrigin = "managed_runtime"
	// OriginPath means the binary was found via the process PATH.
	OriginPath ResolveOrigin = "path"
)

// SymairaBinEnvVar is the environment variable that, when set, is checked
// ahead of the default managed-runtime directory in Resolve's search order.
const SymairaBinEnvVar = "SYMAIRA_BIN"

// ManagedRuntimeDir returns the default managed-runtime binary directory,
// ~/.symaira/bin, or "" when the home directory cannot be determined (e.g.
// $HOME is unset in a minimal environment).
func ManagedRuntimeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".symaira", "bin")
}

// ManagedRuntimeDirExists reports whether the managed-runtime directory
// exists on disk. Callers use this to decide whether recommending
// `symbrain setup` (which populates that directory) is genuinely useful
// advice, as opposed to the managed runtime already being present but
// simply missing a particular tool.
func ManagedRuntimeDirExists() bool {
	dir := ManagedRuntimeDir()
	if dir == "" {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// Resolve finds the path to a sibling-tool binary by name, checking in
// order:
//
//  1. $SYMAIRA_BIN/<name>, if the SYMAIRA_BIN environment variable is set.
//  2. ~/.symaira/bin/<name>, the default managed-runtime directory.
//  3. <name> resolved via the process PATH (os/exec.LookPath).
//
// A name that already contains a path separator (for example a test double
// or an operator-supplied absolute path) bypasses the managed-runtime tiers
// and is resolved directly, matching exec.LookPath's own behavior for such
// names.
func Resolve(name string) (string, error) {
	path, _, err := ResolveWithOrigin(name)
	return path, err
}

// ResolveWithOrigin behaves like Resolve but also reports which source
// produced the match, so callers such as `symdesk doctor --json` can
// surface where a tool was actually found.
func ResolveWithOrigin(name string) (string, ResolveOrigin, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		path, err := exec.LookPath(name)
		if err != nil {
			return "", "", err
		}
		return path, OriginPath, nil
	}

	if dir := os.Getenv(SymairaBinEnvVar); dir != "" {
		if path := executableIn(dir, name); path != "" {
			return path, OriginSymairaBinEnv, nil
		}
	}

	if dir := ManagedRuntimeDir(); dir != "" {
		if path := executableIn(dir, name); path != "" {
			return path, OriginManagedRuntime, nil
		}
	}

	path, err := exec.LookPath(name)
	if err != nil {
		return "", "", fmt.Errorf("%s not found via $%s, %s, or PATH: %w", name, SymairaBinEnvVar, managedRuntimeDirOrDefault(), err)
	}
	return path, OriginPath, nil
}

func managedRuntimeDirOrDefault() string {
	if dir := ManagedRuntimeDir(); dir != "" {
		return dir
	}
	return "the managed runtime directory"
}

// executableIn returns the joined path to name inside dir if it exists and
// is a regular, executable file; otherwise "".
func executableIn(dir, name string) string {
	candidate := filepath.Join(dir, name)
	info, err := os.Stat(candidate)
	if err != nil || info.IsDir() {
		return ""
	}
	if info.Mode()&0111 == 0 {
		return ""
	}
	return candidate
}
