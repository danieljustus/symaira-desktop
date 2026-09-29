package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/tools"
)

const askOfflineMCPFixturePath = "../../testdata/port/mcp/ask-offline.json"

type askOfflineMCPFixture struct {
	SchemaVersion int                        `json:"schema_version"`
	Cases         []askOfflineMCPFixtureCase `json:"cases"`
}

type askOfflineMCPFixtureCase struct {
	ID               string                     `json:"id"`
	Documents        []askOfflineMCPDocument    `json:"documents"`
	ExpectedTool     json.RawMessage            `json:"expected_tool"`
	Calls            []askOfflineMCPFixtureCall `json:"calls"`
	ProviderRequests []json.RawMessage          `json:"provider_requests"`
}

type askOfflineMCPFixtureCall struct {
	ID            string          `json:"id"`
	ArgumentsJSON string          `json:"arguments_json"`
	Expected      json.RawMessage `json:"expected"`
}

type askOfflineMCPDocument struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

func TestAskOfflineMCPOracle(t *testing.T) {
	input := askOfflineMCPFixtureCase{
		ID: "unscoped-offline-search-plan",
		Documents: []askOfflineMCPDocument{{
			Path: "notes/ask.md",
			Body: "---\ntitle: Ask note\ntags: [askscope]\n---\n\nThis note is selected by the tag plan.",
		}},
		Calls: []askOfflineMCPFixtureCall{
			{ID: "query-lowercase", ArgumentsJSON: `{"query":"tag:askscope"}`},
			{ID: "query-uppercase", ArgumentsJSON: `{"QUERY":"tag:askscope"}`},
			{ID: "query-title-case", ArgumentsJSON: `{"Query":"tag:askscope"}`},
			{ID: "query-null", ArgumentsJSON: `{"query":null}`},
			{ID: "arguments-null", ArgumentsJSON: `null`},
			{ID: "query-wrong-type", ArgumentsJSON: `{"query":42}`},
			{ID: "query-whitespace", ArgumentsJSON: `{"query":"   "}`},
			{ID: "notebook-kelvin-case", ArgumentsJSON: `{"query":"tag:askscope","notebooK":"notebook-fixture"}`},
			{ID: "notebook-wrong-type", ArgumentsJSON: `{"query":"tag:askscope","notebook":7}`},
			{ID: "duplicate-folded-query-last-valid", ArgumentsJSON: `{"query":"","QUERY":"tag:askscope"}`},
			{ID: "duplicate-folded-query-last-empty", ArgumentsJSON: `{"QUERY":"tag:askscope","query":""}`},
		},
	}
	got := observeAskOfflineMCPCase(t, input)
	assertAskOfflineMCPOracleBehavior(t, got)
	current := askOfflineMCPFixture{SchemaVersion: 1, Cases: []askOfflineMCPFixtureCase{got}}
	if os.Getenv("PORT_GENERATE") == "1" {
		encoded, err := json.MarshalIndent(current, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, '\n')
		if err := os.WriteFile(askOfflineMCPFixturePath, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	data, err := os.ReadFile(askOfflineMCPFixturePath)
	if err != nil {
		t.Fatalf("read Go-generated offline Ask MCP fixture (generate with PORT_GENERATE=1): %v", err)
	}
	var want askOfflineMCPFixture
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatalf("decode offline Ask MCP fixture: %v", err)
	}
	if want.SchemaVersion != current.SchemaVersion || !sameAskOfflineMCPCases(want.Cases, current.Cases) {
		gotJSON, _ := json.MarshalIndent(current, "", "  ")
		wantJSON, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("offline Ask MCP fixture is stale; regenerate with PORT_GENERATE=1 go test ./internal/mcp -run '^TestAskOfflineMCPOracle$'\ncurrent: %s\nfixture: %s", gotJSON, wantJSON)
	}
}

func sameAskOfflineMCPCases(left, right []askOfflineMCPFixtureCase) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		a, b := left[i], right[i]
		if a.ID != b.ID || !reflect.DeepEqual(a.Documents, b.Documents) ||
			!sameAskMCPJSON(a.ExpectedTool, b.ExpectedTool) ||
			len(a.Calls) != len(b.Calls) || len(a.ProviderRequests) != len(b.ProviderRequests) {
			return false
		}
		for j := range a.Calls {
			if a.Calls[j].ID != b.Calls[j].ID || a.Calls[j].ArgumentsJSON != b.Calls[j].ArgumentsJSON ||
				!sameAskMCPJSON(a.Calls[j].Expected, b.Calls[j].Expected) {
				return false
			}
		}
	}
	return true
}

func sameAskMCPJSON(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	return json.Unmarshal(left, &leftValue) == nil && json.Unmarshal(right, &rightValue) == nil && reflect.DeepEqual(leftValue, rightValue)
}

func assertAskOfflineMCPOracleBehavior(t *testing.T, fixture askOfflineMCPFixtureCase) {
	t.Helper()
	byID := make(map[string]askOfflineMCPFixtureCall, len(fixture.Calls))
	for _, call := range fixture.Calls {
		byID[call.ID] = call
	}
	for _, id := range []string{"query-lowercase", "query-uppercase", "query-title-case", "duplicate-folded-query-last-valid"} {
		if !sameAskMCPResult(t, byID["query-lowercase"].Expected, byID[id].Expected) {
			t.Fatalf("Go Query field matching differs for %s", id)
		}
	}
	for _, id := range []string{"query-null", "arguments-null", "duplicate-folded-query-last-empty"} {
		if got := askMCPText(t, byID[id].Expected); got != "query is required" {
			t.Fatalf("Go null/missing Query result for %s = %q", id, got)
		}
	}
	if got := askMCPText(t, byID["query-wrong-type"].Expected); got != "json: cannot unmarshal number into Go struct field .query of type string" {
		t.Fatalf("Go wrong Query type error = %q", got)
	}
	if got := askMCPText(t, byID["notebook-wrong-type"].Expected); got != "json: cannot unmarshal number into Go struct field .notebook of type string" {
		t.Fatalf("Go wrong Notebook type error = %q", got)
	}
	if got := askMCPText(t, byID["notebook-kelvin-case"].Expected); got != "notebook not found" {
		t.Fatalf("Go EqualFold did not recognize Kelvin-sign Notebook key: %q", got)
	}
	if got := askMCPText(t, byID["query-whitespace"].Expected); !strings.HasSuffix(got, "Here are the most relevant search results from your vault:\\n\\n\"}") {
		t.Fatalf("Go whitespace query should succeed with an empty-result fallback, got %q", got)
	}
}

func sameAskMCPResult(t *testing.T, left, right json.RawMessage) bool {
	t.Helper()
	var leftFrame, rightFrame map[string]json.RawMessage
	if err := json.Unmarshal(left, &leftFrame); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(right, &rightFrame); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(leftFrame["result"], rightFrame["result"])
}

func askMCPText(t *testing.T, frame json.RawMessage) string {
	t.Helper()
	var response struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode Go MCP result: %v", err)
	}
	if len(response.Result.Content) != 1 {
		t.Fatalf("Go MCP content blocks = %d, want 1: %s", len(response.Result.Content), frame)
	}
	return response.Result.Content[0].Text
}

func observeAskOfflineMCPCase(t *testing.T, input askOfflineMCPFixtureCase) askOfflineMCPFixtureCase {
	t.Helper()
	home := t.TempDir()
	vaultRoot := filepath.Join(home, "vault")
	if err := os.MkdirAll(vaultRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("TMPDIR", filepath.Join(home, "tmp"))
	t.Setenv("TMP", filepath.Join(home, "tmp"))
	t.Setenv("TEMP", filepath.Join(home, "tmp"))
	for _, name := range []string{"SYMDESK_OLLAMA_URL", "SYMDESK_LLM_PROVIDER", "SYMDESK_LLM_MODEL", "SYMDESK_LLM_API_KEY", "OLLAMA_HOST"} {
		t.Setenv(name, "")
	}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "TMPDIR"} {
		if err := os.MkdirAll(os.Getenv(name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, document := range input.Documents {
		path := filepath.Join(vaultRoot, filepath.FromSlash(document.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(document.Body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshIndex(vaultRoot); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	factory := func() (*service.Service, *sidecar.DB, error) {
		opened, err := sidecar.OpenForVault(vaultRoot)
		if err != nil {
			return nil, nil, err
		}
		return service.New(vaultRoot, opened), opened, nil
	}
	entry, ok := tools.NewRegistry(tools.RegistryOptions{
		Config: &config.Config{Vault: vaultRoot}, GetService: factory, AllowWrite: false,
	}).Lookup("desk_ask")
	if !ok {
		_ = db.Close()
		t.Fatal("canonical tools registry has no desk_ask entry")
	}
	server := mcpserver.New("symdesk", "test-version")
	server.RegisterTool(adaptTool(entry))
	listRequest := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	var output bytes.Buffer
	output.Write(listRequest)
	output.WriteByte('\n')
	for index := range input.Calls {
		call := &input.Calls[index]
		request, err := json.Marshal(struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Method  string `json:"method"`
			Params  struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			} `json:"params"`
		}{
			JSONRPC: "2.0", ID: index + 2, Method: "tools/call",
			Params: struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}{Name: "desk_ask", Arguments: json.RawMessage(call.ArgumentsJSON)},
		})
		if err != nil {
			_ = db.Close()
			t.Fatalf("marshal actual Go MCP call %q: %v", call.ID, err)
		}
		output.Write(request)
		output.WriteByte('\n')
	}
	var actual bytes.Buffer
	if err := server.ServeIO(context.Background(), &output, &actual); err != nil {
		_ = db.Close()
		t.Fatalf("run actual Go MCP ServeIO: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	frames := bytes.Split(bytes.TrimSpace(actual.Bytes()), []byte{'\n'})
	if len(frames) != len(input.Calls)+1 {
		t.Fatalf("Go MCP returned %d frames, want %d: %q", len(frames), len(input.Calls)+1, actual.String())
	}
	byID := make(map[int][]byte, len(frames))
	for _, frame := range frames {
		var response struct {
			ID int `json:"id"`
		}
		if err := json.Unmarshal(frame, &response); err != nil {
			t.Fatalf("decode Go MCP response id: %v: %s", err, frame)
		}
		byID[response.ID] = frame
	}
	input.ExpectedTool = normalizeAskToolFrame(t, byID[1])
	for index := range input.Calls {
		input.Calls[index].Expected = normalizeAskMCPFrame(t, byID[index+2], vaultRoot)
	}
	input.ProviderRequests = []json.RawMessage{}
	return input
}

func normalizeAskToolFrame(t *testing.T, frame []byte) json.RawMessage {
	t.Helper()
	var response struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(frame, &response); err != nil {
		t.Fatalf("decode Go tools/list frame: %v: %s", err, frame)
	}
	if len(response.Result.Tools) != 1 {
		t.Fatalf("Go ask catalog response contains %d tools, want the actual canonical desk_ask entry: %s", len(response.Result.Tools), frame)
	}
	return response.Result.Tools[0]
}

func normalizeAskMCPFrame(t *testing.T, frame []byte, vaultRoot string) json.RawMessage {
	t.Helper()
	var value any
	if err := json.Unmarshal(frame, &value); err != nil {
		t.Fatalf("decode Go Ask MCP frame: %v: %s", err, frame)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal(encoded, &response); err != nil {
		t.Fatal(err)
	}
	if result, ok := response["result"].(map[string]any); ok {
		if content, ok := result["content"].([]any); ok {
			for _, item := range content {
				if block, ok := item.(map[string]any); ok {
					if text, ok := block["text"].(string); ok {
						block["text"] = strings.ReplaceAll(text, vaultRoot, "$VAULT")
					}
				}
			}
		}
	}
	encoded, err = json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
