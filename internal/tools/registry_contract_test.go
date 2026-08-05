package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
)

// toolContract is the exact, canonical contract for one registry entry:
// name, description, input schema and capability flag. Every value is
// transcribed from tools.go so drift in the shared registry is caught here.
type toolContract struct {
	name        string
	description string
	schema      string
	readOnly    bool
}

// toolContracts is ordered exactly like NewRegistry's catalog so the test can
// also pin the canonical tool order.
var toolContracts = []toolContract{
	{
		name:        "desk_status",
		description: "Returns the current version and vault path configuration for symdesk.",
		schema:      `{"type":"object","properties":{}}`,
		readOnly:    true,
	},
	{
		name:        "desk_ls",
		description: "Lists files in the vault.",
		schema:      `{"type":"object","properties":{"dir":{"type":"string"}}}`,
		readOnly:    true,
	},
	{
		name:        "desk_search",
		description: "Searches notes with full-text terms plus path:, tag:, type:, status:, quoted phrases, -negation and /regex/. Invalid syntax falls back to plain full-text and returns a hint.",
		schema:      `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_props",
		description: "Gets properties (frontmatter) for a note.",
		schema:      `{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_backlinks",
		description: "Gets backlinks for a note.",
		schema:      `{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_ask",
		description: "Asks the AI a question about the vault. Uses a local Ollama instance when configured; otherwise returns the top search results with a note that AI is not configured. The answer is returned as one aggregated text (no streaming).",
		schema:      `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_transform",
		description: "Transforms the given text with a local AI action. intent is one of summarize, rewrite or continue. Uses a local Ollama instance when configured; otherwise returns a note that AI is not configured. The result is returned as one aggregated text (no streaming).",
		schema:      `{"type":"object","properties":{"text":{"type":"string"},"intent":{"type":"string","enum":["summarize","rewrite","continue"]}},"required":["text","intent"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_docs",
		description: "Lists indexed documents with optional filters (status, person, correspondent, type, year, due-before, min/max confidence). Returns structured document metadata.",
		schema:      `{"type":"object","properties":{"type":{"type":"string"},"status":{"type":"string"},"person":{"type":"string"},"correspondent":{"type":"string"},"year":{"type":"string"},"due_before":{"type":"string"},"min_confidence":{"type":"integer"},"max_confidence":{"type":"integer"}}}`,
		readOnly:    true,
	},
	{
		name:        "docs_review",
		description: "Returns documents needing review: confidence below threshold or missing document_type/document_date.",
		schema:      `{"type":"object","properties":{"threshold":{"type":"integer"}}}`,
		readOnly:    true,
	},
	{
		name:        "docs_similar",
		description: "Finds near-duplicate documents using SimHash similarity. Returns documents with similarity >= threshold.",
		schema:      `{"type":"object","properties":{"file":{"type":"string"},"threshold":{"type":"integer"}},"required":["file"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_related",
		description: "Gets related entities and notes for a given file path based on composition with symmemory.",
		schema:      `{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_ingest_jobs",
		description: "Lists ingestion jobs in the queue from symingest.",
		schema:      `{"type":"object","properties":{}}`,
		readOnly:    true,
	},
	{
		name:        "meeting_list",
		description: "Lists meetings already imported into the vault as reviewed meeting notes.",
		schema:      `{"type":"object","properties":{}}`,
		readOnly:    true,
	},
	{
		name:        "meeting_get",
		description: "Gets one imported meeting note by its vault-relative path, including the reviewed transcript.",
		schema:      `{"type":"object","properties":{"path":{"type":"string","description":"vault-relative meeting note path"}},"required":["path"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_note_new",
		description: "Create a new note in the Symaira vault.",
		schema:      `{"type":"object","properties":{"title":{"type":"string","description":"The title of the new note"},"content":{"type":"string","description":"The Markdown body content of the note"},"template":{"type":"string","description":"Optional template name to use"}},"required":["title","content"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_ingest",
		description: "Ingests a file into the vault: copies it into inbox/ and creates a corresponding markdown note. Takes an absolute source path, returns the relative path of the new note.",
		schema:      `{"type":"object","properties":{"source_path":{"type":"string"}},"required":["source_path"]}`,
		readOnly:    false,
	},
	{
		name:        "doc_set_status",
		description: "Sets the status of a document (open|paid|submitted|done|needs_review|waiting_for_reply). Updates frontmatter and re-indexes.",
		schema:      `{"type":"object","properties":{"file":{"type":"string"},"status":{"type":"string"}},"required":["file","status"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_ingest_retry",
		description: "Retries a failed ingestion job by ID.",
		schema:      `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_clip",
		description: "Fetches a URL via symfetch and saves it as a note in the vault.",
		schema:      `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_export",
		description: "Exports a note or view to PDF or HTML. Provide either note or view, not both.",
		schema:      `{"type":"object","properties":{"note":{"type":"string","description":"vault-relative note path"},"view":{"type":"string","description":"view id"},"output":{"type":"string","description":"output file path"},"format":{"type":"string","enum":["pdf","html"]},"profile":{"type":"string","description":"symprint profile for PDF"}},"oneOf":[{"required":["note"]},{"required":["view"]}]}`,
		readOnly:    false,
	},
	{
		name:        "desk_autofill",
		description: "Autofills a frontmatter property on all notes matching a view using the configured AI provider.",
		schema:      `{"type":"object","properties":{"view":{"type":"string","description":"view id"},"property":{"type":"string","description":"frontmatter property to fill"},"prompt":{"type":"string","description":"extra instruction for the AI"},"dry_run":{"type":"boolean","description":"show changes without writing"}},"required":["view","property"]}`,
		readOnly:    false,
	},
	{
		name:        "meeting_import",
		description: "Imports one SymMeet meeting into the vault as a contract-v2 meeting note. Requires symmeet on PATH with a compatible artifact schema.",
		schema:      `{"type":"object","properties":{"meeting_id":{"type":"string"}},"required":["meeting_id"]}`,
		readOnly:    false,
	},
}

func contractByName() map[string]toolContract {
	byName := make(map[string]toolContract, len(toolContracts))
	for _, c := range toolContracts {
		byName[c.name] = c
	}
	return byName
}

func testRegistryOptions() RegistryOptions {
	return RegistryOptions{
		Config:        &config.Config{Vault: "/test/vault"},
		ServerVersion: "test-version",
		AllowWrite:    true,
	}
}

// testServiceFactory opens a fresh sidecar DB and service for one request,
// mirroring the factory the MCP transport tests use.
func testServiceFactory(t *testing.T) ServiceFactory {
	t.Helper()
	vaultRoot := t.TempDir()
	return func() (*service.Service, *sidecar.DB, error) {
		db, err := sidecar.Open(filepath.Join(vaultRoot, "sidecar.db"))
		if err != nil {
			return nil, nil, err
		}
		return service.New(vaultRoot, db), db, nil
	}
}

func errorServiceFactory() ServiceFactory {
	return func() (*service.Service, *sidecar.DB, error) {
		return nil, nil, fmt.Errorf("service unavailable")
	}
}

// restrictPATH hides the symaira companion binaries (symingest, symfetch,
// symmeet, symmemory, symprint) so external-tool paths fail deterministically.
func restrictPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", "/usr/bin:/bin")
	compose.ResetCache()
	t.Cleanup(compose.ResetCache)
}

func assertContract(t *testing.T, got Tool, want toolContract, checkReadOnly bool) {
	t.Helper()
	if got.Name != want.name {
		t.Errorf("tool name = %q, want %q", got.Name, want.name)
	}
	if got.Description != want.description {
		t.Errorf("tool %q description = %q, want %q", got.Name, got.Description, want.description)
	}
	if checkReadOnly && got.ReadOnly != want.readOnly {
		t.Errorf("tool %q read-only = %v, want %v", got.Name, got.ReadOnly, want.readOnly)
	}
	if got.Handler == nil {
		t.Errorf("tool %q has nil handler", got.Name)
	}
	if !json.Valid(got.InputSchema) {
		t.Fatalf("tool %q input schema is not valid JSON: %q", got.Name, got.InputSchema)
	}
	var gotSchema, wantSchema any
	if err := json.Unmarshal(got.InputSchema, &gotSchema); err != nil {
		t.Fatalf("tool %q: decode input schema: %v", got.Name, err)
	}
	if err := json.Unmarshal([]byte(want.schema), &wantSchema); err != nil {
		t.Fatalf("tool %q: decode expected schema: %v", got.Name, err)
	}
	if !reflect.DeepEqual(gotSchema, wantSchema) {
		t.Errorf("tool %q input schema = %s, want %s", got.Name, got.InputSchema, want.schema)
	}
}

// TestToolContractTable pins the canonical registry contract: every entry
// must carry the exact name, description, input schema and capability flag
// in the canonical order.
func TestToolContractTable(t *testing.T) {
	registry := NewRegistry(testRegistryOptions())
	entries := registry.All()
	if len(entries) != len(toolContracts) {
		t.Fatalf("registry has %d tools, contract table has %d", len(entries), len(toolContracts))
	}
	for i, want := range toolContracts {
		assertContract(t, entries[i], want, true)
		if got, ok := registry.Lookup(want.name); !ok {
			t.Errorf("registry lookup missing canonical tool %q", want.name)
		} else {
			assertContract(t, got, want, true)
		}
	}
}

// TestDefinitionsMatchesRegistry pins Definitions as the constructor-backed
// catalog: same entries, same order, and it works with a nil config.
func TestDefinitionsMatchesRegistry(t *testing.T) {
	got := Definitions(testRegistryOptions())
	want := NewRegistry(testRegistryOptions()).All()
	if len(got) != len(want) {
		t.Fatalf("Definitions() returned %d tools, NewRegistry().All() returned %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Name != want[i].Name {
			t.Errorf("Definitions()[%d].Name = %q, want %q", i, got[i].Name, want[i].Name)
		}
		if got[i].Description != want[i].Description {
			t.Errorf("Definitions()[%d].Description differs for %q", i, got[i].Name)
		}
	}

	// Definitions must not panic and must still produce the full catalog when
	// the caller passes zero options (nil config, nil factory).
	zero := Definitions(RegistryOptions{})
	if len(zero) != len(toolContracts) {
		t.Errorf("Definitions(RegistryOptions{}) returned %d tools, want %d", len(zero), len(toolContracts))
	}
}

// TestConstructorsExposeCanonicalContract calls every constructor directly
// (not through NewRegistry) and checks the returned Tool against the
// canonical contract, so a constructor that drifts from the registry is
// caught on both sides of the seam.
func TestConstructorsExposeCanonicalContract(t *testing.T) {
	byName := contractByName()
	constructors := []struct {
		label string
		build func() *Tool
	}{
		{"newStatusTool", func() *Tool { return newStatusTool(&config.Config{Vault: "/test/vault"}, true, "v1") }},
		{"newLsTool", func() *Tool { return newLsTool(nil) }},
		{"newSearchTool", func() *Tool { return newSearchTool(nil) }},
		{"newPropsTool", func() *Tool { return newPropsTool(nil) }},
		{"newBacklinksTool", func() *Tool { return newBacklinksTool(nil) }},
		{"newAskTool", func() *Tool { return newAskTool(nil) }},
		{"newTransformTool-default-config", func() *Tool { return newTransformTool() }},
		{"newTransformTool-explicit-config", func() *Tool { return newTransformTool(&config.Config{Vault: "/test/vault"}) }},
		{"newDocsTool", func() *Tool { return newDocsTool(nil) }},
		{"newDocsReviewTool", func() *Tool { return newDocsReviewTool(nil, &config.Config{ReviewThreshold: 85}) }},
		{"newDocsSimilarTool", func() *Tool { return newDocsSimilarTool(nil) }},
		{"newRelatedTool", func() *Tool { return newRelatedTool(nil) }},
		{"newIngestJobsTool", func() *Tool { return newIngestJobsTool(nil) }},
		{"newMeetingListTool", func() *Tool { return newMeetingListTool(nil) }},
		{"newMeetingGetTool", func() *Tool { return newMeetingGetTool(nil) }},
		{"newNoteNewTool", func() *Tool { return newNoteNewTool(nil) }},
		{"newIngestTool", func() *Tool { return newIngestTool(nil) }},
		{"newDocSetStatusTool", func() *Tool { return newDocSetStatusTool(nil) }},
		{"newIngestRetryTool", func() *Tool { return newIngestRetryTool(nil) }},
		{"newClipTool", func() *Tool { return newClipTool(nil) }},
		{"newExportTool", func() *Tool { return newExportTool(nil) }},
		{"newAutofillTool", func() *Tool { return newAutofillTool(nil) }},
		{"newMeetingImportTool", func() *Tool { return newMeetingImportTool(nil) }},
	}
	for _, tc := range constructors {
		t.Run(tc.label, func(t *testing.T) {
			tool := tc.build()
			want, ok := byName[tool.Name]
			if !ok {
				t.Fatalf("constructor produced unknown tool %q, not in canonical contract", tool.Name)
			}
			// The capability flag is a registry-level concern: NewRegistry's
			// entry() wrapper assigns ReadOnly, so constructors must not be
			// checked for it here.
			assertContract(t, *tool, want, false)
		})
	}
}

// TestStatusToolHandlerPinsCapabilityString verifies the status payload
// reflects the AllowWrite option through the registry entry wrapper.
func TestStatusToolHandlerPinsCapabilityString(t *testing.T) {
	cfg := &config.Config{Vault: "/test/vault"}
	for _, tc := range []struct {
		allowWrite bool
		want       string
	}{
		{false, "read_only"},
		{true, "read_write"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			entry := newStatusTool(cfg, tc.allowWrite, "test-version")
			out, err := entry.Handler(context.Background(), json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			status, ok := out.(map[string]string)
			if !ok {
				t.Fatalf("expected map[string]string, got %T", out)
			}
			if status["version"] != "test-version" {
				t.Errorf("version = %q, want %q", status["version"], "test-version")
			}
			if status["vault"] != "/test/vault" {
				t.Errorf("vault = %q, want %q", status["vault"], "/test/vault")
			}
			if status["capabilities"] != tc.want {
				t.Errorf("capabilities = %q, want %q", status["capabilities"], tc.want)
			}
		})
	}
}

// TestToolHandlersServiceError drives every service-backed handler against a
// failing factory: the shared contract is that the factory error surfaces.
func TestToolHandlersServiceError(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"desk_ls", `{"dir":"test"}`},
		{"desk_search", `{"query":"test"}`},
		{"desk_props", `{"file":"test.md"}`},
		{"desk_backlinks", `{"file":"test.md"}`},
		{"desk_ask", `{"query":"test"}`},
		{"desk_docs", `{}`},
		{"docs_review", `{"threshold":85}`},
		{"docs_similar", `{"file":"test.md","threshold":50}`},
		{"desk_related", `{"file":"test.md"}`},
		{"desk_ingest_jobs", `{}`},
		{"meeting_list", `{}`},
		{"meeting_get", `{"path":"meetings/nope.md"}`},
		{"desk_note_new", `{"title":"T","content":"C"}`},
		{"desk_ingest", `{"source_path":"/tmp/x"}`},
		{"doc_set_status", `{"file":"test.md","status":"done"}`},
		{"desk_ingest_retry", `{"id":"x"}`},
		{"desk_clip", `{"url":"https://example.com"}`},
		{"desk_export", `{"note":"test.md","format":"html"}`},
		{"desk_autofill", `{"view":"v","property":"p"}`},
		{"meeting_import", `{"meeting_id":"m1"}`},
	}
	withError := map[string]func(ServiceFactory) *Tool{
		"desk_ls":           newLsTool,
		"desk_search":       newSearchTool,
		"desk_props":        newPropsTool,
		"desk_backlinks":    newBacklinksTool,
		"desk_ask":          newAskTool,
		"desk_docs":         newDocsTool,
		"docs_review":       func(f ServiceFactory) *Tool { return newDocsReviewTool(f, &config.Config{ReviewThreshold: 85}) },
		"docs_similar":      newDocsSimilarTool,
		"desk_related":      newRelatedTool,
		"desk_ingest_jobs":  newIngestJobsTool,
		"meeting_list":      newMeetingListTool,
		"meeting_get":       newMeetingGetTool,
		"desk_note_new":     newNoteNewTool,
		"desk_ingest":       newIngestTool,
		"doc_set_status":    newDocSetStatusTool,
		"desk_ingest_retry": newIngestRetryTool,
		"desk_clip":         newClipTool,
		"desk_export":       newExportTool,
		"desk_autofill":     newAutofillTool,
		"meeting_import":    newMeetingImportTool,
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := withError[tc.name](errorServiceFactory())
			_, err := tool.Handler(context.Background(), json.RawMessage(tc.input))
			if err == nil || !strings.Contains(err.Error(), "service unavailable") {
				t.Errorf("expected service unavailable error, got %v", err)
			}
		})
	}
}

// TestToolHandlersRequireArgs pins the required-argument validation each
// handler performs on an empty arguments object.
func TestToolHandlersRequireArgs(t *testing.T) {
	cases := []struct {
		name string
		tool func(ServiceFactory) *Tool
	}{
		{"desk_search", newSearchTool},
		{"desk_props", newPropsTool},
		{"desk_backlinks", newBacklinksTool},
		{"desk_ask", newAskTool},
		{"desk_transform", func(f ServiceFactory) *Tool { return newTransformTool(&config.Config{}) }},
		{"desk_note_new", newNoteNewTool},
		{"desk_ingest", newIngestTool},
		{"doc_set_status", newDocSetStatusTool},
		{"docs_similar", newDocsSimilarTool},
		{"desk_related", newRelatedTool},
		{"desk_clip", newClipTool},
		{"desk_export", newExportTool},
		{"desk_autofill", newAutofillTool},
		{"meeting_get", newMeetingGetTool},
		{"meeting_import", newMeetingImportTool},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := tc.tool(testServiceFactory(t))
			if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
				t.Errorf("expected error for missing required args")
			}
		})
	}
}

// TestToolHandlersInvalidJSON pins the JSON validation of every handler that
// unmarshals its input.
func TestToolHandlersInvalidJSON(t *testing.T) {
	cases := []struct {
		name string
		tool func(ServiceFactory) *Tool
	}{
		{"desk_ls", newLsTool},
		{"desk_search", newSearchTool},
		{"desk_props", newPropsTool},
		{"desk_backlinks", newBacklinksTool},
		{"desk_ask", newAskTool},
		{"desk_transform", func(f ServiceFactory) *Tool { return newTransformTool(&config.Config{}) }},
		{"desk_docs", newDocsTool},
		{"docs_review", func(f ServiceFactory) *Tool { return newDocsReviewTool(f, &config.Config{ReviewThreshold: 85}) }},
		{"docs_similar", newDocsSimilarTool},
		{"desk_related", newRelatedTool},
		{"desk_note_new", newNoteNewTool},
		{"desk_ingest", newIngestTool},
		{"doc_set_status", newDocSetStatusTool},
		{"desk_ingest_retry", newIngestRetryTool},
		{"desk_clip", newClipTool},
		{"desk_export", newExportTool},
		{"desk_autofill", newAutofillTool},
		{"meeting_get", newMeetingGetTool},
		{"meeting_import", newMeetingImportTool},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := tc.tool(testServiceFactory(t))
			if _, err := tool.Handler(context.Background(), json.RawMessage(`{invalid`)); err == nil {
				t.Errorf("expected json unmarshal error")
			}
		})
	}
}

// TestToolHandlersHappyPaths exercises each handler end-to-end against a real
// service, including the external-tool degradation paths (symingest,
// symfetch, symmeet, symmemory not installed).
func TestToolHandlersHappyPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("desk_ls empty vault", func(t *testing.T) {
		tool := newLsTool(testServiceFactory(t))
		out, err := tool.Handler(ctx, json.RawMessage(`{"dir":""}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := out.([]service.FileEntry); !ok {
			t.Errorf("expected []service.FileEntry, got %T", out)
		}
	})

	t.Run("desk_search returns response", func(t *testing.T) {
		tool := newSearchTool(testServiceFactory(t))
		out, err := tool.Handler(ctx, json.RawMessage(`{"query":"nothing"}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := out.(service.SearchResponse); !ok {
			t.Errorf("expected service.SearchResponse, got %T", out)
		}
	})

	t.Run("desk_props reads created note", func(t *testing.T) {
		factory := testServiceFactory(t)
		path := createNote(t, factory, "Props Note", "body")
		tool := newPropsTool(factory)
		in, _ := json.Marshal(map[string]string{"file": path})
		out, err := tool.Handler(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		props, ok := out.(map[string]interface{})
		if !ok {
			t.Fatalf("expected map[string]interface{}, got %T", out)
		}
		if props["title"] == nil {
			t.Errorf("expected title in props, got %#v", props)
		}
	})

	t.Run("desk_backlinks empty", func(t *testing.T) {
		factory := testServiceFactory(t)
		path := createNote(t, factory, "Backlinks Note", "body")
		tool := newBacklinksTool(factory)
		in, _ := json.Marshal(map[string]string{"file": path})
		out, err := tool.Handler(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := out.([]string); !ok {
			t.Errorf("expected []string, got %T", out)
		}
	})

	t.Run("desk_ask honest fallback without ollama", func(t *testing.T) {
		t.Setenv("SYMDESK_OLLAMA_URL", "")
		tool := newAskTool(testServiceFactory(t))
		out, err := tool.Handler(ctx, json.RawMessage(`{"query":"anything"}`))
		if err != nil {
			t.Fatal(err)
		}
		answer := out.(map[string]string)["answer"]
		if !strings.Contains(answer, "not configured") {
			t.Errorf("expected honest unconfigured note, got: %s", answer)
		}
	})

	t.Run("desk_transform aggregates fallback text", func(t *testing.T) {
		t.Setenv("SYMDESK_OLLAMA_URL", "")
		tool := newTransformTool(&config.Config{})
		out, err := tool.Handler(ctx, json.RawMessage(`{"text":"hello","intent":"summarize"}`))
		if err != nil {
			t.Fatal(err)
		}
		result, ok := out.(map[string]string)
		if !ok {
			t.Fatalf("expected map[string]string, got %T", out)
		}
		if !strings.Contains(result["result"], "not configured") {
			t.Errorf("expected honest unconfigured note, got: %q", result["result"])
		}
	})

	t.Run("desk_note_new creates note", func(t *testing.T) {
		tool := newNoteNewTool(testServiceFactory(t))
		in, _ := json.Marshal(map[string]string{"title": "New Note", "content": "body"})
		out, err := tool.Handler(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]string)["path"] == "" {
			t.Error("expected non-empty note path")
		}
	})

	t.Run("desk_ingest creates inbox note", func(t *testing.T) {
		restrictPATH(t)
		src := filepath.Join(t.TempDir(), "doc.txt")
		if err := os.WriteFile(src, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		tool := newIngestTool(testServiceFactory(t))
		in, _ := json.Marshal(map[string]string{"source_path": src})
		out, err := tool.Handler(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		path := out.(map[string]string)["path"]
		if !strings.HasPrefix(path, "inbox/") || !strings.HasSuffix(path, ".md") {
			t.Errorf("unexpected note path: %s", path)
		}
	})

	t.Run("desk_docs empty vault", func(t *testing.T) {
		tool := newDocsTool(testServiceFactory(t))
		out, err := tool.Handler(ctx, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if results, ok := out.([]service.DocsListResult); !ok || len(results) != 0 {
			t.Errorf("expected empty []service.DocsListResult, got %T %#v", out, out)
		}
	})

	t.Run("doc_set_status updates file", func(t *testing.T) {
		factory := testServiceFactory(t)
		svc, db, err := factory()
		if err != nil {
			t.Fatal(err)
		}
		content := "---\ntitle: \"Test\"\nstatus: \"open\"\n---\n\nBody.\n"
		if err := os.WriteFile(filepath.Join(svc.VaultRoot, "test.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		tool := newDocSetStatusTool(factory)
		in, _ := json.Marshal(map[string]string{"file": "test.md", "status": "done"})
		out, err := tool.Handler(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]string)["new_status"] != "done" {
			t.Errorf("expected new_status done, got %#v", out)
		}
	})

	t.Run("docs_review empty vault", func(t *testing.T) {
		tool := newDocsReviewTool(testServiceFactory(t), &config.Config{ReviewThreshold: 85})
		out, err := tool.Handler(ctx, json.RawMessage(`{"threshold":85}`))
		if err != nil {
			t.Fatal(err)
		}
		if results, ok := out.([]sidecar.ReviewResult); !ok || len(results) != 0 {
			t.Errorf("expected empty []sidecar.ReviewResult, got %T %#v", out, out)
		}
	})

	t.Run("docs_similar missing file errors", func(t *testing.T) {
		tool := newDocsSimilarTool(testServiceFactory(t))
		_, err := tool.Handler(ctx, json.RawMessage(`{"file":"nonexistent.md","threshold":50}`))
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("desk_related empty result without symmemory", func(t *testing.T) {
		restrictPATH(t)
		factory := testServiceFactory(t)
		path := createNote(t, factory, "Related Note", "body")
		tool := newRelatedTool(factory)
		in, _ := json.Marshal(map[string]string{"file": path})
		out, err := tool.Handler(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := out.(*service.RelatedData); !ok {
			t.Errorf("expected *service.RelatedData, got %T", out)
		}
	})

	t.Run("desk_ingest_jobs degrades without symingest", func(t *testing.T) {
		restrictPATH(t)
		tool := newIngestJobsTool(testServiceFactory(t))
		if _, err := tool.Handler(ctx, json.RawMessage(`{}`)); err == nil {
			t.Error("expected error when symingest is not installed")
		}
	})

	t.Run("desk_ingest_retry degrades without symingest", func(t *testing.T) {
		restrictPATH(t)
		tool := newIngestRetryTool(testServiceFactory(t))
		if _, err := tool.Handler(ctx, json.RawMessage(`{"id":"x"}`)); err == nil {
			t.Error("expected error when symingest is not installed")
		}
	})

	t.Run("desk_clip degrades without symfetch", func(t *testing.T) {
		restrictPATH(t)
		tool := newClipTool(testServiceFactory(t))
		in, _ := json.Marshal(map[string]string{"url": "https://example.com"})
		if _, err := tool.Handler(ctx, in); err == nil {
			t.Error("expected error when symfetch is not installed")
		}
	})

	t.Run("desk_export renders html note", func(t *testing.T) {
		factory := testServiceFactory(t)
		path := createNote(t, factory, "Export Note", "body text")
		output := filepath.Join(t.TempDir(), "out.html")
		tool := newExportTool(factory)
		in, _ := json.Marshal(map[string]string{"note": path, "format": "html", "output": output})
		if _, err := tool.Handler(ctx, in); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(output); err != nil {
			t.Errorf("expected exported file at %s: %v", output, err)
		}
	})

	t.Run("desk_autofill unknown view errors", func(t *testing.T) {
		tool := newAutofillTool(testServiceFactory(t))
		in, _ := json.Marshal(map[string]interface{}{"view": "bogus", "property": "p", "dry_run": true})
		if _, err := tool.Handler(ctx, in); err == nil {
			t.Error("expected error for unknown view")
		}
	})

	t.Run("meeting_list empty vault", func(t *testing.T) {
		tool := newMeetingListTool(testServiceFactory(t))
		out, err := tool.Handler(ctx, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if summaries, ok := out.([]service.MeetingNoteSummary); !ok || len(summaries) != 0 {
			t.Errorf("expected empty []service.MeetingNoteSummary, got %T %#v", out, out)
		}
	})

	t.Run("meeting_get missing note errors", func(t *testing.T) {
		tool := newMeetingGetTool(testServiceFactory(t))
		if _, err := tool.Handler(ctx, json.RawMessage(`{"path":"meetings/nope.md"}`)); err == nil {
			t.Error("expected error for missing meeting note")
		}
	})

	t.Run("meeting_import degrades without symmeet", func(t *testing.T) {
		restrictPATH(t)
		tool := newMeetingImportTool(testServiceFactory(t))
		if _, err := tool.Handler(ctx, json.RawMessage(`{"meeting_id":"m1"}`)); err == nil {
			t.Error("expected error when symmeet is not installed")
		}
	})
}

// TestRegistryNilReceiverGuards pins the nil-safety of the registry methods.
func TestRegistryNilReceiverGuards(t *testing.T) {
	var registry *Registry
	if got := registry.All(); got != nil {
		t.Errorf("nil registry All() = %#v, want nil", got)
	}
	if got := registry.Enabled(false); got != nil {
		t.Errorf("nil registry Enabled(false) = %#v, want nil", got)
	}
	if got, ok := registry.Lookup("desk_ls"); ok || got.Name != "" {
		t.Errorf("nil registry Lookup = (%#v, %v), want zero value", got, ok)
	}
}

// createNote writes a note through the service and returns its vault-relative
// path. The temporary database is closed before returning so the caller's
// next factory() open is uncontended.
func createNote(t *testing.T, factory ServiceFactory, title, body string) string {
	t.Helper()
	svc, db, err := factory()
	if err != nil {
		t.Fatal(err)
	}
	path, err := svc.NoteNew(title, body, "")
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	return path
}
