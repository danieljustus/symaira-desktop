package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// The Cobra/pflag front half of the CLI (CLI-001..CLI-004): command
// resolution, flag grammar at every argv position, argument counts, required
// and mutually exclusive flags, generated help/completion/__complete and the
// exact diagnostics. Every case is a real Go process in a disposable
// HOME/XDG root; the Rust port replays them byte for byte.
//
// Cases only execute a command when that is safe and deterministic (version,
// help, completion, ls without a vault). Everything else is terminated by
// --help or fails during parsing, argument-count or required-flag validation,
// all of which Cobra runs before any command body.

const (
	cobraParseFixtureRel = "../../testdata/port/cli/cobra-parse-process.json"
	cobraParseTreeRel    = "../../testdata/port/cli/symdesk-command-tree.json"
	cobraParseHelpRel    = "../../testdata/port/cli/help-process.json"
	cobraParseOrderRel   = "../../testdata/port/cli/cobra-root-order.txt"
	cobraParseVersion    = "devel"
)

type cobraParseFlag struct {
	Name        string              `json:"name"`
	Shorthand   string              `json:"shorthand"`
	Type        string              `json:"type"`
	NoOptDef    string              `json:"no_opt_default"`
	Hidden      bool                `json:"hidden"`
	Annotations map[string][]string `json:"annotations"`
}

type cobraParseProbe struct {
	Count    int  `json:"count"`
	Accepted bool `json:"accepted"`
}

type cobraParseNode struct {
	Path           string            `json:"path"`
	Name           string            `json:"name"`
	Aliases        []string          `json:"aliases"`
	Hidden         bool              `json:"hidden"`
	Runnable       bool              `json:"runnable"`
	HasSubcommands bool              `json:"has_subcommands"`
	LocalFlags     []cobraParseFlag  `json:"local_flags"`
	Persistent     []cobraParseFlag  `json:"persistent_flags"`
	Probes         []cobraParseProbe `json:"argument_count_probes"`
}

func (n cobraParseNode) tokens() []string {
	return strings.Fields(strings.TrimPrefix(n.Path, "symdesk"))
}

func (n cobraParseNode) requiredFlags() []string {
	var names []string
	for _, flag := range n.LocalFlags {
		if values, ok := flag.Annotations["cobra_annotation_bash_completion_one_required_flag"]; ok && len(values) > 0 && values[0] == "true" {
			names = append(names, flag.Name)
		}
	}
	return names
}

// leastAccepted is the smallest argument count the validator accepts.
func (n cobraParseNode) leastAccepted() (int, bool) {
	for _, probe := range n.Probes {
		if probe.Accepted {
			return probe.Count, true
		}
	}
	return 0, false
}

type cobraParseCase struct {
	Family   string            `json:"family"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env,omitempty"`
	Files    map[string]string `json:"files,omitempty"`
	ExitCode int               `json:"exit_code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
}

type cobraParseFixture struct {
	SchemaVersion    int              `json:"schema_version"`
	BinaryVersion    string           `json:"binary_version"`
	RootRegistration []string         `json:"root_registration_order"`
	CommandPaths     int              `json:"command_paths"`
	FamilyCounts     map[string]int   `json:"family_counts"`
	Cases            []cobraParseCase `json:"cases"`
}

// cobraRegistrationOrder returns the root command's children in the order
// they were registered. cobra sorts `commands` lazily, so this must run
// before anything calls Commands().
func cobraRegistrationOrder(t *testing.T) []string {
	t.Helper()
	root := newRootCmd()
	field := reflect.ValueOf(root).Elem().FieldByName("commands")
	if !field.IsValid() {
		t.Fatal("cobra Command has no commands field")
	}
	//nolint:gosec // test-only read of cobra's registration order via its private slice.
	commands := reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem()
	var names []string
	for i := 0; i < commands.Len(); i++ {
		names = append(names, commands.Index(i).Interface().(interface{ Name() string }).Name())
	}
	return names
}

func TestPortCobraParseProcessContract(t *testing.T) {
	order := cobraRegistrationOrder(t)
	nodes := readCobraParseNodes(t)
	helpDocs := readCobraParseDocuments(t)

	binary := buildCobraParseBinary(t)
	cases := generateCobraParseCases(nodes)
	if len(cases) < 1000 {
		t.Fatalf("generated only %d cases", len(cases))
	}
	runCobraParseCases(t, binary, cases, helpDocs)

	counts := map[string]int{}
	for _, c := range cases {
		counts[c.Family]++
	}
	fixture := cobraParseFixture{
		SchemaVersion:    1,
		BinaryVersion:    cobraParseVersion,
		RootRegistration: order,
		CommandPaths:     len(nodes),
		FamilyCounts:     counts,
		Cases:            cases,
	}
	encoded, err := encodeCobraParseFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	orderText := strings.Join(order, "\n") + "\n"
	artifacts := map[string][]byte{
		cobraParseFixtureRel: encoded,
		cobraParseOrderRel:   []byte(orderText),
	}
	paths := make([]string, 0, len(artifacts))
	for path := range artifacts {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if os.Getenv("PORT_GENERATE") == "1" {
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, artifacts[path], 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		//nolint:gosec // path is a fixed repository fixture.
		current, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(current, artifacts[path]) {
			t.Fatalf("cobra parse fixture drift: %s", path)
		}
	}
	t.Logf("verified %d real-process cases over %d command paths: %v", len(cases), len(nodes), counts)
}

func readCobraParseNodes(t *testing.T) []cobraParseNode {
	t.Helper()
	data, err := os.ReadFile(cobraParseTreeRel)
	if err != nil {
		t.Fatal(err)
	}
	var tree struct {
		Commands []cobraParseNode `json:"commands"`
	}
	if err := json.Unmarshal(data, &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Commands) != 207 {
		t.Fatalf("frozen tree changed: %d", len(tree.Commands))
	}
	return tree.Commands
}

type cobraParseDocuments struct {
	byText    map[string]string
	rootUsage string
}

func readCobraParseDocuments(t *testing.T) cobraParseDocuments {
	t.Helper()
	data, err := os.ReadFile(cobraParseHelpRel)
	if err != nil {
		t.Fatal(err)
	}
	var fixture helpProcessFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	docs := cobraParseDocuments{byText: map[string]string{}}
	var keys []string
	for key := range fixture.Help {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, taken := docs.byText[fixture.Help[key]]; !taken {
			docs.byText[fixture.Help[key]] = "@help:" + key
		}
	}
	keys = keys[:0]
	for key := range fixture.Completion {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, taken := docs.byText[fixture.Completion[key]]; !taken {
			docs.byText[fixture.Completion[key]] = "@completion:" + key
		}
	}
	docs.byText[fixture.RootUsage] = "@root_usage"
	docs.rootUsage = fixture.RootUsage
	return docs
}

func buildCobraParseBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "symdesk")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	//nolint:gosec // fixed Go build of this package with a fixed version stamp.
	command := exec.Command("go", "build", "-ldflags", "-X main.version="+cobraParseVersion, "-o", binary, ".")
	command.Env = processFixtureBuild.environment
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build Go CLI for cobra parse fixtures: %v\n%s", err, output)
	}
	return binary
}

func runCobraParseCases(t *testing.T, binary string, cases []cobraParseCase, docs cobraParseDocuments) {
	t.Helper()
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	workers := 8
	jobs := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if err := runCobraParseCase(binary, filepath.Join(base, fmt.Sprintf("c%05d", index)), &cases[index], docs); err != nil {
					mu.Lock()
					failures = append(failures, fmt.Sprintf("%v: %v", cases[index].Args, err))
					mu.Unlock()
				}
			}
		}()
	}
	for index := range cases {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	if len(failures) > 0 {
		sort.Strings(failures)
		t.Fatalf("%d cobra parse cases failed to run, first: %s", len(failures), failures[0])
	}
}

func runCobraParseCase(binary, root string, c *cobraParseCase, docs cobraParseDocuments) error {
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(root) }()
	for rel, content := range c.Files {
		path := filepath.Join(home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
	}
	environment := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"), "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH=" + root}
	var keys []string
	for key := range c.Env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environment = append(environment, key+"="+c.Env[key])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	//nolint:gosec // binary is the locally built symdesk fixture; arguments come from generated cases.
	cmd := exec.CommandContext(ctx, binary, c.Args...)
	cmd.Dir, cmd.Env = root, environment
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("timeout")
		}
		exit, ok := err.(*exec.ExitError)
		if !ok {
			return err
		}
		code = exit.ExitCode()
	}
	normalize := func(text string) string {
		text = strings.ReplaceAll(text, home, "$HOME")
		text = strings.ReplaceAll(text, root, "$ROOT")
		if reference, ok := docs.byText[text]; ok {
			return reference
		}
		// `Unknown help topic [...]` is followed by the root usage text.
		if prefix, found := strings.CutSuffix(text, docs.rootUsage); found && prefix != "" {
			return prefix + "@root_usage"
		}
		return text
	}
	c.ExitCode, c.Stdout, c.Stderr = code, normalize(stdout.String()), normalize(stderr.String())
	return nil
}

// ---------------------------------------------------------------------------
// case generation

type cobraParseBuilder struct {
	cases []cobraParseCase
	seen  map[string]bool
}

func (b *cobraParseBuilder) add(family string, args ...string) {
	b.addWith(family, nil, nil, args...)
}

func (b *cobraParseBuilder) addWith(family string, env, files map[string]string, args ...string) {
	key := family + "\x00" + fmt.Sprint(args) + "\x00" + fmt.Sprint(env) + "\x00" + fmt.Sprint(files)
	if b.seen[key] {
		return
	}
	b.seen[key] = true
	b.cases = append(b.cases, cobraParseCase{Family: family, Args: append([]string{}, args...), Env: env, Files: files})
}

func join(parts ...[]string) []string {
	var out []string
	for _, part := range parts {
		out = append(out, part...)
	}
	return out
}

func generateCobraParseCases(nodes []cobraParseNode) []cobraParseCase {
	b := &cobraParseBuilder{seen: map[string]bool{}}
	generateVersionCases(b)
	generateTreeCases(b, nodes)
	generateFlagCases(b, nodes)
	generateHelpCases(b, nodes)
	generateCompleteCases(b, nodes)
	return b.cases
}

func generateVersionCases(b *cobraParseBuilder) {
	for _, args := range [][]string{
		{"version"}, {"version", "--json"}, {"--json", "version"}, {"version", "--json=true"}, {"version", "--json=false"},
		{"version", "--json=x"}, {"version", "--json="}, {"version", "--json", "false"},
		{"version", "--output", "json"}, {"version", "--output=json"}, {"--output", "json", "version"}, {"--output=json", "version"},
		{"version", "--output", "text"}, {"version", "--output", "yaml"}, {"version", "--output=yaml"}, {"version", "--output", "bad"},
		{"version", "--output="}, {"version", "--output"}, {"version", "--output", ""}, {"version", "--output", "JSON"},
		{"version", "--output", "json", "--output", "text"}, {"version", "--output", "text", "--output", "json"},
		{"version", "--json", "--output", "text"}, {"version", "--output", "json", "--json=false"}, {"--json", "--output=text", "version"},
		{"version", "extra"}, {"version", "extra", "more"}, {"version", "extra", "--json"}, {"version", "--json", "extra"},
		{"version", "--", "--json"}, {"version", "--json", "--", "extra"}, {"version", "--", "--bogus"}, {"--", "version"}, {"-", "version"}, {"version", "-"},
		{"version", "--vault", "x"}, {"--vault", "x", "version"}, {"version", "--vault"}, {"version", "--vault="}, {"--vault=x", "version"},
		{"--version"}, {"-v"}, {"--version=true"}, {"--version=false"}, {"--version=x"}, {"--version", "version"}, {"-v", "version"},
		{"-v", "--json"}, {"--json", "--version"}, {"--output", "json", "--version"}, {"--output=bad", "--version"}, {"--vault", "x", "--version"}, {"--version", "--help"}, {"--help", "--version"},
		{"-v=true"}, {"-v=false"}, {"-vv"}, {"-vh"}, {"-hv"}, {"-vx"}, {"-v", "x"}, {"--version", "extra"}, {"extra", "--version"},
		{"version", "--version"}, {"version", "-v"}, {"version", "--version=false"}, {"completion", "--version"}, {"ls", "-v"},
		{"version", "--help"}, {"version", "-h"}, {"help", "version"}, {"version", "--help=false"}, {"version", "--help=x"},
		{"version", "--bogus"}, {"version", "-x"}, {"version", "--json", "--bogus"}, {"version", "--output", "json", "--bogus"}, {"version", "--json=x", "--bogus"},
		{"--bogus", "version"}, {"-x", "version"}, {"version", "---"}, {"version", "--=x"},
	} {
		b.add("version", args...)
	}
	// cobra.OnInitialize(initConfig) runs for every executed command but not
	// for --help/--version or a parse failure: a broken config file proves
	// which commands never read it.
	broken := map[string]string{"config/symdesk/config.toml": "this is [not toml\n"}
	for _, args := range [][]string{{"--version"}, {"version", "--help"}, {"--help"}, {"help", "--help"}, {"ls", "--help"}, {"version", "extra", "--bogus"}, {"version", "-x"}, {"version", "--output"}, {"--bogus", "version"}} {
		b.addWith("version", nil, broken, args...)
	}
}

func generateTreeCases(b *cobraParseBuilder, nodes []cobraParseNode) {
	for _, node := range nodes {
		tokens := node.tokens()
		if len(tokens) == 0 {
			continue
		}
		// Aliases resolve to the same command.
		for _, alias := range node.Aliases {
			alt := append(append([]string{}, tokens[:len(tokens)-1]...), alias)
			b.add("tree", join(alt, []string{"--help"})...)
			b.add("tree", join(alt, []string{"--bogus"})...)
		}
		if node.Hidden {
			b.add("tree", join(tokens, []string{"--help"})...)
		}
		// Groups are not runnable: any words after them still print help.
		if !node.Runnable {
			b.add("tree", tokens...)
			b.add("tree", join(tokens, []string{"bogus-word"})...)
			b.add("tree", join(tokens, []string{"bogus-word", "more"})...)
			b.add("tree", join(tokens, []string{"--json"})...)
			b.add("tree", join(tokens, []string{"--bogus"})...)
			b.add("tree", join(tokens, []string{"--", "--bogus"})...)
		}
		// Rejected argument counts fail before any command body runs.
		if node.Runnable && !node.HasSubcommands && len(node.Probes) > 0 {
			for _, probe := range node.Probes {
				if probe.Accepted {
					continue
				}
				positional := make([]string, 0, probe.Count)
				for i := 1; i <= probe.Count; i++ {
					positional = append(positional, fmt.Sprintf("arg-%d", i))
				}
				b.add("tree", join(tokens, positional)...)
				if probe.Count == 1 || probe.Count == 3 {
					b.add("tree", join([]string{"--json"}, tokens, positional)...)
					b.add("tree", join(tokens, []string{"--json"}, positional)...)
					b.add("tree", join(tokens, positional, []string{"--json"})...)
				}
			}
			// Counts beyond the recorded probes follow the same validator.
			many := make([]string, 0, 12)
			for i := 1; i <= 12; i++ {
				many = append(many, fmt.Sprintf("arg-%d", i))
			}
			if last := node.Probes[len(node.Probes)-1]; !last.Accepted {
				b.add("tree", join(tokens, many)...)
			}
		}
	}
	// Unknown top-level commands and Cobra's suggestions.
	for _, word := range []string{"bogus", "lss", "l", "ls2", "LS", "Ls", "a", "as", "ai", "se", "sear", "serach", "sarch", "sources", "source", "ver", "versio", "versions", "hel", "helpp", "complet", "completion2", "x", "xx", "doc", "docs2", "relate", "relat", "relation", "tag", "tagz", "indx", "inde", "trash2", "perm", "prm", "con", "cond", "conf", "confi", "conflic", "d", "da", "dat", "e", "ex", "exp", "m", "ma", "mai", "mc", "mcpp", "n", "no", "not", "notes", "p", "pr", "pro", "r", "re", "rec", "s", "se", "ser", "t", "tr", "v", "vi", "vie", "w", "wo", "work", "__complet", "__completeNoDes", "-", "é", "ö", "\u00e9x", "a b", "`", "a\"b", "a\\b", "a\nb", "\t", "😀"} {
		b.add("tree", word)
		b.add("tree", "--json", word)
		b.add("tree", "--output", "json", word)
		b.add("tree", word, "--json")
		b.add("tree", word, "--bogus")
		b.add("tree", word, "extra")
	}
	b.add("tree", "")
	b.add("tree", "", "version")
	b.add("tree", "version", "")
	b.add("tree", "--", "")
	b.add("tree")
	b.add("tree", "--json")
	b.add("tree", "--output", "json")
	b.add("tree", "--output=bad")
	b.add("tree", "--vault", "x")
	b.add("tree", "--json", "--help")
}

type cobraParseValues struct {
	valid   [][]string
	invalid [][]string
}

func flagValueForms(kind string, name string) cobraParseValues {
	long := "--" + name
	switch kind {
	case "bool":
		return cobraParseValues{
			valid:   [][]string{{long}, {long + "=true"}, {long + "=false"}, {long + "=1"}, {long + "=F"}, {long, "false"}},
			invalid: [][]string{{long + "=maybe"}, {long + "="}, {long + "=yes"}, {long + "=TRUe"}},
		}
	case "string":
		return cobraParseValues{
			valid:   [][]string{{long + "=v"}, {long, "v"}, {long + "="}, {long, ""}, {long, "-x"}, {long + "=--help=1"}},
			invalid: nil,
		}
	case "int", "int64":
		return cobraParseValues{
			valid:   [][]string{{long + "=7"}, {long, "5"}, {long + "=0x10"}, {long + "=-3"}, {long + "=+4"}, {long + "=1_000"}, {long + "=0b11"}, {long + "=0o17"}, {long + "=017"}},
			invalid: [][]string{{long + "=abc"}, {long + "=1.5"}, {long + "="}, {long + "=99999999999999999999"}, {long + "=1__0"}, {long + "=0x"}, {long, "word"}, {long + "= 1"}, {long + "=-"}, {long + "=9223372036854775808"}},
		}
	case "duration":
		return cobraParseValues{
			valid:   [][]string{{long + "=1h30m"}, {long, "1.5s"}, {long, "0"}, {long + "=-2m"}, {long + "=300ms"}},
			invalid: [][]string{{long + "=1"}, {long + "=x"}, {long + "=1x"}, {long + "="}, {long + "=9999999999999h"}, {long, "bad"}, {long + "=.s"}},
		}
	case "stringArray":
		return cobraParseValues{
			valid:   [][]string{{long, "a", long, "b"}, {long + "=a", long + "=b,c"}, {long + "="}},
			invalid: nil,
		}
	}
	return cobraParseValues{}
}

func isValueTaking(kind string) bool { return kind != "bool" }

// cobraParseFullNodes get the complete flag battery; every other node gets a
// reduced one so the fixture stays reviewable. The sample spans every flag
// type, groups, aliases, hidden commands, generated commands and flag groups.
var cobraParseFullNodes = map[string]bool{
	"symdesk": true, "symdesk version": true, "symdesk ls": true, "symdesk export": true, "symdesk index status": true,
	"symdesk dataset sync": true, "symdesk meeting publish": true, "symdesk ai autofill": true, "symdesk completion": true,
	"symdesk completion bash": true, "symdesk help": true, "symdesk asset store": true, "symdesk search": true,
	"symdesk search export": true, "symdesk sources": true, "symdesk perm": true, "symdesk duplicates": true,
	"symdesk worker": true, "symdesk ingest reocr": true, "symdesk ingest rotate": true, "symdesk paperless migrate": true,
	"symdesk ai-config set": true, "symdesk doc": true, "symdesk note new": true, "symdesk conflict resolve": true,
	"symdesk notebook generate": true,
}

func generateFlagCases(b *cobraParseBuilder, nodes []cobraParseNode) {
	inherited := []cobraParseFlag{}
	for _, node := range nodes {
		if node.Path == "symdesk" {
			inherited = node.Persistent
		}
	}
	for _, node := range nodes {
		tokens := node.tokens()
		isRoot := len(tokens) == 0
		full := cobraParseFullNodes[node.Path]
		// A command body is never reached here: every case ends in --help or
		// fails in Cobra's parsing, argument-count, required-flag or flag-group
		// validation. Only commands whose body is a fixed text may run.
		bodySafe := !node.Runnable || node.Path == "symdesk version" || node.Path == "symdesk help" || strings.HasPrefix(node.Path, "symdesk completion")
		// Command-local flags.
		for _, flag := range node.LocalFlags {
			if flag.Name == "help" {
				continue
			}
			forms := flagValueForms(flag.Type, flag.Name)
			valid, invalid := forms.valid, forms.invalid
			if !full {
				if len(valid) > 2 {
					valid = valid[:2]
				}
				if len(invalid) > 1 {
					invalid = invalid[:1]
				}
			}
			for _, form := range valid {
				b.add("flags", join(tokens, form, []string{"--help"})...)
			}
			for _, form := range invalid {
				b.add("flags", join(tokens, form, []string{"--help"})...)
				b.add("flags", join(tokens, form)...)
			}
			if isValueTaking(flag.Type) {
				b.add("flags", join(tokens, []string{"--" + flag.Name})...)
				b.add("flags", join(tokens, []string{"--help", "--" + flag.Name})...)
			}
			if len(tokens) > 0 {
				b.add("flags", join(tokens[:len(tokens)-1], []string{"--" + flag.Name + valueSuffix(flag.Type)}, tokens[len(tokens)-1:], []string{"--help"})...)
			}
			if flag.Type == "bool" {
				b.add("flags", join(tokens, []string{"--" + flag.Name, "--" + flag.Name + "=false", "--help"})...)
			}
			if flag.Shorthand != "" {
				b.add("flags", join(tokens, []string{"-" + flag.Shorthand, "--help"})...)
			}
		}
		// Inherited persistent flags at every argv position.
		tail := []string{"--help"}
		for _, flag := range inherited {
			forms := flagValueForms(flag.Type, flag.Name)
			valid, invalid := forms.valid, forms.invalid
			if !full {
				if len(valid) > 2 {
					valid = valid[:2]
				}
				if len(invalid) > 1 {
					invalid = invalid[:1]
				}
			}
			for position := 0; position <= len(tokens); position++ {
				// Every position for the sampled nodes; the first and the
				// last (before and after the whole command path) elsewhere.
				if !full && position != 0 && position != len(tokens) {
					continue
				}
				for _, form := range valid {
					b.add("flags", join(tokens[:position], form, tokens[position:], tail)...)
				}
				for _, form := range invalid {
					b.add("flags", join(tokens[:position], form, tokens[position:], tail)...)
				}
			}
			if isValueTaking(flag.Type) {
				b.add("flags", join(tokens, []string{"--" + flag.Name})...)
			}
		}
		// Several persistent flags and repetition.
		b.add("flags", join([]string{"--json"}, tokens, []string{"--output", "yaml", "--vault", "v", "--help"})...)
		b.add("flags", join(tokens, []string{"--json", "--json=false", "--help"})...)
		b.add("flags", join(tokens, []string{"--output", "a", "--output", "b", "--help"})...)
		b.add("flags", join(tokens, []string{"--vault", "a", "--vault=b", "--json", "--help"})...)
		// Help flag spellings and positions.
		spellings := [][]string{{"--help"}, {"-h"}, {"--help=true"}, {"-hh"}, {"-hx"}, {"-h", "--bogus"}, {"--help=x"}}
		if full {
			spellings = append(spellings, []string{"--help", "--help"}, []string{"--bogus", "-h"}, []string{"-xh"}, []string{"-h=true"}, []string{"-h", "extra"})
		}
		if bodySafe {
			spellings = append(spellings, []string{"--help=false"}, []string{"-h=false"}, []string{"-h=0"}, []string{"--help=false", "--help"}, []string{"--help", "--help=false"})
		}
		for _, spelling := range spellings {
			b.add("flags", join(tokens, spelling)...)
			if len(tokens) > 0 && full {
				b.add("flags", join(spelling, tokens)...)
				b.add("flags", join(tokens[:len(tokens)-1], spelling, tokens[len(tokens)-1:])...)
			}
		}
		// Unknown flags and malformed flag syntax.
		bads := [][]string{{"--zzz"}, {"-z"}, {"---"}, {"--=x"}, {"-é"}}
		if full {
			bads = append(bads, []string{"--zzz=1"}, []string{"--zzz", "v"}, []string{"-z=1"}, []string{"-zh"}, []string{"--="}, []string{"---x"}, []string{"-\u00ff"}, []string{"-htest.v"}, []string{"--Json"}, []string{"--JSON=true"}, []string{"--js"}, []string{"--json-"}, []string{"-j"}, []string{"-o", "json"}, []string{"-0"})
		}
		for _, bad := range bads {
			b.add("flags", join(tokens, bad)...)
			b.add("flags", join(tokens, bad, []string{"--help"})...)
			if full {
				b.add("flags", join(tokens, []string{"--json"}, bad)...)
				b.add("flags", join(tokens, []string{"--output", "json"}, bad)...)
				if len(tokens) > 0 {
					b.add("flags", join(tokens[:1], bad, tokens[1:], []string{"--help"})...)
				}
				if isRoot {
					b.add("flags", join(bad, []string{"version"})...)
				}
			}
		}
		// The terminator: --help before it still wins; after it, words are
		// positional. Only a group (never run) may see words after it alone.
		b.add("flags", join(tokens, []string{"--help", "--", "x"})...)
		b.add("flags", join(tokens, []string{"-test.v", "--help"})...)
		if !node.Runnable {
			b.add("flags", join(tokens, []string{"--", "--help"})...)
			b.add("flags", join(tokens, []string{"--json", "--", "--bogus"})...)
		}
		// Required flags are validated after the argument count, before the
		// body runs.
		if required := node.requiredFlags(); len(required) > 0 && node.Runnable {
			count, _ := node.leastAccepted()
			positional := make([]string, 0, count)
			for i := 1; i <= count; i++ {
				positional = append(positional, fmt.Sprintf("arg-%d", i))
			}
			b.add("flags", join(tokens, positional)...)
			b.add("flags", join([]string{"--json"}, tokens, positional)...)
			b.add("flags", join(tokens, positional, []string{"--output", "json"})...)
			b.add("flags", join(tokens, positional, []string{"--output", "bad"})...)
			b.add("flags", join(tokens, positional, []string{"--output", "yaml"})...)
		}
	}
	for _, args := range [][]string{
		{"export", "--note", "a", "--view", "b"}, {"export", "--view", "b", "--note", "a"}, {"export", "--note=a", "--view=b"},
		{"--json", "export", "--note", "a", "--view", "b"}, {"export", "--note", "a", "--view", "b", "--output", "json"},
		{"export", "--note", "a", "--note", "b", "--view", "c"}, {"export", "--note", "a", "--view", "b", "--help"}, {"export", "--note", "a", "--view", "b", "--bogus"},
		{"ls", "--dir"}, {"ls", "--bogus"}, {"ls", "--output", "bad"}, {"ls", "--json=maybe"},
		{"search", "--bogus"}, {"search", "--json=maybe"}, {"search", "--output", "bad"},
	} {
		b.add("flags", args...)
	}
}

func valueSuffix(kind string) string {
	if kind == "bool" {
		return ""
	}
	switch kind {
	case "int", "int64":
		return "=1"
	case "duration":
		return "=1s"
	}
	return "=v"
}

func generateHelpCases(b *cobraParseBuilder, nodes []cobraParseNode) {
	b.add("help")
	b.add("help", "--help")
	b.add("help", "-h")
	b.add("help", "help")
	b.add("help", "help", "help")
	b.add("help", "--json", "help")
	b.add("help", "--output", "json", "help")
	b.add("help", "--output", "bad", "help")
	b.add("help", "help", "--output", "bad")
	b.add("help", "--bogus")
	b.add("help", "help", "--bogus")
	b.add("help", "help", "--")
	b.add("help", "help", "--", "ls")
	b.add("help", "help", "-", "ls")
	b.add("help", "--vault", "x", "help", "ls")
	for _, topics := range [][]string{
		{"nope"}, {"nope", "more"}, {"ls", "nope"}, {"asset", "nope"}, {"asset", "store", "nope"}, {"completion", "nope"}, {"`"}, {"a`b", "c"}, {"a\"b"}, {"\t"}, {"é"}, {"\u00a0"}, {"-"}, {""}, {"ls", ""}, {"--"}, {"__complete"}, {"__completeNoDesc"},
		{"help", "nope"}, {"help", "ls"}, {"ls", "extra", "more"},
	} {
		b.add("help", join([]string{"help"}, topics)...)
		b.add("help", join([]string{"help", "--json"}, topics)...)
		b.add("help", join([]string{"--json", "help"}, topics)...)
	}
	for _, node := range nodes {
		tokens := node.tokens()
		if len(tokens) == 0 {
			continue
		}
		b.add("help", join([]string{"help"}, tokens)...)
		b.add("help", join([]string{"help"}, tokens, []string{"extra"})...)
		b.add("help", join([]string{"help", "--json"}, tokens)...)
		for _, alias := range node.Aliases {
			alt := append(append([]string{}, tokens[:len(tokens)-1]...), alias)
			b.add("help", join([]string{"help"}, alt)...)
		}
	}
	// Shell completion scripts and their flags.
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		for _, args := range [][]string{
			{"completion", shell}, {"completion", shell, "--no-descriptions"}, {"completion", shell, "--no-descriptions=true"}, {"completion", shell, "--no-descriptions=false"},
			{"completion", shell, "--no-descriptions=x"}, {"completion", "--no-descriptions", shell}, {"--json", "completion", shell}, {"completion", "--json", shell}, {"completion", shell, "--json"},
			{"--output", "json", "completion", shell}, {"completion", shell, "--output", "bad"}, {"completion", shell, "--output=text"}, {"completion", shell, "--vault", "x"},
			{"completion", shell, "extra"}, {"completion", shell, "extra", "--no-descriptions"}, {"completion", shell, "--help"}, {"completion", shell, "-h"}, {"help", "completion", shell},
			{"completion", shell, "--bogus"}, {"completion", shell, "-x"}, {"completion", shell, "--no-descriptions", "--help"}, {"completion", shell, "--"}, {"completion", "--", shell},
			{"completion", shell, "--json", "--bogus"}, {"completion", shell, "--no-descriptions"}, {"completion", shell, "-n"},
		} {
			b.add("help", args...)
		}
	}
	for _, args := range [][]string{
		{"completion"}, {"completion", "--help"}, {"completion", "nope"}, {"completion", "tcsh"}, {"completion", "--no-descriptions"}, {"completion", "bash", "zsh"}, {"completion", "Bash"}, {"completion", "bash", "--no-descriptions", "zsh"},
	} {
		b.add("help", args...)
	}
	broken := map[string]string{"config/symdesk/config.toml": "this is [not toml\n"}
	for _, args := range [][]string{{"completion", "bash", "--help"}, {"completion", "bash", "--bogus"}, {"completion", "bash", "--no-descriptions=x"}, {"asset"}, {"ls", "--help"}, {"completion"}, {"help", "--help"}, {"completion", "--help"}} {
		b.addWith("help", nil, broken, args...)
	}
}

func generateCompleteCases(b *cobraParseBuilder, nodes []cobraParseNode) {
	for _, request := range []string{"__complete", "__completeNoDesc"} {
		b.add("complete", request)
		b.add("complete", request, "--json")
		b.add("complete", request, "--help")
		b.add("complete", request, "-h")
		b.add("complete", request, "")
		b.add("complete", request, "--")
		b.add("complete", request, "-")
		b.add("complete", request, "--json", "")
		b.add("complete", request, "--bogus", "")
		b.add("complete", "--json", request, "")
		b.add("complete", "--output", "json", request, "")
		b.add("complete", "--output", "bad", request, "")
		b.add("complete", request, "bogus", "")
		b.add("complete", request, "bogus")
		b.add("complete", request, "v")
		b.add("complete", request, "ver")
		b.add("complete", request, "version", "")
		b.add("complete", request, "version", "--")
		b.add("complete", request, "--version")
		b.add("complete", request, "--ver")
		b.add("complete", request, "--vault", "")
		b.add("complete", request, "--vault=")
		b.add("complete", request, "--vault=x")
		b.add("complete", request, "--output", "")
		b.add("complete", request, "--output=")
		b.add("complete", request, "--json", "--")
		b.add("complete", request, "--json", "")
		b.add("complete", request, "--json=false", "--")
		b.add("complete", request, "help", "")
		b.add("complete", request, "help", "ls", "")
		b.add("complete", request, "help", "asset", "")
		b.add("complete", request, "help", "completion", "")
		b.add("complete", request, "help", "bogus", "")
		b.add("complete", request, "help", "")
		b.add("complete", request, "help", "-")
		b.add("complete", request, "completion", "")
		b.add("complete", request, "completion", "bash", "")
		b.add("complete", request, "completion", "bash", "--")
		b.add("complete", request, "completion", "bash", "--no-descriptions", "")
		b.add("complete", request, "__complete", "")
		b.add("complete", request, "__complete", "ls", "")
		b.add("complete", request, "ls", "--dir", "")
		b.add("complete", request, "ls", "--dir=", "")
		b.add("complete", request, "ls", "--dir=x", "")
		b.add("complete", request, "ls", "--dir", "x", "--")
		b.add("complete", request, "ls", "--dir", "x", "")
		b.add("complete", request, "ls", "--di")
		b.add("complete", request, "ls", "--dir=v", "--")
		b.add("complete", request, "ls", "-h", "")
		b.add("complete", request, "ls", "--help", "--")
		b.add("complete", request, "ls", "--bogus", "")
		b.add("complete", request, "ls", "--bogus=1", "")
		b.add("complete", request, "ls", "-z", "")
		b.add("complete", request, "ls", "-z")
		b.add("complete", request, "ls", "--", "")
		b.add("complete", request, "ls", "--", "--")
		b.add("complete", request, "ls", "x", "")
		b.add("complete", request, "ls", "x", "--")
		b.add("complete", request, "export", "--")
		b.add("complete", request, "export", "--note", "a", "--")
		b.add("complete", request, "export", "--note=a", "--")
		b.add("complete", request, "export", "--view", "a", "--")
		b.add("complete", request, "export", "--note", "a", "--view", "b", "--")
		b.add("complete", request, "ai", "autofill", "--")
		b.add("complete", request, "ai", "autofill", "")
		b.add("complete", request, "ai", "autofill", "--property", "x", "--")
		b.add("complete", request, "dataset", "sync", "--")
		b.add("complete", request, "dataset", "sync", "")
		b.add("complete", request, "dataset", "sync", "a", "b", "--")
		b.add("complete", request, "index", "status", "--")
		b.add("complete", request, "index", "status", "--worker")
		b.add("complete", request, "index", "status", "--timeout", "")
		b.add("complete", request, "meeting", "publish", "--fact", "a", "--")
		b.add("complete", request, "meeting", "publish", "--fact", "a", "--fact", "b", "--")
		b.add("complete", request, "note", "new", "--")
		b.add("complete", request, "search", "export", "--")
		b.add("complete", request, "save-note", "")
		b.add("complete", request, "search", "save-note", "--")
		b.add("complete", request, "search", "keep", "--")
		b.add("complete", request, "search", "")
		b.add("complete", request, "sources", "")
		b.add("complete", request, "source", "")
	}
	env := func(pairs ...string) map[string]string {
		m := map[string]string{}
		for i := 0; i+1 < len(pairs); i += 2 {
			m[pairs[i]] = pairs[i+1]
		}
		return m
	}
	for _, e := range []map[string]string{
		env("SYMDESK_COMPLETION_DESCRIPTIONS", "0"), env("SYMDESK_COMPLETION_DESCRIPTIONS", "false"), env("SYMDESK_COMPLETION_DESCRIPTIONS", "1"), env("SYMDESK_COMPLETION_DESCRIPTIONS", "x"),
		env("COBRA_COMPLETION_DESCRIPTIONS", "0"), env("COBRA_COMPLETION_DESCRIPTIONS", "0", "SYMDESK_COMPLETION_DESCRIPTIONS", "1"), env("SYMDESK_ACTIVE_HELP", "0"), env("COBRA_ACTIVE_HELP", "0"),
	} {
		b.addWith("complete", e, nil, "__complete", "")
		b.addWith("complete", e, nil, "__complete", "ls", "--")
		b.addWith("complete", e, nil, "__completeNoDesc", "ls", "--")
	}
	for _, node := range nodes {
		tokens := node.tokens()
		if len(tokens) == 0 {
			continue
		}
		b.add("complete", join([]string{"__complete"}, tokens, []string{""})...)
		b.add("complete", join([]string{"__complete"}, tokens, []string{"-"})...)
		b.add("complete", join([]string{"__complete"}, tokens, []string{"--"})...)
		b.add("complete", join([]string{"__complete"}, tokens, []string{"--h"})...)
		b.add("complete", join([]string{"__complete"}, tokens, []string{"-h", ""})...)
		b.add("complete", join([]string{"__completeNoDesc"}, tokens, []string{""})...)
		b.add("complete", join([]string{"__completeNoDesc"}, tokens, []string{"--"})...)
		b.add("complete", join([]string{"__complete", "--json"}, tokens, []string{"--"})...)
		b.add("complete", join([]string{"__complete"}, tokens[:len(tokens)-1], []string{tokens[len(tokens)-1][:1]})...)
		b.add("complete", join([]string{"__complete"}, tokens, []string{"x", ""})...)
		for _, alias := range node.Aliases {
			alt := append(append([]string{}, tokens[:len(tokens)-1]...), alias)
			b.add("complete", join([]string{"__complete"}, alt, []string{"--"})...)
		}
		for _, flag := range node.LocalFlags {
			if flag.Name == "help" {
				continue
			}
			b.add("complete", join([]string{"__complete"}, tokens, []string{"--" + flag.Name, ""})...)
			b.add("complete", join([]string{"__complete"}, tokens, []string{"--" + flag.Name + "="})...)
			b.add("complete", join([]string{"__complete"}, tokens, []string{"--" + flag.Name + valueSuffix(flag.Type), "--"})...)
			b.add("complete", join([]string{"__complete"}, tokens, []string{"--" + flag.Name[:1]})...)
		}
	}
}

// encodeCobraParseFixture writes the header, then one case per line.
func encodeCobraParseFixture(fixture cobraParseFixture) ([]byte, error) {
	cases := fixture.Cases
	fixture.Cases = nil
	header, err := json.MarshalIndent(fixture, "", " ")
	if err != nil {
		return nil, err
	}
	// Reopen the object: drop `"cases": null` and the closing brace.
	text := strings.TrimSuffix(string(header), "\n}")
	text = strings.TrimSuffix(text, "\"cases\": null")
	var buffer bytes.Buffer
	buffer.WriteString(text)
	buffer.WriteString("\"cases\": [\n")
	for index, c := range cases {
		line, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		buffer.WriteString("  ")
		buffer.Write(line)
		if index+1 < len(cases) {
			buffer.WriteByte(',')
		}
		buffer.WriteByte('\n')
	}
	buffer.WriteString(" ]\n}\n")
	return buffer.Bytes(), nil
}
