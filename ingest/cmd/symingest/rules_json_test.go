package main

import (
	"encoding/json"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/danieljustus/symaira-ingest/internal/store"
)

type rulesJSONListResponse struct {
	SchemaVersion int                        `json:"schema_version"`
	Rules         []store.ClassificationRule `json:"rules"`
}

type rulesJSONRuleResponse struct {
	SchemaVersion int                      `json:"schema_version"`
	Rule          store.ClassificationRule `json:"rule"`
}

type rulesJSONTestResponse struct {
	SchemaVersion int             `json:"schema_version"`
	Matches       []ruleTestMatch `json:"matches"`
}

type rulesJSONDeleteResponse struct {
	SchemaVersion int   `json:"schema_version"`
	ID            int64 `json:"id"`
	Deleted       bool  `json:"deleted"`
}

func TestRunRulesJSON_ListEnvelope(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "rules.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "-json", "list"}); err != nil {
		t.Fatalf("rules list -json: %v", err)
	}

	var response rulesJSONListResponse
	if err := json.Unmarshal([]byte(sb.String()), &response); err != nil {
		t.Fatalf("decode list response: %v\n%s", err, sb.String())
	}
	if response.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", response.SchemaVersion)
	}
	if response.Rules == nil {
		t.Fatal("rules must be a JSON array, not null")
	}
	if len(response.Rules) != 0 {
		t.Fatalf("rules = %+v, want empty", response.Rules)
	}
}

func TestRunRulesJSON_AddUpdateTestDeleteEnvelopes(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "rules.db")
	sb := withCapturedStdout(t)

	if err := run([]string{"rules", "-db", tempDB, "-json", "add", "invoice", "category", "Finance"}); err != nil {
		t.Fatalf("rules add -json: %v", err)
	}
	var addResponse rulesJSONRuleResponse
	if err := json.Unmarshal([]byte(sb.String()), &addResponse); err != nil {
		t.Fatalf("decode add response: %v\n%s", err, sb.String())
	}
	if addResponse.SchemaVersion != 1 || addResponse.Rule.Pattern != "invoice" || addResponse.Rule.Kind != "category" || addResponse.Rule.Value != "Finance" {
		t.Fatalf("unexpected add response: %+v", addResponse)
	}

	ruleID := strconv.FormatInt(addResponse.Rule.ID, 10)
	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "-json", "update", ruleID, "receipt", "tag", "Finance"}); err != nil {
		t.Fatalf("rules update -json: %v", err)
	}
	var updateResponse rulesJSONRuleResponse
	if err := json.Unmarshal([]byte(sb.String()), &updateResponse); err != nil {
		t.Fatalf("decode update response: %v\n%s", err, sb.String())
	}
	if updateResponse.SchemaVersion != 1 || updateResponse.Rule.ID != addResponse.Rule.ID || updateResponse.Rule.Pattern != "receipt" || updateResponse.Rule.Kind != "tag" || updateResponse.Rule.Value != "Finance" {
		t.Fatalf("unexpected update response: %+v", updateResponse)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "-json", "test", "a receipt document"}); err != nil {
		t.Fatalf("rules test -json: %v", err)
	}
	var testResponse rulesJSONTestResponse
	if err := json.Unmarshal([]byte(sb.String()), &testResponse); err != nil {
		t.Fatalf("decode test response: %v\n%s", err, sb.String())
	}
	if testResponse.SchemaVersion != 1 || len(testResponse.Matches) != 1 || testResponse.Matches[0].ID != addResponse.Rule.ID {
		t.Fatalf("unexpected test response: %+v", testResponse)
	}

	sb.Reset()
	if err := run([]string{"rules", "-db", tempDB, "-json", "delete", ruleID}); err != nil {
		t.Fatalf("rules delete -json: %v", err)
	}
	var deleteResponse rulesJSONDeleteResponse
	if err := json.Unmarshal([]byte(sb.String()), &deleteResponse); err != nil {
		t.Fatalf("decode delete response: %v\n%s", err, sb.String())
	}
	if deleteResponse.SchemaVersion != 1 || deleteResponse.ID != addResponse.Rule.ID || !deleteResponse.Deleted {
		t.Fatalf("unexpected delete response: %+v", deleteResponse)
	}
}
