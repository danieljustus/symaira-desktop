package vault

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

var wikilinkRegex = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Document represents a parsed Markdown file from the vault
type Document struct {
	Path        string
	SHA256      string
	Title       string
	Created     string
	Tags        []string
	Aliases     []string
	Frontmatter map[string]interface{}
	Body        string
	Links       []string

	// Size and ModTime are the on-disk file size and modification time as of
	// parsing. ParseFile populates both via os.Stat; ParseBytes only sets
	// Size (from the byte slice it was handed) since it has no path to stat.
	// A zero ModTime means "unknown" to callers that cache these values for
	// a stat-based skip check.
	Size    int64
	ModTime time.Time

	// Contract v2: first-class document metadata (all optional)
	DocumentDate string // ISO-8601 date the document refers to
	Person       string // household member
	Status       string // enum: open|paid|submitted|done|needs_review|waiting_for_reply
	DueDate      string // ISO-8601 date deadline
	Confidence   int    // 0-100 classification confidence
	OcrJSONPath  string // path to plain-text OCR JSON
	Simhash      string // 64-bit SimHash hex
	ASN          *int   // optional, vault-wide unique positive archive serial number

	// Contract v3/v4: document kind classification (note|document|meeting|notebook)
	// Resolved at parse time: explicit frontmatter `type` wins, then inference.
	Type string
}

// ValidStatuses enumerates the allowed values for Document.Status.
var ValidStatuses = map[string]bool{
	"open":              true,
	"paid":              true,
	"submitted":         true,
	"done":              true,
	"needs_review":      true,
	"waiting_for_reply": true,
}

// ResolveVaultRoot determines the actual vault root directory.
// Priority: explicitly passed path (from flag) > config.Vault (which already prioritizes SYMDESK_VAULT)
func ResolveVaultRoot(flagPath string, cfg *config.Config) (string, error) {
	root := flagPath
	if root == "" {
		if cfg != nil && cfg.Vault != "" {
			root = cfg.Vault
		}
	}
	if root == "" {
		return "", fmt.Errorf("vault path not configured (use flag or SYMDESK_VAULT env)")
	}

	absPath, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute vault path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("vault path does not exist: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("vault path is not a directory")
	}

	return absPath, nil
}

// skipDirNames lists non-hidden directory names that are always excluded
// from vault walks. These are conventional locations for third-party
// dependency trees or build artifacts (e.g. an npm/pip project living
// inside an otherwise ordinary documents folder) and should never be
// treated as vault content, even though they aren't dot-prefixed.
var skipDirNames = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"venv":         true,
	".venv":        true,
	"__pycache__":  true,
}

// Walk iterates over all Markdown files in the vault, respecting ignore rules.
func Walk(vaultRoot string, fn func(path string) error) error {
	return filepath.WalkDir(vaultRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Ignore hidden directories (e.g., .obsidian, .trash, .git)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && d.Name() != "." {
				return filepath.SkipDir
			}
			if skipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}

		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}

		if filepath.Ext(d.Name()) == ".md" {
			return fn(path)
		}

		return nil
	})
}

// ParseFile parses a markdown file, extracting frontmatter, body, and wikilinks.
// It also stats the file so the returned Document carries the on-disk size
// and modification time, letting callers cache them for a stat-based skip
// check on a later refresh.
func ParseFile(path string) (*Document, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	doc, err := ParseBytes(path, fileBytes)
	if err != nil {
		return nil, err
	}
	doc.ModTime = info.ModTime()
	return doc, nil
}

// ParseBytes parses Markdown already read by a caller while retaining path as
// the document identity. It is useful for rooted filesystems where reopening an
// absolute path would discard the caller's confinement guarantees.
func ParseBytes(path string, fileBytes []byte) (*Document, error) {
	var err error

	// Calculate SHA256
	hash := sha256.Sum256(fileBytes)
	shaHex := hex.EncodeToString(hash[:])

	doc := &Document{
		Path:        path,
		SHA256:      shaHex,
		Frontmatter: make(map[string]interface{}),
		Size:        int64(len(fileBytes)),
	}

	// Split Frontmatter and Body
	reader := bufio.NewReader(bytes.NewReader(fileBytes))
	var frontmatterBytes []byte
	var bodyBytes []byte
	inFrontmatter := false
	frontmatterFound := false

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("read lines: %w", err)
		}

		lineStr := string(line)
		trimmed := strings.TrimRight(lineStr, "\r\n")

		if trimmed == "---" {
			if !inFrontmatter && !frontmatterFound && len(frontmatterBytes) == 0 {
				inFrontmatter = true
			} else if inFrontmatter {
				inFrontmatter = false
				frontmatterFound = true
			} else {
				// Just part of the body
				bodyBytes = append(bodyBytes, line...)
			}
		} else {
			if inFrontmatter {
				frontmatterBytes = append(frontmatterBytes, line...)
			} else {
				bodyBytes = append(bodyBytes, line...)
			}
		}

		if err == io.EOF {
			break
		}
	}

	if frontmatterFound && len(frontmatterBytes) > 0 {
		err = yaml.Unmarshal(frontmatterBytes, &doc.Frontmatter)
		if err != nil {
			return nil, fmt.Errorf("invalid frontmatter in %s: %w", path, err)
		}
	}

	// Extract standard fields
	if t, ok := doc.Frontmatter["title"].(string); ok {
		doc.Title = t
	} else {
		// Fallback to filename
		doc.Title = strings.TrimSuffix(filepath.Base(path), ".md")
	}

	if c, ok := doc.Frontmatter["created"].(string); ok {
		doc.Created = c
	}

	if tagsRaw, ok := doc.Frontmatter["tags"]; ok {
		switch tv := tagsRaw.(type) {
		case []interface{}:
			for _, item := range tv {
				if s, ok := item.(string); ok {
					doc.Tags = append(doc.Tags, s)
				}
			}
		case string:
			// single tag string
			doc.Tags = append(doc.Tags, tv)
		}
	}

	// Contract v5: parse aliases from frontmatter (optional, backwards-compatible)
	if aliasesRaw, ok := doc.Frontmatter["aliases"]; ok {
		switch av := aliasesRaw.(type) {
		case []interface{}:
			for _, item := range av {
				if s, ok := item.(string); ok {
					doc.Aliases = append(doc.Aliases, s)
				}
			}
		case string:
			// single alias string
			doc.Aliases = append(doc.Aliases, av)
		}
	}

	// Contract v2: extract document metadata from frontmatter (all optional, backwards-compatible)
	doc.DocumentDate = getStringFrontmatter(doc.Frontmatter, "document_date")
	doc.Person = getStringFrontmatter(doc.Frontmatter, "person")
	doc.Status = getStringFrontmatter(doc.Frontmatter, "status")
	doc.DueDate = getStringFrontmatter(doc.Frontmatter, "due_date")
	doc.Confidence = getIntFrontmatter(doc.Frontmatter, "confidence")
	doc.OcrJSONPath = getStringFrontmatter(doc.Frontmatter, "ocr_json_path")
	doc.Simhash = getStringFrontmatter(doc.Frontmatter, "simhash")
	doc.ASN, err = asnFromFrontmatter(path, doc.Frontmatter)
	if err != nil {
		return nil, err
	}

	// Contract v3/v4: resolve document kind (note|document|meeting|notebook)
	doc.Type = inferType(doc.Frontmatter)

	doc.Body = string(bodyBytes)
	doc.Links = extractWikilinks(doc.Body)

	// Extract inline tags from body (issue #522)
	inlineTags := ExtractInlineTags(doc.Body)
	for _, it := range inlineTags {
		if !containsTagCaseInsensitive(doc.Tags, it) {
			doc.Tags = append(doc.Tags, it)
		}
	}

	return doc, nil
}

func containsTagCaseInsensitive(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			return true
		}
	}
	return false
}

// SecurePath resolves a relative path against the vault root and ensures it does not traverse outside.
// It canonicalizes symlinks to prevent escapes via symlinked directories.
func SecurePath(vaultRoot, relPath string) (string, error) {
	absVault, err := filepath.Abs(vaultRoot)
	if err != nil {
		return "", err
	}
	targetPath := filepath.Join(absVault, relPath)
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}

	if !isUnder(absTarget, absVault) {
		return "", fmt.Errorf("path traversal denied: %s is outside vault", relPath)
	}

	canonicalVault, err := filepath.EvalSymlinks(absVault)
	if err != nil {
		return "", fmt.Errorf("cannot resolve vault root: %w", err)
	}

	canonicalTarget, err := canonicalize(absTarget)
	if err != nil {
		return "", err
	}

	if !isUnder(canonicalTarget, canonicalVault) {
		return "", fmt.Errorf("symlink escape denied: %s resolves outside vault", relPath)
	}

	return canonicalTarget, nil
}

func isUnder(path, root string) bool {
	return strings.HasPrefix(path, root+string(filepath.Separator)) || path == root
}

func canonicalize(path string) (string, error) {
	if _, err := os.Stat(path); err == nil { // CodeQL: exclude
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("cannot resolve path: %w", err)
		}
		return resolved, nil
	}

	parent := path
	for {
		if _, err := os.Stat(parent); err == nil { // CodeQL: exclude
			break
		}
		parent = filepath.Dir(parent)
		if parent == filepath.Dir(parent) {
			break
		}
	}

	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("cannot resolve parent directory: %w", err)
	}

	remaining := strings.TrimPrefix(path, parent)
	return filepath.Join(resolvedParent, remaining), nil
}

// extractWikilinks extracts links from markdown body. Both plain wikilinks
// ([[Note]]) and transclusion embeds (![[Note]], ![[Note#Heading]]) are
// indexed as links so backlinks and the graph see embedded notes too.
func extractWikilinks(body string) []string {
	matches := wikilinkRegex.FindAllStringSubmatch(body, -1)
	var links []string
	seen := make(map[string]bool)
	for _, m := range matches {
		if len(m) > 1 {
			target := m[1]
			// Handle display text: [[Target|Display]]
			parts := strings.SplitN(target, "|", 2)
			link := strings.TrimSpace(parts[0])
			// Strip heading (#Heading) and block (#^block) fragments so
			// [[Note#Section]] and ![[Note#Section]] resolve to the file.
			if idx := strings.Index(link, "#"); idx >= 0 {
				link = strings.TrimSpace(link[:idx])
			}
			if link == "" || seen[link] {
				continue
			}
			seen[link] = true
			links = append(links, link)
		}
	}
	return links
}

func getStringFrontmatter(fm map[string]interface{}, key string) string {
	if v, ok := fm[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func getIntFrontmatter(fm map[string]interface{}, key string) int {
	if v, ok := fm[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case int64:
			return int(n)
		case float64:
			return int(n)
		}
	}
	return 0
}

// SetFrontmatterKey writes a single YAML key=value line inside the frontmatter
// block of a markdown file, preserving every other byte exactly.  Only bare
// scalar values (strings, numbers) are supported.
func SetFrontmatterKey(filePath, key, value string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	lines := strings.Split(string(data), "\n")

	fmStart := -1
	fmEnd := -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" {
			if fmStart == -1 {
				fmStart = i
			} else {
				fmEnd = i
				break
			}
		}
	}
	if fmStart == -1 || fmEnd == -1 {
		newLines := make([]string, 0, len(lines)+3)
		newLines = append(newLines, "---")
		newLines = append(newLines, key+": "+quoteYAML(value))
		newLines = append(newLines, "---")
		newLines = append(newLines, lines...)
		return os.WriteFile(filePath, []byte(strings.Join(newLines, "\n")), 0644)
	}

	keyPrefix := key + ": "
	replaced := false
	for i := fmStart + 1; i < fmEnd; i++ {
		trimmed := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(trimmed, keyPrefix) || trimmed == key {
			lines[i] = keyPrefix + quoteYAML(value)
			replaced = true
			break
		}
	}

	if !replaced {
		newLine := keyPrefix + quoteYAML(value)
		lines = append(lines[:fmEnd], append([]string{newLine}, lines[fmEnd:]...)...)
	}

	return os.WriteFile(filePath, []byte(strings.Join(lines, "\n")), 0644)
}

// quoteYAML returns a YAML-safe quoted string; numeric-looking values are
// left unquoted.
func quoteYAML(v string) string {
	if v == "" {
		return `""`
	}
	isNumber := true
	for i, c := range v {
		if c >= '0' && c <= '9' {
			continue
		}
		if c == '.' {
			continue
		}
		if c == '-' && i == 0 {
			continue
		}
		isNumber = false
		break
	}
	if isNumber && len(v) > 0 {
		return v
	}
	escaped := strings.ReplaceAll(v, `"`, `\"`)
	return `"` + escaped + `"`
}

// SetFrontmatterValue writes a typed frontmatter value to a markdown file,
// preserving the surrounding frontmatter, comments, key ordering and body bytes
// as far as possible. The write is performed atomically (temp file + fsync +
// rename) so a failed write cannot leave a partially written note.
func SetFrontmatterValue(filePath, key string, value interface{}) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	lineEnding := detectLineEnding(data)
	content := string(data)
	var lines []string
	if lineEnding == "\r\n" {
		lines = strings.Split(content, "\r\n")
	} else {
		lines = strings.Split(content, "\n")
	}

	valueStr, err := yamlValueString(value)
	if err != nil {
		return fmt.Errorf("marshal value: %w", err)
	}

	fmStart, fmEnd := findFrontmatterBounds(lines)
	if fmStart == -1 || fmEnd == -1 {
		// No frontmatter block: create one at the top of the file.
		newLines := make([]string, 0, len(lines)+4)
		newLines = append(newLines, "---")
		newLines = append(newLines, key+": "+valueStr)
		newLines = append(newLines, "---")
		newLines = append(newLines, lines...)
		return writeFileAtomic(filePath, []byte(strings.Join(newLines, lineEnding)))
	}

	keyPrefix := key + ": "
	replaced := false
	for i := fmStart + 1; i < fmEnd; i++ {
		trimmed := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(trimmed, keyPrefix) || trimmed == key {
			lines[i] = keyPrefix + valueStr
			replaced = true
			break
		}
	}

	if !replaced {
		// Preserve existing key order by inserting before the closing ---.
		insert := append([]string{keyPrefix + valueStr}, lines[fmEnd:]...)
		lines = append(lines[:fmEnd], insert...)
	}

	return writeFileAtomic(filePath, []byte(strings.Join(lines, lineEnding)))
}

// DeleteFrontmatterValue removes a frontmatter key while preserving the rest of
// the frontmatter block and body. If the key does not exist the file is unchanged.
func DeleteFrontmatterValue(filePath, key string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("read file: %w", err)
	}

	lineEnding := detectLineEnding(data)
	content := string(data)
	var lines []string
	if lineEnding == "\r\n" {
		lines = strings.Split(content, "\r\n")
	} else {
		lines = strings.Split(content, "\n")
	}

	fmStart, fmEnd := findFrontmatterBounds(lines)
	if fmStart == -1 || fmEnd == -1 {
		return nil
	}

	keyPrefix := key + ": "
	for i := fmStart + 1; i < fmEnd; i++ {
		trimmed := strings.TrimRight(lines[i], "\r")
		if strings.HasPrefix(trimmed, keyPrefix) || trimmed == key {
			lines = append(lines[:i], lines[i+1:]...)
			break
		}
	}

	return writeFileAtomic(filePath, []byte(strings.Join(lines, lineEnding)))
}

// yamlValueString renders a typed value as a single-line YAML scalar or inline
// array suitable for a frontmatter key line. Multi-line values fall back to the
// YAML encoder and are trimmed to a single line where possible.
func yamlValueString(v interface{}) (string, error) {
	switch val := v.(type) {
	case string:
		return quoteYAML(val), nil
	case int:
		return strconv.Itoa(val), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(val), nil
	case []string:
		parts := make([]string, 0, len(val))
		for _, s := range val {
			parts = append(parts, quoteYAML(s))
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case []interface{}:
		parts := make([]string, 0, len(val))
		for _, item := range val {
			s, err := yamlValueString(item)
			if err != nil {
				return "", err
			}
			parts = append(parts, s)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	default:
		b, err := yaml.Marshal(v)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(b)), nil
	}
}

// findFrontmatterBounds returns the line indices of the opening and closing
// frontmatter markers (---) in a line-slice split on "\n".
func findFrontmatterBounds(lines []string) (start, end int) {
	start = -1
	end = -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" {
			if start == -1 {
				start = i
			} else {
				end = i
				break
			}
		}
	}
	return start, end
}

// detectLineEnding preserves CRLF files by returning "\r\n" when the file
// already contains it, otherwise "\n".
func detectLineEnding(data []byte) string {
	if strings.Contains(string(data), "\r\n") {
		return "\r\n"
	}
	return "\n"
}

// writeFileAtomic writes data to a temporary file in the same directory, fsyncs
// it, and renames it over path so the update is atomic and durable.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".symdesk-frontmatter-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// documentInferenceFields lists frontmatter keys whose presence suggests a
// file is a "document" rather than a free-form note.
var documentInferenceFields = map[string]bool{
	"source_path":   true,
	"mime":          true,
	"sha256":        true,
	"document_date": true,
	"asn":           true,
}

// inferType resolves a file's document kind when the frontmatter does not
// contain an explicit `type` field. Evaluation order:
//  1. If explicit `type` exists, return it.
//  2. If any document-inference field is present → "document".
//  3. If `meeting_id` is present → "meeting".
//  4. Otherwise → "note".
//
// "notebook" (contract_version 4) is never inferred — it is only ever
// returned here when the frontmatter declares it explicitly (VAULT.md
// section 3: "a sources list alone is not a strong enough signal").
func inferType(fm map[string]interface{}) string {
	if t, ok := fm["type"]; ok {
		if s, ok := t.(string); ok {
			switch s {
			case "note", "document", "meeting", "notebook":
				return s
			}
		}
	}
	// Check inference rules for document
	for key := range documentInferenceFields {
		if _, ok := fm[key]; ok {
			return "document"
		}
	}
	// Check meeting_id
	if _, ok := fm["meeting_id"]; ok {
		return "meeting"
	}
	return "note"
}
