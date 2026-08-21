package engine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
)

// sidecarDirName mirrors symingest's internal/annotate.SidecarDirName: the
// vault-relative directory where grounded extraction sidecars are written,
// named "<sha256-of-source-file>.jsonl".
const sidecarDirName = ".symaira/extractions"

var frontmatterBlockRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*(\n|$)`)
var frontmatterSHA256RE = regexp.MustCompile(`(?m)^sha256:\s*"?([0-9a-fA-F]{64})"?\s*$`)

// sidecarSpan mirrors symingest's internal/annotate.Span JSON shape.
type sidecarSpan struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Snippet string `json:"snippet"`
}

// sidecarExtraction mirrors symingest's internal/annotate.Extraction JSON
// shape. symseek only consumes the sidecar format; it does not depend on
// symingest as a Go module.
type sidecarExtraction struct {
	Field   string       `json:"field"`
	Type    string       `json:"type"`
	Value   string       `json:"value"`
	Span    *sidecarSpan `json:"span,omitempty"`
	Matched bool         `json:"matched"`
}

// frontmatterSHA256 extracts the sha256 field from a Markdown note's YAML
// frontmatter block, as documented by symingest's FRONTMATTER.md. Returns
// ("", false) when there is no frontmatter block or no sha256 field.
func frontmatterSHA256(content string) (string, bool) {
	m := frontmatterBlockRE.FindStringSubmatch(content)
	if m == nil {
		return "", false
	}
	sm := frontmatterSHA256RE.FindStringSubmatch(m[1])
	if sm == nil {
		return "", false
	}
	return strings.ToLower(sm[1]), true
}

// findSidecarPath walks upward from the document's directory looking for a
// ".symaira/extractions/<sha256>.jsonl" sidecar, mirroring how tools like
// git locate their marker directory. It stops at the first vault root found
// (any ancestor containing a ".symaira" directory) or at the filesystem
// root. Returns "" when no sidecar is found.
func findSidecarPath(docPath, sha256 string) string {
	dir := filepath.Dir(docPath)
	for {
		candidate := filepath.Join(dir, sidecarDirName, sha256+".jsonl")
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// detectSidecarPath returns the extraction sidecar path for a document, or
// "" if the document is not Markdown, has no frontmatter sha256, or no
// matching sidecar file exists on disk.
func detectSidecarPath(path, content string) string {
	if strings.ToLower(filepath.Ext(path)) != ".md" {
		return ""
	}
	sha, ok := frontmatterSHA256(content)
	if !ok {
		return ""
	}
	return findSidecarPath(path, sha)
}

// ImportExtractionSidecar reads a sidecar JSONL file and replaces the
// document's persisted extractions with its contents. It always deletes
// existing extractions for the document path first, so reindexing (or
// re-running an import) never duplicates rows and never leaves stale ones
// behind. Exported for reuse by the CLI's manual "extract import" command.
func ImportExtractionSidecar(dbClient db.Store, docPath, sidecarPath string) error {
	f, err := os.Open(sidecarPath)
	if err != nil {
		return fmt.Errorf("open sidecar %s: %w", sidecarPath, err)
	}
	defer f.Close()

	chunkSpans, err := chunkSpansStore(dbClient, docPath)
	if err != nil {
		return fmt.Errorf("load chunk spans for %s: %w", docPath, err)
	}

	now := time.Now()
	var extractions []*db.Extraction
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var se sidecarExtraction
		if err := json.Unmarshal([]byte(line), &se); err != nil {
			return fmt.Errorf("%s: line %d: invalid JSON: %w", sidecarPath, lineNo, err)
		}

		class := se.Field
		if class == "" {
			class = se.Type
		}
		e := &db.Extraction{
			DocumentPath: docPath,
			Class:        class,
			Value:        se.Value,
			Matched:      se.Matched,
			Producer:     "symingest/annotate",
			SourceRef:    sidecarPath,
			CreatedAt:    now,
		}
		if se.Span != nil {
			start, end := se.Span.Start, se.Span.End
			e.SpanStart = &start
			e.SpanEnd = &end
			e.EvidenceText = se.Span.Snippet
			e.ChunkID = bestMatchingChunkID(chunkSpans, start, end)
		}
		extractions = append(extractions, e)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read sidecar %s: %w", sidecarPath, err)
	}

	if err := dbClient.DeleteExtractionsForDocument(docPath); err != nil {
		return fmt.Errorf("clear existing extractions for %s: %w", docPath, err)
	}
	if len(extractions) == 0 {
		return nil
	}
	return dbClient.SaveExtractions(extractions)
}

// chunkSpanProvider is implemented by db.Store's concrete type to fetch
// chunk id/span data without pulling in content and embeddings.
type chunkSpanProvider interface {
	GetChunkSpansForDocument(docPath string) ([]*db.Chunk, error)
}

func chunkSpansStore(dbClient db.Store, docPath string) ([]*db.Chunk, error) {
	provider, ok := dbClient.(chunkSpanProvider)
	if !ok {
		return nil, nil
	}
	return provider.GetChunkSpansForDocument(docPath)
}

// bestMatchingChunkID returns the ID of the chunk whose character span
// contains the extraction span's midpoint, or the chunk with the closest
// span edge if none contains it exactly. Returns nil when no chunk has span
// data (e.g. indexed before chunk spans were added).
func bestMatchingChunkID(chunks []*db.Chunk, spanStart, spanEnd int) *int64 {
	mid := (spanStart + spanEnd) / 2

	var best *db.Chunk
	bestDist := -1
	for _, c := range chunks {
		if c.CharStart == nil || c.CharEnd == nil {
			continue
		}
		if mid >= *c.CharStart && mid < *c.CharEnd {
			id := c.ID
			return &id
		}
		dist := 0
		if mid < *c.CharStart {
			dist = *c.CharStart - mid
		} else {
			dist = mid - *c.CharEnd
		}
		if bestDist == -1 || dist < bestDist {
			bestDist = dist
			best = c
		}
	}
	if best == nil {
		return nil
	}
	id := best.ID
	return &id
}
