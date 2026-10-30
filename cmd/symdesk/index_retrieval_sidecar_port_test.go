package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The extraction-sidecar import of `symdesk index` is only reachable when the
// Markdown *body* (the text after the document's own frontmatter) starts with
// a `sha256` frontmatter block: the vault parser strips the first block, and
// the hybrid retrieval engine then looks at the body it is handed. Every
// document here therefore carries two blocks: an outer one the vault consumes
// and an inner one the engine reads.

// Excluded on purpose: the engine's extension test is case-insensitive, but the
// bulk `symdesk index` walk does not visit an upper-case `.MD` file (observed
// on the first run), and `index retry` only reaches documents already recorded
// as failed. The sidecar import through `--re-embed` (Go's IndexFile reads the
// raw file, not the body) and a sidecar whose path cannot be opened (permission
// bits, privilege dependent) are likewise not driven here.

const indexRetrievalSidecarMaxLine = 1 << 20

type indexRetrievalStepWith func(id string, writes []indexRetrievalProcessFile, fills []indexRetrievalProcessFill, removes []string, args ...string)

// indexRetrievalSidecarSHA derives a stable, distinct 64-hex digest per case.
func indexRetrievalSidecarSHA(name string) string {
	sum := sha256.Sum256([]byte("symdesk-extraction-sidecar-" + name))
	return hex.EncodeToString(sum[:])
}

// indexRetrievalSidecarDoc renders a document whose body starts with inner.
func indexRetrievalSidecarDoc(title, inner, body string) string {
	return "---\ntitle: " + title + "\n---\n" + inner + body
}

func indexRetrievalSidecarBlock(sha string) string {
	return "---\nsha256: " + sha + "\n---\n"
}

func indexRetrievalSidecarPath(sha string) string {
	return ".symaira/extractions/" + sha + ".jsonl"
}

func indexRetrievalSidecarLong(word string) string {
	return "# Body\n\n" + strings.Repeat(word+" ", 150)
}

func observeIndexRetrievalSidecarSteps(t *testing.T, fixture *indexRetrievalProcessFixture, stepWith indexRetrievalStepWith) {
	t.Helper()
	step := func(id string, writes []indexRetrievalProcessFile, args ...string) {
		stepWith(id, writes, nil, nil, args...)
	}
	file := func(path, body string) indexRetrievalProcessFile {
		return indexRetrievalProcessFile{Path: path, Body: body}
	}
	run := []string{"index"}

	// Import: spans inside a chunk, inside the chunk overlap, past the last
	// chunk, negative, absent, and the JSON decoder's key/null/duplicate rules.
	main := indexRetrievalSidecarSHA("main")
	mainBody := indexRetrievalSidecarLong("invoice grounded extraction sidecar text")
	mainDoc := indexRetrievalSidecarDoc("Main", indexRetrievalSidecarBlock(main), mainBody)
	mainLines := []string{
		`{"field":"invoice_number","value":"INV-2026-001","span":{"start":0,"end":10,"snippet":"grounded invoice header"},"matched":true}`,
		`{"field":"overlap","value":"inside overlap","span":{"start":900,"end":950,"snippet":"overlap invoice"},"matched":true}`,
		`{"type":"tail","value":"late span","span":{"start":2500,"end":2600,"snippet":"tail grounded"},"matched":false}`,
		`{"field":"beyond","value":"past end","span":{"start":99990,"end":100000,"snippet":"beyond"},"matched":true}`,
		`{"field":"neg","value":"negative","span":{"start":-20,"end":-10,"snippet":"neg"},"matched":true}`,
		``,
		`   {"field":"nospan","value":"no span cafe","matched":true}   `,
		`{"Field":"CASE","VALUE":"mixed case keys","SPAN":{"START":5,"END":7,"SNIPPET":"x"},"MATCHED":true}`,
		`{"field":"unknown","value":"extra keys ignored","extra":[1,{"a":null}],"matched":false}`,
		`{"field":"dup","value":"first","value":"last wins","span":{"start":1,"end":2},"span":{"start":3},"matched":true,"matched":false}`,
		`null`,
		`{}`,
		`{"field":"","type":"fallback","value":"v"}`,
		`{"field":null,"value":null,"span":null,"matched":null}`,
		"{\"field\":\"crlf\",\"value\":\"carriage return\"}\r",
		`{"field":"caf\u00e9 cafe","value":"unicode \u00e9","span":{"start":9223372036854775807,"end":9223372036854775807,"snippet":"max int64"}}`,
	}
	step("sidecar-import", []indexRetrievalProcessFile{
		file("sc/main.md", mainDoc),
		file(indexRetrievalSidecarPath(main), strings.Join(mainLines, "\n")+"\n"),
	}, run...)
	step("sidecar-unchanged", nil, run...)

	// A sidecar edit alone does not re-import: the retrieval engine returns
	// early when the body hash is unchanged and nothing is pending.
	step("sidecar-edit-without-body-change", []indexRetrievalProcessFile{
		file(indexRetrievalSidecarPath(main), `{"field":"edited","value":"edited invoice","span":{"start":0,"end":1,"snippet":"grounded"},"matched":true}`+"\n"),
	}, run...)
	// A body change re-indexes: the old rows are dropped with the document and
	// the edited sidecar is imported against the new chunks.
	step("sidecar-body-change-reimport", []indexRetrievalProcessFile{
		file("sc/main.md", indexRetrievalSidecarDoc("Main", indexRetrievalSidecarBlock(main), indexRetrievalSidecarLong("invoice grounded changed body text"))),
	}, run...)
	// An empty sidecar imports successfully with zero rows.
	step("sidecar-empty-clears", []indexRetrievalProcessFile{
		file(indexRetrievalSidecarPath(main), ""),
		file("sc/main.md", indexRetrievalSidecarDoc("Main", indexRetrievalSidecarBlock(main), indexRetrievalSidecarLong("invoice grounded second body text"))),
	}, run...)
	// A sidecar that has vanished imports nothing and says nothing.
	stepWith("sidecar-removed", []indexRetrievalProcessFile{
		file("sc/main.md", indexRetrievalSidecarDoc("Main", indexRetrievalSidecarBlock(main), indexRetrievalSidecarLong("invoice grounded third body text"))),
	}, nil, []string{indexRetrievalSidecarPath(main)}, run...)

	// Not-detected shapes: each document has a valid sidecar for its digest
	// (or a near miss) that must stay unread.
	missing := indexRetrievalSidecarSHA("missing")
	directory := indexRetrievalSidecarSHA("directory")
	short := indexRetrievalSidecarSHA("short")[:63]
	notStart := indexRetrievalSidecarSHA("not-start")
	indented := indexRetrievalSidecarSHA("indented")
	noBlock := indexRetrievalSidecarSHA("no-block")
	unclosed := indexRetrievalSidecarSHA("unclosed")
	oneLine := `{"field":"never","value":"never imported invoice","matched":true}` + "\n"
	step("sidecar-not-detected", []indexRetrievalProcessFile{
		file("sc/missing.md", indexRetrievalSidecarDoc("Missing", indexRetrievalSidecarBlock(missing), "# Missing\n\nno sidecar file exists.\n")),
		file("sc/directory.md", indexRetrievalSidecarDoc("Directory", indexRetrievalSidecarBlock(directory), "# Directory\n\nthe sidecar path is a directory.\n")),
		file(indexRetrievalSidecarPath(directory)+"/keep.txt", "directory placeholder\n"),
		file("sc/short.md", indexRetrievalSidecarDoc("Short", "---\nsha256: "+short+"\n---\n", "# Short\n\ndigest has 63 hex digits.\n")),
		file(indexRetrievalSidecarPath(short), oneLine),
		file("sc/not-start.md", indexRetrievalSidecarDoc("Not start", "intro line before the block\n"+indexRetrievalSidecarBlock(notStart), "# Not start\n\nblock is not at the body start.\n")),
		file(indexRetrievalSidecarPath(notStart), oneLine),
		file("sc/indented.md", indexRetrievalSidecarDoc("Indented", "---\n  sha256: "+indented+"\n---\n", "# Indented\n\nkey is indented.\n")),
		file(indexRetrievalSidecarPath(indented), oneLine),
		file("sc/no-block.md", indexRetrievalSidecarDoc("No block", "", "# No block\n\nbody has no inner block, sha256: "+noBlock+"\n")),
		file(indexRetrievalSidecarPath(noBlock), oneLine),
		file("sc/unclosed.md", indexRetrievalSidecarDoc("Unclosed", "---\nsha256: "+unclosed+"\n", "# Unclosed\n\nblock is never closed.\n")),
		file(indexRetrievalSidecarPath(unclosed), oneLine),
	}, run...)

	// Digest spellings the engine's regular expressions accept.
	quoted := indexRetrievalSidecarSHA("quoted")
	upper := indexRetrievalSidecarSHA("upper")
	crlf := indexRetrievalSidecarSHA("crlf")
	later := indexRetrievalSidecarSHA("later-key")
	spaced := indexRetrievalSidecarSHA("spaced")
	blank := indexRetrievalSidecarSHA("blank-lines")
	one := func(name string) string {
		return `{"field":"` + name + `","value":"` + name + ` invoice","span":{"start":2,"end":4,"snippet":"grounded"},"matched":true}` + "\n"
	}
	step("sidecar-digest-forms", []indexRetrievalProcessFile{
		file("sc/quoted.md", indexRetrievalSidecarDoc("Quoted", "---\nsha256: \""+quoted+"\"\n---\n", "# Quoted\n\nquoted digest.\n")),
		file(indexRetrievalSidecarPath(quoted), one("quoted")),
		file("sc/upper.md", indexRetrievalSidecarDoc("Upper", "---\nsha256: "+strings.ToUpper(upper)+"\n---\n", "# Upper\n\nupper-case digest names a lower-case file.\n")),
		file(indexRetrievalSidecarPath(upper), one("upper")),
		file("sc/crlf.md", indexRetrievalSidecarDoc("Crlf", "---\r\nsha256: "+crlf+"\r\n---\r\n", "# Crlf\n\ncarriage return line endings.\n")),
		file(indexRetrievalSidecarPath(crlf), one("crlf")),
		file("sc/later.md", indexRetrievalSidecarDoc("Later", "---\nsource: scan.pdf\nlanguage: en\nsha256:   "+later+"   \nextra: 1\n---\n", "# Later\n\ndigest after other keys.\n")),
		file(indexRetrievalSidecarPath(later), one("later")),
		file("sc/spaced.md", indexRetrievalSidecarDoc("Spaced", "---   \nsha256:\n"+spaced+"\n---   \n", "# Spaced\n\ndigest on the next line, spaced fences.\n")),
		file(indexRetrievalSidecarPath(spaced), one("spaced")),
		file("sc/blank.md", indexRetrievalSidecarDoc("Blank", "---\n\n\nsha256: "+blank+"\n\n---", "\n# Blank\n\nblank lines inside the block.\n")),
		file(indexRetrievalSidecarPath(blank), one("blank")),
	}, run...)

	// Discovery walks up from the document's directory without stopping at the
	// vault root: the nearest directory holding the file wins.
	near := indexRetrievalSidecarSHA("near")
	up := indexRetrievalSidecarSHA("up")
	step("sidecar-discovery", []indexRetrievalProcessFile{
		file("sc/near/near.md", indexRetrievalSidecarDoc("Near", indexRetrievalSidecarBlock(near), "# Near\n\nnearest sidecar wins.\n")),
		file("sc/near/"+indexRetrievalSidecarPath(near), one("nearest")),
		file("sc/far.md", indexRetrievalSidecarDoc("Far", indexRetrievalSidecarBlock(near), "# Far\n\nsame digest, vault-level sidecar.\n")),
		file(indexRetrievalSidecarPath(near), one("vault-level")),
		file("sc/deep/er/up.md", indexRetrievalSidecarDoc("Up", indexRetrievalSidecarBlock(up), "# Up\n\nsidecar above the vault root.\n")),
		file("../"+indexRetrievalSidecarPath(up), one("above-vault")),
	}, run...)

	// A failed re-import after a good one: the rows of the first import are
	// gone because the document was replaced before the import failed.
	redo := indexRetrievalSidecarSHA("redo")
	step("sidecar-good-then-broken-1", []indexRetrievalProcessFile{
		file("sc/redo.md", indexRetrievalSidecarDoc("Redo", indexRetrievalSidecarBlock(redo), "# Redo\n\nfirst body.\n")),
		file(indexRetrievalSidecarPath(redo), one("redo-good")),
	}, run...)
	step("sidecar-good-then-broken-2", []indexRetrievalProcessFile{
		file("sc/redo.md", indexRetrievalSidecarDoc("Redo", indexRetrievalSidecarBlock(redo), "# Redo\n\nsecond body.\n")),
		file(indexRetrievalSidecarPath(redo), one("redo-good")+"{broken\n"),
	}, run...)

	// JSON syntax failures: the whole line is validated first, so a syntax
	// error wins over a type error on the same line, and the first failing
	// line stops the import with no rows written.
	syntax := []struct{ name, content string }{
		{"trailing-comma", `{"field":"a",}` + "\n"},
		{"second-line", `{"field":"a"}` + "\n" + `{"field"}` + "\n"},
		{"eof-in-object", `{"field":`},
		{"eof-open-array", "[\n"},
		{"trailing-text", `{"field":"a"} trailing` + "\n"},
		{"bare-word", "not json\n"},
		{"bom", "\ufeff" + `{"field":"a"}` + "\n"},
		{"bad-escape", `{"field":"a\q"}` + "\n"},
		{"bad-unicode-escape", `{"field":"\u12G4"}` + "\n"},
		{"leading-zero", `{"span":{"start":01}}` + "\n"},
		{"single-quotes", `{'field':1}` + "\n"},
		{"raw-tab", "{\"field\":\"a\tb\"}\n"},
		{"partial-true", `{"matched":tru}` + "\n"},
		{"missing-colon", `{"field" "a"}` + "\n"},
		{"missing-comma", `{"field":"a" "value":"b"}` + "\n"},
		{"bad-number", `{"span":{"start":-}}` + "\n"},
		{"bad-fraction", `{"span":{"start":1.}}` + "\n"},
		{"bad-exponent", `{"span":{"start":1e}}` + "\n"},
		{"syntax-beats-type", `{"field":5,}` + "\n"},
		{"type-line-first", `{"field":5}` + "\n" + `{bad` + "\n"},
		{"array-missing-comma", `[1 2]` + "\n"},
	}
	var syntaxWrites []indexRetrievalProcessFile
	for _, c := range syntax {
		sha := indexRetrievalSidecarSHA("syntax-" + c.name)
		syntaxWrites = append(syntaxWrites,
			file("sc/syntax/"+c.name+".md", indexRetrievalSidecarDoc("Syntax "+c.name, indexRetrievalSidecarBlock(sha), "# "+c.name+"\n\nbroken sidecar syntax.\n")),
			file(indexRetrievalSidecarPath(sha), c.content))
	}
	step("sidecar-json-syntax-failures", syntaxWrites, run...)

	// JSON type failures: Go's decoder saves the first mismatch and returns it
	// after finishing the value.
	typed := []struct{ name, content string }{
		{"field-number", `{"field":5}` + "\n"},
		{"field-bool", `{"field":true}` + "\n"},
		{"value-object", `{"value":{"a":1}}` + "\n"},
		{"value-array", `{"value":[1]}` + "\n"},
		{"start-string", `{"span":{"start":"1"}}` + "\n"},
		{"start-fraction", `{"span":{"start":1.5}}` + "\n"},
		{"start-exponent", `{"span":{"start":1e2}}` + "\n"},
		{"end-overflow", `{"span":{"end":99999999999999999999}}` + "\n"},
		{"snippet-number", `{"span":{"snippet":1}}` + "\n"},
		{"matched-string", `{"matched":"yes"}` + "\n"},
		{"matched-number", `{"matched":1}` + "\n"},
		{"span-number", `{"span":3}` + "\n"},
		{"span-array", `{"span":[1]}` + "\n"},
		{"span-string", `{"span":"1-2"}` + "\n"},
		{"top-array", `[1]` + "\n"},
		{"top-string", `"str"` + "\n"},
		{"top-number", `7` + "\n"},
		{"top-bool", `true` + "\n"},
		{"first-error-wins", `{"field":5,"span":{"start":"x"},"matched":"y"}` + "\n"},
		{"case-folded-key", `{"SPAN":{"START":"x"}}` + "\n"},
		{"after-good-lines", `{"field":"ok","value":"fine"}` + "\n" + `{"field":"ok2","value":2}` + "\n"},
	}
	var typedWrites []indexRetrievalProcessFile
	for _, c := range typed {
		sha := indexRetrievalSidecarSHA("type-" + c.name)
		typedWrites = append(typedWrites,
			file("sc/typed/"+c.name+".md", indexRetrievalSidecarDoc("Type "+c.name, indexRetrievalSidecarBlock(sha), "# "+c.name+"\n\nsidecar type mismatch.\n")),
			file(indexRetrievalSidecarPath(sha), c.content))
	}
	step("sidecar-json-type-failures", typedWrites, run...)

	// bufio.Scanner's 1 MiB token limit: a line one byte under it is read (and
	// fails as JSON), a line at the limit stops the import.
	good := `{"field":"before","value":"before limit invoice"}` + "\n"
	under := indexRetrievalSidecarSHA("line-under-limit")
	over := indexRetrievalSidecarSHA("line-over-limit")
	stepWith("sidecar-line-limit", []indexRetrievalProcessFile{
		file("sc/limit/under.md", indexRetrievalSidecarDoc("Under", indexRetrievalSidecarBlock(under), "# Under\n\nline just under the limit.\n")),
		file("sc/limit/over.md", indexRetrievalSidecarDoc("Over", indexRetrievalSidecarBlock(over), "# Over\n\nline at the limit.\n")),
	}, []indexRetrievalProcessFill{
		{Path: indexRetrievalSidecarPath(under), Prefix: good, Bytes: len(good) + indexRetrievalSidecarMaxLine - 1},
		{Path: indexRetrievalSidecarPath(over), Prefix: good, Bytes: len(good) + indexRetrievalSidecarMaxLine},
	}, nil, run...)

	// The final report: --json output is unaffected by the import diagnostics.
	step("sidecar-json-output", []indexRetrievalProcessFile{
		file("sc/json.md", indexRetrievalSidecarDoc("Json", indexRetrievalSidecarBlock(indexRetrievalSidecarSHA("json")), "# Json\n\njson report.\n")),
		file(indexRetrievalSidecarPath(indexRetrievalSidecarSHA("json")), one("json")),
	}, "--json", "index")
}
