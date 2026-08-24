package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/history"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
	"github.com/danieljustus/symaira-desktop/internal/retrieval"
	"github.com/danieljustus/symaira-desktop/internal/searchquery"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Service encapsulates the core operations of symdesk.
type Service struct {
	VaultRoot string
	DB        *sidecar.DB
	ViewsMgr  *dbviews.Manager
	History   *history.Store
	Config    *config.Config
}

// New creates a new Service instance.
func New(vaultRoot string, db *sidecar.DB) *Service {
	canonical, err := filepath.EvalSymlinks(vaultRoot)
	if err != nil {
		canonical = vaultRoot
	}
	cfg, err := config.Load()
	if err != nil {
		cfg = config.DefaultConfig()
	}
	return &Service{
		VaultRoot: canonical,
		DB:        db,
		ViewsMgr:  dbviews.NewManager(canonical),
		History:   history.NewStore(canonical),
		Config:    cfg,
	}
}

// snapshotBefore records a history snapshot of the file at absPath before a
// mutation. Snapshot failures never block the write itself; they are logged
// so the user's edit is not lost to a safety-net error.
func (s *Service) snapshotBefore(absPath string) {
	rel, err := filepath.Rel(s.VaultRoot, absPath)
	if err != nil {
		slog.Warn("history snapshot skipped", "path", absPath, "error", err)
		return
	}
	if _, err := s.History.Snapshot(rel); err != nil {
		slog.Warn("history snapshot failed", "path", rel, "error", err)
	}
}

func (s *Service) IndexDocument(doc *vault.Document) error {
	if err := s.DB.IndexDocument(doc); err != nil {
		return err
	}
	retrieval.Index(doc.Path, doc.Body)
	return nil
}

func (s *Service) DeleteDocument(path string) error {
	if err := s.DB.DeleteDocument(path); err != nil {
		return err
	}
	retrieval.Delete(path)
	return nil
}

// Prune removes stale entries from the sidecar index: files that have been
// deleted from the vault or that fall under ignore rules (hidden dirs,
// node_modules, etc.). Returns the number of entries removed.
func (s *Service) Prune() (int, error) {
	return s.DB.Prune(s.VaultRoot)
}

// Ls returns a list of files in the vault.
func (s *Service) Ls(dirPrefix string) ([]FileEntry, error) {
	docs, err := s.DB.ListFiles(dirPrefix)
	if err != nil {
		return nil, err
	}
	// A per-vault sidecar may be new after an upgrade. Populate it lazily on
	// the first list so existing app users do not have to re-run onboarding.
	// DB.RefreshIndex's stat-based fast path also means a later call here
	// (e.g. after the sidecar was cleared) skips re-reading and re-hashing
	// any file whose cached size/mtime still match what's on disk.
	if len(docs) == 0 {
		if err := s.DB.RefreshIndex(s.VaultRoot); err != nil {
			return nil, err
		}
		docs, err = s.DB.ListFiles(dirPrefix)
		if err != nil {
			return nil, err
		}
	}

	var results []FileEntry
	for _, d := range docs {
		relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
		if relPath == "" {
			relPath = d.Path
		}
		results = append(results, FileEntry{
			Path:     relPath,
			Title:    d.Title,
			Type:     d.Type,
			Modified: d.Created, // Re-using Created field for modified_at in docs
		})
	}
	return results, nil
}

// SearchResult is a single typed search hit emitted by Search and
// SearchWithMeta. Its JSON tags match the legacy map[string]interface{} keys
// to keep the CLI/MCP wire format byte-identical.
type SearchResult struct {
	Path    string  `json:"path"`
	Title   string  `json:"title"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// FileEntry is a typed directory listing entry returned by Ls. JSON tags
// match the legacy map[string]interface{} keys for wire compatibility.
type FileEntry struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Type     string `json:"type,omitempty"`
	Modified string `json:"modified"`
}

// SearchResponse is the shared result contract for CLI, MCP and the native app.
// Hint is set only when malformed search syntax was safely retried as plain
// full-text input.
type SearchResponse struct {
	Results []SearchResult `json:"results"`
	Hint    string         `json:"hint,omitempty"`
}

// Search preserves the existing service API for internal callers such as Ask.
// Interactive callers use SearchWithMeta so they can surface syntax hints.
func (s *Service) Search(query string) ([]SearchResult, error) {
	response, err := s.SearchWithMeta(query)
	if err != nil {
		return nil, err
	}
	return response.Results, nil
}

// SearchWithMeta parses the scoped query language and dispatches it to the
// sidecar when its semantics are not supported by sibling search tools. Invalid
// syntax never becomes an error to end users: the original query is searched as
// safe plain text and the caller receives a concise hint.
func (s *Service) SearchWithMeta(query string) (SearchResponse, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResponse{Results: []SearchResult{}}, nil
	}

	plan, err := searchquery.Parse(query)
	if err != nil {
		results, plainErr := s.searchSidecarPlain(query)
		if plainErr != nil {
			return SearchResponse{}, plainErr
		}
		return SearchResponse{
			Results: results,
			Hint:    "Search syntax was invalid, so this was searched as plain full text.",
		}, nil
	}

	if plan.RequiresSidecar() {
		matches, err := s.DB.SearchPlan(plan)
		if err != nil {
			return SearchResponse{}, err
		}
		results := make([]SearchResult, 0, len(matches))
		for _, match := range matches {
			relPath, err := filepath.Rel(s.VaultRoot, match.Path)
			if err != nil {
				relPath = match.Path
			}
			results = append(results, SearchResult{
				Path:    relPath,
				Title:   match.Title,
				Snippet: match.Snippet,
				Score:   0.0,
			})
		}
		return SearchResponse{Results: results}, nil
	}

	results, err := s.searchPlain(query)
	if err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{Results: results}, nil
}

// searchPlain keeps the pre-query-language search behaviour: hybrid
// keyword+vector retrieval for ordinary unscoped full-text terms, falling
// back to the sidecar full-text index when the hybrid index yields nothing
// (or cannot be reached at all).
func (s *Service) searchPlain(query string) ([]SearchResult, error) {
	seekResults := retrieval.Search(query)
	results := make([]SearchResult, 0, len(seekResults))
	for _, r := range seekResults {
		relPath := r.Path
		if filepath.IsAbs(r.Path) {
			candidate := r.Path
			if canonical, canonicalErr := filepath.EvalSymlinks(candidate); canonicalErr == nil {
				candidate = canonical
			}
			rel, relErr := filepath.Rel(s.VaultRoot, candidate)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			relPath = rel
		}
		resolved, secureErr := vault.SecurePath(s.VaultRoot, relPath)
		if secureErr != nil {
			continue
		}
		if info, statErr := os.Stat(resolved); statErr != nil || !info.Mode().IsRegular() {
			continue
		}

		title := ""
		if docTitle, err := s.DB.GetTitle(r.Path); err == nil {
			title = docTitle
		} else if docTitle, err := s.DB.GetTitle(resolved); err == nil {
			title = docTitle
		} else {
			base := filepath.Base(r.Path)
			title = strings.TrimSuffix(base, filepath.Ext(base))
		}

		results = append(results, SearchResult{
			Path:    relPath,
			Title:   title,
			Snippet: r.Snippet,
			Score:   r.Score,
		})
	}
	if len(results) > 0 {
		return results, nil
	}

	return s.searchSidecarPlain(query)
}

// searchSidecarPlain is the safe FTS5-only fallback used when query syntax is
// malformed. It intentionally does not delegate to the hybrid engine, which
// would interpret the invalid characters as its own query language.
func (s *Service) searchSidecarPlain(query string) ([]SearchResult, error) {
	docs, err := s.DB.Search(query)
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(docs))
	for _, d := range docs {
		relPath, _ := filepath.Rel(s.VaultRoot, d.Path)
		results = append(results, SearchResult{
			Path:    relPath,
			Title:   d.Title,
			Snippet: d.Body,
			Score:   0.0, // FTS doesn't return score trivially here without further query modifications
		})
	}
	return results, nil
}

// Props returns the properties for a given file.
func (s *Service) Props(file string) (map[string]interface{}, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, file)
	if err != nil {
		return nil, err
	}
	return s.DB.GetProperties(absPath)
}

// Backlinks returns the files linking to the given file.
func (s *Service) Backlinks(file string) ([]string, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, file)
	if err != nil {
		return nil, err
	}
	links, err := s.DB.GetBacklinks(absPath)
	if err != nil {
		return nil, err
	}

	var relLinks []string
	for _, p := range links {
		rel, _ := filepath.Rel(s.VaultRoot, p)
		relLinks = append(relLinks, rel)
	}
	return relLinks, nil
}

// NoteNew creates a new note in the vault and indexes it.
func (s *Service) NoteNew(title, content, templateName string) (string, error) {
	if title == "" {
		return "", fmt.Errorf("title is required")
	}

	fileName := strings.ReplaceAll(title, " ", "_") + ".md"
	absPath, err := vault.SecurePath(s.VaultRoot, fileName)
	if err != nil {
		return "", err
	}

	// Create content with frontmatter
	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	// Load template if specified
	templateContent := ""
	if templateName != "" {
		tplPath, err := vault.SecurePath(s.VaultRoot, filepath.Join("templates", templateName+".md"))
		if err == nil {
			if b, err := os.ReadFile(tplPath); err == nil {
				templateContent = string(b)
			}
		}
	}

	fullContent := ""
	if templateContent != "" {
		// Substitute placeholders
		templateContent = strings.ReplaceAll(templateContent, "{{title}}", title)
		templateContent = strings.ReplaceAll(templateContent, "{{date}}", now.Format("2006-01-02"))
		templateContent = strings.ReplaceAll(templateContent, "{{time}}", now.Format("15:04"))

		// If template has frontmatter, we just use the template directly and append content
		if strings.HasPrefix(templateContent, "---\n") {
			fullContent = templateContent
			if content != "" {
				fullContent += "\n" + content
			}
		} else {
			fullContent = fmt.Sprintf("---\ntitle: \"%s\"\ncreated: \"%s\"\ntags: []\n---\n\n%s", title, nowStr, templateContent)
			if content != "" {
				fullContent += "\n" + content
			}
		}
	} else {
		fullContent = fmt.Sprintf("---\ntitle: \"%s\"\ncreated: \"%s\"\ntags: []\n---\n\n%s", title, nowStr, content)
	}

	s.snapshotBefore(absPath)
	if err := os.WriteFile(absPath, []byte(fullContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	// For templates with frontmatter that might lack created/title, ensure they are set
	if templateContent != "" && strings.HasPrefix(templateContent, "---\n") {
		_ = vault.SetFrontmatterKey(absPath, "title", title)
		_ = vault.SetFrontmatterKey(absPath, "created", nowStr)
	}

	// Index immediately
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return fileName, err
	}

	hash := sha256.Sum256([]byte(fullContent))
	doc.SHA256 = hex.EncodeToString(hash[:])

	if err := s.IndexDocument(doc); err != nil {
		return fileName, fmt.Errorf("failed to index new file: %w", err)
	}

	return fileName, nil
}

// NoteDaily creates or opens today's note.
func (s *Service) NoteDaily(dateStr string) (string, error) {
	t := time.Now().UTC()
	if dateStr != "" {
		parsed, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			return "", fmt.Errorf("invalid date format, use YYYY-MM-DD: %w", err)
		}
		t = parsed
	}

	// Default daily note naming: YYYY-MM-DD
	title := t.Format("2006-01-02")
	fileName := title + ".md"
	absPath, err := vault.SecurePath(s.VaultRoot, fileName)
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(absPath); err == nil {
		// Already exists
		return fileName, nil
	}

	// Create it, trying "daily" template
	return s.NoteNew(title, "", "daily")
}

// webClippers are the sibling binaries that can render a URL into the shared
// fetch output schema, in preference order. symbrowse is first because it
// absorbed symfetch's static engine in the repo consolidation; symfetch stays
// supported so an existing installation keeps working.
var webClippers = []struct {
	binary string
	args   func(url string) []string
}{
	{binary: "symbrowse", args: func(url string) []string { return []string{"read", url} }},
	{binary: "symfetch", args: func(url string) []string { return []string{url} }},
}

// clipURL renders a URL with the first available web clipper and returns its
// markdown. Both clippers emit the same document schema, so the caller does
// not need to know which one answered.
func clipURL(url string) (string, error) {
	var lastErr error
	for _, clipper := range webClippers {
		// Resolve order: $SYMAIRA_BIN, the managed runtime directory
		// (~/.symaira/bin), then PATH.
		bin, err := compose.Resolve(clipper.binary)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		cmd := exec.CommandContext(ctx, bin, clipper.args(url)...) //nolint:gosec // resolved via compose.Resolve; url is a CLI argument, not shell-interpreted
		var out bytes.Buffer
		cmd.Stdout = &out
		err = cmd.Run()
		cancel()
		if err != nil {
			lastErr = fmt.Errorf("%s failed: %w", clipper.binary, err)
			continue
		}
		return out.String(), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("no web clipper found: install symbrowse (danieljustus/tap/symbrowse)")
}

// clipTitle extracts the document title from a clipper's markdown. It accepts
// both shapes of the shared schema: the YAML frontmatter symbrowse emits and
// the quoted header block symfetch emits.
func clipTitle(body, url string) string {
	lines := strings.Split(body, "\n")

	// symbrowse: YAML frontmatter with a title key.
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for _, line := range lines[1:] {
			if strings.TrimSpace(line) == "---" {
				break
			}
			if rest, ok := strings.CutPrefix(line, "title:"); ok {
				if t := strings.Trim(strings.TrimSpace(rest), `"`); t != "" {
					return t
				}
			}
		}
	}

	// symfetch: `> **Title** · 200 · ~42 tokens`
	if len(lines) > 0 && strings.HasPrefix(lines[0], "> **") {
		parts := strings.SplitN(lines[0][4:], "**", 2)
		if len(parts) > 1 && strings.TrimSpace(parts[0]) != "" {
			return strings.TrimSpace(parts[0])
		}
	}

	// Fallback to the first Markdown heading.
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(line[2:])
		}
	}

	// Last resort: the URL itself.
	return url
}

// NoteClip fetches a URL via the available web clipper and saves it as a note.
func (s *Service) NoteClip(url string) (string, error) {
	bodyStr, err := clipURL(url)
	if err != nil {
		return "", err
	}
	title := clipTitle(bodyStr, url)

	// Prepare the note content
	nowStr := time.Now().UTC().Format(time.RFC3339)
	noteTitle := "Clipped: " + title

	// Sanitize path separators out of the file name: the title can fall back
	// to the raw URL above, which always contains "/" and would otherwise be
	// rejected by vault.SecurePath's path-traversal protection.
	safeTitle := strings.NewReplacer("/", "-", "\\", "-").Replace(noteTitle)
	fileName := strings.ReplaceAll(safeTitle, " ", "_") + ".md"
	absPath, err := vault.SecurePath(s.VaultRoot, fileName)
	if err != nil {
		return "", err
	}

	fullContent := fmt.Sprintf("---\ntitle: %q\ncreated: %q\nsource_uri: %q\ningested_at: %q\ntags: []\n---\n\n%s",
		noteTitle, nowStr, url, nowStr, bodyStr)

	s.snapshotBefore(absPath)
	if err := os.WriteFile(absPath, []byte(fullContent), 0644); err != nil {
		return "", fmt.Errorf("failed to write clipped file: %w", err)
	}

	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return fileName, err
	}

	hash := sha256.Sum256([]byte(fullContent))
	doc.SHA256 = hex.EncodeToString(hash[:])

	if err := s.IndexDocument(doc); err != nil {
		return fileName, fmt.Errorf("failed to index clipped file: %w", err)
	}

	return fileName, nil
}

// NoteMove renames a note and updates the index.
func (s *Service) NoteMove(oldPath, newPath string) error {
	absOld, err := vault.SecurePath(s.VaultRoot, oldPath)
	if err != nil {
		return err
	}
	absNew, err := vault.SecurePath(s.VaultRoot, newPath)
	if err != nil {
		return err
	}

	if err := os.Rename(absOld, absNew); err != nil {
		return fmt.Errorf("failed to move file: %w", err)
	}

	if err := s.DeleteDocument(absOld); err != nil {
		return err
	}

	doc, err := vault.ParseFile(absNew)
	if err != nil {
		return err
	}
	return s.IndexDocument(doc)
}

// PropsEdit updates a frontmatter property in the file and re-indexes.
func (s *Service) PropsEdit(relPath, key, value string) error {
	if key == "asn" {
		return fmt.Errorf("use \"symdesk doc asn <file> <next|N>\" to assign an ASN safely")
	}
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}

	s.snapshotBefore(absPath)
	if err := vault.SetFrontmatterValue(absPath, key, value); err != nil {
		return err
	}

	// Re-parse and index
	newDoc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	return s.IndexDocument(newDoc)
}

func (s *Service) Ask(ctx context.Context, query string, out chan<- interface{}) {
	out <- ai.ToolEvent("search", "running")
	results, err := s.Search(query)
	if err != nil {
		out <- ai.ToolEvent("search", "error")
		out <- ai.AnswerEvent("Search failed: " + err.Error())
		out <- ai.DoneEvent()
		close(out)
		return
	}
	out <- ai.ToolEvent("search", "done")

	for _, r := range results {
		out <- ai.CitationEvent(r.Path, r.Title, r.Snippet, r.Score)
	}

	out <- ai.ToolEvent("llm", "running")
	chunkChan := make(chan ai.AskChunk)
	go func() {
		ai.Ask(ctx, s.Config, query, resultsToMapSlice(results), chunkChan)
	}()

	for chunk := range chunkChan {
		out <- ai.AnswerEvent(chunk.Chunk)
	}
	out <- ai.ToolEvent("llm", "done")
	out <- ai.DoneEvent()
	close(out)
}

// AskText is the buffered variant of Ask for non-streaming consumers
// (MCP): it aggregates all chunks into one answer string.
func (s *Service) AskText(ctx context.Context, query string) (string, error) {
	results, err := s.Search(query)
	if err != nil {
		return "", err
	}
	chunkChan := make(chan ai.AskChunk)
	go ai.Ask(ctx, s.Config, query, resultsToMapSlice(results), chunkChan)

	var b strings.Builder
	for chunk := range chunkChan {
		b.WriteString(chunk.Chunk)
	}
	return b.String(), nil
}

// resultsToMapSlice is a bridge: it converts typed SearchResults to the
// untyped []map[string]interface{} that the internal ai package still
// expects. This conversion exists so that the ai package can be migrated
// independently without breaking the typed service contract.
func resultsToMapSlice(results []SearchResult) []map[string]interface{} {
	out := make([]map[string]interface{}, len(results))
	for i, r := range results {
		out[i] = map[string]interface{}{
			"path":    r.Path,
			"title":   r.Title,
			"snippet": r.Snippet,
			"score":   r.Score,
		}
	}
	return out
}

// Ingest copies a file into the inbox and indexes the new note.
func (s *Service) Ingest(sourcePath string) (map[string]string, error) {
	relPath, err := ingest.IngestFile(s.VaultRoot, sourcePath)
	if err != nil {
		return nil, err
	}

	// Index the new note
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseFile(absPath)
	if err == nil {
		_ = s.IndexDocument(doc)
	}

	return map[string]string{"path": relPath}, nil
}

type RelatedEntity struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Relation string `json:"relation,omitempty"`
}

type RelatedData struct {
	Entities []RelatedEntity `json:"entities"`
	Notes    []string        `json:"notes"`
}

// Related returns entities and notes related to the given file based on symmemory.
func (s *Service) Related(file string) (*RelatedData, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, file)
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return nil, err
	}

	result := &RelatedData{
		Entities: []RelatedEntity{},
		Notes:    []string{},
	}

	if ok, _ := compose.HasSymmemory(); !ok {
		return result, nil
	}

	// 1. Fetch all entities from symmemory
	entities, err := compose.ListEntities()
	if err != nil {
		return nil, err
	}

	// 2. Find matching entities for this document
	matchedEntities := make(map[string]compose.MemoryEntity)
	for _, e := range entities {
		matches := false
		if strings.EqualFold(doc.Title, e.Name) {
			matches = true
		} else {
			baseName := filepath.Base(doc.Path)
			nameWithoutExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
			if strings.EqualFold(nameWithoutExt, e.Name) {
				matches = true
			}
		}
		for _, alias := range e.Aliases {
			if strings.EqualFold(doc.Title, alias) {
				matches = true
			}
		}
		// Substring check in content body (case-insensitive)
		if !matches && doc.Body != "" {
			bodyLower := strings.ToLower(doc.Body)
			if strings.Contains(bodyLower, strings.ToLower(e.Name)) {
				matches = true
			} else {
				for _, alias := range e.Aliases {
					if strings.Contains(bodyLower, strings.ToLower(alias)) {
						matches = true
					}
				}
			}
		}

		if matches {
			matchedEntities[e.ID] = e
			result.Entities = append(result.Entities, RelatedEntity{
				Name: e.Name,
				Type: e.Type,
			})
		}
	}

	// 3. For each matched entity, get neighbors to find 1-hop related entities
	neighborEntities := make(map[string]compose.MemoryEntity)
	for _, me := range matchedEntities {
		neighbors, err := compose.GetNeighbors(me.Name)
		if err == nil && neighbors != nil {
			for _, node := range neighbors.Nodes {
				if _, ok := matchedEntities[node.ID]; !ok {
					// Find the edge type connecting me to node
					relType := ""
					for _, edge := range neighbors.Edges {
						if (edge.FromEntityID == me.ID && edge.ToEntityID == node.ID) ||
							(edge.FromEntityID == node.ID && edge.ToEntityID == me.ID) {
							relType = edge.RelationType
							break
						}
					}
					if _, ok := neighborEntities[node.ID]; !ok {
						neighborEntities[node.ID] = node
						result.Entities = append(result.Entities, RelatedEntity{
							Name:     node.Name,
							Type:     node.Type,
							Relation: relType,
						})
					}
				}
			}
		}
	}

	// 4. Find other files in the vault that also match these entities (direct or neighbor)
	allDocs, err := s.DB.ListFiles("")
	if err != nil {
		return result, nil
	}

	noteMap := make(map[string]bool)
	for _, d := range allDocs {
		rel, _ := filepath.Rel(s.VaultRoot, d.Path)
		if rel == file {
			continue // skip self
		}

		// Parse the document to match against the entities
		otherDoc, err := vault.ParseFile(d.Path)
		if err != nil {
			continue
		}

		matches := false
		// Check against matchedEntities and neighborEntities
		for _, e := range matchedEntities {
			if matchesOther(otherDoc, e) {
				matches = true
				break
			}
		}
		if !matches {
			for _, e := range neighborEntities {
				if matchesOther(otherDoc, e) {
					matches = true
					break
				}
			}
		}

		if matches && !noteMap[rel] {
			noteMap[rel] = true
			result.Notes = append(result.Notes, rel)
		}
	}

	return result, nil
}

func matchesOther(doc *vault.Document, e compose.MemoryEntity) bool {
	if strings.EqualFold(doc.Title, e.Name) {
		return true
	}
	baseName := filepath.Base(doc.Path)
	nameWithoutExt := strings.TrimSuffix(baseName, filepath.Ext(baseName))
	if strings.EqualFold(nameWithoutExt, e.Name) {
		return true
	}
	for _, alias := range e.Aliases {
		if strings.EqualFold(doc.Title, alias) {
			return true
		}
	}
	if doc.Body != "" {
		bodyLower := strings.ToLower(doc.Body)
		if strings.Contains(bodyLower, strings.ToLower(e.Name)) {
			return true
		}
		for _, alias := range e.Aliases {
			if strings.Contains(bodyLower, strings.ToLower(alias)) {
				return true
			}
		}
	}
	return false
}

// IngestJobs lists the jobs from symingest.
func (s *Service) IngestJobs() (string, error) {
	return ingest.IngestJobs()
}

// IngestRetry retries a failed job by ID in symingest.
func (s *Service) IngestRetry(jobID string) error {
	return ingest.IngestRetry(jobID)
}

// StoreAsset stores a binary asset in the vault's assets folder under collision-safe naming rules.
func (s *Service) StoreAsset(data []byte, preferredName, ext string) (string, error) {
	return vault.StoreAsset(s.VaultRoot, data, preferredName, ext, "", time.Now())
}

// StoreAssetWithLink stores a binary asset and returns both its vault-relative path and standard Markdown link snippet.
func (s *Service) StoreAssetWithLink(data []byte, preferredName, ext string) (relPath string, mdLink string, err error) {
	relPath, err = s.StoreAsset(data, preferredName, ext)
	if err != nil {
		return "", "", err
	}
	return relPath, vault.AssetMarkdownLink(relPath), nil
}
