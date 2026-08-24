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

// TagsRename rewrites `from` to `to` in the frontmatter and inline occurrences
// of every Markdown file in the vault and re-indexes each changed file, so the
// sidecar never keeps a stale row for the old tag (issue #306, issue #522).
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

	return s.walkTags(
		func(fmTags []string) ([]string, bool) {
			tags := append([]string(nil), fmTags...)
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
		},
		func(body string) (string, bool) {
			return vault.RewriteInlineTags(body, func(tag string) (string, bool) {
				if strings.EqualFold(strings.TrimSpace(tag), from) {
					return to, true
				}
				return tag, true
			})
		},
	)
}

// TagsMerge moves every occurrence of `from` onto `to` (deduplicated) across
// frontmatter and inline occurrences and re-indexes changed files.
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

	return s.walkTags(
		func(fmTags []string) ([]string, bool) {
			tags := append([]string(nil), fmTags...)
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
		},
		func(body string) (string, bool) {
			return vault.RewriteInlineTags(body, func(tag string) (string, bool) {
				if strings.EqualFold(strings.TrimSpace(tag), from) {
					return to, true
				}
				return tag, true
			})
		},
	)
}

// TagsDelete removes `tag` from the frontmatter and inline occurrences of every
// Markdown file in the vault and re-indexes changed files.
func (s *Service) TagsDelete(tag string) ([]TagRenameResult, error) {
	tag = normalizeTag(tag)
	if tag == "" {
		return nil, fmt.Errorf("tag must not be empty")
	}

	return s.walkTags(
		func(fmTags []string) ([]string, bool) {
			tags := append([]string(nil), fmTags...)
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
		},
		func(body string) (string, bool) {
			return vault.RewriteInlineTags(body, func(t string) (string, bool) {
				if strings.EqualFold(strings.TrimSpace(t), tag) {
					return "", false
				}
				return t, true
			})
		},
	)
}

// walkTags walks every Markdown file, applies mutateFM and mutateBody, and
// writes + re-indexes the file when mutations report a change.
func (s *Service) walkTags(
	mutateFM func(fmTags []string) ([]string, bool),
	mutateBody func(body string) (string, bool),
) ([]TagRenameResult, error) {
	var results []TagRenameResult
	err := vault.Walk(s.VaultRoot, func(path string) error {
		rel := strings.TrimPrefix(path, s.VaultRoot)
		rel = strings.TrimPrefix(rel, string(filepath.Separator))

		doc, err := vault.ParseFile(path)
		if err != nil {
			results = append(results, TagRenameResult{File: rel, Status: "error", Error: err.Error()})
			return nil // keep walking; one bad file must not abort the batch
		}

		changed, err := vault.RewriteDocumentTagsAndBody(path, doc, mutateFM, mutateBody)
		if err != nil {
			results = append(results, TagRenameResult{File: rel, Status: "error", Error: err.Error()})
			return nil
		}
		if !changed {
			results = append(results, TagRenameResult{File: rel, Status: "skipped"})
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
