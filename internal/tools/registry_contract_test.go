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
		description: "Searches notes with full-text terms plus path:, tag:, type:, status:, filename:, filetype:, created:, modified:, quoted phrases, -negation and /regex/. Filetype accepts comma-separated extensions (for example pdf,epub); dates accept YYYY-MM-DD, YYYY-MM-DD..YYYY-MM-DD and last day/week/month/year. Invalid syntax falls back to plain full-text and returns a hint.",
		schema:      `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
		readOnly:    true,
	},
	{
		name:        "vault_timeline",
		description: "Answers \"what happened on day X\": returns journal activity from the vault (daily NDJSON journal) plus indexed documents whose document date or modification time falls in the given range. Dates are YYYY-MM-DD; from and to default to today.",
		schema:      `{"type":"object","properties":{"from":{"type":"string"},"to":{"type":"string"},"limit":{"type":"integer"}}}`,
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
		description: "Asks the AI a question about the vault. Uses a local Ollama instance when configured; otherwise returns the top search results with a note that AI is not configured. The answer is returned as one aggregated text (no streaming). Pass notebook to restrict retrieval and citations to that notebook's sources instead of the whole vault.",
		schema:      `{"type":"object","properties":{"query":{"type":"string"},"notebook":{"type":"string","description":"optional: notebook id or path to restrict retrieval and citations to"}},"required":["query"]}`,
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
		name:        "vault_health",
		description: "Scans the Markdown vault for parse errors, missing frontmatter, broken wikilinks and near-duplicate documents, returning a reviewable repair plan without changing files.",
		schema:      `{"type":"object","properties":{"duplicate_threshold":{"type":"integer"}}}`,
		readOnly:    true,
	},
	{
		name:        "desk_related",
		description: "Gets related entities and notes for a given file path based on composition with symmemory.",
		schema:      `{"type":"object","properties":{"file":{"type":"string"}},"required":["file"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_diagram",
		description: "Renders the vault link graph as an SVG diagram. Returns the SVG document; use it to embed or display the vault's structure.",
		schema:      `{"type":"object","properties":{"title":{"type":"string","description":"optional diagram title"}}}`,
		readOnly:    true,
	},
	{
		name:        "desk_ingest_jobs",
		description: "Lists ingestion jobs in the queue from symingest.",
		schema:      `{"type":"object","properties":{}}`,
		readOnly:    true,
	},
	{
		name:        "desk_rules_list",
		description: "Lists the configured document classification rules. Legacy alias: list_rules.",
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
		name:        "desk_read_result",
		description: "Reads the full output of an externalized tool result by its handle (returned as \"[Full result externalized to <handle>…]\"). Use this to re-read a result that was summarized because it was large.",
		schema:      `{"type":"object","properties":{"handle":{"type":"string","description":"the externalized-result handle from the tool output"}},"required":["handle"]}`,
		readOnly:    true,
	},
	{
		name:        "notebook_list",
		description: "Lists all notebooks in the vault. A notebook is a named, bounded set of vault sources used to scope AI grounding, retrieval and generated artifacts.",
		schema:      `{"type":"object","properties":{}}`,
		readOnly:    true,
	},
	{
		name:        "notebook_get",
		description: "Gets one notebook by id or vault-relative path, including its resolved sources (path, current title, and whether the source file still exists).",
		schema:      `{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"}},"required":["notebook"]}`,
		readOnly:    true,
	},
	{
		name:        "notebook_ask",
		description: "Asks the AI a question restricted to one notebook's sources: retrieval and citations never leave the notebook's scope. Uses a local Ollama instance when configured; otherwise returns a note that AI is not configured. The answer is returned as one aggregated text (no streaming).",
		schema:      `{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"},"query":{"type":"string"}},"required":["notebook","query"]}`,
		readOnly:    true,
	},
	{
		name:        "desk_dataset_list",
		description: "Lists Markdown-backed datasets and their materialized row counts.",
		schema:      `{"type":"object","properties":{},"additionalProperties":false}`,
		readOnly:    true,
	},
	{
		name:        "desk_dataset_describe",
		description: "Describes one Markdown-backed dataset, including schema, provenance, coverage and row count.",
		schema:      `{"type":"object","properties":{"dataset":{"type":"string"}},"required":["dataset"],"additionalProperties":false}`,
		readOnly:    true,
	},
	{
		name:        "desk_dataset_query",
		description: "Queries a dataset with selected columns, existing view filter operators, grouping and bounded sum/count/min/max/average aggregates. Raw SQL is not accepted.",
		schema:      `{"type":"object","properties":{"dataset":{"type":"string"},"columns":{"type":"array","items":{"type":"string"}},"filters":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"operator":{"type":"string"},"value":{"type":"string"}},"required":["key","value"],"additionalProperties":false}},"filter_group":{"type":"object"},"group_by":{"type":"string"},"aggregates":{"type":"array","items":{"type":"object","properties":{"column":{"type":"string"},"function":{"type":"string","enum":["sum","count","min","max","average"]},"as":{"type":"string"}},"required":["function"],"additionalProperties":false}},"limit":{"type":"integer","minimum":1}},"required":["dataset"],"additionalProperties":false}`,
		readOnly:    true,
	},
	{
		name:        "desk_dataset_sync",
		description: "Persists producer rows into a Markdown-backed dataset. Every row must have an explicit identity and every sync must include source_name, source_sha256 and imported_at provenance; repeated provenance is idempotent.",
		schema:      `{"type":"object","properties":{"dataset":{"type":"string"},"title":{"type":"string"},"identity_field":{"type":"string"},"sensitivity":{"type":"string","enum":["public","internal","confidential","restricted"]},"retention_rule":{"type":"string"},"schema":{"type":"object"},"provenance":{"type":"object","properties":{"imported_at":{"type":"string"},"source_name":{"type":"string"},"source_sha256":{"type":"string"}},"required":["imported_at","source_name","source_sha256"],"additionalProperties":false},"rows":{"type":"array","items":{"type":"object","properties":{"identity":{"type":"string"},"values":{"type":"object"}},"required":["identity","values"],"additionalProperties":false}}},"required":["dataset","identity_field","provenance","rows"],"additionalProperties":false}`,
		readOnly:    false,
	},
	{
		name:        "desk_undo_task",
		description: "Rejects an agent run as a unit: restores every file that existed before the task and deletes files the task created. Takes the task id from a prior checkpoint.",
		schema:      `{"type":"object","properties":{"task_id":{"type":"string","description":"the checkpoint task id to undo"}},"required":["task_id"]}`,
		readOnly:    false,
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
		name:        "desk_ingest_reocr",
		description: "Re-runs OCR/extraction for an already-ingested document, either by its registered archived source path or by document ID, and refreshes the existing note in place. Legacy alias: reocr.",
		schema:      `{"type":"object","properties":{"document_id":{"type":"integer","description":"reprocess by document ID"},"archive_path":{"type":"string","description":"the registered archived original path"},"source":{"type":"string","description":"legacy alias for archive_path"}},"anyOf":[{"required":["document_id"]},{"required":["archive_path"]}]}`,
		readOnly:    false,
	},
	{
		name:        "desk_rules_add",
		description: "Adds a document classification rule. kind must be one of category, tag, correspondent or document_type. Legacy alias: add_rule.",
		schema:      `{"type":"object","properties":{"pattern":{"type":"string","description":"case-insensitive substring matched against extracted document text"},"kind":{"type":"string","enum":["category","tag","correspondent","document_type"]},"value":{"type":"string","description":"the category, tag, correspondent or document type to assign"}},"required":["pattern","kind","value"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_rules_update",
		description: "Updates an existing document classification rule by ID, replacing its pattern, kind and value.",
		schema:      `{"type":"object","properties":{"id":{"type":"integer","description":"rule ID"},"pattern":{"type":"string"},"kind":{"type":"string","enum":["category","tag","correspondent","document_type"]},"value":{"type":"string"}},"required":["id","pattern","kind","value"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_rules_delete",
		description: "Deletes a document classification rule by ID. Legacy alias: delete_rule.",
		schema:      `{"type":"object","properties":{"id":{"type":"integer","description":"rule ID"}},"required":["id"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_split_pdf",
		description: "Splits a PDF into parts after the given pages and writes them into an output directory. at is a comma-separated page spec such as \"2,4\" or \"2-3,6\". Requires the Poppler utilities (pdfinfo, pdfseparate, pdfunite) on PATH. Legacy alias: split_pdf.",
		schema:      `{"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"at":{"type":"string","description":"split after these pages, e.g. 2,4 or 2-3,6"},"output_dir":{"type":"string","description":"directory for the generated part PDFs"}},"required":["input","at","output_dir"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_merge_pdf",
		description: "Merges two or more PDFs into one output file without modifying the inputs. inputs is an array of PDF paths; output is the destination PDF path. Requires the Poppler utility pdfunite on PATH. Legacy alias: merge_pdf.",
		schema:      `{"type":"object","properties":{"inputs":{"type":"array","items":{"type":"string"},"description":"input PDF paths, at least two"},"output":{"type":"string","description":"destination PDF path"}},"required":["inputs","output"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_rotate_pdf",
		description: "Rotates pages of a PDF and writes the result to an output path without modifying the input. degrees must be one of -270, -180, -90, 90, 180, 270; pages is an optional comma-separated 1-based page selector (e.g. 1-3,5), empty rotates all pages. Requires qpdf on PATH. Legacy alias: rotate_pdf.",
		schema:      `{"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"output":{"type":"string","description":"destination PDF path"},"degrees":{"type":"integer","description":"rotation in degrees: -270, -180, -90, 90, 180 or 270"},"pages":{"type":"string","description":"optional page selector, e.g. 1-3,5; empty rotates all pages"}},"required":["input","output","degrees"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_clip",
		description: "Fetches a URL via symbrowse and saves it as a note in the vault.",
		schema:      `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_export",
		description: "Exports a note or view to PDF, HTML, or CSV. Dataset-backed views are subject to the configured sensitivity gate. Provide either note or view, not both.",
		schema:      `{"type":"object","properties":{"note":{"type":"string","description":"vault-relative note path"},"view":{"type":"string","description":"view id"},"output":{"type":"string","description":"output file path"},"format":{"type":"string","enum":["pdf","html","csv"]},"profile":{"type":"string","description":"symprint profile for PDF"}},"oneOf":[{"required":["note"]},{"required":["view"]}]}`,
		readOnly:    false,
	},
	{
		name:        "notebook_create",
		description: "Creates a new notebook: a named, bounded set of vault sources used to scope later AI grounding, retrieval and generated artifacts.",
		schema:      `{"type":"object","properties":{"title":{"type":"string"},"description":{"type":"string"}},"required":["title"]}`,
		readOnly:    false,
	},
	{
		name:        "notebook_add_source",
		description: "Adds a vault file to a notebook's source set.",
		schema:      `{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"},"path":{"type":"string","description":"vault-relative path of the file to add"}},"required":["notebook","path"]}`,
		readOnly:    false,
	},
	{
		name:        "notebook_remove_source",
		description: "Removes a vault file from a notebook's source set. The file itself is left untouched.",
		schema:      `{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"},"path":{"type":"string","description":"vault-relative path of the file to remove"}},"required":["notebook","path"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_autofill",
		description: "Autofills a frontmatter property on all notes matching a view using the configured AI provider.",
		schema:      `{"type":"object","properties":{"view":{"type":"string","description":"view id"},"property":{"type":"string","description":"frontmatter property to fill"},"prompt":{"type":"string","description":"extra instruction for the AI"},"dry_run":{"type":"boolean","description":"show changes without writing"}},"required":["view","property"]}`,
		readOnly:    false,
	},
	{
		name:        "desk_asset_store",
		description: "Stores binary data (base64-encoded or plain text) into the vault assets folder with collision-safe naming and returns the vault-relative path and Markdown link.",
		schema:      `{"type":"object","properties":{"data":{"type":"string","description":"content to store (base64 string or plain text)"},"extension":{"type":"string","description":"file extension (e.g. png, pdf, svg)"},"is_base64":{"type":"boolean","description":"true if data is base64 encoded"},"preferred_name":{"type":"string","description":"optional preferred filename (e.g. image.png)"}},"required":["data"]}`,
		readOnly:    false,
	},
	// Legacy compatibility aliases (issue #598): the absorbed SymIngest and
	// SymSeek MCP contracts. Each alias delegates to the canonical tool's
	// handler, so name, description and schema are pinned here exactly as
	// NewRegistry exposes them.
	{
		name:        "ingest_file",
		description: "Ingests a file into the vault: copies it into inbox/ and creates a corresponding markdown note. Legacy alias for desk_ingest.",
		schema:      `{"type":"object","properties":{"source_path":{"type":"string"}},"required":["source_path"]}`,
		readOnly:    false,
	},
	{
		name:        "list_jobs",
		description: "Lists ingestion jobs in the queue. Legacy alias for desk_ingest_jobs.",
		schema:      `{"type":"object","properties":{}}`,
		readOnly:    true,
	},
	{
		name:        "retry_job",
		description: "Retries a failed ingestion job by ID. Legacy alias for desk_ingest_retry.",
		schema:      `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`,
		readOnly:    false,
	},
	{
		name:        "list_documents",
		description: "Lists indexed documents with optional filters (status, person, correspondent, type, year, due-before, min/max confidence). Legacy alias for desk_docs.",
		schema:      `{"type":"object","properties":{"type":{"type":"string"},"status":{"type":"string"},"person":{"type":"string"},"correspondent":{"type":"string"},"year":{"type":"string"},"due_before":{"type":"string"},"min_confidence":{"type":"integer"},"max_confidence":{"type":"integer"}}}`,
		readOnly:    true,
	},
	{
		name:        "search_documents",
		description: "Searches notes with full-text terms plus path:, tag:, type:, status:, filename:, filetype:, created:, modified:, quoted phrases, -negation and /regex/. Filetype accepts comma-separated extensions (for example pdf,epub); dates accept YYYY-MM-DD, YYYY-MM-DD..YYYY-MM-DD and last day/week/month/year. Invalid syntax falls back to plain full-text and returns a hint. Legacy alias for desk_search.",
		schema:      `{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`,
		readOnly:    true,
	},
	{
		name:        "reocr",
		description: "Re-runs OCR/extraction for an already-ingested document by document ID or registered archived source path. Legacy alias for desk_ingest_reocr.",
		schema:      `{"type":"object","properties":{"document_id":{"type":"integer","description":"reprocess by document ID"},"archive_path":{"type":"string","description":"the registered archived original path"},"source":{"type":"string","description":"legacy alias for archive_path"}},"anyOf":[{"required":["document_id"]},{"required":["archive_path"]}]}`,
		readOnly:    false,
	},
	{
		name:        "list_rules",
		description: "Lists the configured document classification rules. Legacy alias for desk_rules_list.",
		schema:      `{"type":"object","properties":{}}`,
		readOnly:    true,
	},
	{
		name:        "add_rule",
		description: "Adds a document classification rule (kind: category|tag|correspondent|document_type). Legacy alias for desk_rules_add.",
		schema:      `{"type":"object","properties":{"pattern":{"type":"string","description":"case-insensitive substring matched against extracted document text"},"kind":{"type":"string","enum":["category","tag","correspondent","document_type"]},"value":{"type":"string","description":"the category, tag, correspondent or document type to assign"}},"required":["pattern","kind","value"]}`,
		readOnly:    false,
	},
	{
		name:        "delete_rule",
		description: "Deletes a document classification rule by ID. Legacy alias for desk_rules_delete.",
		schema:      `{"type":"object","properties":{"id":{"type":"integer","description":"rule ID"}},"required":["id"]}`,
		readOnly:    false,
	},
	{
		name:        "split_pdf",
		description: "Splits a PDF into parts after the given pages. Legacy alias for desk_split_pdf.",
		schema:      `{"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"at":{"type":"string","description":"split after these pages, e.g. 2,4 or 2-3,6"},"output_dir":{"type":"string","description":"directory for the generated part PDFs"}},"required":["input","at","output_dir"]}`,
		readOnly:    false,
	},
	{
		name:        "merge_pdf",
		description: "Merges two or more PDFs into one output file. Legacy alias for desk_merge_pdf.",
		schema:      `{"type":"object","properties":{"inputs":{"type":"array","items":{"type":"string"},"description":"input PDF paths, at least two"},"output":{"type":"string","description":"destination PDF path"}},"required":["inputs","output"]}`,
		readOnly:    false,
	},
	{
		name:        "rotate_pdf",
		description: "Rotates pages of a PDF and writes the result to an output path. Legacy alias for desk_rotate_pdf.",
		schema:      `{"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"output":{"type":"string","description":"destination PDF path"},"degrees":{"type":"integer","description":"rotation in degrees: -270, -180, -90, 90, 180 or 270"},"pages":{"type":"string","description":"optional page selector, e.g. 1-3,5; empty rotates all pages"}},"required":["input","output","degrees"]}`,
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

// restrictPATH hides the symaira companion binaries (symingest, symmeet,
// symmemory, symprint) so external-tool paths fail deterministically.
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
		{"newRulesListTool", func() *Tool { return newRulesListTool(&config.Config{Vault: "/test/vault"}) }},
		{"newMeetingListTool", func() *Tool { return newMeetingListTool(nil) }},
		{"newMeetingGetTool", func() *Tool { return newMeetingGetTool(nil) }},
		{"newNotebookListTool", func() *Tool { return newNotebookListTool(nil) }},
		{"newNotebookGetTool", func() *Tool { return newNotebookGetTool(nil) }},
		{"newNotebookAskTool", func() *Tool { return newNotebookAskTool(nil) }},
		{"newNoteNewTool", func() *Tool { return newNoteNewTool(nil) }},
		{"newIngestTool", func() *Tool { return newIngestTool(nil) }},
		{"newDocSetStatusTool", func() *Tool { return newDocSetStatusTool(nil) }},
		{"newIngestRetryTool", func() *Tool { return newIngestRetryTool(nil) }},
		{"newIngestReocrTool", func() *Tool { return newIngestReocrTool(&config.Config{Vault: "/test/vault"}) }},
		{"newRulesAddTool", func() *Tool { return newRulesAddTool(&config.Config{Vault: "/test/vault"}) }},
		{"newRulesUpdateTool", func() *Tool { return newRulesUpdateTool(&config.Config{Vault: "/test/vault"}) }},
		{"newRulesDeleteTool", func() *Tool { return newRulesDeleteTool(&config.Config{Vault: "/test/vault"}) }},
		{"newSplitPDFTool", func() *Tool { return newSplitPDFTool() }},
		{"newMergePDFTool", func() *Tool { return newMergePDFTool() }},
		{"newRotatePDFTool", func() *Tool { return newRotatePDFTool() }},
		{"newClipTool", func() *Tool { return newClipTool(nil) }},
		{"newExportTool", func() *Tool { return newExportTool(nil) }},
		{"newNotebookCreateTool", func() *Tool { return newNotebookCreateTool(nil) }},
		{"newNotebookAddSourceTool", func() *Tool { return newNotebookAddSourceTool(nil) }},
		{"newNotebookRemoveSourceTool", func() *Tool { return newNotebookRemoveSourceTool(nil) }},
		{"newAutofillTool", func() *Tool { return newAutofillTool(nil) }},
		{"newAssetStoreTool", func() *Tool { return newAssetStoreTool(nil) }},
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
		{"vault_timeline", `{"from":"2026-08-01","to":"2026-08-02"}`},
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
		{"desk_read_result", `{"handle":"task-1/desk_ls-001.txt"}`},
		{"desk_undo_task", `{"task_id":"t1"}`},
		{"desk_note_new", `{"title":"T","content":"C"}`},
		{"desk_ingest", `{"source_path":"/tmp/x"}`},
		{"doc_set_status", `{"file":"test.md","status":"done"}`},
		{"desk_ingest_retry", `{"id":"x"}`},
		{"desk_clip", `{"url":"https://example.com"}`},
		{"desk_export", `{"note":"test.md","format":"html"}`},
		{"desk_autofill", `{"view":"v","property":"p"}`},
		{"desk_asset_store", `{"data":"aGVsbG8=","is_base64":true}`},
	}
	withError := map[string]func(ServiceFactory) *Tool{
		"desk_ls":           newLsTool,
		"desk_search":       newSearchTool,
		"vault_timeline":    newTimelineTool,
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
		"desk_read_result":  newReadResultTool,
		"desk_undo_task":    newUndoTaskTool,
		"desk_note_new":     newNoteNewTool,
		"desk_ingest":       newIngestTool,
		"doc_set_status":    newDocSetStatusTool,
		"desk_ingest_retry": newIngestRetryTool,
		"desk_clip":         newClipTool,
		"desk_export":       newExportTool,
		"desk_autofill":     newAutofillTool,
		"desk_asset_store":  newAssetStoreTool,
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
		{"desk_asset_store", newAssetStoreTool},
		{"meeting_get", newMeetingGetTool},
		{"desk_ingest_reocr", func(f ServiceFactory) *Tool { return newIngestReocrTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_list", func(f ServiceFactory) *Tool { return newRulesListTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_add", func(f ServiceFactory) *Tool { return newRulesAddTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_update", func(f ServiceFactory) *Tool { return newRulesUpdateTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_delete", func(f ServiceFactory) *Tool { return newRulesDeleteTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_split_pdf", func(f ServiceFactory) *Tool { return newSplitPDFTool() }},
		{"desk_merge_pdf", func(f ServiceFactory) *Tool { return newMergePDFTool() }},
		{"desk_rotate_pdf", func(f ServiceFactory) *Tool { return newRotatePDFTool() }},
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
		{"vault_timeline", newTimelineTool},
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
		{"desk_asset_store", newAssetStoreTool},
		{"meeting_get", newMeetingGetTool},
		{"desk_ingest_reocr", func(f ServiceFactory) *Tool { return newIngestReocrTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_list", func(f ServiceFactory) *Tool { return newRulesListTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_add", func(f ServiceFactory) *Tool { return newRulesAddTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_update", func(f ServiceFactory) *Tool { return newRulesUpdateTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_rules_delete", func(f ServiceFactory) *Tool { return newRulesDeleteTool(&config.Config{Vault: "/test/vault"}) }},
		{"desk_split_pdf", func(f ServiceFactory) *Tool { return newSplitPDFTool() }},
		{"desk_merge_pdf", func(f ServiceFactory) *Tool { return newMergePDFTool() }},
		{"desk_rotate_pdf", func(f ServiceFactory) *Tool { return newRotatePDFTool() }},
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
// service, including the external-tool degradation paths (symingest, symmeet,
// symmemory not installed).
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

	t.Run("desk_clip degrades without symbrowse", func(t *testing.T) {
		restrictPATH(t)
		tool := newClipTool(testServiceFactory(t))
		in, _ := json.Marshal(map[string]string{"url": "https://example.com"})
		if _, err := tool.Handler(ctx, in); err == nil {
			t.Error("expected error when symbrowse is not installed")
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

	t.Run("desk_asset_store writes asset and returns link", func(t *testing.T) {
		tool := newAssetStoreTool(testServiceFactory(t))
		in, _ := json.Marshal(map[string]any{
			"data":           "aGVsbG8td29ybGQ=",
			"preferred_name": "icon.png",
			"extension":      "png",
			"is_base64":      true,
		})
		out, err := tool.Handler(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		res, ok := out.(map[string]string)
		if !ok {
			t.Fatalf("expected map[string]string, got %T", out)
		}
		if res["path"] != "assets/icon.png" {
			t.Errorf("expected assets/icon.png, got %q", res["path"])
		}
		if res["markdown_link"] != "![icon.png](assets/icon.png)" {
			t.Errorf("expected ![icon.png](assets/icon.png), got %q", res["markdown_link"])
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
