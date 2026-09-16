package main

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var fullGitCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// verifyProvenanceCommit proves that checked-in provenance is the sole derived
// change in a direct child Q of the recorded immutable oracle commit P.
func verifyProvenanceCommit(repoRoot, oracleCommit string) error {
	head, err := resolveGitCommit(repoRoot, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve checked provenance commit: %w", err)
	}
	return verifyProvenanceCommitAt(repoRoot, head, oracleCommit)
}

func verifyProvenanceCommitAt(repoRoot, head, oracleCommit string) error {
	if !fullGitCommit.MatchString(head) {
		return fmt.Errorf("checked provenance commit %q must be a lowercase full 40-character SHA", head)
	}
	if !fullGitCommit.MatchString(oracleCommit) {
		return fmt.Errorf("oracle commit %q must be a lowercase full 40-character SHA", oracleCommit)
	}

	parents, err := gitOutput(repoRoot, "show", "-s", "--format=%P", head)
	if err != nil {
		return fmt.Errorf("read checked provenance parents: %w", err)
	}
	parentIDs := strings.Fields(string(parents))
	if len(parentIDs) != 1 {
		return fmt.Errorf("checked provenance commit must have exactly one parent, got %d", len(parentIDs))
	}
	if parentIDs[0] != oracleCommit {
		return fmt.Errorf("recorded oracle %s does not equal the direct parent %s of the checked provenance commit", oracleCommit, parentIDs[0])
	}

	changed, err := gitOutput(repoRoot, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--no-renames", head)
	if err != nil {
		return fmt.Errorf("list checked provenance changes: %w", err)
	}
	changedPaths := strings.Split(strings.TrimSuffix(string(changed), "\x00"), "\x00")
	if len(changedPaths) == 0 || changedPaths[0] == "" {
		return fmt.Errorf("checked provenance commit has no derived fixture changes")
	}

	hasProvenance := false
	for _, rel := range changedPaths {
		if rel == provenanceFixture {
			hasProvenance = true
		}
		if !isPortDerivedOutput(rel) {
			return fmt.Errorf("checked provenance commit contains unexpected path %q", rel)
		}
	}
	if !hasProvenance {
		return fmt.Errorf("checked provenance commit must change %s", provenanceFixture)
	}
	return nil
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
	if requested != head {
		return "", fmt.Errorf("requested oracle commit %s must equal current HEAD %s so generation can produce a direct P-to-Q provenance commit", requested, head)
	}
	return head, nil
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

func verifyCleanWorktree(repoRoot string) error {
	status, err := gitOutput(repoRoot, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("inspect worktree cleanliness: %w", err)
	}
	if len(status) != 0 {
		return fmt.Errorf("checked provenance requires a clean worktree")
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
	command := gitCommand(repoRoot, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

func gitCommand(repoRoot string, args ...string) *exec.Cmd {
	//nolint:gosec // every caller uses fixed Git subcommands and repository-derived revisions.
	command := exec.Command("git", append([]string{"--no-replace-objects"}, args...)...)
	command.Dir = repoRoot
	command.Env = sanitizedGitEnvironment(os.Environ())
	return command
}

func sanitizedGitEnvironment(environment []string) []string {
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
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
}
