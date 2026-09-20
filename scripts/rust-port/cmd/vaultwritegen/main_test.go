package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestVerifyOracleSourceGuard(t *testing.T) {
	tests := []struct {
		name     string
		revision string
		edit     func(t *testing.T, root string)
	}{
		{
			name: "pristine passes",
		},
		{
			name: "changed source rejected",
			edit: func(t *testing.T, root string) {
				path := firstPinnedSource(t, root)
				content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path))) // #nosec G304 -- path is checked by firstPinnedSource and root is a disposable test clone
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), append(content, '\n'), 0o600); err != nil { // #nosec G703 -- path is verified local within disposable cloned repository
					t.Fatal(err)
				}
			},
		},
		{
			name: "added source rejected",
			edit: func(t *testing.T, root string) {
				path := filepath.Join(root, "internal", "vault", "added_guard.go")
				if err := os.WriteFile(path, []byte("package vault\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, root, "add", "internal/vault/added_guard.go")
			},
		},
		{
			name: "ignored added source rejected",
			edit: func(t *testing.T, root string) {
				ignore := filepath.Join(root, ".gitignore")
				file, err := os.OpenFile(ignore, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600) // #nosec G304 -- target is fixed .gitignore within disposable test clone
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("internal/vault/ignored_guard.go\n"); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(root, "internal", "vault", "ignored_guard.go")
				if err := os.WriteFile(path, []byte("package vault\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:     "wrong revision rejected",
			revision: "HEAD",
		},
		{
			name: "missing source rejected",
			edit: func(t *testing.T, root string) {
				path := firstPinnedSource(t, root)
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := clonePinnedRepo(t)
			revision := test.revision
			if revision == "" {
				revision = defaultOracleCommit
			}
			_, err := verifyOracle(root, revision)
			if test.edit == nil {
				if test.revision == "" && err != nil {
					t.Fatalf("verifyOracle rejected pristine clone: %v", err)
				}
				if test.revision != "" && err == nil {
					t.Fatal("verifyOracle accepted an unpinned revision")
				}
				return
			}
			test.edit(t, root)
			if _, err := verifyOracle(root, revision); err == nil {
				t.Fatal("verifyOracle accepted a mutated clone")
			}
		})
	}
}

func TestNormalizeUnixModesOnlyClearsUnixFields(t *testing.T) {
	before := fixture{
		SchemaVersion: 2,
		Oracle:        oracle{Commit: defaultOracleCommit, Release: "test-release"},
		SourceHashes:  map[string]string{"internal/vault/vault.go": "digest"},
		Cases: []writeCase{{
			ID:           "case",
			Operation:    "set_value",
			InputBase64:  "aW5wdXQ=",
			Key:          "title",
			Value:        capturedValue{Kind: "mapping", Value: map[string]capturedValue{"nested": {Kind: "bool", Value: true}}},
			OutputBase64: "b3V0cHV0",
			ErrorClass:   "filesystem",
			ModeBefore:   uint32Pointer(0o640),
			ModeAfter:    uint32Pointer(0o600),
		}},
	}
	after := before
	after.Cases = append([]writeCase(nil), before.Cases...)
	normalizeUnixModes(&after)
	expected := before
	expected.Cases = append([]writeCase(nil), before.Cases...)
	expected.Cases[0].ModeBefore = nil
	expected.Cases[0].ModeAfter = nil
	if !reflect.DeepEqual(after, expected) {
		t.Fatalf("normalization changed non-mode fixture fields: got %#v want %#v", after, expected)
	}
}

func TestCompareFixtureForPlatformWindowsIgnoresOnlyModeFields(t *testing.T) {
	retained := syntheticComparisonFixture()
	retained.Cases[0].ModeBefore = uint32Pointer(0o640)
	retained.Cases[0].ModeAfter = uint32Pointer(0o600)
	generated := syntheticComparisonFixture()
	generated.Cases[0].ModeBefore = uint32Pointer(0o644)
	generated.Cases[0].ModeAfter = uint32Pointer(0o644)

	if err := compareFixtureForPlatform(marshalComparisonFixture(t, retained), marshalComparisonFixture(t, generated), "windows"); err != nil {
		t.Fatalf("Windows mode-only drift was rejected: %v", err)
	}
	if err := compareFixtureForPlatform(marshalComparisonFixture(t, retained), marshalComparisonFixture(t, generated), "darwin"); err == nil {
		t.Fatal("Unix comparison accepted mode drift")
	}
}

func TestCompareFixtureForPlatformWindowsRejectsNonModeDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*fixture)
	}{
		{name: "input bytes", mutate: func(value *fixture) { value.Cases[0].InputBase64 = "Y2hhbmdlZA==" }},
		{name: "output bytes", mutate: func(value *fixture) { value.Cases[0].OutputBase64 = "Y2hhbmdlZA==" }},
		{name: "oracle provenance", mutate: func(value *fixture) { value.Oracle.Release = "changed-release" }},
		{name: "source hash provenance", mutate: func(value *fixture) { value.SourceHashes["internal/vault/vault.go"] = "changed-digest" }},
		{name: "operation", mutate: func(value *fixture) { value.Cases[0].Operation = "delete_value" }},
		{name: "value", mutate: func(value *fixture) { value.Cases[0].Value = capturedValue{Kind: "string", Value: "changed-value"} }},
	}

	retained := marshalComparisonFixture(t, syntheticComparisonFixture())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			generated := syntheticComparisonFixture()
			test.mutate(&generated)
			if err := compareFixtureForPlatform(retained, marshalComparisonFixture(t, generated), "windows"); err == nil {
				t.Fatal("Windows comparison accepted non-mode fixture drift")
			}
		})
	}
}

func TestCompareFixtureForPlatformWindowsPreservesAdjacentLargeIntegers(t *testing.T) {
	retained := syntheticComparisonFixture()
	retained.Cases[0].Value = capturedValue{Kind: "i64", Value: "9007199254740992"}
	generated := syntheticComparisonFixture()
	generated.Cases[0].Value = capturedValue{Kind: "i64", Value: "9007199254740993"}

	if err := compareFixtureForPlatform(marshalComparisonFixture(t, retained), marshalComparisonFixture(t, generated), "windows"); err == nil {
		t.Fatal("Windows comparison accepted adjacent integers beyond 2^53 as equal")
	}
}

func TestDecodeFixtureRejectsTrailingJSONAndUnknownFields(t *testing.T) {
	clean := marshalComparisonFixture(t, syntheticComparisonFixture())
	trailingJSON := append(append([]byte(nil), clean...), []byte("\n{}")...)
	trailingText := append(append([]byte(nil), clean...), []byte("\nnot-json")...)
	unknownField := appendUnknownFixtureField(clean)
	tests := []struct {
		name      string
		existing  []byte
		generated []byte
	}{
		{name: "retained trailing JSON", existing: trailingJSON, generated: clean},
		{name: "generated trailing JSON", existing: clean, generated: trailingJSON},
		{name: "retained trailing text", existing: trailingText, generated: clean},
		{name: "generated trailing text", existing: clean, generated: trailingText},
		{name: "retained unknown field", existing: unknownField, generated: clean},
		{name: "generated unknown field", existing: clean, generated: unknownField},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := compareFixtureForPlatform(test.existing, test.generated, "windows"); err == nil {
				t.Fatal("fixture validator accepted malformed JSON")
			}
		})
	}
}

func TestCaptureValuePreservesRecursiveTypes(t *testing.T) {
	value := map[string]any{
		"signed":       int64(-7),
		"unsigned":     uint64(9007199254740993),
		"float":        math.Copysign(0, -1),
		"eighth":       0.125,
		"milli_eighth": -0.00125,
		"items":        []any{nil, true, "yes", float64(1e-5)},
		"seq_of_maps":  []any{map[string]any{"a": "b"}},
		"nested_seq":   []any{[]any{"c", "d"}},
	}
	got, err := captureValue(value)
	if err != nil {
		t.Fatalf("captureValue returned error: %v", err)
	}
	if got.Kind != "mapping" {
		t.Fatalf("captured kind = %q, want mapping", got.Kind)
	}
	values, ok := got.Value.(map[string]capturedValue)
	if !ok {
		t.Fatalf("captured mapping has type %T", got.Value)
	}
	if values["signed"] != (capturedValue{Kind: "i64", Value: "-7"}) {
		t.Fatalf("signed value = %#v", values["signed"])
	}
	if values["unsigned"] != (capturedValue{Kind: "u64", Value: "9007199254740993"}) {
		t.Fatalf("unsigned value = %#v", values["unsigned"])
	}
	if values["float"] != (capturedValue{Kind: "float64", Value: "-0"}) {
		t.Fatalf("float value = %#v", values["float"])
	}
	if values["eighth"] != (capturedValue{Kind: "float64", Value: "0.125"}) {
		t.Fatalf("eighth float value = %#v", values["eighth"])
	}
	if values["milli_eighth"] != (capturedValue{Kind: "float64", Value: "-0.00125"}) {
		t.Fatalf("milli_eighth float value = %#v", values["milli_eighth"])
	}
	seqOfMaps, ok := values["seq_of_maps"].Value.([]capturedValue)
	if !ok || len(seqOfMaps) != 1 {
		t.Fatalf("seq_of_maps value = %#v", values["seq_of_maps"])
	}
	nestedSeq, ok := values["nested_seq"].Value.([]capturedValue)
	if !ok || len(nestedSeq) != 1 {
		t.Fatalf("nested_seq value = %#v", values["nested_seq"])
	}
	items, ok := values["items"].Value.([]capturedValue)
	if !ok || len(items) != 4 {
		t.Fatalf("sequence value = %#v", values["items"])
	}
	if items[0] != (capturedValue{Kind: "null"}) || items[1] != (capturedValue{Kind: "bool", Value: true}) {
		t.Fatalf("sequence scalar values = %#v", items[:2])
	}
	if items[3] != (capturedValue{Kind: "float64", Value: "1e-05"}) {
		t.Fatalf("sequence float value = %#v", items[3])
	}
	for _, test := range []struct {
		name  string
		value float64
		text  string
	}{
		{name: "nan", value: math.NaN(), text: "NaN"},
		{name: "positive infinity", value: math.Inf(1), text: "+Inf"},
		{name: "negative infinity", value: math.Inf(-1), text: "-Inf"},
	} {
		t.Run(test.name, func(t *testing.T) {
			captured, err := captureValue(test.value)
			if err != nil {
				t.Fatal(err)
			}
			if captured != (capturedValue{Kind: "float64", Value: test.text}) {
				t.Fatalf("captured special float = %#v", captured)
			}
		})
	}
}

func TestCaptureValueRejectsUnsupportedTypes(t *testing.T) {
	if _, err := captureValue(struct{ Value string }{Value: "unsupported"}); err == nil {
		t.Fatal("captureValue accepted an unsupported Go type")
	}
}

func syntheticComparisonFixture() fixture {
	return fixture{
		SchemaVersion: 2,
		Oracle:        oracle{Commit: defaultOracleCommit, Release: "test-release"},
		SourceHashes:  map[string]string{"internal/vault/vault.go": "digest"},
		Cases: []writeCase{{
			ID:           "case",
			Operation:    "set_value",
			InputBase64:  "aW5wdXQ=",
			Key:          "title",
			Value:        capturedValue{Kind: "string", Value: "value"},
			OutputBase64: "b3V0cHV0",
		}},
	}
}

func marshalComparisonFixture(t *testing.T, value fixture) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func appendUnknownFixtureField(data []byte) []byte {
	trimmed := bytes.TrimSpace(data)
	result := append([]byte(nil), trimmed[:len(trimmed)-1]...)
	return append(result, []byte(`,"unexpected":true}`)...)
}

func uint32Pointer(value uint32) *uint32 {
	return &value
}

func clonePinnedRepo(t *testing.T) string {
	t.Helper()
	source, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "repo")
	runGit(t, filepath.Dir(root), "clone", "--no-local", source, root)
	runGit(t, root, "checkout", "--detach", defaultOracleCommit)
	return root
}

func firstPinnedSource(t *testing.T, root string) string {
	t.Helper()
	output, err := gitOutput(root, "ls-tree", "-r", "--name-only", defaultOracleCommit, "--", "internal/vault")
	if err != nil {
		t.Fatal(err)
	}
	paths := goSourcePaths(output)
	if len(paths) == 0 {
		t.Fatal("pinned revision has no Go sources")
	}
	if !filepath.IsLocal(paths[0]) {
		t.Fatalf("pinned source path %q is not local", paths[0])
	}
	return paths[0]
}

const testGitCommandTimeout = 30 * time.Second

func runGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testGitCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- literal git executable with internal test harness argument vectors
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			t.Fatalf("git %v timed out: %v", args, ctx.Err())
		}
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return output
}
