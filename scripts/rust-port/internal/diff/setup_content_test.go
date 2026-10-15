package diff

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSetupContentEscapesSerializedPathPlaceholders(t *testing.T) {
	for _, path := range []string{`C:\a\fixture`, `C:\new\test`, "C:\\quoted\"<&>\t\u2028"} {
		for _, format := range []string{"json", "toml"} {
			t.Run(format+"/"+path, func(t *testing.T) {
				content := `{"index_path":"${WORKSPACE}/index.db","index_path":null,"enabled":true}`
				if format == "toml" {
					content = "index_path = \"${WORKSPACE}/index.db\"\nenabled = true\n"
				}
				setup := SetupFile{Path: "config." + format, Content: content, ContentFormat: format}
				replacements := map[string]string{"${WORKSPACE}": path}
				// The old raw interpolation must reject or corrupt this same input.
				probe := `{"index_path":"${WORKSPACE}/index.db"}`
				if format == "toml" {
					probe = "index_path = \"${WORKSPACE}/index.db\"\n"
				}
				var unescaped map[string]any
				var probeErr error
				if format == "json" {
					probeErr = json.Unmarshal([]byte(replace(probe, replacements)), &unescaped)
				} else {
					_, probeErr = toml.Decode(replace(probe, replacements), &unescaped)
				}
				if probeErr == nil && unescaped["index_path"] == path+"/index.db" {
					t.Fatal("negative control failed to expose raw Windows path interpolation")
				}
				encoded, err := setupContent(setup, replacements)
				if err != nil {
					t.Fatal(err)
				}
				var decoded struct {
					IndexPath string `json:"index_path" toml:"index_path"`
					Enabled   bool   `json:"enabled" toml:"enabled"`
				}
				if format == "json" {
					err = json.Unmarshal([]byte(encoded), &decoded)
					if !strings.Contains(encoded, `,"index_path":null,`) {
						t.Fatal("duplicate null key was rewritten")
					}
				} else {
					_, err = toml.Decode(encoded, &decoded)
				}
				if err != nil {
					t.Fatal(err)
				}
				if decoded.IndexPath != path+"/index.db" || !decoded.Enabled {
					t.Fatalf("decoded fixture changed its values: %#v", decoded)
				}
				root := t.TempDir()
				if err := setupSandboxFile(root, root, root, setup, replacements); err != nil {
					t.Fatal(err)
				}
				written, err := os.ReadFile(filepath.Join(root, setup.Path)) // #nosec G304 -- test-owned setup path
				if err != nil || string(written) != encoded {
					t.Fatalf("file setup did not use encoded content: %v", err)
				}
				if raw := replace("${WORKSPACE}", replacements); raw != path {
					t.Fatal("raw process/path substitution changed")
				}
			})
		}
	}
	if _, err := setupContent(SetupFile{ContentFormat: "unknown"}, nil); err == nil {
		t.Fatal("unsupported content format must fail closed")
	}
	raw := SetupFile{Content: "${WORKSPACE}"}
	if got, err := setupContent(raw, map[string]string{"${WORKSPACE}": `C:\new\test`}); err != nil || got != `C:\new\test` {
		t.Fatalf("unformatted setup content changed: %q, %v", got, err)
	}
}
