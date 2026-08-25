package dbviews

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// Dir is the vault-relative folder where base notes are stored.
const Dir = "bases"

var (
	// ErrNotFound is returned when a view cannot be found.
	ErrNotFound = errors.New("view not found")

	// ErrBaseNotFound is returned when a base note cannot be found.
	ErrBaseNotFound = errors.New("base not found")

	slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)
)

type Filter struct {
	Key      string `json:"key" yaml:"key"`
	Operator string `json:"operator,omitempty" yaml:"operator,omitempty"`
	Value    string `json:"value" yaml:"value"`
}

// FilterGroup allows views to express nested all/any conditions. Filters is
// deliberately kept on View for compatibility with views written before
// groups were introduced.
type FilterGroup struct {
	Operator string        `json:"operator" yaml:"operator"`
	Filters  []Filter      `json:"filters,omitempty" yaml:"filters,omitempty"`
	Groups   []FilterGroup `json:"groups,omitempty" yaml:"groups,omitempty"`
}

// Template describes the note template and frontmatter values used when a
// user creates an entry from a saved view.
type Template struct {
	Ref      string            `json:"ref,omitempty" yaml:"ref,omitempty"`
	Defaults map[string]string `json:"defaults,omitempty" yaml:"defaults,omitempty"`
}

type Sort struct {
	Key       string `json:"key" yaml:"key"`
	Ascending bool   `json:"ascending" yaml:"ascending"`
}

type ComputedColumn struct {
	Formula string `json:"formula,omitempty" yaml:"formula,omitempty"`
	Rollup  string `json:"rollup,omitempty" yaml:"rollup,omitempty"`
}

type View struct {
	ID           string                    `json:"id" yaml:"id"`
	Name         string                    `json:"name" yaml:"name"`
	Type         string                    `json:"type,omitempty" yaml:"type,omitempty"`
	GroupBy      string                    `json:"group_by,omitempty" yaml:"group_by,omitempty"`
	DateProperty string                    `json:"date_property,omitempty" yaml:"date_property,omitempty"`
	Computed     map[string]ComputedColumn `json:"computed,omitempty" yaml:"computed,omitempty"`
	Filters      []Filter                  `json:"filters" yaml:"filters,omitempty"`
	FilterGroup  *FilterGroup              `json:"filter_group,omitempty" yaml:"filter_group,omitempty"`
	Sorts        []Sort                    `json:"sorts" yaml:"sorts,omitempty"`
	Columns      []string                  `json:"columns" yaml:"columns,omitempty"`
	// Source defines the scope of documents evaluated by the view:
	// - empty (""): query the entire vault
	// - folder prefix (e.g. "invoices/"): only documents under that directory
	// - tag (e.g. "tag:invoice"): only documents carrying that tag
	// - notebook (e.g. "notebook:<id>"): only documents in the notebook's sources
	Source   string    `json:"source,omitempty" yaml:"source,omitempty"`
	Template *Template `json:"template,omitempty" yaml:"template,omitempty"`
}

// PropertyConfig defines the declared schema and metadata for a note property in a base.
type PropertyConfig struct {
	Type        string   `json:"type,omitempty" yaml:"type,omitempty"`               // text, number, date, select, multiselect, status, tags, checkbox/bool, relation
	Label       string   `json:"label,omitempty" yaml:"label,omitempty"`             // Human-readable display label
	Options     []string `json:"options,omitempty" yaml:"options,omitempty"`         // Ordered allowed options for select/multiselect/status
	Description string   `json:"description,omitempty" yaml:"description,omitempty"` // Description of the property
	Default     string   `json:"default,omitempty" yaml:"default,omitempty"`         // Default value
}

// Base is a named collection of saved views stored as a readable Markdown note in bases/<slug>.md.
type Base struct {
	ID          string                    `json:"id"`
	Path        string                    `json:"path"` // vault-relative, e.g. bases/invoices.md
	Title       string                    `json:"title"`
	Description string                    `json:"description,omitempty"`
	Created     string                    `json:"created"`
	Tags        []string                  `json:"tags,omitempty"`
	Properties  map[string]PropertyConfig `json:"properties,omitempty"`
	Views       []View                    `json:"views"`
	Extras      map[string]interface{}    `json:"-"`
}

type baseFrontmatter struct {
	Type        string                    `yaml:"type"`
	Title       string                    `yaml:"title"`
	Created     string                    `yaml:"created"`
	Tags        []string                  `yaml:"tags,omitempty"`
	BaseID      string                    `yaml:"base_id"`
	Description string                    `yaml:"description,omitempty"`
	Properties  map[string]PropertyConfig `yaml:"properties,omitempty"`
	Views       []View                    `yaml:"views"`
	Extras      map[string]interface{}    `yaml:",inline"`
}

// Slugify derives a filesystem-safe slug from a title: lowercased,
// non-alphanumeric runs collapsed to a single hyphen, leading/trailing
// hyphens trimmed.
func Slugify(title string) string {
	s := slugUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(title)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "base"
	}
	return s
}

func canonicalRoot(vaultRoot string) string {
	if resolved, err := filepath.EvalSymlinks(vaultRoot); err == nil {
		return resolved
	}
	return vaultRoot
}

// RenderBase renders a Base struct into standard Markdown format with YAML frontmatter
// and a human-readable ## Views section.
func RenderBase(b *Base) ([]byte, error) {
	tags := b.Tags
	if len(tags) == 0 {
		tags = []string{"base"}
	}
	views := b.Views
	if views == nil {
		views = []View{}
	}
	cleanViews := make([]View, len(views))
	for i, v := range views {
		if v.Filters == nil {
			v.Filters = []Filter{}
		}
		if v.Sorts == nil {
			v.Sorts = []Sort{}
		}
		if v.Columns == nil {
			v.Columns = []string{}
		}
		cleanViews[i] = v
	}

	fm := baseFrontmatter{
		Type:        "base",
		Title:       b.Title,
		Created:     b.Created,
		Tags:        tags,
		BaseID:      b.ID,
		Description: b.Description,
		Properties:  b.Properties,
		Views:       cleanViews,
		Extras:      b.Extras,
	}
	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return nil, fmt.Errorf("encode base frontmatter: %w", err)
	}

	var body strings.Builder
	body.WriteString("# " + b.Title + "\n\n")
	if b.Description != "" {
		body.WriteString(b.Description + "\n\n")
	}
	body.WriteString("## Views\n\n")
	if len(cleanViews) == 0 {
		body.WriteString("_No views yet._\n")
	} else {
		for _, v := range cleanViews {
			vType := v.Type
			if vType == "" {
				vType = "table"
			}
			line := fmt.Sprintf("- **%s** (`%s`)", v.Name, vType)
			var details []string
			if v.Source != "" {
				if strings.HasPrefix(v.Source, "notebook:") {
					nbID := strings.TrimPrefix(v.Source, "notebook:")
					details = append(details, fmt.Sprintf("Source: [[%s]]", nbID))
				} else if strings.HasPrefix(v.Source, "tag:") {
					details = append(details, fmt.Sprintf("Source: %s", v.Source))
				} else {
					details = append(details, fmt.Sprintf("Source: `%s`", v.Source))
				}
			}
			if len(v.Filters) > 0 {
				filterSummaries := make([]string, 0, len(v.Filters))
				for _, f := range v.Filters {
					op := f.Operator
					if op == "" || op == "equals" {
						filterSummaries = append(filterSummaries, fmt.Sprintf("%s = %s", f.Key, f.Value))
					} else {
						filterSummaries = append(filterSummaries, fmt.Sprintf("%s %s %s", f.Key, op, f.Value))
					}
				}
				details = append(details, fmt.Sprintf("Filters: %s", strings.Join(filterSummaries, ", ")))
			}
			if len(details) > 0 {
				line += " · " + strings.Join(details, " · ")
			}
			body.WriteString(line + "\n")
		}
	}

	full := "---\n" + string(fmBytes) + "---\n\n" + body.String()
	return []byte(full), nil
}

// ParseBase parses a base note's frontmatter and body into a Base struct.
func ParseBase(relPath string, data []byte) (*Base, error) {
	doc, err := vault.ParseBytes(relPath, data)
	if err != nil {
		return nil, err
	}
	t, _ := doc.Frontmatter["type"].(string)
	baseID, _ := doc.Frontmatter["base_id"].(string)
	if t != "base" && baseID == "" {
		return nil, fmt.Errorf("%s is not a base note (type=%q)", relPath, t)
	}
	if baseID == "" {
		baseID = strings.TrimSuffix(filepath.Base(relPath), ".md")
	}

	var fm baseFrontmatter
	fmBytes := extractFrontmatterBytes(data)
	if len(fmBytes) > 0 {
		if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
			return nil, fmt.Errorf("parse base frontmatter: %w", err)
		}
	}

	title := doc.Title
	if fm.Title != "" {
		title = fm.Title
	}
	created := doc.Created
	if fm.Created != "" {
		created = fm.Created
	}
	description := fm.Description
	if description == "" {
		if d, ok := doc.Frontmatter["description"].(string); ok {
			description = d
		}
	}

	views := fm.Views
	if views == nil {
		views = []View{}
	}
	for i := range views {
		if views[i].Filters == nil {
			views[i].Filters = []Filter{}
		}
		if views[i].Sorts == nil {
			views[i].Sorts = []Sort{}
		}
		if views[i].Columns == nil {
			views[i].Columns = []string{}
		}
	}

	props := fm.Properties
	if props == nil {
		props = make(map[string]PropertyConfig)
	}

	return &Base{
		ID:          baseID,
		Path:        relPath,
		Title:       title,
		Description: description,
		Created:     created,
		Tags:        doc.Tags,
		Properties:  props,
		Views:       views,
		Extras:      fm.Extras,
	}, nil
}

func extractFrontmatterBytes(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	if len(lines) == 0 {
		return nil
	}
	start := -1
	end := -1
	for i, l := range lines {
		trimmed := bytes.TrimRight(l, "\r")
		if bytes.Equal(trimmed, []byte("---")) {
			if start == -1 {
				start = i
			} else {
				end = i
				break
			}
		}
	}
	if start == -1 || end == -1 || start >= end {
		return nil
	}
	return bytes.Join(lines[start+1:end], []byte("\n"))
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".symdesk-base-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// Manager coordinates storage and resolution of bases and saved views.
type Manager struct {
	vaultRoot  string
	mu         sync.Mutex
	snapshotFn func(absPath string)
}

func NewManager(vaultRoot string) *Manager {
	m := &Manager{
		vaultRoot: canonicalRoot(vaultRoot),
	}
	m.migrateLegacyViews()
	return m
}

func (m *Manager) SetSnapshotFn(fn func(absPath string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshotFn = fn
}

func (m *Manager) migrateLegacyViews() {
	legacyPath := filepath.Join(m.vaultRoot, ".symdesk", "views.json")
	// #nosec G304 -- legacyPath is derived from the trusted vault root and fixed filename.
	data, err := os.ReadFile(legacyPath)
	if err != nil {
		return
	}
	var legacyViews []View
	if err := json.Unmarshal(data, &legacyViews); err != nil || len(legacyViews) == 0 {
		return
	}

	existingBases, _ := m.listBasesLocked()
	existingViewIDs := make(map[string]bool)
	for _, b := range existingBases {
		for _, v := range b.Views {
			existingViewIDs[v.ID] = true
		}
	}

	var toMigrate []View
	for _, v := range legacyViews {
		if !existingViewIDs[v.ID] {
			toMigrate = append(toMigrate, v)
		}
	}
	if len(toMigrate) == 0 {
		return
	}

	groups := make(map[string][]View)
	groupTitles := make(map[string]string)
	for _, v := range toMigrate {
		slug := ""
		title := ""
		if v.Source != "" {
			slug = Slugify(v.Source)
			title = strings.TrimSuffix(v.Source, "/")
			title = strings.TrimPrefix(title, "tag:")
			title = strings.TrimPrefix(title, "notebook:")
			title = cases.Title(language.English).String(strings.ReplaceAll(title, "-", " "))
		} else {
			slug = Slugify(v.Name)
			title = v.Name
		}
		if slug == "" {
			slug = "views"
		}
		if title == "" {
			title = "Saved Views"
		}
		groups[slug] = append(groups[slug], v)
		groupTitles[slug] = title
	}

	for slug, views := range groups {
		base := &Base{
			ID:      slug,
			Path:    filepath.Join(Dir, slug+".md"),
			Title:   groupTitles[slug],
			Created: time.Now().UTC().Format(time.RFC3339),
			Tags:    []string{"base"},
			Views:   views,
		}
		_ = m.saveBaseLocked(base)
	}
}

func (m *Manager) ListBases() ([]*Base, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listBasesLocked()
}

func (m *Manager) listBasesLocked() ([]*Base, error) {
	dirAbs, err := vault.SecurePath(m.vaultRoot, Dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dirAbs)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Base{}, nil
		}
		return nil, err
	}

	bases := []*Base{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		rel := filepath.Join(Dir, e.Name())
		absPath, err := vault.SecurePath(m.vaultRoot, rel)
		if err != nil {
			continue
		}
		// #nosec G304 -- absPath has been confined with vault.SecurePath.
		data, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}
		b, err := ParseBase(rel, data)
		if err != nil {
			continue
		}
		bases = append(bases, b)
	}

	sort.Slice(bases, func(i, j int) bool {
		return strings.ToLower(bases[i].Title) < strings.ToLower(bases[j].Title)
	})
	return bases, nil
}

func (m *Manager) GetBase(ref string) (*Base, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("base reference is required")
	}

	rel := ref
	if !strings.HasSuffix(rel, ".md") {
		rel += ".md"
	}
	if !strings.HasPrefix(filepath.ToSlash(rel), Dir+"/") {
		rel = filepath.Join(Dir, filepath.Base(rel))
	}

	absPath, err := vault.SecurePath(m.vaultRoot, rel)
	if err == nil {
		// #nosec G304 -- absPath has been confined with vault.SecurePath.
		if data, err := os.ReadFile(absPath); err == nil {
			return ParseBase(rel, data)
		}
	}

	bases, err := m.listBasesLocked()
	if err != nil {
		return nil, err
	}
	for _, b := range bases {
		if b.ID == ref || strings.EqualFold(b.Title, ref) {
			return b, nil
		}
	}

	return nil, ErrBaseNotFound
}

func (m *Manager) SaveBase(b *Base) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveBaseLocked(b)
}

func (m *Manager) saveBaseLocked(b *Base) error {
	if b.ID == "" {
		b.ID = Slugify(b.Title)
	}
	if b.Path == "" {
		b.Path = filepath.Join(Dir, b.ID+".md")
	}
	if b.Created == "" {
		b.Created = time.Now().UTC().Format(time.RFC3339)
	}
	if len(b.Tags) == 0 {
		b.Tags = []string{"base"}
	}

	absPath, err := vault.SecurePath(m.vaultRoot, b.Path)
	if err != nil {
		return err
	}

	if m.snapshotFn != nil {
		m.snapshotFn(absPath)
	}

	if err := os.MkdirAll(filepath.Dir(absPath), 0750); err != nil {
		return fmt.Errorf("create bases directory: %w", err)
	}

	content, err := RenderBase(b)
	if err != nil {
		return err
	}

	return writeFileAtomic(absPath, content)
}

func (m *Manager) DeleteBase(ref string) error {
	b, err := m.GetBase(ref)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	absPath, err := vault.SecurePath(m.vaultRoot, b.Path)
	if err != nil {
		return err
	}
	if m.snapshotFn != nil {
		m.snapshotFn(absPath)
	}
	return os.Remove(absPath)
}

func (m *Manager) List() ([]View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	bases, err := m.listBasesLocked()
	if err != nil {
		return nil, err
	}

	views := make([]View, 0)
	for _, b := range bases {
		for _, v := range b.Views {
			if v.Filters == nil {
				v.Filters = []Filter{}
			}
			if v.Sorts == nil {
				v.Sorts = []Sort{}
			}
			if v.Columns == nil {
				v.Columns = []string{}
			}
			views = append(views, v)
		}
	}
	return views, nil
}

func (m *Manager) Get(id string) (*View, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	bases, err := m.listBasesLocked()
	if err != nil {
		return nil, err
	}

	for _, b := range bases {
		for _, v := range b.Views {
			if v.ID == id {
				if v.Filters == nil {
					v.Filters = []Filter{}
				}
				if v.Sorts == nil {
					v.Sorts = []Sort{}
				}
				if v.Columns == nil {
					v.Columns = []string{}
				}
				return &v, nil
			}
		}
	}
	return nil, fmt.Errorf("view not found")
}

func (m *Manager) Save(view View) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if view.Filters == nil {
		view.Filters = []Filter{}
	}
	if view.Sorts == nil {
		view.Sorts = []Sort{}
	}
	if view.Columns == nil {
		view.Columns = []string{}
	}

	bases, err := m.listBasesLocked()
	if err != nil {
		return err
	}

	if view.ID == "" {
		totalViews := 0
		for _, b := range bases {
			totalViews += len(b.Views)
		}
		if view.Name != "" {
			view.ID = Slugify(view.Name)
		} else {
			view.ID = fmt.Sprintf("view_%d", totalViews+1)
		}
	}

	// 1. Check if view exists in an existing base
	for _, b := range bases {
		for i, existing := range b.Views {
			if existing.ID == view.ID {
				b.Views[i] = view
				return m.saveBaseLocked(b)
			}
		}
	}

	// 2. Check if an existing base matches view.Source
	if view.Source != "" {
		sourceSlug := Slugify(view.Source)
		for _, b := range bases {
			if b.ID == sourceSlug || (len(b.Views) > 0 && b.Views[0].Source == view.Source) {
				b.Views = append(b.Views, view)
				return m.saveBaseLocked(b)
			}
		}
	}

	// 3. Create a new base for this view
	baseSlug := Slugify(view.Name)
	if baseSlug == "" {
		baseSlug = Slugify(view.ID)
	}
	if baseSlug == "" {
		baseSlug = "base"
	}

	slug := baseSlug
	for i := 2; ; i++ {
		exists := false
		for _, b := range bases {
			if b.ID == slug {
				exists = true
				break
			}
		}
		if !exists {
			break
		}
		slug = fmt.Sprintf("%s-%d", baseSlug, i)
	}

	title := view.Name
	if title == "" {
		title = "Saved Views"
	}

	newBase := &Base{
		ID:      slug,
		Path:    filepath.Join(Dir, slug+".md"),
		Title:   title,
		Created: time.Now().UTC().Format(time.RFC3339),
		Tags:    []string{"base"},
		Views:   []View{view},
	}
	return m.saveBaseLocked(newBase)
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	bases, err := m.listBasesLocked()
	if err != nil {
		return err
	}

	for _, b := range bases {
		for i, existing := range b.Views {
			if existing.ID == id {
				b.Views = append(b.Views[:i], b.Views[i+1:]...)
				return m.saveBaseLocked(b)
			}
		}
	}
	return fmt.Errorf("view not found")
}
