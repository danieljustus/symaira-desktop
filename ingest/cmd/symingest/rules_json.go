package main

import (
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-ingest/internal/store"
)

const rulesJSONSchemaVersion = 1

type rulesListJSONResponse struct {
	SchemaVersion int                         `json:"schema_version"`
	Rules         []*store.ClassificationRule `json:"rules"`
}

type rulesRuleJSONResponse struct {
	SchemaVersion int                       `json:"schema_version"`
	Rule          *store.ClassificationRule `json:"rule"`
}

type rulesTestJSONResponse struct {
	SchemaVersion int             `json:"schema_version"`
	Matches       []ruleTestMatch `json:"matches"`
}

type rulesDeleteJSONResponse struct {
	SchemaVersion int   `json:"schema_version"`
	ID            int64 `json:"id"`
	Deleted       bool  `json:"deleted"`
}

func printRulesJSON(value any, message string) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return exitcodes.Wrap(err, exitcodes.ExitGeneric, exitcodes.KindInternal, message)
	}
	fmt.Fprintln(stdout, string(data))
	return nil
}
