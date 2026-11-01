package main

import (
	"encoding/json"
	"fmt"
)

// Ingest tools: desk_ingest/ingest_file, desk_ingest_jobs/list_jobs,
// desk_ingest_retry/retry_job and desk_ingest_reocr/reocr. Sources are plain
// text, Markdown and HTML so no OCR engine is needed.
func mcpCallsIngestCases() []mcpCallsCase {
	var cases []mcpCallsCase
	jsonArgs := func(v map[string]any) string {
		data, err := json.Marshal(v)
		if err != nil {
			panic(err)
		}
		return string(data)
	}
	sources := map[string]string{
		"cwd/doc.txt":   "Invoice 2026 from Müller\nrechnung total 12 EUR\n",
		"cwd/note.md":   "# Meeting\n\nstrom und wasser\n",
		"cwd/page.html": "<html><body><h1>Title</h1><p>hello html world</p></body></html>\n",
		"cwd/empty.txt": "",
		"cwd/other.txt": "a second distinct text\n",
		"cwd/dir/x.txt": "inside a directory\n",
	}
	withSources := func(c mcpCallsCase) mcpCallsCase {
		c.RootFiles = sources
		return c
	}
	call := func(name, args string) mcpCallsStep { return mcpCallsStep{Request: mcpCallsRequest(name, args)} }
	// Decoder matrix for every ingest tool in both modes.
	decode := []string{``, `null`, `{}`, `[]`, `"s"`, `7`, `{"source_path":5}`, `{"source_path":""}`, `{"id":5}`, `{"id":null}`, `{"document_id":"x"}`, `{"document_id":1.5}`, `{"document_id":1e3}`, `{"document_id":9223372036854775808}`, `{"archive_path":7}`, `{"SOURCE_PATH":"$ROOT/cwd/doc.txt","ID":"1"}`, `{"source_path":"a","source_path":"b"}`, `{"source_path":"/nonexistent/file.txt"}`, `{"id":"x","document_id":1,"archive_path":"a"}`}
	for _, name := range []string{"desk_ingest", "ingest_file", "desk_ingest_jobs", "list_jobs", "desk_ingest_retry", "retry_job", "desk_ingest_reocr", "reocr"} {
		for i, a := range decode {
			cases = append(cases, withSources(mcpCallsCase1(fmt.Sprintf("%s-decode-%02d", name, i), true, name, a)))
		}
		cases = append(cases, withSources(mcpCallsCase1(name+"-read-only", false, name, `{"source_path":"$ROOT/cwd/doc.txt","id":"1","document_id":1}`)))
	}
	// Ingest flow: every source type, repeats, bad sources, then the queue.
	flow := mcpCallsCase{ID: "ingest-flow", AllowWrite: true}
	flow.Prepare = [][]string{{"rules", "add", "invoice", "tag", "billing"}}
	flow.Steps = []mcpCallsStep{
		call("desk_ingest", `{"source_path":"$ROOT/cwd/doc.txt"}`),
		call("desk_ingest", `{"source_path":"$ROOT/cwd/note.md"}`),
		call("ingest_file", `{"source_path":"$ROOT/cwd/page.html"}`),
		call("desk_ingest", `{"source_path":"$ROOT/cwd/doc.txt"}`),
		call("desk_ingest", `{"source_path":"cwd/other.txt"}`),
		call("desk_ingest", `{"source_path":"$ROOT/cwd/missing.txt"}`),
		call("desk_ingest", `{"source_path":"$ROOT/cwd/dir"}`),
		call("desk_ingest", `{"source_path":"$ROOT/cwd/empty.txt"}`),
		call("desk_ingest_jobs", ``),
		call("list_jobs", `{}`),
	}
	flow.RootFiles = sources
	cases = append(cases, flow)
	// Queue operations after a prepared ingest.
	prepared := func(id string, allowWrite bool, steps ...mcpCallsStep) mcpCallsCase {
		c := mcpCallsCase{ID: id, AllowWrite: allowWrite, RootFiles: sources, Steps: steps}
		c.Prepare = [][]string{{"ingest", "$ROOT/cwd/doc.txt"}, {"ingest", "$ROOT/cwd/note.md"}}
		return c
	}
	cases = append(cases, prepared("jobs-after-prepare", false, call("desk_ingest_jobs", ``), call("list_jobs", ``)))
	cases = append(cases, prepared("jobs-after-prepare-write", true, call("desk_ingest_jobs", ``)))
	retryIDs := []string{"1", " 2 ", "abc", "999", "-3", "", "0", "1 2", "9223372036854775808", "+1", "0x1", "１"}
	for i, id := range retryIDs {
		cases = append(cases, prepared(fmt.Sprintf("retry-id-%02d", i), true, call("desk_ingest_retry", jsonArgs(map[string]any{"id": id})), call("list_jobs", ``)))
	}
	cases = append(cases, prepared("retry-alias", true, call("retry_job", `{"id":"1"}`), call("list_jobs", ``)))
	cases = append(cases, prepared("retry-no-id", true, call("desk_ingest_retry", `{}`)))
	// Re-OCR by document id / archive path.
	reocrs := []string{`{"document_id":1}`, `{"document_id":2}`, `{"document_id":999}`, `{"document_id":-1}`, `{"document_id":0}`, `{}`, `{"archive_path":"archive/ingest/nope.txt"}`, `{"archive_path":"archive/ingest/nope.txt","document_id":1}`, `{"source":"archive/ingest/nope.txt"}`, `{"source":"x","archive_path":"y"}`, `{"archive_path":"/abs/nope.txt"}`, `{"document_id":1,"archive_path":""}`, `{"archive_path":"../escape.txt"}`}
	for i, a := range reocrs {
		cases = append(cases, prepared(fmt.Sprintf("reocr-%02d", i), true, call("desk_ingest_reocr", a), call("list_jobs", ``)))
	}
	cases = append(cases, prepared("reocr-alias", true, call("reocr", `{"document_id":1}`), call("reocr", `{"document_id":1}`), call("list_jobs", ``)))
	// No vault configured.
	for _, name := range []string{"desk_ingest", "desk_ingest_jobs", "desk_ingest_retry", "desk_ingest_reocr"} {
		c := withSources(mcpCallsCase1("no-vault-"+name, true, name, `{"source_path":"$ROOT/cwd/doc.txt","id":"1","document_id":1}`))
		c.NoVault = true
		cases = append(cases, c)
	}
	// Archive path override and framed transport.
	override := prepared("reocr-archive-env", true, call("desk_ingest_reocr", `{"document_id":1}`))
	override.Env = map[string]string{"SYMINGEST_ARCHIVE_PATH": "$ROOT/home/shared-archive"}
	cases = append(cases, override)
	framed := withSources(mcpCallsCase1("ingest-framed", true, "desk_ingest", `{"source_path":"$ROOT/cwd/doc.txt"}`))
	framed.Steps[0].Framed = true
	cases = append(cases, framed)
	return cases
}
