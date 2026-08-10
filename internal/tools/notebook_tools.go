package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// newNotebookListTool lists every notebook in the vault (issue #428).
func newNotebookListTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "notebook_list",
		Description: "Lists all notebooks in the vault. A notebook is a named, bounded set of vault sources used to scope AI grounding, retrieval and generated artifacts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.NotebookList()
		},
	}
}

// newNotebookGetTool resolves one notebook and its current source list
// (issue #428). A source whose file has moved or been deleted is reported
// with missing=true rather than failing the whole call.
func newNotebookGetTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "notebook_get",
		Description: "Gets one notebook by id or vault-relative path, including its resolved sources (path, current title, and whether the source file still exists).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"}},"required":["notebook"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Notebook string `json:"notebook"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Notebook == "" {
				return nil, fmt.Errorf("notebook is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			nb, err := svc.NotebookGet(args.Notebook)
			if err != nil {
				return nil, err
			}
			refs, err := nb.ResolveSources(svc.VaultRoot)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"id":          nb.ID,
				"path":        nb.Path,
				"title":       nb.Title,
				"description": nb.Description,
				"created":     nb.Created,
				"sources":     refs,
			}, nil
		},
	}
}

// newNotebookAskTool is desk_ask restricted to one notebook's sources
// (issue #425/#428): retrieval and citations never leave the notebook's
// scope. Read-only — it only reads, never mutates the vault.
func newNotebookAskTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "notebook_ask",
		Description: "Asks the AI a question restricted to one notebook's sources: retrieval and citations never leave the notebook's scope. Uses a local Ollama instance when configured; otherwise returns a note that AI is not configured. The answer is returned as one aggregated text (no streaming).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"},"query":{"type":"string"}},"required":["notebook","query"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Notebook string `json:"notebook"`
				Query    string `json:"query"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Notebook == "" {
				return nil, fmt.Errorf("notebook is required")
			}
			if args.Query == "" {
				return nil, fmt.Errorf("query is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			answer, err := svc.AskTextScoped(ctx, args.Notebook, args.Query)
			if err != nil {
				return nil, err
			}
			return map[string]string{"answer": answer}, nil
		},
	}
}

// newNotebookCreateTool creates a notebook (issue #428). Mutating — only
// registered on write-enabled surfaces, matching every other note-creating
// tool in this registry.
func newNotebookCreateTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "notebook_create",
		Description: "Creates a new notebook: a named, bounded set of vault sources used to scope later AI grounding, retrieval and generated artifacts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"description":{"type":"string"}},"required":["title"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Title       string `json:"title"`
				Description string `json:"description"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Title == "" {
				return nil, fmt.Errorf("title is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.NotebookNew(args.Title, args.Description)
		},
	}
}

// newNotebookAddSourceTool adds a vault file to a notebook's source set
// (issue #428). Mutating. The path is validated against the vault boundary
// the same way every other vault-relative-path tool is.
func newNotebookAddSourceTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "notebook_add_source",
		Description: "Adds a vault file to a notebook's source set.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"},"path":{"type":"string","description":"vault-relative path of the file to add"}},"required":["notebook","path"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Notebook string `json:"notebook"`
				Path     string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Notebook == "" || args.Path == "" {
				return nil, fmt.Errorf("notebook and path are required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.NotebookAddSource(args.Notebook, args.Path)
		},
	}
}

// newNotebookRemoveSourceTool removes a vault file from a notebook's source
// set (issue #428). Mutating. The referenced file itself is never touched
// (VAULT.md section 10).
func newNotebookRemoveSourceTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "notebook_remove_source",
		Description: "Removes a vault file from a notebook's source set. The file itself is left untouched.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"notebook":{"type":"string","description":"notebook id or path"},"path":{"type":"string","description":"vault-relative path of the file to remove"}},"required":["notebook","path"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Notebook string `json:"notebook"`
				Path     string `json:"path"`
			}
			if err := json.Unmarshal(input, &args); err != nil {
				return nil, err
			}
			if args.Notebook == "" || args.Path == "" {
				return nil, fmt.Errorf("notebook and path are required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.NotebookRemoveSource(args.Notebook, args.Path)
		},
	}
}
