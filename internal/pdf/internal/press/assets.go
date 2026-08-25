package press

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// markdownImageRe matches inline Markdown image references:
// ![alt](path), ![alt](path "title") and ![alt](<path with spaces>).
// Reference-style images (![alt][id]) are not resolved yet.
var markdownImageRe = regexp.MustCompile(`!\[[^\]]*\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)

// collectAssets copies every locally referenced Markdown image from srcDir
// into dst (the render work dir), preserving the relative path so the engine
// can resolve it under --root. Remote refs (http/https/data:) are skipped —
// the engine handles (or rejects) them itself.
//
// The function fails closed: a missing file, an absolute path, a `..`
// traversal or a symlink escaping srcDir is a hard error with the offending
// reference named, instead of a confusing engine failure later.
func collectAssets(srcDir, dst string, body []byte) error {
	refs := localImageRefs(body)
	if len(refs) == 0 {
		return nil
	}
	if srcDir == "" {
		return &RenderError{
			Stage:   "assets",
			Message: fmt.Sprintf("image reference %q cannot be resolved: the input has no source directory", refs[0]),
			Hint:    "render from a Markdown file on disk (symprint render <file.md>) so relative image paths have a base directory",
		}
	}

	srcRoot, err := filepath.EvalSymlinks(srcDir)
	if err != nil {
		return &RenderError{Stage: "assets", Message: "could not resolve source directory", Err: err}
	}

	for _, ref := range refs {
		if err := copyAsset(srcRoot, srcDir, dst, ref); err != nil {
			return err
		}
	}
	return nil
}

// localImageRefs extracts the local (non-remote) image paths from a Markdown
// body. Only inline image syntax is covered.
func localImageRefs(body []byte) []string {
	var out []string
	for _, m := range markdownImageRe.FindAllSubmatch(body, -1) {
		ref := string(m[1])
		if isRemoteRef(ref) {
			continue
		}
		out = append(out, ref)
	}
	return out
}

func isRemoteRef(ref string) bool {
	lower := strings.ToLower(ref)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "data:")
}

// copyAsset validates a single referenced path and copies it from the source
// directory into the work dir at the same relative location.
func copyAsset(srcRoot, srcDir, dst, ref string) error {
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") {
		return &RenderError{
			Stage:   "assets",
			Message: fmt.Sprintf("image reference %q is an absolute path", ref),
			Hint:    "reference images relative to the Markdown file (e.g. ![alt](images/logo.png))",
		}
	}
	clean := filepath.Clean(ref)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return &RenderError{
			Stage:   "assets",
			Message: fmt.Sprintf("image reference %q escapes the source directory (path traversal is not allowed)", ref),
			Hint:    "keep referenced assets inside the document's directory tree",
		}
	}

	srcPath := filepath.Join(srcDir, clean)

	// Containment check with symlinks resolved on both sides: a symlink inside
	// the source tree must not smuggle in files from outside it.
	resolved, err := filepath.EvalSymlinks(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &RenderError{
				Stage:   "assets",
				Message: fmt.Sprintf("referenced image %q not found next to the input document", ref),
				Hint:    "check the path — it is resolved relative to the Markdown file",
			}
		}
		return &RenderError{Stage: "assets", Message: fmt.Sprintf("could not resolve image reference %q", ref), Err: err}
	}
	if resolved != srcRoot && !strings.HasPrefix(resolved, srcRoot+string(filepath.Separator)) {
		return &RenderError{
			Stage:   "assets",
			Message: fmt.Sprintf("image reference %q resolves outside the source directory (symlink escape)", ref),
			Hint:    "copy the asset into the document's directory tree instead of linking to it",
		}
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return &RenderError{Stage: "assets", Message: fmt.Sprintf("could not stat image %q", ref), Err: err}
	}
	if info.IsDir() {
		return &RenderError{
			Stage:   "assets",
			Message: fmt.Sprintf("image reference %q points at a directory", ref),
		}
	}

	dstPath := filepath.Join(dst, clean)
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return &RenderError{Stage: "write", Message: "could not create asset directory in work dir", Err: err}
	}
	if err := copyFile(resolved, dstPath); err != nil {
		return &RenderError{Stage: "write", Message: fmt.Sprintf("could not copy image %q into the work dir", ref), Err: err}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
