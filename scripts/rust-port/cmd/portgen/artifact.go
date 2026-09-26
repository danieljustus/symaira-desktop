package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const artifactMagic = "# SYMAIRA PORT FIXTURE PATCH V1"

const maxArtifactSize = 64 << 20

var errNoFixtureChanges = errors.New("fixture generation produced no changes")

func runGeneratorCommand(goTool, repoRoot string, environment []string, name string, args ...string) error {
	command := exec.Command(goTool, args...)
	command.Dir = repoRoot
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("generate %s: %w\noutput: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func generationEnvWithActivation(environment []string) []string {
	return append(append([]string(nil), environment...), "PORT_GENERATE=1")
}

func commitFixtureOutputs(repoRoot, base string) error {
	otherFiles, err := gitOutput(repoRoot, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return err
	}
	if len(otherFiles) != 0 {
		return fmt.Errorf("fixture generation created untracked outputs: %q", strings.Split(strings.TrimSuffix(string(otherFiles), "\x00"), "\x00"))
	}
	changed, err := gitOutput(repoRoot, "diff", "--name-only", "-z", base, "--")
	if err != nil {
		return err
	}
	paths, err := checkedChangedPaths(changed)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errNoFixtureChanges
	}
	args := append([]string{"add", "--"}, paths...)
	if _, err := gitOutput(repoRoot, args...); err != nil {
		return fmt.Errorf("stage generated outputs: %w", err)
	}
	return commitStagedFixtureOutputs(repoRoot, base)
}

func commitStagedFixtureOutputs(repoRoot, base string) error {
	staged, err := gitOutput(repoRoot, "diff", "--cached", "--name-only", "-z", base, "--")
	if err != nil {
		return err
	}
	if _, err := checkedChangedPaths(staged); err != nil {
		return err
	}
	hooksDir, err := os.MkdirTemp("", "portgen-empty-hooks-")
	if err != nil {
		return fmt.Errorf("create empty private Git hooks directory: %w", err)
	}
	defer os.RemoveAll(hooksDir)
	if _, err := gitOutput(repoRoot,
		"-c", "core.hooksPath="+hooksDir,
		"-c", "user.name=Symaira Port Fixture Generator",
		"-c", "user.email=port-fixtures@localhost",
		"commit", "--no-gpg-sign", "-m", "testdata(port): refresh generated fixtures"); err != nil {
		return fmt.Errorf("commit generated P/Q outputs: %w", err)
	}
	q, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return err
	}
	parent, err := gitOutput(repoRoot, "rev-parse", q+"^")
	if err != nil {
		return fmt.Errorf("read generated Q parent: %w", err)
	}
	if strings.TrimSpace(string(parent)) != base {
		return fmt.Errorf("generated Q must have P as its sole parent: P=%s Q-parent=%s", base, strings.TrimSpace(string(parent)))
	}
	if err := validateQChanges(repoRoot, base, q); err != nil {
		return err
	}
	return verifyPortFixtureTreeEntries(repoRoot, q)
}

func createPatchArtifact(repoRoot, base string) ([]byte, error) {
	q, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return nil, err
	}
	if q != base {
		if err := validateQChanges(repoRoot, base, q); err != nil {
			return nil, err
		}
	}
	args := []string{"diff", "--binary", "--no-ext-diff", "--no-renames", base, q, "--"}
	args = append(args, fixturePaths...)
	args = append(args, provenanceFixture)
	patch, err := gitOutput(repoRoot, args...)
	if err != nil {
		return nil, fmt.Errorf("create binary patch: %w", err)
	}
	if len(patch) != 0 {
		if err := validatePatchPaths(patch); err != nil {
			return nil, err
		}
	}
	var artifact bytes.Buffer
	fmt.Fprintf(&artifact, "%s\n# base-commit: %s\n", artifactMagic, base)
	artifact.Write(patch)
	if artifact.Len() > maxArtifactSize {
		return nil, fmt.Errorf("generated patch artifact exceeds %d bytes", maxArtifactSize)
	}
	return artifact.Bytes(), nil
}

func applyArtifactCommit(repoRoot, artifactPath string) error {
	if err := verifyCleanWorktree(repoRoot); err != nil {
		return fmt.Errorf("artifact application requires a clean worktree: %w", err)
	}
	if err := verifyNoUntrackedGeneratorInputs(repoRoot); err != nil {
		return fmt.Errorf("artifact application source guard: %w", err)
	}
	base, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return err
	}
	callerState, err := captureCallerState(repoRoot)
	if err != nil {
		return err
	}
	//nolint:gosec // path is an explicit operator-supplied read-only artifact
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		return fmt.Errorf("read patch artifact: %w", err)
	}
	if len(artifact) > maxArtifactSize {
		return fmt.Errorf("patch artifact exceeds %d bytes", maxArtifactSize)
	}
	patch, artifactBase, err := decodePatchArtifact(artifact)
	if err != nil {
		return err
	}
	if artifactBase != base {
		return fmt.Errorf("patch base does not match checked-out P: artifact=%s current=%s", artifactBase, base)
	}
	if len(patch) == 0 {
		if err := runProvenanceCheck(repoRoot); err != nil {
			return fmt.Errorf("validate no-op fixture artifact: %w", err)
		}
		finalState, err := captureCallerState(repoRoot)
		if err != nil {
			return err
		}
		if finalState != callerState {
			return errors.New("no-op artifact application changed the invoking worktree or Git index")
		}
		fmt.Fprintf(os.Stdout, "No fixture changes for P=%s; no Q commit was created.\n", base)
		return nil
	}
	if err := validatePatchPaths(patch); err != nil {
		return err
	}
	snapshot, cleanup, err := createImmutableSourceSnapshot(repoRoot, base)
	if err != nil {
		return fmt.Errorf("create private apply worktree: %w", err)
	}
	defer cleanup()
	if err := applyPatchInWorktree(snapshot, base, patch); err != nil {
		return err
	}
	if err := runProvenanceCheck(snapshot); err != nil {
		return fmt.Errorf("validate applied P/Q: %w", err)
	}
	q, err := resolveGitCommit(snapshot, "HEAD")
	if err != nil {
		return err
	}
	finalState, err := captureCallerState(repoRoot)
	if err != nil {
		return err
	}
	if finalState != callerState {
		return errors.New("artifact application changed the invoking worktree or Git index")
	}
	fmt.Fprintf(os.Stdout, "Validated fixture commit Q=%s (parent P=%s). Review the artifact, then explicitly move your branch to Q.\n", q, base)
	return nil
}

func applyPatchInWorktree(snapshot, base string, patch []byte) error {
	if err := validateFixtureDestinations(snapshot); err != nil {
		return fmt.Errorf("validate private apply destinations: %w", err)
	}
	command, done, err := gitCommand(snapshot, "apply", "--index", "--binary")
	if err != nil {
		return err
	}
	command.Stdin = bytes.NewReader(patch)
	output, applyErr := command.CombinedOutput()
	done()
	if applyErr != nil {
		return fmt.Errorf("apply allowlisted patch in private worktree: %w: %s", applyErr, strings.TrimSpace(string(output)))
	}
	if err := validateFixtureDestinations(snapshot); err != nil {
		return fmt.Errorf("validate applied fixture destinations: %w", err)
	}
	if err := commitStagedFixtureOutputs(snapshot, base); err != nil {
		return err
	}
	return nil
}

func decodePatchArtifact(artifact []byte) ([]byte, string, error) {
	first, rest, ok := bytes.Cut(artifact, []byte("\n"))
	if !ok || string(first) != artifactMagic {
		return nil, "", errors.New("unrecognized fixture patch artifact")
	}
	second, patch, ok := bytes.Cut(rest, []byte("\n"))
	if !ok || !strings.HasPrefix(string(second), "# base-commit: ") {
		return nil, "", errors.New("fixture patch artifact has no base commit")
	}
	base := strings.TrimPrefix(string(second), "# base-commit: ")
	if !fullGitCommit.MatchString(base) {
		return nil, "", errors.New("fixture patch artifact has an invalid base commit")
	}
	return patch, base, nil
}

func validatePatchPaths(patch []byte) error {
	allowed := make(map[string]struct{}, len(fixturePaths)+1)
	for _, path := range fixturePaths {
		allowed[path] = struct{}{}
	}
	allowed[provenanceFixture] = struct{}{}
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(patch), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			fields := strings.Fields(strings.TrimPrefix(line, "diff --git "))
			if len(fields) != 2 || !strings.HasPrefix(fields[0], "a/") || !strings.HasPrefix(fields[1], "b/") {
				return fmt.Errorf("patch contains an invalid path header: %q", line)
			}
			oldPath := strings.TrimPrefix(fields[0], "a/")
			newPath := strings.TrimPrefix(fields[1], "b/")
			if oldPath != newPath {
				return fmt.Errorf("patch renames a path: %s -> %s", oldPath, newPath)
			}
			if _, ok := allowed[oldPath]; !ok {
				return fmt.Errorf("patch path is outside the fixture allowlist: %s", oldPath)
			}
			if _, duplicate := seen[oldPath]; duplicate {
				return fmt.Errorf("patch repeats path: %s", oldPath)
			}
			seen[oldPath] = struct{}{}
		case strings.HasPrefix(line, "--- "):
			path := strings.TrimPrefix(line, "--- ")
			if !strings.HasPrefix(path, "a/") || strings.TrimPrefix(path, "a/") == "/dev/null" {
				return fmt.Errorf("patch contains an invalid old path: %q", line)
			}
			if _, ok := allowed[strings.TrimPrefix(path, "a/")]; !ok {
				return fmt.Errorf("patch old path is outside the fixture allowlist: %q", line)
			}
		case strings.HasPrefix(line, "+++ "):
			path := strings.TrimPrefix(line, "+++ ")
			if !strings.HasPrefix(path, "b/") || strings.TrimPrefix(path, "b/") == "/dev/null" {
				return fmt.Errorf("patch contains an invalid new path: %q", line)
			}
			if _, ok := allowed[strings.TrimPrefix(path, "b/")]; !ok {
				return fmt.Errorf("patch new path is outside the fixture allowlist: %q", line)
			}
		case strings.HasPrefix(line, "new file mode "), strings.HasPrefix(line, "deleted file mode "),
			strings.HasPrefix(line, "rename from "), strings.HasPrefix(line, "rename to "),
			strings.HasPrefix(line, "copy from "), strings.HasPrefix(line, "copy to "):
			return fmt.Errorf("patch contains a file creation, deletion, rename, or copy: %q", line)
		case strings.HasPrefix(line, "old mode "), strings.HasPrefix(line, "new mode "):
			return fmt.Errorf("patch changes file mode: %q", line)
		}
	}
	if len(seen) == 0 && len(patch) != 0 {
		return errors.New("patch has no derived fixture paths")
	}
	return nil
}

func checkedChangedPaths(output []byte) ([]string, error) {
	paths := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	if len(paths) == 1 && paths[0] == "" {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !isPortDerivedOutput(path) {
			return nil, fmt.Errorf("generated change is outside fixture allowlist: %s", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("generated path is repeated: %s", path)
		}
		seen[path] = struct{}{}
	}
	return paths, nil
}

func validateQChanges(repoRoot, base, q string) error {
	output, err := gitOutput(repoRoot, "diff-tree", "--no-commit-id", "--name-only", "-z", "-r", base, q, "--")
	if err != nil {
		return err
	}
	paths, err := checkedChangedPaths(output)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("P/Q contains no generated outputs")
	}
	parent, err := gitOutput(repoRoot, "rev-list", "--parents", "-n", "1", q)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(parent))
	if len(fields) != 2 || fields[1] != base {
		return fmt.Errorf("Q must have exactly one parent P: got %q", strings.TrimSpace(string(parent)))
	}
	return nil
}

func validateFixtureDestinations(root string) error {
	paths := append(append([]string(nil), fixturePaths...), provenanceFixture)
	return validateDestinations(root, paths)
}

func validateDestinations(root string, paths []string) error {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("fixture root is not a real directory: %s", root)
	}
	for _, rel := range paths {
		current := root
		parts := strings.Split(filepath.FromSlash(rel), string(filepath.Separator))
		for i, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				return fmt.Errorf("inspect fixture path %s: %w", rel, err)
			}
			if i < len(parts)-1 {
				if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
					return fmt.Errorf("fixture parent is not a real directory: %s", current)
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("fixture destination is not a regular file: %s", current)
			}
			links, err := fileLinkCount(current, info)
			if err != nil {
				return fmt.Errorf("inspect fixture hard links %s: %w", rel, err)
			}
			if links != 1 {
				return fmt.Errorf("fixture destination has %d hard links, want 1: %s", links, rel)
			}
		}
	}
	return nil
}

func captureCallerState(repoRoot string) (string, error) {
	status, err := gitOutput(repoRoot, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	head, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return "", err
	}
	return head + "\x00" + string(status), nil
}

func verifyCallerSnapshot(repoRoot, base string) error {
	state, err := captureCallerState(repoRoot)
	if err != nil {
		return fmt.Errorf("inspect invoking worktree: %w", err)
	}
	if !strings.HasPrefix(state, base+"\x00") {
		return fmt.Errorf("invoking HEAD changed during fixture operation: expected P=%s", base)
	}
	return nil
}
