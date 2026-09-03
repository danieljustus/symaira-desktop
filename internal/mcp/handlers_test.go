package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-desktop/internal/config"
)

func TestNewTransformToolRequiresText(t *testing.T) {
	tool := newTransformTool()
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"intent":"summarize"}`)); err == nil {
		t.Error("expected an error for missing text")
	}
}

func TestNewTransformToolAggregatesChunks(t *testing.T) {
	t.Setenv("SYMDESK_OLLAMA_URL", "")
	tool := newTransformTool()

	out, err := tool.Handler(context.Background(), json.RawMessage(`{"text":"hello world","intent":"summarize"}`))
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]string)["result"]
	if result == "" {
		t.Error("expected a non-empty aggregated transform result")
	}
}

func TestRegisterDeskStatusReturnsVersionAndVault(t *testing.T) {
	cfg := &config.Config{Vault: "/tmp/my-vault"}
	server := mcpserver.New("symdesk", "test-version")
	registerDeskStatus(server, cfg)

	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"desk_status","arguments":{}}}` + "\n")
	if err := server.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "my-vault") {
		t.Errorf("expected status response to contain the vault path, got %q", out.String())
	}
	if !strings.Contains(out.String(), ServerVersion) {
		t.Errorf("expected status response to contain the version, got %q", out.String())
	}
}

func TestNewRelatedToolRequiresFile(t *testing.T) {
	tool := newRelatedTool(testFactory(t))
	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error for missing file")
	}
}

func TestNewRelatedToolReturnsEmptyWithoutComposition(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	factory := testFactory(t)
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := svc.NoteNew("Related Note", "some content", ""); err != nil {
		t.Fatal(err)
	}

	tool := newRelatedTool(factory)
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"file":"Related_Note.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("expected a non-nil related-data result even without symmemory composed")
	}
}

func TestNewIngestJobsToolWithoutSymingest(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	tool := newIngestJobsTool(testFactory(t))

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("expected an error when symingest is not installed")
	}
}

func TestNewIngestRetryToolWithoutSymingest(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	tool := newIngestRetryTool(testFactory(t))

	if _, err := tool.Handler(context.Background(), json.RawMessage(`{"id":"job-1"}`)); err == nil {
		t.Error("expected an error when symingest is not installed")
	}
}
