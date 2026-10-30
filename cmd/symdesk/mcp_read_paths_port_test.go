package main

import (
	"encoding/json"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

var mcpReadRootToken = regexp.MustCompile(`\$ROOT[A-Za-z0-9._/\\-]*`)

// MCP carries JSON inside JSON strings: temporary roots can therefore be
// escaped twice. Replace only the disposable root,
// retaining closing-quote escapes, newlines, and arbitrary user backslashes.
func normalizeMCPReadPaths(text, root string) string {
	escaped := strings.ReplaceAll(root, `\`, `\\`)
	for _, spelling := range []string{strings.ReplaceAll(escaped, `\`, `\\`), escaped, root} {
		text = strings.ReplaceAll(text, spelling, "$ROOT")
	}
	return text
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
			`{"path":"$ROOT/vault\\alpha.md"}`,
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
		{`{"path":"C:\\Users\\runner\\fixture\\vault\\note.md"}`, `{"path":"$ROOT\\vault\\note.md"}`},
		{`{"text":"{\"path\":\"C:\\\\Users\\\\runner\\\\fixture\\\\vault\\\\note.md\"}"}`, `{"text":"{\"path\":\"$ROOT\\\\vault\\\\note.md\"}"}`},
		{`{"text":"[{\"path\":\"notebooks\\\\empty.md\"}]"}`, `{"text":"[{\"path\":\"notebooks\\\\empty.md\"}]"}`},
		{`{"text":"[{\"path\":\"bases\\\\work.md\"}]"}`, `{"text":"[{\"path\":\"bases\\\\work.md\"}]"}`},
		{`{"text":"[{\"path\":\"meetings\\\\a.md\"}]"}`, `{"text":"[{\"path\":\"meetings\\\\a.md\"}]"}`},
		{`notebooks\\ordinary.md type=\"\"\n bad\\literal`, `notebooks\\ordinary.md type=\"\"\n bad\\literal`},
	} {
		if got := normalizeMCPReadPaths(test.input, root); got != test.want {
			t.Fatalf("nested MCP path: got %q, want %q", got, test.want)
		}
	}
}

// Absolute host paths legitimately use native separators. Derive this platform's
// expectation from the Unix recording, only within $ROOT path tokens. Actual
// output is never slash-folded, so an incorrect Rust separator still fails.
func mcpReadPlatformExpectation(text string, windows bool) string {
	if !windows {
		return text
	}
	return mcpReadRootToken.ReplaceAllStringFunc(text, func(token string) string {
		path := strings.TrimRight(token, `\`)
		// A quote escaped once denotes JSON inside an MCP text string. Each
		// additional JSON layer doubles the escaping of a native separator.
		escaping := 2 * (len(token) - len(path) + 1)
		return strings.ReplaceAll(path, "/", strings.Repeat(`\`, escaping)) + token[len(path):]
	})
}

func TestMCPReadPlatformExpectation(t *testing.T) {
	recorded := `{"text":"[{\"path\":\"$ROOT/vault/nested/note.md\"},{\"path\":\"notebooks/nested/note.md\"}]"}`
	native := `{"text":"[{\"path\":\"$ROOT\\\\vault\\\\nested\\\\note.md\"},{\"path\":\"notebooks/nested/note.md\"}]"}`
	if got := mcpReadPlatformExpectation(recorded, true); got != native {
		t.Fatalf("platform expectation: %q != %q", got, native)
	}
	if mcpReadPlatformExpectation(recorded, false) != recorded {
		t.Fatal("Unix expectation changed")
	}
	wrongAbsolute := strings.Replace(native, `vault\\\\nested`, "vault/nested", 1)
	wrongRelative := strings.Replace(native, "notebooks/nested", `notebooks\\\\nested`, 1)
	for _, wrong := range []string{wrongAbsolute, wrongRelative} {
		if wrong == native || normalizeMCPReadPaths(wrong, "unused-root") == native {
			t.Fatal("wrong output separator was hidden")
		}
	}
}

func adjustMCPReadFixtureExpectation(data []byte) ([]byte, error) {
	if runtime.GOOS != "windows" {
		return data, nil
	}
	var fixture mcpReadFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		return nil, err
	}
	for i := range fixture.Cases {
		fixture.Cases[i].Stdout = mcpReadPlatformExpectation(fixture.Cases[i].Stdout, true)
		fixture.Cases[i].Stderr = mcpReadPlatformExpectation(fixture.Cases[i].Stderr, true)
	}
	adjusted, err := json.MarshalIndent(fixture, "", "  ")
	return append(adjusted, '\n'), err
}
