package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-print/internal/config"
)

func TestToJSON(t *testing.T) {
	s, err := toJSON(map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("toJSON() error = %v", err)
	}
	if !strings.Contains(s, `"a": "b"`) {
		t.Errorf("toJSON() = %q, want it to contain %q", s, `"a": "b"`)
	}

	// Functions are not JSON-marshalable; toJSON must surface the error.
	if _, err := toJSON(func() {}); err == nil {
		t.Error("toJSON(func) error = nil, want non-nil")
	}
}

func TestEngineFromConfig(t *testing.T) {
	cfg := &config.Config{
		Engine: config.Engine{
			Typst:             "/usr/bin/typst",
			FontPaths:         []string{"/fonts"},
			IgnoreSystemFonts: true,
			TimeoutSeconds:    30,
		},
	}
	ec := engineFromConfig(cfg)
	if ec.TypstBin != "/usr/bin/typst" {
		t.Errorf("TypstBin = %q, want %q", ec.TypstBin, "/usr/bin/typst")
	}
	if len(ec.FontPaths) != 1 || ec.FontPaths[0] != "/fonts" {
		t.Errorf("FontPaths = %v, want [/fonts]", ec.FontPaths)
	}
	if !ec.IgnoreSystemFonts {
		t.Error("IgnoreSystemFonts = false, want true")
	}
	if ec.Timeout != cfg.Engine.Timeout() {
		t.Errorf("Timeout = %v, want %v", ec.Timeout, cfg.Engine.Timeout())
	}
}

// rpcLine encodes a JSON-RPC request as a single newline-delimited line, the
// "line mode" transport ServeIO also accepts (alongside Content-Length framing).
func rpcLine(t *testing.T, id int, method string, params any) string {
	t.Helper()
	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return string(b) + "\n"
}

// readResponses reads newline-delimited JSON-RPC responses from out.
func readResponses(t *testing.T, out *bytes.Buffer) []map[string]any {
	t.Helper()
	var responses []map[string]any
	scanner := bufio.NewScanner(out)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Fatalf("unmarshal response %q: %v", line, err)
		}
		responses = append(responses, resp)
	}
	return responses
}

func TestBuildServer_ToolRegistration(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	in := strings.NewReader(
		rpcLine(t, 1, "initialize", map[string]any{}) +
			rpcLine(t, 2, "tools/list", map[string]any{}),
	)
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}

	initResult, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize response missing result: %v", responses[0])
	}
	serverInfo, ok := initResult["serverInfo"].(map[string]any)
	if !ok || serverInfo["name"] != "symprint" {
		t.Errorf("serverInfo = %v, want name=symprint", initResult["serverInfo"])
	}
	if initResult["instructions"] == "" || initResult["instructions"] == nil {
		t.Error("initialize result missing non-empty instructions")
	}

	listResult, ok := responses[1]["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list response missing result: %v", responses[1])
	}
	tools, ok := listResult["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list result.tools is not an array: %v", listResult)
	}

	wantTools := map[string]bool{
		"list_profiles":     false,
		"doctor":            false,
		"validate_document": false,
		"render_pdf":        false,
	}
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		if _, known := wantTools[name]; known {
			wantTools[name] = true
		}
	}
	for name, found := range wantTools {
		if !found {
			t.Errorf("tool %q was not registered", name)
		}
	}
}

func TestBuildServer_ListProfiles(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name":      "list_profiles",
		"arguments": map[string]any{},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("list_profiles response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("list_profiles returned isError=true: %v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("list_profiles result.content empty or missing: %v", result)
	}
}

func TestBuildServer_Doctor(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name":      "doctor",
		"arguments": map[string]any{},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("doctor response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("doctor returned isError=true: %v", result)
	}
}

func TestBuildServer_ValidateDocument(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	markdown := "---\nprofile: report\ntitle: Test\n---\n\nHello world.\n"
	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "validate_document",
		"arguments": map[string]any{
			"markdown": markdown,
		},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("validate_document response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("validate_document returned isError=true: %v", result)
	}
}

func TestBuildServer_ValidateDocument_InvalidArguments(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	// "arguments" is a JSON array, which cannot unmarshal into the handler's
	// struct{Markdown, Profile} args — exercises the unmarshal-error branch.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"validate_document","arguments":["not","an","object"]}}` + "\n")
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("validate_document response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("validate_document with malformed arguments: isError = false, want true: %v", result)
	}
}

func TestBuildServer_ValidateDocument_ParseError(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	// Closed frontmatter block with an unknown key: KnownFields(true) rejects it.
	markdown := "---\nnot_a_real_field: oops\n---\n\nBody.\n"
	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "validate_document",
		"arguments": map[string]any{
			"markdown": markdown,
		},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("validate_document response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("validate_document with malformed frontmatter: isError = false, want true: %v", result)
	}
}

func TestBuildServer_ValidateDocument_FallsBackToConfigDefaultProfile(t *testing.T) {
	cfg := &config.Config{Defaults: config.Defaults{Profile: "report"}}
	s := buildServer(cfg)

	// No "profile" argument and no profile in frontmatter: falls back to cfg.Defaults.Profile.
	markdown := "---\ntitle: Test\n---\n\nHello.\n"
	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "validate_document",
		"arguments": map[string]any{
			"markdown": markdown,
		},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("validate_document response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); isErr {
		t.Fatalf("validate_document returned isError=true: %v", result)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("validate_document result.content empty or missing: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] is not an object: %v", content[0])
	}
	text, _ := first["text"].(string)
	if !strings.Contains(text, `"profile": "report"`) {
		t.Errorf("validate_document text = %q, want it to report profile %q (the config default)", text, "report")
	}
}

func TestBuildServer_ValidateDocument_RequiredFieldError(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	// "report" requires "title", which is omitted, so Validate() returns a
	// severity "error" issue and the handler must flip ok=false.
	markdown := "---\nprofile: report\n---\n\nHello.\n"
	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "validate_document",
		"arguments": map[string]any{
			"markdown": markdown,
		},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("validate_document response missing result: %v", responses[0])
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("validate_document result.content empty or missing: %v", result)
	}
	first, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content[0] is not an object: %v", content[0])
	}
	text, _ := first["text"].(string)
	if !strings.Contains(text, `"ok": false`) {
		t.Errorf("validate_document text = %q, want it to report ok=false for a missing required field", text)
	}
}

func TestBuildServer_ValidateDocument_UnknownProfile(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	markdown := "---\nprofile: does-not-exist\ntitle: Test\n---\n\nHello world.\n"
	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "validate_document",
		"arguments": map[string]any{
			"markdown": markdown,
		},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("validate_document response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("validate_document with unknown profile: isError = false, want true: %v", result)
	}
}

func TestBuildServer_RenderPDF_MissingOutputPath(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "render_pdf",
		"arguments": map[string]any{
			"markdown": "---\nprofile: report\ntitle: Test\n---\n\nHello.\n",
		},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("render_pdf response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("render_pdf without output_path: isError = false, want true: %v", result)
	}
}

func TestBuildServer_RenderPDF_InvalidArguments(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	// "arguments" is a JSON array, which cannot unmarshal into the handler's
	// args struct — exercises the unmarshal-error branch.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"render_pdf","arguments":["not","an","object"]}}` + "\n")
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	result, ok := responses[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("render_pdf response missing result: %v", responses[0])
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("render_pdf with malformed arguments: isError = false, want true: %v", result)
	}
}

func TestBuildServer_RenderPDF_BuildsRequestAndInvokesRender(t *testing.T) {
	cfg := config.Default()
	s := buildServer(cfg)

	outputPath := t.TempDir() + "/out.pdf"
	in := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "render_pdf",
		"arguments": map[string]any{
			"markdown":     "---\nprofile: report\ntitle: Test\n---\n\nHello.\n",
			"output_path":  outputPath,
			"profile":      "report",
			"pdf_standard": []string{"a-2a"},
			"reproducible": true,
		},
	}))
	var out bytes.Buffer

	if err := s.ServeIO(context.Background(), in, &out); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}

	responses := readResponses(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	// The result depends on whether a typst binary is available in this
	// environment; either outcome exercises building the press.Request and
	// calling press.Render, which is all this test needs to cover.
	if _, ok := responses[0]["result"]; !ok {
		t.Fatalf("render_pdf response missing result: %v", responses[0])
	}
}

func TestStartServer_ReturnsOnImmediateEOF(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	cfg := config.Default()
	if err := StartServer(context.Background(), cfg); err != nil {
		t.Fatalf("StartServer() error = %v", err)
	}
}

func TestStartServer_WrapsServeError(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	// A header-shaped line (contains ":", doesn't start with "{") with no
	// Content-Length field is a framing protocol violation that ServeIO
	// surfaces as a non-EOF read error, which StartServer must wrap.
	if _, err := w.WriteString("X-Custom: 1\n\n"); err != nil {
		t.Fatalf("write malformed frame: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	err = StartServer(context.Background(), config.Default())
	if err == nil {
		t.Fatal("StartServer() error = nil, want a wrapped read error")
	}
	if !strings.Contains(err.Error(), "mcp server:") {
		t.Errorf("StartServer() error = %q, want it prefixed with %q", err.Error(), "mcp server:")
	}
}

func TestBuildServer_RenderPDF_PathContainment(t *testing.T) {
	tempDir := t.TempDir()
	allowedRoot := filepath.Join(tempDir, "allowed")
	if err := os.MkdirAll(allowedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	cfg := config.Default()
	cfg.MCP.OutputRoot = allowedRoot
	s := buildServer(cfg)

	// Case 1: inside allowed root (should pass containment check)
	insidePath := filepath.Join(allowedRoot, "out.pdf")
	inInside := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "render_pdf",
		"arguments": map[string]any{
			"markdown":    "---\nprofile: report\ntitle: Test\n---\n\nHello.\n",
			"output_path": insidePath,
		},
	}))
	var outInside bytes.Buffer
	if err := s.ServeIO(context.Background(), inInside, &outInside); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}
	resInside := readResponses(t, &outInside)[0]
	resultInside, ok := resInside["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing: %v", resInside)
	}
	isErrInside, _ := resultInside["isError"].(bool)
	// It shouldn't fail with the path containment error (might fail due to typst missing, but that's normal and doesn't mention containment).
	if isErrInside {
		content, _ := resultInside["content"].([]any)
		if len(content) > 0 {
			msg, _ := content[0].(map[string]any)["text"].(string)
			if strings.Contains(msg, "outside the allowed output root") {
				t.Errorf("expected inside path to pass containment, but failed: %s", msg)
			}
		}
	}

	// Case 2: outside allowed root
	outsidePath := filepath.Join(tempDir, "outside.pdf")
	inOutside := strings.NewReader(rpcLine(t, 2, "tools/call", map[string]any{
		"name": "render_pdf",
		"arguments": map[string]any{
			"markdown":    "---\nprofile: report\ntitle: Test\n---\n\nHello.\n",
			"output_path": outsidePath,
		},
	}))
	var outOutside bytes.Buffer
	if err := s.ServeIO(context.Background(), inOutside, &outOutside); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}
	resOutside := readResponses(t, &outOutside)[0]
	resultOutside, ok := resOutside["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing: %v", resOutside)
	}
	isErrOutside, _ := resultOutside["isError"].(bool)
	if !isErrOutside {
		t.Error("expected outside path to be rejected, but succeeded")
	} else {
		content, _ := resultOutside["content"].([]any)
		if len(content) > 0 {
			msg, _ := content[0].(map[string]any)["text"].(string)
			if !strings.Contains(msg, "outside the allowed output root") {
				t.Errorf("expected error message to mention 'outside the allowed output root', got: %q", msg)
			}
		} else {
			t.Error("missing error content")
		}
	}
}

func TestBuildServer_RenderPDF_OverwriteProtection(t *testing.T) {
	tempDir := t.TempDir()
	nonPDFPath := filepath.Join(tempDir, "existing_text.txt")
	if err := os.WriteFile(nonPDFPath, []byte("some text content"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cfg := config.Default()
	s := buildServer(cfg)

	// Case 1: Overwrite existing non-PDF with overwrite=false (rejected)
	in1 := strings.NewReader(rpcLine(t, 1, "tools/call", map[string]any{
		"name": "render_pdf",
		"arguments": map[string]any{
			"markdown":    "---\nprofile: report\ntitle: Test\n---\n\nHello.\n",
			"output_path": nonPDFPath,
			"overwrite":   false,
		},
	}))
	var out1 bytes.Buffer
	if err := s.ServeIO(context.Background(), in1, &out1); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}
	res1 := readResponses(t, &out1)[0]
	result1, ok := res1["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing: %v", res1)
	}
	isErr1, _ := result1["isError"].(bool)
	if !isErr1 {
		t.Error("expected overwrite of non-PDF to fail with overwrite=false, but succeeded")
	} else {
		content, _ := result1["content"].([]any)
		if len(content) > 0 {
			msg, _ := content[0].(map[string]any)["text"].(string)
			if !strings.Contains(msg, "refusing to overwrite existing non-PDF file") {
				t.Errorf("expected error message to mention overwrite refusal, got: %q", msg)
			}
		}
	}

	// Case 2: Overwrite existing non-PDF with overwrite=true (passes overwrite check)
	in2 := strings.NewReader(rpcLine(t, 2, "tools/call", map[string]any{
		"name": "render_pdf",
		"arguments": map[string]any{
			"markdown":    "---\nprofile: report\ntitle: Test\n---\n\nHello.\n",
			"output_path": nonPDFPath,
			"overwrite":   true,
		},
	}))
	var out2 bytes.Buffer
	if err := s.ServeIO(context.Background(), in2, &out2); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}
	res2 := readResponses(t, &out2)[0]
	result2, ok := res2["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing: %v", res2)
	}
	isErr2, _ := result2["isError"].(bool)
	if isErr2 {
		content, _ := result2["content"].([]any)
		if len(content) > 0 {
			msg, _ := content[0].(map[string]any)["text"].(string)
			if strings.Contains(msg, "refusing to overwrite") {
				t.Errorf("expected overwrite=true to bypass overwrite check, but failed: %s", msg)
			}
		}
	}

	// Case 3: Overwrite existing PDF with overwrite=false (passes overwrite check)
	pdfPath := filepath.Join(tempDir, "existing_pdf.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4\n..."), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	in3 := strings.NewReader(rpcLine(t, 3, "tools/call", map[string]any{
		"name": "render_pdf",
		"arguments": map[string]any{
			"markdown":    "---\nprofile: report\ntitle: Test\n---\n\nHello.\n",
			"output_path": pdfPath,
			"overwrite":   false,
		},
	}))
	var out3 bytes.Buffer
	if err := s.ServeIO(context.Background(), in3, &out3); err != nil {
		t.Fatalf("ServeIO() error = %v", err)
	}
	res3 := readResponses(t, &out3)[0]
	result3, ok := res3["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing: %v", res3)
	}
	isErr3, _ := result3["isError"].(bool)
	if isErr3 {
		content, _ := result3["content"].([]any)
		if len(content) > 0 {
			msg, _ := content[0].(map[string]any)["text"].(string)
			if strings.Contains(msg, "refusing to overwrite") {
				t.Errorf("expected existing PDF to be overwriteable with overwrite=false, but failed: %s", msg)
			}
		}
	}
}

// captureFD redirects *fd (os.Stdout or os.Stderr) to a pipe for the duration
// of fn and returns everything written to it.
func captureFD(t *testing.T, fd **os.File, fn func()) string {
	t.Helper()
	orig := *fd
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	*fd = w
	defer func() {
		*fd = orig
		w.Close()
	}()
	fn()
	w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

func TestWarnOnEmptyOutputRoot(t *testing.T) {
	cfg := config.Default()
	if cfg.MCP.OutputRoot != "" {
		t.Fatalf("precondition: Default() MCP.OutputRoot = %q, want empty", cfg.MCP.OutputRoot)
	}

	var buf bytes.Buffer
	warnOnEmptyOutputRoot(&buf, cfg)
	if !strings.Contains(buf.String(), "warning: mcp.output_root is not set") {
		t.Errorf("warnOnEmptyOutputRoot() = %q, want a one-line warning about the unset output_root", buf.String())
	}

	buf.Reset()
	cfg.MCP.OutputRoot = t.TempDir()
	warnOnEmptyOutputRoot(&buf, cfg)
	if buf.Len() != 0 {
		t.Errorf("warnOnEmptyOutputRoot() with explicit root = %q, want no output", buf.String())
	}
}

func TestStartServer_WarnsOnStderr_NotStdout(t *testing.T) {
	cfg := config.Default() // MCP.OutputRoot is empty: startup warning expected.

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()
	// One initialize request, then EOF: the server must answer on stdout and
	// return, with the startup warning on stderr and nothing else on stdout.
	if _, err := w.WriteString(rpcLine(t, 1, "initialize", map[string]any{})); err != nil {
		t.Fatalf("write initialize request: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	var stdout string
	stderr := captureFD(t, &os.Stderr, func() {
		stdout = captureFD(t, &os.Stdout, func() {
			if err := StartServer(context.Background(), cfg); err != nil {
				t.Fatalf("StartServer() error = %v", err)
			}
		})
	})

	if !strings.Contains(stderr, "warning: mcp.output_root is not set") {
		t.Errorf("stderr = %q, want a startup warning about the unset output_root", stderr)
	}
	if strings.Contains(stdout, "warning") {
		t.Errorf("warning leaked onto stdout: %q", stdout)
	}
	// Zero stdio pollution: every non-empty stdout line is JSON-RPC.
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var resp map[string]any
		if err := json.Unmarshal([]byte(line), &resp); err != nil {
			t.Errorf("stdout line %q is not JSON-RPC: %v", line, err)
		}
	}
}

func TestStartServer_NoWarningWhenOutputRootSet(t *testing.T) {
	cfg := config.Default()
	cfg.MCP.OutputRoot = t.TempDir()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}

	stderr := captureFD(t, &os.Stderr, func() {
		if err := StartServer(context.Background(), cfg); err != nil {
			t.Fatalf("StartServer() error = %v", err)
		}
	})
	if strings.Contains(stderr, "output_root") {
		t.Errorf("stderr = %q, want no output_root warning when the root is set", stderr)
	}
}

func TestCheckContainment_EmptyRootAllowsAnyPath(t *testing.T) {
	// Containment is opt-in: with no output_root configured, any absolute
	// output path is allowed and the startup warning is the only mitigation.
	if err := checkContainment("/some/absolute/path/out.pdf", ""); err != nil {
		t.Errorf("checkContainment() with empty root error = %v, want nil", err)
	}
}

func TestCheckContainment_ExplicitRootEnforced(t *testing.T) {
	tempDir := t.TempDir()
	allowedRoot := filepath.Join(tempDir, "allowed")
	if err := os.MkdirAll(allowedRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	if err := checkContainment(filepath.Join(allowedRoot, "out.pdf"), allowedRoot); err != nil {
		t.Errorf("inside root: error = %v, want nil", err)
	}
	err := checkContainment(filepath.Join(tempDir, "outside.pdf"), allowedRoot)
	if err == nil {
		t.Fatal("outside root: error = nil, want containment error")
	}
	if !strings.Contains(err.Error(), "outside the allowed output root") {
		t.Errorf("outside root: error = %q, want a mention of containment", err)
	}
}
