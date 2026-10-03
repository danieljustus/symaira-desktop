package health

import (
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Local Markdown destinations use vault-relative paths. URI schemes, remote
// network references, and same-document fragments are never filesystem probes.
func markdownLinkTarget(destination string) (string, bool) {
	if strings.HasPrefix(destination, "//") {
		return "", false
	}
	if colon := strings.IndexByte(destination, ':'); colon > 0 {
		scheme := destination[:colon]
		valid := scheme[0] >= 'a' && scheme[0] <= 'z' || scheme[0] >= 'A' && scheme[0] <= 'Z'
		for _, c := range scheme[1:] {
			valid = valid && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("+.-", c))
		}
		if valid {
			return "", false
		}
	}
	if index := strings.IndexAny(destination, "#?"); index >= 0 {
		destination = destination[:index]
	}
	if destination == "" {
		return "", false
	}
	decoded, err := url.PathUnescape(destination)
	if err != nil || !utf8.ValidString(decoded) {
		return destination, true
	}
	return decoded, true
}

func addMarkdownFile(inventory map[string][]string, file string) {
	full, base := strings.ToLower(file), strings.ToLower(path.Base(file))
	inventory[full] = append(inventory[full], file)
	if full != base {
		inventory[base] = append(inventory[base], file)
	}
}

func markdownLinkExists(root, target string, inventory map[string][]string) bool {
	// Reject traversal before basename matching and before touching the root.
	// Backslashes are not portable URL separators; do not let Windows reinterpret
	// them as traversal or an absolute UNC path.
	if strings.ContainsAny(target, "\\\x00") || strings.HasPrefix(target, "/") {
		return false
	}
	cleaned := path.Clean(target)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return false
	}
	candidates := inventory[strings.ToLower(cleaned)]
	for _, candidate := range candidates {
		info, err := vault.StatInRoot(root, filepath.Join(root, filepath.FromSlash(candidate)))
		if err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}
