package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/internal/ingest"
)

// These tools/call tests exercise the canonical ingest tools end to end
// through their handlers (issue #636). The ingest seams are overridden per
// test because TestMain already pointed them at inert doubles; no test here
// reaches the developer's real vault, archive or document store.

func TestIngestReocrToolEnvelope(t *testing.T) {
	ingest.ReprocessFunc = func(_ context.Context, _ ingest.Options, id int64) (*ingest.ReprocessResult, error) {
		return &ingest.ReprocessResult{DocumentID: id, JobID: 42, Status: "completed", OutputPath: "archive/doc.pdf"}, nil
	}
	ingest.ReprocessByArchivePathFunc = func(_ context.Context, _ ingest.Options, path string) (*ingest.ReprocessResult, error) {
		return &ingest.ReprocessResult{DocumentID: 7, JobID: 43, Status: "completed", OutputPath: path}, nil
	}
	defer func() {
		ingest.ReprocessFunc = ingest.Reprocess
		ingest.ReprocessByArchivePathFunc = ingest.ReprocessByArchivePath
	}()

	tool := newIngestReocrTool(&config.Config{Vault: "/test/vault"})

	t.Run("by document_id", func(t *testing.T) {
		out, err := tool.Handler(context.Background(), json.RawMessage(`{"document_id":5}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		if res["status"] != "completed" || res["document_id"] != int64(5) || res["job_id"] != int64(42) {
			t.Errorf("unexpected reocr envelope: %#v", res)
		}
		if res["schema_version"] != int(ingest.SchemaVersion) {
			t.Errorf("schema_version = %v, want %d", res["schema_version"], ingest.SchemaVersion)
		}
	})

	t.Run("by archive_path", func(t *testing.T) {
		out, err := tool.Handler(context.Background(), json.RawMessage(`{"archive_path":"/vault/originals/doc.pdf"}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		if res["document_id"] != int64(7) || res["output_path"] != "/vault/originals/doc.pdf" {
			t.Errorf("unexpected reocr envelope: %#v", res)
		}
	})

	t.Run("legacy source alias", func(t *testing.T) {
		out, err := tool.Handler(context.Background(), json.RawMessage(`{"source":"/vault/originals/doc.pdf"}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		if res["document_id"] != int64(7) {
			t.Errorf("source alias did not route to archive path: %#v", res)
		}
	})

	t.Run("conflicting selectors rejected", func(t *testing.T) {
		if _, err := tool.Handler(context.Background(), json.RawMessage(`{"document_id":1,"archive_path":"/x.pdf"}`)); err == nil {
			t.Error("expected error for conflicting selectors")
		}
	})
}

func TestIngestReocrToolFailureEnvelope(t *testing.T) {
	ingest.ReprocessFunc = func(_ context.Context, _ ingest.Options, id int64) (*ingest.ReprocessResult, error) {
		return nil, ingest.ErrDocumentNotFound
	}
	defer func() { ingest.ReprocessFunc = ingest.Reprocess }()

	tool := newIngestReocrTool(&config.Config{Vault: "/test/vault"})
	out, err := tool.Handler(context.Background(), json.RawMessage(`{"document_id":9999}`))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["status"] != "failed" {
		t.Errorf("status = %v, want failed", res["status"])
	}
	errObj, ok := res["error"].(map[string]string)
	if !ok || errObj["code"] != "not_found" {
		t.Errorf("error object = %#v, want code not_found", res["error"])
	}
}

func TestRulesToolsCRUD(t *testing.T) {
	origRules, origAdd, origUpdate, origDelete := ingest.RulesFunc, ingest.AddRuleFunc, ingest.UpdateRuleFunc, ingest.DeleteRuleFunc
	ingest.RulesFunc = func(_ context.Context, _ ingest.Options) ([]ingest.Rule, error) {
		return []ingest.Rule{{ID: 3, Pattern: "invoice", Kind: "category", Value: "Invoices", CreatedAt: "2026-08-27T00:00:00Z"}}, nil
	}
	ingest.AddRuleFunc = func(_ context.Context, _ ingest.Options, pattern, kind, value string) (*ingest.Rule, error) {
		return &ingest.Rule{ID: 3, Pattern: pattern, Kind: kind, Value: value, CreatedAt: "2026-08-27T00:00:00Z"}, nil
	}
	ingest.UpdateRuleFunc = func(_ context.Context, _ ingest.Options, id int64, pattern, kind, value string) (*ingest.Rule, error) {
		return &ingest.Rule{ID: id, Pattern: pattern, Kind: kind, Value: value, CreatedAt: "2026-08-27T00:00:00Z"}, nil
	}
	ingest.DeleteRuleFunc = func(_ context.Context, _ ingest.Options, id int64) error {
		if id == 0 {
			return errors.New("no rule")
		}
		return nil
	}
	defer func() {
		ingest.RulesFunc, ingest.AddRuleFunc, ingest.UpdateRuleFunc, ingest.DeleteRuleFunc = origRules, origAdd, origUpdate, origDelete
	}()

	cfg := &config.Config{Vault: "/test/vault"}

	t.Run("list", func(t *testing.T) {
		out, err := newRulesListTool(cfg).Handler(context.Background(), json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		rules, ok := res["rules"].([]ingest.Rule)
		if !ok || len(rules) != 1 || rules[0].Pattern != "invoice" {
			t.Errorf("unexpected rules list: %#v", res)
		}
	})

	t.Run("add", func(t *testing.T) {
		out, err := newRulesAddTool(cfg).Handler(context.Background(), json.RawMessage(`{"pattern":"invoice","kind":"category","value":"Invoices"}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		rule, ok := res["rule"].(*ingest.Rule)
		if !ok || rule.ID != 3 || rule.Value != "Invoices" {
			t.Errorf("unexpected add_rule response: %#v", res)
		}
	})

	t.Run("update", func(t *testing.T) {
		out, err := newRulesUpdateTool(cfg).Handler(context.Background(), json.RawMessage(`{"id":3,"pattern":"rechnung","kind":"category","value":"Bills"}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		rule, ok := res["rule"].(*ingest.Rule)
		if !ok || rule.ID != 3 || rule.Value != "Bills" {
			t.Errorf("unexpected update response: %#v", res)
		}
	})

	t.Run("delete", func(t *testing.T) {
		out, err := newRulesDeleteTool(cfg).Handler(context.Background(), json.RawMessage(`{"id":3}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		if res["id"] != int64(3) || res["deleted"] != true {
			t.Errorf("unexpected delete response: %#v", res)
		}
	})
}

func TestSplitPDFTool(t *testing.T) {
	tool := newSplitPDFTool()

	t.Run("missing args rejected", func(t *testing.T) {
		if _, err := tool.Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Error("expected error for missing args")
		}
	})

	t.Run("clear message when poppler absent", func(t *testing.T) {
		if ingest.HasPopplerSplit() {
			t.Skip("poppler present; absence path covered only when it is not")
		}
		_, err := tool.Handler(context.Background(), json.RawMessage(`{"input":"/x.pdf","at":"2","output_dir":"/tmp/out"}`))
		if err == nil || !strings.Contains(err.Error(), "Poppler") {
			t.Errorf("expected a clear Poppler-required error, got %v", err)
		}
	})

	t.Run("splits through the seam", func(t *testing.T) {
		if !ingest.HasPopplerSplit() {
			t.Skip("poppler absent; success path needs the probe to pass")
		}
		orig := ingest.SplitPDFFunc
		ingest.SplitPDFFunc = func(_ context.Context, input, at, out string) ([]string, error) {
			return []string{out + "/part-1.pdf", out + "/part-2.pdf"}, nil
		}
		defer func() { ingest.SplitPDFFunc = orig }()

		out, err := tool.Handler(context.Background(), json.RawMessage(`{"input":"/vault/scan.pdf","at":"2,4","output_dir":"/tmp/parts"}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		parts, ok := res["parts"].([]string)
		if !ok || len(parts) != 2 || parts[0] != "/tmp/parts/part-1.pdf" {
			t.Errorf("unexpected split response: %#v", res)
		}
	})
}

// TestMergeRotatePDFTools exercises the canonical merge/rotate handlers
// through their seams (issue #637). No real PDF tooling is invoked.
func TestMergeRotatePDFTools(t *testing.T) {
	origMerge, origRotate := ingest.MergePDFsFunc, ingest.RotatePDFFunc
	ingest.MergePDFsFunc = func(_ context.Context, inputs []string, output string) error {
		if len(inputs) < 2 {
			return errors.New("need at least two inputs")
		}
		return nil
	}
	ingest.RotatePDFFunc = func(_ context.Context, input, output string, degrees int, pages string) error {
		if input == "" || output == "" {
			return errors.New("input/output required")
		}
		return nil
	}
	defer func() { ingest.MergePDFsFunc, ingest.RotatePDFFunc = origMerge, origRotate }()

	t.Run("merge happy path", func(t *testing.T) {
		out, err := newMergePDFTool().Handler(context.Background(), json.RawMessage(`{"inputs":["/a.pdf","/b.pdf"],"output":"/merged.pdf"}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		if res["output"] != "/merged.pdf" {
			t.Errorf("unexpected merge response: %#v", res)
		}
	})

	t.Run("merge requires at least two inputs", func(t *testing.T) {
		if _, err := newMergePDFTool().Handler(context.Background(), json.RawMessage(`{"inputs":["/a.pdf"],"output":"/m.pdf"}`)); err == nil {
			t.Error("expected error for single input")
		}
		if _, err := newMergePDFTool().Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Error("expected error for empty args")
		}
	})

	t.Run("rotate happy path", func(t *testing.T) {
		out, err := newRotatePDFTool().Handler(context.Background(), json.RawMessage(`{"input":"/a.pdf","output":"/rot.pdf","degrees":90,"pages":"1-3"}`))
		if err != nil {
			t.Fatal(err)
		}
		res := out.(map[string]any)
		if res["output"] != "/rot.pdf" {
			t.Errorf("unexpected rotate response: %#v", res)
		}
	})

	t.Run("rotate rejects missing args", func(t *testing.T) {
		if _, err := newRotatePDFTool().Handler(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Error("expected error for empty args")
		}
	})
}

// TestIngestAliasesDelegateToCanonical verifies the legacy names for the new
// ingest tools share the exact handler of their canonical tool, so tools/call
// under the historical name behaves identically (acceptance criterion for
// #636). This complements the generic delegation test in aliases_test.go,
// which covers the full alias set through the failing-factory contract.
func TestIngestAliasesDelegateToCanonical(t *testing.T) {
	registry := NewRegistry(testRegistryOptions())
	for _, want := range []struct {
		alias     string
		canonical string
	}{
		{"reocr", "desk_ingest_reocr"},
		{"list_rules", "desk_rules_list"},
		{"add_rule", "desk_rules_add"},
		{"delete_rule", "desk_rules_delete"},
		{"split_pdf", "desk_split_pdf"},
		{"merge_pdf", "desk_merge_pdf"},
		{"rotate_pdf", "desk_rotate_pdf"},
	} {
		alias, ok := registry.Lookup(want.alias)
		if !ok {
			t.Fatalf("alias %q missing", want.alias)
		}
		canonical, ok := registry.Lookup(want.canonical)
		if !ok {
			t.Fatalf("canonical %q missing", want.canonical)
		}
		// Both tools are registered read-only or mutating as one unit; calling
		// with {} yields an identical validation error when the canonical
		// requires arguments, or the same seam result otherwise.
		ctx := context.Background()
		aliasOut, aliasErr := alias.Handler(ctx, json.RawMessage(`{}`))
		canonOut, canonErr := canonical.Handler(ctx, json.RawMessage(`{}`))
		if aliasErr == nil || canonErr == nil {
			t.Fatalf("alias %q and canonical %q must both fail on {} (validation or isolated seam)", want.alias, want.canonical)
		}
		if aliasErr.Error() != canonErr.Error() {
			t.Errorf("alias %q error %q != canonical %q", want.alias, aliasErr, canonErr)
		}
		if aliasOut != nil || canonOut != nil {
			t.Errorf("alias %q and canonical %q must both return nil output on error", want.alias, want.canonical)
		}
	}
}
