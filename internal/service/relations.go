package service

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/vault"
	"gopkg.in/yaml.v3"
)

// InverseRelation describes a note that points at the target note, either via
// a frontmatter property or via a wikilink in the note body.
type InverseRelation struct {
	Source   string `json:"source"`
	Title    string `json:"title"`
	Property string `json:"property"`
}

// bodyLinkProperty is the pseudo property reported for plain wikilinks that
// appear in a note body rather than in frontmatter.
const bodyLinkProperty = "_link"

// RelationsInverse computes which notes reference the given note. Frontmatter
// properties containing a matching wikilink or title are reported under their
// property key; body wikilinks are reported under the pseudo property "_link".
func (s *Service) RelationsInverse(relPath string) ([]InverseRelation, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return nil, err
	}

	baseName := filepath.Base(absPath)
	title := strings.TrimSuffix(baseName, filepath.Ext(baseName))

	targetNames := []string{title, baseName}
	if props, err := s.DB.GetProperties(absPath); err == nil {
		if t, ok := props["title"].(string); ok && t != "" {
			targetNames = append(targetNames, t)
		}
		if aRaw, ok := props["aliases"]; ok {
			aliasesStr := fmt.Sprintf("%v", aRaw)
			var list []string
			if err := yaml.Unmarshal([]byte(aliasesStr), &list); err == nil && len(list) > 0 {
				for _, item := range list {
					if trimmed := strings.TrimSpace(item); trimmed != "" {
						targetNames = append(targetNames, trimmed)
					}
				}
			} else {
				trimmed := strings.TrimPrefix(strings.TrimSuffix(aliasesStr, "]"), "[")
				for _, part := range strings.Split(trimmed, ",") {
					cleaned := strings.Trim(strings.TrimSpace(part), `"'`)
					if cleaned != "" {
						targetNames = append(targetNames, cleaned)
					}
				}
			}
		}
	}

	docs, err := s.DB.ListFiles("")
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	results := make([]InverseRelation, 0)

	add := func(sourceAbs, sourceTitle, property string) {
		rel, err := filepath.Rel(s.VaultRoot, sourceAbs)
		if err != nil {
			rel = sourceAbs
		}
		rel = filepath.ToSlash(rel)
		key := rel + "\x00" + property
		if seen[key] {
			return
		}
		seen[key] = true
		results = append(results, InverseRelation{Source: rel, Title: sourceTitle, Property: property})
	}

	for _, d := range docs {
		if d.Path == absPath {
			continue
		}
		props, err := s.DB.GetProperties(d.Path)
		if err != nil {
			continue
		}
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			for _, targetName := range targetNames {
				if propertyReferences(props[k], targetName) {
					add(d.Path, d.Title, k)
					break
				}
			}
		}
	}

	backlinks, err := s.DB.GetBacklinks(absPath)
	if err != nil {
		return nil, err
	}
	titleByPath := make(map[string]string, len(docs))
	for _, d := range docs {
		titleByPath[d.Path] = d.Title
	}
	sort.Strings(backlinks)
	for _, p := range backlinks {
		if p == absPath {
			continue
		}
		add(p, titleByPath[p], bodyLinkProperty)
	}

	return results, nil
}

// propertyReferences reports whether a frontmatter value points at the given
// note title, either as a bare value or as a [[wikilink]] (with optional
// alias) embedded in the value. Lists are matched element-wise.
func propertyReferences(value interface{}, title string) bool {
	switch v := value.(type) {
	case []interface{}:
		for _, item := range v {
			if propertyReferences(item, title) {
				return true
			}
		}
		return false
	case string:
		return stringReferences(v, title)
	default:
		return stringReferences(fmt.Sprintf("%v", value), title)
	}
}

func stringReferences(value, title string) bool {
	if strings.EqualFold(strings.TrimSpace(value), title) {
		return true
	}
	rest := value
	for {
		start := strings.Index(rest, "[[")
		if start < 0 {
			return false
		}
		rest = rest[start+2:]
		end := strings.Index(rest, "]]")
		if end < 0 {
			return false
		}
		target := rest[:end]
		if pipe := strings.Index(target, "|"); pipe >= 0 {
			target = target[:pipe]
		}
		if strings.EqualFold(strings.TrimSpace(target), title) {
			return true
		}
		rest = rest[end+2:]
	}
}
