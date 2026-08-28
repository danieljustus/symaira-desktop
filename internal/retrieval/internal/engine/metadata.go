package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/parser"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const (
	searchMetadataStart           = "__SYMDESK_SEARCH_METADATA_START__"
	searchMetadataEnd             = "__SYMDESK_SEARCH_METADATA_END__"
	metadataTitleTagBoost float32 = 0.1
	metadataFieldBoost    float32 = 0.02
)

// SearchMetadata is the selected, already-parsed metadata included in the
// hybrid index representation. Fields are sorted into a canonical order when
// rendered, making repeatable indexing independent of map or caller order.
type SearchMetadata struct {
	Fields []SearchMetadataField
}

// SearchMetadataField is one scalar metadata value. Weight controls how many
// times the value is emitted in the representation; title and tags use the
// fixed higher weight chosen by the vault integration.
type SearchMetadataField struct {
	Name   string
	Value  string
	Weight int
}

// SearchMetadataFromVault maps the selected vault contract fields to the
// canonical search representation used by every local-file indexing path.
func SearchMetadataFromVault(doc *vault.Document) SearchMetadata {
	if doc == nil {
		return SearchMetadata{}
	}
	metadata := SearchMetadata{}
	add := func(name, value string, weight int) {
		if strings.TrimSpace(value) != "" {
			metadata.Fields = append(metadata.Fields, SearchMetadataField{Name: name, Value: value, Weight: weight})
		}
	}
	add("title", doc.Title, 3)
	add("tags", strings.Join(doc.Tags, " "), 3)
	add("aliases", strings.Join(doc.Aliases, " "), 2)
	add("created", doc.Created, 1)
	add("document_date", doc.DocumentDate, 1)
	add("person", doc.Person, 1)
	add("status", doc.Status, 1)
	add("due_date", doc.DueDate, 1)
	add("ocr_json_path", doc.OcrJSONPath, 1)
	add("simhash", doc.Simhash, 1)
	add("type", doc.Type, 1)
	if doc.ASN != nil {
		add("asn", fmt.Sprintf("%d", *doc.ASN), 1)
	}
	for _, key := range []string{"document_type", "correspondent", "source_path", "mime", "category", "ocr_engine", "archive_path", "imported_from", "import_run_id", "source_uri", "download_uri", "ingested_at", "sha256", "meeting_id", "notebook_id", "base_id"} {
		if value, ok := doc.Frontmatter[key]; ok {
			add(key, fmt.Sprint(value), 1)
		}
	}
	if value, ok := doc.Frontmatter["confidence"]; ok {
		add("confidence", fmt.Sprint(value), 1)
	}
	return metadata
}

// metadataSections prepends a synthetic metadata section while retaining all
// source sections and their existing location anchors. Synthetic chunks do not
// receive source character spans because their text is not present in the file.
func prependSearchMetadata(sections []parser.Section, metadata SearchMetadata) []parser.Section {
	text := formatSearchMetadata(metadata)
	if text == "" {
		return sections
	}
	result := make([]parser.Section, 0, len(sections)+1)
	result = append(result, parser.Section{
		Text:      text,
		Anchor:    parser.Anchor{Kind: "section", Value: "metadata"},
		Synthetic: true,
	})
	return append(result, sections...)
}

func formatSearchMetadata(metadata SearchMetadata) string {
	fields := append([]SearchMetadataField(nil), metadata.Fields...)
	sort.SliceStable(fields, func(i, j int) bool {
		left, right := metadataFieldRank(fields[i].Name), metadataFieldRank(fields[j].Name)
		if left != right {
			return left < right
		}
		if fields[i].Name != fields[j].Name {
			return fields[i].Name < fields[j].Name
		}
		return fields[i].Value < fields[j].Value
	})

	var b strings.Builder
	for _, field := range fields {
		name := strings.TrimSpace(field.Name)
		value := strings.Join(strings.Fields(field.Value), " ")
		if name == "" || value == "" {
			continue
		}
		weight := field.Weight
		if weight <= 0 {
			weight = 1
		}
		if weight > 4 {
			weight = 4
		}
		for i := 0; i < weight; i++ {
			fmt.Fprintf(&b, "%s: %s\n", name, value)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return searchMetadataStart + "\n" + b.String() + searchMetadataEnd
}

func metadataFieldRank(name string) int {
	switch name {
	case "title":
		return 0
	case "tags":
		return 1
	case "aliases":
		return 2
	case "created":
		return 3
	case "document_date":
		return 4
	case "due_date":
		return 5
	case "type":
		return 6
	case "status":
		return 7
	case "person":
		return 8
	case "correspondent":
		return 9
	case "asn":
		return 10
	default:
		return 100
	}
}

// MetadataMatches returns metadata field names whose values contain one of the
// free-text query terms. The order is the representation order and duplicates
// are removed. It is intentionally derived from the indexed text so old index
// rows remain readable and no database schema migration is needed.
func MetadataMatches(query, content string) []string {
	start := strings.Index(content, searchMetadataStart)
	if start < 0 {
		return nil
	}
	start += len(searchMetadataStart)
	end := strings.Index(content[start:], searchMetadataEnd)
	if end < 0 {
		return nil
	}
	metadataText := content[start : start+end]
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}

	seen := make(map[string]struct{})
	var matches []string
	for _, line := range strings.Split(metadataText, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		value = strings.ToLower(strings.TrimSpace(value))
		if name == "" || value == "" {
			continue
		}
		for _, term := range terms {
			term = strings.Trim(term, "\"'")
			if term != "" && strings.Contains(value, term) {
				if _, exists := seen[name]; !exists {
					seen[name] = struct{}{}
					matches = append(matches, name)
				}
				break
			}
		}
	}
	return matches
}

func metadataBoost(fields []string) float32 {
	for _, field := range fields {
		if field == "title" || field == "tags" {
			return metadataTitleTagBoost
		}
	}
	if len(fields) > 0 {
		return metadataFieldBoost
	}
	return 0
}

func StripSearchMetadata(content string) string {
	start := strings.Index(content, searchMetadataStart)
	if start < 0 {
		return content
	}
	end := strings.Index(content[start+len(searchMetadataStart):], searchMetadataEnd)
	if end < 0 {
		return strings.TrimSpace(content[:start])
	}
	end += start + len(searchMetadataStart) + len(searchMetadataEnd)
	return strings.TrimSpace(content[:start] + "\n" + content[end:])
}
