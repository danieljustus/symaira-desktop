package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// createImmutableSourceSnapshot checks the named Git revision out as a
// disposable linked worktree. Unlike a working-tree copy, it contains exactly
// the committed revision (including its Git metadata) and cannot inherit
// ignored, deleted, renamed, or untracked caller inputs.
func createImmutableSourceSnapshot(repoRoot, revision string) (string, func(), error) {
	parent, err := os.MkdirTemp("", "portgen-check-")
	if err != nil {
		return "", nil, fmt.Errorf("create immutable snapshot directory: %w", err)
	}
	//nolint:gosec // 0700 on a private scratch directory is the intended restriction
	if err := os.Chmod(parent, 0o700); err != nil {
		_ = os.RemoveAll(parent)
		return "", nil, fmt.Errorf("protect immutable snapshot directory: %w", err)
	}
	snapshotRoot := filepath.Join(parent, "source")
	cleanup := func() {
		command, done, err := gitCommand(repoRoot, "worktree", "remove", "--force", snapshotRoot)
		if err == nil {
			_ = command.Run()
			done()
		}
		_ = os.RemoveAll(parent)
	}

	command, done, err := gitCommand(repoRoot, "worktree", "add", "--detach", snapshotRoot, revision)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	output, err := command.CombinedOutput()
	done()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create immutable source worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return snapshotRoot, cleanup, nil
}
