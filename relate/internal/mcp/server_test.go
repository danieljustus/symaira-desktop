package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/mcpserver"
	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/contact"
)

func testApp(t *testing.T) *app.App {
	t.Helper()
	a, err := app.OpenMemory(context.Background())
	if err != nil {
		t.Fatalf("app.OpenMemory() error = %v", err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

// call sends one JSON-RPC request line through Server.Run and returns the
// decoded response. Run is driven over an in-memory pipe so this exercises
// the exact stdio framing symrelate mcp uses in production (mcpserver
// accepts newline-delimited JSON as well as Content-Length framing).
func call(t *testing.T, s *Server, id int, method string, params any) map[string]any {
	t.Helper()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	reqLine, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var stdout bytes.Buffer
	stdin := bytes.NewReader(append(reqLine, '\n'))
	if err := s.Run(context.Background(), stdin, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, stdout.String())
	}
	return resp
}

// toolTextJSON parses the text content block of a tool-call result into
// its JSON value. mcpserver's tool-result envelope is
// {content: [{type: "text", text}], isError}, where text carries the
// compact JSON of the handler's return value.
func toolTextJSON(t *testing.T, result map[string]any) any {
	t.Helper()
	content, _ := result["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("expected one content block, got %v", result["content"])
	}
	block, _ := content[0].(map[string]any)
	text, _ := block["text"].(string)
	if text == "" {
		t.Fatalf("expected non-empty text content in %v", result)
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("text content is not valid JSON: %v (%s)", err, text)
	}
	return v
}

func TestServer_StdoutIsProtocolClean(t *testing.T) {
	s := New(testApp(t))
	var stdout bytes.Buffer
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	if err := s.Run(context.Background(), stdin, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	scanner := bufio.NewScanner(&stdout)
	lines := 0
	for scanner.Scan() {
		lines++
		var v map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &v); err != nil {
			t.Errorf("stdout line %d is not valid JSON: %v (%s)", lines, err, scanner.Text())
		}
	}
	if lines != 1 {
		t.Errorf("expected exactly 1 stdout line, got %d", lines)
	}
}

func TestServer_Initialize(t *testing.T) {
	resp := call(t, New(testApp(t)), 1, "initialize", nil)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in %v", resp)
	}
	if result["protocolVersion"] != mcpserver.ProtocolVersion {
		t.Errorf("protocolVersion = %v, want %v", result["protocolVersion"], mcpserver.ProtocolVersion)
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != "symrelate" {
		t.Errorf("serverInfo.name = %v, want symrelate", serverInfo["name"])
	}
}

func TestServer_ToolsList_SnakeCaseNames(t *testing.T) {
	resp := call(t, New(testApp(t)), 1, "tools/list", nil)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in %v", resp)
	}
	tools, _ := result["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("expected at least one tool")
	}
	want := map[string]bool{
		"contact_search": false, "contact_get": false, "contact_create": false, "contact_update": false,
		"organization_search": false, "organization_get": false, "followup_list": false, "timeline_get": false,
	}
	for _, raw := range tools {
		tl := raw.(map[string]any)
		name, _ := tl["name"].(string)
		if name != strings.ToLower(name) || strings.Contains(name, "-") {
			t.Errorf("tool name %q is not snake_case", name)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
		if _, ok := tl["inputSchema"].(map[string]any); !ok {
			t.Errorf("tool %q missing inputSchema", name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("expected tool %q not found in tools/list", name)
		}
	}
}

func TestServer_ToolsCall_ContactCreateAndGet(t *testing.T) {
	s := New(testApp(t))

	createResp := call(t, s, 1, "tools/call", map[string]any{
		"name":      "contact_create",
		"arguments": map[string]any{"display_name": "Ada Lovelace", "email": "ada@example.com"},
	})
	createResult, ok := createResp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in %v", createResp)
	}
	if createResult["isError"] != false {
		t.Fatalf("contact_create reported an error: %v", createResult)
	}
	structured := toolTextJSON(t, createResult).(map[string]any)
	id, _ := structured["ID"].(string)
	if id == "" {
		t.Fatalf("created contact missing ID: %v", structured)
	}

	getResp := call(t, s, 2, "tools/call", map[string]any{
		"name":      "contact_get",
		"arguments": map[string]any{"id": id},
	})
	getResult := getResp["result"].(map[string]any)
	got := toolTextJSON(t, getResult).(map[string]any)
	if got["DisplayName"] != "Ada Lovelace" {
		t.Errorf("DisplayName = %v, want Ada Lovelace", got["DisplayName"])
	}
}

// An unknown tool name is a protocol-level mistake by the caller (it can
// never succeed by retrying with different arguments), so it is reported
// as a JSON-RPC error (CodeMethodNotFound, per corekit/mcpserver) — unlike
// a valid tool that fails during execution (not-found, validation, ...),
// which is reported in-band via isError so a client can distinguish "the
// tool ran and failed" from "the call itself was invalid".
func TestServer_ToolsCall_UnknownTool_IsRPCError(t *testing.T) {
	resp := call(t, New(testApp(t)), 1, "tools/call", map[string]any{"name": "does_not_exist", "arguments": map[string]any{}})
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an RPC error for an unknown tool, got %v", resp)
	}
	if code, _ := errObj["code"].(float64); int(code) != mcpserver.CodeMethodNotFound {
		t.Errorf("code = %v, want %d", errObj["code"], mcpserver.CodeMethodNotFound)
	}
}

func TestServer_ToolsCall_ContactGet_NotFound_IsInBandError(t *testing.T) {
	resp := call(t, New(testApp(t)), 1, "tools/call", map[string]any{
		"name":      "contact_get",
		"arguments": map[string]any{"id": "does-not-exist"},
	})
	result := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Errorf("expected isError=true for a not-found contact, got %v", result)
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	resp := call(t, New(testApp(t)), 1, "bogus/method", nil)
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error response, got %v", resp)
	}
	if code, _ := errObj["code"].(float64); int(code) != mcpserver.CodeMethodNotFound {
		t.Errorf("code = %v, want %d", errObj["code"], mcpserver.CodeMethodNotFound)
	}
}

func TestServer_MalformedJSON_ReturnsParseError_NotCrash(t *testing.T) {
	s := New(testApp(t))
	var stdout bytes.Buffer
	stdin := strings.NewReader("{not json\n")
	if err := s.Run(context.Background(), stdin, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, stdout.String())
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a parse error response, got %v", resp)
	}
	if code, _ := errObj["code"].(float64); int(code) != mcpserver.CodeParseError {
		t.Errorf("code = %v, want %d", errObj["code"], mcpserver.CodeParseError)
	}
}

func TestServer_Notification_GetsNoResponse(t *testing.T) {
	s := New(testApp(t))
	var stdout bytes.Buffer
	// No "id" field: this is a notification per JSON-RPC 2.0 and must not
	// produce a response line. notifications/initialized is the MCP
	// lifecycle notification clients actually send; mcpserver acknowledges
	// it silently.
	stdin := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	if err := s.Run(context.Background(), stdin, &stdout); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("expected no stdout output for a notification, got %q", stdout.String())
	}
}

func TestServer_UnknownFieldInArguments_IsRejected(t *testing.T) {
	resp := call(t, New(testApp(t)), 1, "tools/call", map[string]any{
		"name":      "contact_create",
		"arguments": map[string]any{"display_name": "X", "not_a_real_field": "y"},
	})
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result in %v", resp)
	}
	if result["isError"] != true {
		t.Errorf("expected isError=true for an unknown argument field, got %v", result)
	}
}

// Regression net over the full 8-tool catalog: every tool must answer a
// minimal valid call with isError=false and a JSON-parseable text payload
// over the corekit transport.
func TestServer_ToolsCall_CatalogRegression(t *testing.T) {
	ctx := context.Background()
	a := testApp(t)
	s := New(a)

	person, err := a.Contacts.CreatePerson(ctx, contact.PersonInput{DisplayName: "Grace Hopper"})
	if err != nil {
		t.Fatalf("seed person: %v", err)
	}
	org, err := a.Contacts.CreateOrganization(ctx, contact.OrganizationInput{Name: "ACME"})
	if err != nil {
		t.Fatalf("seed org: %v", err)
	}

	toolOK := func(name string, args map[string]any) {
		t.Helper()
		resp := call(t, s, 1, "tools/call", map[string]any{"name": name, "arguments": args})
		result, ok := resp["result"].(map[string]any)
		if !ok {
			t.Fatalf("%s: expected a result envelope, got %v", name, resp)
		}
		if result["isError"] != false {
			t.Fatalf("%s: unexpected isError=true: %v", name, result)
		}
		toolTextJSON(t, result) // text block must be parseable JSON
	}

	toolOK("contact_search", map[string]any{"query": "Grace"})
	toolOK("contact_get", map[string]any{"id": person.ID})
	toolOK("contact_create", map[string]any{"display_name": "Katherine Johnson"})
	toolOK("contact_update", map[string]any{"id": person.ID, "display_name": "Grace B. Hopper"})
	toolOK("organization_search", map[string]any{"query": ""})
	toolOK("organization_get", map[string]any{"id": org.ID})
	toolOK("followup_list", map[string]any{"person_id": person.ID})
	toolOK("timeline_get", map[string]any{"person_id": person.ID})
}

// The PII boundary must hold on the wire: an error message carrying a
// contact-point value is masked by redactErr before mcpserver can put it
// into the client-visible text block.
func TestRedactErr_MasksContactPoints(t *testing.T) {
	err := redactErr(fmt.Errorf("duplicate contact point ada@example.com (+49 170 1234567) rejected"))
	msg := err.Error()
	if strings.Contains(msg, "ada@example.com") {
		t.Errorf("error message reaches client unredacted: %q", msg)
	}
	if strings.Contains(msg, "170 1234567") {
		t.Errorf("error message reaches client unredacted: %q", msg)
	}
}
