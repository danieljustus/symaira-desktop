package service

import (
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/dbviews"
)

func (s *Service) executeDatasetView(view *dbviews.View, limit int) ([]map[string]interface{}, error) {
	rows, _, err := s.executeDatasetViewWithTotal(view, limit)
	return rows, err
}

func (s *Service) executeDatasetViewWithTotal(view *dbviews.View, limit int) ([]map[string]interface{}, int, error) {
	source := strings.TrimSpace(view.Source)
	slug := strings.TrimSpace(strings.TrimPrefix(source, "dataset:"))
	if slug == "" {
		return nil, 0, fmt.Errorf("view source dataset slug cannot be empty")
	}
	description, err := s.DatasetDescribe(slug)
	if err != nil {
		return nil, 0, err
	}
	options := DatasetQueryOptions{
		Filters:     view.Filters,
		FilterGroup: view.FilterGroup,
		Sorts:       view.Sorts,
		Limit:       limit,
	}
	if view.GroupBy != "" {
		options.GroupBy = view.GroupBy
		options.Aggregates = []DatasetAggregate{{Function: "count", As: "count"}}
	}
	queryOptions, err := datasetSidecarQueryOptions(description, options)
	if err != nil {
		return nil, 0, err
	}
	queried, err := s.DB.QueryDataset(description.Slug, queryOptions)
	if err != nil {
		return nil, 0, err
	}
	results := make([]map[string]interface{}, 0, len(queried.Rows))
	for _, row := range queried.Rows {
		values := row.Values
		if row.Identity != "" {
			values["identity"] = row.Identity
			values["_identity"] = row.Identity
			if _, ok := values["_title"]; !ok {
				values["_title"] = row.Identity
			}
		}
		if row.RowKey != "" {
			values["_key"] = row.RowKey
		}
		if row.SourcePath != "" {
			values["_path"] = row.SourcePath
		}
		for colName, computed := range view.Computed {
			if computed.Formula != "" {
				values[colName] = s.evaluateFormula(computed.Formula, values)
			} else if computed.Rollup != "" {
				// Dataset rows do not participate in the note-link graph. Match
				// note-view rollup semantics for a row with no linked targets.
				values[colName] = ""
			}
		}
		results = append(results, values)
	}
	return results, queried.TotalRows, nil
}

func aggregateColumns(groupBy string, aggregates []DatasetAggregate) []string {
	columns := make([]string, 0, 1+len(aggregates))
	if groupBy != "" {
		columns = append(columns, groupBy)
	}
	for _, aggregate := range aggregates {
		name := aggregate.As
		if name == "" {
			name = strings.ToLower(aggregate.Function) + "_" + aggregate.Column
			if strings.EqualFold(aggregate.Function, "count") && aggregate.Column == "" {
				name = "count"
			}
		}
		columns = append(columns, name)
	}
	if len(aggregates) == 0 && groupBy != "" {
		columns = append(columns, "count")
	}
	return columns
}
