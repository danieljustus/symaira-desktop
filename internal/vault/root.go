package vault

import (
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
	canonicalPath, canonicalErr := canonicalize(absPath)
	if canonicalErr != nil {
		canonicalPath = absPath
	}
	rel, err := filepath.Rel(r.canonical, canonicalPath)
	if err != nil {
		return "", fmt.Errorf("relativize vault file path: %w", err)
	}
	if rel == ".." || filepath.IsAbs(rel) || len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("vault path escapes root: %s", path)
	}
	return rel, nil
}

// Stat returns metadata for a path through the confined root.
func (r *Root) Stat(path string) (fs.FileInfo, error) {
	rel, err := r.relative(path)
	if err != nil {
		return nil, err
	}
	info, err := r.root.Stat(rel)
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
	file, err := openRootReadFile(r.root, rel)
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
