package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

func committedCases(t *testing.T) cases {
	t.Helper()
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		t.Fatalf("find repository root: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(defaultOutput))) // #nosec G304 -- committed fixture
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var doc document
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return doc.Cases
}

func freshCases() (cases, int) {
	eval, skipped := buildEval()
	return cases{Compile: buildCompile(), Eval: eval, Disambiguate: buildDisambiguate()}, skipped
}

// TestFixtureMatchesProductionPackage is the staleness guard: the production
// templatepath package must still produce every committed case, and a mutated
// copy of the committed cases must be detected.
func TestFixtureMatchesProductionPackage(t *testing.T) {
	committed := committedCases(t)
	fresh, _ := freshCases()
	if !reflect.DeepEqual(fresh, committed) {
		t.Fatal("committed template-path cases differ from the production package")
	}

	mutated := committedCases(t)
	mutated.Eval[0].Output += "x"
	if reflect.DeepEqual(fresh, mutated) {
		t.Fatal("mutated eval output was not detected")
	}
	mutated = committedCases(t)
	mutated.Compile[7].Error = ""
	if reflect.DeepEqual(fresh, mutated) {
		t.Fatal("mutated compile error was not detected")
	}
}

// TestMapOrderDependentStrftimeCasesAreWithheld proves the generator notices
// Go's random directive order: `%%a` on a Monday becomes `%Mon`, which `%M`
// may or may not rewrite depending on map iteration order.
func TestMapOrderDependentStrftimeCasesAreWithheld(t *testing.T) {
	fresh, skipped := freshCases()
	if skipped == 0 {
		t.Fatal("expected order-dependent strftime cases to be withheld")
	}
	for _, item := range fresh.Eval {
		if strings.Contains(item.Template, "%%a") && strings.Contains(item.Fields["document_date"], "2026-11-23") {
			t.Fatalf("order-dependent case %s was recorded", item.ID)
		}
	}
}
