package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newDatasetCmd() *cobra.Command {
	datasetCmd := &cobra.Command{Use: "dataset", Short: "Manage bounded, Markdown-backed datasets"}

	datasetCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List datasets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			res, err := service.New(vRoot, db).DatasetList()
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	})

	datasetCmd.AddCommand(&cobra.Command{
		Use:   "describe [dataset]",
		Short: "Describe a dataset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			res, err := service.New(vRoot, db).DatasetDescribe(args[0])
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	})

	var columns, filters, filterGroup, groupBy, aggregates string
	var limit int
	queryCmd := &cobra.Command{
		Use:   "query [dataset]",
		Short: "Query a dataset with bounded structured filters and aggregates",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			var opts service.DatasetQueryOptions
			if columns != "" {
				opts.Columns = splitCSVFlag(columns)
			}
			if err := unmarshalOptionalJSON(filters, &opts.Filters); err != nil {
				return fmt.Errorf("parse --filters: %w", err)
			}
			if err := unmarshalOptionalJSON(filterGroup, &opts.FilterGroup); err != nil {
				return fmt.Errorf("parse --filter-group: %w", err)
			}
			if err := unmarshalOptionalJSON(aggregates, &opts.Aggregates); err != nil {
				return fmt.Errorf("parse --aggregates: %w", err)
			}
			opts.GroupBy, opts.Limit = groupBy, limit
			res, err := service.New(vRoot, db).DatasetQuery(args[0], opts)
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	queryCmd.Flags().StringVar(&columns, "columns", "", "comma-separated selected columns")
	queryCmd.Flags().StringVar(&filters, "filters", "", "JSON array of view filters")
	queryCmd.Flags().StringVar(&filterGroup, "filter-group", "", "JSON view filter group")
	queryCmd.Flags().StringVar(&groupBy, "group-by", "", "column to group by")
	queryCmd.Flags().StringVar(&aggregates, "aggregates", "", "JSON array of sum/count/min/max/average aggregates")
	queryCmd.Flags().IntVar(&limit, "limit", 0, "maximum rows to return (default and hard maximum are enforced)")
	datasetCmd.AddCommand(queryCmd)

	var rowsInput, provenanceInput, sourceName, sourceSHA256, importedAt, title, identityField, schemaInput, sensitivity, retentionRule string
	syncCmd := &cobra.Command{
		Use:   "sync [dataset]",
		Short: "Sync producer rows with explicit provenance",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			rowData, err := readJSONInput(rowsInput)
			if err != nil {
				return fmt.Errorf("read --rows: %w", err)
			}
			var rows []service.DatasetSyncRow
			if err := json.Unmarshal(rowData, &rows); err != nil {
				return fmt.Errorf("parse --rows: %w", err)
			}
			var provenance dataset.Provenance
			if provenanceInput != "" {
				data, err := readJSONInput(provenanceInput)
				if err != nil {
					return fmt.Errorf("read --provenance: %w", err)
				}
				if err := json.Unmarshal(data, &provenance); err != nil {
					return fmt.Errorf("parse --provenance: %w", err)
				}
			}
			if sourceName != "" {
				provenance.SourceName = sourceName
			}
			if sourceSHA256 != "" {
				provenance.SourceSHA256 = sourceSHA256
			}
			if importedAt != "" {
				provenance.ImportedAt = importedAt
			}
			var schema map[string]dbviews.PropertyConfig
			if schemaInput != "" {
				data, err := readJSONInput(schemaInput)
				if err != nil {
					return fmt.Errorf("read --schema: %w", err)
				}
				if err := json.Unmarshal(data, &schema); err != nil {
					return fmt.Errorf("parse --schema: %w", err)
				}
			}
			res, err := service.New(vRoot, db).DatasetSync(service.DatasetSyncOptions{Slug: args[0], Title: title, IdentityField: identityField, Schema: schema, Sensitivity: sensitivity, RetentionRule: retentionRule, Provenance: provenance, Rows: rows})
			if err != nil {
				return err
			}
			return outputResult(res)
		},
	}
	syncCmd.Flags().StringVar(&rowsInput, "rows", "", "JSON array or path to JSON array of {identity,values} rows")
	syncCmd.Flags().StringVar(&provenanceInput, "provenance", "", "JSON object or path with imported_at, source_name and source_sha256")
	syncCmd.Flags().StringVar(&sourceName, "source-name", "", "producer/source name")
	syncCmd.Flags().StringVar(&sourceSHA256, "source-sha256", "", "producer source SHA-256")
	syncCmd.Flags().StringVar(&importedAt, "imported-at", "", "RFC3339 import timestamp")
	syncCmd.Flags().StringVar(&title, "title", "", "dataset title")
	syncCmd.Flags().StringVar(&identityField, "identity-field", "", "stable identity column")
	syncCmd.Flags().StringVar(&schemaInput, "schema", "", "JSON object or path with dbviews property definitions")
	syncCmd.Flags().StringVar(&sensitivity, "sensitivity", "", "dataset sensitivity: public|internal|confidential|restricted (default restricted)")
	syncCmd.Flags().StringVar(&retentionRule, "retention-rule", "", "named retention-rule reference (default: default)")
	_ = syncCmd.MarkFlagRequired("rows")
	datasetCmd.AddCommand(syncCmd)
	return datasetCmd
}

func splitCSVFlag(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			result = append(result, part)
		}
	}
	return result
}

func unmarshalOptionalJSON(input string, target interface{}) error {
	if strings.TrimSpace(input) == "" {
		return nil
	}
	return json.Unmarshal([]byte(input), target)
}

func readJSONInput(input string) ([]byte, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("input is required")
	}
	if strings.HasPrefix(input, "[") || strings.HasPrefix(input, "{") {
		return []byte(input), nil
	}
	// #nosec G304 -- explicit CLI input path.
	return os.ReadFile(input)
}
