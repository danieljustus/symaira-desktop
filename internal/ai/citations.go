package ai

import (
	"bufio"
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

const citationBodyScanLineLimit = 4000
const citationFrontmatterLineLimit = 200

// CitationWarning describes a citation-shaped link in generated content that
// was not observed in the current agent run. It is advisory: callers may still
// write the generated content.
type CitationWarning struct {
	Path string `json:"path"`
	Line int    `json:"line,omitempty"`
}

var citationSourceKeys = map[string]struct{}{
	"quelle": {}, "quellen": {}, "source": {}, "sources": {},
	"referenz": {}, "referenzen": {}, "reference": {}, "references": {},
}

var citationDocumentExtensions = map[string]struct{}{
	".csv": {}, ".doc": {}, ".docx": {}, ".eml": {}, ".md": {},
	".markdown": {}, ".odt": {}, ".pdf": {}, ".ppt": {}, ".pptx": {},
	".rtf": {}, ".txt": {}, ".xls": {}, ".xlsx": {},
}

// CheckCitationWarnings finds citation-shaped wikilinks to unread files in
// generated Markdown. Source/Quellen/References frontmatter and source
// sections are citation contexts; ordinary body links are not. The body scan
// is capped so validation cannot stall a write on a huge note. Warnings never
// block writes.
func CheckCitationWarnings(content string, readPaths []string) []CitationWarning {
	read := readPathKeys(readPaths)
	var candidates []citationCandidate
	bodyStart := scanCitationFrontmatter(content, &candidates)
	scanCitationBody(content, bodyStart, &candidates)

	warnings := make([]CitationWarning, 0, len(candidates))
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		key := citationPathKey(candidate.Path)
		if key == "" {
			continue
		}
		if _, ok := read[key]; ok {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		warnings = append(warnings, CitationWarning(candidate))
	}
	return warnings
}

// CheckCitationWarningsSafe is the write-path wrapper. A validation bug is
// never allowed to turn an advisory citation warning into a failed write.
func CheckCitationWarningsSafe(content string, readPaths []string) (warnings []CitationWarning) {
	defer func() {
		if recover() != nil {
			warnings = nil
		}
	}()
	return CheckCitationWarnings(content, readPaths)
}

type citationCandidate struct {
	Path string
	Line int
}

func scanCitationFrontmatter(content string, candidates *[]citationCandidate) int {
	lines := strings.SplitN(content, "\n", citationFrontmatterLineLimit+2)
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return 0
	}
	inSourceBlock := false
	for i := 1; i < len(lines) && i <= citationFrontmatterLineLimit; i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			return i + 1
		}
		if key, value, ok := frontmatterKey(line); ok {
			inSourceBlock = isCitationSourceKey(key)
			if inSourceBlock {
				appendCitationCandidates(candidates, value, i+1, false)
			}
			continue
		}
		if inSourceBlock && strings.Contains(strings.TrimSpace(line), "[[") {
			appendCitationCandidates(candidates, line, i+1, false)
		}
	}
	return 0
}

func scanCitationBody(content string, bodyStartLine int, candidates *[]citationCandidate) {
	if bodyStartLine < 0 {
		bodyStartLine = 0
	}
	inSourceHeading := false
	inTable := false
	citationColumn := -1
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for absoluteLine := 0; scanner.Scan(); absoluteLine++ {
		if absoluteLine < bodyStartLine {
			continue
		}
		i := absoluteLine - bodyStartLine
		if i >= citationBodyScanLineLimit {
			break
		}
		line := scanner.Text()
		lineNumber := absoluteLine + 1

		if strings.HasPrefix(strings.TrimSpace(line), "#") && isMarkdownHeading(line) {
			inSourceHeading = isCitationHeading(line)
			inTable = false
			citationColumn = -1
			continue
		}
		if inSourceHeading {
			trimmed := strings.TrimSpace(line)
			switch {
			case trimmed == "":
				continue
			case strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, "*"):
				appendCitationCandidates(candidates, line, lineNumber, true)
				continue
			default:
				inSourceHeading = false
			}
		}

		if strings.Contains(line, "|") {
			cells := tableCells(line)
			if !inTable {
				for column, cell := range cells {
					if isCitationColumn(cell) {
						citationColumn = column
						inTable = true
						break
					}
				}
				continue
			}
			if isTableSeparator(line) {
				continue
			}
			if citationColumn >= 0 && citationColumn < len(cells) {
				appendCitationCandidates(candidates, cells[citationColumn], lineNumber, true)
			}
		} else {
			inTable = false
			citationColumn = -1
		}
	}
}

func appendCitationCandidates(candidates *[]citationCandidate, text string, line int, documentsOnly bool) {
	for start := 0; start < len(text); {
		open := strings.Index(text[start:], "[[")
		if open < 0 {
			return
		}
		open += start
		close := strings.Index(text[open+2:], "]]")
		if close < 0 {
			return
		}
		close += open + 2
		target := strings.TrimSpace(text[open+2 : close])
		if pipe := strings.IndexByte(target, '|'); pipe >= 0 {
			target = target[:pipe]
		}
		if anchor := strings.IndexAny(target, "#^"); anchor >= 0 {
			target = target[:anchor]
		}
		target = strings.TrimSpace(target)
		if target != "" && (!documentsOnly || looksLikeDocumentReference(target)) {
			*candidates = append(*candidates, citationCandidate{Path: target, Line: line})
		}
		start = close + 2
	}
}

func frontmatterKey(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	colon := strings.IndexByte(trimmed, ':')
	if colon <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(trimmed[:colon]), trimmed[colon+1:], true
}

func isCitationSourceKey(key string) bool {
	_, ok := citationSourceKeys[strings.ToLower(strings.TrimSpace(key))]
	return ok
}

func isMarkdownHeading(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	return level > 0 && level < len(trimmed) && (trimmed[level] == ' ' || trimmed[level] == '\t')
}

func isCitationHeading(line string) bool {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimLeft(trimmed, "#")
	trimmed = strings.TrimSpace(trimmed)
	lower := strings.ToLower(trimmed)
	return lower == "quelle" || lower == "quellen" || lower == "source" || lower == "sources" || lower == "referenz" || lower == "referenzen" || lower == "reference" || lower == "references"
}

func isCitationColumn(cell string) bool {
	lower := strings.ToLower(cell)
	for _, word := range []string{"gesprächspartner", "gespraechspartner", "interview", "quelle", "source", "verfasser", "author", "speaker", "sprecher", "befragter", "zitat", "citation"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func tableCells(line string) []string {
	parts := strings.Split(line, "|")
	if len(parts) > 1 && strings.TrimSpace(parts[0]) == "" {
		parts = parts[1:]
	}
	if len(parts) > 1 && strings.TrimSpace(parts[len(parts)-1]) == "" {
		parts = parts[:len(parts)-1]
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func isTableSeparator(line string) bool {
	cells := tableCells(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.Trim(cell, "-:")
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

func looksLikeDocumentReference(target string) bool {
	if ext := strings.ToLower(filepath.Ext(target)); ext != "" {
		if _, ok := citationDocumentExtensions[ext]; ok {
			return true
		}
	}
	lower := strings.ToLower(target)
	for _, word := range []string{"interview", "notiz", "note", "meeting", "bericht", "report", "use case", "use-case", "protokoll", "document", "dokument", "briefing", "memo", "synthese"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func citationPathKey(path string) string {
	path = cleanReadPath(path)
	if path == "" {
		return ""
	}
	base := filepath.Base(path)
	if dot := strings.LastIndexByte(base, '.'); dot > 0 {
		path = path[:len(path)-len(base)+dot]
	}
	return strings.ToLower(path)
}

func cleanReadPath(path string) string {
	path = strings.TrimSpace(strings.TrimPrefix(path, "./"))
	if path == "" || strings.ContainsAny(path, "\r\n") || strings.Contains(path, "://") {
		return ""
	}
	path = filepath.ToSlash(filepath.Clean(path))
	if path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return ""
	}
	return path
}

func readPathKeys(paths []string) map[string]struct{} {
	keys := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if key := citationPathKey(path); key != "" {
			keys[key] = struct{}{}
		}
	}
	return keys
}

func readPathsForTool(call ToolCall, output string) []string {
	paths := make(map[string]struct{})
	var input any
	if json.Unmarshal(call.Input, &input) == nil {
		collectDocumentPaths(input, paths)
	}
	var result any
	if json.Unmarshal([]byte(output), &result) == nil {
		collectDocumentPaths(result, paths)
	}
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}

func collectDocumentPaths(value any, paths map[string]struct{}) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "path" || key == "file" || key == "note" {
				if path, ok := child.(string); ok {
					if normalized := cleanReadPath(path); normalized != "" {
						paths[normalized] = struct{}{}
					}
				}
			}
			collectDocumentPaths(child, paths)
		}
	case []any:
		for _, child := range value {
			collectDocumentPaths(child, paths)
		}
	}
}
