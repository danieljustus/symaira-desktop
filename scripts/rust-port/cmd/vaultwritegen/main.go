// Command vaultwritegen generates the Go-owned frontmatter write fixture.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const defaultOracleCommit = "6a91639f4f6ef8201cf4cbe7eed6ccc77a3874f1"

type fixture struct {
	SchemaVersion int               `json:"schema_version"`
	Oracle        oracle            `json:"oracle"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []writeCase       `json:"cases"`
}

type oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type writeCase struct {
	ID           string        `json:"id"`
	Operation    string        `json:"operation"`
	InputBase64  string        `json:"input_base64"`
	Key          string        `json:"key"`
	Value        capturedValue `json:"value"`
	OutputBase64 string        `json:"output_base64"`
	ErrorClass   string        `json:"error_class,omitempty"`
	ModeBefore   *uint32       `json:"unix_mode_before,omitempty"`
	ModeAfter    *uint32       `json:"unix_mode_after,omitempty"`
}

// capturedValue is the lossless replay description of a Go input. Its value
// field is deliberately not the value passed to the Go writer: JSON cannot
// represent all of the supported Go values (notably uint64 and non-finite
// float64), and decoding an untagged tree would erase nested type information.
type capturedValue struct {
	Kind  string `json:"kind"`
	Value any    `json:"value,omitempty"`
}

type operation struct {
	id            string
	kind          string
	input         []byte
	key           string
	value         any
	makeFile      bool
	makeDirectory bool
	fileMode      uint32
}

func main() {
	output := flag.String("output", "testdata/port/vault/frontmatter-write.json", "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", defaultOracleCommit, "Go oracle commit")
	release := flag.String("oracle-release", "post-v0.13.0-dependency-refresh", "Go oracle release")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fatal("find repository root: %v", err)
	}
	sourceHashes, err := verifyOracle(root, *commit)
	if err != nil {
		fatal("verify pinned Go oracle: %v", err)
	}
	value, err := build(root, oracle{Commit: *commit, Release: *release}, sourceHashes)
	if err != nil {
		fatal("build fixture: %v", err)
	}
	content, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		fatal("marshal fixture: %v", err)
	}
	content = append(content, '\n')
	path := filepath.Join(root, filepath.FromSlash(*output))
	if *check {
		existing, err := os.ReadFile(path) // #nosec G304 -- operator-selected fixture path relative to repository root
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if err := compareFixture(existing, content); err != nil {
			fatal("%v", err)
		}
		fmt.Printf("PASS frontmatter write fixture (%s)\n", runtime.GOOS)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("PASS frontmatter write fixture generated (%d cases)\n", len(value.Cases))
}

func compareFixture(existing, generated []byte) error {
	return compareFixtureForPlatform(existing, generated, runtime.GOOS)
}

func compareFixtureForPlatform(existing, generated []byte, goos string) error {
	if goos != "windows" {
		if !bytes.Equal(existing, generated) {
			return fmt.Errorf("frontmatter write fixture drift; regenerate from pinned Go oracle")
		}
		return nil
	}

	retained, err := decodeFixture(existing)
	if err != nil {
		return fmt.Errorf("decode retained fixture: %w", err)
	}
	generatedFixture, err := decodeFixture(generated)
	if err != nil {
		return fmt.Errorf("decode generated fixture: %w", err)
	}
	normalizeUnixModes(&retained)
	normalizeUnixModes(&generatedFixture)
	if !reflect.DeepEqual(retained, generatedFixture) {
		return fmt.Errorf("frontmatter write fixture drift; regenerate from pinned Go oracle")
	}
	return nil
}

func decodeFixture(data []byte) (fixture, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var value fixture
	if err := decoder.Decode(&value); err != nil {
		return fixture{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != nil {
		if errors.Is(err, io.EOF) {
			return value, nil
		}
		return fixture{}, err
	}
	return fixture{}, fmt.Errorf("trailing JSON data")
}

func normalizeUnixModes(value *fixture) {
	for index := range value.Cases {
		value.Cases[index].ModeBefore = nil
		value.Cases[index].ModeAfter = nil
	}
}

func build(root string, oracle oracle, sourceHashes map[string]string) (fixture, error) {
	cases := make([]writeCase, 0, len(testOperations()))
	for _, spec := range testOperations() {
		result, err := runOperation(root, spec)
		if err != nil {
			return fixture{}, fmt.Errorf("case %s: %w", spec.id, err)
		}
		cases = append(cases, result)
	}
	return fixture{SchemaVersion: 2, Oracle: oracle, SourceHashes: sourceHashes, Cases: cases}, nil
}

func testOperations() []operation {
	operations := []operation{
		{id: "scalar-create", kind: "set_key", input: []byte("Body only\n"), key: "title", value: "New title", makeFile: true, fileMode: 0o640},
		{id: "scalar-replace-first-and-preserve-comments", kind: "set_key", input: []byte("---\ntitle: old\n# keep this\ntitle: later\n---\nbody\n"), key: "title", value: "new\"value", makeFile: true, fileMode: 0o640},
		{id: "scalar-missing-key", kind: "set_key", input: []byte("---\nexisting: yes\n---\nbody\n"), key: "added", value: "42", makeFile: true, fileMode: 0o640},
		{id: "scalar-crlf", kind: "set_key", input: []byte("---\r\ntitle: old\r\ncomment: keep\r\n---\r\nbody\r\n"), key: "title", value: "line", makeFile: true, fileMode: 0o640},
		{id: "scalar-crlf-doubled-trailing-cr", kind: "set_key", input: []byte("---\r\r\ntitle: old\r\r\ncomment: keep\r\r\n---\r\r\nbody\r\r\n"), key: "title", value: "line", makeFile: true, fileMode: 0o640},
		{id: "scalar-malformed-header", kind: "set_key", input: []byte("---\ntitle: old\nbody without closing marker\n"), key: "title", value: "line", makeFile: true, fileMode: 0o640},
		{id: "typed-create", kind: "set_value", input: []byte("Opaque body\n"), key: "count", value: int64(7), makeFile: true, fileMode: 0o640},
		{id: "typed-crlf", kind: "set_value", input: []byte("---\r\ntitle: old\r\n---\r\nbody\r\n"), key: "count", value: int64(8), makeFile: true, fileMode: 0o640},
		{id: "typed-crlf-doubled-trailing-cr", kind: "set_value", input: []byte("---\r\r\ntitle: old\r\r\n---\r\r\nbody\r\r\n"), key: "count", value: int64(9), makeFile: true, fileMode: 0o640},
		{id: "typed-mixed-lines", kind: "set_value", input: []byte("---\nfirst: one\r\nsecond: two\n---\nbody\n"), key: "third", value: true, makeFile: true, fileMode: 0o640},
		{id: "typed-opaque-invalid-utf8-body", kind: "set_value", input: append([]byte("---\ntitle: x\n---\nopaque"), append([]byte{0xff}, '\n')...), key: "status", value: "kept", makeFile: true, fileMode: 0o640},
		{id: "typed-number-float", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: 1.25, makeFile: true, fileMode: 0o640},
		{id: "typed-number-float-point-one-two-five", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: 0.125, makeFile: true, fileMode: 0o640},
		{id: "typed-number-float-negative-point-one-two-five", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: -0.125, makeFile: true, fileMode: 0o640},
		{id: "typed-number-float-small-leading-zeroes", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: 0.00125, makeFile: true, fileMode: 0o640},
		{id: "typed-number-float-negative-small-leading-zeroes", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: -0.00125, makeFile: true, fileMode: 0o640},
		{id: "typed-number-float64-one", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: float64(1), makeFile: true, fileMode: 0o640},
		{id: "typed-number-negative-zero", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: math.Copysign(0, -1), makeFile: true, fileMode: 0o640},
		{id: "typed-number-small-exponent", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: float64(1e-7), makeFile: true, fileMode: 0o640},
		{id: "typed-number-large-exponent", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: float64(1e20), makeFile: true, fileMode: 0o640},
		{id: "typed-number-uint64-max", kind: "set_value", input: []byte("---\n---\n"), key: "count", value: ^uint64(0), makeFile: true, fileMode: 0o640},
		{id: "typed-quote", kind: "set_value", input: []byte("---\n---\n"), key: "text", value: "a\\b\"c", makeFile: true, fileMode: 0o640},
		{id: "typed-array", kind: "set_value", input: []byte("---\n---\n"), key: "items", value: []any{"one", int64(2), false, nil}, makeFile: true, fileMode: 0o640},
		{id: "typed-map", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"first": "one", "number": int64(2)}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-nested-scalars", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"nested": map[string]any{"count": int64(2), "name": "leaf"}}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-natural-key-order", kind: "set_value", input: []byte("---\n---\n"), key: "items", value: map[string]any{"item2": "two", "item10": "ten", "item1": "one"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-legacy-bool-strings", kind: "set_value", input: []byte("---\n---\n"), key: "labels", value: map[string]any{"yes": "yes", "no": "no", "on": "on", "off": "off", "true": "true"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-multiline-string", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"description": "line one\nline two"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-sequence-and-mapping", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"items": []any{"one", int64(2)}, "nested": map[string]any{"enabled": true}}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-quoted-keys", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"key:with-colon": "colon", "key with spaces": "spaces", "true": "reserved"}, makeFile: true, fileMode: 0o640},
		{id: "typed-null", kind: "set_value", input: []byte("---\n---\n"), key: "empty", value: nil, makeFile: true, fileMode: 0o640},
		{id: "typed-root-float-nan", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: math.NaN(), makeFile: true, fileMode: 0o640},
		{id: "typed-root-float-positive-infinity", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: math.Inf(1), makeFile: true, fileMode: 0o640},
		{id: "typed-root-float-negative-infinity", kind: "set_value", input: []byte("---\n---\n"), key: "ratio", value: math.Inf(-1), makeFile: true, fileMode: 0o640},
		{id: "typed-map-nested-floats", kind: "set_value", input: []byte("---\n---\n"), key: "ratios", value: map[string]any{"one": float64(1.0), "negative_zero": math.Copysign(0, -1), "small": float64(1e-5), "large": float64(1e6), "huge": float64(1e20)}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-nested-leading-zero-floats", kind: "set_value", input: []byte("---\n---\n"), key: "ratios", value: map[string]any{"eighth": 0.125, "neg_eighth": -0.125, "milli_eighth": 0.00125, "neg_milli_eighth": -0.00125}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-nested-empty-map", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"empty": map[string]any{}, "populated": map[string]any{"key": "value"}}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-sequence-of-maps", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"items": []any{map[string]any{"first": "one", "second": "two"}, map[string]any{"name": "second"}}}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-nested-sequences", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"matrix": []any{[]any{"a", "b"}, []any{"c", "d"}}}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-blank-line-block-string", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"description": "line one\n\nline two\n"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-natural-key-counterexamples", kind: "set_value", input: []byte("---\n---\n"), key: "items", value: map[string]any{"item02": "leading zero", "item2": "two", "item10": "ten", "digit": "digit", "é": "non-ASCII letter"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-string-rendering-counterexamples", kind: "set_value", input: []byte("---\n---\n"), key: "labels", value: map[string]any{"yes": "yes", "Yes": "Yes", "YES": "YES", "no": "no", "No": "No", "NO": "NO", "on": "on", "On": "On", "ON": "ON", "off": "off", "Off": "Off", "OFF": "OFF", "base60": "1:20", "tabs": "tab\tvalue", "newlines": "line one\nline two", "trailing_newline": "line\n", "backslashes": `C:\tmp\path`}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-multiline-key", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"line one\nline two": "multiline key"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-natural-key-multidigit-prefix", kind: "set_value", input: []byte("---\n---\n"), key: "items", value: map[string]any{"a12b": "1", "a100b": "2", "item20": "20", "item100": "100", "item19": "19"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-natural-key-letter-digit-after-digits", kind: "set_value", input: []byte("---\n---\n"), key: "items", value: map[string]any{"item1a": "letter", "item10": "digit", "item1_": "symbol", "item-a": "after non-digit letter", "item-0": "after non-digit number"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-natural-key-leading-zeros", kind: "set_value", input: []byte("---\n---\n"), key: "items", value: map[string]any{"item001": "three digits", "item01": "two digits", "item1": "one digit", "item0001": "four digits", "item02": "two with two", "item2": "one with two"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-natural-key-unicode-nd-marks", kind: "set_value", input: []byte("---\n---\n"), key: "items", value: map[string]any{"item\uFF11": "fullwidth 1", "item\uFF12": "fullwidth 2", "item\u0661": "arabic-indic 1", "item\u0662": "arabic-indic 2", "item\u0967": "devanagari 1", "e\u0301": "combining mark"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-base60-valid-invalid-suffixes", kind: "set_value", input: []byte("---\n---\n"), key: "labels", value: map[string]any{"v_1_59": "1:59", "v_1_20_dot": "1:20.", "v_1_underscore_2": "1_:2", "inv_1_70": "1:70", "inv_1_2_5_3": "1:2.5:3", "inv_1_0_underscore": "1:0_"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-string-escape-and-whitespace-corpus", kind: "set_value", input: []byte("---\n---\n"), key: "corpus", value: map[string]any{"nul": "pre\x00suf", "bel": "bell\x07sound", "bs": "back\x08space", "esc": "esc\x1bcode", "nel": "next\u0085line", "nbsp": "non\u00a0breaking", "u2028": "line\u2028sep", "u2029": "para\u2029sep", "cr": "cr\rline", "tab": "tab\tvalue", "blank": "", "trailing_spaces": "trailing  ", "leading_spaces": "  leading", "multiple_newlines": "multi\n\n\nnewlines\n"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-tab-unicode-whitespace-combinations", kind: "set_value", input: []byte("---\n---\n"), key: "corpus", value: map[string]any{"tab_nbsp": "tab\t\u00a0nbsp", "tab_ls": "tab\t\u2028line", "tab_ps": "tab\t\u2029para", "tab_nbsp_ls": "tab\t\u00a0\u2028mixed", "tab_nbsp_ps": "tab\t\u00a0\u2029mixed", "tab_ls_ps": "tab\t\u2028\u2029mixed", "tab_all_mixed": "tab\t\u00a0\u2028\u2029all"}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-multiline-whitespace-quotes-separators", kind: "set_value", input: []byte("---\n---\n"), key: "corpus", value: map[string]any{"leading_spaces": "  line one\nline two", "trailing_spaces": "line one\nline two  ", "line_trailing_spaces": "line one  \nline two", "newline_ls": "one\n\u2028two", "newline_ps": "one\n\u2029two", "consecutive_ls": "one\u2028\u2028two", "consecutive_ps": "one\u2029\u2029two", "consecutive_ls_ps": "one\u2028\u2029two", "double_quotes": "line one \"quoted\"\nline two", "single_and_double_quotes": "line 'one'\n\"line two\""}, makeFile: true, fileMode: 0o640},
		{id: "typed-map-nested-sequences-multiline-block-values", kind: "set_value", input: []byte("---\n---\n"), key: "details", value: map[string]any{"sequence_block_strings": []any{"line one\nline two", "  leading space", "  line one\nline two"}, "nested_mapping_leading_space": map[string]any{"leading_space_multiline": "  line one\nline two", "normal": "value"}, "sequence_with_mapping_multiline_key": []any{map[string]any{"line one\nline two": "multiline key in sequence mapping"}}}, makeFile: true, fileMode: 0o640},
		{id: "delete-present", kind: "delete_value", input: []byte("---\ntitle: remove\nkeep: yes\n---\nbody\n"), key: "title", makeFile: true, fileMode: 0o640},
		{id: "delete-absent-inside-frontmatter", kind: "delete_value", input: []byte("---\nkeep: yes\n---\nbody\n"), key: "missing", makeFile: true, fileMode: 0o640},
		{id: "delete-without-frontmatter", kind: "delete_value", input: []byte("body only\n"), key: "missing", makeFile: true, fileMode: 0o640},
		{id: "read-directory", kind: "set_value", input: nil, key: "title", value: "directory", makeDirectory: true, fileMode: 0o750},
		{id: "missing-file", kind: "set_value", input: nil, key: "title", value: "missing", makeFile: false},
		{id: "missing-parent", kind: "delete_value", input: nil, key: "title", makeFile: false},
	}

	shapes := []struct {
		slug string
		base any
	}{
		{
			slug: "map-seq-map-first-multiline-key",
			base: map[string]any{
				"items": []any{
					map[string]any{
						"line one\nline two": "scalar value",
					},
				},
			},
		},
		{
			slug: "map-seq-map-normal-then-multiline-key",
			base: map[string]any{
				"items": []any{
					map[string]any{
						"first_normal":      "normal value",
						"second\nmultiline": "multiline value",
					},
				},
			},
		},
		{
			slug: "map-seq-map-nested-map-sequence",
			base: map[string]any{
				"items": []any{
					map[string]any{
						"nested": map[string]any{
							"inner_list": []any{"elem1", "elem2"},
							"inner_map": map[string]any{
								"leaf": "value",
							},
						},
					},
				},
			},
		},
		{
			slug: "nested-sequence-block-strings-maps",
			base: map[string]any{
				"items": []any{
					[]any{
						"line one\nline two",
						map[string]any{
							"inner_key": "inner_value",
						},
					},
				},
			},
		},
	}

	for depth := 1; depth <= 3; depth++ {
		for _, shape := range shapes {
			operations = append(operations, operation{
				id:       fmt.Sprintf("typed-systematic-depth%d-%s", depth, shape.slug),
				kind:     "set_value",
				input:    []byte("---\n---\n"),
				key:      "details",
				value:    wrapDepth(depth, shape.base),
				makeFile: true,
				fileMode: 0o640,
			})
		}
	}

	return operations
}

func wrapDepth(depth int, payload any) any {
	current := payload
	for d := depth; d >= 1; d-- {
		current = map[string]any{
			fmt.Sprintf("level%d", d): current,
		}
	}
	return current
}

func runOperation(root string, spec operation) (writeCase, error) {
	dir, err := os.MkdirTemp("", "symdesk-frontmatter-write-")
	if err != nil {
		return writeCase{}, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	path := filepath.Join(dir, "note.md")
	if spec.id == "missing-parent" {
		path = filepath.Join(dir, "missing", "note.md")
	}
	if spec.makeFile {
		if err := os.WriteFile(path, spec.input, os.FileMode(spec.fileMode)); err != nil {
			return writeCase{}, err
		}
		if err := os.Chmod(path, os.FileMode(spec.fileMode)); err != nil {
			return writeCase{}, err
		}
	} else if spec.makeDirectory {
		if err := os.Mkdir(path, os.FileMode(spec.fileMode)); err != nil {
			return writeCase{}, err
		}
		if err := os.Chmod(path, os.FileMode(spec.fileMode)); err != nil {
			return writeCase{}, err
		}
	}
	before := fileMode(path)
	recordedValue, err := captureValue(spec.value)
	if err != nil {
		return writeCase{}, fmt.Errorf("capture value: %w", err)
	}
	var mutationErr error
	switch spec.kind {
	case "set_key":
		value, ok := spec.value.(string)
		if !ok {
			return writeCase{}, fmt.Errorf("scalar value has type %T", spec.value)
		}
		mutationErr = vault.SetFrontmatterKey(path, spec.key, value)
	case "set_value":
		mutationErr = vault.SetFrontmatterValue(path, spec.key, spec.value)
	case "delete_value":
		mutationErr = vault.DeleteFrontmatterValue(path, spec.key)
	default:
		return writeCase{}, fmt.Errorf("unknown operation %q", spec.kind)
	}
	output, err := os.ReadFile(path) // #nosec G304 -- private temporary case file output generated during fixture execution
	if err != nil && !errors.Is(err, os.ErrNotExist) && !spec.makeDirectory {
		return writeCase{}, err
	}
	result := writeCase{
		ID:           spec.id,
		Operation:    spec.kind,
		InputBase64:  base64.StdEncoding.EncodeToString(spec.input),
		Key:          spec.key,
		OutputBase64: base64.StdEncoding.EncodeToString(output),
		ErrorClass:   errorClass(mutationErr),
		ModeBefore:   before,
		ModeAfter:    fileMode(path),
		Value:        recordedValue,
	}
	return result, nil
}

func captureValue(value any) (capturedValue, error) {
	switch value := value.(type) {
	case nil:
		return capturedValue{Kind: "null"}, nil
	case bool:
		return capturedValue{Kind: "bool", Value: value}, nil
	case string:
		return capturedValue{Kind: "string", Value: value}, nil
	case int:
		return capturedValue{Kind: "i64", Value: strconv.FormatInt(int64(value), 10)}, nil
	case int64:
		return capturedValue{Kind: "i64", Value: strconv.FormatInt(value, 10)}, nil
	case uint64:
		return capturedValue{Kind: "u64", Value: strconv.FormatUint(value, 10)}, nil
	case float64:
		return capturedValue{Kind: "float64", Value: strconv.FormatFloat(value, 'g', -1, 64)}, nil
	case []string:
		values := make([]capturedValue, len(value))
		for index, item := range value {
			captured, err := captureValue(item)
			if err != nil {
				return capturedValue{}, fmt.Errorf("sequence item %d: %w", index, err)
			}
			values[index] = captured
		}
		return capturedValue{Kind: "sequence", Value: values}, nil
	case []any:
		values := make([]capturedValue, len(value))
		for index, item := range value {
			captured, err := captureValue(item)
			if err != nil {
				return capturedValue{}, fmt.Errorf("sequence item %d: %w", index, err)
			}
			values[index] = captured
		}
		return capturedValue{Kind: "sequence", Value: values}, nil
	case map[string]any:
		values := make(map[string]capturedValue, len(value))
		for key, item := range value {
			captured, err := captureValue(item)
			if err != nil {
				return capturedValue{}, fmt.Errorf("mapping key %q: %w", key, err)
			}
			values[key] = captured
		}
		return capturedValue{Kind: "mapping", Value: values}, nil
	default:
		return capturedValue{}, fmt.Errorf("unsupported Go value type %T", value)
	}
}

func errorClass(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, os.ErrNotExist) {
		return "not_found"
	}
	return "filesystem"
}

func fileMode(path string) *uint32 {
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS == "windows" {
		return nil
	}
	mode := uint32(info.Mode().Perm())
	return &mode
}

func verifyOracle(root, revision string) (map[string]string, error) {
	if revision != defaultOracleCommit {
		return nil, fmt.Errorf("oracle commit must be pinned to %s", defaultOracleCommit)
	}
	if _, err := gitOutput(root, "rev-parse", "--verify", revision+"^{commit}"); err != nil {
		return nil, fmt.Errorf("revision %s: %w", revision, err)
	}
	list, err := gitOutput(root, "ls-tree", "-r", "--name-only", revision, "--", "internal/vault")
	if err != nil {
		return nil, err
	}
	pinnedSources := goSourcePaths(list)
	if len(pinnedSources) == 0 {
		return nil, fmt.Errorf("pinned revision has incomplete Go source set")
	}

	currentList, err := gitOutput(root, "ls-files", "--cached", "--others", "--exclude-standard", "--", "internal/vault")
	if err != nil {
		return nil, err
	}
	currentSources := goSourcePaths(currentList)
	actualSources, err := enumerateGoSources(root)
	if err != nil {
		return nil, fmt.Errorf("enumerate current internal/vault Go sources: %w", err)
	}
	if !sameStrings(pinnedSources, currentSources) || !sameStrings(pinnedSources, actualSources) {
		return nil, fmt.Errorf("current internal/vault Go source set differs from pinned Git source set")
	}

	paths := append([]string{}, pinnedSources...)
	paths = append(paths, "go.mod", "go.sum")
	hashes := make(map[string]string, len(paths))
	for _, path := range paths {
		pinned, err := gitOutput(root, "show", revision+":"+path)
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", path, revision, err)
		}
		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path))) // #nosec G304 -- reading repository source file verified against pinned Git tree
		if err != nil {
			return nil, fmt.Errorf("read current %s: %w", path, err)
		}
		if !bytes.Equal(current, pinned) {
			return nil, fmt.Errorf("current %s differs from pinned Git blob", path)
		}
		digest := sha256.Sum256(pinned)
		hashes[path] = hex.EncodeToString(digest[:])
	}
	return hashes, nil
}

func goSourcePaths(output []byte) []string {
	paths := make([]string, 0)
	for _, path := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func enumerateGoSources(root string) ([]string, error) {
	base := filepath.Join(root, "internal", "vault")
	paths := make([]string, 0)
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(filepath.ToSlash(relative), ".go") &&
			!strings.HasSuffix(filepath.ToSlash(relative), "_test.go") {
			paths = append(paths, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

const gitCommandTimeout = 30 * time.Second

func gitOutput(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...) // #nosec G204 -- literal git executable with internal generator argument vectors
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
