package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

var fullGitCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// verifyProvenanceAncestry proves that the recorded immutable oracle commit is a
// real revision in the history of the checked tree, so a fixture set can never
// be relabelled with a future, malformed or side-branch identity.
//
// A direct P-to-Q child relation is deliberately NOT required. This repository
// squash-merges every pull request, so the commit that records an oracle advance
// can never be a direct child of the functional commit whose bytes it records.
// Fail-closed binding does not depend on that shape: the recorded production and
// generator digests must equal the bytes of the checked tree, the recorded
// production digest must also equal the bytes at the recorded oracle commit, and
// every fixture is compared by checksum against the checked tree — so a fixture
// or source tree edited after the oracle was recorded is rejected regardless of
// the commit graph.
func verifyProvenanceAncestry(repoRoot, oracleCommit string) error {
	head, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve checked provenance revision: %w", err)
	}
	return verifyProvenanceAncestryAt(repoRoot, head, oracleCommit)
}

func verifyProvenanceAncestryAt(repoRoot, head, oracleCommit string) error {
	if !fullGitCommit.MatchString(head) {
		return fmt.Errorf("checked revision %q must be a lowercase full 40-character SHA", head)
	}
	if !fullGitCommit.MatchString(oracleCommit) {
		return fmt.Errorf("oracle commit %q must be a lowercase full 40-character SHA", oracleCommit)
	}
	if oracleCommit == head {
		return nil
	}
	ancestor, err := isAncestorOf(repoRoot, oracleCommit, head)
	if err != nil {
		return err
	}
	if !ancestor {
		return fmt.Errorf("recorded oracle %s is neither the checked revision %s nor one of its ancestors", oracleCommit, head)
	}
	return nil
}

func isAncestorOf(repoRoot, ancestor, descendant string) (bool, error) {
	command, cleanup, err := gitCommand(repoRoot, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		return false, err
	}
	defer cleanup()
	err = command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	// git reports exit 1 for "not an ancestor" and exit 128 for an unknown or
	// malformed revision; both mean the caller's revision is not part of the
	// checked history.
	if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 128) {
		return false, nil
	}
	return false, fmt.Errorf("compare %s with %s: %w", ancestor, descendant, err)
}

func resolveGitCommit(repoRoot, revision string) (string, error) {
	commit, err := gitOutput(repoRoot, "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	resolved := strings.TrimSpace(string(commit))
	if !fullGitCommit.MatchString(resolved) {
		return "", fmt.Errorf("resolved revision %q is not a lowercase full 40-character SHA", resolved)
	}
	return resolved, nil
}

// resolveGenerationOracleCommit resolves the revision generation records as the
// immutable oracle. It defaults to HEAD, and accepts an explicit revision only
// when that revision is HEAD or one of its ancestors: generation may document a
// deliberate advance onto an existing revision, never an arbitrary or future
// identity. The caller additionally proves the working-tree production source is
// byte-identical to the recorded revision.
func resolveGenerationOracleCommit(repoRoot, requested string) (string, error) {
	head, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve current generation commit: %w", err)
	}
	if requested == "" {
		return head, nil
	}
	if !fullGitCommit.MatchString(requested) {
		return "", fmt.Errorf("requested oracle commit %q must be a lowercase full 40-character SHA", requested)
	}
	if requested == head {
		return head, nil
	}
	ancestor, err := isAncestorOf(repoRoot, requested, head)
	if err != nil {
		return "", err
	}
	if !ancestor {
		return "", fmt.Errorf("requested oracle commit %s must be the checked revision %s or one of its ancestors", requested, head)
	}
	return requested, nil
}

func isPortDerivedOutput(rel string) bool {
	if rel == provenanceFixture {
		return true
	}
	for _, fixture := range fixturePaths {
		if rel == fixture {
			return true
		}
	}
	return false
}

// verifyCleanWorktree requires tracked content to match the checked revision.
//
// Line-ending-only differences are ignored: Windows runners check out with
// core.autocrlf=true, so every text file would otherwise look modified and the
// port contract would fail there for a reason that has nothing to do with the
// verified bytes. Nothing that is verified is read from the worktree — the
// fixtures and provenance come from Git blobs at the checked revision and the
// package checks run in a disposable snapshot worktree.
func verifyCleanWorktree(repoRoot string) error {
	command, cleanup, err := gitCommand(repoRoot, "diff", "--quiet", "--no-ext-diff", "--ignore-cr-at-eol", "HEAD", "--")
	if err != nil {
		return err
	}
	defer cleanup()
	output, err := command.CombinedOutput()
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return fmt.Errorf("checked provenance requires a worktree matching the checked revision")
	}
	return fmt.Errorf("inspect worktree cleanliness: %w: %s", err, strings.TrimSpace(string(output)))
}

// Generation executes Go packages in the caller's worktree. Untracked and
// ignored files in those packages can change the oracle without changing HEAD.
func verifyNoUntrackedGeneratorInputs(repoRoot string) error {
	for _, ignored := range []bool{false, true} {
		args := []string{"ls-files", "--others", "--exclude-standard", "-z"}
		if ignored {
			args = append(args, "--ignored")
		}
		args = append(args, "--", "cmd", "internal", "scripts/rust-port", "vendor", "go.work", "go.work.sum")
		files, err := gitOutput(repoRoot, args...)
		if err != nil {
			return fmt.Errorf("inspect untracked generator inputs: %w", err)
		}
		if len(files) != 0 {
			return fmt.Errorf("generation requires no untracked or ignored Go inputs in cmd, internal, scripts/rust-port, vendor, or go.work")
		}
	}
	return nil
}

type gitTreeEntry struct {
	mode   string
	kind   string
	object string
	path   string
}

func verifyPortFixtureTreeEntries(repoRoot, revision string) error {
	paths := append(append([]string(nil), fixturePaths...), provenanceFixture)
	for _, rel := range paths {
		entry, err := gitTreeEntryAt(repoRoot, revision, rel)
		if err != nil {
			return err
		}
		if entry.mode != "100644" || entry.kind != "blob" || !fullGitCommit.MatchString(entry.object) {
			return fmt.Errorf("%s at %s must be a regular 100644 blob, got mode=%s kind=%s object=%s", rel, revision, entry.mode, entry.kind, entry.object)
		}
	}
	return nil
}

func gitTreeEntryAt(repoRoot, revision, rel string) (gitTreeEntry, error) {
	output, err := gitOutput(repoRoot, "ls-tree", "-z", revision, "--", rel)
	if err != nil {
		return gitTreeEntry{}, fmt.Errorf("read tree entry %s at %s: %w", rel, revision, err)
	}
	records := strings.Split(strings.TrimSuffix(string(output), "\x00"), "\x00")
	if len(records) != 1 || records[0] == "" {
		return gitTreeEntry{}, fmt.Errorf("tree entry %s at %s is missing or ambiguous", rel, revision)
	}
	metadata, name, found := strings.Cut(records[0], "	")
	if !found || name != rel {
		return gitTreeEntry{}, fmt.Errorf("tree entry %s at %s is malformed", rel, revision)
	}
	fields := strings.Fields(metadata)
	if len(fields) != 3 {
		return gitTreeEntry{}, fmt.Errorf("tree entry %s at %s has malformed metadata", rel, revision)
	}
	return gitTreeEntry{mode: fields[0], kind: fields[1], object: fields[2], path: name}, nil
}

func gitOutput(repoRoot string, args ...string) ([]byte, error) {
	command, cleanup, err := gitCommand(repoRoot, args...)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func gitCommand(repoRoot string, args ...string) (*exec.Cmd, func(), error) {
	configPath, cleanup, err := inventory.PrivateGitConfig()
	if err != nil {
		return nil, nil, err
	}
	//nolint:gosec // every caller uses fixed Git subcommands and repository-derived revisions.
	// The sanitized environment omits Actions' global safe.directory setting.
	// Trust only this checkout, never a user-wide wildcard.
	command := exec.Command("git", append([]string{"-c", "safe.directory=" + filepath.ToSlash(repoRoot), "--no-replace-objects"}, args...)...)
	command.Dir = repoRoot
	command.Env = sanitizedGitEnvironment(os.Environ(), configPath)
	return command, cleanup, nil
}

func sanitizedGitEnvironment(environment []string, configPath string) []string {
	result := make([]string, 0, len(environment)+3)
	for _, item := range environment {
		name, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			continue
		}
		result = append(result, item)
	}
	return append(result,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+configPath,
		"GIT_TERMINAL_PROMPT=0",
	)
}
