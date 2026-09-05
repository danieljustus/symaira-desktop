package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/retention"
	"github.com/danieljustus/symaira-desktop/internal/service"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

func captureCommandStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = write
	commandErr := run()
	if err := write.Close(); err != nil && commandErr == nil {
		commandErr = err
	}
	os.Stdout = original
	data, err := io.ReadAll(read)
	if closeErr := read.Close(); err == nil {
		err = closeErr
	}
	if err != nil && commandErr == nil {
		commandErr = err
	}
	return string(data), commandErr
}

func isolatedCommandVault(t *testing.T) string {
	t.Helper()
	vaultRoot := t.TempDir()
	originalConfig := cfg
	cfg = &config.Config{Vault: vaultRoot}
	t.Cleanup(func() { cfg = originalConfig })
	originalJSON := jsonFlag
	jsonFlag = true
	t.Cleanup(func() { jsonFlag = originalJSON })
	t.Setenv("SYMDESK_SIDECAR", "")
	return vaultRoot
}

func datasetSubcommand(t *testing.T, name string) *cobra.Command {
	t.Helper()
	parent := newDatasetCmd()
	for _, command := range parent.Commands() {
		if command.Name() == name {
			return command
		}
	}
	t.Fatalf("dataset subcommand %q not found", name)
	return nil
}

func retentionSubcommand(t *testing.T, name string) *cobra.Command {
	t.Helper()
	parent := newRetentionCmd()
	for _, command := range parent.Commands() {
		if command.Name() == name {
			return command
		}
	}
	t.Fatalf("retention subcommand %q not found", name)
	return nil
}

func decodeCommandJSON(t *testing.T, output string) map[string]interface{} {
	t.Helper()
	var value map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &value); err != nil {
		t.Fatalf("decode command JSON %q: %v", output, err)
	}
	return value
}

func syncDatasetForCommandTest(t *testing.T, vaultRoot, slug, retentionRule string) {
	t.Helper()
	originalConfig := cfg
	cfg = &config.Config{Vault: vaultRoot}
	t.Cleanup(func() { cfg = originalConfig })
	originalJSON := jsonFlag
	jsonFlag = true
	t.Cleanup(func() { jsonFlag = originalJSON })

	command := datasetSubcommand(t, "sync")
	for name, value := range map[string]string{
		"rows":           `[{"identity":"row-1","values":{"name":"Example","score":4}}]`,
		"provenance":     `{"imported_at":"2026-01-01T00:00:00Z","source_name":"producer.csv","source_sha256":"sha-` + slug + `"}`,
		"identity-field": "id",
		"title":          "Dataset " + slug,
		"retention-rule": retentionRule,
	} {
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := captureCommandStdout(t, func() error {
		return command.RunE(command, []string{slug})
	}); err != nil {
		t.Fatalf("seed dataset %q: %v", slug, err)
	}
}

func TestDatasetCommandExecutionPaths(t *testing.T) {
	vaultRoot := isolatedCommandVault(t)

	syncCommand := datasetSubcommand(t, "sync")
	inlineFlags := map[string]string{
		"rows":           `[{"identity":"a","values":{"name":"Alice","score":3,"status":"open"}}]`,
		"provenance":     `{"imported_at":"2026-01-01T00:00:00Z","source_name":"inline.csv","source_sha256":"sha-inline"}`,
		"identity-field": "id",
		"schema":         `{"id":{"type":"text"},"name":{"type":"text"},"score":{"type":"number"},"status":{"type":"text"}}`,
		"title":          "People",
		"retention-rule": "people-retention",
	}
	for name, value := range inlineFlags {
		if err := syncCommand.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	output, err := captureCommandStdout(t, func() error {
		return syncCommand.RunE(syncCommand, []string{"people"})
	})
	if err != nil {
		t.Fatalf("inline dataset sync failed: %v", err)
	}
	syncResult := decodeCommandJSON(t, output)
	if syncResult["slug"] != "people" || syncResult["rows"] != float64(1) || syncResult["idempotent"] != false {
		t.Fatalf("unexpected inline sync result: %#v", syncResult)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "datasets", "people.md")); err != nil {
		t.Fatalf("inline sync did not write dataset handle: %v", err)
	}

	rowsPath := filepath.Join(t.TempDir(), "rows.json")
	provenancePath := filepath.Join(t.TempDir(), "provenance.json")
	schemaPath := filepath.Join(t.TempDir(), "schema.json")
	files := map[string]string{
		rowsPath:       `[{"identity":"a","values":{"name":"Alice","score":3,"status":"open"}},{"identity":"b","values":{"name":"Bob","score":7,"status":"closed"}}]`,
		provenancePath: `{"imported_at":"2026-02-01T00:00:00Z","source_name":"file.csv","source_sha256":"sha-file"}`,
		schemaPath:     `{"id":{"type":"text"},"name":{"type":"text"},"score":{"type":"number"},"status":{"type":"text"}}`,
	}
	for path, data := range files {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fileSync := datasetSubcommand(t, "sync")
	for name, value := range map[string]string{
		"rows":           rowsPath,
		"provenance":     provenancePath,
		"schema":         schemaPath,
		"identity-field": "id",
		"retention-rule": "people-retention",
	} {
		if err := fileSync.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	output, err = captureCommandStdout(t, func() error {
		return fileSync.RunE(fileSync, []string{"people"})
	})
	if err != nil {
		t.Fatalf("file dataset sync failed: %v", err)
	}
	syncResult = decodeCommandJSON(t, output)
	if syncResult["rows"] != float64(2) || syncResult["idempotent"] != false {
		t.Fatalf("unexpected file sync result: %#v", syncResult)
	}

	idempotentSync := datasetSubcommand(t, "sync")
	for name, value := range map[string]string{
		"rows":           rowsPath,
		"provenance":     provenancePath,
		"schema":         schemaPath,
		"identity-field": "id",
	} {
		if err := idempotentSync.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	output, err = captureCommandStdout(t, func() error {
		return idempotentSync.RunE(idempotentSync, []string{"people"})
	})
	if err != nil {
		t.Fatalf("idempotent dataset sync failed: %v", err)
	}
	syncResult = decodeCommandJSON(t, output)
	if syncResult["idempotent"] != true || syncResult["rows"] != float64(2) {
		t.Fatalf("unexpected idempotent sync result: %#v", syncResult)
	}

	listCommand := datasetSubcommand(t, "list")
	output, err = captureCommandStdout(t, func() error {
		return listCommand.RunE(listCommand, nil)
	})
	if err != nil {
		t.Fatalf("dataset list failed: %v", err)
	}
	var listed []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &listed); err != nil {
		t.Fatalf("decode dataset list: %v", err)
	}
	if len(listed) != 1 || listed[0]["slug"] != "people" || listed[0]["rows"] != float64(2) {
		t.Fatalf("unexpected dataset list: %#v", listed)
	}

	describeCommand := datasetSubcommand(t, "describe")
	output, err = captureCommandStdout(t, func() error {
		return describeCommand.RunE(describeCommand, []string{"people"})
	})
	if err != nil {
		t.Fatalf("dataset describe failed: %v", err)
	}
	description := decodeCommandJSON(t, output)
	if description["slug"] != "people" || description["title"] != "People" || description["retention_rule"] != "people-retention" {
		t.Fatalf("unexpected dataset description: %#v", description)
	}

	queryCommand := datasetSubcommand(t, "query")
	for name, value := range map[string]string{
		"columns":      "identity, name, score",
		"filters":      `[{"key":"score","operator":"gte","value":"5"}]`,
		"filter-group": `{"operator":"any","filters":[{"key":"status","operator":"equals","value":"closed"}]}`,
		"group-by":     "status",
		"aggregates":   `[{"function":"count","as":"rows"}]`,
		"limit":        "5001",
	} {
		if err := queryCommand.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	output, err = captureCommandStdout(t, func() error {
		return queryCommand.RunE(queryCommand, []string{"people"})
	})
	if err != nil {
		t.Fatalf("dataset aggregate query failed: %v", err)
	}
	queryResult := decodeCommandJSON(t, output)
	if queryResult["limit"] != float64(1000) || queryResult["total_rows"] != float64(1) || queryResult["returned_rows"] != float64(1) {
		t.Fatalf("unexpected capped aggregate query: %#v", queryResult)
	}

	defaultQuery := datasetSubcommand(t, "query")
	output, err = captureCommandStdout(t, func() error {
		return defaultQuery.RunE(defaultQuery, []string{"people"})
	})
	if err != nil {
		t.Fatalf("dataset default query failed: %v", err)
	}
	queryResult = decodeCommandJSON(t, output)
	if queryResult["limit"] != float64(10) || queryResult["total_rows"] != float64(2) {
		t.Fatalf("unexpected default query cap: %#v", queryResult)
	}

	badQuery := datasetSubcommand(t, "query")
	if err := badQuery.Flags().Set("filters", "["); err != nil {
		t.Fatal(err)
	}
	if _, err := captureCommandStdout(t, func() error {
		return badQuery.RunE(badQuery, []string{"people"})
	}); err == nil || !strings.Contains(err.Error(), "parse --filters") {
		t.Fatalf("malformed query JSON error = %v", err)
	}

	badSync := datasetSubcommand(t, "sync")
	for name, value := range map[string]string{
		"rows":           "[",
		"provenance":     inlineFlags["provenance"],
		"identity-field": "id",
	} {
		if err := badSync.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := captureCommandStdout(t, func() error {
		return badSync.RunE(badSync, []string{"broken"})
	}); err == nil || !strings.Contains(err.Error(), "parse --rows") {
		t.Fatalf("malformed sync JSON error = %v", err)
	}
}

func TestRetentionAcceptanceDatasetAndOrdinaryPaths(t *testing.T) {
	vaultRoot := isolatedCommandVault(t)
	syncDatasetForCommandTest(t, vaultRoot, "wrong", "actual-rule")

	wrongProposal := retention.Proposal{
		RunID:   "wrong-rule-run",
		Created: time.Now().UTC(),
		Status:  "pending",
		Items:   []retention.ProposalItem{{Path: "datasets/wrong.md", Title: "Dataset wrong", Action: retention.ActionTrash, RuleName: "wrong-rule"}},
	}
	if err := retention.WriteProposal(vaultRoot, wrongProposal); err != nil {
		t.Fatal(err)
	}
	acceptWrong := retentionSubcommand(t, "accept")
	output, err := captureCommandStdout(t, func() error {
		return acceptWrong.RunE(acceptWrong, []string{wrongProposal.RunID})
	})
	_ = output
	if err == nil || !strings.Contains(err.Error(), "retention rule") {
		t.Fatalf("mismatched dataset rule error = %v", err)
	}
	failedWrong, loadErr := retention.LoadProposal(vaultRoot, wrongProposal.RunID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if failedWrong.Status != retention.ProposalStatusFailed || failedWrong.Items[0].Failure == "" {
		t.Fatalf("mismatched retention rule was not persisted as retryable: %#v", failedWrong)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "datasets", "wrong.md")); err != nil {
		t.Fatalf("mismatched-rule dataset was purged: %v", err)
	}
	history, err := retention.LoadHistory(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 0 {
		t.Fatalf("mismatched dataset action was recorded in history: %#v", history)
	}

	syncDatasetForCommandTest(t, vaultRoot, "matching", "matching-rule")
	stateDB, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	matchingState, err := service.New(vaultRoot, stateDB).RetentionState("datasets/matching.md")
	if closeErr := stateDB.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	matchingProposal := retention.Proposal{
		RunID:   "matching-rule-run",
		Created: time.Now().UTC(),
		Status:  "pending",
		Items:   []retention.ProposalItem{{Path: "datasets/matching.md", Title: "Dataset matching", Action: retention.ActionTrash, RuleName: "matching-rule", Fingerprint: matchingState.Fingerprint}},
	}
	if err := retention.WriteProposal(vaultRoot, matchingProposal); err != nil {
		t.Fatal(err)
	}
	acceptMatching := retentionSubcommand(t, "accept")
	output, err = captureCommandStdout(t, func() error {
		return acceptMatching.RunE(acceptMatching, []string{matchingProposal.RunID})
	})
	if err != nil {
		t.Fatalf("accepting matching dataset rule failed: %v", err)
	}
	acceptedMatching := decodeCommandJSON(t, output)
	if acceptedMatching["acted"] != float64(1) || acceptedMatching["status"] != "accepted" {
		t.Fatalf("unexpected matching dataset acceptance: %#v", acceptedMatching)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "datasets", "matching.md")); !os.IsNotExist(err) {
		t.Fatalf("matching dataset handle still exists, stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vaultRoot, "datasets", "matching")); !os.IsNotExist(err) {
		t.Fatalf("matching dataset raw directory still exists, stat error = %v", err)
	}
	retryMatching := retentionSubcommand(t, "accept")
	output, err = captureCommandStdout(t, func() error {
		return retryMatching.RunE(retryMatching, []string{matchingProposal.RunID})
	})
	if err != nil {
		t.Fatalf("retrying accepted proposal failed: %v", err)
	}
	retryResult := decodeCommandJSON(t, output)
	if retryResult["acted"] != float64(0) || retryResult["status"] != retention.ProposalStatusAccepted {
		t.Fatalf("retry duplicated accepted action: %#v", retryResult)
	}
	history, err = retention.LoadHistory(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Path != "datasets/matching.md" {
		t.Fatalf("retry changed retention history: %#v", history)
	}

	notePath := filepath.Join(vaultRoot, "ordinary.md")
	noteData := []byte("---\ntitle: Ordinary\ncreated: \"2026-01-01T00:00:00Z\"\ndocument_date: \"2026-01-01\"\nstatus: open\n---\n\nBody\n")
	if err := os.WriteFile(notePath, noteData, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sidecar.OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := vault.ParseBytes("ordinary.md", noteData)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.IndexDocument(doc); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rulesPath := filepath.Join(vaultRoot, "retention-rules.yaml")
	if err := os.WriteFile(rulesPath, []byte("name: skip-dataset\nselector:\n  document_type: dataset\nperiod_days: 1\nreference_field: created\naction: trash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalSkipped := retentionSubcommand(t, "eval")
	if err := evalSkipped.Flags().Set("rules", rulesPath); err != nil {
		t.Fatal(err)
	}
	output, err = captureCommandStdout(t, func() error {
		return evalSkipped.RunE(evalSkipped, nil)
	})
	if err != nil {
		t.Fatalf("retention eval with non-matching rule failed: %v", err)
	}
	evalResult := decodeCommandJSON(t, output)
	if evalResult["item_count"] != float64(0) {
		t.Fatalf("wrong retention selector did not skip ordinary document: %#v", evalResult)
	}

	if err := os.WriteFile(rulesPath, []byte("name: ordinary-review\nselector:\n  status: open\nperiod_days: 1\nreference_field: created\naction: flag_review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	evalOrdinary := retentionSubcommand(t, "eval")
	if err := evalOrdinary.Flags().Set("rules", rulesPath); err != nil {
		t.Fatal(err)
	}
	output, err = captureCommandStdout(t, func() error {
		return evalOrdinary.RunE(evalOrdinary, nil)
	})
	if err != nil {
		t.Fatalf("ordinary retention eval failed: %v", err)
	}
	evalResult = decodeCommandJSON(t, output)
	if evalResult["item_count"] != float64(1) {
		t.Fatalf("ordinary retention item missing: %#v", evalResult)
	}
	var evalItems []map[string]interface{}
	if err := json.Unmarshal([]byte(mustJSONField(t, evalResult, "items")), &evalItems); err != nil {
		t.Fatal(err)
	}
	if len(evalItems) != 1 || evalItems[0]["path"] != "ordinary.md" || evalItems[0]["action"] != string(retention.ActionFlagReview) {
		t.Fatalf("unexpected ordinary retention proposal: %#v", evalItems)
	}
	ordinaryRunID := evalResult["run_id"].(string)
	acceptOrdinary := retentionSubcommand(t, "accept")
	if _, err := captureCommandStdout(t, func() error {
		return acceptOrdinary.RunE(acceptOrdinary, []string{ordinaryRunID})
	}); err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- notePath is created inside this isolated test vault.
	updated, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "status: \"needs_review\"") && !strings.Contains(string(updated), "status: needs_review") {
		t.Fatalf("ordinary retention action changed unexpectedly: %s", updated)
	}
}

func mustJSONField(t *testing.T, object map[string]interface{}, field string) string {
	t.Helper()
	value, ok := object[field]
	if !ok {
		t.Fatalf("JSON field %q missing from %#v", field, object)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestDatasetRetentionSlug(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{path: "datasets/people.md", want: "people", ok: true},
		{path: "datasets/people.md ", want: "people", ok: true},
		{path: "notes/people.md", ok: false},
		{path: "datasets/people", ok: false},
		{path: "datasets/nested/people.md", ok: false},
		{path: "datasets/.md", ok: false},
	}
	for _, test := range tests {
		got, ok := datasetRetentionSlug(test.path)
		if got != test.want || ok != test.ok {
			t.Errorf("datasetRetentionSlug(%q) = %q, %v; want %q, %v", test.path, got, ok, test.want, test.ok)
		}
	}

}

func syncExpiringDatasetForRetentionTest(t *testing.T, vaultRoot, slug, retentionRule string) {
	t.Helper()
	originalConfig := cfg
	cfg = &config.Config{Vault: vaultRoot}
	t.Cleanup(func() { cfg = originalConfig })
	originalJSON := jsonFlag
	jsonFlag = true
	t.Cleanup(func() { jsonFlag = originalJSON })

	command := datasetSubcommand(t, "sync")
	for name, value := range map[string]string{
		"rows":           `[{"identity":"row-1","values":{"id":"row-1","when":"2020-01-01"}}]`,
		"provenance":     `{"imported_at":"2026-01-01T00:00:00Z","source_name":"` + slug + `.csv","source_sha256":"sha-` + slug + `"}`,
		"schema":         `{"id":{"type":"text"},"when":{"type":"date"}}`,
		"identity-field": "id",
		"retention-rule": retentionRule,
		"title":          "Dataset " + slug,
	} {
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := captureCommandStdout(t, func() error { return command.RunE(command, []string{slug}) }); err != nil {
		t.Fatalf("seed expiring dataset %q: %v", slug, err)
	}
}

func TestRetentionEvalBindsDatasetRuleAndFingerprintAndRejectsStaleState(t *testing.T) {
	vaultRoot := isolatedCommandVault(t)
	syncExpiringDatasetForRetentionTest(t, vaultRoot, "orders", "dataset-retention")
	rulesPath := filepath.Join(vaultRoot, "retention-rules.yaml")
	if err := os.WriteFile(rulesPath, []byte("name: dataset-retention\nselector:\n  document_type: dataset\nperiod_days: 1\nreference_field: created\naction: trash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eval := retentionSubcommand(t, "eval")
	if err := eval.Flags().Set("rules", rulesPath); err != nil {
		t.Fatal(err)
	}
	output, err := captureCommandStdout(t, func() error { return eval.RunE(eval, nil) })
	if err != nil {
		t.Fatalf("dataset retention eval failed: %v", err)
	}
	result := decodeCommandJSON(t, output)
	if result["item_count"] != float64(1) {
		t.Fatalf("dataset was not selected by its exact retention rule: %#v", result)
	}
	var items []retention.ProposalItem
	if err := json.Unmarshal([]byte(mustJSONField(t, result, "items")), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].RuleName != "dataset-retention" || items[0].Fingerprint == "" {
		t.Fatalf("dataset proposal is not bound to rule and fingerprint: %#v", items)
	}
	runID := result["run_id"].(string)

	handlePath := filepath.Join(vaultRoot, "datasets", "orders.md")
	handle, err := os.ReadFile(handlePath)
	if err != nil {
		t.Fatal(err)
	}
	changedHandle := strings.Replace(string(handle), `retention_rule: dataset-retention`, `retention_rule: other-rule`, 1)
	if changedHandle == string(handle) {
		t.Fatal("dataset handle retention rule fixture was not found")
	}
	if err := os.WriteFile(handlePath, []byte(changedHandle), 0o600); err != nil {
		t.Fatal(err)
	}
	accept := retentionSubcommand(t, "accept")
	if _, err := captureCommandStdout(t, func() error { return accept.RunE(accept, []string{runID}) }); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("changed dataset policy did not produce stale failure: %v", err)
	}
	failed, err := retention.LoadProposal(vaultRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != retention.ProposalStatusFailed || failed.Items[0].Failure == "" {
		t.Fatalf("stale dataset proposal was not persisted as retryable: %#v", failed)
	}
	if _, err := os.Stat(handlePath); err != nil {
		t.Fatalf("stale dataset proposal changed active handle: %v", err)
	}
	if history, err := retention.LoadHistory(vaultRoot); err != nil || len(history) != 0 {
		t.Fatalf("stale dataset proposal wrote history: %#v %v", history, err)
	}

	if err := os.WriteFile(handlePath, handle, 0o600); err != nil {
		t.Fatal(err)
	}
	rawMatches, err := filepath.Glob(filepath.Join(vaultRoot, "datasets", "orders", "*.csv"))
	if err != nil || len(rawMatches) != 1 {
		t.Fatalf("find dataset raw CSV: %v (%v)", rawMatches, err)
	}
	rawPath := rawMatches[0]
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rawPath, append(append([]byte(nil), raw...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	accept = retentionSubcommand(t, "accept")
	if _, err := captureCommandStdout(t, func() error { return accept.RunE(accept, []string{runID}) }); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("changed dataset raw CSV did not produce stale failure: %v", err)
	}
	failed, err = retention.LoadProposal(vaultRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != retention.ProposalStatusFailed || failed.Items[0].Failure == "" {
		t.Fatalf("raw CSV stale proposal was not retryable: %#v", failed)
	}
	if _, err := os.Stat(handlePath); err != nil {
		t.Fatalf("raw CSV stale proposal changed active handle: %v", err)
	}
	if history, err := retention.LoadHistory(vaultRoot); err != nil || len(history) != 0 {
		t.Fatalf("raw CSV stale proposal wrote history: %#v %v", history, err)
	}
}

func TestRetentionAcceptResumesMixedDatasetProposalWithoutDuplicateHistory(t *testing.T) {
	vaultRoot := isolatedCommandVault(t)
	for _, slug := range []string{"alpha", "beta"} {
		syncExpiringDatasetForRetentionTest(t, vaultRoot, slug, "dataset-retention")
	}
	rulesPath := filepath.Join(vaultRoot, "retention-rules.yaml")
	if err := os.WriteFile(rulesPath, []byte("name: dataset-retention\nselector:\n  document_type: dataset\nperiod_days: 1\nreference_field: created\naction: trash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	eval := retentionSubcommand(t, "eval")
	if err := eval.Flags().Set("rules", rulesPath); err != nil {
		t.Fatal(err)
	}
	output, err := captureCommandStdout(t, func() error { return eval.RunE(eval, nil) })
	if err != nil {
		t.Fatalf("mixed dataset retention eval failed: %v", err)
	}
	result := decodeCommandJSON(t, output)
	if result["item_count"] != float64(2) {
		t.Fatalf("mixed dataset proposal item count = %#v", result)
	}
	runID := result["run_id"].(string)
	betaRaw, err := filepath.Glob(filepath.Join(vaultRoot, "datasets", "beta", "*.csv"))
	if err != nil || len(betaRaw) != 1 {
		t.Fatalf("find beta raw CSV: %v (%v)", betaRaw, err)
	}
	betaOriginal, err := os.ReadFile(betaRaw[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(betaRaw[0], append(append([]byte(nil), betaOriginal...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	accept := retentionSubcommand(t, "accept")
	if _, err := captureCommandStdout(t, func() error { return accept.RunE(accept, []string{runID}) }); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("mixed proposal did not report failed item: %v", err)
	}
	partial, err := retention.LoadProposal(vaultRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != retention.ProposalStatusPartial {
		t.Fatalf("mixed proposal status = %q, want partial", partial.Status)
	}
	accepted, failed := 0, 0
	for _, item := range partial.Items {
		switch {
		case item.Status == retention.ProposalItemStatusAccepted:
			accepted++
		case item.Failure != "":
			failed++
		}
	}
	if accepted != 1 || failed != 1 {
		t.Fatalf("mixed proposal item states accepted=%d failed=%d: %#v", accepted, failed, partial.Items)
	}
	history, err := retention.LoadHistory(vaultRoot)
	if err != nil || len(history) != 1 {
		t.Fatalf("mixed proposal history = %#v (%v), want one completed action", history, err)
	}

	if err := os.WriteFile(betaRaw[0], betaOriginal, 0o600); err != nil {
		t.Fatal(err)
	}
	accept = retentionSubcommand(t, "accept")
	if _, err := captureCommandStdout(t, func() error { return accept.RunE(accept, []string{runID}) }); err != nil {
		t.Fatalf("retrying restored mixed proposal failed: %v", err)
	}
	complete, err := retention.LoadProposal(vaultRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	if complete.Status != retention.ProposalStatusAccepted {
		t.Fatalf("restored mixed proposal status = %q", complete.Status)
	}
	history, err = retention.LoadHistory(vaultRoot)
	if err != nil || len(history) != 2 {
		t.Fatalf("restored mixed proposal history = %#v (%v), want two unique actions", history, err)
	}
	if history[0].ActionID == "" || history[1].ActionID == "" || history[0].ActionID == history[1].ActionID {
		t.Fatalf("mixed proposal history action IDs are not unique: %#v", history)
	}
}
