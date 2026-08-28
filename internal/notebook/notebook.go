// Package notebook implements the notebook primitive defined by VAULT.md
// section 10 (contract_version 4): a named, bounded set of vault sources,
// stored as an ordinary Markdown note under notebooks/<slug>.md. There is
// no separate database — the frontmatter `sources` list is the single
// source of truth, and AI features scope their retrieval to it.
package notebook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Dir is the vault-relative folder notebook notes are stored under.
const Dir = "notebooks"

// ErrNotFound is returned when a notebook cannot be resolved by ID or path.
var ErrNotFound = errors.New("notebook not found")

// ErrTitleRequired is returned by New when title is empty.
var ErrTitleRequired = errors.New("title is required")

// ErrSourceIsSelf is returned when a notebook's own note is added as a source.
var ErrSourceIsSelf = errors.New("a notebook cannot be a source of itself")

// Notebook is a bounded, named set of vault sources (VAULT.md section 10).
type Notebook struct {
	// ID is the stable identifier assigned at creation (VAULT.md: "never
	// changed by a rename"). It doubles as the file's slug.
	ID          string `json:"id"`
	Path        string `json:"path"` // vault-relative, e.g. notebooks/research-x.md
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Created     string `json:"created"`
	// Sources are vault-relative paths, sorted and deduplicated.
	Sources []string `json:"sources"`
	// Query is set for notebooks promoted from a search result set. It is
	// optional so ordinary hand-created notebooks keep the existing shape.
	Query string `json:"query,omitempty"`
}

// SourceRef is one resolved source: the reference path plus what could be
// determined about the referenced file. Missing is true when the source
// path no longer resolves to a file in the vault (moved or deleted
// independently of the notebook) — resolution never fails outright on a
// dangling reference, since callers need to keep working with the rest.
type SourceRef struct {
	Path    string `json:"path"`
	Title   string `json:"title,omitempty"`
	Missing bool   `json:"missing,omitempty"`
}

// frontmatter is the on-disk YAML shape of a notebook note (VAULT.md
// section 10). Extras preserves unknown top-level fields written by a
// newer contract version across edits, matching the meeting note contract.
type frontmatter struct {
	Type        string                 `yaml:"type"`
	Title       string                 `yaml:"title"`
	Created     string                 `yaml:"created"`
	Tags        []string               `yaml:"tags"`
	NotebookID  string                 `yaml:"notebook_id"`
	Description string                 `yaml:"description,omitempty"`
	Sources     []string               `yaml:"sources"`
	Query       string                 `yaml:"query,omitempty"`
	Extras      map[string]interface{} `yaml:",inline"`
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify derives a filesystem-safe slug from a notebook title: lowercased,
// non-alphanumeric runs collapsed to a single hyphen, leading/trailing
// hyphens trimmed. An empty result (e.g. a title of only punctuation) falls
// back to "notebook" so New always has a usable base to de-duplicate from.
func Slugify(title string) string {
	s := slugUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "notebook"
	}
	return s
}

// notePath returns the vault-relative path for a notebook slug.
func notePath(slug string) string {
	return filepath.Join(Dir, slug+".md")
}

// canonicalRoot resolves symlinks in vaultRoot so it matches the
// canonicalized paths vault.SecurePath returns (e.g. macOS /var ->
// /private/var). Without this, filepath.Rel(vaultRoot, absPath) computes
// nonsense once SecurePath's returned path and vaultRoot disagree on which
// form of the path they use. Falls back to the original value if symlink
// resolution fails (matches Service.New's fallback), so a not-yet-existing
// root during tests never turns into a hard error here.
func canonicalRoot(vaultRoot string) string {
	if resolved, err := filepath.EvalSymlinks(vaultRoot); err == nil {
		return resolved
	}
	return vaultRoot
}

// New creates a new notebook note at notebooks/<slug>.md and returns it.
// The slug is derived from title and de-duplicated against existing
// notebooks by appending "-2", "-3", ... The caller is responsible for
// re-indexing the written file (see Service.NotebookNew).
// NewWithQuery creates a notebook whose Markdown frontmatter remembers the
// search query that produced it. The query is metadata only; Sources remains
// the bounded grounding set and the Markdown note remains the authority.
func NewWithQuery(vaultRoot, title, description, query string) (*Notebook, error) {
	nb, err := New(vaultRoot, title, description)
	if err != nil {
		return nil, err
	}
	nb.Query = strings.TrimSpace(query)
	if err := write(canonicalRoot(vaultRoot), nb); err != nil {
		return nil, err
	}
	return nb, nil
}

func New(vaultRoot, title, description string) (*Notebook, error) {
	vaultRoot = canonicalRoot(vaultRoot)
	title = strings.TrimSpace(title)
	if title == "" {
		return nil, ErrTitleRequired
	}

	base := Slugify(title)
	slug := base
	for i := 2; ; i++ {
		absPath, err := vault.SecurePath(vaultRoot, notePath(slug))
		if err != nil {
			return nil, err
		}
		if _, statErr := os.Stat(absPath); os.IsNotExist(statErr) {
			break
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}

	nb := &Notebook{
		ID:          slug,
		Path:        notePath(slug),
		Title:       title,
		Description: description,
		Created:     time.Now().UTC().Format(time.RFC3339),
		// Never nil: Go's encoding/json marshals a nil slice as JSON null,
		// which native Decodable clients (the macOS app) cannot decode into
		// a non-optional array field. An empty notebook is `sources: []`,
		// not `sources: null`.
		Sources: []string{},
	}
	if err := write(vaultRoot, nb); err != nil {
		return nil, err
	}
	return nb, nil
}

// Load reads and parses the notebook note at the given vault-relative path.
func Load(vaultRoot, relPath string) (*Notebook, error) {
	absPath, err := vault.SecurePath(vaultRoot, relPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absPath) //nolint:gosec // absPath was already validated by vault.SecurePath above
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return parse(relPath, data)
}

// Resolve looks up a notebook by ID/slug ("research-x"), by vault-relative
// path with or without extension ("notebooks/research-x", "notebooks/
// research-x.md"), so CLI, MCP and HTTP callers can all pass the natural
// form for their surface.
func Resolve(vaultRoot, ref string) (*Notebook, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("notebook reference is required")
	}

	rel := ref
	if !strings.HasSuffix(rel, ".md") {
		rel += ".md"
	}
	if !strings.HasPrefix(filepath.ToSlash(rel), Dir+"/") {
		rel = notePath(strings.TrimSuffix(filepath.Base(rel), ".md"))
	}

	return Load(vaultRoot, rel)
}

// List returns every notebook note in the vault, sorted by title. Files
// under notebooks/ that are not classified as type: notebook are skipped
// rather than erroring, since notebooks/ is a plain vault folder and any
// file a user drops there must not break listing.
func List(vaultRoot string) ([]*Notebook, error) {
	dirAbs, err := vault.SecurePath(vaultRoot, Dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Notebook{}, nil
		}
		return nil, err
	}

	// Never nil, for the same JSON-null-vs-empty-array reason as Sources
	// above: notebooks/ existing with zero valid notebooks in it is a
	// legitimate empty result, not an absent one.
	out := []*Notebook{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		rel := filepath.Join(Dir, e.Name())
		nb, err := Load(vaultRoot, rel)
		if err != nil {
			continue
		}
		out = append(out, nb)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return out, nil
}

// AddSource validates sourcePath, appends it to nb.Sources (idempotent —
// an already-present source is a no-op, not an error) and persists the
// note. The caller is responsible for re-indexing (see
// Service.NotebookAddSource).
func AddSource(vaultRoot string, nb *Notebook, sourcePath string) error {
	vaultRoot = canonicalRoot(vaultRoot)
	rel, err := validateSource(vaultRoot, nb, sourcePath)
	if err != nil {
		return err
	}
	for _, s := range nb.Sources {
		if s == rel {
			return nil
		}
	}
	nb.Sources = append(nb.Sources, rel)
	sort.Strings(nb.Sources)
	return write(vaultRoot, nb)
}

// RemoveSource removes sourcePath from nb.Sources if present and persists
// the note. It never deletes the referenced file (VAULT.md section 10).
// Removing a source that is not present is a no-op, not an error.
func RemoveSource(vaultRoot string, nb *Notebook, sourcePath string) error {
	vaultRoot = canonicalRoot(vaultRoot)
	absPath, err := vault.SecurePath(vaultRoot, sourcePath)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(vaultRoot, absPath)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)

	kept := nb.Sources[:0:0]
	for _, s := range nb.Sources {
		if s != rel {
			kept = append(kept, s)
		}
	}
	if len(kept) == len(nb.Sources) {
		return nil
	}
	nb.Sources = kept
	return write(vaultRoot, nb)
}

// validateSource resolves sourcePath against the vault, rejecting anything
// outside it or referring to the notebook's own note, and returns the
// canonical vault-relative form. It does not require the source to exist
// on disk beyond what vault.SecurePath already enforces, so a source can be
// added moments before the referenced file lands (e.g. from an in-flight
// ingest job) without an ordering dependency.
func validateSource(vaultRoot string, nb *Notebook, sourcePath string) (string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", fmt.Errorf("source path is required")
	}
	absPath, err := vault.SecurePath(vaultRoot, sourcePath)
	if err != nil {
		return "", fmt.Errorf("invalid source path %q: %w", sourcePath, err)
	}
	rel, err := filepath.Rel(vaultRoot, absPath)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel == nb.Path {
		return "", ErrSourceIsSelf
	}
	return rel, nil
}

// ResolveSources resolves every source path to its current title. A source
// whose file is gone is reported with Missing=true rather than failing the
// whole call, so a stale reference doesn't block reading the rest.
func (nb *Notebook) ResolveSources(vaultRoot string) ([]SourceRef, error) {
	refs := make([]SourceRef, 0, len(nb.Sources))
	for _, src := range nb.Sources {
		absPath, err := vault.SecurePath(vaultRoot, src)
		if err != nil {
			refs = append(refs, SourceRef{Path: src, Missing: true})
			continue
		}
		doc, err := vault.ParseFile(absPath)
		if err != nil {
			refs = append(refs, SourceRef{Path: src, Missing: true})
			continue
		}
		refs = append(refs, SourceRef{Path: src, Title: doc.Title})
	}
	return refs, nil
}

// write renders nb to Markdown and persists it at nb.Path.
func write(vaultRoot string, nb *Notebook) error {
	absPath, err := vault.SecurePath(vaultRoot, nb.Path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0750); err != nil {
		return fmt.Errorf("create notebooks directory: %w", err)
	}
	content, err := render(nb)
	if err != nil {
		return err
	}
	if err := os.WriteFile(absPath, content, 0600); err != nil {
		return fmt.Errorf("write notebook note: %w", err)
	}
	return nil
}

// render produces the full Markdown content (frontmatter + body) for nb.
// The `## Sources` heading is a generated view of the sources frontmatter
// field (VAULT.md section 10: "hand edits to that section are not read
// back and are overwritten on the next write").
func render(nb *Notebook) ([]byte, error) {
	fm := frontmatter{
		Type:        "notebook",
		Title:       nb.Title,
		Created:     nb.Created,
		Tags:        []string{"notebook"},
		NotebookID:  nb.ID,
		Description: nb.Description,
		Sources:     nb.Sources,
		Query:       nb.Query,
	}
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("encode notebook frontmatter: %w", err)
	}

	var body strings.Builder
	body.WriteString("# " + nb.Title + "\n\n")
	if nb.Description != "" {
		body.WriteString(nb.Description + "\n\n")
	}
	body.WriteString("## Sources\n\n")
	if len(nb.Sources) == 0 {
		body.WriteString("_No sources yet._\n")
	}
	for _, src := range nb.Sources {
		name := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
		fmt.Fprintf(&body, "- [[%s]] (`%s`)\n", name, src)
	}

	full := "---\n" + string(fmBytes) + "---\n\n" + body.String()
	return []byte(full), nil
}

// parse reads a notebook note's frontmatter into a Notebook. relPath is
// the vault-relative path the note was loaded from (used for ID fallback
// when notebook_id is absent from an older/hand-edited note).
func parse(relPath string, data []byte) (*Notebook, error) {
	doc, err := vault.ParseBytes(relPath, data)
	if err != nil {
		return nil, err
	}
	if t, _ := doc.Frontmatter["type"].(string); t != "notebook" {
		return nil, fmt.Errorf("%s is not a notebook note (type=%q)", relPath, t)
	}

	id, _ := doc.Frontmatter["notebook_id"].(string)
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(relPath), ".md")
	}
	description, _ := doc.Frontmatter["description"].(string)
	query, _ := doc.Frontmatter["query"].(string)

	// Never nil (see the same comment in New): an empty notebook must
	// serialize as `sources: []`, not `sources: null`.
	sources := []string{}
	if raw, ok := doc.Frontmatter["sources"]; ok {
		switch v := raw.(type) {
		case []interface{}:
			for _, item := range v {
				if s, ok := item.(string); ok && s != "" {
					sources = append(sources, s)
				}
			}
		case []string:
			sources = v
		}
	}
	sort.Strings(sources)

	return &Notebook{
		ID:          id,
		Path:        relPath,
		Title:       doc.Title,
		Description: description,
		Created:     doc.Created,
		Query:       query,
		Sources:     sources,
	}, nil
}
