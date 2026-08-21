package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-seek/internal/db"
	symerrors "github.com/danieljustus/symaira-seek/internal/errors"
	"github.com/danieljustus/symaira-seek/internal/pathutil"
)

// marshalExtractionsJSON serializes extraction results to a JSON string. Tool
// handlers must return this as the string content itself (not the raw
// slice/struct) — the MCP "text" content field is a JSON string, so a
// pre-serialized string is required to avoid embedding an unescaped JSON
// array/object where a string is expected.
func marshalExtractionsJSON(extractions []*db.Extraction) (string, error) {
	data, err := json.Marshal(extractions)
	if err != nil {
		return "", fmt.Errorf("marshal extractions: %w", err)
	}
	return string(data), nil
}

func registerSearchExtractions(server *mcpserver.Server, dbClient db.Store) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "search_extractions",
		Description: "Full-text search over grounded document extractions (values and evidence text), such as amounts, deadlines, identifiers, or parties. Returns a JSON array of extraction objects as a string.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Full-text search query"},"limit":{"type":"integer","description":"Maximum number of results to return (default 10)"}},"required":["query"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Query string `json:"query"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Query == "" {
				return nil, &symerrors.ValidationError{Field: "query", Message: "missing or invalid query argument"}
			}
			if params.Limit <= 0 {
				params.Limit = 10
			}

			results, err := dbClient.SearchExtractions(params.Query, params.Limit)
			if err != nil {
				return nil, &symerrors.DatabaseError{Op: "search extractions", Err: err}
			}
			return marshalExtractionsJSON(results)
		},
	})
}

func registerListExtractions(server *mcpserver.Server, dbClient db.Store) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "list_extractions",
		Description: "List grounded document extractions, optionally filtered by class (e.g. amount, deadline, party). Returns a JSON array of extraction objects as a string.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"class":{"type":"string","description":"Optional extraction class to filter by"},"limit":{"type":"integer","description":"Maximum number of results to return (default 50)"}}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Class string `json:"class"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Limit <= 0 {
				params.Limit = 50
			}

			results, err := dbClient.ListExtractions(params.Class, params.Limit)
			if err != nil {
				return nil, &symerrors.DatabaseError{Op: "list extractions", Err: err}
			}
			return marshalExtractionsJSON(results)
		},
	})
}

func registerGetDocumentExtractions(server *mcpserver.Server, dbClient db.Store) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "get_document_extractions",
		Description: "Retrieve all grounded extractions for a specific indexed document. Returns a JSON array of extraction objects as a string.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the indexed document"}},"required":["path"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Path == "" {
				return nil, &symerrors.ValidationError{Field: "path", Message: "missing or invalid path argument"}
			}

			absPath, err := pathutil.RestrictToHome(params.Path)
			if err != nil {
				if _, ok := err.(*pathutil.PathRestrictionError); ok {
					return nil, fmt.Errorf("path restriction: %w", err)
				}
				return nil, fmt.Errorf("path error: %w", err)
			}

			resolvedPath, err := filepath.EvalSymlinks(absPath)
			if err != nil {
				return nil, &symerrors.FileNotFoundError{Path: params.Path}
			}

			results, err := dbClient.GetDocumentExtractions(resolvedPath)
			if err != nil {
				return nil, &symerrors.DatabaseError{Op: "get document extractions", Err: err}
			}
			return marshalExtractionsJSON(results)
		},
	})
}
