package main

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// createImmutableSourceSnapshot materializes only the named Git revision in a
// private temporary directory. It never overlays files from the caller's live
// worktree, so ignored, deleted, and renamed local inputs cannot influence a
// read-only fixture check.
func createImmutableSourceSnapshot(repoRoot, revision string) (string, func(), error) {
	parent, err := os.MkdirTemp("", "portgen-check-")
	if err != nil {
		return "", nil, fmt.Errorf("create immutable snapshot directory: %w", err)
	}
	cleanup := func() {
		_ = os.RemoveAll(parent)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("protect immutable snapshot directory: %w", err)
	}
	snapshotRoot := filepath.Join(parent, "source")
	if err := os.Mkdir(snapshotRoot, 0o700); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create immutable snapshot root: %w", err)
	}

	//nolint:gosec // revision is validated by the provenance commit relationship.
	command := exec.Command("git", "archive", "--format=tar", revision)
	command.Dir = repoRoot
	stream, err := command.StdoutPipe()
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("open immutable source archive: %w", err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("start immutable source archive: %w", err)
	}

	extractErr := extractImmutableSourceArchive(stream, snapshotRoot)
	if extractErr != nil {
		_ = command.Process.Kill()
	}
	waitErr := command.Wait()
	if extractErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract immutable source archive: %w", extractErr)
	}
	if waitErr != nil {
		cleanup()
		return "", nil, fmt.Errorf("create immutable source archive: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return snapshotRoot, cleanup, nil
}

func extractImmutableSourceArchive(stream io.Reader, snapshotRoot string) error {
	reader := tar.NewReader(stream)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeXGlobalHeader || header.Typeflag == tar.TypeXHeader {
			continue
		}
		path, err := immutableSnapshotPath(snapshotRoot, strings.TrimSuffix(header.Name, "/"))
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fmt.Errorf("create archive directory %q: %w", header.Name, err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return fmt.Errorf("create archive parent for %q: %w", header.Name, err)
			}
			mode := os.FileMode(header.Mode).Perm()
			if mode == 0 {
				mode = 0o600
			}
			file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return fmt.Errorf("create archive file %q: %w", header.Name, err)
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("extract archive file %q: %w", header.Name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close archive file %q: %w", header.Name, closeErr)
			}
		default:
			return fmt.Errorf("immutable source archive contains unsupported entry %q (type %d)", header.Name, header.Typeflag)
		}
	}
}

func immutableSnapshotPath(snapshotRoot, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') || strings.ContainsRune(name, '\\') {
		return "", fmt.Errorf("archive path %q is not a portable relative path", name)
	}
	native := filepath.FromSlash(name)
	if filepath.IsAbs(native) || filepath.VolumeName(native) != "" || !filepath.IsLocal(native) {
		return "", fmt.Errorf("archive path %q is not a local relative path", name)
	}
	path := filepath.Join(snapshotRoot, native)
	rel, err := filepath.Rel(snapshotRoot, path)
	if err != nil {
		return "", fmt.Errorf("resolve archive path %q: %w", name, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive path %q escapes snapshot root", name)
	}
	return path, nil
}
