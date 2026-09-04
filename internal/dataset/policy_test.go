package dataset

import (
	"strings"
	"testing"
)

func TestHandlePolicyFieldsAreMandatoryAndClosed(t *testing.T) {
	base := &Handle{Slug: "orders", Title: "Orders", Source: "datasets/orders/2026-01-01.csv", Sensitivity: SensitivityInternal, RetentionRule: "default"}
	if _, err := base.Render(); err != nil {
		t.Fatalf("valid handle rejected: %v", err)
	}
	for name, handle := range map[string]*Handle{
		"missing sensitivity":    {Slug: base.Slug, Title: base.Title, Source: base.Source, RetentionRule: base.RetentionRule},
		"invalid sensitivity":    {Slug: base.Slug, Title: base.Title, Source: base.Source, Sensitivity: "sensitive", RetentionRule: base.RetentionRule},
		"missing retention rule": {Slug: base.Slug, Title: base.Title, Source: base.Source, Sensitivity: base.Sensitivity},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := handle.Render(); err == nil {
				t.Fatal("malformed handle rendered successfully")
			}
		})
	}

	encoded, err := base.Render()
	if err != nil {
		t.Fatal(err)
	}
	for name, field := range map[string]string{
		"missing sensitivity":    "sensitivity",
		"invalid sensitivity":    "sensitivity",
		"missing retention rule": "retention_rule",
	} {
		t.Run("parse "+name, func(t *testing.T) {
			data := string(encoded)
			switch name {
			case "missing sensitivity":
				data = strings.Replace(data, "sensitivity: internal\n", "", 1)
			case "invalid sensitivity":
				data = strings.Replace(data, "sensitivity: internal", "sensitivity: secret", 1)
			case "missing retention rule":
				data = strings.Replace(data, "retention_rule: default\n", "", 1)
			}
			if _, err := ParseHandle("datasets/orders.md", []byte(data)); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("expected clear %s error, got %v", field, err)
			}
		})
	}
	legacy := strings.ReplaceAll(string(encoded), "sensitivity: internal\n", "")
	legacy = strings.ReplaceAll(legacy, "retention_rule: default\n", "")
	parsed, err := ParseHandle("datasets/orders.md", []byte(legacy))
	if err != nil {
		t.Fatalf("legacy policy-free handle was not read conservatively: %v", err)
	}
	if parsed.Sensitivity != DefaultSensitivity || parsed.RetentionRule != DefaultRetentionRule {
		t.Fatalf("legacy defaults = %q/%q, want %q/%q", parsed.Sensitivity, parsed.RetentionRule, DefaultSensitivity, DefaultRetentionRule)
	}
}
