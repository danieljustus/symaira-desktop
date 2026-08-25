// Package health scans a Markdown vault and produces a reviewable repair plan.
package health

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Finding is a non-destructive vault health observation.
type Finding struct {
	Category string `json:"category"`
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

// RepairAction is an explicit, reviewable action suggestion. Scanning never
// applies these actions; a later command can consume this plan safely.
type RepairAction struct {
	Category string `json:"category"`
	Path     string `json:"path,omitempty"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
}

// Report is the machine-readable result of one vault health scan.
type Report struct {
	Vault        string         `json:"vault"`
	FilesScanned int            `json:"files_scanned"`
	Findings     []Finding      `json:"findings"`
	RepairPlan   []RepairAction `json:"repair_plan"`
	Healthy      bool           `json:"healthy"`
}

// Scan performs a read-only scan. The sidecar is used for near-duplicate
// findings when available; nil db is supported for focused unit tests.
func Scan(vaultRoot string, db *sidecar.DB, duplicateThreshold int) (Report, error) {
	report := Report{Vault: vaultRoot, Findings: []Finding{}, RepairPlan: []RepairAction{}}
	var docs []*vault.Document
	paths := make(map[string]struct{})
	titles := make(map[string]struct{})
	aliases := make(map[string]struct{})
	attachments := make(map[string]struct{})

	err := vault.WalkAll(vaultRoot, func(path string, d fs.DirEntry) error {
		rel, relErr := filepath.Rel(vaultRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)

		if filepath.Ext(d.Name()) == ".md" {
			report.FilesScanned++
			doc, parseErr := vault.ParseFile(path)
			if parseErr != nil {
				report.addFinding("parse_error", "error", rel, parseErr.Error(), "review", "The file could not be parsed safely")
				return nil
			}
			docs = append(docs, doc)
			relLower := strings.ToLower(rel)
			baseLower := strings.ToLower(filepath.Base(rel))
			paths[relLower] = struct{}{}
			paths[strings.TrimSuffix(relLower, ".md")] = struct{}{}
			paths[baseLower] = struct{}{}
			paths[strings.TrimSuffix(baseLower, ".md")] = struct{}{}
			if doc.Title != "" {
				titles[strings.ToLower(strings.TrimSpace(doc.Title))] = struct{}{}
			}
			for _, alias := range doc.Aliases {
				if trimmed := strings.TrimSpace(alias); trimmed != "" {
					aliases[strings.ToLower(trimmed)] = struct{}{}
				}
			}
			if len(doc.Frontmatter) == 0 {
				report.addFinding("missing_frontmatter", "warning", rel, "Markdown file has no frontmatter", "review-frontmatter", "Metadata is unavailable to the index and repair tools")
			}
			return nil
		}

		// Non-Markdown file (attachment, canvas, etc.)
		relLower := strings.ToLower(rel)
		baseLower := strings.ToLower(filepath.Base(rel))
		attachments[relLower] = struct{}{}
		attachments[baseLower] = struct{}{}
		return nil
	})
	if err != nil {
		return report, err
	}

	for _, doc := range docs {
		rel, relErr := filepath.Rel(vaultRoot, doc.Path)
		if relErr != nil {
			continue
		}
		relSlash := filepath.ToSlash(rel)

		// Check derived artifact health
		if doc.IsDerived() && doc.DerivedFrom != "" {
			sourcePath, exists, _ := resolveDerivedSource(vaultRoot, doc.Path, doc.DerivedFrom)
			if !exists {
				report.addFinding("orphaned_derived_artifact", "warning", relSlash,
					fmt.Sprintf("derived artifact %q source %q does not exist", relSlash, doc.DerivedFrom),
					"review-derived", "Source document is missing or has been deleted")
			} else {
				sourceInfo, sErr := os.Stat(sourcePath)
				derivedInfo, dErr := os.Stat(doc.Path)
				if sErr == nil && dErr == nil && sourceInfo.ModTime().After(derivedInfo.ModTime()) {
					report.addFinding("stale_derived_artifact", "warning", relSlash,
						fmt.Sprintf("derived artifact %q is older than its source %q", relSlash, doc.DerivedFrom),
						"regenerate-derived", "Source document was modified after artifact generation")
				}
			}
		}

		for _, link := range doc.Links {
			if target := normalizeLinkTarget(link); target != "" && !linkExists(target, paths, titles, aliases, attachments) {
				ext := strings.ToLower(filepath.Ext(target))
				if ext != "" && ext != ".md" && ext != ".canvas" {
					report.addFinding("broken_wikilink", "warning", filepath.ToSlash(rel), fmt.Sprintf("attachment target %q does not resolve to a vault attachment", target), "review-link", "Choose an existing target or remove the stale link")
				} else {
					report.addFinding("broken_wikilink", "warning", filepath.ToSlash(rel), fmt.Sprintf("wikilink target %q does not resolve to a vault document", target), "review-link", "Choose an existing target or remove the stale link")
				}
			}
		}
	}

	if db != nil {
		groups, dupErr := service.New(vaultRoot, db).SimilarAll(service.ResolveDuplicateThreshold(duplicateThreshold))
		if dupErr != nil {
			return report, dupErr
		}
		for _, group := range groups {
			if len(group.Members) < 2 {
				continue
			}
			for _, member := range group.Members {
				report.addFinding("near_duplicate", "warning", member.Path, fmt.Sprintf("belongs to a near-duplicate group at %d%% similarity", member.Similarity), "review-duplicate", "Review the group before choosing a canonical document")
			}
		}
	}

	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Path != report.Findings[j].Path {
			return report.Findings[i].Path < report.Findings[j].Path
		}
		return report.Findings[i].Category < report.Findings[j].Category
	})
	report.Healthy = len(report.Findings) == 0
	return report, nil
}

func (r *Report) addFinding(category, severity, path, message, action, reason string) {
	r.Findings = append(r.Findings, Finding{Category: category, Severity: severity, Path: path, Message: message})
	r.RepairPlan = append(r.RepairPlan, RepairAction{Category: category, Path: path, Action: action, Reason: reason})
}

func normalizeLinkTarget(link string) string {
	link = strings.TrimSpace(link)
	if pipe := strings.IndexByte(link, '|'); pipe >= 0 {
		link = link[:pipe]
	}
	if anchor := strings.IndexAny(link, "#^"); anchor >= 0 {
		link = link[:anchor]
	}
	link = strings.TrimSpace(link)
	if link == "" || strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
		return ""
	}
	// Bare person/name links are legitimate cross-references; only paths and
	// explicit document extensions are safe to classify as broken documents.
	if !strings.Contains(link, "/") && filepath.Ext(link) == "" {
		return ""
	}
	return filepath.ToSlash(strings.TrimPrefix(filepath.Clean(link), "./"))
}

func linkExists(target string, paths, titles, aliases, attachments map[string]struct{}) bool {
	targetLower := strings.ToLower(target)
	if _, ok := paths[targetLower]; ok {
		return true
	}
	targetNoExtLower := strings.TrimSuffix(targetLower, filepath.Ext(targetLower))
	if _, ok := paths[targetNoExtLower]; ok {
		return true
	}
	if _, ok := titles[targetLower]; ok {
		return true
	}
	if _, ok := attachments[targetLower]; ok {
		return true
	}
	targetBaseLower := strings.ToLower(filepath.Base(target))
	if _, ok := attachments[targetBaseLower]; ok {
		return true
	}
	if _, ok := aliases[targetLower]; ok {
		return true
	}
	if _, ok := aliases[targetNoExtLower]; ok {
		return true
	}
	if _, ok := aliases[targetBaseLower]; ok {
		return true
	}
	targetBaseNoExtLower := strings.TrimSuffix(targetBaseLower, filepath.Ext(targetBaseLower))
	if _, ok := aliases[targetBaseNoExtLower]; ok {
		return true
	}
	return false
}

func resolveDerivedSource(vaultRoot, docPath, derivedFrom string) (string, bool, error) {
	target := strings.TrimSpace(derivedFrom)
	if strings.HasPrefix(target, "[[") && strings.HasSuffix(target, "]]") {
		target = strings.TrimPrefix(strings.TrimSuffix(target, "]]"), "[[")
		if pipe := strings.IndexByte(target, '|'); pipe >= 0 {
			target = target[:pipe]
		}
		if anchor := strings.IndexAny(target, "#^"); anchor >= 0 {
			target = target[:anchor]
		}
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return "", false, nil
	}

	candidates := []string{
		filepath.Join(vaultRoot, target),
		filepath.Join(filepath.Dir(docPath), target),
	}
	if filepath.Ext(target) == "" {
		candidates = append(candidates,
			filepath.Join(vaultRoot, target+".md"),
			filepath.Join(filepath.Dir(docPath), target+".md"),
		)
	}

	for _, cand := range candidates {
		rel, err := filepath.Rel(vaultRoot, cand)
		if err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		resolved, err := vault.SecurePath(vaultRoot, rel)
		if err == nil {
			if info, err := os.Stat(resolved); err == nil && !info.IsDir() {
				return resolved, true, nil
			}
		}
	}

	var foundPath string
	_ = vault.Walk(vaultRoot, func(p string) error {
		if foundPath != "" {
			return nil
		}
		base := filepath.Base(p)
		nameWithoutExt := strings.TrimSuffix(base, filepath.Ext(base))
		if strings.EqualFold(base, target) || strings.EqualFold(nameWithoutExt, target) || strings.EqualFold(p, target) {
			foundPath = p
		}
		return nil
	})
	if foundPath != "" {
		return foundPath, true, nil
	}

	return "", false, nil
}
