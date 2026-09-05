package dataset

import (
	"strings"
	"testing"
)

func TestNeedsPolicyMigrationRequiresBothPersistedFields(t *testing.T) {
	base := &Handle{Slug: "orders", Title: "Orders", Source: "datasets/orders/source.csv", Sensitivity: SensitivityInternal, RetentionRule: DefaultRetentionRule}
	encoded, err := base.Render()
	if err != nil {
		t.Fatal(err)
	}

	legacy := strings.ReplaceAll(string(encoded), "sensitivity: internal\n", "")
	legacy = strings.ReplaceAll(legacy, "retention_rule: default\n", "")
	needs, err := NeedsPolicyMigration([]byte(legacy))
	if err != nil || !needs {
		t.Fatalf("legacy policy metadata = needs %v, err %v; want migration", needs, err)
	}

	for name, data := range map[string]string{
		"missing sensitivity":    strings.Replace(string(encoded), "sensitivity: internal\n", "", 1),
		"missing retention_rule": strings.Replace(string(encoded), "retention_rule: default\n", "", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if needs, err := NeedsPolicyMigration([]byte(data)); err == nil || needs {
				t.Fatalf("partial policy = needs %v, err %v; want actionable error", needs, err)
			}
		})
	}
}

func TestValidateRetentionRuleReferenceRejectsUnicodeControlsAndSeparators(t *testing.T) {
	for _, control := range []rune{'\u0000', '\u0008', '\u001b', '\u007f', '\u0085', '\u009f', '\u200e'} {
		for _, value := range []string{"rule" + string(control), string(control) + "rule"} {
			if err := ValidateRetentionRuleReference(value); err == nil {
				t.Errorf("retention rule %q with control U+%04X was accepted", value, control)
			}
		}
	}
	for _, value := range []string{"rule/name", `rule\\name`} {
		if err := ValidateRetentionRuleReference(value); err == nil {
			t.Errorf("retention rule %q with path separator was accepted", value)
		}
	}
	if err := ValidateRetentionRuleReference("  financial-7y  "); err != nil {
		t.Fatalf("ordinary surrounding whitespace was rejected: %v", err)
	}
}
