package parser

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/ledongthuc/pdf"
	"golang.org/x/net/html"
)

// MaxIndexFileSize is the maximum file size (in bytes) that the indexer will
// read into memory. Files and individual ZIP entries exceeding this limit are
// skipped or rejected to prevent memory exhaustion.
const MaxIndexFileSize = 10 << 20

// MaxArchiveEntries is the maximum number of entries an Office-style ZIP
// archive may contain. Archives with more entries are rejected at open
// time, before any entry is decompressed (issue #342).
const MaxArchiveEntries = 10000

// MaxArchiveDecompressedBytes is the maximum total number of decompressed
// bytes the indexer will read across all entries of one Office-style ZIP
// archive, guarding against zip bombs whose individual entries stay under
// MaxIndexFileSize. Reading stops with an error once the budget is spent
// (issue #342).
const MaxArchiveDecompressedBytes = 100 << 20

// knownDocumentExtensions are document formats the indexer recognizes but
// does not index (no extraction branch exists). They are reported with an
// explicit skip message when encountered so unsupported documents are
// visible instead of silently ignored (issue #341).
var knownDocumentExtensions = map[string]bool{
	".doc":  true,
	".xls":  true,
	".ppt":  true,
	".rtf":  true,
	".epub": true,
	".odg":  true,
}

// IsKnownDocumentExtension reports whether ext is a recognized document
// format that the indexer does not support.
func IsKnownDocumentExtension(ext string) bool {
	return knownDocumentExtensions[strings.ToLower(ext)]
}

// UnsupportedDocumentSkipMessage returns the one-line skip message emitted
// when a known document format cannot be indexed. It names the file and
// the reason, following the "Skipping %s: ..." stderr pattern.
func UnsupportedDocumentSkipMessage(path, ext string) string {
	return fmt.Sprintf("Skipping %s: %s is a known document format that is not indexed", path, ext)
}

var (
	fileCache   = make(map[string]fileCacheEntry)
	fileCacheMu sync.RWMutex
)

// GetFileHash computes the SHA-256 hash of a file.
// Uses file metadata (mod time + size) to skip hash computation for unchanged files.
func GetFileHash(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	modTime := info.ModTime().UnixNano()
	size := info.Size()

	fileCacheMu.RLock()
	cached, exists := fileCache[path]
	fileCacheMu.RUnlock()

	if exists && cached.ModTime == modTime && cached.Size == size {
		fileCacheMu.Lock()
		cached = fileCache[path]
		fileCacheMu.Unlock()
		if cached.ModTime == modTime && cached.Size == size {
			return cached.Hash, nil
		}
	}

	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}

	hash := hex.EncodeToString(h.Sum(nil))

	fileCacheMu.Lock()
	fileCache[path] = fileCacheEntry{ModTime: modTime, Size: size, Hash: hash}
	fileCacheMu.Unlock()

	return hash, nil
}

// fileCacheEntry stores file metadata for quick change detection.
type fileCacheEntry struct {
	ModTime int64
	Size    int64
	Hash    string
}

// ParseFile reads a file and returns its text content.
// It dispatches to format-specific extractors for PDF, DOCX, XLSX, PPTX,
// and HTML files; all other files are read as raw text (UTF-8).
func ParseFile(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return parsePDF(path)
	case ".docx":
		return parseDOCX(path)
	case ".xlsx":
		return parseXLSX(path)
	case ".pptx":
		return parsePPTX(path)
	case ".odt", ".ods", ".odp":
		return parseODF(path)
	case ".csv":
		return parseCSV(path)
	case ".html", ".htm":
		return parseHTML(path)
	default:
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("failed to open file: %w", err)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, MaxIndexFileSize+1))
		if err != nil {
			return "", fmt.Errorf("failed to read file: %w", err)
		}
		if int64(len(data)) > MaxIndexFileSize {
			return "", fmt.Errorf("file %s exceeds %d byte limit (%d bytes)", path, MaxIndexFileSize, len(data))
		}
		return string(data), nil
	}
}

// blockHTMLElements are HTML block-level elements whose end introduces a
// paragraph break, matching the block-boundary rule used for Office XML
// (issue #340).
var blockHTMLElements = map[string]bool{
	"p":   true,
	"div": true,
	"li":  true,
	"tr":  true,
	"h1":  true,
	"h2":  true,
	"h3":  true,
	"h4":  true,
	"h5":  true,
	"h6":  true,
}

// parseHTML extracts text from an HTML file. Script, style and head
// subtrees are dropped entirely; block-level element boundaries become
// paragraph breaks; entities are decoded by the tokenizer. The file is
// read through the same capped reader as plain text, so the size limit
// applies to HTML input as well (issue #340).
func parseHTML(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxIndexFileSize+1))
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	if int64(len(data)) > MaxIndexFileSize {
		return "", fmt.Errorf("file %s exceeds %d byte limit (%d bytes)", path, MaxIndexFileSize, len(data))
	}

	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}
	return extractHTMLText(doc), nil
}

// extractHTMLText walks a parsed HTML tree and returns its text content:
// script/style/head subtrees are dropped entirely, block-level element
// boundaries become paragraph breaks, and whitespace-only formatting text
// nodes (source indentation) are skipped.
func extractHTMLText(doc *html.Node) string {
	var text strings.Builder
	walkHTMLText(doc, &text)
	return strings.TrimSpace(collapseNewlineRuns(text.String()))
}

func walkHTMLText(n *html.Node, text *strings.Builder) {
	switch n.Type {
	case html.TextNode:
		// Skip formatting whitespace between elements (indentation);
		// inline spaces between spans are kept so words do not fuse.
		if strings.TrimSpace(n.Data) == "" && strings.Contains(n.Data, "\n") {
			return
		}
		text.WriteString(n.Data)
	case html.ElementNode:
		if n.Data == "script" || n.Data == "style" || n.Data == "head" {
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkHTMLText(c, text)
	}
	if n.Type == html.ElementNode && blockHTMLElements[n.Data] {
		text.WriteString("\n\n")
	}
}

// parsePDF extracts text from a PDF file using a pure-Go PDF reader.
// Returns an error for encrypted or image-only PDFs (no OCR in scope).
func parsePDF(path string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open PDF: %w", err)
	}
	defer f.Close()

	var text strings.Builder
	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		content, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		text.WriteString(content)
		text.WriteString("\n")
	}

	result := strings.TrimSpace(text.String())
	if result == "" {
		return "", fmt.Errorf("PDF contains no extractable text (may be image-only)")
	}
	return result, nil
}

// parseDOCX extracts text from a DOCX file (ZIP archive containing word/document.xml).
func parseDOCX(path string) (string, error) {
	return parseOfficeXML(path, "word/document.xml")
}

// parseODF extracts text from an OpenDocument file (ODT/ODS/ODP). The
// archive part content.xml holds the document body and is read through the
// same capped ZIP part primitive as DOCX, with block boundaries preserved
// (issue #341).
func parseODF(path string) (string, error) {
	return parseOfficeXMLPart(path, "content.xml", extractODFText)
}

// parseCSV reads a CSV file and preserves row boundaries: fields within a
// row are joined with tabs (matching the XLSX cell rule) and each row ends
// with a newline (issue #341). The file is read through the same capped
// reader as plain text, so the size limit applies.
func parseCSV(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxIndexFileSize+1))
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	if int64(len(data)) > MaxIndexFileSize {
		return "", fmt.Errorf("file %s exceeds %d byte limit (%d bytes)", path, MaxIndexFileSize, len(data))
	}

	r := csv.NewReader(bytes.NewReader(data))
	r.FieldsPerRecord = -1
	var text strings.Builder
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		text.WriteString(strings.Join(record, "	"))
		text.WriteString("\n")
	}
	return strings.TrimSpace(text.String()), nil
}

// parseXLSX extracts text from an XLSX file (ZIP archive with shared strings + sheet data).
func parseXLSX(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("failed to open XLSX: %w", err)
	}
	defer r.Close()

	if err := checkArchiveEntryCount(path, r.File); err != nil {
		return "", err
	}

	budget := &archiveBudget{remaining: MaxArchiveDecompressedBytes}

	// Try to read shared strings first
	sharedStrings, err := readXLSXSharedStrings(r.File, budget)
	if err != nil {
		sharedStrings = nil
	}
	if budget.exhausted() {
		return "", archiveBudgetExceededError(path)
	}

	var text strings.Builder
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			if budget.exhausted() {
				return "", archiveBudgetExceededError(path)
			}
			content, err := extractXLSXSheetText(f, sharedStrings, budget)
			if err == nil && strings.TrimSpace(content) != "" {
				text.WriteString(content)
				text.WriteString("\n")
			}
			if budget.exhausted() {
				return "", archiveBudgetExceededError(path)
			}
		}
	}

	result := strings.TrimSpace(text.String())
	if result == "" {
		return "", fmt.Errorf("XLSX contains no extractable text")
	}
	return result, nil
}

// parsePPTX extracts text from a PPTX file (ZIP archive with slide XML files).
func parsePPTX(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("failed to open PPTX: %w", err)
	}
	defer r.Close()

	if err := checkArchiveEntryCount(path, r.File); err != nil {
		return "", err
	}

	budget := &archiveBudget{remaining: MaxArchiveDecompressedBytes}

	var text strings.Builder
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			if budget.exhausted() {
				return "", archiveBudgetExceededError(path)
			}
			content, err := extractPPTXSlideText(f, budget)
			if err == nil && strings.TrimSpace(content) != "" {
				text.WriteString(content)
				text.WriteString("\n")
			}
			if budget.exhausted() {
				return "", archiveBudgetExceededError(path)
			}
		}
	}

	result := strings.TrimSpace(text.String())
	if result == "" {
		return "", fmt.Errorf("PPTX contains no extractable text")
	}
	return result, nil
}

// Span is a chunk of text paired with its byte offset range within the
// original source text. Start/End are exact for chunks that fit without
// overlap reconstruction; for chunks stitched from an overlap tail plus a
// separator that was re-inserted to keep the join readable, End may overshoot
// the true source range by up to len(separator) bytes. This is precise enough
// to find the best-matching chunk for an extraction span, not a guarantee
// that text[Start:End] always reproduces Text byte-for-byte.
type Span struct {
	Text  string
	Start int
	End   int
}

// SplitTextWithSpans behaves like SplitText but also returns each chunk's
// byte offset range within the original text, so callers can persist source
// character spans alongside chunk content.
func SplitTextWithSpans(text string, chunkSize, chunkOverlap int) []Span {
	if chunkSize <= 0 {
		return []Span{{Text: text, Start: 0, End: len(text)}}
	}
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 2
	}

	separators := []string{"\n\n", "\n", " ", ""}
	return splitRecursiveSpans(text, 0, separators, chunkSize, chunkOverlap)
}

func splitRecursiveSpans(text string, base int, separators []string, chunkSize, chunkOverlap int) []Span {
	if len(text) <= chunkSize {
		return []Span{{Text: text, Start: base, End: base + len(text)}}
	}

	var separator string
	var nextSeps []string
	found := false
	for i, sep := range separators {
		if strings.Contains(text, sep) {
			separator = sep
			nextSeps = separators[i+1:]
			found = true
			break
		}
	}

	if !found {
		var spans []Span
		for i := 0; i < len(text); i += chunkSize - chunkOverlap {
			end := i + chunkSize
			if end > len(text) {
				end = len(text)
			}
			spans = append(spans, Span{Text: text[i:end], Start: base + i, End: base + end})
			if end == len(text) {
				break
			}
		}
		return spans
	}

	splits := strings.Split(text, separator)
	var finalSpans []Span
	var currentChunk strings.Builder
	chunkStart := 0
	pos := 0

	for i, part := range splits {
		partStart := pos
		pos += len(part)
		if i < len(splits)-1 {
			pos += len(separator)
		}

		if len(part) > chunkSize {
			if currentChunk.Len() > 0 {
				chunkStr := currentChunk.String()
				finalSpans = append(finalSpans, Span{Text: chunkStr, Start: base + chunkStart, End: base + chunkStart + len(chunkStr)})
				currentChunk.Reset()
			}
			subSpans := splitRecursiveSpans(part, base+partStart, nextSeps, chunkSize, chunkOverlap)
			finalSpans = append(finalSpans, subSpans...)
			continue
		}

		sepLen := len(separator)
		if currentChunk.Len() > 0 {
			if currentChunk.Len()+sepLen+len(part) <= chunkSize {
				currentChunk.WriteString(separator)
				currentChunk.WriteString(part)
			} else {
				chunkStr := currentChunk.String()
				finalSpans = append(finalSpans, Span{Text: chunkStr, Start: base + chunkStart, End: base + chunkStart + len(chunkStr)})

				overlapStart := len(chunkStr) - chunkOverlap
				if overlapStart < 0 {
					overlapStart = 0
				}
				tail := chunkStr[overlapStart:]
				// An empty tail means nothing carries over from the previous
				// chunk, so the new chunk starts exactly at this part's real
				// position rather than at chunkStart+overlapStart (which
				// would land on the separator instead of the part).
				var newChunkStart int
				if len(tail) > 0 {
					newChunkStart = chunkStart + overlapStart
				} else {
					newChunkStart = partStart
				}
				currentChunk.Reset()
				currentChunk.WriteString(tail)
				if currentChunk.Len() > 0 && !strings.HasSuffix(tail, separator) {
					currentChunk.WriteString(separator)
				}
				currentChunk.WriteString(part)
				chunkStart = newChunkStart
			}
		} else {
			currentChunk.WriteString(part)
			chunkStart = partStart
		}
	}

	if currentChunk.Len() > 0 {
		chunkStr := currentChunk.String()
		finalSpans = append(finalSpans, Span{Text: chunkStr, Start: base + chunkStart, End: base + chunkStart + len(chunkStr)})
	}

	return finalSpans
}

// SplitText recursively splits a string into chunks of max chunkSize, overlapping by chunkOverlap.
func SplitText(text string, chunkSize, chunkOverlap int) []string {
	if chunkSize <= 0 {
		return []string{text}
	}
	if chunkOverlap >= chunkSize {
		chunkOverlap = chunkSize / 2
	}

	separators := []string{"\n\n", "\n", " ", ""}
	return splitRecursive(text, separators, chunkSize, chunkOverlap)
}

func splitRecursive(text string, separators []string, chunkSize, chunkOverlap int) []string {
	if len(text) <= chunkSize {
		return []string{text}
	}

	// Find the first separator that splits the text
	var separator string
	var nextSeps []string
	found := false
	for i, sep := range separators {
		if strings.Contains(text, sep) {
			separator = sep
			nextSeps = separators[i+1:]
			found = true
			break
		}
	}

	// If no separator found, just cut it by chunkSize
	if !found {
		var chunks []string
		for i := 0; i < len(text); i += chunkSize - chunkOverlap {
			end := i + chunkSize
			if end > len(text) {
				end = len(text)
			}
			chunks = append(chunks, text[i:end])
			if end == len(text) {
				break
			}
		}
		return chunks
	}

	// Split the text by the selected separator
	splits := strings.Split(text, separator)
	var finalChunks []string
	var currentChunk strings.Builder

	for _, part := range splits {
		// If part itself is larger than chunkSize, split it recursively
		if len(part) > chunkSize {
			// First flush current chunk if it has anything
			if currentChunk.Len() > 0 {
				finalChunks = append(finalChunks, currentChunk.String())
				currentChunk.Reset()
			}
			subChunks := splitRecursive(part, nextSeps, chunkSize, chunkOverlap)
			finalChunks = append(finalChunks, subChunks...)
			continue
		}

		// Check if we can add this part to the current chunk
		sepLen := len(separator)
		if currentChunk.Len() > 0 {
			if currentChunk.Len()+sepLen+len(part) <= chunkSize {
				currentChunk.WriteString(separator)
				currentChunk.WriteString(part)
			} else {
				// Flush current and start a new one with overlap
				chunkStr := currentChunk.String()
				finalChunks = append(finalChunks, chunkStr)

				// Determine overlap: take the end of the previous chunk
				overlapStart := len(chunkStr) - chunkOverlap
				if overlapStart < 0 {
					overlapStart = 0
				}
				// Start next chunk with the overlap portion
				currentChunk.Reset()
				currentChunk.WriteString(chunkStr[overlapStart:])
				if currentChunk.Len() > 0 && !strings.HasSuffix(chunkStr[overlapStart:], separator) {
					currentChunk.WriteString(separator)
				}
				currentChunk.WriteString(part)
			}
		} else {
			currentChunk.WriteString(part)
		}
	}

	if currentChunk.Len() > 0 {
		finalChunks = append(finalChunks, currentChunk.String())
	}

	return finalChunks
}

// parseOfficeXML reads an Office Open XML file (DOCX/PPTX) from a ZIP archive.
func parseOfficeXML(path, xmlEntry string) (string, error) {
	return parseOfficeXMLPart(path, xmlEntry, extractXMLText)
}

// parseOfficeXMLPart reads a single XML part from an Office-style ZIP
// archive and extracts its text with the given extractor. Each entry is
// read through the per-entry size cap reduced by the remaining archive
// decompression budget (issue #342).
func parseOfficeXMLPart(path, xmlEntry string, extract func(io.Reader) (string, error)) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", fmt.Errorf("failed to open Office XML: %w", err)
	}
	defer r.Close()

	if err := checkArchiveEntryCount(path, r.File); err != nil {
		return "", err
	}

	budget := &archiveBudget{remaining: MaxArchiveDecompressedBytes}
	for _, f := range r.File {
		if f.Name != xmlEntry {
			continue
		}
		if budget.exhausted() {
			return "", archiveBudgetExceededError(path)
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("failed to read %s: %w", xmlEntry, err)
		}
		cr := &countingReader{r: rc}
		content, err := extract(io.LimitReader(cr, budget.limit()))
		rc.Close()
		budget.spend(cr.n)
		return content, err
	}
	return "", fmt.Errorf("entry %s not found in archive", xmlEntry)
}

// archiveBudget tracks the remaining decompression budget for a single
// Office-style archive (issue #342).
type archiveBudget struct {
	remaining int64
}

// exhausted reports whether the archive decompression budget is spent.
func (b *archiveBudget) exhausted() bool {
	return b.remaining <= 0
}

// limit returns the read limit for the next entry: the per-entry cap,
// reduced by the remaining archive budget.
func (b *archiveBudget) limit() int64 {
	limit := int64(MaxIndexFileSize)
	if b.remaining < limit {
		limit = b.remaining
	}
	if limit < 0 {
		limit = 0
	}
	return limit
}

// spend subtracts the number of bytes actually decompressed from the
// remaining archive budget.
func (b *archiveBudget) spend(n int64) {
	b.remaining -= n
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// checkArchiveEntryCount rejects archives whose entry count exceeds
// MaxArchiveEntries before any entry is decompressed (issue #342).
func checkArchiveEntryCount(path string, files []*zip.File) error {
	if len(files) > MaxArchiveEntries {
		return fmt.Errorf("archive %s has %d entries, exceeding the %d entry limit", path, len(files), MaxArchiveEntries)
	}
	return nil
}

// archiveBudgetExceededError reports that an archive exceeded the total
// decompression budget (issue #342).
func archiveBudgetExceededError(path string) error {
	return fmt.Errorf("archive %s exceeds the %d byte decompression budget", path, MaxArchiveDecompressedBytes)
}

// extractODFText extracts text from an OpenDocument content.xml stream.
// Text lives as character data inside text:p / text:h blocks (and inside
// office:value cells); text:tab becomes a tab, text:line-break a newline,
// and text:s a run of spaces. Block boundaries follow the same rule as
// the Office extraction fix (issue #341).
func extractODFText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var text strings.Builder
	inBlock := 0

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p", "h", "value":
				inBlock++
			case "tab":
				text.WriteString("	")
			case "line-break":
				text.WriteString("\n")
			case "s":
				n := 1
				for _, attr := range t.Attr {
					if attr.Name.Local == "c" {
						if v, err := strconv.Atoi(attr.Value); err == nil && v > 0 {
							n = v
						}
					}
				}
				text.WriteString(strings.Repeat(" ", n))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "h":
				text.WriteString("\n\n")
				inBlock--
			case "value":
				inBlock--
			}
		case xml.CharData:
			if inBlock > 0 {
				text.Write(t)
			}
		}
	}
	return strings.TrimSpace(collapseNewlineRuns(text.String())), nil
}

// extractXMLText parses an XML document and extracts its text content,
// preserving block boundaries (issue #339): paragraph end elements
// (w:p / a:p) become paragraph breaks, break elements (w:br / w:cr / a:br)
// become newlines, w:tab becomes a tab, and a:sp shape end elements
// separate distinct text boxes. Runs of three or more consecutive
// newlines collapse to a single paragraph break so empty paragraphs do
// not inflate chunk boundaries.
func extractXMLText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var text strings.Builder
	inText := false

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "br", "cr":
				text.WriteString("\n")
			case "tab":
				text.WriteString("	")
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p", "sp":
				text.WriteString("\n\n")
			}
		case xml.CharData:
			if inText {
				text.Write(t)
			}
		}
	}
	return strings.TrimSpace(collapseNewlineRuns(text.String())), nil
}

// collapseNewlineRuns collapses runs of three or more consecutive newlines
// into a single paragraph break ("\n\n") so empty paragraphs do not
// inflate the block boundaries of extracted text.
func collapseNewlineRuns(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	newlines := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			newlines++
			if newlines > 2 {
				continue
			}
		} else {
			newlines = 0
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// readXLSXSharedStrings reads the shared strings table from an XLSX archive.
func readXLSXSharedStrings(files []*zip.File, budget *archiveBudget) ([]string, error) {
	for _, f := range files {
		if f.Name == "xl/sharedStrings.xml" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			cr := &countingReader{r: rc}
			strings_, err := parseSharedStrings(io.LimitReader(cr, budget.limit()))
			budget.spend(cr.n)
			return strings_, err
		}
	}
	return nil, fmt.Errorf("shared strings not found")
}

// parseSharedStrings parses the XLSX shared strings XML file.
func parseSharedStrings(r io.Reader) ([]string, error) {
	decoder := xml.NewDecoder(r)
	var strings_ []string
	var current strings.Builder
	inT := false

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inT = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inT = false
			}
			if t.Name.Local == "si" {
				strings_ = append(strings_, current.String())
				current.Reset()
			}
		case xml.CharData:
			if inT {
				current.Write(t)
			}
		}
	}
	return strings_, nil
}

// extractXLSXSheetText extracts text from a single XLSX worksheet XML file.
// Cells are joined with tabs and each row ends with a newline so row
// boundaries survive extraction (issue #339). The entry is read through
// the per-entry cap reduced by the remaining archive budget (#342).
func extractXLSXSheetText(f *zip.File, sharedStrings []string, budget *archiveBudget) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	cr := &countingReader{r: rc}
	decoder := xml.NewDecoder(io.LimitReader(cr, budget.limit()))
	var text strings.Builder
	inV := false
	var cellType string
	var cellValue strings.Builder
	rowHasCell := false

	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		switch t := token.(type) {
		case xml.StartElement:
			if t.Name.Local == "c" {
				cellType = ""
				for _, attr := range t.Attr {
					if attr.Name.Local == "t" {
						cellType = attr.Value
					}
				}
			}
			if t.Name.Local == "v" {
				inV = true
				cellValue.Reset()
			}
		case xml.EndElement:
			if t.Name.Local == "v" {
				inV = false
			}
			if t.Name.Local == "c" {
				val := cellValue.String()
				if cellType == "s" && sharedStrings != nil {
					idx := 0
					fmt.Sscanf(val, "%d", &idx)
					if idx < len(sharedStrings) {
						val = sharedStrings[idx]
					}
				}
				if val != "" {
					if rowHasCell {
						text.WriteString("	")
					}
					text.WriteString(val)
					rowHasCell = true
				}
			}
			if t.Name.Local == "row" {
				text.WriteString("\n")
				rowHasCell = false
			}
		case xml.CharData:
			if inV {
				cellValue.Write(t)
			}
		}
	}
	budget.spend(cr.n)
	return text.String(), nil
}

// extractPPTXSlideText extracts text from a single PPTX slide XML file.
// It shares the block-boundary extraction rule with DOCX (issue #339).
// The entry is read through the per-entry cap reduced by the remaining
// archive budget (#342).
func extractPPTXSlideText(f *zip.File, budget *archiveBudget) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()

	cr := &countingReader{r: rc}
	content, err := extractXMLText(io.LimitReader(cr, budget.limit()))
	budget.spend(cr.n)
	return content, err
}
