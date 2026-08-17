package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// writeIndexedDoc writes a note and indexes it so the simhash lands in the
// sidecar.
func writeIndexedDoc(t *testing.T, svc *Service, name, body string) {
	t.Helper()
	path := filepath.Join(svc.VaultRoot, name)
	content := "---\ntitle: " + strings.TrimSuffix(name, ".md") + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	doc, err := vault.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.IndexDocument(doc); err != nil {
		t.Fatal(err)
	}
}

func TestSimilarAllFindsDuplicateGroups(t *testing.T) {
	svc := newTestService(t)

	// Two near-identical invoices + one distinct contract.
	bodyA := strings.Repeat("Invoice line item amount total net gross tax\n", 30)
	bodyB := strings.Repeat("Invoice line item amount total net gross tax\n", 30)
	bodyC := strings.Repeat("Contract clause party agrees term duration notice\n", 30)

	writeIndexedDoc(t, svc, "a.md", bodyA)
	writeIndexedDoc(t, svc, "b.md", bodyB)
	writeIndexedDoc(t, svc, "c.md", bodyC)

	groups, err := svc.SimilarAll(90)
	if err != nil {
		t.Fatal(err)
	}

	// a and b must be in one group; c must be alone (thus absent).
	if len(groups) != 1 {
		t.Fatalf("expected exactly 1 duplicate group, got %d: %#v", len(groups), groups)
	}
	paths := []string{groups[0].Path, groups[0].Members[0].Path}
	if !containsPath(paths, "a.md") || !containsPath(paths, "b.md") {
		t.Fatalf("expected a.md and b.md in group, got %#v", paths)
	}
	if containsPath(paths, "c.md") {
		t.Fatalf("c.md must not be in the duplicate group: %#v", paths)
	}
}

// The default threshold has to stay useful in both directions: a pair that
// differs by only a word or two is still reported, while documents sharing
// nothing but a frontmatter and heading skeleton are not (issue #452).
func TestSimilarAllDefaultThresholdSeparatesNearIdenticalFromSkeletonShared(t *testing.T) {
	svc := newTestService(t)

	skeleton := "## Summary\n\n## Details\n\n## Notes\n\n"
	invoice := strings.Repeat("Invoice line item amount total net gross tax due payable on receipt\n", 30)
	writeIndexedDoc(t, svc, "invoice-april.md", skeleton+invoice+"Period April, total 1200 EUR\n")
	writeIndexedDoc(t, svc, "invoice-may.md", skeleton+invoice+"Period May, total 1300 EUR\n")
	writeIndexedDoc(t, svc, "letter.md",
		skeleton+strings.Repeat("Dear neighbour thank you for watering the plants last weekend\n", 30))
	writeIndexedDoc(t, svc, "statement.md",
		skeleton+strings.Repeat("Bank statement balance carried forward interest credited quarterly\n", 30))

	// 0 exercises the fallback, which must resolve to the same default.
	for _, threshold := range []int{0, DefaultDuplicateThreshold} {
		groups, err := svc.SimilarAll(threshold)
		if err != nil {
			t.Fatal(err)
		}
		if len(groups) != 1 {
			t.Fatalf("threshold %d: expected exactly 1 group, got %d: %#v", threshold, len(groups), groups)
		}
		paths := []string{groups[0].Path}
		for _, m := range groups[0].Members {
			paths = append(paths, m.Path)
		}
		if !containsPath(paths, "invoice-april.md") || !containsPath(paths, "invoice-may.md") {
			t.Fatalf("threshold %d: expected both invoices grouped, got %#v", threshold, paths)
		}
		if containsPath(paths, "letter.md") || containsPath(paths, "statement.md") {
			t.Fatalf("threshold %d: unrelated documents must not be grouped, got %#v", threshold, paths)
		}
	}
}

func TestSimilarAllEmptyVaultReturnsNoGroups(t *testing.T) {
	svc := newTestService(t)
	groups, err := svc.SimilarAll(50)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("expected no groups in empty vault, got %#v", groups)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if strings.HasSuffix(p, want) {
			return true
		}
	}
	return false
}
