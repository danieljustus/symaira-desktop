package service

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

// BaseEmbedSpec defines the parameters specified within a ```symdesk-base code block.
type BaseEmbedSpec struct {
	Base    string   `yaml:"base" json:"base"`
	View    string   `yaml:"view,omitempty" json:"view,omitempty"`
	Limit   int      `yaml:"limit,omitempty" json:"limit,omitempty"`
	Cap     int      `yaml:"cap,omitempty" json:"cap,omitempty"`
	Columns []string `yaml:"columns,omitempty" json:"columns,omitempty"`
}

// BaseEmbedResult is the evaluated, read-only result of executing a symdesk-base embed.
type BaseEmbedResult struct {
	BaseID    string                   `json:"base_id"`
	BasePath  string                   `json:"base_path"`
	BaseTitle string                   `json:"base_title"`
	ViewID    string                   `json:"view_id"`
	ViewName  string                   `json:"view_name"`
	Columns   []string                 `json:"columns"`
	Rows      []map[string]interface{} `json:"rows"`
	TotalRows int                      `json:"total_rows"`
	Capped    bool                     `json:"capped"`
	RowCap    int                      `json:"row_cap"`
	Markdown  string                   `json:"markdown"`
}

// ExecuteBaseEmbed parses a symdesk-base embed specification in YAML, resolves
// the target base and view, executes the view queries, applies row caps, and
// generates an inert Markdown table representation with clear errors.
func (s *Service) ExecuteBaseEmbed(specYAML string) (*BaseEmbedResult, error) {
	var spec BaseEmbedSpec
	if err := yaml.Unmarshal([]byte(specYAML), &spec); err != nil {
		return nil, fmt.Errorf("invalid symdesk-base specification: %w", err)
	}

	spec.Base = strings.TrimSpace(spec.Base)
	if spec.Base == "" {
		return nil, errors.New("symdesk-base specification requires a non-empty 'base' field")
	}

	base, err := s.BaseGet(spec.Base)
	if err != nil {
		return nil, fmt.Errorf("base %q not found", spec.Base)
	}

	if len(base.Views) == 0 {
		return nil, fmt.Errorf("base %q has no views defined", base.Title)
	}

	var selectedView *dbviews.View

	specView := strings.TrimSpace(spec.View)
	if specView != "" {
		for i := range base.Views {
			v := &base.Views[i]
			if v.ID == specView || strings.EqualFold(v.Name, specView) {
				selectedView = v
				break
			}
		}
		if selectedView == nil {
			return nil, fmt.Errorf("view %q not found in base %q", spec.View, base.Title)
		}
	} else {
		// Default to the first view in the base
		selectedView = &base.Views[0]
	}

	rowCap := spec.Limit
	if rowCap <= 0 {
		rowCap = spec.Cap
	}
	if rowCap <= 0 {
		rowCap = 10 // default cap
	}

	var rows []map[string]interface{}
	var totalRows int
	if strings.HasPrefix(strings.TrimSpace(selectedView.Source), "dataset:") {
		rows, totalRows, err = s.executeDatasetViewWithTotal(selectedView, rowCap)
	} else {
		rows, err = s.ViewsExec(selectedView.ID)
		totalRows = len(rows)
	}
	if err != nil {
		return nil, fmt.Errorf("execute view %q: %w", selectedView.ID, err)
	}

	// Determine visible columns
	columns := spec.Columns
	if len(columns) == 0 {
		columns = selectedView.Columns
	}
	if len(columns) == 0 {
		colSet := make(map[string]bool)
		for _, r := range rows {
			for k := range r {
				if k == "_path" {
					continue
				}
				colSet[k] = true
			}
		}
		var sortedCols []string
		for k := range colSet {
			sortedCols = append(sortedCols, k)
		}
		sort.Strings(sortedCols)
		if colSet["_title"] {
			columns = append([]string{"_title"}, removeCol(sortedCols, "_title")...)
		} else if colSet["title"] {
			columns = append([]string{"title"}, removeCol(sortedCols, "title")...)
		} else {
			columns = sortedCols
		}
	}

	// Clean columns list
	var cleanCols []string
	for _, c := range columns {
		c = strings.TrimSpace(c)
		if c != "" {
			cleanCols = append(cleanCols, c)
		}
	}
	if len(cleanCols) == 0 {
		cleanCols = []string{"title"}
	}

	capped := len(rows) < totalRows
	displayRows := rows
	if len(rows) > rowCap {
		displayRows = rows[:rowCap]
		capped = true
	}

	markdown := renderInertMarkdownTable(cleanCols, displayRows, base, selectedView.Name, totalRows, capped, rowCap)

	return &BaseEmbedResult{
		BaseID:    base.ID,
		BasePath:  base.Path,
		BaseTitle: base.Title,
		ViewID:    selectedView.ID,
		ViewName:  selectedView.Name,
		Columns:   cleanCols,
		Rows:      displayRows,
		TotalRows: totalRows,
		Capped:    capped,
		RowCap:    rowCap,
		Markdown:  markdown,
	}, nil
}

func removeCol(cols []string, target string) []string {
	var res []string
	for _, c := range cols {
		if c != target {
			res = append(res, c)
		}
	}
	return res
}

func renderInertMarkdownTable(cols []string, rows []map[string]interface{}, base *dbviews.Base, viewName string, totalRows int, capped bool, rowCap int) string {
	_ = viewName
	var buf strings.Builder

	// Header row
	buf.WriteString("|")
	for _, c := range cols {
		displayCol := c
		if displayCol == "_title" {
			displayCol = "Title"
		} else if displayCol == "_path" {
			displayCol = "Path"
		} else if base != nil && base.Properties != nil {
			if prop, ok := base.Properties[c]; ok && prop.Label != "" {
				displayCol = prop.Label
			}
		}
		buf.WriteString(" " + sanitizeMarkdownCell(displayCol) + " |")
	}
	buf.WriteString("\n|")
	for range cols {
		buf.WriteString(" --- |")
	}
	buf.WriteString("\n")

	// Data rows
	for _, row := range rows {
		buf.WriteString("|")
		for _, c := range cols {
			val, exists := row[c]
			if !exists && c == "title" {
				val = row["_title"]
			}
			cellStr := ""
			if val != nil {
				switch v := val.(type) {
				case []string:
					cellStr = strings.Join(v, ", ")
				case []interface{}:
					var parts []string
					for _, item := range v {
						parts = append(parts, fmt.Sprintf("%v", item))
					}
					cellStr = strings.Join(parts, ", ")
				default:
					cellStr = fmt.Sprintf("%v", val)
				}
			}
			buf.WriteString(" " + sanitizeMarkdownCell(cellStr) + " |")
		}
		buf.WriteString("\n")
	}

	if capped && base != nil {
		fmt.Fprintf(&buf, "\n*Showing %d of %d rows. [[%s|Open %s]]*\n", len(rows), totalRows, base.Path, base.Title)
	}

	return buf.String()
}

func sanitizeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	return strings.TrimSpace(s)
}
