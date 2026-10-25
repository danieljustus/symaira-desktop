package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/draw/scene"
	"github.com/danieljustus/symaira-desktop/internal/draw/theme"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// TestMain turns the test binary into the registry helper process when the
// parent asks for it; registryHelperCases re-executes os.Executable().
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) != "" {
		if err := runRegistryHelper(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(m.Run())
}

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

// TestGenerateCheckModeMatchesCommittedFixture is the staleness guard: the
// in-process generator must reproduce the committed fixture byte for byte.
func TestGenerateCheckModeMatchesCommittedFixture(t *testing.T) {
	message, err := generate(defaultOptions(t, true))
	if err != nil {
		t.Fatalf("committed scene fixture is stale: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render scene fixture (") {
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
	opts.output = filepath.Join(dir, "nested", "scene-bounds.json")

	message, err := generate(opts)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(message, "PASS render scene fixture generated (") {
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
	opts.output = filepath.Join(blocker, "scene-bounds.json")
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "create directory") {
		t.Fatalf("writing below a regular file must fail: %v", err)
	}
	opts.output = dir // a directory cannot be written as a file
	if _, err := generate(opts); err == nil || !strings.Contains(err.Error(), "write ") {
		t.Fatalf("writing onto a directory must fail: %v", err)
	}
}

// TestFixtureShapeAndHostIndependence pins the corpus shape and proves the
// committed bytes carry no host data: no CRLF, no absolute or Windows paths,
// no repository root.
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
	if parsed.SchemaVersion != 1 || parsed.FMAAmbiguousSkipped == 0 {
		t.Fatalf("unexpected header: %+v", parsed)
	}
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, c := range parsed.Cases {
		counts[c.Entry]++
		if ids[c.ID] || c.Expected == "" || !json.Valid(c.Input) {
			t.Fatalf("duplicate id, empty expectation or invalid input: %s", c.ID)
		}
		ids[c.ID] = true
	}
	for _, entry := range parsed.Entries {
		if counts[entry] == 0 {
			t.Fatalf("entry %s has no cases", entry)
		}
	}
	if err := verifyOrdering(parsed.Cases); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyOrderingRejectsBadCorpora(t *testing.T) {
	if err := verifyOrdering([]testCase{{ID: "a", Entry: "theme_registry"}, {ID: "b", Entry: "scene"}}); err == nil {
		t.Fatal("a case after a registry-mutating case must be rejected")
	}
	if err := verifyOrdering([]testCase{{ID: "a", Entry: "scene"}, {ID: "a", Entry: "scene"}}); err == nil {
		t.Fatal("duplicate ids must be rejected")
	}
}

// TestUnknownKeysAreRejected proves a typo in a corpus spec cannot silently
// test nothing: every decoder fails on keys it does not consume.
func TestUnknownKeysAreRejected(t *testing.T) {
	bad := []any{
		map[string]any{"type": "rect", "bogus": 1.0},
		map[string]any{"type": "polygon", "points": []any{map[string]any{"x": 1.0, "y": 2.0, "z": 3.0}}},
		map[string]any{"type": "path", "segments": []any{map[string]any{"op": "M", "extra": true}}},
		map[string]any{"type": "text", "font_size": "huge"},
		map[string]any{"type": "rect", "x": 1.0 / 3.0}, // more than 15 significant digits
		map[string]any{"type": "rect", "x": "inf"},
		map[string]any{"type": "unknown"},
		map[string]any{"type": "group", "children": []any{nil}},
		"not an object",
	}
	for i, spec := range bad {
		if _, err := decodePrimitive(spec); err == nil {
			t.Fatalf("bad spec %d accepted: %v", i, spec)
		}
	}
	if _, err := decodeTheme(map[string]any{"resolve": "x", "name": "y"}); err == nil {
		t.Fatal("a resolve spec with extra keys must be rejected")
	}
	if _, err := decodeTheme(map[string]any{"palette": []any{1.0}}); err == nil {
		t.Fatal("a non-string palette entry must be rejected")
	}
	if _, err := decodeMarker(map[string]any{"id": "m", "bogus": 1.0}); err == nil {
		t.Fatal("a marker with an unknown key must be rejected")
	}
	if prim, err := decodePrimitive(nil); prim != nil || err != nil {
		t.Fatalf("null must decode to a nil primitive: %v %v", prim, err)
	}
}

func TestNumDecodesSpelledSpecials(t *testing.T) {
	if v, err := num("NaN"); err != nil || !math.IsNaN(v) {
		t.Fatalf("NaN: %v %v", v, err)
	}
	if v, err := num("+Inf"); err != nil || !math.IsInf(v, 1) {
		t.Fatalf("+Inf: %v %v", v, err)
	}
	if v, err := num("-Inf"); err != nil || !math.IsInf(v, -1) {
		t.Fatalf("-Inf: %v %v", v, err)
	}
	if v, err := num("-0"); err != nil || v != 0 || !math.Signbit(v) {
		t.Fatalf("-0: %v %v", v, err)
	}
	if _, err := num(math.NaN()); err == nil {
		t.Fatal("a non-finite JSON number must be rejected")
	}
	if _, err := num(true); err == nil {
		t.Fatal("a bool is not a number")
	}
}

// TestFMAGuardDetectsAmbiguousCases finds a text position whose offset really
// differs between the fused and the unfused evaluation and proves the guard
// flags it (and ignores baselines that do not use the offset).
func TestFMAGuardDetectsAmbiguousCases(t *testing.T) {
	var found *scene.TextElement
	for y := 0; y < 400 && found == nil; y++ {
		for _, size := range []float64{8, 10, 12, 14, 16.5, 24, 72, 100.125} {
			el := &scene.TextElement{Y: float64(y) + 0.5, FontSize: size}
			if math.Float64bits(el.Y-float64(size*0.95)) != math.Float64bits(math.FMA(-size, 0.95, el.Y)) {
				found = el
				break
			}
		}
	}
	if found == nil {
		t.Fatal("no fused/unfused disagreement found; the guard has nothing to guard")
	}
	if !fmaAmbiguous(found) {
		t.Fatalf("ambiguous text %+v not flagged", found)
	}
	group := &scene.GroupElement{Children: []scene.Primitive{&scene.RectElement{}, found}}
	if !fmaAmbiguous(group) {
		t.Fatal("ambiguous text inside a group not flagged")
	}
	top := *found
	top.Baseline = scene.BaselineTop
	if fmaAmbiguous(&top) {
		t.Fatal("a top baseline does not use the fused offset")
	}
	nan := &scene.TextElement{Y: math.NaN(), FontSize: 14}
	if fmaAmbiguous(nan) {
		t.Fatal("NaN offsets agree on every host")
	}
	if fmaAmbiguous(&scene.RectElement{}) {
		t.Fatal("non-text primitives are never ambiguous")
	}
}

// TestRegistryHelperIsolatesGlobalRegistry proves the Register scripts never
// touch the parent's registry and that the helper returns every script.
func TestRegistryHelperIsolatesGlobalRegistry(t *testing.T) {
	before := strings.Join(theme.Names(), ",")
	cases, err := registryHelperCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != len(registryScripts()) {
		t.Fatalf("helper returned %d cases", len(cases))
	}
	if after := strings.Join(theme.Names(), ","); after != before {
		t.Fatalf("parent registry changed: %q -> %q", before, after)
	}
	var sawCustom bool
	for _, c := range cases {
		if c.Entry != "theme_registry" || c.Expected == "" {
			t.Fatalf("unexpected helper case %+v", c)
		}
		if strings.Contains(c.Expected, `"custom"`) {
			sawCustom = true
		}
	}
	if !sawCustom {
		t.Fatal("the helper never registered the custom theme")
	}
}

func TestRecordScenePipelineCases(t *testing.T) {
	b := &builder{}
	b.recordScene("t", spec{
		"width": 100.0, "height": 50.0, "theme": spec{"resolve": "report"},
		"primitives":  []any{nil, p("rect", "x", 10.0, "y", 5.0, "width", 20.0, "height", 10.0)},
		"fit_padding": 4.0,
	})
	b.recordScene("t", spec{"width": "NaN", "height": 1.0})
	if b.err != nil || len(b.cases) != 2 {
		t.Fatalf("scene cases: %d, %v", len(b.cases), b.err)
	}
	if !strings.Contains(b.cases[0].Expected, "fit_view_box={\"x\":6,\"y\":1,\"width\":28,\"height\":18}") {
		t.Fatalf("unexpected fit result:\n%s", b.cases[0].Expected)
	}
	if !strings.Contains(b.cases[1].Expected, "validate=invalid scene width: NaN") {
		t.Fatalf("unexpected NaN result:\n%s", b.cases[1].Expected)
	}

	bad := &builder{}
	bad.recordScene("bad", spec{"width": 1.0, "height": 1.0, "bogus": true})
	if bad.err == nil {
		t.Fatal("an unknown scene key must fail the run")
	}
	first := bad.err
	bad.fail("later failure")
	if bad.err != first {
		t.Fatal("the first error must stick")
	}
}

func TestRenderRejectsUnexpectedHeaderShape(t *testing.T) {
	out, err := render(fixture{SchemaVersion: 1, Cases: []testCase{{ID: "x", Entry: "scene", Input: json.RawMessage(`{}`), Expected: "ok"}}})
	if err != nil || !bytes.HasSuffix(out, []byte("  ]\n}\n")) || !bytes.Contains(out, []byte("\"cases\": [\n")) {
		t.Fatalf("unexpected render output (%v):\n%s", err, out)
	}
	var parsed fixture
	if err := json.Unmarshal(out, &parsed); err != nil || len(parsed.Cases) != 1 {
		t.Fatalf("rendered fixture does not round-trip: %v", err)
	}
	if _, err := render(fixture{Cases: []testCase{{ID: "bad", Input: json.RawMessage(`{`)}}}); err == nil {
		t.Fatal("an unmarshalable case must fail rendering")
	}
}
