package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const duplicatesFixturePath = "../../testdata/port/cli/duplicates-process.json"

type duplicatesPortStep struct {
	Stdin    string                `json:"stdin,omitempty"`
	Args     []string              `json:"args"`
	ExitCode int                   `json:"exit_code"`
	Stdout   string                `json:"stdout"`
	Stderr   string                `json:"stderr"`
	State    map[string][][]string `json:"state,omitempty"`
}

type duplicatesPortCase struct {
	Name string `json:"name"`
	// Files are written under the vault before `symdesk index` runs.
	Files map[string]string `json:"files,omitempty"`
	// SparseFiles maps a vault path to a sparse file size in bytes, created
	// after indexing because an oversized file fails `index`.
	SparseFiles map[string]int64 `json:"sparse_files,omitempty"`
	// SkipIndex leaves the sidecar empty: the steps run on an unindexed vault.
	SkipIndex bool `json:"skip_index,omitempty"`
	// PostIndexFiles are written (or overwritten) after indexing, so the
	// vault on disk differs from the sidecar.
	PostIndexFiles map[string]string `json:"post_index_files,omitempty"`
	// NoVault runs without an injected --vault and without any configured
	// vault; the sidecar is not built either.
	NoVault bool `json:"no_vault,omitempty"`
	// UnixOnly marks error text that uses the operating system's wording.
	UnixOnly bool `json:"unix_only,omitempty"`
	// FSCase marks a case whose bytes depend on whether the vault's file
	// system folds case ("insensitive": APFS, NTFS) or not ("sensitive":
	// ext4). Each class is recorded natively; replays run only their class.
	FSCase string               `json:"fs_case,omitempty"`
	Steps  []duplicatesPortStep `json:"steps"`
}

type duplicatesPortFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	Cases         []duplicatesPortCase `json:"cases"`
}

// duplicatesBase is the shared prose the near-duplicate documents vary.
const duplicatesBase = "the quick brown fox jumps over the lazy dog while the rain falls softly on the quiet town and everyone sleeps through the long grey afternoon beside the river"

func duplicatesDoc(title, body string) string {
	return "---\ntitle: " + title + "\n---\n" + body + "\n"
}

// duplicatesVariant replaces the first n words of the base prose, giving
// documents of graded similarity so a chain A~B~C can form at lower thresholds.
func duplicatesVariant(n int) string {
	words := strings.Fields(duplicatesBase)
	for i := 0; i < n && i < len(words); i++ {
		words[i] = fmt.Sprintf("w%d", i)
	}
	return strings.Join(words, " ")
}

func duplicatesCases() []duplicatesPortCase {
	jsonStep := func(args ...string) duplicatesPortStep {
		return duplicatesPortStep{Args: append([]string{"--json"}, args...)}
	}
	textStep := func(args ...string) duplicatesPortStep {
		return duplicatesPortStep{Args: args}
	}
	both := func(args ...string) []duplicatesPortStep {
		return []duplicatesPortStep{jsonStep(args...), textStep(args...)}
	}
	concat := func(groups ...[]duplicatesPortStep) []duplicatesPortStep {
		var out []duplicatesPortStep
		for _, group := range groups {
			out = append(out, group...)
		}
		return out
	}

	mixed := map[string]string{
		"a.md":          duplicatesDoc("Alpha", duplicatesBase),
		"b.md":          duplicatesDoc("Beta", duplicatesBase+" today"),
		"c.md":          duplicatesDoc("Gamma", "completely different text about rust and go ports of markdown vault tools, with sqlite fts and simhash similarity across a very long document body"),
		"short.md":      duplicatesDoc("Short", "short"),
		"short2.md":     duplicatesDoc("Short Two", "short"),
		"empty.md":      "---\ntitle: Empty\n---\n",
		"blank.md":      "---\ntitle: Blank\n---\n   \n\t\n",
		"sub/nest.md":   duplicatesDoc("Nested", duplicatesBase+" nested"),
		"unicode.md":    duplicatesDoc("Ünï <&> “quoted”", duplicatesBase+" ünï"),
		"w-dup.md":      duplicatesDoc("Dup", duplicatesBase),
		"preset.md":     "---\ntitle: Preset\nsimhash: 0000000000000000\n---\n" + duplicatesBase + " preset\n",
		"badstore.md":   "---\ntitle: Bad Stored\nsimhash: zz\n---\n" + duplicatesBase + " badstore\n",
		"hexstore.md":   "---\ntitle: Hex Stored\nsimhash: zzzzzzzzzzzzzzzz\n---\n" + duplicatesBase + " hexstore\n",
		"long/ref.md":   duplicatesDoc("Ref", duplicatesBase),
		"title-only.md": "---\ntitle: Only\n---\nx\n",
	}

	chain := map[string]string{}
	for i, n := range []int{0, 3, 6, 9, 12, 15, 18, 24} {
		chain[fmt.Sprintf("chain-%d.md", i)] = duplicatesDoc(fmt.Sprintf("Chain %d", i), duplicatesVariant(n))
	}
	chain["other.md"] = duplicatesDoc("Other", "an unrelated essay on sourdough starters, hydration ratios, oven spring, scoring patterns and the patience required to bake good bread at home every weekend")

	cases := []duplicatesPortCase{
		{
			Name:  "groups-mixed",
			Files: mixed,
			Steps: concat(
				both("duplicates"),
				both("duplicates", "--threshold", "99"),
				both("duplicates", "--threshold=60"),
				both("duplicates", "--threshold", "1"),
				both("duplicates", "--threshold", "100"),
				both("duplicates", "--threshold", "0"),
				both("duplicates", "--threshold", "-7"),
				both("duplicates", "--threshold", "0x32"),
				both("duplicates", "--threshold", "1_0"),
				[]duplicatesPortStep{textStep("duplicates", "--threshold", "60", "--threshold", "99")},
			),
		},
		{
			Name:  "groups-chain",
			Files: chain,
			Steps: concat(
				both("duplicates", "--threshold", "99"),
				both("duplicates", "--threshold", "95"),
				both("duplicates", "--threshold", "90"),
				both("duplicates"),
				both("duplicates", "--threshold", "80"),
				both("duplicates", "--threshold", "70"),
				both("duplicates", "--threshold", "50"),
				both("duplicates", "--threshold", "10"),
			),
		},
		{
			Name: "groups-none",
			Files: map[string]string{
				"one.md": duplicatesDoc("One", duplicatesBase),
				"two.md": duplicatesDoc("Two", "an unrelated essay on sourdough starters, hydration ratios, oven spring, scoring patterns and the patience required to bake good bread at home every weekend"),
			},
			Steps: both("duplicates"),
		},
		{
			Name:      "groups-unindexed",
			Files:     map[string]string{"one.md": duplicatesDoc("One", duplicatesBase), "two.md": duplicatesDoc("Two", duplicatesBase)},
			SkipIndex: true,
			Steps:     concat(both("duplicates"), both("duplicates", "--threshold", "10")),
		},
		{
			Name:  "similar-mixed",
			Files: mixed,
			Steps: concat(
				both("similar", "a.md"),
				both("duplicates", "similar", "a.md"),
				both("duplicates", "similar", "a.md", "--threshold", "10"),
				both("similar", "a.md", "--threshold", "99"),
				both("similar", "a.md", "--threshold=1"),
				both("similar", "a.md", "--threshold", "0"),
				both("similar", "a.md", "--threshold", "-4"),
				both("similar", "c.md", "--threshold", "99"),
				both("similar", "sub/nest.md"),
				both("similar", "./sub/../a.md"),
				both("similar", "short.md", "--threshold", "50"),
				both("similar", "empty.md", "--threshold", "50"),
				both("similar", "unicode.md"),
				both("similar", "preset.md", "--threshold", "40"),
				both("similar", "title-only.md", "--threshold", "30"),
				[]duplicatesPortStep{
					jsonStep("similar", "a.md", "--threshold", "50"),
					{Args: []string{"similar", "--threshold", "50", "a.md", "--json"}},
					{Args: []string{"--output", "json", "duplicates", "similar", "a.md"}},
					{Args: []string{"--output", "yaml", "similar", "a.md"}},
					{Args: []string{"--output", "text", "similar", "a.md", "--json"}},
				},
			),
		},
		{
			Name:  "similar-chain",
			Files: chain,
			Steps: concat(
				both("similar", "chain-0.md", "--threshold", "90"),
				both("similar", "chain-3.md", "--threshold", "80"),
				both("similar", "chain-7.md", "--threshold", "1"),
				both("similar", "other.md", "--threshold", "40"),
			),
		},
		{
			Name:  "similar-stored-simhash-errors",
			Files: mixed,
			// A duplicate top-level key fails `index`, so the broken file
			// appears only after the sidecar is built.
			PostIndexFiles: map[string]string{"dupkey.md": "---\ntitle: first\ntitle: second\n---\nbody\n"},
			Steps: concat(
				both("similar", "badstore.md"),
				both("similar", "hexstore.md"),
				both("similar", "dupkey.md"),
			),
		},
		{
			Name:           "similar-disk-drift",
			Files:          mixed,
			PostIndexFiles: map[string]string{"a.md": duplicatesDoc("Alpha", "now a completely different body about gardening, soil acidity, compost heaps and the long slow work of growing tomatoes through a cold wet summer"), "late.md": duplicatesDoc("Late", duplicatesBase+" late")},
			Steps: concat(
				both("similar", "a.md", "--threshold", "30"),
				both("similar", "late.md"),
				both("duplicates"),
			),
		},
		{
			Name:  "similar-path-errors",
			Files: mixed,
			Steps: concat(
				both("similar", "../outside.md"),
				both("similar", "sub/../../outside.md"),
			),
		},
		{
			Name:  "arguments",
			Files: map[string]string{"a.md": duplicatesDoc("Alpha", duplicatesBase)},
			Steps: concat(
				both("similar"),
				both("similar", "a.md", "b.md"),
				both("duplicates", "similar"),
				both("duplicates", "similar", "a.md", "b.md", "c.md"),
				both("duplicates", "extra"),
				both("duplicates", "extra", "more"),
				both("duplicates", "--threshold", "abc"),
				both("duplicates", "--threshold", ""),
				both("duplicates", "--threshold", "1.5"),
				both("duplicates", "--threshold", " 7"),
				both("duplicates", "--threshold", "08"),
				both("duplicates", "--threshold", "1__0"),
				both("duplicates", "--threshold", "99999999999999999999"),
				both("duplicates", "--threshold", "-99999999999999999999"),
				both("similar", "a.md", "--threshold", "x\"y\\z"),
				both("duplicates", "similar", "a.md", "--threshold", "0b2"),
			),
		},
		{
			Name:     "unreadable-targets",
			UnixOnly: true,
			Files:    map[string]string{"a.md": duplicatesDoc("Alpha", duplicatesBase), "sub/inner.md": duplicatesDoc("Inner", duplicatesBase)},
			SparseFiles: map[string]int64{
				"big.md": 64<<20 + 1,
				"max.md": 64 << 20,
			},
			Steps: concat(
				both("similar", "missing.md"),
				both("similar", "sub/missing.md"),
				both("similar", "missing/deep/none.md"),
				both("similar", "sub"),
				both("similar", "."),
				both("similar", ""),
				both("similar", "big.md"),
				both("duplicates", "similar", "big.md"),
				both("duplicates", "similar", "/etc/passwd"),
			),
		},
		{
			Name:    "no-vault",
			NoVault: true,
			Steps: concat(
				both("duplicates"),
				both("similar", "a.md"),
			),
		},
		{
			// $ROOT in an argument is the case root; vault selection is
			// explicit because NoVault omits the injected --vault.
			Name:     "bad-vault",
			NoVault:  true,
			UnixOnly: true,
			Files:    map[string]string{"a.md": duplicatesDoc("Alpha", duplicatesBase)},
			Steps: concat(
				both("--vault", "$ROOT/nope", "duplicates"),
				both("--vault", "$ROOT/vault/a.md", "duplicates"),
				both("--vault", "$ROOT/nope", "similar", "a.md"),
				both("--vault", "$ROOT/vault/a.md", "duplicates", "similar", "a.md"),
			),
		},
	}
	return cases
}

func TestPortDuplicatesProcessContract(t *testing.T) {
	fixture := observeDuplicatesProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), duplicatesFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// Operating-system wording is recorded as the Unix oracle reports it;
		// compare only the cases that do not depend on it.
		var recorded duplicatesPortFixture
		if err := json.Unmarshal(current, &recorded); err != nil {
			t.Fatal(err)
		}
		filtered := recorded
		filtered.Cases = nil
		for _, c := range recorded.Cases {
			if !c.UnixOnly {
				filtered.Cases = append(filtered.Cases, c)
			}
		}
		current, err = json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		current = append(current, '\n')
	}
	if !bytes.Equal(current, encoded) {
		offset := 0
		for offset < len(current) && offset < len(encoded) && current[offset] == encoded[offset] {
			offset++
		}
		from := max(offset-160, 0)
		t.Fatalf("duplicates fixture is stale; regenerate through portgen\nfirst difference at byte %d\ncommitted: %q\nobserved:  %q", offset, string(current[from:min(offset+160, len(current))]), string(encoded[from:min(offset+160, len(encoded))]))
	}
}

func observeDuplicatesProcess(t *testing.T) duplicatesPortFixture {
	t.Helper()
	binaryRoot := t.TempDir()
	binary := buildIndexRetrievalBinary(t, binaryRoot)
	fixture := duplicatesPortFixture{SchemaVersion: 1}
	for _, c := range duplicatesCases() {
		if runtime.GOOS == "windows" && c.UnixOnly {
			continue
		}
		fixture.Cases = append(fixture.Cases, observeDuplicatesCase(t, binary, c))
	}
	return fixture
}

func observeDuplicatesCase(t *testing.T, binary string, c duplicatesPortCase, afterStep ...func(string, string)) duplicatesPortCase {
	t.Helper()
	// The service canonicalizes the vault root but the index stores the path
	// as given, so a root behind a symlink (macOS /var -> /private/var) or a
	// Windows 8.3 short name makes every printed path a long "../.." walk
	// between the two spellings. Observe from the canonical spelling so the
	// recorded bytes do not depend on the runner's temp directory.
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	home := filepath.Join(root, "home")
	vault := filepath.Join(root, "vault")
	cwd := filepath.Join(root, "cwd")
	for _, directory := range []string{home, vault, cwd} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles := func(files map[string]string) {
		for name, content := range files {
			target := filepath.Join(vault, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFiles(c.Files)
	environment := cleanDuplicatesEnv(os.Environ())
	environment = append(environment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"))
	run := func(args []string, input ...string) (int, string, string) {
		cmd := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Dir = cwd
		if len(input) > 0 {
			cmd.Stdin = strings.NewReader(input[0])
		}
		cmd.Env = environment
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run Go CLI %v: %v", args, err)
			}
			code = exit.ExitCode()
		}
		return code, stdout.String(), stderr.String()
	}
	if !c.SkipIndex && !c.NoVault {
		if code, _, stderr := run([]string{"--vault", vault, "index"}); code != 0 {
			t.Fatalf("setup index for %s exited %d: %s", c.Name, code, stderr)
		}
	}
	writeFiles(c.PostIndexFiles)
	for name, size := range c.SparseFiles {
		target := filepath.Join(vault, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(target) //nolint:gosec // test-only path under a temporary vault
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(size); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	observed := c
	observed.Steps = nil
	for _, step := range c.Steps {
		args := append([]string(nil), step.Args...)
		if !c.NoVault {
			args = append([]string{"--vault", vault}, args...)
		}
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], "$ROOT", root)
		}
		code, stdout, stderr := run(args, step.Stdin)
		for _, check := range afterStep {
			check(vault, home)
		}
		observed.Steps = append(observed.Steps, duplicatesPortStep{
			Args:     step.Args,
			Stdin:    step.Stdin,
			ExitCode: code,
			Stdout:   normalizeRetryText(stdout, root),
			Stderr:   normalizeRetryText(stderr, root),
		})
	}
	return observed
}

// cleanDuplicatesEnv drops variables that would select a vault or provider
// outside the isolated case root.
func cleanDuplicatesEnv(environment []string) []string {
	cleaned := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "SYMDESK_") || strings.HasPrefix(strings.ToUpper(name), "SYMRELATE_") || strings.HasPrefix(strings.ToUpper(name), "SYMSEEK_") {
			continue
		}
		cleaned = append(cleaned, entry)
	}
	return cleaned
}
