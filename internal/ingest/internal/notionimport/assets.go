package notionimport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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
	defer sf.Close()

	df, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer df.Close()

	if _, err := io.Copy(df, sf); err != nil {
		return err
	}
	return df.Sync()
}
