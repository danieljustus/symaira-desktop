package main

import (
	"fmt"
)

// Service tools: vault_timeline, desk_transform, vault_health, desk_related,
// desk_dataset_{list,describe,query,sync}, desk_clip, desk_export and
// desk_autofill. Every case runs without network, sibling tools or AI
// providers; the backend-failure cases point the providers at closed local
// ports or an empty PATH.
func mcpCallsServiceCases() []mcpCallsCase {
	var cases []mcpCallsCase
	call := func(name, args string) mcpCallsStep { return mcpCallsStep{Request: mcpCallsRequest(name, args)} }
	note := func(title, extra, body string) string {
		return "---\ntitle: " + title + "\ncreated: 2026-03-01T09:00:00Z\n" + extra + "---\n" + body
	}
	vaultFiles := map[string]string{
		"alpha.md":       note("Alpha", "type: invoice\ndocument_date: 2026-03-05\nperson: Max\nstatus: open\ntags: [billing]\n", "Alpha links to [[beta]] and [[missing-target]].\n"),
		"beta.md":        note("Beta", "type: letter\ndocument_date: 2026-03-07\n", "Beta body shared words for near duplicate detection in the vault health scan.\n"),
		"gamma.md":       note("Gamma", "type: letter\ndocument_date: 2026-03-07\n", "Beta body shared words for near duplicate detection in the vault health scan.\n"),
		"plain.md":       "No frontmatter here, only [[alpha]].\n",
		"meet/weekly.md": "---\ntype: meeting\ntitle: Weekly\ncreated: \"2026-03-05T10:00:00Z\"\ntags: [meeting]\nmeeting_id: mt-1\nstarted_at: \"2026-03-05T10:00:00Z\"\nduration_ms: 1800000\nsymmeet_source:\n  artifact_schema_version: 1\n  review_state: needs_review\n---\n# Weekly\n\nBody\n",
	}
	journal := `{"ts":"2026-03-05T08:00:00Z","event":"file_added","path":"alpha.md","title":"Alpha","details":"created"}
{"ts":"2026-03-05T08:00:00Z","event":"file_added","path":"alpha.md","title":"Alpha","details":"created"}
{"ts":"2026-03-05T09:30:00.250Z","event":"file_changed","path":"beta.md"}
not json
{"ts":"2026-03-05","event":"date_only","path":"gamma.md","title":"Gamma"}
{"ts":1772700000,"event":"unix","path":"plain.md"}
{"ts":"junk","event":"junk_ts","path":"plain.md"}

{"ts":"2026-03-06T12:00:00Z","event":"file_removed","path":"gone.md","title":"Gone","details":"removed"}
`
	rootFiles := map[string]string{
		"vault/.symdesk/journal/2026-03-05.ndjson":     journal,
		"vault/.symdesk/journal/2026-03-06.ndjson":     `{"ts":"2026-03-06T12:00:00Z","event":"file_removed","path":"gone.md","title":"Gone","details":"removed"}` + "\n",
		"vault/.symdesk/retention/history.json":        `[{"timestamp":"2026-03-05T11:00:00Z","rule_name":"old","action":"archive","path":"old.md","title":"Old"},{"timestamp":"2026-04-01T00:00:00Z","rule_name":"r2","action":"delete","path":"x.md","title":"X"}]`,
		"vault/.symdesk/trash/old.md.1.trashinfo.json": `{"name":"old.md.1","original_path":"old.md","deleted_at":"2026-03-05T12:00:00Z","size":5}`,
		"vault/.symdesk/trash/old.md.1":                "trash",
		"vault/.symdesk/trash/corrupt.trashinfo.json":  `{`,
	}
	withVault := func(c mcpCallsCase) mcpCallsCase {
		c.Files = vaultFiles
		c.Indexed = true
		c.RootFiles = rootFiles
		return c
	}

	// ---- vault_timeline ----
	args := []string{``, `null`, `{}`, `[]`, `"s"`, `{"from":5}`, `{"from":""}`, `{"from":"  "}`,
		`{"from":"2026-03-05"}`, `{"from":"2026-03-05","to":"2026-03-05"}`, `{"from":"2026-03-05","to":"2026-03-06"}`,
		`{"from":"2026-03-01","to":"2026-03-31"}`, `{"from":"2026-03-31","to":"2026-03-01"}`,
		`{"from":"2026-03-05T08:00:00Z"}`, `{"from":"2026-03-05T08:00:00Z","to":"2026-03-05T09:00:00Z"}`,
		`{"from":"2026-03-05T08:00:00.5+01:00","to":"2026-03-06"}`, `{"from":"2026-03-05 08:00:00","to":"2026-03-05 23:00:00"}`,
		`{"from":"yesterday"}`, `{"from":"2026-02-30"}`, `{"from":"2026-03-05","to":"nope"}`, `{"from":"2026-03-05","to":""}`,
		`{"from":"2026-03-01","to":"2026-03-31","limit":2}`, `{"from":"2026-03-01","to":"2026-03-31","limit":1}`, `{"from":"2026-03-01","to":"2026-03-31","limit":0}`,
		`{"from":"2026-03-01","to":"2026-03-31","limit":-1}`, `{"from":"2026-03-01","limit":"3"}`, `{"from":"2026-03-01","limit":1.5}`, `{"from":"2026-03-01","limit":9223372036854775808}`,
		`{"from":"2026-03-05","to":5}`, `{"FROM":"2026-03-05","TO":"2026-03-05"}`, `{"from":"2026-03-05","from":"2026-03-06"}`,
		`{"from":"2026-03-01","to":"2026-03-31","extra":true}`, `{"from":"2026-01-01","to":"2026-01-02"}`, `{"from":"0001-01-01","to":"0001-01-02"}`,
		`{"from":"9999-12-30","to":"9999-12-31"}`, `{"from":"2026-03-05\u0000"}`, `{"from":"２０２６-03-05"}`}
	for i, a := range args {
		cases = append(cases, withVault(mcpCallsCase1(fmt.Sprintf("timeline-%02d", i), false, "vault_timeline", a)))
	}
	cases = append(cases, withVault(mcpCallsCase1("timeline-write-mode", true, "vault_timeline", `{"from":"2026-03-05"}`)))
	empty := mcpCallsCase1("timeline-empty-vault", false, "vault_timeline", `{"from":"2026-03-05","to":"2026-03-06"}`)
	cases = append(cases, empty)
	nv := mcpCallsCase1("timeline-no-vault", false, "vault_timeline", `{"from":"2026-03-05"}`)
	nv.NoVault = true
	cases = append(cases, nv)
	badRetention := withVault(mcpCallsCase1("timeline-bad-retention", false, "vault_timeline", `{"from":"2026-03-05"}`))
	badRetention.RootFiles = map[string]string{"vault/.symdesk/retention/history.json": `null`, "vault/.symdesk/journal/2026-03-05.ndjson": journal}
	cases = append(cases, badRetention)

	// ---- desk_transform ----
	tArgs := []string{``, `null`, `{}`, `[]`, `"s"`, `{"text":5}`, `{"text":""}`, `{"text":"hello"}`, `{"text":"hello","intent":"summarize"}`,
		`{"text":"hello","intent":"rewrite"}`, `{"text":"hello","intent":"continue"}`, `{"text":"hello","intent":"other"}`, `{"text":"   ","intent":"summarize"}`,
		`{"text":"hello","intent":5}`, `{"TEXT":"x","INTENT":"summarize"}`, `{"text":"a","text":"b","intent":"rewrite"}`, `{"intent":"summarize"}`}
	for i, a := range tArgs {
		cases = append(cases, mcpCallsCase1(fmt.Sprintf("transform-%02d", i), false, "desk_transform", a))
		cases[len(cases)-1].NoVault = true
	}
	for _, provider := range []string{"hermes", "anthropic", "ollama"} {
		c := mcpCallsCase1("transform-provider-"+provider, false, "desk_transform", `{"text":"hello world","intent":"summarize"}`)
		c.NoVault = true
		c.Env = map[string]string{"SYMDESK_LLM_PROVIDER": provider}
		cases = append(cases, c)
	}
	closed := mcpCallsCase1("transform-ollama-closed-port", false, "desk_transform", `{"text":"hello world","intent":"rewrite"}`)
	closed.NoVault = true
	closed.Env = map[string]string{"SYMDESK_OLLAMA_URL": "http://127.0.0.1:1"}
	cases = append(cases, closed)
	closedV1 := mcpCallsCase1("transform-ollama-closed-port-v1", false, "desk_transform", `{"text":"hello world","intent":"continue"}`)
	closedV1.NoVault = true
	closedV1.Env = map[string]string{"SYMDESK_OLLAMA_URL": "http://127.0.0.1:1/v1/", "SYMDESK_LANGUAGE": "de"}
	cases = append(cases, closedV1)
	badURL := mcpCallsCase1("transform-ollama-bad-url", false, "desk_transform", `{"text":"hello world","intent":"rewrite"}`)
	badURL.NoVault = true
	badURL.Env = map[string]string{"SYMDESK_OLLAMA_URL": "not a url"}
	cases = append(cases, badURL)
	secret := mcpCallsCase1("transform-anthropic-secret-canary", false, "desk_transform", `{"text":"hello","intent":"summarize"}`)
	secret.NoVault = true
	secret.Env = map[string]string{"SYMDESK_LLM_PROVIDER": "anthropic", "SYMDESK_LLM_API_KEY": "sk-canary-0123456789abcdef"}
	cases = append(cases, secret)

	// ---- vault_health ----
	hArgs := []string{``, `null`, `{}`, `[]`, `"s"`, `{"duplicate_threshold":5}`, `{"duplicate_threshold":0}`, `{"duplicate_threshold":-1}`, `{"duplicate_threshold":64}`, `{"duplicate_threshold":100}`,
		`{"duplicate_threshold":"x"}`, `{"duplicate_threshold":1.5}`, `{"duplicate_threshold":9223372036854775808}`, `{"DUPLICATE_THRESHOLD":10}`, `{"extra":1}`}
	for i, a := range hArgs {
		cases = append(cases, withVault(mcpCallsCase1(fmt.Sprintf("health-%02d", i), false, "vault_health", a)))
	}
	cases = append(cases, withVault(mcpCallsCase1("health-write-mode", true, "vault_health", `{}`)))
	hv := mcpCallsCase1("health-empty-vault", false, "vault_health", `{}`)
	cases = append(cases, hv)
	hnv := mcpCallsCase1("health-no-vault", false, "vault_health", `{}`)
	hnv.NoVault = true
	cases = append(cases, hnv)
	broken := withVault(mcpCallsCase1("health-broken-yaml", false, "vault_health", `{}`))
	bf := map[string]string{}
	for k, v := range vaultFiles {
		bf[k] = v
	}
	bf["dup1.md"] = "---\ntitle: D\n---\nthe quick brown fox jumps over the lazy dog again and again\n"
	bf["dup2.md"] = "---\ntitle: D2\n---\nthe quick brown fox jumps over the lazy dog again and again\n"
	broken.Files = bf
	// broken.md cannot be parsed, so the seed indexer must not see it: it is
	// written below the root instead, where only the health scan reads it.
	// Only parse errors whose wording both YAML stacks share are recorded: the
	// scanner wording of an unclosed flow sequence differs (declared limit,
	// RUST-013), so the broken notes below are not YAML syntax errors.
	br := map[string]string{
		"vault/broken-seq.md":   "---\n- a\n- b\n---\nbody\n",
		"vault/broken-open.md":  "---\ntitle: x\nbody without a closing delimiter\n",
		"vault/broken-dup.md":   "---\ntitle: a\ntitle: b\n---\nbody\n",
		"vault/broken-scalar.md": "---\njust text\n---\nbody\n",
	}
	for k, v := range rootFiles {
		br[k] = v
	}
	broken.RootFiles = br
	cases = append(cases, broken)

	// ---- desk_related ----
	rArgs := []string{``, `null`, `{}`, `[]`, `"s"`, `{"file":5}`, `{"file":""}`, `{"file":"alpha.md"}`, `{"file":"missing.md"}`, `{"file":"../escape.md"}`, `{"file":"/etc/hosts"}`,
		`{"file":"meet/weekly.md"}`, `{"file":"."}`, `{"file":"meet"}`, `{"FILE":"alpha.md"}`, `{"file":"alpha.md","file":"beta.md"}`, `{"file":"plain.md","extra":1}`}
	for i, a := range rArgs {
		cases = append(cases, withVault(mcpCallsCase1(fmt.Sprintf("related-%02d", i), false, "desk_related", a)))
	}
	rnv := mcpCallsCase1("related-no-vault", false, "desk_related", `{"file":"alpha.md"}`)
	rnv.NoVault = true
	cases = append(cases, rnv)

	// ---- datasets ----
	ordersRows := `[{"identity":"o1","values":{"order_id":"o1","customer":"Acme","amount":"10","paid":"yes"}},{"identity":"o2","values":{"order_id":"o2","customer":"Bolt","amount":"25.5","paid":"no"}},{"identity":"o3","values":{"order_id":"o3","customer":"Acme","amount":"4","paid":"yes"}}]`
	ordersSchema := `{"order_id":{"type":"text"},"customer":{"type":"text"},"amount":{"type":"number"},"paid":{"type":"text"}}`
	prov := `{"imported_at":"2026-03-05T10:00:00Z","source_name":"erp","source_sha256":"0123abcd"}`
	syncOrders := []string{"dataset", "sync", "orders", "--rows", ordersRows, "--provenance", prov, "--identity-field", "order_id", "--title", "Orders", "--schema", ordersSchema, "--sensitivity", "internal"}
	syncPeople := []string{"dataset", "sync", "people", "--rows", `[{"identity":"p1","values":{"id":"p1","name":"Zed"}}]`, "--provenance", prov, "--identity-field", "id", "--title", "alpha people", "--schema", `{"id":{"type":"text"},"name":{"type":"text"}}`}
	withDatasets := func(c mcpCallsCase) mcpCallsCase {
		c.Prepare = [][]string{syncOrders, syncPeople}
		return c
	}
	dd := []string{``, `null`, `{}`, `[]`, `"s"`, `{"extra":1}`}
	for i, a := range dd {
		cases = append(cases, withDatasets(mcpCallsCase1(fmt.Sprintf("dataset-list-%02d", i), false, "desk_dataset_list", a)))
	}
	cases = append(cases, mcpCallsCase1("dataset-list-empty", false, "desk_dataset_list", `{}`))
	lnv := mcpCallsCase1("dataset-list-no-vault", false, "desk_dataset_list", `{}`)
	lnv.NoVault = true
	cases = append(cases, lnv)
	diag := mcpCallsCase1("dataset-list-diagnostics", false, "desk_dataset_list", `{}`)
	diag.Files = map[string]string{"datasets/bad.md": "no frontmatter\n", "datasets/worse.md": "---\ntype: dataset\n---\n"}
	cases = append(cases, diag)
	desc := []string{``, `null`, `{}`, `[]`, `{"dataset":5}`, `{"dataset":""}`, `{"dataset":"  "}`, `{"dataset":"orders"}`, `{"dataset":" orders "}`, `{"dataset":"people"}`, `{"dataset":"missing"}`,
		`{"dataset":"../x"}`, `{"dataset":"orders","extra":1}`, `{"dataset":"orders"}{"dataset":"people"}`, `{"Dataset":"orders"}`, `{"dataset":"orders","dataset":"people"}`}
	for i, a := range desc {
		cases = append(cases, withDatasets(mcpCallsCase1(fmt.Sprintf("dataset-describe-%02d", i), false, "desk_dataset_describe", a)))
	}
	query := []string{
		`{"dataset":"orders"}`, `{"dataset":"orders","columns":["customer","amount"]}`, `{"dataset":"orders","columns":["nope"]}`, `{"dataset":"orders","columns":"x"}`,
		`{"dataset":"orders","filters":[{"key":"customer","value":"Acme"}]}`, `{"dataset":"orders","filters":[{"key":"amount","operator":"gt","value":"5"}]}`,
		`{"dataset":"orders","filters":[{"key":"amount","operator":"bogus","value":"5"}]}`, `{"dataset":"orders","filters":[{"key":"customer"}]}`,
		`{"dataset":"orders","filters":[{"key":"customer","value":"Acme","extra":1}]}`,
		`{"dataset":"orders","filter_group":{"operator":"or","filters":[{"key":"customer","value":"Bolt"},{"key":"paid","value":"yes"}]}}`,
		`{"dataset":"orders","group_by":"customer","aggregates":[{"column":"amount","function":"sum","as":"total"},{"function":"count"}]}`,
		`{"dataset":"orders","aggregates":[{"column":"amount","function":"average"},{"column":"amount","function":"min"},{"column":"amount","function":"max"}]}`,
		`{"dataset":"orders","aggregates":[{"column":"amount","function":"median"}]}`, `{"dataset":"orders","aggregates":[{"column":"nope","function":"sum"}]}`,
		`{"dataset":"orders","group_by":"nope","aggregates":[{"function":"count"}]}`, `{"dataset":"orders","limit":1}`, `{"dataset":"orders","limit":0}`, `{"dataset":"orders","limit":-1}`, `{"dataset":"orders","limit":100000}`,
		`{"dataset":"orders","limit":"1"}`, `{"dataset":"orders","limit":1.5}`, `{"dataset":"missing"}`, `{"dataset":""}`, `{}`, `null`, ``, `[]`, `{"dataset":"orders","unknown":1}`,
		`{"dataset":"orders","columns":["customer"],"group_by":"customer"}`, `{"dataset":"orders","aggregates":[]}`, `{"dataset":"orders","filters":null,"columns":null}`,
	}
	for i, a := range query {
		cases = append(cases, withDatasets(mcpCallsCase1(fmt.Sprintf("dataset-query-%02d", i), false, "desk_dataset_query", a)))
	}
	cases = append(cases, withDatasets(mcpCallsCase1("dataset-query-write-mode", true, "desk_dataset_query", `{"dataset":"orders","limit":2}`)))
	// Sync (write tool).
	syncBody := func(slug, rows, extra string) string {
		return `{"dataset":"` + slug + `","identity_field":"id","provenance":` + prov + `,"rows":` + rows + extra + `}`
	}
	syncs := []string{
		syncBody("fresh", `[{"identity":"a","values":{"id":"a","v":"1"}},{"identity":"b","values":{"id":"b","v":"2"}}]`, `,"title":"Fresh Data","schema":{"id":{"type":"text"},"v":{"type":"number"}},"sensitivity":"public","retention_rule":"default"`),
		syncBody("fresh", `[{"identity":"a","values":{"id":"a","v":"1"}}]`, ``),
		syncBody("fresh2", `[]`, ``), syncBody("fresh3", `[{"identity":"","values":{}}]`, ``),
		syncBody("fresh4", `[{"identity":"a","values":{}},{"identity":"a","values":{}}]`, ``),
		syncBody("Bad Slug", `[{"identity":"a","values":{"id":"a"}}]`, ``), syncBody("", `[{"identity":"a","values":{"id":"a"}}]`, `,"title":"Titled"`),
		syncBody("fresh5", `[{"identity":"a","values":{"id":"a"}}]`, `,"sensitivity":"bogus"`), syncBody("fresh6", `[{"identity":"a","values":{"id":"a"}}]`, `,"retention_rule":"nope"`),
		`{"dataset":"nop","identity_field":"id","provenance":{"imported_at":"nope","source_name":"s","source_sha256":"x"},"rows":[{"identity":"a","values":{"id":"a"}}]}`,
		`{"dataset":"nop","identity_field":"id","provenance":{"imported_at":"2026-03-05T10:00:00Z","source_name":"","source_sha256":"x"},"rows":[{"identity":"a","values":{"id":"a"}}]}`,
		`{"dataset":"nop","provenance":` + prov + `,"rows":[{"identity":"a","values":{"id":"a"}}]}`,
		`{"dataset":"nop","identity_field":"id","provenance":` + prov + `,"rows":[{"identity":"a","values":{"id":"a"},"extra":1}]}`,
		`{"dataset":"nop","identity_field":"id","provenance":{"imported_at":"2026-03-05T10:00:00Z","source_name":"s","source_sha256":"x","extra":1},"rows":[]}`,
		`{"dataset":"nop","identity_field":"id","provenance":` + prov + `,"rows":"x"}`, `{"dataset":5}`, `{"dataset":""}`, `{}`, `null`, ``, `[]`, `{"dataset":"x","unknown":1}`,
		`{"dataset":"nop","identity_field":"id","provenance":` + prov + `,"rows":[{"identity":"a","values":{"id":"a","nested":{"x":1},"list":[1,2],"n":null,"b":true,"f":1.5}}]}`,
	}
	for i, a := range syncs {
		cases = append(cases, mcpCallsCase1(fmt.Sprintf("dataset-sync-%02d", i), true, "desk_dataset_sync", a))
	}
	cases = append(cases, mcpCallsCase1("dataset-sync-read-only", false, "desk_dataset_sync", syncs[0]))
	idem := withDatasets(mcpCallsCase{ID: "dataset-sync-idempotent", AllowWrite: true, Steps: []mcpCallsStep{
		call("desk_dataset_sync", syncBody("orders", ordersRows, `,"title":"Orders","schema":`+ordersSchema)),
		call("desk_dataset_sync", syncBody("orders", `[{"identity":"o9","values":{"id":"o9"}}]`, ``)),
		call("desk_dataset_list", ``), call("desk_dataset_describe", `{"dataset":"orders"}`),
	}})
	cases = append(cases, idem)
	snv := mcpCallsCase1("dataset-sync-no-vault", true, "desk_dataset_sync", syncs[0])
	snv.NoVault = true
	cases = append(cases, snv)

	// ---- desk_clip (symbrowse is never available; PATH is empty) ----
	cArgs := []string{``, `null`, `{}`, `[]`, `"s"`, `{"url":5}`, `{"url":""}`, `{"url":"https://example.invalid/page"}`, `{"URL":"https://example.invalid/page"}`, `{"url":"https://a","url":"https://b"}`, `{"url":"x","extra":1}`}
	for i, a := range cArgs {
		cases = append(cases, mcpCallsCase1(fmt.Sprintf("clip-%02d", i), true, "desk_clip", a))
	}
	cases = append(cases, mcpCallsCase1("clip-read-only", false, "desk_clip", `{"url":"https://example.invalid/page"}`))
	cnv := mcpCallsCase1("clip-no-vault", true, "desk_clip", `{"url":"https://example.invalid/page"}`)
	cnv.NoVault = true
	cases = append(cases, cnv)

	// ---- desk_export ----
	eArgs := []string{``, `null`, `{}`, `[]`, `"s"`, `{"note":5}`, `{"note":"alpha.md"}`, `{"note":"alpha.md","format":"html"}`, `{"note":"alpha.md","format":"html","output":"$ROOT/out/a.html"}`,
		`{"note":"alpha.md","format":"csv"}`, `{"note":"alpha.md","format":"pdf","output":"$ROOT/out/a.pdf"}`, `{"note":"alpha.md","format":"docx"}`, `{"note":"missing.md","format":"html"}`,
		`{"note":"../x.md","format":"html"}`, `{"view":"nope","format":"html"}`, `{"view":"nope"}`, `{"note":"alpha.md","view":"v","format":"html"}`, `{"output":"$ROOT/x.html"}`,
		`{"note":"alpha.md","format":"html","output":"relative/out.html"}`, `{"note":"alpha.md","format":"html","output":"/nonexistent-dir/x/out.html"}`, `{"note":"alpha.md","format":"html","output":""}`,
		`{"note":"alpha.md","format":"pdf","profile":"nope","output":"$ROOT/out/p.pdf"}`, `{"NOTE":"alpha.md","FORMAT":"html","OUTPUT":"$ROOT/out/b.html"}`, `{"note":"datasets/orders.md","format":"html","output":"$ROOT/out/d.html"}`,
		`{"note":"alpha.md","format":"html","extra":1}`, `{"note":"alpha.md","format":"HTML","output":"$ROOT/out/c.html"}`}
	for i, a := range eArgs {
		c := withVault(mcpCallsCase1(fmt.Sprintf("export-%02d", i), true, "desk_export", a))
		c.Prepare = [][]string{syncOrders}
		cases = append(cases, c)
	}
	cases = append(cases, withVault(mcpCallsCase1("export-read-only", false, "desk_export", `{"note":"alpha.md","format":"html"}`)))
	enNv := mcpCallsCase1("export-no-vault", true, "desk_export", `{"note":"alpha.md","format":"html"}`)
	enNv.NoVault = true
	cases = append(cases, enNv)

	// ---- desk_autofill ----
	viewBase := "---\ntype: base\ntitle: Letters\nviews:\n  - id: letters\n    name: Letters\n    filters:\n      - key: type\n        value: letter\n---\n"
	aArgs := []string{``, `null`, `{}`, `[]`, `"s"`, `{"view":5}`, `{"view":"letters"}`, `{"property":"summary"}`, `{"view":"letters","property":"summary"}`, `{"view":"letters","property":"summary","dry_run":true}`,
		`{"view":"letters","property":"summary","dry_run":"yes"}`, `{"view":"nope","property":"summary"}`, `{"view":"letters","property":"summary","prompt":"short"}`, `{"VIEW":"letters","PROPERTY":"summary","DRY_RUN":true}`,
		`{"view":"letters","property":"title","dry_run":true}`, `{"view":"letters","property":"","dry_run":true}`, `{"view":"","property":"x"}`, `{"view":"letters","property":"summary","extra":1}`}
	for i, a := range aArgs {
		c := withVault(mcpCallsCase1(fmt.Sprintf("autofill-%02d", i), true, "desk_autofill", a))
		f := map[string]string{"bases/letters.md": viewBase}
		for k, v := range vaultFiles {
			f[k] = v
		}
		c.Files = f
		cases = append(cases, c)
	}
	cases = append(cases, withVault(mcpCallsCase1("autofill-read-only", false, "desk_autofill", `{"view":"letters","property":"summary"}`)))
	closedAF := withVault(mcpCallsCase1("autofill-closed-port", true, "desk_autofill", `{"view":"letters","property":"summary"}`))
	closedAF.Env = map[string]string{"SYMDESK_OLLAMA_URL": "http://127.0.0.1:1"}
	cases = append(cases, closedAF)
	anv := mcpCallsCase1("autofill-no-vault", true, "desk_autofill", `{"view":"letters","property":"summary"}`)
	anv.NoVault = true
	cases = append(cases, anv)
	return cases
}
