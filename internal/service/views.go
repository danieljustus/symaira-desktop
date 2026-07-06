package service

import (
	"encoding/json"
	"fmt"
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

	// Basic execution: fetch all files and their properties
	// In a real scenario, we'd translate filters to SQL.
	docs, err := s.DB.ListFiles("")
	if err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	for _, d := range docs {
		props, err := s.DB.GetProperties(d.Path)
		if err != nil {
			props = make(map[string]interface{})
		}
		
		// Add built-ins
		props["_path"] = d.Path
		props["_title"] = d.Title
		
		// Very naive filtering (just exact match for now)
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
