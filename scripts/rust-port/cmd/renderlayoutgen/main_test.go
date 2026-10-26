package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := fixtureoracle.FindRepositoryRoot(".")
	if err != nil {
		t.Fatalf("find repository root: %v", err)
	}
	return root
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test reads committed fixtures or its own temp files
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func defaultOptions(t *testing.T, check bool) options {
	t.Helper()
	return options{
		output:  filepath.Join(repoRoot(t), filepath.FromSlash(defaultOutput)),
		check:   check,
		commit:  fixtureoracle.Defaults().Commit,
		release: fixtureoracle.Defaults().Release,
	}
}

func newBuilder() *builder {
	return &builder{seen: map[string]bool{}, ids: map[string]bool{}}
}

// TestGenerateCheckModeMatchesCommittedFixture is the staleness guard: the
// in-process generator must reproduce the committed fixture byte for byte.
func TestGenerateCheckModeMatchesCommittedFixture(t *testing.T) {
	message, err := generate(defaultOptions(t, true))
	if err != nil {
		t.Fatalf("committed layout fixture is stale: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render layout fixture (") {
		t.Fatalf("unexpected check message %q", message)
	}
}

// TestGenerateWritesByteIdenticalOutputs regenerates into a scratch directory,
// compares with the committed file, then proves check mode rejects a
// corrupted and a missing fixture.
func TestGenerateWritesByteIdenticalOutputs(t *testing.T) {
	dir := t.TempDir()
	opts := defaultOptions(t, false)
	committed := readFile(t, opts.output)
	opts.output = filepath.Join(dir, "nested", "layout.json")

	message, err := generate(opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render layout fixture generated (") {
		t.Fatalf("unexpected generate message %q", message)
	}
	got := readFile(t, opts.output)
	if !bytes.Equal(got, committed) {
		t.Fatal("regenerated fixture differs from the committed fixture")
	}

	check := opts
	check.check = true
	if _, err := generate(check); err != nil {
		t.Fatalf("check on freshly generated file: %v", err)
	}
	mutated := append([]byte(nil), got...)
	mutated[len(mutated)/2] ^= 0x01
	if err := os.WriteFile(opts.output, mutated, 0o600); err != nil { // #nosec G703 -- path is inside t.TempDir()
		t.Fatal(err)
	}
	if _, err := generate(check); err == nil || !strings.Contains(err.Error(), "fixture is stale") {
		t.Fatalf("corrupted fixture accepted by check mode: %v", err)
	}
	missing := check
	missing.output = filepath.Join(dir, "absent.json")
	if _, err := generate(missing); err == nil || !strings.Contains(err.Error(), "fixture is stale") {
		t.Fatalf("missing fixture accepted by check mode: %v", err)
	}
}

func TestGenerateRejectsUnknownOracle(t *testing.T) {
	opts := defaultOptions(t, true)
	opts.commit = strings.Repeat("0", 40)
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "verify selected fixture source") {
		t.Fatalf("unknown oracle commit accepted: %v", err)
	}
}

func TestGenerateReportsUnwritableOutput(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := defaultOptions(t, false)
	opts.output = filepath.Join(blocker, "layout.json")
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "create directory") {
		t.Fatalf("writing below a regular file must fail: %v", err)
	}
	opts.output = dir // a directory cannot be written as a file
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "write ") {
		t.Fatalf("writing onto a directory must fail: %v", err)
	}
}

// TestFixtureShapeAndHostIndependence pins the corpus shape and proves the
// committed bytes carry no host data.
func TestFixtureShapeAndHostIndependence(t *testing.T) {
	data := readFile(t, defaultOptions(t, true).output)
	if bytes.Contains(data, []byte("\r")) {
		t.Fatal("fixture contains a carriage return")
	}
	root := repoRoot(t)
	for _, forbidden := range []string{root, filepath.ToSlash(root), "/Users/", "/private/var/", "\\Users\\"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("fixture leaks host path material %q", forbidden)
		}
	}
	if !bytes.HasSuffix(data, []byte("}\n")) {
		t.Fatal("fixture must end with a single LF")
	}
	var parsed struct {
		SchemaVersion int      `json:"schema_version"`
		Entries       []string `json:"entries"`
		Withheld      withheld `json:"withheld"`
		Cases         []testCase
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	if parsed.SchemaVersion != 1 || len(parsed.Entries) != 2 {
		t.Fatalf("unexpected header: %+v", parsed)
	}
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, c := range parsed.Cases {
		counts[c.Entry]++
		if ids[c.ID] {
			t.Fatalf("duplicate id %s", c.ID)
		}
		ids[c.ID] = true
		if (c.Expected == "") == (c.Error == "") {
			t.Fatalf("%s: exactly one of expected and error must be set", c.ID)
		}
		if c.Expected != "" && !json.Valid([]byte(c.Expected)) {
			t.Fatalf("%s: expected is not JSON: %q", c.ID, c.Expected)
		}
	}
	for _, entry := range parsed.Entries {
		if counts[entry] < 50 {
			t.Fatalf("entry %s has too few cases: %d", entry, counts[entry])
		}
	}
}

func TestCorporaAreDeterministic(t *testing.T) {
	first, second := newBuilder(), newBuilder()
	first.handCorpus()
	first.randomCorpus()
	second.handCorpus()
	second.randomCorpus()
	if first.err != nil || second.err != nil {
		t.Fatalf("corpus failed: %v / %v", first.err, second.err)
	}
	if len(first.cases) == 0 || len(first.cases) != len(second.cases) {
		t.Fatalf("corpus not deterministic: %d vs %d", len(first.cases), len(second.cases))
	}
	for i := range first.cases {
		a, _ := json.Marshal(first.cases[i])
		b, _ := json.Marshal(second.cases[i])
		if !bytes.Equal(a, b) {
			t.Fatalf("case %d differs between runs", i)
		}
	}
}

func TestMinedCorpus(t *testing.T) {
	b := newBuilder()
	if err := b.minedCorpus(repoRoot(t)); err != nil || b.err != nil {
		t.Fatalf("mined corpus: %v / %v", err, b.err)
	}
	if len(b.cases) == 0 || b.withheld.UnparsableSources == 0 {
		t.Fatalf("mined corpus is empty or parsed everything: %d cases, %+v", len(b.cases), b.withheld)
	}
	for _, c := range b.cases {
		if !strings.HasPrefix(c.ID, "mined-") || c.Mode != "source" {
			t.Fatalf("unexpected mined case %+v", c)
		}
	}
	if err := newBuilder().minedCorpus(t.TempDir()); err == nil {
		t.Fatal("mined corpus must fail when the test sources are missing")
	}
}

func TestBuilderGuards(t *testing.T) {
	b := newBuilder()
	doc := `{"kind":"graph","nodes":[{"id":"a"},{"id":"b"}],"edges":[{"from":"a","to":"b"}]}`
	b.add("a", "layered", "ir", doc)
	b.add("a-again", "layered", "ir", doc) // same content: deduplicated
	if len(b.cases) != 1 || b.err != nil {
		t.Fatalf("dedupe failed: %d cases, err %v", len(b.cases), b.err)
	}
	b.add("a", "layered", "ir", `{"kind":"graph","nodes":[{"id":"z"}]}`)
	if b.err == nil || !strings.Contains(b.err.Error(), "duplicate case id") {
		t.Fatalf("duplicate id accepted: %v", b.err)
	}
	first := b.err
	b.fail("later failure")
	if b.err != first {
		t.Fatal("the first error must stick")
	}

	bad := newBuilder()
	bad.add("bad-utf8", "layered", "ir", "\xff")
	if bad.err == nil || !strings.Contains(bad.err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 accepted: %v", bad.err)
	}
	undecodable := newBuilder()
	undecodable.add("bad-ir", "layered", "ir", `{"kind":"graph","bogus":1}`)
	if undecodable.err == nil {
		t.Fatal("an IR input with an unknown field must fail generation")
	}
	skipped := newBuilder()
	skipped.add("not-a-diagram", "layered", "source", "just some prose")
	if len(skipped.cases) != 0 || skipped.withheld.UnparsableSources != 1 || skipped.err != nil {
		t.Fatalf("unparsable source must be counted, not frozen: %+v %v", skipped.withheld, skipped.err)
	}
	circle := newBuilder()
	circle.add("circle", "layered", "ir", `{"kind":"graph","nodes":[{"id":"a","shape":"circle"}]}`)
	circle.add("circle-sized", "layered", "ir", `{"kind":"graph","nodes":[{"id":"a","shape":"circle","width":50,"height":50}]}`)
	if len(circle.cases) != 1 || circle.withheld.CircleHypotAmbiguous != 1 {
		t.Fatalf("circle sizing: %d cases, %+v", len(circle.cases), circle.withheld)
	}
	unknown := newBuilder()
	unknown.add("unknown-entry", "nonsense", "ir", doc)
	if unknown.err == nil {
		t.Fatal("an unknown entry must fail generation")
	}
}

func TestRecordedBytesComeFromTheGoEngines(t *testing.T) {
	b := newBuilder()
	b.graph("g", &ir.Diagram{Nodes: nodes("a", "b"), Edges: edges("a", "b")})
	b.add("s", "sequence", "ir", `{"kind":"sequence","custom":{"actors":[{"ID":"A"},{"ID":"B"}],"messages":[{}]}}`)
	b.add("e", "sequence", "ir", `{"kind":"graph"}`)
	if b.err != nil || len(b.cases) != 3 {
		t.Fatalf("helpers failed: %d cases, %v", len(b.cases), b.err)
	}
	if !strings.HasPrefix(b.cases[0].Expected, `{"Nodes":{"a":{"X":`) {
		t.Fatalf("unexpected layered recording %q", b.cases[0].Expected)
	}
	if b.cases[1].Expected != `{"ActorX":{"A":120,"B":300},"MessageY":[140],"Width":420,"Height":268}` {
		t.Fatalf("unexpected sequence recording %q", b.cases[1].Expected)
	}
	if b.cases[2].Error != "sequence layout requires sequence kind, got *ir.Diagram" {
		t.Fatalf("unexpected sequence error %q", b.cases[2].Error)
	}
}

func TestRenderRejectsUnexpectedHeaderShape(t *testing.T) {
	out, err := render(fixture{SchemaVersion: 1, Cases: []testCase{{ID: "x", Entry: "layered", Mode: "ir", Input: "{}", Expected: "{}"}}})
	if err != nil || !bytes.HasSuffix(out, []byte("  ]\n}\n")) || !bytes.Contains(out, []byte("\"cases\": [\n")) {
		t.Fatalf("unexpected render output (%v):\n%s", err, out)
	}
	var parsed struct {
		Cases []testCase `json:"cases"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed.Cases) != 1 {
		t.Fatalf("rendered fixture does not round-trip: %v", err)
	}
}
