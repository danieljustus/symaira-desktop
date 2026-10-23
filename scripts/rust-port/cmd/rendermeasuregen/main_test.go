package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/measure"
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

func newBuilder(t *testing.T) *builder {
	t.Helper()
	m, err := measure.NewMeasurer()
	if err != nil {
		t.Fatal(err)
	}
	return &builder{m: m, seen: map[string]bool{}, ids: map[string]bool{}}
}

// TestGenerateCheckModeMatchesCommittedFixture is the staleness guard: the
// in-process generator must reproduce the committed fixture byte for byte.
func TestGenerateCheckModeMatchesCommittedFixture(t *testing.T) {
	message, err := generate(defaultOptions(t, true))
	if err != nil {
		t.Fatalf("committed text measure fixture is stale: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render text measure fixture (") {
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
	opts.output = filepath.Join(dir, "nested", "text-measure.json")

	message, err := generate(opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render text measure fixture generated (") {
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
	opts.output = filepath.Join(blocker, "text-measure.json")
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "create directory") {
		t.Fatalf("writing below a regular file must fail: %v", err)
	}
	opts.output = dir // a directory cannot be written as a file
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "write ") {
		t.Fatalf("writing onto a directory must fail: %v", err)
	}
}

// TestFixtureShapeAndHostIndependence pins the corpus size per entry and
// proves the committed bytes carry no host data: no CRLF, no absolute or
// Windows paths, no repository root.
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
	var parsed fixture
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("fixture is not valid JSON: %v", err)
	}
	if parsed.SchemaVersion != 1 || parsed.Fonts.MeasurerHash == "" || parsed.Fonts.VersionKey == "" {
		t.Fatalf("unexpected header: %+v", parsed)
	}
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, c := range parsed.Cases {
		counts[c.Entry]++
		if ids[c.ID] || c.Expected == "" {
			t.Fatalf("duplicate id or empty expectation: %+v", c)
		}
		ids[c.ID] = true
		if !json.Valid([]byte(c.Expected)) {
			t.Fatalf("%s: expected is not JSON: %q", c.ID, c.Expected)
		}
	}
	for _, entry := range parsed.Entries {
		if counts[entry] == 0 {
			t.Fatalf("entry %s has no cases", entry)
		}
	}
	if len(parsed.Cases) < 50 {
		t.Fatalf("corpus too small: %d", len(parsed.Cases))
	}
	// Whole scalar range: 0x110000 minus the 2048 surrogates, two weights.
	if want := (0x110000 - 0x800) / sweepChunk * 2; counts["advance_sweep"] != want {
		t.Fatalf("sweep cases = %d, want %d", counts["advance_sweep"], want)
	}
}

func TestCorporaAreDeterministic(t *testing.T) {
	first, second := newBuilder(t), newBuilder(t)
	first.handCorpus()
	second.handCorpus()
	if first.err != nil || second.err != nil {
		t.Fatalf("hand corpus failed: %v / %v", first.err, second.err)
	}
	if len(first.cases) == 0 || len(first.cases) != len(second.cases) {
		t.Fatalf("hand corpus not deterministic: %d vs %d", len(first.cases), len(second.cases))
	}
	for i := range first.cases {
		a, _ := json.Marshal(first.cases[i])
		b, _ := json.Marshal(second.cases[i])
		if !bytes.Equal(a, b) {
			t.Fatalf("case %d differs between runs", i)
		}
	}
}

func TestMinedCorpusAndCircles(t *testing.T) {
	b := newBuilder(t)
	if err := b.minedCorpus(repoRoot(t)); err != nil || b.err != nil {
		t.Fatalf("mined corpus: %v / %v", err, b.err)
	}
	if len(b.cases) == 0 {
		t.Fatal("mined corpus is empty")
	}
	for _, c := range b.cases {
		if !strings.HasPrefix(c.ID, "mined-") {
			t.Fatalf("unexpected mined id %s", c.ID)
		}
	}
	if err := newBuilder(t).minedCorpus(t.TempDir()); err == nil {
		t.Fatal("mined corpus must fail when the test sources are missing")
	}

	circles := newBuilder(t)
	if err := circles.circleCorpus(); err != nil || circles.err != nil {
		t.Fatalf("circle corpus: %v / %v", err, circles.err)
	}
	for _, c := range circles.cases {
		if c.Shape != "circle" || c.Entry != "measure_node" {
			t.Fatalf("unexpected circle case %+v", c)
		}
		var res nodeResult
		if err := json.Unmarshal([]byte(c.Expected), &res); err != nil || res.Width != res.Height {
			t.Fatalf("%s: circle must be square: %v %+v", c.ID, err, res)
		}
		if res.Width != math.Trunc(res.Width) {
			t.Fatalf("%s: circle size %v is not an exactly representable integer", c.ID, res.Width)
		}
	}
}

func TestCanonicalHypotMatchesExactTriples(t *testing.T) {
	for _, tc := range [][3]float64{{3, 4, 5}, {4, 3, 5}, {-5, 12, 13}, {0, 7, 7}, {7, 0, 7}, {0, 0, 0}} {
		if got := canonicalHypot(tc[0], tc[1]); got != tc[2] {
			t.Fatalf("canonicalHypot(%v,%v) = %v, want %v", tc[0], tc[1], got, tc[2])
		}
	}
}

func TestBuilderGuards(t *testing.T) {
	b := newBuilder(t)
	b.add(testCase{ID: "a", Entry: "measure_text", Text: "ok", Expected: "[]"})
	b.add(testCase{ID: "a-again", Entry: "measure_text", Text: "ok", Expected: "[]"}) // same content: deduplicated
	if len(b.cases) != 1 || b.err != nil {
		t.Fatalf("dedupe failed: %d cases, err %v", len(b.cases), b.err)
	}
	b.add(testCase{ID: "a", Entry: "measure_text", Text: "other", Expected: "[]"})
	if b.err == nil || !strings.Contains(b.err.Error(), "duplicate case id") {
		t.Fatalf("duplicate id accepted: %v", b.err)
	}
	first := b.err
	b.fail("later failure")
	if b.err != first {
		t.Fatal("the first error must stick")
	}

	bad := newBuilder(t)
	bad.add(testCase{ID: "bad-utf8", Entry: "measure_text", Text: "\xff", Expected: "[]"})
	if bad.err == nil || !strings.Contains(bad.err.Error(), "valid UTF-8") {
		t.Fatalf("invalid UTF-8 accepted: %v", bad.err)
	}

	nan := newBuilder(t)
	if got := marshal(nan, "nan", math.NaN()); got != "" || nan.err == nil {
		t.Fatalf("NaN must fail to marshal, got %q err %v", got, nan.err)
	}
	keyed := newBuilder(t)
	keyed.add(testCase{ID: "nan-key", Entry: "measure_text", Size: math.NaN(), Expected: "[]"})
	if keyed.err == nil {
		t.Fatal("a NaN size cannot be a fixture key")
	}
}

func TestEntryHelpersRecordGoBytes(t *testing.T) {
	b := newBuilder(t)
	b.measureText("t", "Ag", 16, "regular")
	b.measureDetailed("d", "Ag", 16, "bold")
	b.breakLines("b", "alpha beta gamma", 60, 14, "regular")
	b.measureNode("n", "Label", "diamond", nodeOptions{FontSize: 14, FontWeight: "regular", PaddingX: 20, PaddingY: 14, MinWidth: 80, MinHeight: 40, MaxWidth: 240})
	b.normalize("w", " BOLD ")
	if b.err != nil || len(b.cases) != 5 {
		t.Fatalf("helpers failed: %d cases, %v", len(b.cases), b.err)
	}
	if b.cases[4].Expected != `"bold"` {
		t.Fatalf("normalize recorded %q", b.cases[4].Expected)
	}
	if !strings.HasPrefix(b.cases[0].Expected, "[") || !strings.Contains(b.cases[1].Expected, "glyph_advances") {
		t.Fatalf("unexpected recordings: %v", b.cases)
	}
	if sizeSlug(-0.4) != "m0_4" || sizeSlug(14) != "14" {
		t.Fatalf("unexpected size slugs %q %q", sizeSlug(-0.4), sizeSlug(14))
	}
}

func TestRenderRejectsUnexpectedHeaderShape(t *testing.T) {
	out, err := render(fixture{SchemaVersion: 1, Cases: []testCase{{ID: "x", Entry: "measure_text", Expected: "[0,0]"}}})
	if err != nil || !bytes.HasSuffix(out, []byte("  ]\n}\n")) || !bytes.Contains(out, []byte("\"cases\": [\n")) {
		t.Fatalf("unexpected render output (%v):\n%s", err, out)
	}
	var parsed fixture
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed.Cases) != 1 {
		t.Fatalf("rendered fixture does not round-trip: %v", err)
	}
	if _, err := render(fixture{Cases: []testCase{{ID: "nan", Size: math.Inf(1)}}}); err == nil {
		t.Fatal("an unmarshalable case must fail rendering")
	}
}
