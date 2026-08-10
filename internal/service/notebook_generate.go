package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/ai"
	"github.com/danieljustus/symaira-desktop/internal/notebook"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// builtinArtifactInstructions are the four shipped notebook studio kinds
// (issue #426). Each is a prompt template, not hardcoded generation logic —
// a vault-local template at templates/notebook-<kind>.md always takes
// priority, so a user can add or override a kind without a code change.
var builtinArtifactInstructions = map[string]string{
	"briefing": "Write a concise briefing document. Summarize the key points, " +
		"decisions and open questions across the sources below. Use clear " +
		"Markdown headings and keep it skimmable.",
	"study-guide": "Write a study guide covering the sources below: the key " +
		"concepts and definitions a reader must know, followed by a short " +
		"quiz of 5 questions with answers underneath.",
	"faq": "Write a FAQ covering the sources below: the 8-12 most important " +
		"questions a reader would have, each followed by a concise answer.",
	"timeline": "Write a chronological timeline of the events, dates and " +
		"milestones mentioned in the sources below. If the sources contain no " +
		"dates, say so explicitly instead of inventing one.",
}

// NotebookGenerateResult is the outcome of NotebookGenerate.
type NotebookGenerateResult struct {
	Path    string   `json:"path"`
	Kind    string   `json:"kind"`
	Content string   `json:"content"`
	Sources []string `json:"sources"`
	// CitationWarnings flags any citation-shaped link in the generated
	// content that does not point at one of Sources — advisory only, never
	// blocks the write (VAULT.md's citation contract, issue #408).
	CitationWarnings []ai.CitationWarning `json:"citation_warnings,omitempty"`
	DryRun           bool                 `json:"dry_run"`
}

type artifactFrontmatter struct {
	Title        string                 `yaml:"title"`
	Created      string                 `yaml:"created"`
	Tags         []string               `yaml:"tags"`
	NotebookID   string                 `yaml:"notebook_id"`
	ArtifactKind string                 `yaml:"artifact_kind"`
	Sources      []string               `yaml:"sources"`
	Extras       map[string]interface{} `yaml:",inline"`
}

// NotebookGenerate produces a studio artifact (issue #426: briefing,
// study-guide, faq, timeline, or a user-defined kind backed by
// templates/notebook-<kind>.md) from a notebook's sources and writes it to
// notebooks/<notebook-id>/<kind>.md. dryRun computes and returns the result
// without touching the vault, mirroring Service.Autofill's dry-run contract.
func (s *Service) NotebookGenerate(notebookRef, kind string, dryRun bool) (*NotebookGenerateResult, error) {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return nil, fmt.Errorf("kind is required")
	}

	nb, err := notebook.Resolve(s.VaultRoot, notebookRef)
	if err != nil {
		return nil, err
	}

	instruction, err := s.resolveArtifactInstruction(kind)
	if err != nil {
		return nil, err
	}

	// Reuse the scoped-answering retrieval path (issue #425): with an empty
	// query it returns one excerpt per existing source rather than a
	// keyword-ranked subset — exactly the full, bounded grounding context a
	// generated artifact needs.
	sourceDocs, scopedPaths, err := s.scopedSearchResults(nb, "")
	if err != nil {
		return nil, err
	}
	if len(sourceDocs) == 0 {
		return nil, fmt.Errorf("notebook %q has no existing sources to generate from", nb.ID)
	}

	prompt := buildArtifactPrompt(instruction, sourceDocs)
	content, err := ai.PromptOne(s.Config, prompt)
	if err != nil {
		return nil, fmt.Errorf("generate %s: %w", kind, err)
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("generate %s: model returned an empty response", kind)
	}

	warnings := ai.CheckCitationWarningsSafe(content, scopedPaths)

	relPath := artifactPath(nb.ID, kind)
	result := &NotebookGenerateResult{
		Path:             relPath,
		Kind:             kind,
		Content:          content,
		Sources:          scopedPaths,
		CitationWarnings: warnings,
		DryRun:           dryRun,
	}
	if dryRun {
		return result, nil
	}

	if err := s.writeArtifact(nb, kind, relPath, content, scopedPaths); err != nil {
		return nil, err
	}
	return result, nil
}

// resolveArtifactInstruction prefers a vault-local template
// (templates/notebook-<kind>.md) over the built-in kinds, so a new kind
// never requires a code change.
func (s *Service) resolveArtifactInstruction(kind string) (string, error) {
	templatePath, err := vault.SecurePath(s.VaultRoot, filepath.Join("templates", "notebook-"+kind+".md"))
	if err == nil {
		if data, readErr := os.ReadFile(templatePath); readErr == nil { //nolint:gosec // templatePath was already validated by vault.SecurePath above
			if doc, parseErr := vault.ParseBytes(templatePath, data); parseErr == nil && strings.TrimSpace(doc.Body) != "" {
				return strings.TrimSpace(doc.Body), nil
			}
			if body := strings.TrimSpace(string(data)); body != "" {
				return body, nil
			}
		}
	}
	if instruction, ok := builtinArtifactInstructions[kind]; ok {
		return instruction, nil
	}
	known := make([]string, 0, len(builtinArtifactInstructions))
	for k := range builtinArtifactInstructions {
		known = append(known, k)
	}
	sort.Strings(known)
	return "", fmt.Errorf("unknown artifact kind %q (built-in kinds: %s; or add templates/notebook-%s.md)",
		kind, strings.Join(known, ", "), kind)
}

// buildArtifactPrompt assembles the instruction plus every source excerpt.
// Unlike ai.buildPrompt (Q&A, capped at 5 sources) this includes every
// resolved source — a notebook's corpus is bounded by construction
// (VAULT.md section 10), so the cap that protects an open-ended vault
// search does not apply here.
func buildArtifactPrompt(instruction string, sources []SearchResult) string {
	var b strings.Builder
	b.WriteString(instruction)
	b.WriteString("\n\nAnswer using only the sources below. Cite them as [[path]] under a " +
		"\"## Sources\" heading at the end. If the sources do not contain enough " +
		"information for part of the request, say so explicitly rather than " +
		"inventing content.\n\n")
	for _, doc := range sources {
		snippet := doc.Snippet
		if len(snippet) > 3000 {
			snippet = snippet[:3000]
		}
		fmt.Fprintf(&b, "--- Source [[%s]] (%s) ---\n%s\n\n", doc.Path, doc.Title, snippet)
	}
	return b.String()
}

func artifactPath(notebookID, kind string) string {
	return filepath.Join(notebook.Dir, notebookID, kind+".md")
}

// writeArtifact snapshots any existing artifact at relPath, then writes and
// re-indexes the new content — the same history-then-write-then-index
// sequence every other mutating write in this service follows.
func (s *Service) writeArtifact(nb *notebook.Notebook, kind, relPath, content string, sources []string) error {
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0750); err != nil {
		return fmt.Errorf("create notebook artifact directory: %w", err)
	}

	fm := artifactFrontmatter{
		Title:        nb.Title + " — " + artifactKindTitle(kind),
		Created:      time.Now().UTC().Format(time.RFC3339),
		Tags:         []string{"notebook-artifact", kind},
		NotebookID:   nb.ID,
		ArtifactKind: kind,
		Sources:      sources,
	}
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("encode artifact frontmatter: %w", err)
	}

	var body strings.Builder
	fmt.Fprintf(&body, "# %s\n\n", fm.Title)
	body.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		body.WriteString("\n")
	}

	full := "---\n" + string(fmBytes) + "---\n\n" + body.String()

	s.snapshotBefore(absPath)
	if err := os.WriteFile(absPath, []byte(full), 0600); err != nil {
		return fmt.Errorf("write artifact: %w", err)
	}

	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return fmt.Errorf("wrote artifact but failed to parse for indexing: %w", err)
	}
	hash := sha256.Sum256([]byte(full))
	doc.SHA256 = hex.EncodeToString(hash[:])
	if err := s.IndexDocument(doc); err != nil {
		return fmt.Errorf("wrote artifact but failed to index: %w", err)
	}
	return nil
}

func artifactKindTitle(kind string) string {
	words := strings.FieldsFunc(kind, func(r rune) bool { return r == '-' || r == '_' })
	for i, w := range words {
		if w == "" {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}
