package inventory

import "testing"

func TestProductionContractInputExcludesTestsAndFixtures(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"internal/vault/vault.go", true},
		{"cmd/symdesk/main.go", true},
		{"internal/vault/vault_test.go", false},
		{"internal/ingest/internal/notionimport/testdata/fixture/page.md", false},
		{"", false},
	}
	for _, test := range tests {
		if got := isProductionContractInput(test.path); got != test.want {
			t.Errorf("isProductionContractInput(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}
