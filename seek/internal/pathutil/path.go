// Package pathutil provides shared path validation for HTTP and MCP interfaces.
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathRestrictionError is returned when a path violates the home directory restriction.
type PathRestrictionError struct {
	Path string
	Root string
}

func (e *PathRestrictionError) Error() string {
	return fmt.Sprintf("access denied: path %q is outside the allowed root (%s)", e.Path, e.Root)
}

// RestrictToHome normalises a path and checks that it resolves to an existing
// entry under the user's home directory. This is the security boundary used by
// both the HTTP /index endpoint and the MCP index_document tool.
//
// On success it returns the absolute path and a nil error. Callers that need to
// differentiate between "does not exist" and "outside home" can inspect the
// error string, but for security any non-nil error should be treated as "access
// denied" from the caller's perspective.
func RestrictToHome(reqPath string) (string, error) {
	absPath, err := filepath.Abs(reqPath)
	if err != nil {
		return "", fmt.Errorf("invalid path: %w", err)
	}

	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("path does not exist: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	homeResolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("cannot resolve home directory: %w", err)
	}

	rel, err := filepath.Rel(homeResolved, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", &PathRestrictionError{Path: resolved, Root: homeResolved}
	}
	return resolved, nil
}
