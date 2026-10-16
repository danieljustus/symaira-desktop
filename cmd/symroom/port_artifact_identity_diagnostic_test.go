package main

import (
	"strings"
	"testing"
)

func TestArtifactIdentityDifferenceReportsFieldWithoutContent(t *testing.T) {
	stored := []byte(`{"identity_key":"private-fixture-value","cases":[{"name":"inspect-success","final_files":[{"name":"journal.jsonl","content":"recorded-private-content"}]}]}`)
	current := []byte(`{"identity_key":"private-fixture-value","cases":[{"name":"inspect-success","final_files":[{"name":"journal.jsonl","content":"changed-private-content"}]}]}`)
	got := noteCLIContractDifference(stored, current)
	if got != "cases[0].final_files" || strings.Contains(got, "private") {
		t.Fatalf("diagnostic must identify the field without values: %q", got)
	}
}
