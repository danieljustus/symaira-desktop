package main

import (
	"regexp"
	"strings"
	"testing"
)

var mcpReadRootToken = regexp.MustCompile(`\$ROOT[A-Za-z0-9._/\\-]*`)
var mcpReadSeparators = regexp.MustCompile(`\\+`)

// MCP carries JSON inside JSON strings: temporary roots can therefore be
// escaped twice. Normalize only rooted tokens and the known notebook/base prefixes,
// retaining closing-quote escapes, newlines, and arbitrary user backslashes.
func normalizeMCPReadPaths(text, root string) string {
	escaped := strings.ReplaceAll(root, `\`, `\\`)
	for _, spelling := range []string{strings.ReplaceAll(escaped, `\`, `\\`), escaped, root} {
		text = strings.ReplaceAll(text, spelling, "$ROOT")
	}
	text = mcpReadRootToken.ReplaceAllStringFunc(text, func(token string) string {
		path := strings.TrimRight(token, `\`)
		return mcpReadSeparators.ReplaceAllString(path, "/") + token[len(path):]
	})
	// Go meeting summaries retain filepath.Rel's native separators.
	text = strings.ReplaceAll(text, `meetings\\\\`, "meetings/")
	text = strings.ReplaceAll(text, `meetings\\`, "meetings/")
	text = strings.ReplaceAll(text, `meetings\`, "meetings/")
	return normalizeBasePaths(normalizeNotebookPaths(text))
}

// These probes characterize suspected spellings without declaring them equivalent.
// Only native drift windows can establish which spelling the process emits; in
// particular, a guessed 8.3 expansion must never erase an unrelated user's path.
func TestNormalizeMCPReadWindowsHypotheses(t *testing.T) {
	root := `C:\Users\runneradmin\AppData\Local\Temp\fixture`
	for _, test := range []struct {
		name, input, want string
	}{
		{
			"short-name-alias-is-not-inferred",
			`{"path":"C:\\Users\\RUNNER~1\\AppData\\Local\\Temp\\fixture\\vault\\alpha.md"}`,
			`{"path":"C:\\Users\\RUNNER~1\\AppData\\Local\\Temp\\fixture\\vault\\alpha.md"}`,
		},
		{
			"slash-root-alias-is-not-inferred",
			`{"path":"C:/Users/runneradmin/AppData/Local/Temp/fixture/vault/alpha.md"}`,
			`{"path":"C:/Users/runneradmin/AppData/Local/Temp/fixture/vault/alpha.md"}`,
		},
		{
			"mixed-separators-below-matching-root",
			`{"path":"C:\\Users\\runneradmin\\AppData\\Local\\Temp\\fixture/vault\\alpha.md"}`,
			`{"path":"$ROOT/vault/alpha.md"}`,
		},
		{
			"relative-external-index-key-is-not-a-root-token",
			`{"after_index":{"..\\external\\link.md":{}}}`,
			`{"after_index":{"..\\external\\link.md":{}}}`,
		},
		{
			"relative-results-key-is-not-a-root-token",
			`{"after_results":{"task\\output.txt":"body"}}`,
			`{"after_results":{"task\\output.txt":"body"}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeMCPReadPaths(test.input, root); got != test.want {
				t.Fatalf("hypothesis normalization: got %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeMCPReadNestedWindowsPaths(t *testing.T) {
	root := `C:\Users\runner\fixture`
	for _, test := range []struct{ input, want string }{
		{`{"path":"C:\\Users\\runner\\fixture\\vault\\note.md"}`, `{"path":"$ROOT/vault/note.md"}`},
		{`{"text":"{\"path\":\"C:\\\\Users\\\\runner\\\\fixture\\\\vault\\\\note.md\"}"}`, `{"text":"{\"path\":\"$ROOT/vault/note.md\"}"}`},
		{`{"text":"[{\"path\":\"notebooks\\\\empty.md\"}]"}`, `{"text":"[{\"path\":\"notebooks/empty.md\"}]"}`},
		{`{"text":"[{\"path\":\"bases\\\\work.md\"}]"}`, `{"text":"[{\"path\":\"bases/work.md\"}]"}`},
		{`{"text":"[{\"path\":\"meetings\\\\a.md\"}]"}`, `{"text":"[{\"path\":\"meetings/a.md\"}]"}`},
		{`notebooks\\ordinary.md type=\"\"\n bad\\literal`, `notebooks/ordinary.md type=\"\"\n bad\\literal`},
	} {
		if got := normalizeMCPReadPaths(test.input, root); got != test.want {
			t.Fatalf("nested MCP path: got %q, want %q", got, test.want)
		}
	}
}
