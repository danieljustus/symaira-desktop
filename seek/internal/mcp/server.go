package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danieljustus/symaira-corekit/mcpserver"

	"github.com/danieljustus/symaira-seek/internal/db"
	"github.com/danieljustus/symaira-seek/internal/engine"
	symerrors "github.com/danieljustus/symaira-seek/internal/errors"
	"github.com/danieljustus/symaira-seek/internal/pathutil"
)

var ServerVersion = "dev"

func StartServer(cfg engine.OllamaConfig, quantCfg *db.QuantConfig, rerankCfg engine.RerankConfig, expandCfg engine.ExpandConfig) error {
	dbClient, err := db.Open()
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer dbClient.Close()
	dbClient.SetQuantConfig(quantCfg)

	embedder := engine.NewEmbeddingsGeneratorWithOllamaConfig(cfg)
	searchOpts := engine.SearchOptions{RerankCfg: rerankCfg, ExpandCfg: expandCfg}
	server := mcpserver.New("symseek", ServerVersion)

	registerSearchDocuments(server, dbClient, dbClient, embedder, searchOpts)
	registerReadDocument(server, dbClient, embedder)
	registerListDocuments(server, dbClient, embedder)
	registerGetContext(server, dbClient, dbClient, embedder, searchOpts)
	registerIndexDocument(server, dbClient, embedder)
	registerIndexURL(server, dbClient, embedder)
	registerMultiGet(server, dbClient, embedder)
	registerSetContext(server, dbClient)
	registerGetContexts(server, dbClient)
	registerSearchExtractions(server, dbClient)
	registerListExtractions(server, dbClient)
	registerGetDocumentExtractions(server, dbClient)

	return server.ServeStdio(context.Background())
}

func registerSearchDocuments(server *mcpserver.Server, dbClient db.Store, vectorStore db.VectorStore, embedder engine.Embedder, searchOpts engine.SearchOptions) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "search_documents",
		Description: "Search the local document index for relevant content using hybrid keyword (BM25) and vector search. Use when the user asks about specific topics, files, or information that might be indexed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"Natural language search query"},"limit":{"type":"integer","description":"Maximum number of search results to return (default 5)"},"format":{"type":"string","description":"Output format: 'json' (structured results) or 'text' (human-readable). Default: 'json'."},"path_prefix":{"type":"string","description":"Optional document path prefix to restrict search results to a subtree"}},"required":["query"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Query      string `json:"query"`
				Limit      int    `json:"limit"`
				Format     string `json:"format"`
				PathPrefix string `json:"path_prefix"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Query == "" {
				return nil, &symerrors.ValidationError{Field: "query", Message: "missing or invalid query argument"}
			}
			if params.Limit <= 0 {
				params.Limit = 5
			}

			opts := searchOpts
			opts.PathFilter = params.PathPrefix
			results, err := engine.SearchHybridWithOptions(dbClient, vectorStore, embedder, params.Query, params.Limit, opts)
			if err != nil {
				return nil, &symerrors.SearchError{Query: params.Query, Err: err}
			}

			// Extract individual terms from the query for snippet focus.
		queryTerms := strings.Fields(params.Query)

		switch strings.ToLower(params.Format) {
		case "text":
			return renderSearchText(results, dbClient, queryTerms)
		default:
			structured := make([]*db.StructuredSearchResult, 0, len(results))
			for _, r := range results {
				if s := r.Structured(); s != nil {
					s.Snippet = engine.BuildSnippet(s.Snippet, queryTerms, engine.DefaultSnippetBound)
					structured = append(structured, s)
				}
			}
			data, err := json.Marshal(structured)
			if err != nil {
				return nil, fmt.Errorf("marshal search results: %w", err)
			}
			return string(data), nil
		}
		},
	})
}

// renderSearchText returns the legacy human-readable text representation of
// search results, including folder context annotations when available.
func renderSearchText(results []*db.SearchResult, dbClient db.Store, queryTerms []string) (string, error) {
	type contextMatcher interface {
		GetMatchingContext(path string) (*db.FolderContext, error)
	}
	var matcher contextMatcher
	if cm, ok := dbClient.(contextMatcher); ok {
		matcher = cm
	}

	var textBuilder strings.Builder
	for idx, r := range results {
		snippet := engine.BuildSnippet(r.Chunk.Content, queryTerms, engine.DefaultSnippetBound)
		textBuilder.WriteString(fmt.Sprintf("[%d] File: %s (Chunk %d, RRF Score: %.4f)\n", idx+1, r.Chunk.DocumentPath, r.Chunk.ChunkIndex, r.RRFScore))
		if matcher != nil {
			if fc, err := matcher.GetMatchingContext(r.Chunk.DocumentPath); err == nil && fc != nil {
				textBuilder.WriteString(fmt.Sprintf("Context: %s — %s\n", fc.PathPrefix, fc.ContextText))
			}
		}
		textBuilder.WriteString(snippet)
		textBuilder.WriteString("\n\n")
	}
	return textBuilder.String(), nil
}

func registerReadDocument(server *mcpserver.Server, dbClient db.Store, _ engine.Embedder) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "read_document",
		Description: "Retrieve the text content of an indexed document, optionally limited to a specific line range. Use when the user needs to inspect the detailed content of a specific file.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the document file"},"fromLine":{"type":"integer","description":"First line to return (1-based, default 1)"},"maxLines":{"type":"integer","description":"Maximum number of lines to return from fromLine (default: all remaining lines)"}},"required":["path"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Path     string `json:"path"`
				FromLine int    `json:"fromLine"`
				MaxLines int    `json:"maxLines"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Path == "" {
				return nil, &symerrors.ValidationError{Field: "path", Message: "missing or invalid path argument"}
			}
			if params.FromLine < 0 {
				return nil, &symerrors.ValidationError{Field: "fromLine", Message: "fromLine must be >= 1"}
			}
			if params.FromLine == 0 {
				params.FromLine = 1
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

			doc, err := dbClient.GetDocument(resolvedPath)
			if err != nil {
				return nil, &symerrors.DatabaseError{Op: "get document", Err: err}
			}
			if doc == nil {
				return nil, &symerrors.FileNotFoundError{Path: resolvedPath}
			}

			const maxReadBytes = 10 << 20
			f, err := os.Open(resolvedPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read file: %w", err)
			}
			defer f.Close()

			limitedReader := io.LimitReader(f, maxReadBytes+1)
			data, err := io.ReadAll(limitedReader)
			if err != nil {
				return nil, fmt.Errorf("failed to read file: %w", err)
			}

			content := string(data)
			if int64(len(data)) > maxReadBytes {
				content = content[:maxReadBytes]
				content += "\n\n[Truncated: file exceeds 10 MB read limit]"
			}

			// Apply line-range filtering when fromLine or maxLines are set.
			if params.FromLine > 1 || params.MaxLines > 0 {
				lines := strings.Split(content, "\n")
				totalLines := len(lines)

				// fromLine is 1-based; convert to 0-based index.
				start := params.FromLine - 1
				if start >= totalLines {
					return "", nil
				}

				end := totalLines
				if params.MaxLines > 0 {
					end = start + params.MaxLines
					if end > totalLines {
						end = totalLines
					}
				}

				return strings.Join(lines[start:end], "\n"), nil
			}

			return content, nil
		},
	})
}

func registerListDocuments(server *mcpserver.Server, dbClient db.Store, _ engine.Embedder) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "list_documents",
		Description: "Browse the list of indexed documents. Use to explore what folders/files are currently in the index.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"folder":{"type":"string","description":"Optional folder path prefix to filter the document list"},"limit":{"type":"integer","description":"Maximum number of documents to return (default 50, max 500)"},"offset":{"type":"integer","description":"Number of documents to skip (default 0)"}}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Folder string `json:"folder"`
				Limit  int    `json:"limit"`
				Offset int    `json:"offset"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}

			if params.Limit <= 0 {
				params.Limit = 50
			}
			const maxLimit = 500
			if params.Limit > maxLimit {
				params.Limit = maxLimit
			}
			if params.Offset < 0 {
				params.Offset = 0
			}

			docs, err := dbClient.ListDocuments()
			if err != nil {
				return nil, &symerrors.DatabaseError{Op: "list documents", Err: err}
			}

			// Filter by folder if specified.
			var filtered []*db.Document
			for _, d := range docs {
				if params.Folder == "" || strings.HasPrefix(d.Path, params.Folder) {
					filtered = append(filtered, d)
				}
			}

			// Deterministic ordering: sort by path.
			slices.SortFunc(filtered, func(a, b *db.Document) int {
				if a.Path < b.Path {
					return -1
				}
				if a.Path > b.Path {
					return 1
				}
				return 0
			})

			totalCount := len(filtered)

			// Apply offset.
			if params.Offset >= totalCount {
				filtered = nil
			} else {
				filtered = filtered[params.Offset:]
			}

			// Apply limit.
			hasMore := len(filtered) > params.Limit
			if len(filtered) > params.Limit {
				filtered = filtered[:params.Limit]
			}

			type docEntry struct {
				Path      string `json:"path"`
				UpdatedAt string `json:"updated_at"`
			}
			entries := make([]docEntry, len(filtered))
			for i, d := range filtered {
				entries[i] = docEntry{Path: d.Path, UpdatedAt: d.UpdatedAt.Format(time.RFC3339)}
			}

			result := struct {
				Count     int        `json:"count"`
				HasMore   bool       `json:"has_more"`
				Documents []docEntry `json:"documents"`
			}{
				Count:     totalCount,
				HasMore:   hasMore,
				Documents: entries,
			}
			data, err := json.Marshal(result)
			if err != nil {
				return nil, fmt.Errorf("marshal list results: %w", err)
			}
			return string(data), nil
		},
	})
}

func registerGetContext(server *mcpserver.Server, dbClient db.Store, vectorStore db.VectorStore, embedder engine.Embedder, searchOpts engine.SearchOptions) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "get_context",
		Description: "Compile a consolidated context block from multiple matching documents on a given topic. This combines search and read tools.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","description":"The topic or concept to extract context for"},"max_chars":{"type":"integer","description":"Maximum character count (Unicode code points) of the combined context (default 4000)."}},"required":["topic"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Topic    string `json:"topic"`
				MaxChars int    `json:"max_chars"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Topic == "" {
				return nil, &symerrors.ValidationError{Field: "topic", Message: "missing or invalid topic argument"}
			}

			maxChars := params.MaxChars
			if maxChars == 0 {
				maxChars = 4000
			}

			results, err := engine.SearchHybridWithOptions(dbClient, vectorStore, embedder, params.Topic, 10, searchOpts)
			if err != nil {
				return nil, &symerrors.SearchError{Query: params.Topic, Err: err}
			}

			var textBuilder strings.Builder
			runeCount := 0
			header := fmt.Sprintf("=== CONTEXT FOR TOPIC: %s ===\n", params.Topic)
			textBuilder.WriteString(header)
			runeCount += utf8.RuneCountInString(header)
			for _, r := range results {
				chunkRunes := utf8.RuneCountInString(r.Chunk.Content)
				if runeCount+chunkRunes > maxChars {
					break
				}
				src := fmt.Sprintf("Source: %s (Chunk %d)\n", r.Chunk.DocumentPath, r.Chunk.ChunkIndex)
				sep := "\n---\n"
				textBuilder.WriteString(src)
				textBuilder.WriteString(r.Chunk.Content)
				textBuilder.WriteString(sep)
				runeCount += utf8.RuneCountInString(src) + chunkRunes + utf8.RuneCountInString(sep)
			}

			// Append index provenance block (best-effort).
			if stats, statErr := dbClient.GetStats(); statErr == nil {
				provenanceParts := make([]string, 0, 3)
				if stats.LastIndexedAt.IsZero() {
					provenanceParts = append(provenanceParts,
						fmt.Sprintf("document_count=%d", stats.DocumentCount))
				} else {
					ageSeconds := int(time.Since(stats.LastIndexedAt).Seconds())
					provenanceParts = append(provenanceParts,
						fmt.Sprintf("document_count=%d", stats.DocumentCount),
						fmt.Sprintf("indexed_at=%s", stats.LastIndexedAt.Format(time.RFC3339)),
						fmt.Sprintf("age_seconds=%d", ageSeconds))
				}
				provenanceLine := fmt.Sprintf("\n---\nIndex provenance: %s\n",
					strings.Join(provenanceParts, ", "))
				textBuilder.WriteString(provenanceLine)
			}
			return textBuilder.String(), nil
		},
	})
}

func registerIndexDocument(server *mcpserver.Server, dbClient db.Store, embedder engine.Embedder) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "index_document",
		Description: "Index a specific local file or folder immediately. Use when the user wants to add a new directory or file to the search database.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Absolute path to the file or directory to index"}},"required":["path"]}`),
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

			info, err := os.Stat(absPath)
			if err != nil {
				return nil, &symerrors.FileNotFoundError{Path: params.Path}
			}

			if info.IsDir() {
				if err := engine.IndexDirectory(dbClient, embedder, absPath); err != nil {
					return nil, &symerrors.IndexError{Path: absPath, Err: err}
				}
				return fmt.Sprintf("Successfully indexed directory: %s", absPath), nil
			}

			hash, err := IndexSingleFile(dbClient, embedder, absPath)
			if err != nil {
				return nil, &symerrors.IndexError{Path: absPath, Err: err}
			}
			return fmt.Sprintf("Successfully indexed file: %s (Hash: %s)", absPath, hash), nil
		},
	})
}

func IndexSingleFile(dbClient db.Store, embedder engine.Embedder, path string) (string, error) {
	return engine.IndexFile(dbClient, embedder, path)
}

func registerIndexURL(server *mcpserver.Server, dbClient db.Store, embedder engine.Embedder) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "index_url",
		Description: "Index content from a URL. Fetches content using symfetch (if available) or HTTP GET, then indexes it for search.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","description":"URL to fetch and index"}},"required":["url"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.URL == "" {
				return nil, &symerrors.ValidationError{Field: "url", Message: "missing or invalid url argument"}
			}

			if err := engine.IndexURL(dbClient, embedder, params.URL); err != nil {
				return nil, &symerrors.IndexError{Path: params.URL, Err: err}
			}
			return fmt.Sprintf("Successfully indexed URL: %s", params.URL), nil
		},
	})
}

func registerMultiGet(server *mcpserver.Server, dbClient db.Store, _ engine.Embedder) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "multi_get",
		Description: "Retrieve multiple indexed documents at once using a glob pattern. Returns each file's content with a path header. Files exceeding limits are skipped.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","description":"Glob pattern to match indexed document paths (e.g., \"docs/**/*.md\")"},"maxBytes":{"type":"integer","description":"Per-file byte limit in bytes (default 10485760, i.e. 10 MB)"},"maxLines":{"type":"integer","description":"Per-file line limit (default: no limit)"}},"required":["pattern"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Pattern  string `json:"pattern"`
				MaxBytes int    `json:"maxBytes"`
				MaxLines int    `json:"maxLines"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Pattern == "" {
				return nil, &symerrors.ValidationError{Field: "pattern", Message: "missing or invalid pattern argument"}
			}

			const defaultMaxBytes = 10 << 20
			if params.MaxBytes <= 0 {
				params.MaxBytes = defaultMaxBytes
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("cannot determine home directory: %w", err)
			}
			homeResolved, err := filepath.EvalSymlinks(home)
			if err != nil {
				return nil, fmt.Errorf("cannot resolve home directory: %w", err)
			}

			absPattern := params.Pattern
			if !filepath.IsAbs(absPattern) {
				absPattern = filepath.Join(homeResolved, absPattern)
			} else {
				dir := filepath.Dir(absPattern)
				base := filepath.Base(absPattern)
				if dirResolved, err := filepath.EvalSymlinks(dir); err == nil {
					absPattern = filepath.Join(dirResolved, base)
				}
			}
			absPattern = filepath.Clean(absPattern)

			if !strings.HasPrefix(absPattern, homeResolved+string(os.PathSeparator)) && absPattern != homeResolved {
				return nil, &pathutil.PathRestrictionError{Path: absPattern, Root: homeResolved}
			}

			docs, err := dbClient.ListDocuments()
			if err != nil {
				return nil, &symerrors.DatabaseError{Op: "list documents", Err: err}
			}

			var textBuilder strings.Builder
			matchCount := 0
			skipCount := 0

			for _, doc := range docs {
				if !matchGlob(absPattern, doc.Path) {
					continue
				}
				matchCount++

				f, err := os.Open(doc.Path)
				if err != nil {
					textBuilder.WriteString(fmt.Sprintf("--- SKIPPED: %s (cannot open: %v) ---\n\n", doc.Path, err))
					skipCount++
					continue
				}

				limitedReader := io.LimitReader(f, int64(params.MaxBytes)+1)
				data, err := io.ReadAll(limitedReader)
				f.Close()
				if err != nil {
					textBuilder.WriteString(fmt.Sprintf("--- SKIPPED: %s (read error: %v) ---\n\n", doc.Path, err))
					skipCount++
					continue
				}

				exceedsBytes := int64(len(data)) > int64(params.MaxBytes)
				content := string(data)
				if exceedsBytes {
					content = content[:params.MaxBytes]
				}

				if params.MaxLines > 0 {
					lines := strings.Split(content, "\n")
					if len(lines) > params.MaxLines {
						content = strings.Join(lines[:params.MaxLines], "\n")
						exceedsBytes = true // treat as skipped due to limit
					}
				}

				if exceedsBytes || int64(len(data)) > int64(params.MaxBytes) {
					textBuilder.WriteString(fmt.Sprintf("--- SKIPPED: %s (exceeds maxBytes or maxLines) ---\n\n", doc.Path))
					skipCount++
					continue
				}

				textBuilder.WriteString(fmt.Sprintf("=== %s ===\n", doc.Path))
				textBuilder.WriteString(content)
				textBuilder.WriteString("\n\n")
			}

			if matchCount == 0 {
				textBuilder.WriteString(fmt.Sprintf("No indexed documents matched pattern: %s\n", params.Pattern))
			} else if skipCount > 0 {
				textBuilder.WriteString(fmt.Sprintf("\n%d file(s) matched, %d skipped due to limits.\n", matchCount, skipCount))
			} else {
				textBuilder.WriteString(fmt.Sprintf("\n%d file(s) matched.\n", matchCount))
			}

			return textBuilder.String(), nil
		},
	})
}

func registerSetContext(server *mcpserver.Server, dbClient db.Store) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "set_context",
		Description: "Store descriptive context text for a filesystem path prefix. Later searches will display the matching context for each result. Use to annotate folder trees with QMD-style metadata.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path prefix to associate with context (e.g. \"/home/user/docs/api\")"},"text":{"type":"string","description":"Descriptive context text for this path prefix"}},"required":["path","text"]}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var params struct {
				Path string `json:"path"`
				Text string `json:"text"`
			}
			if err := json.Unmarshal(input, &params); err != nil {
				return nil, &symerrors.ValidationError{Field: "params", Message: err.Error()}
			}
			if params.Path == "" {
				return nil, &symerrors.ValidationError{Field: "path", Message: "missing or invalid path argument"}
			}
			if params.Text == "" {
				return nil, &symerrors.ValidationError{Field: "text", Message: "missing or invalid text argument"}
			}

			type contextSetter interface {
				SetFolderContext(path, text string) error
			}
			cs, ok := dbClient.(contextSetter)
			if !ok {
				return nil, fmt.Errorf("database does not support folder contexts")
			}
			if err := cs.SetFolderContext(params.Path, params.Text); err != nil {
				return nil, &symerrors.DatabaseError{Op: "set folder context", Err: err}
			}
			return fmt.Sprintf("Context set for %s", params.Path), nil
		},
	})
}

func registerGetContexts(server *mcpserver.Server, dbClient db.Store) {
	server.RegisterTool(&mcpserver.Tool{
		Name:        "get_contexts",
		Description: "List all stored folder context entries. Shows each path prefix and its associated context text.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			type contextLister interface {
				GetFolderContexts() ([]db.FolderContext, error)
			}
			cl, ok := dbClient.(contextLister)
			if !ok {
				return nil, fmt.Errorf("database does not support folder contexts")
			}
			contexts, err := cl.GetFolderContexts()
			if err != nil {
				return nil, &symerrors.DatabaseError{Op: "list folder contexts", Err: err}
			}
			if len(contexts) == 0 {
				return "No folder contexts configured.", nil
			}
			var textBuilder strings.Builder
			for _, fc := range contexts {
				textBuilder.WriteString(fmt.Sprintf("%s — %s\n", fc.PathPrefix, fc.ContextText))
			}
			return textBuilder.String(), nil
		},
	})
}

// matchGlob checks whether path matches the glob pattern, supporting ** for
// matching across directory boundaries. Pattern and path should both be
// absolute, slash-separated paths.
func matchGlob(pattern, path string) bool {
	patParts := strings.Split(filepath.ToSlash(pattern), "/")
	pathParts := strings.Split(filepath.ToSlash(path), "/")
	return matchGlobSegments(patParts, pathParts)
}

func matchGlobSegments(patParts, pathParts []string) bool {
	if len(patParts) == 0 {
		return len(pathParts) == 0
	}

	if patParts[0] == "**" {
		// ** matches zero or more path segments.
		for i := 0; i <= len(pathParts); i++ {
			if matchGlobSegments(patParts[1:], pathParts[i:]) {
				return true
			}
		}
		return false
	}

	if len(pathParts) == 0 {
		return false
	}

	matched, _ := filepath.Match(patParts[0], pathParts[0])
	if !matched {
		return false
	}

	return matchGlobSegments(patParts[1:], pathParts[1:])
}
