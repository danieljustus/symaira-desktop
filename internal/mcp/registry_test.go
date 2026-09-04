package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/tools"
)

func TestMCPToolListMatchesCanonicalRegistry(t *testing.T) {
	for _, allowWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "read_only", true: "read_write"}[allowWrite], func(t *testing.T) {
			registry := tools.NewRegistry(tools.RegistryOptions{
				Config:        &config.Config{Vault: "/test/vault"},
				ServerVersion: "test-version",
				AllowWrite:    allowWrite,
			})
			entries := registry.Enabled(allowWrite)

			server := mcpserver.New("symdesk", "test-version")
			for _, entry := range entries {
				server.RegisterTool(adaptTool(entry))
			}

			var output bytes.Buffer
			request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
			if err := server.ServeIO(context.Background(), bytes.NewReader(request), &output); err != nil {
				t.Fatal(err)
			}

			var response struct {
				Result struct {
					Tools []struct {
						Name        string          `json:"name"`
						Description string          `json:"description"`
						InputSchema json.RawMessage `json:"inputSchema"`
					} `json:"tools"`
				} `json:"result"`
			}
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatalf("decode tools/list response: %v; output=%q", err, output.String())
			}

			if len(response.Result.Tools) != len(entries) {
				t.Fatalf("MCP returned %d tools, registry enabled %d", len(response.Result.Tools), len(entries))
			}
			for i, got := range response.Result.Tools {
				want := entries[i]
				if got.Name != want.Name || got.Description != want.Description {
					t.Errorf("tool %d metadata mismatch: got name=%q description=%q, want name=%q description=%q", i, got.Name, got.Description, want.Name, want.Description)
				}
				if !json.Valid(got.InputSchema) {
					t.Errorf("tool %q returned invalid input schema %q", got.Name, got.InputSchema)
				}
				var gotSchema, wantSchema any
				if err := json.Unmarshal(got.InputSchema, &gotSchema); err != nil {
					t.Fatalf("decode MCP schema for %q: %v", got.Name, err)
				}
				if err := json.Unmarshal(want.InputSchema, &wantSchema); err != nil {
					t.Fatalf("decode registry schema for %q: %v", want.Name, err)
				}
				if !reflect.DeepEqual(gotSchema, wantSchema) {
					t.Errorf("tool %q input schema differs from registry", got.Name)
				}
			}
		})
	}
}

func TestMCPToolCapabilitiesMatchCanonicalRegistry(t *testing.T) {
	registry := tools.NewRegistry(tools.RegistryOptions{})
	entries := registry.All()
	if len(ToolCapabilities) != len(entries) {
		t.Fatalf("MCP capability count=%d, registry count=%d", len(ToolCapabilities), len(entries))
	}
	for _, entry := range entries {
		want := Mutating
		if entry.ReadOnly {
			want = ReadOnly
		}
		if got := ToolCapabilities[entry.Name]; got != want {
			t.Errorf("tool %q: MCP capability=%q, registry capability=%q", entry.Name, got, want)
		}
	}
}

// TestMCPToolListEmitsReadOnlyHint is a contract test (issue #792): every
// tool the server publishes in tools/list must carry an explicit
// "annotations" object whose readOnlyHint/destructiveHint match the
// registry's reviewed ReadOnly/Destructive classification. A newly
// registered tool that skips adaptTool's annotation mapping, or a canonical
// registry entry whose Tool literal is built without going through the
// entry()/destructive() helpers in tools.NewRegistry, fails this test
// instead of silently shipping as an unreviewed "write" tool.
func TestMCPToolListEmitsReadOnlyHint(t *testing.T) {
	registry := tools.NewRegistry(tools.RegistryOptions{
		Config:        &config.Config{Vault: "/test/vault"},
		ServerVersion: "test-version",
		AllowWrite:    true,
	})
	entries := registry.All()

	server := mcpserver.New("symdesk", "test-version")
	for _, entry := range entries {
		server.RegisterTool(adaptTool(entry))
	}

	var output bytes.Buffer
	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n")
	if err := server.ServeIO(context.Background(), bytes.NewReader(request), &output); err != nil {
		t.Fatal(err)
	}

	var response struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations *struct {
					ReadOnlyHint    bool `json:"readOnlyHint"`
					DestructiveHint bool `json:"destructiveHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatalf("decode tools/list response: %v; output=%q", err, output.String())
	}
	if len(response.Result.Tools) != len(entries) {
		t.Fatalf("MCP returned %d tools, registry has %d", len(response.Result.Tools), len(entries))
	}

	for i, got := range response.Result.Tools {
		want := entries[i]
		if got.Annotations == nil {
			t.Errorf("tool %q: tools/list omitted \"annotations\" entirely; every registered tool must set an explicit readOnlyHint", want.Name)
			continue
		}
		if got.Annotations.ReadOnlyHint != want.ReadOnly {
			t.Errorf("tool %q: readOnlyHint=%v, registry ReadOnly=%v", want.Name, got.Annotations.ReadOnlyHint, want.ReadOnly)
		}
		if got.Annotations.DestructiveHint != want.Destructive {
			t.Errorf("tool %q: destructiveHint=%v, registry Destructive=%v", want.Name, got.Annotations.DestructiveHint, want.Destructive)
		}
	}
}
