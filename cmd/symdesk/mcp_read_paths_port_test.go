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
