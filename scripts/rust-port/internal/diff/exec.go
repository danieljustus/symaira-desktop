package diff

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Result is the complete observable result of one isolated process run.
type Result struct {
	ExitCode    int
	Signal      string
	TimedOut    bool
	Stdout      []byte
	Stderr      []byte
	FilesBefore []ManifestEntry
	Files       []ManifestEntry
	SandboxRoot string
}

// Run executes binary in a fresh HOME/XDG/workspace sandbox.
func Run(binary string, testCase Case) (Result, error) {
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		return Result{}, fmt.Errorf("resolve binary: %w", err)
	}
	root, err := os.MkdirTemp("", "symdesk-port-")
	if err != nil {
		return Result{}, fmt.Errorf("create sandbox: %w", err)
	}
	defer removeSandbox(root)
	return runInRoot(absoluteBinary, testCase, root)
}

// RunAt executes in an explicitly owned absolute root and leaves that root in
// place so a caller can retain or inspect its before/after evidence.
func RunAt(binary string, testCase Case, root string) (Result, error) {
	absoluteBinary, err := filepath.Abs(binary)
	if err != nil {
		return Result{}, fmt.Errorf("resolve binary: %w", err)
	}
	if !filepath.IsAbs(root) {
		return Result{}, fmt.Errorf("sandbox root must be absolute: %q", root)
	}
	if _, err := os.Lstat(root); err == nil {
		return Result{}, fmt.Errorf("sandbox root already exists: %s", root)
	} else if !os.IsNotExist(err) {
		return Result{}, fmt.Errorf("inspect sandbox root: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return Result{}, fmt.Errorf("create sandbox parent: %w", err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return Result{}, fmt.Errorf("create sandbox root: %w", err)
	}
	return runInRoot(absoluteBinary, testCase, root)
}

func runInRoot(absoluteBinary string, testCase Case, root string) (Result, error) {
	home := filepath.Join(root, "home")
	workspace := filepath.Join(root, "workspace")
	tmp := filepath.Join(root, "tmp")
	runtimeDir := filepath.Join(root, "runtime")
	state := filepath.Join(home, ".local", "state")
	for _, dir := range []string{home, workspace, tmp, runtimeDir, state} {
		if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
			return Result{}, fmt.Errorf("create sandbox directory: %w", mkdirErr)
		}
	}
	replacements := map[string]string{
		"${SANDBOX}":   root,
		"${HOME}":      home,
		"${WORKSPACE}": workspace,
		"${TMPDIR}":    tmp,
	}
	for _, setup := range testCase.Setup {
		if err := setupSandboxFile(root, home, workspace, setup, replacements); err != nil {
			return Result{}, err
		}
	}
	filesBefore, err := buildManifest(root)
	if err != nil {
		return Result{}, fmt.Errorf("manifest sandbox before run: %w", err)
	}

	args := replaceAll(testCase.Args, replacements)
	command := exec.Command(absoluteBinary, args...) // #nosec G204,G702 -- explicit harness operand, never derived from fixture output
	configureProcessTree(command)
	command.Dir = workspace
	if testCase.WorkingDir != "" {
		command.Dir, err = safeWorkspacePath(workspace, replace(testCase.WorkingDir, replacements))
		if err != nil {
			return Result{}, err
		}
	}
	command.Env, err = isolatedEnvForCase(
		home,
		tmp,
		runtimeDir,
		state,
		testCase.Env,
		testCase.SandboxEnv,
		testCase.UnsetSandboxEnv,
		replacements,
	)
	if err != nil {
		return Result{}, err
	}
	if len(testCase.PrepareArgs) > 0 {
		prepare := exec.Command(absoluteBinary, replaceAll(testCase.PrepareArgs, replacements)...) // #nosec G204,G702 -- explicit harness operand
		configureProcessTree(prepare)
		prepare.Dir = command.Dir
		prepare.Env = command.Env
		prepare.Stdout = io.Discard
		prepare.Stderr = io.Discard
		if prepareErr := runPrepare(prepare, testCase.timeout()); prepareErr != nil {
			return Result{}, fmt.Errorf("prepare process: %w", prepareErr)
		}
	}
	command.Stdin = strings.NewReader(replace(testCase.Stdin, replacements))
	stdout := newLimitedBuffer()
	stderr := newLimitedBuffer()
	command.Stdout = stdout
	command.Stderr = stderr

	if startErr := command.Start(); startErr != nil {
		return Result{}, fmt.Errorf("start %s: %w", absoluteBinary, startErr)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()

	var waitErr error
	timedOut := false
	timer := time.NewTimer(testCase.timeout())
	select {
	case waitErr = <-waitDone:
		timer.Stop()
	case <-timer.C:
		timedOut = true
		killErr := killProcessTree(command)
		select {
		case waitErr = <-waitDone:
		case <-time.After(2 * time.Second):
			_ = command.Process.Kill()
			return Result{}, fmt.Errorf("process did not exit within 2s after timeout")
		}
		if killErr != nil {
			return Result{}, fmt.Errorf("terminate timed-out process tree: %w", killErr)
		}
	}

	exitCode := 0
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return Result{}, fmt.Errorf("wait for process: %w", waitErr)
		}
	}
	if stdout.Truncated() || stderr.Truncated() {
		return Result{}, fmt.Errorf("captured process output exceeded %d bytes per stream", maxCapturedStreamBytes)
	}
	signal := terminationSignal(command.ProcessState)
	files, err := buildManifest(root)
	if err != nil {
		return Result{}, fmt.Errorf("manifest sandbox: %w", err)
	}
	return Result{
		ExitCode:    exitCode,
		Signal:      signal,
		TimedOut:    timedOut,
		Stdout:      append([]byte(nil), stdout.Bytes()...),
		Stderr:      append([]byte(nil), stderr.Bytes()...),
		FilesBefore: filesBefore,
		Files:       files,
		SandboxRoot: root,
	}, nil
}

func setupSandboxFile(root, home, workspace string, setup SetupFile, replacements map[string]string) error {
	base := workspace
	switch setup.Base {
	case "", "workspace":
	case "home":
		base = home
	case "sandbox":
		base = root
	default:
		return fmt.Errorf("unsupported setup base %q", setup.Base)
	}
	path, err := safeWorkspacePath(base, replace(setup.Path, replacements))
	if err != nil {
		return err
	}
	mode := os.FileMode(setup.Mode)
	if mode == 0 {
		mode = 0o600
	}
	kind := setup.Kind
	if kind == "" {
		kind = "file"
	}
	switch kind {
	case "directory":
		if err := os.MkdirAll(path, mode); err != nil {
			return err
		}
	case "symlink":
		target := replace(setup.LinkTarget, replacements)
		if target == "" {
			return errors.New("symlink fixture requires link_target")
		}
		resolvedTarget := target
		if !filepath.IsAbs(resolvedTarget) {
			resolvedTarget = filepath.Join(filepath.Dir(path), resolvedTarget)
		}
		resolvedTarget = filepath.Clean(resolvedTarget)
		relative, err := filepath.Rel(root, resolvedTarget)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("symlink fixture escapes sandbox: %q", setup.LinkTarget)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.Symlink(target, path); err != nil {
			return err
		}
	case "file":
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(replace(setup.Content, replacements)), mode); err != nil {
			return err
		}
		if setup.MTimeNS != nil {
			mtime := time.Unix(0, *setup.MTimeNS)
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				return fmt.Errorf("set fixture mtime: %w", err)
			}
		}
	default:
		return fmt.Errorf("unsupported setup kind %q", kind)
	}
	return nil
}

func runPrepare(command *exec.Cmd, timeout time.Duration) error {
	if err := command.Start(); err != nil {
		return err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- command.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waitDone:
		return err
	case <-timer.C:
		killErr := killProcessTree(command)
		select {
		case <-waitDone:
		case <-time.After(2 * time.Second):
			_ = command.Process.Kill()
			select {
			case <-waitDone:
			case <-time.After(2 * time.Second):
				return errors.New("prepare process did not exit within 2s after final kill")
			}
			return errors.New("prepare process required direct kill after tree timeout")
		}
		if killErr != nil {
			return fmt.Errorf("terminate timed-out prepare process tree: %w", killErr)
		}
		return errors.New("prepare process timed out")
	}
}

func isolatedEnv(home, tmp, runtimeDir, state string, extra map[string]string, replacements map[string]string) ([]string, error) {
	return isolatedEnvForCase(home, tmp, runtimeDir, state, extra, nil, nil, replacements)
}

func isolatedEnvForCase(
	home, tmp, runtimeDir, state string,
	extra, sandboxExtra map[string]string,
	unsetSandbox []string,
	replacements map[string]string,
) ([]string, error) {
	values := map[string]string{
		"HOME":                 home,
		"USERPROFILE":          home,
		"XDG_CONFIG_HOME":      filepath.Join(home, ".config"),
		"XDG_DATA_HOME":        filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":       filepath.Join(home, ".cache"),
		"XDG_STATE_HOME":       state,
		"XDG_RUNTIME_DIR":      runtimeDir,
		"TMPDIR":               tmp,
		"TMP":                  tmp,
		"TEMP":                 tmp,
		"LANG":                 "C",
		"LC_ALL":               "C",
		"TZ":                   "UTC",
		"TERM":                 "dumb",
		"NO_COLOR":             "1",
		"SYMDESK_VAULT":        "",
		"SYMDESK_SIDECAR":      "",
		"SYMROOM_IDENTITY_KEY": "",
		"SYMROOM_ROOM_DIR":     "",
	}
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT"} {
		if value, ok := lookupEnvFold(key); ok {
			values[key] = value
		}
	}
	sandboxRoot := replacements["${SANDBOX}"]
	workspace := replacements["${WORKSPACE}"]
	for key, raw := range sandboxExtra {
		if key != strings.ToUpper(key) || !allowedSandboxEnv(key) {
			return nil, fmt.Errorf("case sandbox environment cannot set %q", key)
		}
		value := replace(raw, replacements)
		if isSandboxPathEnv(key) && value != "" && !pathWithinSandbox(sandboxRoot, workspace, value) {
			return nil, fmt.Errorf("case path environment %q escapes sandbox", key)
		}
		values[key] = value
	}
	for _, key := range unsetSandbox {
		if key != strings.ToUpper(key) || (key != "HOME" && key != "USERPROFILE") {
			return nil, fmt.Errorf("case cannot unset sandbox variable %q", key)
		}
		delete(values, key)
	}

	keys := make([]string, 0, len(extra))
	for key := range extra {
		if key != strings.ToUpper(key) {
			return nil, fmt.Errorf("case environment key must use its canonical uppercase spelling: %q", key)
		}
		if reservedSandboxEnv(key) {
			return nil, fmt.Errorf("case environment cannot override sandbox variable %q", key)
		}
		if isSandboxPathEnv(key) {
			return nil, fmt.Errorf("case path environment %q must use sandbox_env", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values[key] = replace(extra[key], replacements)
	}
	keys = keys[:0]
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env, nil
}

func allowedSandboxEnv(key string) bool {
	switch key {
	case "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP",
		"SYMDESK_VAULT", "SYMDESK_SIDECAR", "SYMRELATE_CONFIG_HOME", "SYMRELATE_DATA_HOME", "SYMRELATE_CACHE_HOME",
		"SYMINGEST_VAULT", "SYMINGEST_OCR_LANG", "SYMINGEST_DB_PATH", "SYMINGEST_ARCHIVE_PATH", "SYMINGEST_INBOX",
		"SYMINGEST_PAPERLESS_BASE_URL", "SYMINGEST_SYMSEEK_ENABLED", "SYMINGEST_SYMSEEK_BINARY", "SYMINGEST_IMAP_ACCOUNTS",
		"SYMINGEST_IMAP_POLL_INTERVAL", "SYMINGEST_OLLAMA_BASE_URL", "SYMINGEST_OLLAMA_MODEL":
		return true
	default:
		return false
	}
}

func isSandboxPathEnv(key string) bool {
	switch key {
	case "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMPDIR", "TMP", "TEMP",
		"SYMDESK_VAULT", "SYMDESK_SIDECAR", "SYMRELATE_CONFIG_HOME", "SYMRELATE_DATA_HOME", "SYMRELATE_CACHE_HOME",
		"SYMINGEST_VAULT", "SYMINGEST_DB_PATH", "SYMINGEST_ARCHIVE_PATH", "SYMINGEST_INBOX":
		return true
	default:
		return false
	}
}

func pathWithinSandbox(root, workspace, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(workspace, path)
	}
	relative, err := filepath.Rel(root, filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func reservedSandboxEnv(key string) bool {
	switch key {
	case "HOME", "USERPROFILE", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"XDG_STATE_HOME", "XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP",
		"LANG", "LC_ALL", "TZ",
		"SYMDESK_VAULT", "SYMDESK_SIDECAR", "SYMROOM_IDENTITY_KEY", "SYMROOM_ROOM_DIR":
		return true
	default:
		return false
	}
}

func lookupEnvFold(key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok {
		return value, true
	}
	if runtime.GOOS != "windows" {
		return "", false
	}
	for _, pair := range os.Environ() {
		name, value, found := strings.Cut(pair, "=")
		if found && strings.EqualFold(name, key) {
			return value, true
		}
	}
	return "", false
}

func replaceAll(values []string, replacements map[string]string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = replace(value, replacements)
	}
	return result
}

func replace(value string, replacements map[string]string) string {
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value = strings.ReplaceAll(value, key, replacements[key])
	}
	return value
}

func safeWorkspacePath(workspace, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("workspace path must be non-empty and relative: %q", rel)
	}
	clean := filepath.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace path escapes sandbox: %q", rel)
	}
	return filepath.Join(workspace, clean), nil
}

func removeSandbox(path string) {
	for attempt := 0; attempt < 5; attempt++ {
		if err := os.RemoveAll(path); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(time.Duration(1<<attempt) * 10 * time.Millisecond)
	}
}
