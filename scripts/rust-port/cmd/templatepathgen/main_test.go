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
	eval, skipped, _ := buildEval()
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

// TestRecordedOutputsAreHostIndependent guards the Windows byte check: no
// recorded output may have a ':' in its first path element (other than an
// `x:` volume prefix), because Windows filepath.Clean spells such a path with
// a leading `.\`.
func TestRecordedOutputsAreHostIndependent(t *testing.T) {
	_, _, hostDependent := buildEval()
	if hostDependent == 0 {
		t.Fatal("expected colon-led outputs to be withheld")
	}
	for _, item := range committedCases(t).Eval {
		if hostDependentOutput(item.Output) {
			t.Fatalf("host-dependent case %s (%q) was recorded", item.ID, item.Output)
		}
	}
	for _, output := range []string{"10:30 meeting", "ab:c/x", ":x/y", "2026-03-05T14:07:09/t"} {
		if !hostDependentOutput(output) {
			t.Fatalf("%q must be treated as host dependent", output)
		}
	}
	for _, output := range []string{"", "a/b:c", "/abs/10:30", "documents/_:x/2026", "plain", "_:x/unknown", "a:b/_/unknown", "C:"} {
		if hostDependentOutput(output) {
			t.Fatalf("%q must not be treated as host dependent", output)
		}
	}
}

// TestDescribeDriftNamesFirstDifferingCases proves the --check diagnostic is
// bounded, deterministic and points at the differing case.
func TestDescribeDriftNamesFirstDifferingCases(t *testing.T) {
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		t.Fatalf("find repository root: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(defaultOutput))) // #nosec G304 -- committed fixture
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if lines := describeDrift(content, content, maxDriftReports); len(lines) != 1 || !strings.Contains(lines[0], "decoded fixtures are equal") {
		t.Fatalf("identical fixtures reported %v", lines)
	}
	var doc document
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	doc.HostDependentSkipped++
	for i := range doc.Cases.Eval {
		doc.Cases.Eval[i].Output += "!"
	}
	doc.Cases.Compile = doc.Cases.Compile[:len(doc.Cases.Compile)-1]
	mutated, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines := describeDrift(content, mutated, maxDriftReports)
	if len(lines) != maxDriftReports {
		t.Fatalf("expected %d bounded lines, got %d: %v", maxDriftReports, len(lines), lines)
	}
	if !strings.Contains(lines[0], "host_dependent_skipped") || !strings.Contains(lines[1], "compile case count") ||
		!strings.Contains(lines[2], "eval case "+doc.Cases.Eval[0].ID) {
		t.Fatalf("unexpected diagnostic order: %v", lines)
	}
	if again := describeDrift(content, mutated, maxDriftReports); !reflect.DeepEqual(lines, again) {
		t.Fatal("diagnostic is not deterministic")
	}
}
