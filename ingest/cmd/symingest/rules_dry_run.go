package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/store"
	"gopkg.in/yaml.v3"
)

type rulesDryRunMatch struct {
	DocumentID     int64   `json:"document_id"`
	NotePath       string  `json:"note_path"`
	Title          string  `json:"title"`
	MatchedRuleIDs []int64 `json:"matched_rule_ids"`
}

type rulesDryRunSkipped struct {
	DocumentID int64  `json:"document_id"`
	NotePath   string `json:"note_path"`
	Reason     string `json:"reason"`
}

type rulesDryRunJSONResponse struct {
	SchemaVersion    int                  `json:"schema_version"`
	Operation        string               `json:"operation"`
	ProposedRule     dryRunRule           `json:"proposed_rule"`
	VaultPath        string               `json:"vault_path"`
	TotalDocuments   int                  `json:"total_documents"`
	MatchedDocuments int                  `json:"matched_documents"`
	SkippedDocuments int                  `json:"skipped_documents"`
	Matches          []rulesDryRunMatch   `json:"matches"`
	Skipped          []rulesDryRunSkipped `json:"skipped"`
}

type dryRunRule struct {
	Pattern string `json:"pattern"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
}

func dryRunRuleAgainstDocuments(ctx context.Context, st *store.Store, vault, pattern, kind, value string) (*rulesDryRunJSONResponse, error) {
	if err := store.ValidateClassificationRule(pattern, kind, value); err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitData, exitcodes.KindValidation, "invalid proposed classification rule")
	}
	vault, err := filepath.Abs(vault)
	if err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "resolve vault path")
	}
	info, err := os.Stat(vault)
	if err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitConfig, exitcodes.KindConfig, "stat vault")
	}
	if !info.IsDir() {
		return nil, exitcodes.Wrapf(nil, exitcodes.ExitConfig, exitcodes.KindConfig, "vault path is not a directory: %s", vault)
	}

	documents, err := st.ListDocuments(ctx)
	if err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to list ingested documents")
	}
	rules, err := st.ListRules(ctx)
	if err != nil {
		return nil, exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, "failed to list classification rules")
	}

	response := &rulesDryRunJSONResponse{
		SchemaVersion:  rulesJSONSchemaVersion,
		Operation:      "dry_run",
		ProposedRule:   dryRunRule{Pattern: pattern, Kind: kind, Value: value},
		VaultPath:      vault,
		TotalDocuments: len(documents),
		Matches:        []rulesDryRunMatch{},
		Skipped:        []rulesDryRunSkipped{},
	}
	needle := strings.ToLower(pattern)
	for _, document := range documents {
		if document.VaultPath == nil || *document.VaultPath == "" {
			continue
		}
		notePath, reason := safeNotePath(vault, *document.VaultPath)
		if reason != "" {
			response.Skipped = append(response.Skipped, rulesDryRunSkipped{DocumentID: document.ID, NotePath: *document.VaultPath, Reason: reason})
			continue
		}
		data, err := os.ReadFile(notePath)
		if err != nil {
			response.Skipped = append(response.Skipped, rulesDryRunSkipped{DocumentID: document.ID, NotePath: notePath, Reason: "cannot read note"})
			continue
		}
		body := noteBody(string(data))
		if !strings.Contains(strings.ToLower(body), needle) {
			continue
		}
		matchedRuleIDs := make([]int64, 0)
		for _, rule := range rules {
			if strings.Contains(strings.ToLower(body), strings.ToLower(rule.Pattern)) {
				matchedRuleIDs = append(matchedRuleIDs, rule.ID)
			}
		}
		response.Matches = append(response.Matches, rulesDryRunMatch{
			DocumentID:     document.ID,
			NotePath:       notePath,
			Title:          noteTitle(string(data), notePath),
			MatchedRuleIDs: matchedRuleIDs,
		})
	}
	response.MatchedDocuments = len(response.Matches)
	response.SkippedDocuments = len(response.Skipped)
	sort.Slice(response.Matches, func(i, j int) bool { return response.Matches[i].NotePath < response.Matches[j].NotePath })
	sort.Slice(response.Skipped, func(i, j int) bool { return response.Skipped[i].NotePath < response.Skipped[j].NotePath })
	return response, nil
}

func safeNotePath(vault, configuredPath string) (string, string) {
	notePath := configuredPath
	if !filepath.IsAbs(notePath) {
		notePath = filepath.Join(vault, notePath)
	}
	notePath, err := filepath.Abs(notePath)
	if err != nil {
		return configuredPath, "cannot resolve note path"
	}
	rel, err := filepath.Rel(vault, notePath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return notePath, "note path is outside the configured vault"
	}
	return notePath, ""
}

func noteBody(content string) string {
	if !strings.HasPrefix(content, "---\n") {
		return content
	}
	end := strings.Index(content[4:], "\n---\n")
	if end < 0 {
		return content
	}
	return content[end+10:]
}

func noteTitle(content, notePath string) string {
	if strings.HasPrefix(content, "---\n") {
		if end := strings.Index(content[4:], "\n---\n"); end >= 0 {
			var frontmatter map[string]interface{}
			if err := yaml.Unmarshal([]byte(content[4:end+4]), &frontmatter); err == nil {
				if title, ok := frontmatter["title"].(string); ok && strings.TrimSpace(title) != "" {
					return title
				}
				if paperless, ok := frontmatter["paperless"].(map[string]interface{}); ok {
					if title, ok := paperless["title"].(string); ok && strings.TrimSpace(title) != "" {
						return title
					}
				}
			}
			content = noteBody(content)
		}
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		}
	}
	base := filepath.Base(notePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func formatDryRunHuman(response *rulesDryRunJSONResponse) {
	fmt.Fprintf(stdout, "Dry-run: %d/%d documents match pattern %q\n", response.MatchedDocuments, response.TotalDocuments, response.ProposedRule.Pattern)
	for _, match := range response.Matches {
		fmt.Fprintf(stdout, "match: %s (%s)\n", match.NotePath, match.Title)
	}
	if response.SkippedDocuments > 0 {
		fmt.Fprintf(stdout, "Skipped documents: %d\n", response.SkippedDocuments)
	}
}
