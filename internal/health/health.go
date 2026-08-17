// Package health scans a Markdown vault and produces a reviewable repair plan.
package health

import (
	"fmt"
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

	err := vault.Walk(vaultRoot, func(path string) error {
		rel, relErr := filepath.Rel(vaultRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		report.FilesScanned++
		doc, parseErr := vault.ParseFile(path)
		if parseErr != nil {
			report.addFinding("parse_error", "error", rel, parseErr.Error(), "review", "The file could not be parsed safely")
			return nil
		}
		docs = append(docs, doc)
		paths[rel] = struct{}{}
		paths[strings.TrimSuffix(rel, filepath.Ext(rel))] = struct{}{}
		if doc.Title != "" {
			titles[strings.ToLower(strings.TrimSpace(doc.Title))] = struct{}{}
		}
		if len(doc.Frontmatter) == 0 {
			report.addFinding("missing_frontmatter", "warning", rel, "Markdown file has no frontmatter", "review-frontmatter", "Metadata is unavailable to the index and repair tools")
		}
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
		for _, link := range doc.Links {
			if target := normalizeLinkTarget(link); target != "" && !linkExists(target, paths, titles) {
				report.addFinding("broken_wikilink", "warning", filepath.ToSlash(rel), fmt.Sprintf("wikilink target %q does not resolve to a vault document", target), "review-link", "Choose an existing target or remove the stale link")
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

func linkExists(target string, paths, titles map[string]struct{}) bool {
	if _, ok := paths[target]; ok {
		return true
	}
	if _, ok := paths[strings.TrimSuffix(target, filepath.Ext(target))]; ok {
		return true
	}
	_, ok := titles[strings.ToLower(target)]
	return ok
}
