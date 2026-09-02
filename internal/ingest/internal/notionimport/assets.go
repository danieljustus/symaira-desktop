package notionimport

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// requireWithinDir returns an error if path does not resolve to a location
// inside dir (after both are cleaned to absolute form). It is used as a
// defense-in-depth boundary check at file-write/open sinks fed by names
// taken from imported source content (Notion asset links, page titles),
// which must never be able to escape the source or vault directory via a
// "../" segment.
func requireWithinDir(dir, path string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve directory %q: %w", dir, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path %q: %w", path, err)
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path escapes target directory: " + path)
	}
	return nil
}

// assetLinkRe matches markdown image/link references to assets.
// Matches both ![alt](assets/...) and [text](assets/...).
var assetLinkRe = regexp.MustCompile(`(!?\[[^\]]*\]\()assets/([^)]+)\)`)

// copyAndRewriteAssets copies files from srcAssetDir to dstAssetDir and
// rewrites the body's asset links to point at the new relative path.
func copyAndRewriteAssets(srcAssetDir, dstAssetDir, body string) (string, error) {
	if _, err := os.Stat(srcAssetDir); os.IsNotExist(err) {
		return body, nil
	}

	if err := os.MkdirAll(dstAssetDir, 0o700); err != nil {
		return "", fmt.Errorf("create asset dir: %w", err)
	}

	// Find all asset references in the body.
	matches := assetLinkRe.FindAllStringSubmatch(body, -1)
	seen := make(map[string]string) // original name -> copied path

	for _, m := range matches {
		assetName := m[2]
		src := filepath.Join(srcAssetDir, assetName)
		// assetName comes from the imported Markdown body, not our own code,
		// so reject any reference that would resolve outside srcAssetDir
		// (e.g. via a "../" segment) before touching the filesystem.
		if err := requireWithinDir(srcAssetDir, src); err != nil {
			continue
		}
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}

		if _, ok := seen[assetName]; ok {
			continue
		}

		// Generate a content-addressed filename to avoid collisions.
		data, err := os.ReadFile(src)
		if err != nil {
			continue
		}
		hash := sha256.Sum256(data)
		ext := filepath.Ext(assetName)
		dstName := hex.EncodeToString(hash[:]) + ext
		dst := filepath.Join(dstAssetDir, dstName)

		if err := copyFile(src, dst); err != nil {
			return "", fmt.Errorf("copy asset %s: %w", assetName, err)
		}
		seen[assetName] = dstName
	}

	// Rewrite all asset links in the body.
	result := body
	for orig, new := range seen {
		old := "assets/" + orig
		// Use relative path from the note's location to the vault assets dir.
		new := "assets/" + new
		result = strings.ReplaceAll(result, old, new)
	}

	return result, nil
}

// copyFile copies src to dst, creating parent directories as needed.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = sf.Close() }()

	df, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer func() { _ = df.Close() }()

	if _, err := io.Copy(df, sf); err != nil {
		return err
	}
	return df.Sync()
}
