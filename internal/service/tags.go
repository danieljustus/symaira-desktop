package service

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// TagRenameResult reports the outcome of a vault-wide tag operation for one
// file. Status is "updated" when the file carried the tag and was rewritten,
// or "skipped" when it did not.
type TagRenameResult struct {
	File   string `json:"file"`
	Status string `json:"status"` // "updated" | "skipped" | "error"
	Error  string `json:"error,omitempty"`
}

// TagsRename rewrites `from` to `to` in the frontmatter of every Markdown file
// in the vault and re-indexes each changed file, so the sidecar never keeps a
// stale row for the old tag (issue #306).
func (s *Service) TagsRename(from, to string) ([]TagRenameResult, error) {
	from = normalizeTag(from)
	to = normalizeTag(to)
	if from == "" {
		return nil, fmt.Errorf("source tag must not be empty")
	}
	if to == "" {
		return nil, fmt.Errorf("target tag must not be empty")
	}
	if from == to {
		return nil, fmt.Errorf("source and target tag are identical")
	}

	return s.walkTags(func(doc *vault.Document) ([]string, bool) {
		tags := append([]string(nil), doc.Tags...)
		changed := false
		for i, t := range tags {
			if strings.EqualFold(strings.TrimSpace(t), from) {
				tags[i] = to
				changed = true
			}
		}
		if !changed {
			return nil, false
		}
		return dedupeTags(tags), true
	})
}

// TagsMerge moves every occurrence of `from` onto `to` (deduplicated) and
// re-indexes changed files.
func (s *Service) TagsMerge(from, to string) ([]TagRenameResult, error) {
	from = normalizeTag(from)
	to = normalizeTag(to)
	if from == "" {
		return nil, fmt.Errorf("source tag must not be empty")
	}
	if to == "" {
		return nil, fmt.Errorf("target tag must not be empty")
	}
	if from == to {
		return nil, fmt.Errorf("source and target tag are identical")
	}

	return s.walkTags(func(doc *vault.Document) ([]string, bool) {
		tags := append([]string(nil), doc.Tags...)
		changed := false
		out := tags[:0]
		for _, t := range tags {
			if strings.EqualFold(strings.TrimSpace(t), from) {
				changed = true
				continue
			}
			out = append(out, t)
		}
		if !changed {
			return nil, false
		}
		if !containsTag(out, to) {
			out = append(out, to)
		}
		return out, true
	})
}

// TagsDelete removes `tag` from the frontmatter of every Markdown file in the
// vault and re-indexes changed files.
func (s *Service) TagsDelete(tag string) ([]TagRenameResult, error) {
	tag = normalizeTag(tag)
	if tag == "" {
		return nil, fmt.Errorf("tag must not be empty")
	}

	return s.walkTags(func(doc *vault.Document) ([]string, bool) {
		tags := append([]string(nil), doc.Tags...)
		out := tags[:0]
		changed := false
		for _, t := range tags {
			if strings.EqualFold(strings.TrimSpace(t), tag) {
				changed = true
				continue
			}
			out = append(out, t)
		}
		if !changed {
			return nil, false
		}
		return out, true
	})
}

// walkTags walks every Markdown file, applies `mutate` to its tag list and
// writes + re-indexes the file when the mutation reports a change.
func (s *Service) walkTags(mutate func(doc *vault.Document) ([]string, bool)) ([]TagRenameResult, error) {
	var results []TagRenameResult
	err := vault.Walk(s.VaultRoot, func(path string) error {
		rel := strings.TrimPrefix(path, s.VaultRoot)
		rel = strings.TrimPrefix(rel, string(filepath.Separator))

		doc, err := vault.ParseFile(path)
		if err != nil {
			results = append(results, TagRenameResult{File: rel, Status: "error", Error: err.Error()})
			return nil // keep walking; one bad file must not abort the batch
		}
		tags, changed := mutate(doc)
		if !changed {
			results = append(results, TagRenameResult{File: rel, Status: "skipped"})
			return nil
		}
		if tags == nil {
			tags = []string{}
		}
		if err := vault.SetFrontmatterValue(path, "tags", tags); err != nil {
			results = append(results, TagRenameResult{File: rel, Status: "error", Error: err.Error()})
			return nil
		}
		reparsed, err := vault.ParseFile(path)
		if err != nil {
			results = append(results, TagRenameResult{File: rel, Status: "error", Error: err.Error()})
			return nil
		}
		if err := s.IndexDocument(reparsed); err != nil {
			results = append(results, TagRenameResult{File: rel, Status: "error", Error: err.Error()})
			return nil
		}
		results = append(results, TagRenameResult{File: rel, Status: "updated"})
		return nil
	})
	return results, err
}

func normalizeTag(tag string) string {
	return strings.TrimSpace(tag)
}

func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if strings.EqualFold(strings.TrimSpace(t), tag) {
			return true
		}
	}
	return false
}

func dedupeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if !containsTag(out, t) {
			out = append(out, t)
		}
	}
	return out
}
