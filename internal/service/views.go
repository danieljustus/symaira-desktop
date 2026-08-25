package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func (s *Service) ViewsList() ([]dbviews.View, error) {
	return s.ViewsMgr.List()
}

func (s *Service) ViewsGet(id string) (*dbviews.View, error) {
	return s.ViewsMgr.Get(id)
}

func (s *Service) ViewsSave(data []byte) error {
	var v dbviews.View
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	if err := s.ViewsMgr.Save(v); err != nil {
		return err
	}
	return s.reindexAllBases()
}

func (s *Service) ViewsDelete(id string) error {
	if err := s.ViewsMgr.Delete(id); err != nil {
		return err
	}
	return s.reindexAllBases()
}

// BaseNew creates a new base note in the vault and indexes it.
func (s *Service) BaseNew(title, description string) (*dbviews.Base, error) {
	slug := dbviews.Slugify(title)
	base := &dbviews.Base{
		ID:          slug,
		Path:        filepath.Join(dbviews.Dir, slug+".md"),
		Title:       title,
		Description: description,
		Created:     time.Now().UTC().Format(time.RFC3339),
		Tags:        []string{"base"},
		Views:       []dbviews.View{},
	}
	if err := s.ViewsMgr.SaveBase(base); err != nil {
		return nil, err
	}
	if err := s.reindexBase(base); err != nil {
		return base, err
	}
	return base, nil
}

// BaseList lists every base in the vault.
func (s *Service) BaseList() ([]*dbviews.Base, error) {
	return s.ViewsMgr.ListBases()
}

// BaseGet resolves a base by ID or path.
func (s *Service) BaseGet(ref string) (*dbviews.Base, error) {
	return s.ViewsMgr.GetBase(ref)
}

// BaseSave persists a base note and reindexes it into the sidecar.
func (s *Service) BaseSave(b *dbviews.Base) error {
	if err := s.ViewsMgr.SaveBase(b); err != nil {
		return err
	}
	return s.reindexBase(b)
}

// BaseDelete moves a base note to the vault trash.
func (s *Service) BaseDelete(ref string) error {
	b, err := s.ViewsMgr.GetBase(ref)
	if err != nil {
		return err
	}
	_, err = s.NoteDelete(b.Path)
	return err
}

func (s *Service) reindexAllBases() error {
	if s.DB == nil {
		return nil
	}
	bases, err := s.ViewsMgr.ListBases()
	if err != nil {
		return err
	}
	for _, b := range bases {
		if err := s.reindexBase(b); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) reindexBase(b *dbviews.Base) error {
	if s.DB == nil {
		return nil
	}
	absPath, err := vault.SecurePath(s.VaultRoot, b.Path)
	if err != nil {
		return err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return fmt.Errorf("wrote base but failed to parse for indexing: %w", err)
	}
	if err := s.IndexDocument(doc); err != nil {
		return fmt.Errorf("wrote base but failed to index: %w", err)
	}
	return nil
}

// ViewsSiblings returns views that point at the same source. An empty source
// intentionally has no siblings: legacy views are independent by default.
func (s *Service) ViewsSiblings(id string) ([]dbviews.View, error) {
	view, err := s.ViewsGet(id)
	if err != nil {
		return nil, err
	}
	if view.Source == "" {
		return []dbviews.View{*view}, nil
	}
	views, err := s.ViewsList()
	if err != nil {
		return nil, err
	}
	result := make([]dbviews.View, 0)
	for _, candidate := range views {
		if candidate.Source == view.Source {
			result = append(result, candidate)
		}
	}
	return result, nil
}

// ViewsNewEntry creates a note from a view's optional template and fills in
// explicit defaults plus equality filters so the new note belongs in the view.
func (s *Service) ViewsNewEntry(id, title string) (string, error) {
	view, err := s.ViewsGet(id)
	if err != nil {
		return "", err
	}
	templateRef := ""
	defaults := map[string]string{}
	if view.Template != nil {
		templateRef = view.Template.Ref
		for key, value := range view.Template.Defaults {
			defaults[key] = value
		}
	}
	for _, filter := range view.Filters {
		if filter.Operator == "" || strings.EqualFold(filter.Operator, "equals") {
			defaults[filter.Key] = filter.Value
		}
	}
	path, err := s.NoteNew(title, "", templateRef)
	if err != nil {
		return "", err
	}
	for key, value := range defaults {
		if err := s.PropsEdit(path, key, value); err != nil {
			return path, err
		}
	}
	return path, nil
}

// resolveViewDocuments resolves the scoped candidate documents for a view
// before properties are retrieved.
// Supported source scopes:
//   - empty (""): entire vault
//   - folder prefix (e.g. "invoices/"): only documents under that directory
//   - tag (e.g. "tag:invoice"): only documents carrying that tag
//   - notebook (e.g. "notebook:<id>"): only documents in that notebook's sources
func (s *Service) resolveViewDocuments(view *dbviews.View) ([]*vault.Document, error) {
	source := strings.TrimSpace(view.Source)
	if source == "" {
		return s.DB.ListFiles("")
	}

	if strings.HasPrefix(source, "tag:") {
		tagName := strings.TrimSpace(strings.TrimPrefix(source, "tag:"))
		tagName = strings.TrimPrefix(tagName, "#")
		if tagName == "" {
			return nil, fmt.Errorf("view source tag cannot be empty")
		}
		docsList, err := s.DB.DocsList(sidecar.DocsFilter{})
		if err != nil {
			return nil, err
		}
		var docs []*vault.Document
		for _, r := range docsList {
			if containsTag(r.Tags, tagName) {
				docs = append(docs, &vault.Document{
					Path:  r.Path,
					Title: r.Title,
				})
			}
		}
		return docs, nil
	}

	if strings.HasPrefix(source, "notebook:") {
		notebookRef := strings.TrimSpace(strings.TrimPrefix(source, "notebook:"))
		if notebookRef == "" {
			return nil, fmt.Errorf("view source notebook reference cannot be empty")
		}
		nb, err := s.NotebookGet(notebookRef)
		if err != nil {
			return nil, fmt.Errorf("view source notebook %q not found: %w", notebookRef, err)
		}
		var docs []*vault.Document
		for _, src := range nb.Sources {
			absPath, err := vault.SecurePath(s.VaultRoot, src)
			if err != nil {
				continue
			}
			info, statErr := os.Stat(absPath)
			if statErr != nil || info.IsDir() {
				continue
			}
			title := ""
			if docTitle, err := s.DB.GetTitle(absPath); err == nil && docTitle != "" {
				title = docTitle
			} else if parsed, err := vault.ParseFile(absPath); err == nil && parsed.Title != "" {
				title = parsed.Title
			} else {
				base := filepath.Base(src)
				title = strings.TrimSuffix(base, filepath.Ext(base))
			}
			docs = append(docs, &vault.Document{
				Path:  absPath,
				Title: title,
			})
		}
		return docs, nil
	}

	// Folder prefix source
	folderRel := strings.TrimPrefix(source, "/")
	absDir, err := vault.SecurePath(s.VaultRoot, folderRel)
	if err != nil {
		return nil, fmt.Errorf("view source folder %q is invalid: %w", view.Source, err)
	}
	info, statErr := os.Stat(absDir)
	if statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, fmt.Errorf("view source folder %q does not exist", view.Source)
		}
		return nil, fmt.Errorf("view source folder %q: %w", view.Source, statErr)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("view source folder %q is not a directory", view.Source)
	}

	dirPrefix := filepath.Clean(absDir) + string(filepath.Separator)
	return s.DB.ListFiles(dirPrefix)
}

func (s *Service) ViewsExec(id string) ([]map[string]interface{}, error) {
	view, err := s.ViewsGet(id)
	if err != nil {
		return nil, err
	}

	docs, err := s.resolveViewDocuments(view)
	if err != nil {
		return nil, err
	}

	allProps := make(map[string]map[string]interface{})
	for _, d := range docs {
		props, err := s.DB.GetProperties(d.Path)
		if err != nil {
			props = make(map[string]interface{})
		}
		props["_path"] = d.Path
		props["_title"] = d.Title
		allProps[d.Path] = props
	}

	// Precompute links for rollups
	linksMap := make(map[string][]string)
	if len(view.Computed) > 0 {
		edges, err := s.DB.GetAllLinks()
		if err == nil {
			for _, e := range edges {
				linksMap[e.Source] = append(linksMap[e.Source], e.Target)
			}
		}
	}

	var results []map[string]interface{}
	for _, d := range docs {
		props := allProps[d.Path]

		// Evaluate formulas and rollups
		for colName, comp := range view.Computed {
			if comp.Formula != "" {
				props[colName] = s.evaluateFormula(comp.Formula, props)
			} else if comp.Rollup != "" {
				props[colName] = s.evaluateRollup(comp.Rollup, d.Path, linksMap, allProps)
			}
		}

		match := true
		for _, f := range view.Filters {
			if !matchesFilter(props, f) {
				match = false
				break
			}
		}
		if match && view.FilterGroup != nil {
			match = matchesGroup(props, *view.FilterGroup)
		}

		if match {
			results = append(results, props)
		}
	}

	return results, nil
}

func matchesGroup(props map[string]interface{}, group dbviews.FilterGroup) bool {
	all := strings.ToLower(group.Operator) != "any"
	matched := 0
	total := len(group.Filters) + len(group.Groups)
	for _, filter := range group.Filters {
		if matchesFilter(props, filter) {
			matched++
		} else if all {
			return false
		}
	}
	for _, child := range group.Groups {
		if matchesGroup(props, child) {
			matched++
		} else if all {
			return false
		}
	}
	if total == 0 {
		return true
	}
	return matched > 0
}

func parseNumberFlexible(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func parseDateFlexible(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	formats := []string{
		"2006-01-02",
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func extractSetElements(val interface{}) []string {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case []string:
		var res []string
		for _, s := range v {
			trimmed := strings.TrimSpace(s)
			if trimmed != "" {
				res = append(res, trimmed)
			}
		}
		return res
	case []interface{}:
		var res []string
		for _, item := range v {
			trimmed := strings.TrimSpace(fmt.Sprintf("%v", item))
			if trimmed != "" && trimmed != "<nil>" {
				res = append(res, trimmed)
			}
		}
		return res
	case string:
		s := strings.TrimSpace(v)
		if s == "" || s == "<nil>" {
			return nil
		}
		// Handle Go slice string formatting like "[a b c]" or "[a, b, c]"
		if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
			s = strings.TrimPrefix(s, "[")
			s = strings.TrimSuffix(s, "]")
			s = strings.TrimSpace(s)
		}
		if s == "" {
			return nil
		}
		var items []string
		if strings.Contains(s, ",") {
			items = strings.Split(s, ",")
		} else {
			items = strings.Fields(s)
		}
		var res []string
		for _, item := range items {
			trimmed := strings.TrimSpace(item)
			if trimmed != "" {
				res = append(res, trimmed)
			}
		}
		return res
	default:
		s := strings.TrimSpace(fmt.Sprintf("%v", val))
		if s != "" && s != "<nil>" {
			return []string{s}
		}
		return nil
	}
}

func matchesFilter(props map[string]interface{}, filter dbviews.Filter) bool {
	value, exists := props[filter.Key]
	actual := ""
	if exists && value != nil {
		actual = fmt.Sprintf("%v", value)
	}
	op := strings.ToLower(strings.TrimSpace(filter.Operator))
	filterVal := strings.TrimSpace(filter.Value)

	switch op {
	case "is_empty", "empty":
		return !exists || actual == "" || actual == "<nil>"
	case "is_not_empty", "not_empty":
		return exists && actual != "" && actual != "<nil>"
	case "not_equals", "is_not", "!=":
		if !exists || actual == "<nil>" {
			return true
		}
		return !valuesEqual(actual, filterVal)
	case "contains":
		return exists && strings.Contains(strings.ToLower(actual), strings.ToLower(filterVal))
	case "not_contains":
		return !exists || !strings.Contains(strings.ToLower(actual), strings.ToLower(filterVal))
	case "starts_with", "prefix":
		return exists && strings.HasPrefix(strings.ToLower(actual), strings.ToLower(filterVal))
	case "ends_with", "suffix":
		return exists && strings.HasSuffix(strings.ToLower(actual), strings.ToLower(filterVal))

	// Numeric and relational operators
	case "greater_than", "gt", ">":
		if !exists || actual == "" {
			return false
		}
		return compareRelational(actual, filterVal) > 0
	case "greater_than_or_equal", "gte", ">=":
		if !exists || actual == "" {
			return false
		}
		return compareRelational(actual, filterVal) >= 0
	case "less_than", "lt", "<":
		if !exists || actual == "" {
			return false
		}
		return compareRelational(actual, filterVal) < 0
	case "less_than_or_equal", "lte", "<=":
		if !exists || actual == "" {
			return false
		}
		return compareRelational(actual, filterVal) <= 0

	// Date-specific operators
	case "after":
		if !exists || actual == "" {
			return false
		}
		if dActual, okA := parseDateFlexible(actual); okA {
			if dFilter, okF := parseDateFlexible(filterVal); okF {
				return dActual.After(dFilter)
			}
		}
		return actual > filterVal
	case "before":
		if !exists || actual == "" {
			return false
		}
		if dActual, okA := parseDateFlexible(actual); okA {
			if dFilter, okF := parseDateFlexible(filterVal); okF {
				return dActual.Before(dFilter)
			}
		}
		return actual < filterVal
	case "on_or_after":
		if !exists || actual == "" {
			return false
		}
		if dActual, okA := parseDateFlexible(actual); okA {
			if dFilter, okF := parseDateFlexible(filterVal); okF {
				return dActual.After(dFilter) || dActual.Equal(dFilter)
			}
		}
		return actual >= filterVal
	case "on_or_before":
		if !exists || actual == "" {
			return false
		}
		if dActual, okA := parseDateFlexible(actual); okA {
			if dFilter, okF := parseDateFlexible(filterVal); okF {
				return dActual.Before(dFilter) || dActual.Equal(dFilter)
			}
		}
		return actual <= filterVal

	// Set / Multi-value operators
	case "in":
		if !exists || actual == "" {
			return false
		}
		filterSet := extractSetElements(filterVal)
		actualItems := extractSetElements(value)
		for _, a := range actualItems {
			for _, f := range filterSet {
				if strings.EqualFold(a, f) {
					return true
				}
			}
		}
		return false
	case "not_in":
		if !exists || actual == "" {
			return true
		}
		filterSet := extractSetElements(filterVal)
		actualItems := extractSetElements(value)
		for _, a := range actualItems {
			for _, f := range filterSet {
				if strings.EqualFold(a, f) {
					return false
				}
			}
		}
		return true
	case "contains_all":
		if !exists || actual == "" {
			return false
		}
		filterSet := extractSetElements(filterVal)
		actualItems := extractSetElements(value)
		for _, f := range filterSet {
			found := false
			for _, a := range actualItems {
				if strings.EqualFold(a, f) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return len(filterSet) > 0
	case "contains_any":
		if !exists || actual == "" {
			return false
		}
		filterSet := extractSetElements(filterVal)
		actualItems := extractSetElements(value)
		for _, f := range filterSet {
			for _, a := range actualItems {
				if strings.EqualFold(a, f) {
					return true
				}
			}
		}
		return false
	case "contains_none":
		if !exists || actual == "" {
			return true
		}
		filterSet := extractSetElements(filterVal)
		actualItems := extractSetElements(value)
		for _, f := range filterSet {
			for _, a := range actualItems {
				if strings.EqualFold(a, f) {
					return false
				}
			}
		}
		return true

	default: // "equals", "is", "=", "==", ""
		if !exists {
			return filterVal == ""
		}
		return valuesEqual(actual, filterVal)
	}
}

func valuesEqual(a, b string) bool {
	if a == b {
		return true
	}
	if nA, okA := parseNumberFlexible(a); okA {
		if nB, okB := parseNumberFlexible(b); okB {
			return nA == nB
		}
	}
	if dA, okA := parseDateFlexible(a); okA {
		if dB, okB := parseDateFlexible(b); okB {
			return dA.Equal(dB)
		}
	}
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func compareRelational(a, b string) int {
	if nA, okA := parseNumberFlexible(a); okA {
		if nB, okB := parseNumberFlexible(b); okB {
			if nA < nB {
				return -1
			} else if nA > nB {
				return 1
			}
			return 0
		}
	}
	if dA, okA := parseDateFlexible(a); okA {
		if dB, okB := parseDateFlexible(b); okB {
			if dA.Before(dB) {
				return -1
			} else if dA.After(dB) {
				return 1
			}
			return 0
		}
	}
	if a < b {
		return -1
	} else if a > b {
		return 1
	}
	return 0
}

func (s *Service) evaluateFormula(formula string, props map[string]interface{}) string {
	res := formula
	for k, v := range props {
		res = strings.ReplaceAll(res, "{"+k+"}", fmt.Sprintf("%v", v))
	}
	return res
}

func (s *Service) evaluateRollup(rollup string, path string, links map[string][]string, allProps map[string]map[string]interface{}) string {
	targets := links[path]
	if len(targets) == 0 {
		return ""
	}

	if strings.HasPrefix(rollup, "count") {
		return fmt.Sprintf("%d", len(targets))
	}

	if strings.HasPrefix(rollup, "sum(links.") {
		prop := strings.TrimSuffix(strings.TrimPrefix(rollup, "sum(links."), ")")
		sum := 0.0
		for _, t := range targets {
			if p, ok := allProps[t]; ok {
				if val, ok2 := p[prop]; ok2 {
					if f, err := strconv.ParseFloat(fmt.Sprintf("%v", val), 64); err == nil {
						sum += f
					}
				}
			}
		}
		return fmt.Sprintf("%.2f", sum)
	}

	if strings.HasPrefix(rollup, "list(links.") {
		prop := strings.TrimSuffix(strings.TrimPrefix(rollup, "list(links."), ")")
		var vals []string
		for _, t := range targets {
			if p, ok := allProps[t]; ok {
				if val, ok2 := p[prop]; ok2 {
					vals = append(vals, fmt.Sprintf("%v", val))
				}
			}
		}
		return strings.Join(vals, ", ")
	}

	return ""
}
