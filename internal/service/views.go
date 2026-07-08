package service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
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
	return s.ViewsMgr.Save(v)
}

func (s *Service) ViewsExec(id string) ([]map[string]interface{}, error) {
	view, err := s.ViewsGet(id)
	if err != nil {
		return nil, err
	}

	docs, err := s.DB.ListFiles("")
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
			val, exists := props[f.Key]
			if !exists {
				match = false
				break
			}
			if fmt.Sprintf("%v", val) != f.Value {
				match = false
				break
			}
		}

		if match {
			results = append(results, props)
		}
	}

	return results, nil
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
