package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newDatasetListTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_dataset_list",
		Description: "Lists Markdown-backed datasets and their materialized row counts.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			if _, err := decodeObject(input); err != nil {
				return nil, err
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.DatasetList()
		},
	}
}

func newDatasetDescribeTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_dataset_describe",
		Description: "Describes one Markdown-backed dataset, including schema, provenance, coverage and row count.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"dataset":{"type":"string"}},"required":["dataset"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Dataset string `json:"dataset"`
			}
			if err := decodeDatasetInput(input, &args); err != nil {
				return nil, err
			}
			if args.Dataset == "" {
				return nil, fmt.Errorf("dataset is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.DatasetDescribe(args.Dataset)
		},
	}
}

func newDatasetQueryTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_dataset_query",
		Description: "Queries a dataset with selected columns, existing view filter operators, grouping and bounded sum/count/min/max/average aggregates. Raw SQL is not accepted.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"dataset":{"type":"string"},"columns":{"type":"array","items":{"type":"string"}},"filters":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"operator":{"type":"string"},"value":{"type":"string"}},"required":["key","value"],"additionalProperties":false}},"filter_group":{"type":"object"},"group_by":{"type":"string"},"aggregates":{"type":"array","items":{"type":"object","properties":{"column":{"type":"string"},"function":{"type":"string","enum":["sum","count","min","max","average"]},"as":{"type":"string"}},"required":["function"],"additionalProperties":false}},"limit":{"type":"integer","minimum":1}},"required":["dataset"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Dataset     string                     `json:"dataset"`
				Columns     []string                   `json:"columns"`
				Filters     []dbviews.Filter           `json:"filters"`
				FilterGroup *dbviews.FilterGroup       `json:"filter_group"`
				GroupBy     string                     `json:"group_by"`
				Aggregates  []service.DatasetAggregate `json:"aggregates"`
				Limit       int                        `json:"limit"`
			}
			if err := decodeDatasetInput(input, &args); err != nil {
				return nil, err
			}
			if args.Dataset == "" {
				return nil, fmt.Errorf("dataset is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.DatasetQuery(args.Dataset, service.DatasetQueryOptions{Columns: args.Columns, Filters: args.Filters, FilterGroup: args.FilterGroup, GroupBy: args.GroupBy, Aggregates: args.Aggregates, Limit: args.Limit})
		},
	}
}

func newDatasetSyncTool(getService ServiceFactory) *Tool {
	return &Tool{
		Name:        "desk_dataset_sync",
		Description: "Persists producer rows into a Markdown-backed dataset. Every row must have an explicit identity and every sync must include source_name, source_sha256 and imported_at provenance; repeated provenance is idempotent.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"dataset":{"type":"string"},"title":{"type":"string"},"identity_field":{"type":"string"},"sensitivity":{"type":"string","enum":["public","internal","confidential","restricted"]},"retention_rule":{"type":"string"},"schema":{"type":"object"},"provenance":{"type":"object","properties":{"imported_at":{"type":"string"},"source_name":{"type":"string"},"source_sha256":{"type":"string"}},"required":["imported_at","source_name","source_sha256"],"additionalProperties":false},"rows":{"type":"array","items":{"type":"object","properties":{"identity":{"type":"string"},"values":{"type":"object"}},"required":["identity","values"],"additionalProperties":false}}},"required":["dataset","identity_field","provenance","rows"],"additionalProperties":false}`),
		Handler: func(ctx context.Context, input json.RawMessage) (any, error) {
			var args struct {
				Dataset       string                            `json:"dataset"`
				Title         string                            `json:"title"`
				IdentityField string                            `json:"identity_field"`
				Sensitivity   string                            `json:"sensitivity"`
				RetentionRule string                            `json:"retention_rule"`
				Schema        map[string]dbviews.PropertyConfig `json:"schema"`
				Provenance    datasetProvenance                 `json:"provenance"`
				Rows          []service.DatasetSyncRow          `json:"rows"`
			}
			if err := decodeDatasetInput(input, &args); err != nil {
				return nil, err
			}
			if args.Dataset == "" {
				return nil, fmt.Errorf("dataset is required")
			}
			svc, db, err := getService()
			if err != nil {
				return nil, err
			}
			defer func() { _ = db.Close() }()
			return svc.DatasetSync(service.DatasetSyncOptions{Slug: args.Dataset, Title: args.Title, IdentityField: args.IdentityField, Schema: args.Schema, Sensitivity: args.Sensitivity, RetentionRule: args.RetentionRule, Provenance: args.Provenance.Provenance(), Rows: args.Rows})
		},
	}
}

type datasetProvenance struct {
	ImportedAt   string `json:"imported_at"`
	SourceName   string `json:"source_name"`
	SourceSHA256 string `json:"source_sha256"`
}

func (p datasetProvenance) Provenance() dataset.Provenance {
	return dataset.Provenance{ImportedAt: p.ImportedAt, SourceName: p.SourceName, SourceSHA256: p.SourceSHA256}
}

func decodeObject(input json.RawMessage) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(input, &object); err != nil {
		return nil, err
	}
	return object, nil
}

func decodeDatasetInput(input json.RawMessage, target interface{}) error {
	decoder := json.NewDecoder(bytes.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("dataset input must contain one JSON object")
		}
		return err
	}
	return nil
}
