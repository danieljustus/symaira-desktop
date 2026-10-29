// Command templatepathgen freezes the Go storage-path template contract
// (internal/templatepath) as a Rust replay fixture: Compile acceptance and
// error text, Eval over field maps (date parsing, strftime directives, path
// segment sanitizing, Clean) and Disambiguate.
//
// Host independence: the corpus uses no backslash or drive-letter inputs, so
// filepath.Clean and filepath.Ext agree on every supported OS, with one
// exception. On Windows, filepath.Clean prefixes `.\` to a cleaned relative
// path whose first element contains ':' (so `a/../c:` cannot become the drive
// `c:`), and Eval's filepath.ToSlash then returns `./10:30 meeting` where Unix
// returns `10:30 meeting`. A fixture that records such an output would fail
// the byte check on every Windows host, so every Eval case whose output has a
// ':' in its first path element is withheld and counted in the header, except
// when the colon is the second byte: Windows reads `x:` as a volume name and
// Clean then adds no prefix. The rule is deliberately broader than the exact
// divergence (Clean only prefixes when it rewrote the path), because the
// generator sees the cleaned output, not the string handed to Clean.
//
// Go's strftime applies its directive table by ranging over a map, so a
// format whose substitution output can complete another directive (for
// example `%%a`, which becomes `%Sun`, then `%S`) depends on the random map
// order. Every Eval case is run many times; a case that does not settle on a
// single output is not recorded and is counted in the header.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/templatepath"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const (
	defaultOutput = "testdata/port/core/template-path.json"
	stabilityRuns = 64
)

type document struct {
	SchemaVersion int              `json:"schema_version"`
	Oracle        inventory.Oracle `json:"oracle"`
	// NondeterministicSkipped counts Eval cases withheld because Go's map
	// iteration order changed their output between runs.
	NondeterministicSkipped int `json:"nondeterministic_skipped"`
	// HostDependentSkipped counts Eval cases withheld because the Windows
	// filepath.Clean spelling of their output differs from the Unix one.
	HostDependentSkipped int   `json:"host_dependent_skipped"`
	Cases                cases `json:"cases"`
}

type cases struct {
	Compile      []compileCase      `json:"compile"`
	Eval         []evalCase         `json:"eval"`
	Disambiguate []disambiguateCase `json:"disambiguate"`
}

type compileCase struct {
	ID      string `json:"id"`
	Raw     string `json:"raw"`
	Error   string `json:"error,omitempty"`
	RawOut  string `json:"raw_out"`
	IsEmpty bool   `json:"is_empty"`
}

type evalCase struct {
	ID       string            `json:"id"`
	Template string            `json:"template"`
	Fields   map[string]string `json:"fields"`
	Output   string            `json:"output"`
}

type disambiguateCase struct {
	ID     string   `json:"id"`
	Target string   `json:"target"`
	Known  []string `json:"known"`
	Output string   `json:"output"`
}

func main() {
	output := flag.String("output", defaultOutput, "fixture path")
	check := flag.Bool("check", false, "fail if fixture differs")
	commit := flag.String("oracle-commit", fixtureoracle.Defaults().Commit, "Go oracle commit")
	release := flag.String("oracle-release", fixtureoracle.Defaults().Release, "Go oracle release")
	flag.Parse()

	if *check {
		oracle, err := inventory.ResolveCheckOracle(inventory.Oracle{Commit: *commit, Release: *release}, flag.CommandLine, *output)
		if err != nil {
			fatal("resolve replay oracle: %v", err)
		}
		*commit, *release = oracle.Commit, oracle.Release
	}
	identityRoot, identityErr := fixtureoracle.FindRepositoryRoot(".")
	if identityErr != nil {
		fatal("resolve fixture source: %v", identityErr)
	}
	if identityErr := fixtureoracle.ValidateSource(identityRoot, inventory.Oracle{Commit: *commit, Release: *release}); identityErr != nil {
		fatal("verify selected fixture source: %v", identityErr)
	}

	doc := document{SchemaVersion: 1, Oracle: inventory.Oracle{Commit: *commit, Release: *release}}
	doc.Cases.Compile = buildCompile()
	doc.Cases.Eval, doc.NondeterministicSkipped, doc.HostDependentSkipped = buildEval()
	doc.Cases.Disambiguate = buildDisambiguate()

	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fatal("marshal: %v", err)
	}
	content = append(content, '\n')
	if *check {
		existing, err := os.ReadFile(*output)
		if err != nil {
			fatal("read fixture: %v", err)
		}
		if !bytes.Equal(existing, content) {
			fatal("template-path fixture drift; regenerate deliberately")
		}
		fmt.Println("PASS template-path fixture")
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0o750); err != nil {
		fatal("create fixture directory: %v", err)
	}
	if err := os.WriteFile(*output, content, 0o600); err != nil {
		fatal("write fixture: %v", err)
	}
	fmt.Printf("PASS template-path fixture generated (%d compile, %d eval, %d disambiguate, %d nondeterministic skipped, %d host-dependent skipped)\n",
		len(doc.Cases.Compile), len(doc.Cases.Eval), len(doc.Cases.Disambiguate), doc.NondeterministicSkipped, doc.HostDependentSkipped)
}

var compileRaws = []string{
	"",
	"   ",
	"\t\n",
	"\u00a0\u3000",
	" documents/{title} ",
	"{title}",
	"documents/{correspondent}/{document_date:%Y}/{title}",
	"{nonexistent}",
	"{tags}",
	"{category}/{tags}",
	"{created:%Y}",
	"{document_date}",
	"{document_date:}",
	"{document_date:%Y-%m}",
	"{ title }",
	"{}",
	"{title",
	"title}",
	"{title:%Y",
	"{a-b}",
	"{title}{title}",
	"{TITLE}",
	"{asn}/{asn}",
	"{ünï}",
	"{title:a}b}",
	"{title:\n}",
	"{\tfoo}",
	"x{person:%Y}y",
	"{person:%Y/%m}",
	"{document_type:%}",
	"{title:{}",
	"{{title}}",
	"{title}{",
	"}{title}",
	"a {title} b",
	"Ärzte/{person}/日本語",
	"..",
	"../",
	"../escape",
	"../../x",
	"a/../../b",
	"a/../b",
	"a/..",
	"a/b/../..",
	"a/b/../../..",
	"./x",
	"./",
	".",
	"./..",
	"a/./b",
	"a//b",
	"a/b/",
	"/abs/{title}",
	"/",
	"/../x",
	"/..",
	"{title}/..",
	"{title}/../..",
	"{correspondent}/../../x",
	"{title:%Y/../..}",
	"{title:%Y}/../../x",
	"{document_date:%Y/..}/x",
	"..\u00a0",
	"\u00a0..\u00a0",
	" ../x",
	"...",
	"..a/b",
	"a/..b",
	"a/b..",
	"{asn}..",
	"{person}.",
	"x/{title}/../{title}",
	"{missing_a}{missing_b}",
	"{document_date:%Y}{created:%m}{title}",
	"{document_date:%H:%M:%S}",
	"{document_date:%A, %d %B %Y}",
	"{_}",
	"{_x}",
	"{1}",
	"{9asn}",
	"{asn:}",
	"{asn:%}",
}

func buildCompile() []compileCase {
	result := make([]compileCase, 0, len(compileRaws))
	for index, raw := range compileRaws {
		item := compileCase{ID: fmt.Sprintf("compile-%03d", index), Raw: raw}
		tmpl, err := templatepath.Compile(raw)
		if err != nil {
			item.Error = err.Error()
		} else {
			item.RawOut = tmpl.Raw()
			item.IsEmpty = tmpl.IsEmpty()
		}
		result = append(result, item)
	}
	return result
}

type fieldSet struct {
	name   string
	fields map[string]string
}

var fieldSets = []fieldSet{
	{"empty", map[string]string{}},
	{"nil-like-empty-values", map[string]string{"correspondent": "", "title": "", "document_date": "", "created": ""}},
	{"normal", map[string]string{
		"correspondent": "ACME GmbH", "document_type": "Invoice", "document_date": "2026-03-05",
		"person": "Alice", "category": "Finance", "asn": "42", "title": "Quarterly Report",
		"created": "2026-03-05T14:07:09Z",
	}},
	{"tricky", map[string]string{
		"correspondent": "../../etc", "title": "a/b\\c", "person": ": leading", "asn": "  ",
		"document_date": "not-a-date", "category": "..", "document_type": ". x .", "created": "2026-02-30",
	}},
	{"unicode-control", map[string]string{
		"title": "tab\there\u0085nl\n", "correspondent": "Müller & Söhne", "person": "日本語",
		"asn": "\u00a0", "category": "x\u0000y", "document_type": "\u007fdel", "document_date": "2024-02-29",
	}},
	{"dotted", map[string]string{
		"title": "report.final.", "correspondent": ".hidden", "person": "...", "category": " . ",
		"document_type": "a.b.c", "asn": ".", "document_date": "2026-03", "created": "2026-03-05",
	}},
	{"colon", map[string]string{
		"title": "10:30 meeting", "person": ":", "correspondent": "::x", "category": "a:b",
		"document_date": "2026-03-05T14:07:09", "created": "2026-03-05T14:07:09+02:00",
	}},
}

var evalTemplates = []string{
	"",
	"   ",
	"{title}",
	"documents/{correspondent}/{document_date:%Y}/{title}",
	"{correspondent}/{document_type}/{document_date:%Y-%m-%d}_{title}",
	"{category}/{person}/{asn}",
	"{created}",
	"{created:%d.%m.%Y %H:%M}",
	"{document_date}",
	"{document_date:}",
	"{document_date:%Y/%m}",
	"x{person}y{asn}z",
	"{asn}",
	"a/./b//c/{title}",
	"a/b/../c/{title}",
	"/abs/{title}",
	"{title}/",
	"{title}/..",
	"./{title}",
	".",
	"literal/only",
	"{correspondent}{correspondent}",
	"{document_date:%Y}{created:%m}{title}",
	"{person}-{asn}",
	"{title}.pdf",
	"Ärzte/{person}/日本語/{title}",
}

var dateInputs = []string{
	"2026-03-05",
	"2026-03",
	"2026-3-5",
	"2026-03-5",
	"2026",
	"2026-03-05T14:07:09Z",
	"2026-03-05T14:07:09",
	"2026-03-05T14:07:09+02:00",
	"2026-03-05T14:07:09-05:30",
	"2026-03-05T14:07:09-00:00",
	"2026-03-05T14:07:09+00:00",
	"2026-03-05T14:07:09+24:00",
	"2026-03-05T14:07:09+25:00",
	"2026-03-05T14:07:09+02:60",
	"2026-03-05T14:07:09+0200",
	"2026-03-05T14:07:09+02",
	"2026-03-05T14:07:09.123456789Z",
	"2026-03-05T14:07:09.5",
	"2026-03-05T14:07:09,5Z",
	"2026-03-05T14:07:09.Z",
	"2026-03-05T14:07:09.",
	"2026-03-05T14:07:09z",
	"2026-03-05t14:07:09Z",
	"2026-03-05 14:07:09",
	"2026-03-05T14:07Z",
	"2026-03-05T14:07:09Zjunk",
	"2026-03-05T24:00:00Z",
	"2026-03-05T23:59:59Z",
	"2026-03-05T23:59:60Z",
	"2026-03-05T14:60:00Z",
	"2026-03-05T1:07:09Z",
	"2026-03-05T14:7:09Z",
	"2026-03-05T14:07:9Z",
	"2026-02-29",
	"2024-02-29",
	"2023-02-29",
	"2100-02-29",
	"2000-02-29",
	"2026-04-31",
	"2026-00-10",
	"2026-13-01",
	"2026-01-00",
	"2026-01-32",
	"0000-01-01",
	"0001-01-01",
	"0099-12-31",
	"0999-07-04",
	"1970-01-01",
	"1969-12-31",
	"9999-12-31",
	"2026-03-05 ",
	" 2026-03-05",
	"2026-03-05T",
	"+2026-03-05",
	"-2026-03-05",
	"20260305",
	"26-03-05",
	"2026/03/05",
	"05.03.2026",
	"March 5 2026",
	"",
	"   ",
	"2026-03-05T14:07:09+02:00:30",
	"2026-12-25",
	"2026-01-01",
	"2025-12-28",
	"2026-06-15",
	"2026-07-04",
	"2026-08-31",
	"2026-09-09",
	"2026-10-10",
	"2026-11-11",
	"2026-05-17",
	"2026-02-14",
}

var dateFormats = []string{
	"",
	"%Y-%m-%d",
	"%Y",
	"%y",
	"%m",
	"%d",
	"%H:%M:%S",
	"%H%M%S",
	"%b",
	"%B",
	"%a",
	"%A",
	"%Y/%m/%d",
	"%d.%m.%Y",
	"%y%m%d",
	"%B %d, %Y",
	"%A, %a %b %B",
	"%Y%",
	"%",
	"%%",
	"%%Y",
	"%%%Y",
	"%Z",
	"%z",
	"%e",
	"%j",
	"%p",
	"%I",
	"%-d",
	"%Y年%m月",
	"%%a",
	"%%A",
	"%%b",
	"%%B",
	"week-%a",
	"plain",
	"%Y %Y %Y",
	"%Y-%m-%d_%H-%M-%S",
	"Q%m",
	"%Y../%m",
	"../%Y",
	"%Y/..",
	" %Y ",
	".%Y.",
}

var sanitizeValues = []string{
	"plain",
	"  spaced  ",
	"a/b",
	"a\\b",
	"/leading",
	"trailing/",
	"..",
	"../..",
	"../../etc/passwd",
	".",
	"...",
	". .",
	" . ",
	"a.b.",
	".hidden",
	":lead",
	"mid:colon",
	"::two",
	"\u0000nul",
	"nul\u0000mid",
	"\u0001\u001f",
	"\u007f\u0080\u009f",
	"tab\tand\nnewline\r",
	"\u00a0nbsp\u00a0",
	"\u2028line sep\u2029",
	"Müller",
	"日本語/ファイル",
	"emoji 😀",
	"a  b",
	"a\u200bzero-width",
	"{title}",
	"%Y",
	"x\u0000",
	"\u0000",
	"//",
	"\\",
	"-",
	"_",
	"a/../b",
	"CON",
	"name.",
	"name ",
	" name",
	"\u3000wide\u3000",
	"a\ufffdb",
	"üüüüüüüüüüüü",
}

// hostDependentOutput reports whether the Windows spelling of an Eval output
// can differ from the Unix one: filepath.Clean inserts a leading `.\` when the
// first path element holds a ':' that does not form a `x:` volume name (see the
// package comment).
func hostDependentOutput(output string) bool {
	first, _, _ := strings.Cut(strings.TrimPrefix(output, "./"), "/")
	return strings.Contains(first, ":") && (len(first) < 2 || first[1] != ':')
}

func buildEval() (recorded []evalCase, nondeterministic, hostDependent int) {
	var candidates []evalCase
	add := func(id, template string, fields map[string]string) {
		candidates = append(candidates, evalCase{ID: id, Template: template, Fields: fields})
	}
	for ti, template := range evalTemplates {
		for _, set := range fieldSets {
			add(fmt.Sprintf("tmpl-%02d-%s", ti, set.name), template, set.fields)
		}
	}
	for di, date := range dateInputs {
		add(fmt.Sprintf("date-%03d-default", di), "{document_date}", map[string]string{"document_date": date})
		add(fmt.Sprintf("created-%03d-default", di), "{created}", map[string]string{"created": date})
		add(fmt.Sprintf("date-%03d-ymd-title", di), "{document_date:%Y/%m/%d}/{title}",
			map[string]string{"document_date": date, "title": "t"})
	}
	for fi, format := range dateFormats {
		for _, date := range []string{
			"2026-03-05T14:07:09Z", "2026-03-05T14:07:09+02:00", "2026-03", "2026-11-23", "0001-01-01",
			"0099-12-31", "2024-02-29", "2026-12-28", "2026-09-30", "2026-05-17", "bad",
		} {
			add(fmt.Sprintf("fmt-%02d-%s", fi, date), "{document_date:"+format+"}", map[string]string{"document_date": date})
		}
		add(fmt.Sprintf("fmt-%02d-created", fi), "{created:"+format+"}", map[string]string{"created": "2026-07-04T01:02:03Z"})
		add(fmt.Sprintf("fmt-%02d-nonDate-field", fi), "{title:"+format+"}", map[string]string{"title": "t", "document_date": "2026-03-05"})
	}
	for vi, value := range sanitizeValues {
		for _, field := range []string{"title", "correspondent"} {
			add(fmt.Sprintf("san-%02d-%s", vi, field), "{"+field+"}", map[string]string{field: value})
		}
		add(fmt.Sprintf("san-%02d-nested", vi), "p/{title}/q", map[string]string{"title": value})
		add(fmt.Sprintf("san-%02d-pair", vi), "{person}{category}", map[string]string{"person": value, "category": value})
		add(fmt.Sprintf("san-%02d-date-fallback", vi), "{document_date:%Y}", map[string]string{"document_date": value})
	}

	result := make([]evalCase, 0, len(candidates))
	for _, item := range candidates {
		tmpl, err := templatepath.Compile(item.Template)
		if err != nil {
			fatal("eval case %s: template does not compile: %v", item.ID, err)
		}
		first := tmpl.Eval(item.Fields)
		stable := true
		for run := 1; run < stabilityRuns; run++ {
			if tmpl.Eval(item.Fields) != first {
				stable = false
				break
			}
		}
		if !stable {
			nondeterministic++
			continue
		}
		if hostDependentOutput(first) {
			hostDependent++
			continue
		}
		item.Output = first
		result = append(result, item)
	}
	return result, nondeterministic, hostDependent
}

func buildDisambiguate() []disambiguateCase {
	type input struct {
		id     string
		target string
		known  []string
	}
	inputs := []input{
		{"absent", "a/b.pdf", nil},
		{"absent-with-others", "a/b.pdf", []string{"a/c.pdf", "x"}},
		{"taken-once", "a/b.pdf", []string{"a/b.pdf"}},
		{"taken-twice", "a/b.pdf", []string{"a/b.pdf", "a/b_1.pdf"}},
		{"taken-thrice", "a/b.pdf", []string{"a/b.pdf", "a/b_1.pdf", "a/b_2.pdf"}},
		{"gap", "a/b.pdf", []string{"a/b.pdf", "a/b_2.pdf"}},
		{"no-ext", "a/b", []string{"a/b"}},
		{"no-ext-twice", "a/b", []string{"a/b", "a/b_1"}},
		{"dotted-dir", "a.d/b", []string{"a.d/b"}},
		{"dotted-dir-ext", "a.d/b.md", []string{"a.d/b.md"}},
		{"multi-dot", "a/b.tar.gz", []string{"a/b.tar.gz"}},
		{"leading-dot", "a/.hidden", []string{"a/.hidden"}},
		{"leading-dot-twice", "a/.hidden", []string{"a/.hidden", "a/_1.hidden"}},
		{"trailing-dot", "a/b.", []string{"a/b."}},
		{"only-dot", ".", []string{"."}},
		{"empty", "", []string{""}},
		{"empty-absent", "", nil},
		{"unicode", "Ärzte/日本語.pdf", []string{"Ärzte/日本語.pdf"}},
		{"suffix-lookalike", "a/b_1.pdf", []string{"a/b_1.pdf"}},
		{"suffix-lookalike-chain", "a/b_1.pdf", []string{"a/b_1.pdf", "a/b_1_1.pdf"}},
		{"duplicates-in-known", "a/b.pdf", []string{"a/b.pdf", "a/b.pdf", "a/b_1.pdf"}},
		{"case-differs", "a/B.pdf", []string{"a/b.pdf"}},
		{"slash-ext", "a.b/c", []string{"a.b/c"}},
		{"many", "n/x.md", manyKnown("n/x", ".md", 25)},
	}
	result := make([]disambiguateCase, 0, len(inputs))
	for _, item := range inputs {
		known := map[string]bool{}
		for _, path := range item.known {
			known[path] = true
		}
		kn := item.known
		if kn == nil {
			kn = []string{}
		}
		result = append(result, disambiguateCase{ID: item.id, Target: item.target, Known: kn, Output: templatepath.Disambiguate(item.target, known)})
	}
	return result
}

func manyKnown(base, ext string, count int) []string {
	known := []string{base + ext}
	for i := 1; i < count; i++ {
		known = append(known, fmt.Sprintf("%s_%d%s", base, i, ext))
	}
	return known
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL "+strings.TrimSpace(format)+"\n", args...)
	os.Exit(1)
}
