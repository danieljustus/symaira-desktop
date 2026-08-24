package vault

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// DefaultAssetsFolder is the default relative folder name where binary assets
// (images, attachments) are stored inside the vault.
const DefaultAssetsFolder = "assets"

// AssetsFolderName sanitizes and validates the configured assets folder name.
// It rejects absolute paths and path traversal (..) so the assets folder is
// guaranteed to remain inside the vault root. If raw is empty or invalid,
// DefaultAssetsFolder is returned.
func AssetsFolderName(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return DefaultAssetsFolder
	}
	if strings.HasPrefix(trimmed, "/") {
		return DefaultAssetsFolder
	}
	cleaned := strings.Trim(trimmed, "/")
	if cleaned == "" || strings.Contains(cleaned, "..") {
		return DefaultAssetsFolder
	}
	return cleaned
}

// SanitizeAssetName replaces path separators (/ and \), colons (:), control
// characters, and newlines in a file-name base with hyphens (-).
// If the resulting string is empty after trimming whitespace, it falls back to
// "pasted-image".
func SanitizeAssetName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == ':' || unicode.IsControl(r) || r == '\n' || r == '\r' {
			b.WriteRune('-')
		} else {
			b.WriteRune(r)
		}
	}
	cleaned := strings.TrimSpace(b.String())
	if cleaned == "" {
		return "pasted-image"
	}
	return cleaned
}

// CollisionSafeAssetName builds a collision-safe file name: `base.ext`, then
// `base-2.ext`, `base-3.ext`, and so on.
// exists is a predicate reporting whether a candidate file name is already taken.
func CollisionSafeAssetName(base, ext string, exists func(candidate string) bool) string {
	sanitizedBase := SanitizeAssetName(base)
	ext = strings.TrimPrefix(ext, ".")

	formatCandidate := func(stem string) string {
		if ext == "" {
			return stem
		}
		return fmt.Sprintf("%s.%s", stem, ext)
	}

	candidate := formatCandidate(sanitizedBase)
	counter := 2
	for exists(candidate) {
		candidate = formatCandidate(fmt.Sprintf("%s-%d", sanitizedBase, counter))
		counter++
	}
	return candidate
}

// StoreAsset writes binary data into the vault's assets folder under a
// collision-safe name and returns the vault-relative path (for Markdown links).
//
// Parity note: This matches Sources/SymDeskCore/VaultAssets.swift. Future
// iterations can point the Swift side at this implementation via FFI / CLI / API.
func StoreAsset(vaultRoot string, data []byte, preferredName, ext, folder string, now time.Time) (string, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}

	folderName := AssetsFolderName(folder)
	dirPath, err := SecurePath(vaultRoot, folderName)
	if err != nil {
		return "", fmt.Errorf("resolve assets folder: %w", err)
	}

	// folderName is validated above and dirPath is rooted by SecurePath.
	//nolint:gosec // the directory is confined to the vault root
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return "", fmt.Errorf("create assets directory: %w", err)
	}

	var base string
	cleanExt := strings.TrimPrefix(ext, ".")
	if preferredName != "" {
		base = strings.TrimSuffix(preferredName, filepath.Ext(preferredName))
		if cleanExt == "" {
			cleanExt = strings.TrimPrefix(filepath.Ext(preferredName), ".")
		}
	} else {
		base = fmt.Sprintf("pasted-%s", now.Format("2006-01-02-150405"))
	}

	name := CollisionSafeAssetName(base, cleanExt, func(candidate string) bool {
		target := filepath.Join(dirPath, candidate)
		_, statErr := os.Stat(target)
		return statErr == nil
	})

	filePath := filepath.Join(dirPath, name)
	if err := writeFileAtomic(filePath, data); err != nil {
		return "", fmt.Errorf("write asset file: %w", err)
	}

	return filepath.ToSlash(filepath.Join(folderName, name)), nil
}

// AssetMarkdownLink returns the standard Markdown image embedding snippet for
// a vault-relative asset path, percent-encoding spaces so the link works in
// standard Markdown renderers.
func AssetMarkdownLink(relativePath string) string {
	encoded := strings.ReplaceAll(relativePath, " ", "%20")
	name := filepath.Base(relativePath)
	return fmt.Sprintf("![%s](%s)", name, encoded)
}
