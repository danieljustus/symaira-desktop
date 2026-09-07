package vault

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

const maxRootReadBytes int64 = 64 << 20

// Root confines vault reads to one opened directory tree. os.Root follows
// symlinks only when their targets remain below the opened root, and keeps the
// stat and read on the same opened file handle.
type Root struct {
	path      string
	canonical string
	root      *os.Root
}

// OpenRoot opens vaultRoot as a confined read root.
func OpenRoot(vaultRoot string) (*Root, error) {
	absRoot, err := filepath.Abs(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve vault root: %w", err)
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("stat vault root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("vault root is not a directory")
	}
	canonicalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve canonical vault root: %w", err)
	}
	handle, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, fmt.Errorf("open vault root: %w", err)
	}
	return &Root{path: absRoot, canonical: canonicalRoot, root: handle}, nil
}

// Close releases the opened vault root.
func (r *Root) Close() error {
	if r == nil || r.root == nil {
		return nil
	}
	return r.root.Close()
}

func (r *Root) relative(path string) (string, error) {
	if r == nil || r.root == nil {
		return "", fmt.Errorf("vault root is closed")
	}
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(r.path, absPath)
	}
	absPath, err := filepath.Abs(absPath)
	if err != nil {
		return "", fmt.Errorf("resolve vault file path: %w", err)
	}
	for _, base := range []string{r.path, r.canonical} {
		rel, relErr := filepath.Rel(base, absPath)
		if relErr != nil {
			continue
		}
		if validRootRelative(rel) {
			return rel, nil
		}
	}
	if resolved, resolveErr := canonicalize(absPath); resolveErr == nil {
		rel, relErr := filepath.Rel(r.canonical, resolved)
		if relErr == nil && validRootRelative(rel) {
			return rel, nil
		}
	}
	return "", fmt.Errorf("vault path escapes root: %s", path)
}

func validRootRelative(rel string) bool {
	return rel != ".." && !filepath.IsAbs(rel) && (len(rel) <= 3 || rel[:3] != ".."+string(filepath.Separator))
}

// RelativePath returns path relative to the opened root while accepting both
// the lexical root spelling and its canonical alias.
func (r *Root) RelativePath(path string) (string, error) {
	return r.relative(path)
}

func (r *Root) open(path, rel string) (*os.File, error) {
	file, err := openRootReadFile(r.root, rel)
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		return file, err
	}
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(r.path, absPath)
	}
	resolved, resolveErr := filepath.EvalSymlinks(absPath)
	if resolveErr != nil {
		return nil, err
	}
	resolvedRel, relErr := r.relative(resolved)
	if relErr != nil {
		return nil, err
	}
	return openRootReadFile(r.root, resolvedRel)
}

// Stat returns metadata for a path through the confined root.
func (r *Root) Stat(path string) (fs.FileInfo, error) {
	rel, err := r.relative(path)
	if err != nil {
		return nil, err
	}
	file, err := r.open(path, rel)
	if err != nil {
		return nil, fmt.Errorf("open vault file for stat %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat vault file %s: %w", path, err)
	}
	return info, nil
}

// ReadDir returns directory entries through the confined root.
func (r *Root) ReadDir(path string) ([]fs.DirEntry, error) {
	rel, err := r.relative(path)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(r.root.FS(), filepath.ToSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("read vault directory %s: %w", path, err)
	}
	return entries, nil
}

// ReadFile reads path and returns metadata from the same opened file handle.
// Contained symlinks are followed; symlinks that resolve outside the vault
// are rejected by os.Root.
func (r *Root) ReadFile(path string) ([]byte, fs.FileInfo, error) {
	rel, err := r.relative(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := r.open(path, rel)
	if err != nil {
		return nil, nil, fmt.Errorf("open vault file %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("stat vault file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("vault path is not a regular file: %s", path)
	}
	if info.Size() > maxRootReadBytes {
		return nil, nil, fmt.Errorf("vault file exceeds %d byte read limit: %s", maxRootReadBytes, path)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRootReadBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read vault file %s: %w", path, err)
	}
	if int64(len(data)) > maxRootReadBytes {
		return nil, nil, fmt.Errorf("vault file exceeds %d byte read limit: %s", maxRootReadBytes, path)
	}
	return data, info, nil
}

// ParseFile parses a file through the confined root. The returned path keeps
// the caller's path identity while bytes and metadata come from one handle.
func (r *Root) ParseFile(path string) (*Document, error) {
	data, info, err := r.ReadFile(path)
	if err != nil {
		return nil, err
	}
	doc, err := ParseBytes(path, data)
	if err != nil {
		return nil, err
	}
	doc.ModTime = info.ModTime()
	return doc, nil
}

// ParseFileInRoot parses a vault file without reopening an unconfined path.
func ParseFileInRoot(vaultRoot, path string) (*Document, error) {
	root, err := OpenRoot(vaultRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ParseFile(path)
}

// IsExternalSymlink reports whether path is a symlink that cannot be
// resolved through vaultRoot. Broken and external links are both rejected;
// contained links remain valid under the vault contract.
func IsExternalSymlink(vaultRoot, path string) bool {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return false
	}
	_, err = StatInRoot(vaultRoot, path)
	return err != nil
}

// StatInRoot stats a vault file through an opened confined root.
func StatInRoot(vaultRoot, path string) (fs.FileInfo, error) {
	root, err := OpenRoot(vaultRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.Stat(path)
}

// ReadDirInRoot reads a vault directory through an opened confined root.
func ReadDirInRoot(vaultRoot, path string) ([]fs.DirEntry, error) {
	root, err := OpenRoot(vaultRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadDir(path)
}

// ReadFileInRoot reads a vault file through an opened confined root.
func ReadFileInRoot(vaultRoot, path string) ([]byte, fs.FileInfo, error) {
	root, err := OpenRoot(vaultRoot)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()
	return root.ReadFile(path)
}

// RelativePathInRoot returns path relative to vaultRoot across path aliases.
func RelativePathInRoot(vaultRoot, path string) (string, error) {
	root, err := OpenRoot(vaultRoot)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	return root.RelativePath(path)
}
